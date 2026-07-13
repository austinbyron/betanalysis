package analysis

import (
	"math"
	"testing"

	"github.com/austinbyron/betanalysis/internal/config"
	"github.com/austinbyron/betanalysis/pkg/types"
)

// fakeStats is a StatsProvider with fixed records
type fakeStats map[string][2]int

func (f fakeStats) TeamRecord(team, _ string) (int, int) {
	rec := f[team]
	return rec[0], rec[1]
}

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestKellyStake(t *testing.T) {
	tests := []struct {
		name                       string
		prob, odds, bankroll       float64
		kellyFraction, maxFraction float64
		want                       float64
	}{
		{"positive edge capped at max fraction", 0.6, 2.0, 1000, 0.5, 0.05, 50},
		{"positive edge uncapped", 0.6, 2.0, 1000, 0.5, 0.5, 100},
		{"no edge returns zero", 0.5, 2.0, 1000, 0.5, 0.05, 0},
		{"negative edge returns zero", 0.4, 2.0, 1000, 0.5, 0.05, 0},
		{"invalid odds returns zero", 0.9, 1.0, 1000, 0.5, 0.05, 0},
		{"zero bankroll returns zero", 0.6, 2.0, 0, 0.5, 0.05, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := KellyStake(tt.prob, tt.odds, tt.bankroll, tt.kellyFraction, tt.maxFraction)
			if !almostEqual(got, tt.want) {
				t.Errorf("KellyStake() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExpectedValue(t *testing.T) {
	// EV per unit staked is p*odds - 1: a coin flip at fair odds is zero EV
	if got := ExpectedValue(0.5, 2.0); !almostEqual(got, 0) {
		t.Errorf("ExpectedValue(0.5, 2.0) = %v, want 0", got)
	}
	if got := ExpectedValue(0.6, 2.0); !almostEqual(got, 0.2) {
		t.Errorf("ExpectedValue(0.6, 2.0) = %v, want 0.2", got)
	}
	if got := ExpectedValue(0.4, 2.0); !almostEqual(got, -0.2) {
		t.Errorf("ExpectedValue(0.4, 2.0) = %v, want -0.2", got)
	}
}

func TestDevig(t *testing.T) {
	// Symmetric juiced market devigs to 50/50
	home, away := Devig(1.91, 1.91)
	if !almostEqual(home, 0.5) || !almostEqual(away, 0.5) {
		t.Errorf("Devig(1.91, 1.91) = %v, %v, want 0.5, 0.5", home, away)
	}

	// Asymmetric market: implied 2/3 and 2/5 normalize to 5/8 and 3/8
	home, away = Devig(1.5, 2.5)
	if !almostEqual(home, 0.625) || !almostEqual(away, 0.375) {
		t.Errorf("Devig(1.5, 2.5) = %v, %v, want 0.625, 0.375", home, away)
	}
	if !almostEqual(home+away, 1.0) {
		t.Errorf("devigged probabilities must sum to 1, got %v", home+away)
	}
}

func TestSmoothedWinRate(t *testing.T) {
	stats := fakeStats{"Winners": {8, 2}}

	// Unknown team gets 0.5, not 0
	if got := smoothedWinRate(stats, "Unknown", "nfl"); !almostEqual(got, 0.5) {
		t.Errorf("smoothedWinRate(unknown) = %v, want 0.5", got)
	}

	// 8-2 team: (8+1)/(10+2) = 0.75
	if got := smoothedWinRate(stats, "Winners", "nfl"); !almostEqual(got, 0.75) {
		t.Errorf("smoothedWinRate(8-2) = %v, want 0.75", got)
	}
}

func TestHistoricalEstimator(t *testing.T) {
	stats := fakeStats{"Home": {8, 2}, "Away": {2, 8}}
	h := NewHistorical(stats)

	game := types.Game{HomeTeam: "Home", AwayTeam: "Away"}
	homeProb, awayProb := h.EstimateProbabilities(game)

	if !almostEqual(homeProb+awayProb, 1.0) {
		t.Fatalf("probabilities must sum to 1, got %v", homeProb+awayProb)
	}
	if homeProb <= awayProb {
		t.Errorf("8-2 home team should be favored over 2-8 away: home %v away %v", homeProb, awayProb)
	}
	// Shrinkage keeps it away from the raw 0.75 rate
	if homeProb > 0.72 {
		t.Errorf("shrinkage should pull home prob toward 0.5, got %v", homeProb)
	}
}

func TestThompsonSamplingFavorsStrongTeam(t *testing.T) {
	stats := fakeStats{"Strong": {90, 10}, "Weak": {10, 90}}
	ts := NewThompsonSampling(config.AnalysisConfig{ThompsonAlphaPrior: 1, ThompsonBetaPrior: 1}, stats)

	game := types.Game{HomeTeam: "Strong", AwayTeam: "Weak"}
	sum := 0.0
	const n = 500
	for i := 0; i < n; i++ {
		homeProb, awayProb := ts.EstimateProbabilities(game)
		if homeProb < 0 || homeProb > 1 || !almostEqual(homeProb+awayProb, 1.0) {
			t.Fatalf("invalid probability pair: %v, %v", homeProb, awayProb)
		}
		sum += homeProb
	}

	if avg := sum / n; avg < 0.7 {
		t.Errorf("90-10 team should average well above 0.5 vs 10-90 team, got %v", avg)
	}
}

func TestEpsilonGreedyDeterministicWithoutExploration(t *testing.T) {
	stats := fakeStats{"Home": {6, 4}, "Away": {4, 6}}
	e := NewEpsilonGreedy(config.AnalysisConfig{Epsilon: 0}, stats)

	game := types.Game{HomeTeam: "Home", AwayTeam: "Away"}
	h1, a1 := e.EstimateProbabilities(game)
	h2, a2 := e.EstimateProbabilities(game)

	if !almostEqual(h1, h2) || !almostEqual(a1, a2) {
		t.Errorf("epsilon=0 must be deterministic: got (%v,%v) then (%v,%v)", h1, a1, h2, a2)
	}
	if h1 <= a1 {
		t.Errorf("6-4 team should be favored: home %v away %v", h1, a1)
	}
}

func TestNewEstimator(t *testing.T) {
	stats := fakeStats{}

	for _, modelType := range []string{"", "thompson", "epsilon_greedy", "historical"} {
		if _, err := NewEstimator(config.AnalysisConfig{ModelType: modelType}, stats, nil); err != nil {
			t.Errorf("NewEstimator(%q) unexpected error: %v", modelType, err)
		}
	}

	if _, err := NewEstimator(config.AnalysisConfig{ModelType: "bogus"}, stats, nil); err == nil {
		t.Error("NewEstimator(bogus) should error")
	}
}

func f64(v float64) *float64 { return &v }

func TestSelectorPicksBestOddsAcrossBookmakers(t *testing.T) {
	stats := fakeStats{"Home": {9, 1}, "Away": {1, 9}}
	// marketWeight 0 isolates the model probability for a deterministic test
	selector := NewSelector(NewHistorical(stats), "", 0, 1.5, 0.05)

	game := types.Game{ID: "g1", HomeTeam: "Home", AwayTeam: "Away"}
	odds := []types.GameOdds{
		{GameID: "g1", Bookmaker: "book_a", MarketType: types.MarketMoneyline, HomeOdds: f64(1.6), AwayOdds: f64(2.4)},
		{GameID: "g1", Bookmaker: "book_b", MarketType: types.MarketMoneyline, HomeOdds: f64(1.7), AwayOdds: f64(2.3)},
		// Non-moneyline rows must be ignored
		{GameID: "g1", Bookmaker: "book_b", MarketType: types.MarketSpread, HomeOdds: f64(5.0), AwayOdds: f64(5.0)},
	}

	bet := selector.RecommendBet(game, odds)
	if bet == nil {
		t.Fatal("expected a recommendation for a heavy favorite at generous odds")
	}
	if bet.Selection != types.OutcomeHome {
		t.Errorf("expected home selection, got %s", bet.Selection)
	}
	if bet.Bookmaker != "book_b" {
		t.Errorf("expected best-price bookmaker book_b, got %s", bet.Bookmaker)
	}
	if bet.Probability <= 0.5 || bet.Probability >= 1 {
		t.Errorf("bet must carry the blended probability, got %v", bet.Probability)
	}
	if wantEV := bet.Probability*bet.Odds - 1; !almostEqual(bet.ExpectedValue, wantEV) {
		t.Errorf("EV %v inconsistent with probability and odds (want %v)", bet.ExpectedValue, wantEV)
	}
	if bet.ModelID != "historical" {
		t.Errorf("bet should record the estimator name, got %q", bet.ModelID)
	}
}

func TestSelectorRespectsThresholds(t *testing.T) {
	stats := fakeStats{}

	// With full market weight, blended probability equals the devigged
	// market probability, so EV is always negative after the vig — no bet.
	selector := NewSelector(NewHistorical(stats), "", 1.0, 1.5, 0.05)
	game := types.Game{ID: "g1", HomeTeam: "Home", AwayTeam: "Away"}
	odds := []types.GameOdds{
		{GameID: "g1", Bookmaker: "book_a", MarketType: types.MarketMoneyline, HomeOdds: f64(1.91), AwayOdds: f64(1.91)},
	}
	if bet := selector.RecommendBet(game, odds); bet != nil {
		t.Errorf("pure market probabilities can never clear a positive EV threshold, got bet %+v", bet)
	}

	// Odds below minOdds are skipped even at huge model edges
	strong := fakeStats{"Home": {99, 1}, "Away": {1, 99}}
	selector = NewSelector(NewHistorical(strong), "", 0, 3.0, 0.05)
	odds = []types.GameOdds{
		{GameID: "g1", Bookmaker: "book_a", MarketType: types.MarketMoneyline, HomeOdds: f64(2.0), AwayOdds: f64(2.0)},
	}
	if bet := selector.RecommendBet(game, odds); bet != nil {
		t.Errorf("odds below minOdds must be skipped, got bet %+v", bet)
	}
}

func TestSharpeRatio(t *testing.T) {
	if got := SharpeRatio([]float64{0.1}, 0); got != 0 {
		t.Errorf("SharpeRatio with <2 returns = %v, want 0", got)
	}
	if got := SharpeRatio([]float64{0.1, 0.1, 0.1}, 0); got != 0 {
		t.Errorf("SharpeRatio with zero variance = %v, want 0", got)
	}
	if got := SharpeRatio([]float64{0.2, -0.1, 0.2, -0.1}, 0); got <= 0 {
		t.Errorf("SharpeRatio of positive-mean returns = %v, want > 0", got)
	}
}

func TestSelectorStampsConfiguredModelID(t *testing.T) {
	stats := fakeStats{"Home": {9, 1}, "Away": {1, 9}}
	game := types.Game{ID: "g1", HomeTeam: "Home", AwayTeam: "Away"}
	odds := []types.GameOdds{
		{GameID: "g1", Bookmaker: "book", MarketType: types.MarketMoneyline, HomeOdds: f64(1.7), AwayOdds: f64(2.3)},
	}

	named := NewSelector(NewHistorical(stats), "thompson-raw", 0, 1.5, 0.05)
	bet := named.RecommendBet(game, odds)
	if bet == nil {
		t.Fatal("expected a recommendation")
	}
	if bet.ModelID != "thompson-raw" {
		t.Errorf("ModelID = %q, want thompson-raw", bet.ModelID)
	}

	unnamed := NewSelector(NewHistorical(stats), "", 0, 1.5, 0.05)
	if bet := unnamed.RecommendBet(game, odds); bet == nil || bet.ModelID != "historical" {
		t.Errorf("empty modelID must fall back to estimator name, got %+v", bet)
	}
}

func TestWarmupCollapsesBlendToMarket(t *testing.T) {
	stats := fakeStats{} // every team 0-0
	game := types.Game{ID: "g1", HomeTeam: "Fav", AwayTeam: "Dog"}
	// Fair-ish line: devigged 66.7%/33.3%. An uninformed 50/50 model
	// blended at 30% inflates the dog to 38.3% → +15% EV → dog bet.
	odds := []types.GameOdds{
		{GameID: "g1", Bookmaker: "book", MarketType: types.MarketMoneyline, HomeOdds: f64(1.5), AwayOdds: f64(3.0)},
	}

	unguarded := NewSelector(NewHistorical(stats), "", 0.7, 1.5, 0.05)
	if bet := unguarded.RecommendBet(game, odds); bet == nil || bet.Selection != types.OutcomeAway {
		t.Fatalf("premise: without warmup the cold model must bet the dog, got %+v", bet)
	}

	guarded := NewHistorical(stats)
	guarded.warmupGames = 20
	if bet := NewSelector(guarded, "", 0.7, 1.5, 0.05).RecommendBet(game, odds); bet != nil {
		t.Errorf("zero-record teams must yield no bet under warmup, got %+v", bet)
	}
}

func TestRecommendBothPreviewsSuppressedBet(t *testing.T) {
	stats := fakeStats{} // every team 0-0
	game := types.Game{ID: "g1", HomeTeam: "Fav", AwayTeam: "Dog"}
	odds := []types.GameOdds{
		{GameID: "g1", Bookmaker: "book", MarketType: types.MarketMoneyline, HomeOdds: f64(1.5), AwayOdds: f64(3.0)},
	}

	guarded := NewHistorical(stats)
	guarded.warmupGames = 20
	sel := NewSelector(guarded, "hist", 0.7, 1.5, 0.05)

	bet, preview := sel.RecommendBoth(game, odds)
	if bet != nil {
		t.Fatalf("warmup must suppress the bet, got %+v", bet)
	}
	if preview == nil || preview.Selection != types.OutcomeAway {
		t.Fatalf("preview must carry the full-confidence pick, got %+v", preview)
	}
	if preview.ModelID != "hist" {
		t.Errorf("preview model = %q, want hist", preview.ModelID)
	}

	// Past warmup the bet clears and there is nothing to preview.
	warmed := NewSelector(NewHistorical(stats), "hist", 0.7, 1.5, 0.05)
	if bet, preview := warmed.RecommendBoth(game, odds); bet == nil || preview != nil {
		t.Errorf("full confidence must return (bet, nil), got %v %v", bet, preview)
	}

	// When even the full-confidence blend has no edge, neither side exists.
	strict := NewHistorical(stats)
	strict.warmupGames = 20
	if bet, preview := NewSelector(strict, "hist", 0.7, 1.5, 5.0).RecommendBoth(game, odds); bet != nil || preview != nil {
		t.Errorf("no edge must mean no preview either, got %v %v", bet, preview)
	}
}

func TestConfidenceRampsWithGamesPlayed(t *testing.T) {
	stats := fakeStats{"Ready": {15, 10}, "Half": {5, 5}, "Cold": {0, 0}}

	h := NewHistorical(stats)
	h.warmupGames = 20

	conf := func(home, away string) float64 {
		return h.Confidence(types.Game{HomeTeam: home, AwayTeam: away})
	}
	if got := conf("Ready", "Cold"); got != 0 {
		t.Errorf("confidence = %v, want 0 (limited by the colder team)", got)
	}
	if got := conf("Ready", "Half"); got != 0.5 {
		t.Errorf("confidence = %v, want 0.5 (10 of 20 games)", got)
	}
	if got := conf("Ready", "Ready"); got != 1 {
		t.Errorf("confidence = %v, want 1 (25 games, past warmup)", got)
	}

	disabled := NewHistorical(stats)
	if got := disabled.Confidence(types.Game{HomeTeam: "Cold", AwayTeam: "Cold"}); got != 1 {
		t.Errorf("warmup disabled must mean full confidence, got %v", got)
	}
}

func TestFullConfidenceReproducesBlendExactly(t *testing.T) {
	stats := fakeStats{"Home": {30, 10}, "Away": {10, 30}}
	game := types.Game{ID: "g1", HomeTeam: "Home", AwayTeam: "Away"}
	odds := []types.GameOdds{
		{GameID: "g1", Bookmaker: "book", MarketType: types.MarketMoneyline, HomeOdds: f64(1.7), AwayOdds: f64(2.3)},
	}

	plain := NewHistorical(stats)
	warmed := NewHistorical(stats)
	warmed.warmupGames = 20 // both teams have 40 games → confidence 1

	a := NewSelector(plain, "", 0.7, 1.5, 0.01).RecommendBet(game, odds)
	b := NewSelector(warmed, "", 0.7, 1.5, 0.01).RecommendBet(game, odds)
	if a == nil || b == nil {
		t.Fatalf("expected recommendations, got %v %v", a, b)
	}
	if a.Probability != b.Probability || a.Selection != b.Selection {
		t.Errorf("full confidence must not change the blend: %+v vs %+v", a, b)
	}
}

func TestNewEstimatorWiresWarmup(t *testing.T) {
	stats := fakeStats{}
	est, err := NewEstimator(config.AnalysisConfig{ModelType: "historical", WarmupGames: 20}, stats, nil)
	if err != nil {
		t.Fatalf("NewEstimator: %v", err)
	}
	if got := est.Confidence(types.Game{HomeTeam: "A", AwayTeam: "B"}); got != 0 {
		t.Errorf("confidence = %v, want 0 for unseen teams with warmup from config", got)
	}
}

// fakePriors is a PriorsProvider with fixed pseudo-counts
type fakePriors map[string][2]float64

func (f fakePriors) TeamPrior(team, _ string) (float64, float64) {
	p := f[team]
	return p[0], p[1]
}

func TestWithPriorsAddsPseudoCounts(t *testing.T) {
	stats := fakeStats{"Seeded": {2, 1}}
	priors := fakePriors{"Seeded": {9.12, 6.88}}

	s := WithPriors(stats, priors)
	if w, l := s.TeamRecord("Seeded", "x"); w != 11 || l != 8 {
		t.Errorf("record = %d-%d, want 11-8 (real + rounded priors)", w, l)
	}
	if w, l := s.TeamRecord("Unseeded", "x"); w != 0 || l != 0 {
		t.Errorf("unseeded record = %d-%d, want 0-0", w, l)
	}
}

func TestPriorsRaiseWarmupConfidence(t *testing.T) {
	stats := fakeStats{} // every team 0-0
	priors := fakePriors{"A": {8, 8}, "B": {8, 8}}

	h := NewHistorical(WithPriors(stats, priors))
	h.warmupGames = 32
	if got := h.Confidence(types.Game{HomeTeam: "A", AwayTeam: "B"}); got != 0.5 {
		t.Errorf("confidence = %v, want 0.5 — 16 seeded pseudo-games of 32 warmup", got)
	}
	if got := h.Confidence(types.Game{HomeTeam: "A", AwayTeam: "Cold"}); got != 0 {
		t.Errorf("confidence = %v, want 0 — limited by the unseeded team", got)
	}
}
