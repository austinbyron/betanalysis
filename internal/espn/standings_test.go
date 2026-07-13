package espn

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The college shape repeats wins/losses per split (home, conference, vs
// ranked); type wins/losses must be read as the overall record.
const standingsFixture = `{
  "children": [
    {
      "standings": {
        "entries": [
          {
            "team": {"displayName": "New England Patriots"},
            "stats": [
              {"name": "wins", "type": "wins", "value": 14.0},
              {"name": "wins", "type": "homerecord_wins", "value": 8.0},
              {"name": "losses", "type": "losses", "value": 3.0},
              {"name": "losses", "type": "homerecord_losses", "value": 1.0}
            ]
          },
          {
            "team": {"displayName": "New York Jets"},
            "stats": [
              {"name": "wins", "type": "wins", "value": 4.0},
              {"name": "losses", "type": "losses", "value": 13.0}
            ]
          }
        ]
      }
    },
    {
      "standings": {
        "entries": [
          {
            "team": {"displayName": "Chicago Bears"},
            "stats": [
              {"name": "wins", "type": "wins", "value": 10.0},
              {"name": "losses", "type": "losses", "value": 7.0}
            ]
          }
        ]
      }
    }
  ]
}`

func TestStandingsParsesOverallRecords(t *testing.T) {
	var gotPath, gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(standingsFixture))
	}))
	defer server.Close()

	client := &StandingsClient{baseURL: server.URL, client: server.Client()}
	records, err := client.Standings("americanfootball_nfl", 2025)
	if err != nil {
		t.Fatalf("Standings: %v", err)
	}

	if gotPath != "/apis/v2/sports/football/nfl/standings" {
		t.Errorf("path = %s", gotPath)
	}
	if gotQuery != "season=2025" {
		t.Errorf("query = %s", gotQuery)
	}

	if len(records) != 3 {
		t.Fatalf("records = %d, want 3 across both conferences", len(records))
	}
	if records[0].Name != "New England Patriots" || records[0].Wins != 14 || records[0].Losses != 3 {
		t.Errorf("first record = %+v, want Patriots 14-3 (overall, not home split)", records[0])
	}
}

func TestStandingsAddsCollegeGroup(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Write([]byte(standingsFixture))
	}))
	defer server.Close()

	client := &StandingsClient{baseURL: server.URL, client: server.Client()}
	if _, err := client.Standings("americanfootball_ncaaf", 2025); err != nil {
		t.Fatalf("Standings: %v", err)
	}
	if gotQuery != "season=2025&group=80" {
		t.Errorf("query = %s, want the FBS group", gotQuery)
	}
}

func TestStandingsRejectsUnknownSportAndEmptyPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"children": []}`))
	}))
	defer server.Close()

	client := &StandingsClient{baseURL: server.URL, client: server.Client()}
	if _, err := client.Standings("cricket_ipl", 2025); err == nil {
		t.Error("unmapped sport must error")
	}
	if _, err := client.Standings("americanfootball_nfl", 2025); err == nil {
		t.Error("empty standings must error, not seed nothing silently")
	}
}
