package trading

import (
	"math"
	"testing"
	"time"

	"github.com/austinbyron/betanalysis/internal/espn"
	"github.com/austinbyron/betanalysis/pkg/types"
)

// fakeResults maps game IDs to canned ESPN results
type fakeResults struct {
	res map[string]espn.Result
}

func (f fakeResults) GameResult(g types.Game) (espn.Result, bool) {
	r, ok := f.res[g.ID]
	return r, ok
}

func (f *fakeStore) GetStaleScheduledGames(before time.Time) ([]types.Game, error) {
	var stale []types.Game
	for _, g := range f.games {
		if g.Status != "scheduled" || !g.CommenceTime.Before(before) {
			continue
		}
		referenced := false
		for _, b := range f.bets {
			if b.GameID == g.ID && b.Status == types.BetStatusPending {
				referenced = true
			}
		}
		for _, pb := range f.previews {
			if pb.GameID == g.ID && pb.Status == types.BetStatusPending {
				referenced = true
			}
		}
		if referenced {
			stale = append(stale, g)
		}
	}
	return stale, nil
}

func (f *fakeStore) UpdateGameScores(gameID string, homeScore, awayScore int, status string) error {
	for i := range f.games {
		if f.games[i].ID == gameID {
			f.games[i].HomeScore = &homeScore
			f.games[i].AwayScore = &awayScore
			f.games[i].Status = status
		}
	}
	return nil
}

// staleGame is a game the odds feed abandoned: still 'scheduled' long
// after its commence time.
func staleGame(id string, age time.Duration) types.Game {
	return types.Game{
		ID: id, SportKey: "baseball_mlb",
		HomeTeam: "Baltimore Orioles", AwayTeam: "Chicago Cubs",
		CommenceTime: time.Now().Add(-age),
		Status:       "scheduled",
	}
}

func reconcilerFixture(game types.Game) *fakeStore {
	store := newFakeStore()
	store.games = []types.Game{game}
	store.portfolios["p1"] = types.Portfolio{ID: "p1", Balance: 1000}
	store.bets = []types.Bet{{
		ID: "b1", PortfolioID: "p1", GameID: game.ID,
		Selection: types.OutcomeAway, Odds: 2.14,
		Stake: 20, PotentialWin: 22.8,
		Status: types.BetStatusPending,
	}}
	store.previews = []types.PreviewBet{{
		ID: "pv1", ModelID: "m1", GameID: game.ID,
		Selection: types.OutcomeAway, Odds: 2.14, Stake: 10,
		Status: types.BetStatusPending,
	}}
	return store
}

func TestReconcileSettlesAgainstESPNFinal(t *testing.T) {
	game := staleGame("g-stale", 48*time.Hour)
	store := reconcilerFixture(game)

	// Real game finished 3-2 home — the away bet lost
	results := fakeResults{res: map[string]espn.Result{
		"g-stale": {Status: espn.ResultFinal, HomeScore: 3, AwayScore: 2},
	}}

	if err := NewReconciler(store, results).ReconcileStaleBets(); err != nil {
		t.Fatal(err)
	}

	bet := store.bets[0]
	if bet.Status != types.BetStatusLost {
		t.Errorf("bet status = %q, want lost", bet.Status)
	}
	if bet.ActualWin == nil || *bet.ActualWin != 0 {
		t.Errorf("ActualWin = %v, want 0", bet.ActualWin)
	}
	p := store.portfolios["p1"]
	if p.ActiveBetsCount != -1 || p.ActiveBetsValue != -20 {
		t.Errorf("active bets delta = %d/%v, want -1/-20", p.ActiveBetsCount, p.ActiveBetsValue)
	}
	if p.BetsLost != 1 || p.BetsWon != 0 {
		t.Errorf("record delta = %d-%d, want 0-1", p.BetsWon, p.BetsLost)
	}

	// The game must NOT become 'finished' — the real result already lives
	// under the replacement event id and estimators replay finished games.
	g := store.games[0]
	if g.Status != "superseded" {
		t.Errorf("game status = %q, want superseded", g.Status)
	}
	if g.HomeScore == nil || *g.HomeScore != 3 || g.AwayScore == nil || *g.AwayScore != 2 {
		t.Errorf("game scores = %v-%v, want 3-2", g.HomeScore, g.AwayScore)
	}

	pv := store.previews[0]
	if pv.Status != types.BetStatusLost {
		t.Errorf("preview status = %q, want lost", pv.Status)
	}
	if pv.Payout == nil || *pv.Payout != 0 {
		t.Errorf("preview payout = %v, want 0", pv.Payout)
	}
}

