package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/austinbyron/betanalysis/internal/config"
	"github.com/austinbyron/betanalysis/pkg/types"
)

func newTestClient(baseURL string) *Client {
	return NewClient(config.OddsAPIConfig{
		APIKey:       "test-key",
		BaseURL:      baseURL,
		Regions:      "us",
		RateLimitRPS: 1000, // effectively no throttling in tests
	})
}

const oddsFixture = `[
  {
    "id": "g1",
    "sport_key": "americanfootball_nfl",
    "sport_title": "NFL",
    "commence_time": "2026-01-04T18:00:00Z",
    "home_team": "Kansas City Chiefs",
    "away_team": "Buffalo Bills",
    "bookmakers": [
      {
        "key": "draftkings",
        "title": "DraftKings",
        "last_update": "2026-01-04T12:00:00Z",
        "markets": [
          {
            "key": "h2h",
            "outcomes": [
              {"name": "Kansas City Chiefs", "price": 1.87},
              {"name": "Buffalo Bills", "price": 1.95}
            ]
          },
          {
            "key": "spreads",
            "outcomes": [
              {"name": "Kansas City Chiefs", "price": 1.91, "point": -2.5},
              {"name": "Buffalo Bills", "price": 1.91, "point": 2.5}
            ]
          },
          {
            "key": "totals",
            "outcomes": [
              {"name": "Over", "price": 1.90, "point": 47.5},
              {"name": "Under", "price": 1.92, "point": 47.5}
            ]
          }
        ]
      }
    ]
  }
]`

func TestGetOdds(t *testing.T) {
	var gotQuery map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sports/americanfootball_nfl/odds") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		gotQuery = map[string]string{}
		for k := range r.URL.Query() {
			gotQuery[k] = r.URL.Query().Get(k)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(oddsFixture))
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	games, odds, err := client.GetOdds("americanfootball_nfl")
	if err != nil {
		t.Fatalf("GetOdds: %v", err)
	}

	// Auth and required params go in the query string per the v4 contract
	if gotQuery["apiKey"] != "test-key" {
		t.Errorf("apiKey param = %q, want test-key", gotQuery["apiKey"])
	}
	if gotQuery["regions"] != "us" {
		t.Errorf("regions param = %q, want us", gotQuery["regions"])
	}
	if gotQuery["oddsFormat"] != "decimal" {
		t.Errorf("oddsFormat param = %q, want decimal", gotQuery["oddsFormat"])
	}
	if !strings.Contains(gotQuery["markets"], "h2h") {
		t.Errorf("markets param = %q, want h2h included", gotQuery["markets"])
	}

	if len(games) != 1 {
		t.Fatalf("games = %d, want 1", len(games))
	}
	if games[0].ID != "g1" || games[0].HomeTeam != "Kansas City Chiefs" || games[0].Status != "scheduled" {
		t.Errorf("unexpected game: %+v", games[0])
	}

	if len(odds) != 3 {
		t.Fatalf("odds rows = %d, want 3 (h2h, spreads, totals)", len(odds))
	}

	byMarket := map[string]types.GameOdds{}
	for _, o := range odds {
		byMarket[o.MarketType] = o
	}

	h2h := byMarket[types.MarketMoneyline]
	if h2h.HomeOdds == nil || *h2h.HomeOdds != 1.87 || h2h.AwayOdds == nil || *h2h.AwayOdds != 1.95 {
		t.Errorf("h2h odds parsed wrong: %+v", h2h)
	}

	spread := byMarket[types.MarketSpread]
	if spread.HomeSpread == nil || *spread.HomeSpread != -2.5 {
		t.Errorf("home spread parsed wrong: %+v", spread)
	}

	totals := byMarket[types.MarketTotals]
	if totals.OverUnder == nil || *totals.OverUnder != 47.5 || totals.OverOdds == nil || *totals.OverOdds != 1.90 {
		t.Errorf("totals parsed wrong: %+v", totals)
	}
}

const scoresFixture = `[
  {
    "id": "g1",
    "sport_key": "americanfootball_nfl",
    "commence_time": "2026-01-03T18:00:00Z",
    "completed": true,
    "home_team": "Detroit Lions",
    "away_team": "Chicago Bears",
    "scores": [
      {"name": "Detroit Lions", "score": "27"},
      {"name": "Chicago Bears", "score": "20"}
    ],
    "last_update": "2026-01-03T22:00:00Z"
  },
  {
    "id": "g2",
    "sport_key": "americanfootball_nfl",
    "commence_time": "2026-01-05T18:00:00Z",
    "completed": false,
    "home_team": "Green Bay Packers",
    "away_team": "Minnesota Vikings",
    "scores": null,
    "last_update": null
  }
]`

func TestGetScores(t *testing.T) {
	var gotDaysFrom string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotDaysFrom = r.URL.Query().Get("daysFrom")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(scoresFixture))
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	scores, err := client.GetScores("americanfootball_nfl", 3)
	if err != nil {
		t.Fatalf("GetScores: %v", err)
	}

	if gotDaysFrom != "3" {
		t.Errorf("daysFrom param = %q, want 3", gotDaysFrom)
	}

	if len(scores) != 2 {
		t.Fatalf("scores = %d, want 2", len(scores))
	}

	// Completed game: string scores mapped to the right teams
	done := scores[0]
	if !done.Completed || done.HomeScore != 27 || done.AwayScore != 20 {
		t.Errorf("completed game parsed wrong: %+v", done)
	}

	// In-progress game with null scores parses without error
	if scores[1].Completed {
		t.Errorf("game 2 should not be completed: %+v", scores[1])
	}
}

func TestErrorIncludesStatusAndBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message": "Invalid API key"}`))
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	_, _, err := client.GetOdds("americanfootball_nfl")
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Invalid API key") {
		t.Errorf("error should surface status and body, got: %v", err)
	}
}

func TestQuotaHookReceivesHeaderCounters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-requests-remaining", "412.5")
		w.Header().Set("x-requests-used", "87.5")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	var gotRemaining, gotUsed float64
	calls := 0
	client.SetQuotaHook(func(remaining, used float64) {
		gotRemaining, gotUsed = remaining, used
		calls++
	})

	if _, _, err := client.GetOdds("baseball_mlb"); err != nil {
		t.Fatalf("GetOdds: %v", err)
	}
	if calls != 1 || gotRemaining != 412.5 || gotUsed != 87.5 {
		t.Errorf("hook got %d calls, %v/%v — want 1 call with 412.5/87.5", calls, gotRemaining, gotUsed)
	}
}
