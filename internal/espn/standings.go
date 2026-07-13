package espn

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// standingsGroups narrows college sports to the divisions we bet:
// FBS for football, Division I for basketball.
var standingsGroups = map[string]string{
	"americanfootball_ncaaf": "80",
	"basketball_ncaab":       "50",
}

// TeamRecord is one team's overall record from an ESPN standings page.
// Names are ESPN displayNames, which match The Odds API's team names.
type TeamRecord struct {
	Name   string
	Wins   int
	Losses int
}

// StandingsClient fetches season standings from the free ESPN API
type StandingsClient struct {
	baseURL string
	client  *http.Client
}

// NewStandingsClient creates a client against the public ESPN API
func NewStandingsClient() *StandingsClient {
	return &StandingsClient{
		baseURL: "https://site.api.espn.com",
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

// Standings returns every team's overall record for a season. season is
// ESPN's season year (2025 = the 2025 NFL/MLB season). Unmapped sport keys
// error rather than guess.
func (s *StandingsClient) Standings(sportKey string, season int) ([]TeamRecord, error) {
	lg, ok := leagues[sportKey]
	if !ok {
		return nil, fmt.Errorf("no ESPN league mapping for sport %s", sportKey)
	}

	url := fmt.Sprintf("%s/apis/v2/sports/%s/standings?season=%d", s.baseURL, lg.apiPath, season)
	if group, ok := standingsGroups[sportKey]; ok {
		url += "&group=" + group
	}

	resp, err := s.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("ESPN standings fetch failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ESPN standings returned status %d", resp.StatusCode)
	}

	type entry struct {
		Team struct {
			DisplayName string `json:"displayName"`
		} `json:"team"`
		Stats []struct {
			// College pages repeat wins/losses per split (home, conference,
			// vs ranked); type wins/losses is the overall record everywhere.
			Type  string  `json:"type"`
			Value float64 `json:"value"`
		} `json:"stats"`
	}
	type standings struct {
		Entries []entry `json:"entries"`
	}
	var payload struct {
		Children []struct {
			Standings standings `json:"standings"`
		} `json:"children"`
		Standings standings `json:"standings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("ESPN standings decode failed: %w", err)
	}

	groups := make([]standings, 0, len(payload.Children)+1)
	for _, c := range payload.Children {
		groups = append(groups, c.Standings)
	}
	if len(groups) == 0 {
		groups = append(groups, payload.Standings)
	}

	var records []TeamRecord
	for _, g := range groups {
		for _, e := range g.Entries {
			r := TeamRecord{Name: e.Team.DisplayName}
			for _, st := range e.Stats {
				switch st.Type {
				case "wins":
					r.Wins = int(st.Value)
				case "losses":
					r.Losses = int(st.Value)
				}
			}
			if r.Name != "" && r.Wins+r.Losses > 0 {
				records = append(records, r)
			}
		}
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("ESPN standings for %s season %d had no team records", sportKey, season)
	}

	return records, nil
}
