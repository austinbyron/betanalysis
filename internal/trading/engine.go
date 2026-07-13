package trading

import (
	"fmt"
	"time"

	"github.com/austinbyron/betanalysis/internal/analysis"
	"github.com/austinbyron/betanalysis/internal/config"
	"github.com/austinbyron/betanalysis/pkg/types"
	"github.com/rs/zerolog/log"
)

// Store is the storage surface the trading engine and settler need. It is an
// interface so tests can run against a fake instead of a live Postgres.
type Store interface {
	GetUpcomingGames(sportKey string) ([]types.Game, error)
	GetGameByID(id string) (*types.Game, error)
	GetOddsForGame(gameID string) ([]types.GameOdds, error)
	GetPortfolio(id string) (*types.Portfolio, error)
	CreatePortfolio(portfolio types.Portfolio) error
	ApplyPortfolioDelta(id string, delta types.PortfolioDelta) error
	PlaceBet(bet types.Bet) error
	BetExists(portfolioID, gameID string) (bool, error)
	GetPendingBets() ([]types.Bet, error)
	SettleBet(bet types.Bet, delta types.PortfolioDelta) error
	RecordPreviewBet(pb types.PreviewBet) error
	GetPendingPreviewBets() ([]types.PreviewBet, error)
	SettlePreviewBet(pb types.PreviewBet) error
}

// Engine handles paper trading operations
type Engine struct {
	store    Store
	selector *analysis.Selector
	config   config.TradingConfig
}

// NewEngine creates a new trading engine
func NewEngine(store Store, selector *analysis.Selector, cfg config.TradingConfig) *Engine {
	return &Engine{
		store:    store,
		selector: selector,
		config:   cfg,
	}
}

// RunTradingCycle executes one trading cycle
func (e *Engine) RunTradingCycle(sport string) error {
	log.Info().Str("sport", sport).Str("model", e.config.ModelName).Msg("Running trading cycle")

	games, err := e.store.GetUpcomingGames(sport)
	if err != nil {
		return fmt.Errorf("failed to get games: %w", err)
	}

	portfolio, err := e.store.GetPortfolio(e.config.PortfolioID)
	if err != nil {
		return fmt.Errorf("failed to get portfolio: %w", err)
	}
	if portfolio == nil {
		name := e.config.ModelName
		if name == "" {
			name = "Default Portfolio"
		}
		portfolio = &types.Portfolio{
			ID:              e.config.PortfolioID,
			Name:            name,
			Balance:         e.config.InitialBankroll,
			InitialBankroll: e.config.InitialBankroll,
		}
		if err := e.store.CreatePortfolio(*portfolio); err != nil {
			return fmt.Errorf("failed to create portfolio: %w", err)
		}
	}

	// The portfolio snapshot is only for stake sizing; all persisted changes
	// go through an atomic delta so a concurrently-running settler can't be
	// overwritten (the daemon fires both on the same tick every hour).
	var delta types.PortfolioDelta
	betsPlaced := 0
	for _, game := range games {
		// One bet per game per portfolio — cycles run every 30 minutes and
		// must not re-bet games they already acted on.
		exists, err := e.store.BetExists(portfolio.ID, game.ID)
		if err != nil {
			log.Error().Err(err).Str("game", game.ID).Msg("Failed to check existing bets")
			continue
		}
		if exists {
			continue
		}

		odds, err := e.store.GetOddsForGame(game.ID)
		if err != nil {
			log.Error().Err(err).Str("game", game.ID).Msg("Failed to get odds")
			continue
		}

		bet, preview := e.selector.RecommendBoth(game, odds)
		if bet == nil {
			if preview != nil {
				e.recordPreview(game, preview, portfolio.Balance)
			}
			continue
		}

		// Size the stake with the same probability the EV was computed from.
		// A Kelly stake below the minimum means the edge is marginal — skip
		// the bet rather than rounding the stake up.
		stake := analysis.KellyStake(bet.Probability, bet.Odds, portfolio.Balance, e.config.KellyFraction, e.config.MaxStakeFraction)
		if stake < e.config.MinStake {
			log.Debug().Str("game", game.ID).Float64("stake", stake).Msg("Kelly stake below minimum, skipping")
			continue
		}
		if stake > portfolio.Balance {
			log.Warn().Float64("balance", portfolio.Balance).Float64("stake", stake).Msg("Insufficient balance")
			continue
		}

		bet.PortfolioID = portfolio.ID
		bet.Stake = stake
		bet.PotentialWin = stake * (bet.Odds - 1)

		if err := e.store.PlaceBet(*bet); err != nil {
			log.Error().Err(err).Str("game", game.ID).Msg("Failed to place bet")
			continue
		}

		portfolio.Balance -= stake
		delta.Balance -= stake
		delta.TotalWagered += stake
		delta.ActiveBetsCount++
		delta.ActiveBetsValue += stake
		delta.BetsPlaced++

		log.Info().
			Str("game", game.ID).
			Str("selection", bet.Selection).
			Float64("odds", bet.Odds).
			Float64("stake", stake).
			Float64("ev", bet.ExpectedValue).
			Float64("probability", bet.Probability).
			Msg("Bet placed")

		betsPlaced++
	}

	if betsPlaced > 0 {
		if err := e.store.ApplyPortfolioDelta(portfolio.ID, delta); err != nil {
			return fmt.Errorf("failed to update portfolio: %w", err)
		}
	}

	log.Info().Int("bets_placed", betsPlaced).Float64("balance", portfolio.Balance).Str("model", e.config.ModelName).Msg("Trading cycle complete")

	return nil
}

