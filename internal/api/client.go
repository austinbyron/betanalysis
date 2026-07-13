package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/austinbyron/betanalysis/internal/config"
	"github.com/austinbyron/betanalysis/pkg/types"
	"github.com/rs/zerolog/log"
)

const defaultBaseURL = "https://api.the-odds-api.com/v4"

// Markets requested on every odds call
const oddsMarkets = "h2h,spreads,totals"

// Client handles API requests to The Odds API
type Client struct {
	baseURL     string
	apiKey      string
	regions     string
	httpClient  *http.Client
	minInterval time.Duration
	onQuota     QuotaFunc
	mu          sync.Mutex
	lastRequest time.Time
}

// QuotaFunc receives the credit counters The Odds API reports on every
// response (x-requests-remaining / x-requests-used headers).
type QuotaFunc func(remaining, used float64)

// SetQuotaHook registers a callback invoked with the latest credit counters
// after each successful header parse. Set once before use; not for
// concurrent reconfiguration.
func (c *Client) SetQuotaHook(fn QuotaFunc) {
	c.onQuota = fn
}

// NewClient creates a new API client
func NewClient(cfg config.OddsAPIConfig) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.Regions == "" {
		cfg.Regions = "us"
	}

	// Convert requests-per-second to a minimum interval between requests
	minInterval := 2 * time.Second
	if cfg.RateLimitRPS > 0 {
		minInterval = time.Duration(float64(time.Second) / cfg.RateLimitRPS)
	}

	return &Client{
		baseURL:     cfg.BaseURL,
		apiKey:      cfg.APIKey,
		regions:     cfg.Regions,
		httpClient:  &http.Client{Timeout: 30 * time.Second},
		minInterval: minInterval,
	}
}

// doRequest performs a rate-limited GET request. The apiKey is added as a
// query parameter per The Odds API v4 authentication scheme.
func (c *Client) doRequest(ctx context.Context, path string, params url.Values) (*http.Response, error) {
	c.mu.Lock()
	wait := c.minInterval - time.Since(c.lastRequest)
	if wait > 0 {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		c.mu.Lock()
	}
	c.lastRequest = time.Now()
	c.mu.Unlock()

	if params == nil {
		params = url.Values{}
	}
	params.Set("apiKey", c.apiKey)

	reqURL := fmt.Sprintf("%s%s?%s", c.baseURL, path, params.Encode())
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	if remaining := resp.Header.Get("x-requests-remaining"); remaining != "" {
		used := resp.Header.Get("x-requests-used")
		log.Debug().
			Str("requests_remaining", remaining).
			Str("requests_used", used).
			Msg("Odds API quota")
		if c.onQuota != nil {
			rem, errR := strconv.ParseFloat(remaining, 64)
			u, errU := strconv.ParseFloat(used, 64)
			if errR == nil && errU == nil {
				c.onQuota(rem, u)
			}
		}
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("API returned status %d for %s: %s", resp.StatusCode, path, strings.TrimSpace(string(body)))
	}

	return resp, nil
}

// OddsResponse represents the API response for odds
type OddsResponse struct {
	ID           string           `json:"id"`
	SportKey     string           `json:"sport_key"`
	CommenceTime time.Time        `json:"commence_time"`
	HomeTeam     string           `json:"home_team"`
	AwayTeam     string           `json:"away_team"`
	Bookmakers   []BookmakerEntry `json:"bookmakers"`
}

// BookmakerEntry represents a bookmaker's odds
type BookmakerEntry struct {
	Key        string       `json:"key"`
	Markets    []MarketData `json:"markets"`
	LastUpdate time.Time    `json:"last_update"`
}

// MarketData represents a betting market
type MarketData struct {
	Key      string    `json:"key"`
	Outcomes []Outcome `json:"outcomes"`
}

// Outcome represents a betting outcome
type Outcome struct {
	Name  string   `json:"name"`
	Price float64  `json:"price"` // Decimal odds (oddsFormat=decimal)
	Point *float64 `json:"point,omitempty"`
}

