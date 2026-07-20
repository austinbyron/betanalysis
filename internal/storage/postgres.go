package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/austinbyron/betanalysis/internal/config"
	"github.com/austinbyron/betanalysis/pkg/types"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/rs/zerolog/log"
)

// PostgresDB wraps sqlx.DB with repository methods
type PostgresDB struct {
	db *sqlx.DB
}

// NewPostgres creates a new PostgreSQL connection
func NewPostgres(cfg config.DatabaseConfig) (*PostgresDB, error) {
	dsn := fmt.Sprintf("host=%s port=%d dbname=%s user=%s password=%s sslmode=%s",
		cfg.Host, cfg.Port, cfg.Name, cfg.User, cfg.Password, cfg.SSLMode)

	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	log.Info().Str("host", cfg.Host).Int("port", cfg.Port).Str("db", cfg.Name).Msg("Connected to PostgreSQL")

	return &PostgresDB{db: db}, nil
}

// Close closes the database connection
func (p *PostgresDB) Close() error {
	return p.db.Close()
}

// DB returns the underlying sqlx.DB
func (p *PostgresDB) DB() *sqlx.DB {
	return p.db
}

// SaveGames saves or updates games in batch
func (p *PostgresDB) SaveGames(games []types.Game) error {
	if len(games) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Never regress a finished game back to scheduled: score/status updates
	// come from UpdateGameScores, not from the collector path.
	query := `
		INSERT INTO games (id, sport_key, commence_time, home_team, away_team, status, home_score, away_score, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW(), NOW())
		ON CONFLICT (id) DO UPDATE SET
			commence_time = EXCLUDED.commence_time,
			updated_at = NOW()
	`

	for _, game := range games {
		_, err := p.db.ExecContext(ctx, query,
			game.ID,
			game.SportKey,
			game.CommenceTime,
			game.HomeTeam,
			game.AwayTeam,
			game.Status,
			game.HomeScore,
			game.AwayScore,
		)
		if err != nil {
			return fmt.Errorf("failed to save game %s: %w", game.ID, err)
		}
	}

	return nil
}

// GetUpcomingGames returns scheduled games for a sport. commence_time holds
// a UTC wall clock, so the comparison must use UTC now — bare NOW() is the
// session's local time, which kept games "upcoming" for hours after first
// pitch and let the engine bet on games already in play.
func (p *PostgresDB) GetUpcomingGames(sportKey string) ([]types.Game, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var games []types.Game
	query := `
		SELECT id, sport_key, commence_time, home_team, away_team, status, home_score, away_score, created_at, updated_at
		FROM games
		WHERE sport_key = $1 AND status = 'scheduled' AND commence_time > timezone('UTC', NOW())
		ORDER BY commence_time ASC
	`

	if err := p.db.SelectContext(ctx, &games, query, sportKey); err != nil {
		return nil, fmt.Errorf("failed to get upcoming games: %w", err)
	}

	return games, nil
}

// GetFinishedGames returns finished games with scores for a sport within a
// date range, in chronological order. Used for backtesting.
func (p *PostgresDB) GetFinishedGames(sportKey string, start, end time.Time) ([]types.Game, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var games []types.Game
	query := `
		SELECT id, sport_key, commence_time, home_team, away_team, status, home_score, away_score, created_at, updated_at
		FROM games
		WHERE sport_key = $1 AND status = 'finished'
			AND home_score IS NOT NULL AND away_score IS NOT NULL
			AND commence_time >= $2 AND commence_time <= $3
		ORDER BY commence_time ASC
	`

	if err := p.db.SelectContext(ctx, &games, query, sportKey, start, end); err != nil {
		return nil, fmt.Errorf("failed to get finished games: %w", err)
	}

	return games, nil
}

