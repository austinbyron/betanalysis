// Package mlb integrates MLB's free StatsAPI (statsapi.mlb.com, no key) to
// refine game probabilities with starting pitcher quality — the largest
// single factor in MLB moneylines that team records can't see.
package mlb

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

const defaultBaseURL = "https://statsapi.mlb.com/api/v1"

// Starter identifies a probable starting pitcher
type Starter struct {
	ID   int
	Name string
}

// gameStarters holds one scheduled game's probables
type gameStarters struct {
	gameTime time.Time
	homeTeam string
	awayTeam string
	home     *Starter
	away     *Starter
}

// Client talks to the MLB StatsAPI with per-day caching. Team names in
// StatsAPI match The Odds API exactly ("Kansas City Royals").
type Client struct {
	baseURL    string
	httpClient *http.Client

	mu            sync.Mutex
	scheduleCache map[string]scheduleEntry // ET date -> games
	qualityCache  map[int]qualityEntry     // pitcher id -> quality
}

type scheduleEntry struct {
	games     []gameStarters
	fetchedAt time.Time
}

type qualityEntry struct {
	quality   float64
	fetchedAt time.Time
}

const (
	scheduleTTL = 3 * time.Hour
	qualityTTL  = 24 * time.Hour
)

// eastern is MLB's scheduling timezone — StatsAPI date params roll on ET
var eastern = func() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.UTC
	}
	return loc
}()

// NewClient creates a StatsAPI client
func NewClient() *Client {
	return NewClientWithBaseURL(defaultBaseURL)
}

// NewClientWithBaseURL creates a client against a custom base URL (tests)
func NewClientWithBaseURL(baseURL string) *Client {
	return &Client{
		baseURL:       baseURL,
		httpClient:    &http.Client{Timeout: 15 * time.Second},
		scheduleCache: make(map[string]scheduleEntry),
		qualityCache:  make(map[int]qualityEntry),
	}
}

// StartersForGame returns the probable starters for a matchup. Doubleheaders
// are disambiguated by picking the scheduled game closest to commence time.
// Either starter may be nil when MLB hasn't announced a probable.
func (c *Client) StartersForGame(homeTeam, awayTeam string, commence time.Time) (home, away *Starter, err error) {
	day := commence.In(eastern).Format("2006-01-02")
	games, err := c.schedule(day)
	if err != nil {
		return nil, nil, err
	}

	var best *gameStarters
	bestDiff := time.Duration(math.MaxInt64)
	for i := range games {
		g := &games[i]
		if !strings.EqualFold(g.homeTeam, homeTeam) || !strings.EqualFold(g.awayTeam, awayTeam) {
			continue
		}
		diff := g.gameTime.Sub(commence)
		if diff < 0 {
			diff = -diff
		}
		if diff < bestDiff {
			bestDiff = diff
			best = g
		}
	}

	if best == nil {
		return nil, nil, fmt.Errorf("no scheduled game found for %s @ %s on %s", awayTeam, homeTeam, day)
	}
	return best.home, best.away, nil
}