// recordPreview stores the pick the warmup gate suppressed, sized as if it
// were real so the stake gate still applies. First write per (model, game)
// wins; the store ignores repeats, so re-sampling cycles can't rewrite it.
func (e *Engine) recordPreview(game types.Game, preview *types.Bet, bankroll float64) {
	stake := analysis.KellyStake(preview.Probability, preview.Odds, bankroll, e.config.KellyFraction, e.config.MaxStakeFraction)
	if stake < e.config.MinStake {
		return
	}

	pb := types.PreviewBet{
		GameID:        game.ID,
		ModelID:       preview.ModelID,
		Selection:     preview.Selection,
		MarketType:    preview.MarketType,
		Bookmaker:     preview.Bookmaker,
		Odds:          preview.Odds,
		Stake:         stake,
		Probability:   preview.Probability,
		ExpectedValue: preview.ExpectedValue,
		Confidence:    e.selector.Confidence(game),
		Status:        types.BetStatusPending,
	}
	if err := e.store.RecordPreviewBet(pb); err != nil {
		log.Error().Err(err).Str("game", game.ID).Str("model", pb.ModelID).Msg("Failed to record preview bet")
	}
}

// Report returns the current portfolio state
func (e *Engine) Report(portfolioID string) (*types.Portfolio, error) {
	portfolio, err := e.store.GetPortfolio(portfolioID)
	if err != nil {
		return nil, fmt.Errorf("failed to get portfolio: %w", err)
	}
	if portfolio == nil {
		return nil, fmt.Errorf("portfolio %s not found", portfolioID)
	}
	return portfolio, nil
}

// Settler handles bet settlement
type Settler struct {
	store Store
}

// NewSettler creates a new settler
func NewSettler(store Store) *Settler {
	return &Settler{store: store}
}

// SettleBets settles all completed bets. Each bet and its portfolio update
// are persisted in a single transaction.
func (s *Settler) SettleBets() error {
	log.Info().Msg("Settling bets")

	bets, err := s.store.GetPendingBets()
	if err != nil {
		return fmt.Errorf("failed to get pending bets: %w", err)
	}

	settled := 0
	for _, bet := range bets {
		game, err := s.store.GetGameByID(bet.GameID)
		if err != nil {
			log.Error().Err(err).Str("bet", bet.ID).Msg("Failed to get game")
			continue
		}
		if game == nil || !game.IsFinished() {
			continue
		}

		won := selectionWon(bet.Selection, game)

		var payout float64
		if won {
			payout = bet.Stake + bet.PotentialWin
			bet.Status = types.BetStatusWon
		} else {
			payout = 0
			bet.Status = types.BetStatusLost
		}

		now := time.Now()
		bet.ActualWin = &payout
		bet.SettledAt = &now

		delta := types.PortfolioDelta{
			Balance:         payout,
			TotalProfitLoss: payout - bet.Stake,
			ActiveBetsCount: -1,
			ActiveBetsValue: -bet.Stake,
		}
		if won {
			delta.BetsWon = 1
		} else {
			delta.BetsLost = 1
		}

		if err := s.store.SettleBet(bet, delta); err != nil {
			log.Error().Err(err).Str("bet", bet.ID).Msg("Failed to settle bet")
			continue
		}

		log.Info().
			Str("bet", bet.ID).
			Str("status", bet.Status).
			Float64("payout", payout).
			Msg("Bet settled")

		settled++
	}

	log.Info().Int("settled", settled).Msg("Settlement complete")

	return s.settlePreviews()
}

// settlePreviews resolves finished preview bets. Previews move no money —
// no portfolio delta, just a would-be payout for the shadow record.
func (s *Settler) settlePreviews() error {
	previews, err := s.store.GetPendingPreviewBets()
	if err != nil {
		return fmt.Errorf("failed to get pending preview bets: %w", err)
	}

	settled := 0
	for _, pb := range previews {
		game, err := s.store.GetGameByID(pb.GameID)
		if err != nil {
			log.Error().Err(err).Str("preview", pb.ID).Msg("Failed to get game")
			continue
		}
		if game == nil || !game.IsFinished() {
			continue
		}

		var payout float64
		if selectionWon(pb.Selection, game) {
			payout = pb.Stake * pb.Odds
			pb.Status = types.BetStatusWon
		} else {
			payout = 0
			pb.Status = types.BetStatusLost
		}

		now := time.Now()
		pb.Payout = &payout
		pb.SettledAt = &now

		if err := s.store.SettlePreviewBet(pb); err != nil {
			log.Error().Err(err).Str("preview", pb.ID).Msg("Failed to settle preview bet")
			continue
		}
		settled++
	}

	if settled > 0 {
		log.Info().Int("settled", settled).Msg("Preview settlement complete")
	}
	return nil
}

// selectionWon reports whether a moneyline selection won a finished game
func selectionWon(selection string, game *types.Game) bool {
	switch selection {
	case types.OutcomeHome:
		return game.Winner() == game.HomeTeam
	case types.OutcomeAway:
		return game.Winner() == game.AwayTeam
	case types.OutcomeDraw:
		return game.Winner() == ""
	}
	return false
}
