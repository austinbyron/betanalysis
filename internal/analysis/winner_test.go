package analysis

import (
	"testing"

	"github.com/austinbyron/betanalysis/internal/config"
	"github.com/austinbyron/betanalysis/pkg/types"
)

func moneyline(book string, home, away float64) types.GameOdds {
	return types.GameOdds{GameID: "g1", Bookmaker: book, MarketType: types.MarketMoneyline, HomeOdds: f64(home), AwayOdds: f64(away)}
}

func TestWinnerStrategyBacksFavoriteWithNegativeEV(t *testing.T) {
	// A modest favorite the market prices fairly: no EV edge anywhere, so
	// the EV strategy passes, but the winner strategy backs the likely winner.
	stats := fakeStats{"Home": {12, 8}, "Away": {8, 12}}
	game := types.Game{ID: "g1", HomeTeam: "Home", AwayTeam: "Away"}
	odds := []types.GameOdds{
		moneyline("book_a", 1.55, 2.50),
		moneyline("book_b", 1.60, 2.40), // best home price
	}

	ev := NewSelector(NewHistorical(stats), "", 0.7, 1.5, 0.05)
	if bet := ev.RecommendBet(game, odds); bet != nil {
		t.Fatalf("EV strategy should pass on a fairly priced favorite, got %+v", bet)
	}

	winner := NewSelector(NewHistorical(stats), "", 0.7, 1.5, 0.05).WithWinnerStrategy(0.55, 0.02)
	bet := winner.RecommendBet(game, odds)
	if bet == nil {
		t.Fatal("winner strategy should back the favorite")
	}
	if bet.Selection != types.OutcomeHome {
		t.Errorf("selection = %s, want home", bet.Selection)
	}
	if bet.Bookmaker != "book_b" || bet.Odds != 1.60 {
		t.Errorf("want best home price at book_b 1.60, got %s %.2f", bet.Bookmaker, bet.Odds)
	}
	if bet.Probability < 0.55 {
		t.Errorf("probability %.3f should clear the floor", bet.Probability)
	}
	if wantEV := bet.Probability*bet.Odds - 1; !almostEqual(bet.ExpectedValue, wantEV) {
		t.Errorf("EV %v should still be reported for display (want %v)", bet.ExpectedValue, wantEV)
	}
}

func TestWinnerStrategyBacksAwayFavoriteToo(t *testing.T) {
	stats := fakeStats{"Home": {5, 15}, "Away": {15, 5}}
	game := types.Game{ID: "g1", HomeTeam: "Home", AwayTeam: "Away"}
	odds := []types.GameOdds{moneyline("book_a", 2.60, 1.50)}

	winner := NewSelector(NewHistorical(stats), "", 0.7, 1.5, 0.05).WithWinnerStrategy(0.55, 0.02)
	bet := winner.RecommendBet(game, odds)
	if bet == nil || bet.Selection != types.OutcomeAway {
		t.Fatalf("want away favorite, got %+v", bet)
	}
}

func TestWinnerStrategyRespectsProbabilityFloor(t *testing.T) {
	// Coin flip: neither side clears 55%
	stats := fakeStats{"Home": {10, 10}, "Away": {10, 10}}
	game := types.Game{ID: "g1", HomeTeam: "Home", AwayTeam: "Away"}
	odds := []types.GameOdds{moneyline("book_a", 1.95, 1.95)}

	winner := NewSelector(NewHistorical(stats), "", 0.7, 1.5, 0.05).WithWinnerStrategy(0.55, 0.02)
	if bet := winner.RecommendBet(game, odds); bet != nil {
		t.Errorf("coin flip should not be bet, got %+v", bet)
	}
	// ...but a 50% floor takes it (floor is inclusive)
	loose := NewSelector(NewHistorical(stats), "", 0.7, 1.5, 0.05).WithWinnerStrategy(0.5, 0.02)
	if bet := loose.RecommendBet(game, odds); bet == nil {
		t.Error("expected a bet when the floor is below the blended probability")
	}
}

func TestWinnerStrategyIgnoresMinOdds(t *testing.T) {
	// Heavy favorite at 1.25 — below the EV strategy's min_odds gate
	stats := fakeStats{"Home": {18, 2}, "Away": {2, 18}}
	game := types.Game{ID: "g1", HomeTeam: "Home", AwayTeam: "Away"}
	odds := []types.GameOdds{moneyline("book_a", 1.25, 4.00)}

	winner := NewSelector(NewHistorical(stats), "", 0.7, 1.5, 0.05).WithWinnerStrategy(0.55, 0.02)
	if bet := winner.RecommendBet(game, odds); bet == nil || bet.Odds != 1.25 {
		t.Errorf("winner strategy should take the short price, got %+v", bet)
	}
}