// GetStaleScheduledGames returns games still 'scheduled' past the given
// cutoff that a pending bet or preview bet is waiting on — games the odds
// feed abandoned (event id churn on reschedules, postponements).
func (p *PostgresDB) GetStaleScheduledGames(before time.Time) ([]types.Game, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var games []types.Game
	query := `
		SELECT id, sport_key, commence_time, home_team, away_team, status, home_score, away_score, created_at, updated_at
		FROM games g
		WHERE g.status = 'scheduled' AND g.commence_time < $1
			AND (EXISTS (SELECT 1 FROM bets b WHERE b.game_id = g.id AND b.status = 'pending')
				OR EXISTS (SELECT 1 FROM preview_bets pb WHERE pb.game_id = g.id AND pb.status = 'pending'))
		ORDER BY commence_time ASC
	`

	if err := p.db.SelectContext(ctx, &games, query, before); err != nil {
		return nil, fmt.Errorf("failed to get stale scheduled games: %w", err)
	}

	return games, nil
}

// GetGameByID returns a game by its ID
func (p *PostgresDB) GetGameByID(id string) (*types.Game, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var game types.Game
	query := `
		SELECT id, sport_key, commence_time, home_team, away_team, status, home_score, away_score, created_at, updated_at
		FROM games WHERE id = $1
	`

	if err := p.db.GetContext(ctx, &game, query, id); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get game: %w", err)
	}

	return &game, nil
}

// SaveOdds appends odds snapshots. Odds are stored append-only so line
// movement history is preserved; use GetOddsForGame for the latest lines.
func (p *PostgresDB) SaveOdds(odds []types.GameOdds) error {
	if len(odds) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	query := `
		INSERT INTO game_odds (game_id, bookmaker, market_type, home_odds, away_odds, draw_odds,
			home_spread, away_spread, over_under, over_odds, under_odds, last_update, retrieved_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`

	for _, o := range odds {
		_, err := p.db.ExecContext(ctx, query,
			o.GameID,
			o.Bookmaker,
			o.MarketType,
			o.HomeOdds,
			o.AwayOdds,
			o.DrawOdds,
			o.HomeSpread,
			o.AwaySpread,
			o.OverUnder,
			o.OverOdds,
			o.UnderOdds,
			o.LastUpdate,
			o.RetrievedAt,
		)
		if err != nil {
			return fmt.Errorf("failed to save odds: %w", err)
		}
	}

	return nil
}

// GetOddsForGame returns the latest odds snapshot per bookmaker and market
func (p *PostgresDB) GetOddsForGame(gameID string) ([]types.GameOdds, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var odds []types.GameOdds
	query := `
		SELECT DISTINCT ON (bookmaker, market_type)
			id, game_id, bookmaker, market_type, home_odds, away_odds, draw_odds,
			home_spread, away_spread, over_under, over_odds, under_odds, last_update, retrieved_at
		FROM game_odds
		WHERE game_id = $1
		ORDER BY bookmaker, market_type, retrieved_at DESC
	`

	if err := p.db.SelectContext(ctx, &odds, query, gameID); err != nil {
		return nil, fmt.Errorf("failed to get odds: %w", err)
	}

	return odds, nil
}

// GetOddsForGameAt returns the latest odds per bookmaker and market as of a
// point in time. Used by the backtester to avoid lookahead.
func (p *PostgresDB) GetOddsForGameAt(gameID string, asOf time.Time) ([]types.GameOdds, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var odds []types.GameOdds
	query := `
		SELECT DISTINCT ON (bookmaker, market_type)
			id, game_id, bookmaker, market_type, home_odds, away_odds, draw_odds,
			home_spread, away_spread, over_under, over_odds, under_odds, last_update, retrieved_at
		FROM game_odds
		WHERE game_id = $1 AND retrieved_at <= $2
		ORDER BY bookmaker, market_type, retrieved_at DESC
	`

	if err := p.db.SelectContext(ctx, &odds, query, gameID, asOf); err != nil {
		return nil, fmt.Errorf("failed to get odds: %w", err)
	}

	return odds, nil
}