// GetOdds fetches current odds for a sport. The odds response includes the
// event details, so the games are returned too — callers must save the games
// first to satisfy the game_odds foreign key.
func (c *Client) GetOdds(sport string) ([]types.Game, []types.GameOdds, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	params := url.Values{}
	params.Set("regions", c.regions)
	params.Set("markets", oddsMarkets)
	params.Set("oddsFormat", "decimal")

	resp, err := c.doRequest(ctx, "/sports/"+sport+"/odds", params)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch odds: %w", err)
	}
	defer resp.Body.Close()

	var oddsResp []OddsResponse
	if err := json.NewDecoder(resp.Body).Decode(&oddsResp); err != nil {
		return nil, nil, fmt.Errorf("failed to parse odds response: %w", err)
	}

	games := make([]types.Game, 0, len(oddsResp))
	odds := make([]types.GameOdds, 0)
	now := time.Now()

	for _, or := range oddsResp {
		games = append(games, types.Game{
			ID:           or.ID,
			SportKey:     sport,
			CommenceTime: or.CommenceTime,
			HomeTeam:     or.HomeTeam,
			AwayTeam:     or.AwayTeam,
			Status:       "scheduled",
		})

		for _, bm := range or.Bookmakers {
			for _, market := range bm.Markets {
				gameOdds := types.GameOdds{
					GameID:      or.ID,
					Bookmaker:   bm.Key,
					MarketType:  market.Key,
					LastUpdate:  bm.LastUpdate,
					RetrievedAt: now,
				}

				for _, outcome := range market.Outcomes {
					price := outcome.Price
					switch market.Key {
					case types.MarketTotals:
						if outcome.Name == "Over" {
							gameOdds.OverOdds = &price
							gameOdds.OverUnder = outcome.Point
						} else if outcome.Name == "Under" {
							gameOdds.UnderOdds = &price
							gameOdds.OverUnder = outcome.Point
						}
					default:
						switch outcome.Name {
						case or.HomeTeam:
							gameOdds.HomeOdds = &price
							if market.Key == types.MarketSpread {
								gameOdds.HomeSpread = outcome.Point
							}
						case or.AwayTeam:
							gameOdds.AwayOdds = &price
							if market.Key == types.MarketSpread {
								gameOdds.AwaySpread = outcome.Point
							}
						case "Draw":
							gameOdds.DrawOdds = &price
						}
					}
				}

				if gameOdds.HomeOdds != nil || gameOdds.AwayOdds != nil || gameOdds.OverOdds != nil {
					odds = append(odds, gameOdds)
				}
			}
		}
	}

	log.Debug().Str("sport", sport).Int("games", len(games)).Int("odds", len(odds)).Msg("Fetched odds")
	return games, odds, nil
}

// GetGames fetches upcoming games for a sport
func (c *Client) GetGames(sport string) ([]types.Game, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := c.doRequest(ctx, "/sports/"+sport+"/events", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch games: %w", err)
	}
	defer resp.Body.Close()

	var events []struct {
		ID           string    `json:"id"`
		CommenceTime time.Time `json:"commence_time"`
		HomeTeam     string    `json:"home_team"`
		AwayTeam     string    `json:"away_team"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return nil, fmt.Errorf("failed to parse games response: %w", err)
	}

	games := make([]types.Game, len(events))
	for i, e := range events {
		games[i] = types.Game{
			ID:           e.ID,
			SportKey:     sport,
			CommenceTime: e.CommenceTime,
			HomeTeam:     e.HomeTeam,
			AwayTeam:     e.AwayTeam,
			Status:       "scheduled",
		}
	}

	return games, nil
}

// GameScore represents a game result with team names
type GameScore struct {
	GameID       string
	SportKey     string
	HomeTeam     string
	AwayTeam     string
	HomeScore    int
	AwayScore    int
	Completed    bool
	CommenceTime time.Time
}

// scoreEntry matches the v4 scores payload: scores is an array of
// {name, score} pairs where score is a numeric string.
type scoreEntry struct {
	Name  string `json:"name"`
	Score string `json:"score"`
}

// GetScores fetches scores for a sport. daysFrom (1-3) includes games
// completed in the last N days; 0 returns only live/upcoming games.
func (c *Client) GetScores(sport string, daysFrom int) ([]GameScore, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	params := url.Values{}
	if daysFrom > 0 {
		if daysFrom > 3 {
			daysFrom = 3
		}
		params.Set("daysFrom", strconv.Itoa(daysFrom))
	}

	resp, err := c.doRequest(ctx, "/sports/"+sport+"/scores", params)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch scores: %w", err)
	}
	defer resp.Body.Close()

	var payload []struct {
		ID           string       `json:"id"`
		SportKey     string       `json:"sport_key"`
		CommenceTime time.Time    `json:"commence_time"`
		Completed    bool         `json:"completed"`
		HomeTeam     string       `json:"home_team"`
		AwayTeam     string       `json:"away_team"`
		Scores       []scoreEntry `json:"scores"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("failed to parse scores response: %w", err)
	}

	scores := make([]GameScore, 0, len(payload))
	for _, s := range payload {
		gs := GameScore{
			GameID:       s.ID,
			SportKey:     s.SportKey,
			HomeTeam:     s.HomeTeam,
			AwayTeam:     s.AwayTeam,
			Completed:    s.Completed,
			CommenceTime: s.CommenceTime,
		}

		for _, entry := range s.Scores {
			value, err := strconv.Atoi(entry.Score)
			if err != nil {
				log.Warn().Str("game", s.ID).Str("score", entry.Score).Msg("Unparseable score value")
				continue
			}
			switch entry.Name {
			case s.HomeTeam:
				gs.HomeScore = value
			case s.AwayTeam:
				gs.AwayScore = value
			}
		}

		scores = append(scores, gs)
	}

	log.Debug().Str("sport", sport).Int("count", len(scores)).Msg("Fetched game scores")
	return scores, nil
}

// GetAvailableSports fetches available sports (does not count against quota)
func (c *Client) GetAvailableSports() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := c.doRequest(ctx, "/sports", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch sports: %w", err)
	}
	defer resp.Body.Close()

	var sports []struct {
		Key    string `json:"key"`
		Active bool   `json:"active"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&sports); err != nil {
		return nil, fmt.Errorf("failed to parse sports response: %w", err)
	}

	activeSports := make([]string, 0)
	for _, s := range sports {
		if s.Active {
			activeSports = append(activeSports, s.Key)
		}
	}

	return activeSports, nil
}
