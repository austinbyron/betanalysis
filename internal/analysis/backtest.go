package analysis

import (
	"fmt"
	"time"

	"github.com/austinbyron/betanalysis/internal/config"
	"github.com/austinbyron/betanalysis/pkg/types"
	"github.com/rs/zerolog/log"
)

// BacktestStore is the storage surface a backtest needs
type BacktestStore interface {
	GetFinishedGames(sportKey string, start, end time.Time) ([]types.Game, error)
	GetOddsForGameAt(gameID string, asOf time.Time) ([]types.GameOdds, error)
}

// BacktestResult contains the results of a backtest
type BacktestResult struct {
	ModelType     string
	TotalBets     int
	Wins          int
	Losses        int
	WinRate       float64
	TotalStaked   float64
	TotalProfit   float64
	ROI           float64
	FinalBankroll float64
	MaxDrawdown   float64
	SharpeRatio   float64
}

// replayStats is an in-memory StatsProvider built up as the backtest replays
// games chronologically, so the estimator only ever sees records that were
// known before each game — no lookahead into the team_stats table.
type replayStats struct {
	wins   map[string]int
	losses map[string]int
}

func newReplayStats() *replayStats {
	return &replayStats{wins: make(map[string]int), losses: make(map[string]int)}
}

// TeamRecord implements StatsProvider
func (r *replayStats) TeamRecord(teamName, _ string) (int, int) {
	return r.wins[teamName], r.losses[teamName]
}

func (r *replayStats) record(game types.Game) {
	winner := game.Winner()
	switch winner {
	case game.HomeTeam:
		r.wins[game.HomeTeam]++
		r.losses[game.AwayTeam]++
	case game.AwayTeam:
		r.wins[game.AwayTeam]++
		r.losses[game.HomeTeam]++
	}
}

// RunBacktest replays stored finished games in chronological order: for each
// game it uses only the odds snapshots taken before kickoff and the team
// records accumulated so far, sizes stakes with the same Kelly logic as live
// trading, and settles against the actual result.
func RunBacktest(store BacktestStore, analysisCfg config.AnalysisConfig, tradingCfg config.TradingConfig,
	sportKey string, start, end time.Time, bankroll float64) (*BacktestResult, error) {

	stats := newReplayStats()
	estimator, err := NewEstimator(analysisCfg, stats, nil)
	if err != nil {
		return nil, err
	}
	selector := NewSelector(estimator, "", analysisCfg.MarketWeight, tradingCfg.MinOdds, tradingCfg.MinExpectedValue)

	games, err := store.GetFinishedGames(sportKey, start, end)
	if err != nil {
		return nil, fmt.Errorf("failed to get finished games: %w", err)
	}
	if len(games) == 0 {
		return nil, fmt.Errorf("no finished games with scores stored for %s in range — run collection for a while first", sportKey)
	}

	result := BacktestResult{ModelType: estimator.Name()}
	var returns []float64
	peak := bankroll
	maxDrawdown := 0.0

	for _, game := range games {
		odds, err := store.GetOddsForGameAt(game.ID, game.CommenceTime)
		if err != nil {
			log.Warn().Err(err).Str("game", game.ID).Msg("Skipping game: odds lookup failed")
			stats.record(game)
			continue
		}

		bet := selector.RecommendBet(game, odds)
		if bet != nil {
			stake := KellyStake(bet.Probability, bet.Odds, bankroll, tradingCfg.KellyFraction, tradingCfg.MaxStakeFraction)
			if stake >= tradingCfg.MinStake && stake <= bankroll {
				won := (bet.Selection == types.OutcomeHome && game.Winner() == game.HomeTeam) ||
					(bet.Selection == types.OutcomeAway && game.Winner() == game.AwayTeam)

				result.TotalBets++
				result.TotalStaked += stake
				if won {
					profit := stake * (bet.Odds - 1)
					bankroll += profit
					result.TotalProfit += profit
					result.Wins++
					returns = append(returns, bet.Odds-1)
				} else {
					bankroll -= stake
					result.TotalProfit -= stake
					result.Losses++
					returns = append(returns, -1)
				}

				if bankroll > peak {
					peak = bankroll
				}
				if peak > 0 {
					if dd := (peak - bankroll) / peak; dd > maxDrawdown {
						maxDrawdown = dd
					}
				}
			}
		}

		// Learn from the result only after the betting decision
		stats.record(game)
	}

	if result.Wins+result.Losses > 0 {
		result.WinRate = float64(result.Wins) / float64(result.Wins+result.Losses) * 100
	}
	if result.TotalStaked > 0 {
		result.ROI = result.TotalProfit / result.TotalStaked * 100
	}
	result.FinalBankroll = bankroll
	result.MaxDrawdown = maxDrawdown * 100
	result.SharpeRatio = SharpeRatio(returns, 0)

	log.Info().
		Str("model", result.ModelType).
		Int("games", len(games)).
		Int("bets", result.TotalBets).
		Float64("win_rate", result.WinRate).
		Float64("roi", result.ROI).
		Float64("final_bankroll", result.FinalBankroll).
		Float64("max_drawdown_pct", result.MaxDrawdown).
		Msg("Backtest complete")

	return &result, nil
}
