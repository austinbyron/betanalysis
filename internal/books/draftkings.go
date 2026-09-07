package books

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	http "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// DraftKings reads the sportsbook's league feed. The site sits behind
// Akamai bot management: plain Go TLS gets 403, a Chrome TLS fingerprint
// with browser headers in browser order gets through (2026-09). Team
// names are abbreviated ("NE Patriots") — the matcher handles that.
type DraftKings struct {
	fetch Fetcher
}

// NewDraftKings creates the source over a fetcher (use NewBrowserFetcher live)
func NewDraftKings(f Fetcher) *DraftKings { return &DraftKings{fetch: f} }

// Name implements Source
func (d *DraftKings) Name() string { return "draftkings" }

var draftKingsLeagues = map[string]string{
	"baseball_mlb":           "84240",
	"americanfootball_nfl":   "88808",
	"americanfootball_ncaaf": "87637",
	"basketball_nba":         "42648",
	"basketball_ncaab":       "92483",
}

type dkFeed struct {
	Events []struct {
		ID             string `json:"id"`
		Name           string `json:"name"`
		StartEventDate string `json:"startEventDate"`
		Status         string `json:"status"`
		Participants   []struct {
			Name      string `json:"name"`
			VenueRole string `json:"venueRole"`
		} `json:"participants"`
	} `json:"events"`
	Markets []struct {
		ID      string `json:"id"`
		EventID string `json:"eventId"`
		Name    string `json:"name"`
		Main    bool   `json:"main"`
	} `json:"markets"`
	Selections []struct {
		MarketID    string   `json:"marketId"`
		TrueOdds    float64  `json:"trueOdds"`
		Points      *float64 `json:"points"`
		OutcomeType string   `json:"outcomeType"` // Home, Away, Over, Under
	} `json:"selections"`
}

// Fetch implements Source
func (d *DraftKings) Fetch(sportKey string) ([]Line, error) {
	league, ok := draftKingsLeagues[sportKey]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedSport, sportKey)
	}
	body, err := d.fetch.Get("https://sportsbook-nash.draftkings.com/api/sportscontent/dkusoh/v1/leagues/" + league)
	if err != nil {
		return nil, err
	}
	var feed dkFeed
	if err := json.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("draftkings: decode: %w", err)
	}

	// market id -> (event id, market name), main markets only
	type mk struct{ event, name string }
	markets := make(map[string]mk)
	for _, m := range feed.Markets {
		if m.Main && (m.Name == "Moneyline" || m.Name == "Spread" || m.Name == "Total") {
			markets[m.ID] = mk{m.EventID, m.Name}
		}
	}

	lines := make(map[string]*Line)
	var order []string
	for _, ev := range feed.Events {
		if ev.Status != "" && ev.Status != "NOT_STARTED" {
			continue
		}
		start, err := time.Parse(time.RFC3339Nano, ev.StartEventDate)
		if err != nil {
			continue
		}
		l := &Line{Bookmaker: "draftkings", Commence: start.UTC()}
		for _, p := range ev.Participants {
			switch p.VenueRole {
			case "Home":
				l.Home = p.Name
			case "Away":
				l.Away = p.Name
			}
		}
		if l.Home == "" || l.Away == "" {
			continue
		}
		lines[ev.ID] = l
		order = append(order, ev.ID)
	}

	for _, s := range feed.Selections {
		m, ok := markets[s.MarketID]
		if !ok {
			continue
		}
		l, ok := lines[m.event]
		if !ok || s.TrueOdds <= 0 {
			continue
		}
		odds := s.TrueOdds
		switch m.name {
		case "Moneyline":
			switch s.OutcomeType {
			case "Home":
				l.HomeML = &odds
			case "Away":
				l.AwayML = &odds
			}
		case "Spread":
			switch s.OutcomeType {
			case "Home":
				l.HomeSpread, l.HomeSpreadOdds = s.Points, &odds
			case "Away":
				l.AwaySpread, l.AwaySpreadOdds = s.Points, &odds
			}
		case "Total":
			switch s.OutcomeType {
			case "Over":
				l.Total, l.OverOdds = s.Points, &odds
			case "Under":
				if l.Total == nil {
					l.Total = s.Points
				}
				l.UnderOdds = &odds
			}
		}
	}

	var out []Line
	for _, id := range order {
		l := lines[id]
		if l.HomeML != nil || l.AwayML != nil || l.Total != nil || l.HomeSpread != nil {
			out = append(out, *l)
		}
	}
	return out, nil
}

// BrowserFetcher GETs with a Chrome TLS fingerprint and browser headers
type BrowserFetcher struct {
	client tls_client.HttpClient
}

// NewBrowserFetcher builds the fingerprinted client
func NewBrowserFetcher() (*BrowserFetcher, error) {
	client, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(),
		tls_client.WithTimeoutSeconds(30),
		tls_client.WithClientProfile(profiles.Chrome_133),
		tls_client.WithCookieJar(tls_client.NewCookieJar()))
	if err != nil {
		return nil, err
	}
	return &BrowserFetcher{client: client}, nil
}

// Get implements Fetcher. Header order matters to the bot wall.
func (b *BrowserFetcher) Get(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header = http.Header{
		"accept":            {"application/json"},
		"accept-language":   {"en-US,en;q=0.9"},
		"referer":           {"https://sportsbook.draftkings.com/"},
		"user-agent":        {browserUA},
		http.HeaderOrderKey: {"accept", "accept-language", "referer", "user-agent"},
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}
	return body, nil
}