// GetBestOdds returns the best available odds for a selection
func (p *PostgresDB) GetBestOdds(gameID, marketType, selection string) (*types.GameOdds, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var odds types.GameOdds
	var column string

	switch selection {
	case "home":
		column = "home_odds"
	case "away":
		column = "away_odds"
	case "draw":
		column = "draw_odds"
	default:
		return nil, fmt.Errorf("invalid selection: %s", selection)
	}

	// Latest snapshot per bookmaker first, then the best price among them
	query := fmt.Sprintf(`
		SELECT id, game_id, bookmaker, market_type, home_odds, away_odds, draw_odds,
			home_spread, away_spread, over_under, over_odds, under_odds, last_update, retrieved_at
		FROM (
			SELECT DISTINCT ON (bookmaker) *
			FROM game_odds
			WHERE game_id = $1 AND market_type = $2 AND %s IS NOT NULL
			ORDER BY bookmaker, retrieved_at DESC
		) latest
		ORDER BY %s DESC
		LIMIT 1
	`, column, column)

	if err := p.db.GetContext(ctx, &odds, query, gameID, marketType); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get best odds: %w", err)
	}

	return &odds, nil
}

// CreatePortfolio creates a new portfolio
func (p *PostgresDB) CreatePortfolio(portfolio types.Portfolio) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if portfolio.ID == "" {
		portfolio.ID = uuid.New().String()
	}

	query := `
		INSERT INTO portfolios (id, name, balance, initial_bankroll, bets_placed, bets_won, bets_lost,
			total_wagered, total_profit_loss, active_bets_count, active_bets_value, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NOW(), NOW())
	`

	_, err := p.db.ExecContext(ctx, query,
		portfolio.ID,
		portfolio.Name,
		portfolio.Balance,
		portfolio.InitialBankroll,
		portfolio.BetsPlaced,
		portfolio.BetsWon,
		portfolio.BetsLost,
		portfolio.TotalWagered,
		portfolio.TotalProfitLoss,
		portfolio.ActiveBetsCount,
		portfolio.ActiveBetsValue,
	)
	return err
}

// GetPortfolio returns a portfolio by ID
func (p *PostgresDB) GetPortfolio(id string) (*types.Portfolio, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var portfolio types.Portfolio
	query := `
		SELECT id, name, balance, initial_bankroll, bets_placed, bets_won, bets_lost,
			total_wagered, total_profit_loss, active_bets_count, active_bets_value, created_at, updated_at
		FROM portfolios WHERE id = $1
	`

	if err := p.db.GetContext(ctx, &portfolio, query, id); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get portfolio: %w", err)
	}

	return &portfolio, nil
}

// portfolioDeltaQuery increments portfolio metrics in place. All portfolio
// mutations must go through it: the trading cycle and the settler run
// concurrently, and whole-row snapshot writes lose each other's updates.
const portfolioDeltaQuery = `
	UPDATE portfolios SET
		balance = balance + $2, bets_placed = bets_placed + $3,
		bets_won = bets_won + $4, bets_lost = bets_lost + $5,
		total_wagered = total_wagered + $6, total_profit_loss = total_profit_loss + $7,
		active_bets_count = active_bets_count + $8, active_bets_value = active_bets_value + $9,
		updated_at = NOW()
	WHERE id = $1
`

// ApplyPortfolioDelta atomically applies a set of increments to a portfolio
func (p *PostgresDB) ApplyPortfolioDelta(id string, delta types.PortfolioDelta) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := p.db.ExecContext(ctx, portfolioDeltaQuery,
		id,
		delta.Balance,
		delta.BetsPlaced,
		delta.BetsWon,
		delta.BetsLost,
		delta.TotalWagered,
		delta.TotalProfitLoss,
		delta.ActiveBetsCount,
		delta.ActiveBetsValue,
	)
	return err
}

// PlaceBet records a new bet
func (p *PostgresDB) PlaceBet(bet types.Bet) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if bet.ID == "" {
		bet.ID = uuid.New().String()
	}

	query := `
		INSERT INTO bets (id, portfolio_id, game_id, selection, market_type, odds, stake,
			potential_win, actual_win, status, expected_value, model_probability, model_id, bookmaker, notes, settled_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, NOW(), NOW())
	`

	_, err := p.db.ExecContext(ctx, query,
		bet.ID,
		bet.PortfolioID,
		bet.GameID,
		bet.Selection,
		bet.MarketType,
		bet.Odds,
		bet.Stake,
		bet.PotentialWin,
		bet.ActualWin,
		bet.Status,
		bet.ExpectedValue,
		bet.Probability,
		bet.ModelID,
		bet.Bookmaker,
		bet.Notes,
		bet.SettledAt,
	)
	return err
}

