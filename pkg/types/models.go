package types

import (
	"time"
)

// Sport keys for The Odds API
const (
	SportNFL   = "americanfootball_nfl"
	SportNCAAF = "americanfootball_ncaaf"
	SportNBA   = "basketball_nba"
	SportNCAAB = "basketball_ncaab"
	SportMLB   = "baseball_mlb"
)

// Market types
const (
	MarketMoneyline = "h2h"
	MarketSpread    = "spreads"
	MarketTotals    = "totals"
)

// Bet status
const (
	BetStatusPending  = "pending"
	BetStatusWon      = "won"
	BetStatusLost     = "lost"
	BetStatusCanceled = "canceled"
	BetStatusVoid     = "void"
)

// Outcome types
const (
	OutcomeHome = "home"
	OutcomeAway = "away"
	OutcomeDraw = "draw"
)

// Game represents a sports game
type Game struct {
	ID            string    `json:"id" db:"id"`
	SportKey      string    `json:"sport_key" db:"sport_key"`
	CommenceTime  time.Time `json:"commence_time" db:"commence_time"`
	HomeTeam      string    `json:"home_team" db:"home_team"`
	AwayTeam      string    `json:"away_team" db:"away_team"`
	HomeScore     *int      `json:"home_score,omitempty" db:"home_score"`
	AwayScore     *int      `json:"away_score,omitempty" db:"away_score"`
	Status        string    `json:"status" db:"status"` // scheduled, in_progress, finished
	HomeLineScore *string   `json:"home_line_score,omitempty" db:"-"`
	AwayLineScore *string   `json:"away_line_score,omitempty" db:"-"`
	CreatedAt     time.Time `json:"created_at" db:"created_at"`
	UpdatedAt     time.Time `json:"updated_at" db:"updated_at"`
}

// IsFinished returns true if the game is complete
func (g *Game) IsFinished() bool {
	return g.Status == "finished"
}

// IsUpcoming returns true if the game hasn't started
func (g *Game) IsUpcoming() bool {
	return g.Status == "scheduled" || g.CommenceTime.After(time.Now())
}

// Winner returns the winning team or empty string
func (g *Game) Winner() string {
	if g.HomeScore == nil || g.AwayScore == nil {
		return ""
	}
	if *g.HomeScore > *g.AwayScore {
		return g.HomeTeam
	}
	if *g.AwayScore > *g.HomeScore {
		return g.AwayTeam
	}
	return "" // Draw
}

// GameOdds represents odds for a game from a bookmaker
type GameOdds struct {
	ID          string    `json:"id" db:"id"`
	GameID      string    `json:"game_id" db:"game_id"`
	Bookmaker   string    `json:"bookmaker" db:"bookmaker"`
	MarketType  string    `json:"market_type" db:"market_type"`
	HomeOdds    *float64  `json:"home_odds,omitempty" db:"home_odds"`
	AwayOdds    *float64  `json:"away_odds,omitempty" db:"away_odds"`
	DrawOdds    *float64  `json:"draw_odds,omitempty" db:"draw_odds"`
	HomeSpread  *float64  `json:"home_spread,omitempty" db:"home_spread"`
	AwaySpread  *float64  `json:"away_spread,omitempty" db:"away_spread"`
	OverUnder   *float64  `json:"over_under,omitempty" db:"over_under"`
	OverOdds    *float64  `json:"over_odds,omitempty" db:"over_odds"`
	UnderOdds   *float64  `json:"under_odds,omitempty" db:"under_odds"`
	LastUpdate  time.Time `json:"last_update" db:"last_update"`
	RetrievedAt time.Time `json:"retrieved_at" db:"retrieved_at"`
}

// ImpliedProbability returns the implied probability from decimal odds
func ImpliedProbability(odds float64) float64 {
	return 1.0 / odds
}

// AmericanToDecimal converts American odds to decimal
func AmericanToDecimal(americanOdds int) float64 {
	if americanOdds > 0 {
		return float64(americanOdds)/100.0 + 1.0
	}
	return 100.0/float64(americanOdds) + 1.0
}

// Portfolio tracks betting performance
type Portfolio struct {
	ID              string    `json:"id" db:"id"`
	Name            string    `json:"name" db:"name"`
	Balance         float64   `json:"balance" db:"balance"`
	InitialBankroll float64   `json:"initial_bankroll" db:"initial_bankroll"`
	BetsPlaced      int       `json:"bets_placed" db:"bets_placed"`
	BetsWon         int       `json:"bets_won" db:"bets_won"`
	BetsLost        int       `json:"bets_lost" db:"bets_lost"`
	TotalWagered    float64   `json:"total_wagered" db:"total_wagered"`
	TotalProfitLoss float64   `json:"total_profit_loss" db:"total_profit_loss"`
	ActiveBetsCount int       `json:"active_bets_count" db:"active_bets_count"`
	ActiveBetsValue float64   `json:"active_bets_value" db:"active_bets_value"`
	CreatedAt       time.Time `json:"created_at" db:"created_at"`
	UpdatedAt       time.Time `json:"updated_at" db:"updated_at"`
}

// PortfolioDelta is a set of increments applied atomically to a portfolio.
// Concurrent writers (trading cycle, settler) must use deltas rather than
// writing whole-row snapshots, or they overwrite each other's updates.
type PortfolioDelta struct {
	Balance         float64
	BetsPlaced      int
	BetsWon         int
	BetsLost        int
	TotalWagered    float64
	TotalProfitLoss float64
	ActiveBetsCount int
	ActiveBetsValue float64
}

