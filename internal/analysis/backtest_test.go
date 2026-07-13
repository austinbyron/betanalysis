package analysis

import (
	"testing"
	"time"

	"github.com/austinbyron/betanalysis/internal/config"
	"github.com/austinbyron/betanalysis/pkg/types"
)

// fakeBacktestStore serves canned games and odds
type fakeBacktestStore struct {
	games []types.Game
	odds  map[string][]types.GameOdds
}

func (f *fakeBacktestStore) GetFinishedGames(_ string, _, _ time.Time) ([]types.Game, error) {
	return f.games, nil
}

func (f *fakeBacktestStore) GetOddsForGameAt(gameID string, _ time.Time) ([]types.GameOdds, error) {
	return f.odds[gameID], nil
}

func intp(v int) *int { return &v }

func backtestConfigs() (config.AnalysisConfig, config.TradingConfig) {
	analysisCfg := config.AnalysisConfig{
		ModelType:    "historical",
		MarketWeight: 0, // isolate the deterministic model probability
	}
	tradingCfg := config.TradingConfig{
		MinStake:         1,
		MaxStakeFraction: 0.05,
		KellyFraction:    0.5,
		MinOdds:          1.5,
		MinExpectedValue: 0.05,
	}
	return analysisCfg, tradingCfg
}

func TestRunBacktestAccounting(t *testing.T) {
	kickoff := time.Date(2026, 1, 1, 18, 0, 0, 0, time.UTC)

	// Same matchup four times; the home team always wins. With no prior
	// records the first game estimates 50/50, and 2.4 odds clear the EV bar.
	var games []types.Game
	odds := make(map[string][]types.GameOdds)
	for i := 0; i < 4; i++ {
		id := string(rune('a' + i))
		games = append(games, types.Game{
			ID:           id,
			SportKey:     "test_sport",
			CommenceTime: kickoff.AddDate(0, 0, i*7),
			HomeTeam:     "Home",
			AwayTeam:     "Away",
			HomeScore:    intp(30),
			AwayScore:    intp(10),
			Status:       "finished",
		})
		odds[id] = []types.GameOdds{
			{GameID: id, Bookmaker: "book", MarketType: types.MarketMoneyline, HomeOdds: f64(2.4), AwayOdds: f64(2.4)},
		}
	}

	analysisCfg, tradingCfg := backtestConfigs()
	result, err := RunBacktest(&fakeBacktestStore{games: games, odds: odds}, analysisCfg, tradingCfg,
		"test_sport", kickoff.AddDate(0, 0, -1), kickoff.AddDate(0, 1, 0), 1000)
	if err != nil {
		t.Fatalf("RunBacktest: %v", err)
	}

	if result.TotalBets == 0 {
		t.Fatal("expected at least one bet")
	}
	if result.Wins+result.Losses != result.TotalBets {
		t.Errorf("wins+losses (%d+%d) != total bets (%d)", result.Wins, result.Losses, result.TotalBets)
	}
	// Home always wins, and the increasingly strong record only ever favors
	// the home side more — every placed bet should have won.
	if result.Losses != 0 {
		t.Errorf("expected no losing bets, got %d", result.Losses)
	}
	if !almostEqual(result.FinalBankroll, 1000+result.TotalProfit) {
		t.Errorf("final bankroll %v != 1000 + profit %v", result.FinalBankroll, result.TotalProfit)
	}
	if result.TotalProfit <= 0 {
		t.Errorf("all-winning backtest should be profitable, got %v", result.TotalProfit)
	}
	if result.ModelType != "historical" {
		t.Errorf("result should carry model type, got %q", result.ModelType)
	}
}

func TestRunBacktestNoGames(t *testing.T) {
	analysisCfg, tradingCfg := backtestConfigs()
	_, err := RunBacktest(&fakeBacktestStore{}, analysisCfg, tradingCfg,
		"test_sport", time.Now().AddDate(0, -1, 0), time.Now(), 1000)
	if err == nil {
		t.Fatal("expected an error when no finished games are stored")
	}
}

func TestReplayStatsNoLookahead(t *testing.T) {
	stats := newReplayStats()

	// Before any results, everything is 0-0
	if w, l := stats.TeamRecord("Home", ""); w != 0 || l != 0 {
		t.Fatalf("fresh replay stats should be 0-0, got %d-%d", w, l)
	}

	game := types.Game{HomeTeam: "Home", AwayTeam: "Away", HomeScore: intp(21), AwayScore: intp(14)}
	stats.record(game)

	if w, l := stats.TeamRecord("Home", ""); w != 1 || l != 0 {
		t.Errorf("home team should be 1-0, got %d-%d", w, l)
	}
	if w, l := stats.TeamRecord("Away", ""); w != 0 || l != 1 {
		t.Errorf("away team should be 0-1, got %d-%d", w, l)
	}

	// Draws (no winner) change nothing
	draw := types.Game{HomeTeam: "Home", AwayTeam: "Away", HomeScore: intp(20), AwayScore: intp(20)}
	stats.record(draw)
	if w, l := stats.TeamRecord("Home", ""); w != 1 || l != 0 {
		t.Errorf("draw must not change records, got %d-%d", w, l)
	}
}