func TestReconcileSettlesWinAgainstESPNFinal(t *testing.T) {
	game := staleGame("g-stale", 48*time.Hour)
	store := reconcilerFixture(game)

	results := fakeResults{res: map[string]espn.Result{
		"g-stale": {Status: espn.ResultFinal, HomeScore: 2, AwayScore: 9},
	}}

	if err := NewReconciler(store, results).ReconcileStaleBets(); err != nil {
		t.Fatal(err)
	}

	bet := store.bets[0]
	if bet.Status != types.BetStatusWon {
		t.Errorf("bet status = %q, want won", bet.Status)
	}
	if bet.ActualWin == nil || *bet.ActualWin != 42.8 {
		t.Errorf("ActualWin = %v, want 42.8 (stake+potential win)", bet.ActualWin)
	}
	p := store.portfolios["p1"]
	if p.Balance != 1042.8 {
		t.Errorf("balance = %v, want 1042.8", p.Balance)
	}
	if pv := store.previews[0]; pv.Payout == nil || math.Abs(*pv.Payout-21.4) > 1e-9 {
		t.Errorf("preview payout = %v, want 21.4 (stake*odds)", pv.Payout)
	}
}

func TestReconcileVoidsPostponedGame(t *testing.T) {
	game := staleGame("g-ppd", 24*time.Hour)
	store := reconcilerFixture(game)

	results := fakeResults{res: map[string]espn.Result{
		"g-ppd": {Status: espn.ResultPostponed},
	}}

	if err := NewReconciler(store, results).ReconcileStaleBets(); err != nil {
		t.Fatal(err)
	}

	bet := store.bets[0]
	if bet.Status != types.BetStatusVoid {
		t.Errorf("bet status = %q, want void", bet.Status)
	}
	if bet.ActualWin == nil || *bet.ActualWin != 20 {
		t.Errorf("ActualWin = %v, want 20 (stake refund)", bet.ActualWin)
	}
	p := store.portfolios["p1"]
	if p.Balance != 1020 {
		t.Errorf("balance = %v, want 1020 (stake back)", p.Balance)
	}
	if p.TotalWagered != -20 {
		t.Errorf("total wagered delta = %v, want -20", p.TotalWagered)
	}
	if p.BetsWon != 0 || p.BetsLost != 0 {
		t.Errorf("void must not touch the record, got %d-%d", p.BetsWon, p.BetsLost)
	}
	if g := store.games[0]; g.Status != "postponed" {
		t.Errorf("game status = %q, want postponed", g.Status)
	}
	if pv := store.previews[0]; pv.Status != types.BetStatusVoid {
		t.Errorf("preview status = %q, want void", pv.Status)
	}
}

func TestReconcileLeavesUnmatchedYoungGameAlone(t *testing.T) {
	game := staleGame("g-unknown", 24*time.Hour)
	store := reconcilerFixture(game)

	if err := NewReconciler(store, fakeResults{}).ReconcileStaleBets(); err != nil {
		t.Fatal(err)
	}

	if bet := store.bets[0]; bet.Status != types.BetStatusPending {
		t.Errorf("bet status = %q, want still pending", bet.Status)
	}
	if g := store.games[0]; g.Status != "scheduled" {
		t.Errorf("game status = %q, want still scheduled", g.Status)
	}
}

func TestReconcileVoidsVanishedGameAfterCutoff(t *testing.T) {
	game := staleGame("g-vanished", 96*time.Hour)
	store := reconcilerFixture(game)

	if err := NewReconciler(store, fakeResults{}).ReconcileStaleBets(); err != nil {
		t.Fatal(err)
	}

	if bet := store.bets[0]; bet.Status != types.BetStatusVoid {
		t.Errorf("bet status = %q, want void", bet.Status)
	}
	if g := store.games[0]; g.Status != "postponed" {
		t.Errorf("game status = %q, want postponed", g.Status)
	}
}

func TestReconcileLeavesInProgressGameAlone(t *testing.T) {
	game := staleGame("g-live", 24*time.Hour)
	store := reconcilerFixture(game)

	results := fakeResults{res: map[string]espn.Result{
		"g-live": {Status: espn.ResultPending, HomeScore: 1, AwayScore: 0},
	}}

	if err := NewReconciler(store, results).ReconcileStaleBets(); err != nil {
		t.Fatal(err)
	}

	if bet := store.bets[0]; bet.Status != types.BetStatusPending {
		t.Errorf("bet status = %q, want still pending", bet.Status)
	}
}
