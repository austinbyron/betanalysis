package analysis

import (
	"math"
	"testing"

	"github.com/austinbyron/betanalysis/internal/config"
	"github.com/austinbyron/betanalysis/pkg/types"
)

func TestEloSeedsRatingsFromPriors(t *testing.T) {
	// 12-4 prior (p=0.75) -> +191 Elo; 4-12 -> -191; unseeded stays at 1500
	priors := fakePriors{"Strong": {12, 4}, "Weak": {4, 12}}
	elo := NewElo(&fakeGames{}, 0).WithPriors(priors)

	game := types.Game{SportKey: "americanfootball_nfl", HomeTeam: "Strong", AwayTeam: "Weak"}
	home, _ := elo.EstimateProbabilities(game)
	want := EloProbability(EloInitial+191.0, EloInitial-191.0)
	if math.Abs(home-want) > 0.01 {
		t.Errorf("seeded home prob = %.3f, want ~%.3f", home, want)
	}

	even := types.Game{SportKey: "americanfootball_nfl", HomeTeam: "Nobody", AwayTeam: "Anyone"}
	h, _ := elo.EstimateProbabilities(even)
	if h0, _ := NewElo(&fakeGames{}, 0).EstimateProbabilities(even); h != h0 {
		t.Errorf("unseeded teams must match the unseeded estimator: %.3f vs %.3f", h, h0)
	}
}

func TestEloPriorsCountTowardWarmup(t *testing.T) {
	priors := fakePriors{"Seeded": {9, 7}} // 16 pseudo-games
	elo := NewElo(&fakeGames{}, 20).WithPriors(priors)

	both := types.Game{SportKey: "x", HomeTeam: "Seeded", AwayTeam: "Seeded"}
	if c := elo.Confidence(both); !almostEqual(c, 0.8) {
		t.Errorf("confidence = %.3f, want 0.8 (16/20)", c)
	}
	mixed := types.Game{SportKey: "x", HomeTeam: "Seeded", AwayTeam: "Cold"}
	if c := elo.Confidence(mixed); c != 0 {
		t.Errorf("lesser-known team gates confidence, got %.3f", c)
	}
}

func TestEloPriorSeedThenLearns(t *testing.T) {
	// A seeded rating is the starting point real results update from
	priors := fakePriors{"A": {12, 4}, "B": {4, 12}}
	games := &fakeGames{games: []types.Game{finished("g1", "B", "A", 30, 3, 1)}} // upset, B by 27 at home
	elo := NewElo(games, 0).WithPriors(priors)

	g := types.Game{SportKey: "baseball_mlb", HomeTeam: "A", AwayTeam: "B"}
	after, _ := elo.EstimateProbabilities(g)
	before, _ := NewElo(&fakeGames{}, 0).WithPriors(priors).EstimateProbabilities(g)
	if after >= before {
		t.Errorf("upset should pull A down: before %.3f after %.3f", before, after)
	}
	if after <= 0.5 {
		t.Errorf("one upset should not fully erase a +382 prior gap: %.3f", after)
	}
}

func TestNewEstimatorPassesPriorsToElo(t *testing.T) {
	priors := fakePriors{"Strong": {12, 4}, "Weak": {4, 12}}
	stats := WithPriors(fakeStats{}, priors)
	est, err := NewEstimator(config.AnalysisConfig{ModelType: "elo"}, stats, &fakeGames{})
	if err != nil {
		t.Fatal(err)
	}
	home, _ := est.EstimateProbabilities(types.Game{SportKey: "x", HomeTeam: "Strong", AwayTeam: "Weak"})
	if home < 0.85 {
		t.Errorf("elo built through NewEstimator should see priors, got home %.3f", home)
	}
}

func TestEloSeedRatingMath(t *testing.T) {
	if r := EloSeedRating(8, 8); r != EloInitial {
		t.Errorf(".500 prior = %v, want %v", r, EloInitial)
	}
	if r := EloSeedRating(0, 0); r != EloInitial {
		t.Errorf("empty prior = %v, want %v", r, EloInitial)
	}
	if r := EloSeedRating(12, 4); math.Abs(r-(EloInitial+190.85)) > 0.1 {
		t.Errorf("12-4 prior = %v, want ~1690.85", r)
	}
	// The seed inverts the probability curve exactly
	if p := EloProbability(EloSeedRating(12, 4), EloInitial+eloHomeAdvantage); math.Abs(p-0.75) > 1e-6 {
		t.Errorf("seed should round-trip to 0.75 (net of home advantage), got %.4f", p)
	}
}

func TestEloHistoryFromSeed(t *testing.T) {
	games := []types.Game{finished("g1", "A", "B", 5, 3, 1)}
	seed := func(team string) float64 {
		if team == "A" {
			return 1600
		}
		return EloInitial
	}
	hist := EloHistoryFrom(games, seed)
	if len(hist["A"]) != 1 || hist["A"][0].Rating <= 1600 {
		t.Errorf("A should start from 1600 and gain: %+v", hist["A"])
	}
	// Plain EloHistory is the unseeded special case
	plain := EloHistory(games)
	if plain["A"][0].Rating >= hist["A"][0].Rating {
		t.Errorf("seeded history should sit above unseeded: %v vs %v", hist["A"][0].Rating, plain["A"][0].Rating)
	}
}