// GetBetsForGame returns all bets for a game
func (p *PostgresDB) GetBetsForGame(gameID string) ([]types.Bet, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var bets []types.Bet
	query := `
		SELECT id, portfolio_id, game_id, selection, market_type, odds, stake,
			potential_win, actual_win, status, expected_value, COALESCE(model_probability, 0) AS model_probability,
			model_id, bookmaker, notes, settled_at, created_at, updated_at
		FROM bets WHERE game_id = $1
	`

	if err := p.db.SelectContext(ctx, &bets, query, gameID); err != nil {
		return nil, fmt.Errorf("failed to get bets: %w", err)
	}

	return bets, nil
}

// FinishedGames returns a sport's completed games with scores, oldest
// first — the input for rating models that replay history (Elo).
func (p *PostgresDB) FinishedGames(sportKey string) ([]types.Game, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var games []types.Game
	query := `
		SELECT id, sport_key, home_team, away_team, commence_time, status,
			home_score, away_score, created_at, updated_at
		FROM games
		WHERE sport_key = $1 AND home_score IS NOT NULL AND away_score IS NOT NULL
		ORDER BY commence_time ASC
	`

	if err := p.db.SelectContext(ctx, &games, query, sportKey); err != nil {
		return nil, fmt.Errorf("failed to get finished games: %w", err)
	}

	return games, nil
}

// GetPendingBets returns all pending bets
func (p *PostgresDB) GetPendingBets() ([]types.Bet, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var bets []types.Bet
	query := `
		SELECT id, portfolio_id, game_id, selection, market_type, odds, stake,
			potential_win, actual_win, status, expected_value, COALESCE(model_probability, 0) AS model_probability,
			model_id, bookmaker, notes, settled_at, created_at, updated_at
		FROM bets WHERE status = 'pending'
	`

	if err := p.db.SelectContext(ctx, &bets, query); err != nil {
		return nil, fmt.Errorf("failed to get pending bets: %w", err)
	}

	return bets, nil
}

// BetExists reports whether the portfolio already has a bet on a game
func (p *PostgresDB) BetExists(portfolioID, gameID string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var count int
	query := `SELECT COUNT(*) FROM bets WHERE portfolio_id = $1 AND game_id = $2`
	if err := p.db.GetContext(ctx, &count, query, portfolioID, gameID); err != nil {
		return false, fmt.Errorf("failed to check existing bets: %w", err)
	}

	return count > 0, nil
}

// GetSettledBets returns all settled bets in settlement order (oldest first)
func (p *PostgresDB) GetSettledBets() ([]types.Bet, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var bets []types.Bet
	query := `
		SELECT id, portfolio_id, game_id, selection, market_type, odds, stake,
			potential_win, actual_win, status, expected_value, COALESCE(model_probability, 0) AS model_probability,
			model_id, bookmaker, notes, settled_at, created_at, updated_at
		FROM bets WHERE status IN ('won', 'lost', 'void') AND settled_at IS NOT NULL
		ORDER BY settled_at ASC
	`

	if err := p.db.SelectContext(ctx, &bets, query); err != nil {
		return nil, fmt.Errorf("failed to get settled bets: %w", err)
	}

	return bets, nil
}

// UpdateBet updates a bet
func (p *PostgresDB) UpdateBet(bet types.Bet) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	query := `
		UPDATE bets SET
			status = $2, actual_win = $3, settled_at = $4, updated_at = NOW()
		WHERE id = $1
	`

	_, err := p.db.ExecContext(ctx, query, bet.ID, bet.Status, bet.ActualWin, bet.SettledAt)
	return err
}