func TestStakeUsesFlatFractionForWinnerAndKellyForEV(t *testing.T) {
	cfg := config.TradingConfig{KellyFraction: 0.5, MaxStakeFraction: 0.05, MinStake: 1}
	bet := &types.Bet{Probability: 0.6, Odds: 1.6} // negative EV: Kelly says 0

	ev := NewSelector(NewHistorical(fakeStats{}), "", 0.7, 1.5, 0.05)
	if got := ev.Stake(bet, 1000, cfg); got != 0 {
		t.Errorf("EV stake = %v, want Kelly 0 on negative edge", got)
	}
	if got, want := ev.Stake(&types.Bet{Probability: 0.6, Odds: 2.0}, 1000, cfg), KellyStake(0.6, 2.0, 1000, 0.5, 0.05); got != want {
		t.Errorf("EV stake = %v, want Kelly %v", got, want)
	}

	winner := NewSelector(NewHistorical(fakeStats{}), "", 0.7, 1.5, 0.05).WithWinnerStrategy(0.55, 0.02)
	if got := winner.Stake(bet, 1000, cfg); got != 20 {
		t.Errorf("winner stake = %v, want flat 2%% of 1000", got)
	}
	// Flat fraction is still capped by max_stake_fraction
	capped := NewSelector(NewHistorical(fakeStats{}), "", 0.7, 1.5, 0.05).WithWinnerStrategy(0.55, 0.10)
	if got := capped.Stake(bet, 1000, cfg); got != 50 {
		t.Errorf("winner stake = %v, want capped at 5%%", got)
	}
	if got := winner.Stake(bet, 0, cfg); got != 0 {
		t.Errorf("empty bankroll stake = %v, want 0", got)
	}
}

func TestSelectorStrategyAccessor(t *testing.T) {
	ev := NewSelector(NewHistorical(fakeStats{}), "", 0.7, 1.5, 0.05)
	if ev.Strategy() != StrategyEV {
		t.Errorf("default strategy = %q, want %q", ev.Strategy(), StrategyEV)
	}
	w := ev.WithWinnerStrategy(0.55, 0.02)
	if w.Strategy() != StrategyWinner {
		t.Errorf("strategy = %q, want %q", w.Strategy(), StrategyWinner)
	}
}

func TestHomeFieldAdjusterShiftsTowardHome(t *testing.T) {
	adj := NewHomeFieldAdjuster(0.03)
	if adj.Name() != "home_field" {
		t.Errorf("name = %q", adj.Name())
	}
	home, away := adj.Adjust(types.Game{}, 0.50, 0.50)
	if !almostEqual(home, 0.53) || !almostEqual(away, 0.47) {
		t.Errorf("got %.3f/%.3f, want 0.530/0.470", home, away)
	}
	// Stays a valid distribution at the edges
	home, away = adj.Adjust(types.Game{}, 0.99, 0.01)
	if home <= 0 || home >= 1 || !almostEqual(home+away, 1) {
		t.Errorf("edge case produced %.3f/%.3f", home, away)
	}
	// Composes with the adjuster stack
	est := WithAdjusters(NewHistorical(fakeStats{"H": {10, 10}, "A": {10, 10}}), adj)
	h, _ := est.EstimateProbabilities(types.Game{HomeTeam: "H", AwayTeam: "A"})
	if !almostEqual(h, 0.53) {
		t.Errorf("adjusted home prob = %.3f, want 0.530", h)
	}
}

func TestMaxOddsSkipsLongshots(t *testing.T) {
	// A wildly optimistic record on the away dog makes 9.0 look like a huge
	// edge — the cap must pass on it rather than chase the longshot.
	stats := fakeStats{"Home": {2, 18}, "Away": {18, 2}}
	game := types.Game{ID: "g1", HomeTeam: "Home", AwayTeam: "Away"}
	odds := []types.GameOdds{moneyline("book_a", 1.08, 9.00)}

	uncapped := NewSelector(NewHistorical(stats), "", 0.7, 1.5, 0.05)
	if bet := uncapped.RecommendBet(game, odds); bet == nil || bet.Odds != 9.00 {
		t.Fatalf("uncapped selector should take the longshot, got %+v", bet)
	}
	capped := NewSelector(NewHistorical(stats), "", 0.7, 1.5, 0.05).WithMaxOdds(4.0)
	if bet := capped.RecommendBet(game, odds); bet != nil {
		t.Errorf("odds above max_odds must be skipped, got %+v", bet)
	}
	// At or under the cap the bet still goes through
	if bet := capped.RecommendBet(game, []types.GameOdds{moneyline("book_a", 1.30, 4.00)}); bet == nil || bet.Odds != 4.00 {
		t.Errorf("odds at max_odds should be allowed, got %+v", bet)
	}
}
