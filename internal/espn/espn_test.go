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

const resultsFixture = `{
  "events": [
    {
      "id": "401816100",
      "date": "2026-07-08T17:36Z",
      "status": {"type": {"name": "STATUS_FINAL"}},
      "competitions": [{
        "competitors": [
          {"homeAway": "home", "team": {"displayName": "Detroit Tigers"}, "score": "3"},
          {"homeAway": "away", "team": {"displayName": "Athletics"}, "score": "2"}
        ]
      }]
    },
    {
      "id": "401816101",
      "date": "2026-07-08T22:40Z",
      "status": {"type": {"name": "STATUS_FINAL"}},
      "competitions": [{
        "competitors": [
          {"homeAway": "home", "team": {"displayName": "Detroit Tigers"}, "score": "5"},
          {"homeAway": "away", "team": {"displayName": "Athletics"}, "score": "7"}
        ]
      }]
    },
    {
      "id": "401816102",
      "date": "2026-07-08T23:05Z",
      "status": {"type": {"name": "STATUS_POSTPONED"}},
      "competitions": [{
        "competitors": [
          {"homeAway": "home", "team": {"displayName": "Boston Red Sox"}, "score": "0"},
          {"homeAway": "away", "team": {"displayName": "New York Yankees"}, "score": "0"}
        ]
      }]
    }
  ]
}`

func resultsServer(t *testing.T, requests *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests != nil {
			*requests++
		}
		fmt.Fprint(w, resultsFixture)
	}))
}

func TestGameResultFinal(t *testing.T) {
	srv := resultsServer(t, nil)
	defer srv.Close()

	linker := NewLinker()
	linker.baseURL = srv.URL

	res, ok := linker.GameResult(testGame())
	if !ok {
		t.Fatal("expected a result")
	}
	if res.Status != ResultFinal || res.HomeScore != 5 || res.AwayScore != 7 {
		t.Errorf("got %+v, want final 5-7", res)
	}
}

func TestGameResultDoubleheaderPicksNearestStart(t *testing.T) {
	srv := resultsServer(t, nil)
	defer srv.Close()

	linker := NewLinker()
	linker.baseURL = srv.URL

	early := testGame()
	early.CommenceTime = time.Date(2026, 7, 8, 17, 36, 0, 0, time.UTC)
	res, ok := linker.GameResult(early)
	if !ok {
		t.Fatal("expected a result")
	}
	if res.HomeScore != 3 || res.AwayScore != 2 {
		t.Errorf("got %+v, want the 17:36 game (3-2)", res)
	}
}

func TestGameResultPostponed(t *testing.T) {
	srv := resultsServer(t, nil)
	defer srv.Close()

	linker := NewLinker()
	linker.baseURL = srv.URL

	game := testGame()
	game.HomeTeam, game.AwayTeam = "Boston Red Sox", "New York Yankees"
	game.CommenceTime = time.Date(2026, 7, 8, 23, 5, 0, 0, time.UTC)

	res, ok := linker.GameResult(game)
	if !ok {
		t.Fatal("expected a result")
	}
	if res.Status != ResultPostponed {
		t.Errorf("status = %q, want %q", res.Status, ResultPostponed)
	}
}

func TestGameResultFailsOpen(t *testing.T) {
	srv := resultsServer(t, nil)
	defer srv.Close()

	linker := NewLinker()
	linker.baseURL = srv.URL

	unknown := testGame()
	unknown.HomeTeam = "Springfield Isotopes"
	if _, ok := linker.GameResult(unknown); ok {
		t.Error("unmatched game must yield no result")
	}

	odd := testGame()
	odd.SportKey = "cricket_ipl"
	if _, ok := linker.GameResult(odd); ok {
		t.Error("unmapped sport must yield no result")
	}

	down := NewLinker()
	down.baseURL = "http://127.0.0.1:1"
	if _, ok := down.GameResult(testGame()); ok {
		t.Error("fetch failure must yield no result")
	}
}

func TestGameResultRefreshesStaleNonTerminal(t *testing.T) {
	requests := 0
	inProgress := `{
	  "events": [{
	    "id": "401816100",
	    "date": "2026-07-08T22:40Z",
	    "status": {"type": {"name": "STATUS_IN_PROGRESS"}},
	    "competitions": [{
	      "competitors": [
	        {"homeAway": "home", "team": {"displayName": "Detroit Tigers"}, "score": "1"},
	        {"homeAway": "away", "team": {"displayName": "Athletics"}, "score": "0"}
	      ]
	    }]
	  }]
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			fmt.Fprint(w, inProgress)
			return
		}
		fmt.Fprint(w, resultsFixture)
	}))
	defer srv.Close()

	linker := NewLinker()
	linker.baseURL = srv.URL

	if res, ok := linker.GameResult(testGame()); !ok || res.Status == ResultFinal {
		t.Fatalf("first fetch should be in progress, got %+v ok=%v", res, ok)
	}

	// Fresh cache: a non-terminal result is NOT refetched immediately
	if _, _ = linker.GameResult(testGame()); requests != 1 {
		t.Fatalf("requests = %d, want 1 while cache is fresh", requests)
	}

	// Age the cached day past the refresh window; next lookup refetches
	linker.mu.Lock()
	for k, day := range linker.cache {
		day.fetchedAt = time.Now().Add(-time.Hour)
		linker.cache[k] = day
	}
	linker.mu.Unlock()

	res, ok := linker.GameResult(testGame())
	if !ok || res.Status != ResultFinal {
		t.Fatalf("stale non-terminal day must refetch, got %+v ok=%v (requests=%d)", res, ok, requests)
	}
	if requests != 2 {
		t.Errorf("requests = %d, want 2", requests)
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
