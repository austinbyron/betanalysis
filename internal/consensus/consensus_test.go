package consensus

import (
	"math"
	"testing"
	"time"

	"github.com/austinbyron/betanalysis/internal/analysis"
	"github.com/austinbyron/betanalysis/internal/config"
	"github.com/austinbyron/betanalysis/internal/contenders"
	"github.com/austinbyron/betanalysis/pkg/types"
)

func TestBuild(t *testing.T) {
	g := types.Game{ID: "g1", HomeTeam: "H", AwayTeam: "A"}
	pick := func(model, sel string, prob, ev float64) ModelPick {
		return ModelPick{Model: model, Selection: sel, Prob: prob, EV: ev, Odds: 2.0, Bookmaker: "dk"}
	}

	games := map[string]GamePicks{
		"g1": {Game: g, Picks: []ModelPick{ // 4/4 home, strong
			pick("a", "home", 0.60, 0.20), pick("b", "home", 0.58, 0.16),
			pick("c", "home", 0.62, 0.24), pick("d", "home", 0.55, 0.10),
		}},
		"g2": {Game: types.Game{ID: "g2"}, Picks: []ModelPick{ // 3/4 home, not strong
			pick("a", "home", 0.60, 0.20), pick("b", "home", 0.58, 0.16),
			pick("c", "home", 0.62, 0.24), pick("d", "away", 0.55, 0.10),
		}},
		"g3": {Game: types.Game{ID: "g3"}, Picks: []ModelPick{ // 2/4 votes, excluded
			pick("a", "home", 0.60, 0.20), pick("b", "home", 0.58, 0.16),
		}},
	}

	got := Build(games, 4, 0.05)
	if len(got) != 2 {
		t.Fatalf("consensus picks = %d, want 2", len(got))
	}
	if got[0].Game.ID != "g1" || got[0].Votes != 4 || !got[0].Strong {
		t.Errorf("g1 = %+v, want 4/4 strong", got[0])
	}
	if math.Abs(got[0].AvgProb-0.5875) > 1e-9 {
		t.Errorf("avg prob = %v, want 0.5875", got[0].AvgProb)
	}
	if got[1].Game.ID != "g2" || got[1].Votes != 3 || got[1].Strong {
		t.Errorf("g2 = %+v, want 3/4 not strong", got[1])
	}
	if got[1].Selection != "home" {
		t.Errorf("majority side = %q, want home", got[1].Selection)
	}
	if got[1].MinEV != 0.16 {
		t.Errorf("min EV = %v, want 0.16 (among agreeing picks)", got[1].MinEV)
	}
}

type fakeStore struct {
	games      []types.Game
	odds       map[string][]types.GameOdds
	portfolios map[string]*types.Portfolio
}

func (f *fakeStore) GetUpcomingGames(string) ([]types.Game, error)      { return f.games, nil }
func (f *fakeStore) GetOddsForGame(id string) ([]types.GameOdds, error) { return f.odds[id], nil }
func (f *fakeStore) GetPortfolio(id string) (*types.Portfolio, error)   { return f.portfolios[id], nil }

type fixedStats struct{}

func (fixedStats) TeamRecord(string, string) (int, int) { return 0, 0 }

func f64(v float64) *float64 { return &v }

func testContender(name string) contenders.Contender {
	return contenders.Contender{
		Name: name, Portfolio: name,
		Selector: analysis.NewSelector(analysis.NewHistorical(fixedStats{}), name, 0, 1.5, 0.05),
	}
}

func testConfig() *config.Config {
	return &config.Config{
		Trading: config.TradingConfig{
			InitialBankroll: 1000, MinStake: 1, MaxStakeFraction: 0.05,
			KellyFraction: 0.5, MinOdds: 1.5, MinExpectedValue: 0.05,
		},
		SportKeys: []string{"baseball_mlb"},
	}
}

func TestComputeFindsConsensus(t *testing.T) {
	// Three identical deterministic estimators must agree on the same side
	// of a fairly-priced game -> one 3/3 strong pick.
	store := &fakeStore{
		games: []types.Game{{ID: "g1", SportKey: "baseball_mlb", HomeTeam: "H", AwayTeam: "A",
			Status: "scheduled", CommenceTime: time.Now().UTC().Add(5 * time.Hour)}},
		odds: map[string][]types.GameOdds{
			"g1": {{GameID: "g1", Bookmaker: "dk", MarketType: types.MarketMoneyline,
				HomeOdds: f64(2.4), AwayOdds: f64(2.4), RetrievedAt: time.Now()}},
		},
		portfolios: map[string]*types.Portfolio{},
	}
	lineup := []contenders.Contender{testContender("a"), testContender("b"), testContender("c")}

	picks := Compute(store, lineup, testConfig())
	if len(picks) != 1 {
		t.Fatalf("picks = %d, want 1", len(picks))
	}
	p := picks[0]
	if p.Game.ID != "g1" || p.Votes != 3 || p.Total != 3 || !p.Strong {
		t.Errorf("pick = %+v, want 3/3 strong on g1", p)
	}
}

func TestComputeSkipsGamesWithoutOdds(t *testing.T) {
	store := &fakeStore{
		games: []types.Game{{ID: "g1", SportKey: "baseball_mlb", HomeTeam: "H", AwayTeam: "A",
			Status: "scheduled", CommenceTime: time.Now().UTC().Add(5 * time.Hour)}},
		odds:       map[string][]types.GameOdds{},
		portfolios: map[string]*types.Portfolio{},
	}
	lineup := []contenders.Contender{testContender("a"), testContender("b"), testContender("c")}
	if picks := Compute(store, lineup, testConfig()); len(picks) != 0 {
		t.Errorf("picks = %d, want 0 (no odds)", len(picks))
	}
}