// ROI returns the return on investment as a percentage
func (p *Portfolio) ROI() float64 {
	if p.TotalWagered == 0 {
		return 0
	}
	return (p.TotalProfitLoss / p.TotalWagered) * 100
}

// WinRate returns the win rate as a percentage
func (p *Portfolio) WinRate() float64 {
	total := p.BetsWon + p.BetsLost
	if total == 0 {
		return 0
	}
	return float64(p.BetsWon) / float64(total) * 100
}

// Bet represents a placed bet
type Bet struct {
	ID            string     `json:"id" db:"id"`
	PortfolioID   string     `json:"portfolio_id" db:"portfolio_id"`
	GameID        string     `json:"game_id" db:"game_id"`
	Selection     string     `json:"selection" db:"selection"`     // home, away, draw
	MarketType    string     `json:"market_type" db:"market_type"` // h2h, spreads, totals
	Odds          float64    `json:"odds" db:"odds"`
	Stake         float64    `json:"stake" db:"stake"`
	PotentialWin  float64    `json:"potential_win" db:"potential_win"`
	ActualWin     *float64   `json:"actual_win,omitempty" db:"actual_win"`
	Status        string     `json:"status" db:"status"`
	ExpectedValue float64    `json:"expected_value" db:"expected_value"`
	Probability   float64    `json:"model_probability" db:"model_probability"`
	ModelID       string     `json:"model_id" db:"model_id"`
	Bookmaker     string     `json:"bookmaker" db:"bookmaker"`
	Notes         string     `json:"notes,omitempty" db:"notes"`
	SettledAt     *time.Time `json:"settled_at,omitempty" db:"settled_at"`
	CreatedAt     time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at" db:"updated_at"`
}

// Profit returns the profit/loss from this bet
func (b *Bet) Profit() float64 {
	if b.ActualWin == nil {
		return 0
	}
	return *b.ActualWin - b.Stake
}

// PreviewBet is a bet a model would have placed had the warmup confidence
// gate not suppressed it. Previews settle like bets but move no money — they
// exist to score the models (and the gate itself) while records warm up.
type PreviewBet struct {
	ID            string     `json:"id" db:"id"`
	GameID        string     `json:"game_id" db:"game_id"`
	ModelID       string     `json:"model_id" db:"model_id"`
	Selection     string     `json:"selection" db:"selection"`
	MarketType    string     `json:"market_type" db:"market_type"`
	Bookmaker     string     `json:"bookmaker" db:"bookmaker"`
	Odds          float64    `json:"odds" db:"odds"`
	Stake         float64    `json:"stake" db:"stake"`
	Probability   float64    `json:"model_probability" db:"model_probability"`
	ExpectedValue float64    `json:"expected_value" db:"expected_value"`
	Confidence    float64    `json:"confidence" db:"confidence"`
	Status        string     `json:"status" db:"status"`
	Payout        *float64   `json:"payout,omitempty" db:"payout"`
	SettledAt     *time.Time `json:"settled_at,omitempty" db:"settled_at"`
	CreatedAt     time.Time  `json:"created_at" db:"created_at"`
}

// Profit returns the would-be profit/loss from this preview
func (b *PreviewBet) Profit() float64 {
	if b.Payout == nil {
		return 0
	}
	return *b.Payout - b.Stake
}

// TeamPrior is a per-team Beta prior seeded from market expectations
// (e.g. preseason win totals) so a season doesn't start at 50/50.
type TeamPrior struct {
	TeamName    string    `json:"team_name" db:"team_name"`
	SportKey    string    `json:"sport_key" db:"sport_key"`
	PriorWins   float64   `json:"prior_wins" db:"prior_wins"`
	PriorLosses float64   `json:"prior_losses" db:"prior_losses"`
	Source      string    `json:"source" db:"source"`
	UpdatedAt   time.Time `json:"updated_at" db:"updated_at"`
}

// APIQuota is the last-seen Odds API credit state, from the
// x-requests-remaining / x-requests-used response headers.
type APIQuota struct {
	RequestsRemaining float64   `json:"requests_remaining" db:"requests_remaining"`
	RequestsUsed      float64   `json:"requests_used" db:"requests_used"`
	UpdatedAt         time.Time `json:"updated_at" db:"updated_at"`
}

// TeamStats stores historical team statistics
type TeamStats struct {
	ID            string    `json:"id" db:"id"`
	TeamName      string    `json:"team_name" db:"team_name"`
	SportKey      string    `json:"sport_key" db:"sport_key"`
	GamesPlayed   int       `json:"games_played" db:"games_played"`
	Wins          int       `json:"wins" db:"wins"`
	Losses        int       `json:"losses" db:"losses"`
	Draws         int       `json:"draws" db:"draws"`
	PointsScored  float64   `json:"points_scored" db:"points_scored"`
	PointsAllowed float64   `json:"points_allowed" db:"points_allowed"`
	WinRate       float64   `json:"win_rate" db:"win_rate"`
	LastUpdated   time.Time `json:"last_updated" db:"last_updated"`
}

// WinRate returns the team's win rate
func (t *TeamStats) CalcWinRate() float64 {
	total := t.Wins + t.Losses + t.Draws
	if total == 0 {
		return 0
	}
	return float64(t.Wins) / float64(total)
}
