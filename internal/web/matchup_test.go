package web

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/austinbyron/betanalysis/internal/contenders"
	"github.com/austinbyron/betanalysis/pkg/types"
)

func matchupFixture() (*fakeStore, []contenders.Contender) {
	hs, as := 5, 3
	store := analysisFixture()
	store.finished = map[string][]types.Game{
		"baseball_mlb": {
			{HomeTeam: "Padres", AwayTeam: "Dodgers", SportKey: "baseball_mlb", Status: "finished",
				CommenceTime: time.Now().Add(-48 * time.Hour), HomeScore: &hs, AwayScore: &as},
		},
	}
	store.settled = []types.Bet{{
		ID: "b1", PortfolioID: "default", GameID: "g1", Selection: "home",
		Odds: 2.0, Stake: 10, Probability: 0.6, ModelID: "historical",
		Status: types.BetStatusWon, ActualWin: f64(20), CreatedAt: time.Now().Add(-24 * time.Hour),
	}}
	return store, strengthLineup()
}

func TestMatchupPageRenders(t *testing.T) {
	store, lineup := matchupFixture()
	srv := newTestServerWithLineupAndStats(t, store, lineup, recordStats{
		"Padres": {30, 10}, "Dodgers": {10, 30},
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/analysis/game/g1", nil))
	body := rec.Body.String()

	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	for _, want := range []string{
		"Dodgers @ Padres", // header
		"draftkings",       // odds table
		"historical",       // model board row
		"beta-duel",        // posterior section rendered (stats provided)
		"elo-panel",        // finished games exist
		"Won",              // settled bet listed
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in matchup page", want)
		}
	}
}

func TestMatchupPageUnknownGame404s(t *testing.T) {
	store, lineup := matchupFixture()
	srv := newTestServerWithLineup(t, store, lineup)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/analysis/game/nope", nil))
	if rec.Code != 404 {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "/analysis") {
		t.Fatal("404 page should link back to /analysis")
	}
}

func TestBoardRowDeterministicPick(t *testing.T) {
	store, lineup := matchupFixture()
	srv := newTestServerWithLineupAndStats(t, store, lineup, recordStats{
		"Padres": {30, 10}, "Dodgers": {10, 30},
	})
	game, _ := store.GetGameByID("g1")
	d1 := srv.buildMatchup(*game)
	d2 := srv.buildMatchup(*game)
	if len(d1.Board) != 1 {
		t.Fatalf("one contender, got %d rows", len(d1.Board))
	}
	if d1.Board[0] != d2.Board[0] {
		t.Fatalf("board must be deterministic: %+v vs %+v", d1.Board[0], d2.Board[0])
	}
	// Padres are 30-10 vs 10-30 at even odds with market weight 0: clear home edge
	if d1.Board[0].Pick != "Padres" {
		t.Fatalf("pick = %q, want Padres", d1.Board[0].Pick)
	}
	if d1.Board[0].PickEV <= 0.05 {
		t.Fatalf("EV %v must clear the 0.05 gate", d1.Board[0].PickEV)
	}
}