// SettleBet atomically persists a settled bet and its portfolio delta in a
// single transaction, so a crash mid-settlement can't leave the balance out
// of sync with the bet status.
func (p *PostgresDB) SettleBet(bet types.Bet, delta types.PortfolioDelta) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tx, err := p.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin settlement transaction: %w", err)
	}
	defer tx.Rollback()

	betQuery := `
		UPDATE bets SET
			status = $2, actual_win = $3, settled_at = $4, updated_at = NOW()
		WHERE id = $1
	`
	if _, err := tx.ExecContext(ctx, betQuery, bet.ID, bet.Status, bet.ActualWin, bet.SettledAt); err != nil {
		return fmt.Errorf("failed to update bet: %w", err)
	}

	if _, err := tx.ExecContext(ctx, portfolioDeltaQuery,
		bet.PortfolioID,
		delta.Balance,
		delta.BetsPlaced,
		delta.BetsWon,
		delta.BetsLost,
		delta.TotalWagered,
		delta.TotalProfitLoss,
		delta.ActiveBetsCount,
		delta.ActiveBetsValue,
	); err != nil {
		return fmt.Errorf("failed to update portfolio: %w", err)
	}

	return tx.Commit()
}

// RecordPreviewBet stores a warmup-suppressed pick. The first cycle to see
// a (model, game) pair wins — later cycles hit the unique constraint and do
// nothing, so stochastic models can't rewrite their shadow record.
func (p *PostgresDB) RecordPreviewBet(pb types.PreviewBet) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if pb.ID == "" {
		pb.ID = uuid.New().String()
	}

	query := `
		INSERT INTO preview_bets (id, game_id, model_id, selection, market_type, bookmaker,
			odds, stake, model_probability, expected_value, confidence, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NOW())
		ON CONFLICT (model_id, game_id) DO NOTHING
	`

	_, err := p.db.ExecContext(ctx, query,
		pb.ID,
		pb.GameID,
		pb.ModelID,
		pb.Selection,
		pb.MarketType,
		pb.Bookmaker,
		pb.Odds,
		pb.Stake,
		pb.Probability,
		pb.ExpectedValue,
		pb.Confidence,
		pb.Status,
	)
	return err
}

// GetPendingPreviewBets returns unsettled preview bets
func (p *PostgresDB) GetPendingPreviewBets() ([]types.PreviewBet, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var bets []types.PreviewBet
	query := `
		SELECT id, game_id, model_id, selection, market_type, bookmaker,
			odds, stake, model_probability, expected_value, confidence, status, payout, settled_at, created_at
		FROM preview_bets WHERE status = 'pending'
	`

	if err := p.db.SelectContext(ctx, &bets, query); err != nil {
		return nil, fmt.Errorf("failed to get pending preview bets: %w", err)
	}

	return bets, nil
}

// GetSettledPreviewBets returns settled preview bets, oldest first
func (p *PostgresDB) GetSettledPreviewBets() ([]types.PreviewBet, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var bets []types.PreviewBet
	query := `
		SELECT id, game_id, model_id, selection, market_type, bookmaker,
			odds, stake, model_probability, expected_value, confidence, status, payout, settled_at, created_at
		FROM preview_bets WHERE status IN ('won', 'lost') AND settled_at IS NOT NULL
		ORDER BY settled_at ASC
	`

	if err := p.db.SelectContext(ctx, &bets, query); err != nil {
		return nil, fmt.Errorf("failed to get settled preview bets: %w", err)
	}

	return bets, nil
}

// SettlePreviewBet marks a preview bet won or lost. No portfolio is touched:
// previews never move money.
func (p *PostgresDB) SettlePreviewBet(pb types.PreviewBet) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	query := `
		UPDATE preview_bets SET status = $2, payout = $3, settled_at = $4
		WHERE id = $1
	`

	_, err := p.db.ExecContext(ctx, query, pb.ID, pb.Status, pb.Payout, pb.SettledAt)
	return err
}

