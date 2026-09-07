// Package books polls sportsbook websites directly for moneyline, spread
// and total lines, as a free complement to The Odds API: the aggregator
// still defines the slate (game ids), these sources add fresh snapshots
// to games it already knows. Every source fails open — a blocked or
// changed endpoint costs snapshots, never uptime.
package books

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Line is one book's main-market prices for one game, in the book's own
// team spelling. Nil pointers mean the market wasn't offered.
type Line struct {
	Home, Away     string
	Commence       time.Time
	Bookmaker      string // key shared with The Odds API rows (draftkings, bovada)
	HomeML, AwayML *float64
	HomeSpread     *float64 // points for the home side (-3.5 = favored by 3.5)
	AwaySpread     *float64
	HomeSpreadOdds *float64
	AwaySpreadOdds *float64
	Total          *float64
	OverOdds       *float64
	UnderOdds      *float64
}

// Source fetches a sport's current pre-match lines from one book
type Source interface {
	Name() string
	Fetch(sportKey string) ([]Line, error)
}

// ErrUnsupportedSport is returned for sports a source has no path for
var ErrUnsupportedSport = errors.New("sport not supported by this source")

// Fetcher performs the HTTP GET; sources take it as an interface so tests
// can serve fixtures and DraftKings can swap in a browser-fingerprinted
// client.
type Fetcher interface {
	Get(url string) ([]byte, error)
}

// StdFetcher is a plain net/http GET with a browser User-Agent
type StdFetcher struct {
	client *http.Client
}

// NewStdFetcher creates a fetcher with a 30s timeout
func NewStdFetcher() *StdFetcher {
	return &StdFetcher{client: &http.Client{Timeout: 30 * time.Second}}
}

const browserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36"

// Get implements Fetcher
func (f *StdFetcher) Get(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}
	return body, nil
}

// Build returns the sources named in config order; unknown names error
func Build(names []string) ([]Source, error) {
	var out []Source
	for _, n := range names {
		switch n {
		case "bovada":
			out = append(out, NewBovada(NewStdFetcher()))
		case "draftkings":
			f, err := NewBrowserFetcher()
			if err != nil {
				return nil, fmt.Errorf("draftkings: %w", err)
			}
			out = append(out, NewDraftKings(f))
		default:
			return nil, fmt.Errorf("unknown book source %q (want bovada or draftkings)", n)
		}
	}
	return out, nil
}