func (c *Client) schedule(day string) ([]gameStarters, error) {
	c.mu.Lock()
	if entry, ok := c.scheduleCache[day]; ok && time.Since(entry.fetchedAt) < scheduleTTL {
		games := entry.games
		c.mu.Unlock()
		return games, nil
	}
	c.mu.Unlock()

	params := url.Values{}
	params.Set("sportId", "1")
	params.Set("date", day)
	params.Set("hydrate", "probablePitcher")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/schedule?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("statsapi schedule fetch failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("statsapi schedule returned %d", resp.StatusCode)
	}

	var payload struct {
		Dates []struct {
			Games []struct {
				GameDate time.Time `json:"gameDate"`
				Teams    struct {
					Home scheduleSide `json:"home"`
					Away scheduleSide `json:"away"`
				} `json:"teams"`
			} `json:"games"`
		} `json:"dates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("statsapi schedule parse failed: %w", err)
	}

	var games []gameStarters
	for _, date := range payload.Dates {
		for _, g := range date.Games {
			games = append(games, gameStarters{
				gameTime: g.GameDate,
				homeTeam: g.Teams.Home.Team.Name,
				awayTeam: g.Teams.Away.Team.Name,
				home:     g.Teams.Home.starter(),
				away:     g.Teams.Away.starter(),
			})
		}
	}

	c.mu.Lock()
	c.scheduleCache[day] = scheduleEntry{games: games, fetchedAt: time.Now()}
	c.mu.Unlock()

	log.Debug().Str("date", day).Int("games", len(games)).Msg("Fetched MLB probables")
	return games, nil
}

type scheduleSide struct {
	Team struct {
		Name string `json:"name"`
	} `json:"team"`
	ProbablePitcher *struct {
		ID       int    `json:"id"`
		FullName string `json:"fullName"`
	} `json:"probablePitcher"`
}

func (s scheduleSide) starter() *Starter {
	if s.ProbablePitcher == nil {
		return nil
	}
	return &Starter{ID: s.ProbablePitcher.ID, Name: s.ProbablePitcher.FullName}
}

// FIP model constants. Quality is expressed as runs-per-9 better (+) or
// worse (-) than a league-average starter.
const (
	fipConstant  = 3.10
	leagueFIP    = 4.15
	regressionIP = 40.0 // innings of league-average performance blended in
)

// PitcherQuality returns the starter's regressed quality in runs per 9
// innings relative to league average. Small samples regress hard toward 0.
func (c *Client) PitcherQuality(playerID int) (float64, error) {
	c.mu.Lock()
	if entry, ok := c.qualityCache[playerID]; ok && time.Since(entry.fetchedAt) < qualityTTL {
		q := entry.quality
		c.mu.Unlock()
		return q, nil
	}
	c.mu.Unlock()

	season := time.Now().In(eastern).Year()
	params := url.Values{}
	params.Set("stats", "season")
	params.Set("group", "pitching")
	params.Set("season", strconv.Itoa(season))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	reqURL := fmt.Sprintf("%s/people/%d/stats?%s", c.baseURL, playerID, params.Encode())
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("statsapi pitcher stats fetch failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("statsapi pitcher stats returned %d", resp.StatusCode)
	}

	var payload struct {
		Stats []struct {
			Splits []struct {
				Stat struct {
					InningsPitched string `json:"inningsPitched"`
					StrikeOuts     int    `json:"strikeOuts"`
					BaseOnBalls    int    `json:"baseOnBalls"`
					HitByPitch     int    `json:"hitByPitch"`
					HomeRuns       int    `json:"homeRuns"`
				} `json:"stat"`
			} `json:"splits"`
		} `json:"stats"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0, fmt.Errorf("statsapi pitcher stats parse failed: %w", err)
	}

	quality := 0.0
	for _, s := range payload.Stats {
		for _, split := range s.Splits {
			stat := split.Stat
			ip := parseInnings(stat.InningsPitched)
			quality = qualityFromComponents(ip, stat.StrikeOuts, stat.BaseOnBalls, stat.HitByPitch, stat.HomeRuns)
			break
		}
	}

	c.mu.Lock()
	c.qualityCache[playerID] = qualityEntry{quality: quality, fetchedAt: time.Now()}
	c.mu.Unlock()

	return quality, nil
}

// parseInnings converts baseball innings notation: "117.1" is 117⅓ innings
// (the decimal digit counts outs, not tenths).
func parseInnings(v string) float64 {
	parts := strings.SplitN(v, ".", 2)
	whole, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0
	}
	outs := 0
	if len(parts) == 2 {
		outs, _ = strconv.Atoi(parts[1])
	}
	return float64(whole) + float64(outs)/3
}

// qualityFromComponents computes FIP, regresses it toward league average by
// innings pitched, and returns leagueFIP - regressedFIP (positive = better).
func qualityFromComponents(ip float64, k, bb, hbp, hr int) float64 {
	if ip <= 0 {
		return 0
	}
	fip := (13*float64(hr)+3*float64(bb+hbp)-2*float64(k))/ip + fipConstant
	regressed := (fip*ip + leagueFIP*regressionIP) / (ip + regressionIP)
	return leagueFIP - regressed
}