// SeedTeamPrior upserts a team's market-derived Beta prior
func (p *PostgresDB) SeedTeamPrior(prior types.TeamPrior) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	query := `
		INSERT INTO team_priors (team_name, sport_key, prior_wins, prior_losses, source, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (team_name, sport_key) DO UPDATE SET
			prior_wins = EXCLUDED.prior_wins,
			prior_losses = EXCLUDED.prior_losses,
			source = EXCLUDED.source,
			updated_at = NOW()
	`

	_, err := p.db.ExecContext(ctx, query,
		prior.TeamName,
		prior.SportKey,
		prior.PriorWins,
		prior.PriorLosses,
		prior.Source,
	)
	return err
}

// TeamPrior returns a team's seeded pseudo-counts, (0, 0) when unseeded.
// It implements analysis.PriorsProvider, which cannot error — failures log
// and fall back to no prior, matching TeamStatsService.TeamRecord.
func (p *PostgresDB) TeamPrior(teamName, sportKey string) (priorWins, priorLosses float64) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var prior types.TeamPrior
	query := `
		SELECT team_name, sport_key, prior_wins, prior_losses, source, updated_at
		FROM team_priors WHERE team_name = $1 AND sport_key = $2
	`

	if err := p.db.GetContext(ctx, &prior, query, teamName, sportKey); err != nil {
		if err != sql.ErrNoRows {
			log.Error().Err(err).Str("team", teamName).Msg("Failed to get team prior")
		}
		return 0, 0
	}

	return prior.PriorWins, prior.PriorLosses
}

// CountFinishedGames returns how many finished games a sport has recorded
func (p *PostgresDB) CountFinishedGames(sportKey string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var count int
	query := `SELECT COUNT(*) FROM games WHERE sport_key = $1 AND status = 'finished'`
	if err := p.db.GetContext(ctx, &count, query, sportKey); err != nil {
		return 0, fmt.Errorf("failed to count finished games: %w", err)
	}
	return count, nil
}

// HasTeamPriors reports whether a sport has any seeded priors
func (p *PostgresDB) HasTeamPriors(sportKey string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var count int
	query := `SELECT COUNT(*) FROM team_priors WHERE sport_key = $1`
	if err := p.db.GetContext(ctx, &count, query, sportKey); err != nil {
		return false, fmt.Errorf("failed to check team priors: %w", err)
	}
	return count > 0, nil
}

// SaveAPIQuota records the latest Odds API credit counters
func (p *PostgresDB) SaveAPIQuota(remaining, used float64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	query := `
		INSERT INTO api_quota (id, requests_remaining, requests_used, updated_at)
		VALUES (1, $1, $2, timezone('UTC', NOW()))
		ON CONFLICT (id) DO UPDATE SET
			requests_remaining = EXCLUDED.requests_remaining,
			requests_used = EXCLUDED.requests_used,
			updated_at = timezone('UTC', NOW())
	`

	_, err := p.db.ExecContext(ctx, query, remaining, used)
	return err
}

// GetAPIQuota returns the last-seen Odds API credit state, nil before the
// first API call ever lands.
func (p *PostgresDB) GetAPIQuota() (*types.APIQuota, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var quota types.APIQuota
	query := `SELECT requests_remaining, requests_used, updated_at FROM api_quota WHERE id = 1`

	if err := p.db.GetContext(ctx, &quota, query); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get api quota: %w", err)
	}

	return &quota, nil
}

// SaveTeamStats saves or updates team statistics
func (p *PostgresDB) SaveTeamStats(stats types.TeamStats) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	query := `
		INSERT INTO team_stats (id, team_name, sport_key, games_played, wins, losses, draws,
			points_scored, points_allowed, win_rate, last_updated)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW())
		ON CONFLICT (id) DO UPDATE SET
			games_played = EXCLUDED.games_played,
			wins = EXCLUDED.wins,
			losses = EXCLUDED.losses,
			draws = EXCLUDED.draws,
			points_scored = EXCLUDED.points_scored,
			points_allowed = EXCLUDED.points_allowed,
			win_rate = EXCLUDED.win_rate,
			last_updated = NOW()
	`

	if stats.ID == "" {
		stats.ID = uuid.New().String()
	}

	_, err := p.db.ExecContext(ctx, query,
		stats.ID,
		stats.TeamName,
		stats.SportKey,
		stats.GamesPlayed,
		stats.Wins,
		stats.Losses,
		stats.Draws,
		stats.PointsScored,
		stats.PointsAllowed,
		stats.WinRate,
	)
	return err
}

