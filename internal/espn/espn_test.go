package espn

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/austinbyron/betanalysis/pkg/types"
)

const scoreboardFixture = `{
  "events": [
    {
      "id": "401816068",
      "date": "2026-07-08T22:40Z",
      "competitions": [{
        "competitors": [
          {"homeAway": "home", "team": {"displayName": "Detroit Tigers"}},
          {"homeAway": "away", "team": {"displayName": "Athletics"}}
        ]
      }]
    }
  ]
}`

func testGame() types.Game {
	// 22:40 UTC on Jul 8 = 6:40 PM ET Jul 8
	return types.Game{
		ID: "g1", SportKey: "baseball_mlb",
		HomeTeam: "Detroit Tigers", AwayTeam: "Athletics",
		CommenceTime: time.Date(2026, 7, 8, 22, 40, 0, 0, time.UTC),
	}
}

func TestGameURLMatchesScoreboardEvent(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/apis/site/v2/sports/baseball/mlb/scoreboard" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("dates"); got != "20260708" {
			t.Errorf("dates = %s, want 20260708 (ET date of first pitch)", got)
		}
		fmt.Fprint(w, scoreboardFixture)
	}))
	defer srv.Close()

	linker := NewLinker()
	linker.baseURL = srv.URL

	got := linker.GameURL(testGame())
	want := "https://www.espn.com/mlb/game/_/gameId/401816068"
	if got != want {
		t.Errorf("GameURL = %q, want %q", got, want)
	}

	// Same day resolves from cache — no second request
	if linker.GameURL(testGame()); requests != 1 {
		t.Errorf("requests = %d, want 1 (per-day cache)", requests)
	}
}

func TestGameURLFailsOpen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, scoreboardFixture)
	}))
	defer srv.Close()

	linker := NewLinker()
	linker.baseURL = srv.URL

	unknown := testGame()
	unknown.HomeTeam = "Springfield Isotopes"
	if got := linker.GameURL(unknown); got != "" {
		t.Errorf("unmatched game must yield no link, got %q", got)
	}

	odd := testGame()
	odd.SportKey = "cricket_ipl"
	if got := linker.GameURL(odd); got != "" {
		t.Errorf("unmapped sport must yield no link, got %q", got)
	}

	down := NewLinker()
	down.baseURL = "http://127.0.0.1:1" // nothing listening
	if got := down.GameURL(testGame()); got != "" {
		t.Errorf("fetch failure must yield no link, got %q", got)
	}
}