// GetTeamStats returns statistics for a team
func (p *PostgresDB) GetTeamStats(teamName, sportKey string) (*types.TeamStats, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var stats types.TeamStats
	query := `
		SELECT id, team_name, sport_key, games_played, wins, losses, draws,
			points_scored, points_allowed, win_rate, last_updated
		FROM team_stats WHERE team_name = $1 AND sport_key = $2
	`

	if err := p.db.GetContext(ctx, &stats, query, teamName, sportKey); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get team stats: %w", err)
	}

	return &stats, nil
}

// UpdateGameScores updates game with final scores
func (p *PostgresDB) UpdateGameScores(gameID string, homeScore, awayScore int, status string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	query := `
		UPDATE games SET
			home_score = $2, away_score = $3, status = $4, updated_at = NOW()
		WHERE id = $1
	`

	_, err := p.db.ExecContext(ctx, query, gameID, homeScore, awayScore, status)
	return err
}

// GetAllTeamStats returns all team stats for a sport
func (p *PostgresDB) GetAllTeamStats(sportKey string) ([]types.TeamStats, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var stats []types.TeamStats
	query := `
		SELECT id, team_name, sport_key, games_played, wins, losses, draws,
			points_scored, points_allowed, win_rate, last_updated
		FROM team_stats WHERE sport_key = $1
		ORDER BY win_rate DESC
	`

	if err := p.db.SelectContext(ctx, &stats, query, sportKey); err != nil {
		return nil, fmt.Errorf("failed to get team stats: %w", err)
	}

	return stats, nil
}

// IncrementTeamStats atomically increments team statistics
func (p *PostgresDB) IncrementTeamStats(teamName, sportKey string, isWin, isHome bool, pointsScored, pointsAllowed float64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// First, get current stats
	stats, err := p.GetTeamStats(teamName, sportKey)
	if err != nil {
		return err
	}

	if stats == nil {
		// Create new stats
		newStats := types.TeamStats{
			ID:            uuid.New().String(),
			TeamName:      teamName,
			SportKey:      sportKey,
			GamesPlayed:   1,
			PointsScored:  pointsScored,
			PointsAllowed: pointsAllowed,
		}
		if isWin {
			newStats.Wins = 1
		} else {
			newStats.Losses = 1
		}
		newStats.WinRate = float64(newStats.Wins) / float64(newStats.GamesPlayed)
		return p.SaveTeamStats(newStats)
	}

	// Update existing stats
	query := `
		UPDATE team_stats SET
			games_played = games_played + 1,
			wins = wins + $3,
			losses = losses + $4,
			points_scored = points_scored + $5,
			points_allowed = points_allowed + $6,
			win_rate = (wins + $3)::float / (games_played + 1)::float,
			last_updated = NOW()
		WHERE team_name = $1 AND sport_key = $2
	`

	wins := 0
	losses := 0
	if isWin {
		wins = 1
	} else {
		losses = 1
	}

	_, err = p.db.ExecContext(ctx, query, teamName, sportKey, wins, losses, pointsScored, pointsAllowed)
	return err
}

// GetTeamStatsByName returns team stats by team name only
func (p *PostgresDB) GetTeamStatsByName(teamName string) (*types.TeamStats, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var stats types.TeamStats
	query := `
		SELECT id, team_name, sport_key, games_played, wins, losses, draws,
			points_scored, points_allowed, win_rate, last_updated
		FROM team_stats WHERE team_name = $1
		LIMIT 1
	`

	if err := p.db.GetContext(ctx, &stats, query, teamName); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get team stats: %w", err)
	}

	return &stats, nil
}
