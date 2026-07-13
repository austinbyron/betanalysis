// Package espn resolves games to ESPN gamecast links via the free public
// scoreboard API. Best-effort: any miss or failure yields no link.
package espn

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/austinbyron/betanalysis/pkg/types"
	"github.com/rs/zerolog/log"
)

// league maps an Odds API sport key to ESPN's API path and web slug
type league struct {
	apiPath string
	webPath string
}

var leagues = map[string]league{
	"baseball_mlb":           {"baseball/mlb", "mlb"},
	"americanfootball_nfl":   {"football/nfl", "nfl"},
	"americanfootball_ncaaf": {"football/college-football", "college-football"},
	"basketball_nba":         {"basketball/nba", "nba"},
	"basketball_ncaab":       {"basketball/mens-college-basketball", "mens-college-basketball"},
}

// retryEmptyAfter re-fetches a day whose scoreboard came back empty or
// failed — rosters publish early, so this mostly covers transient errors.
// The same window paces re-fetches of days whose matched game hasn't
// reached a terminal status yet (see GameResult).
const retryEmptyAfter = 15 * time.Minute

// Result statuses, collapsed from ESPN's status.type.name
const (
	ResultFinal     = "final"
	ResultPostponed = "postponed"
	ResultCanceled  = "canceled"
	ResultPending   = "pending" // scheduled, in progress, delayed, ...
)

// Result is the outcome of an ESPN scoreboard event
type Result struct {
	Status    string
	HomeScore int
	AwayScore int
	StartTime time.Time
}

// Terminal reports whether the game can no longer produce a score update
func (r Result) Terminal() bool {
	return r.Status == ResultFinal || r.Status == ResultPostponed || r.Status == ResultCanceled
}

type dayEvents struct {
	byTeams   map[string]string   // "away|home" (lowercase) -> gamecast URL
	results   map[string][]Result // "away|home" -> results (slice: doubleheaders)
	fetchedAt time.Time
}

// Linker matches games to ESPN event ids, one cached scoreboard fetch per
// sport per (Eastern-time) day.
type Linker struct {
	baseURL string
	client  *http.Client
	loc     *time.Location

	mu    sync.Mutex
	cache map[string]dayEvents // "sport|YYYYMMDD"
}

// NewLinker creates a Linker against the public ESPN API
func NewLinker() *Linker {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.UTC
	}
	return &Linker{
		baseURL: "https://site.api.espn.com",
		client:  &http.Client{Timeout: 5 * time.Second},
		loc:     loc,
		cache:   make(map[string]dayEvents),
	}
}

// GameURL returns the ESPN gamecast link for a game, or "" when the sport
// is unmapped, the scoreboard is unreachable, or no event matches.
func (l *Linker) GameURL(game types.Game) string {
	lg, ok := leagues[game.SportKey]
	if !ok {
		return ""
	}

	day := l.day(lg, game, func(day dayEvents, _ string) bool {
		return len(day.byTeams) == 0
	})
	return day.byTeams[teamsKey(game.AwayTeam, game.HomeTeam)]
}

// GameResult returns the outcome of the ESPN event matching the game's
// matchup on its (Eastern-time) date. On a doubleheader day the event
// starting nearest the game's commence time wins. ok is false when the
// sport is unmapped, the scoreboard is unreachable, or nothing matches.
func (l *Linker) GameResult(game types.Game) (Result, bool) {
	if _, ok := leagues[game.SportKey]; !ok {
		return Result{}, false
	}
	lg := leagues[game.SportKey]

	day := l.day(lg, game, func(day dayEvents, matchup string) bool {
		rs, ok := day.results[matchup]
		if !ok {
			return len(day.byTeams) == 0
		}
		for _, r := range rs {
			if !r.Terminal() {
				return true
			}
		}
		return false
	})

	rs := day.results[teamsKey(game.AwayTeam, game.HomeTeam)]
	if len(rs) == 0 {
		return Result{}, false
	}
	best := rs[0]
	for _, r := range rs[1:] {
		if absDuration(r.StartTime.Sub(game.CommenceTime)) < absDuration(best.StartTime.Sub(game.CommenceTime)) {
			best = r
		}
	}
	return best, true
}

// day returns the cached scoreboard for the game's ET date, re-fetching
// when there is no cache entry or when wantsRefresh says the entry is
// incomplete and it is older than retryEmptyAfter.
func (l *Linker) day(lg league, game types.Game, wantsRefresh func(dayEvents, string) bool) dayEvents {
	date := game.CommenceTime.In(l.loc).Format("20060102")
	key := game.SportKey + "|" + date
	matchup := teamsKey(game.AwayTeam, game.HomeTeam)

	l.mu.Lock()
	defer l.mu.Unlock()
	day, ok := l.cache[key]
	if !ok || (wantsRefresh(day, matchup) && time.Since(day.fetchedAt) > retryEmptyAfter) {
		byTeams, results := l.fetchDay(lg, date)
		day = dayEvents{byTeams: byTeams, results: results, fetchedAt: time.Now()}
		l.cache[key] = day
	}
	return day
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func teamsKey(away, home string) string {
	return strings.ToLower(away) + "|" + strings.ToLower(home)
}

// fetchDay pulls one day's scoreboard and indexes gamecast URLs and
// results by matchup
func (l *Linker) fetchDay(lg league, date string) (map[string]string, map[string][]Result) {
	url := fmt.Sprintf("%s/apis/site/v2/sports/%s/scoreboard?dates=%s", l.baseURL, lg.apiPath, date)
	resp, err := l.client.Get(url)
	if err != nil {
		log.Debug().Err(err).Str("date", date).Msg("ESPN scoreboard fetch failed")
		return nil, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Debug().Int("status", resp.StatusCode).Str("date", date).Msg("ESPN scoreboard fetch failed")
		return nil, nil
	}

	var payload struct {
		Events []struct {
			ID     string `json:"id"`
			Date   string `json:"date"`
			Status struct {
				Type struct {
					Name string `json:"name"`
				} `json:"type"`
			} `json:"status"`
			Competitions []struct {
				Competitors []struct {
					HomeAway string `json:"homeAway"`
					Score    string `json:"score"`
					Team     struct {
						DisplayName string `json:"displayName"`
					} `json:"team"`
				} `json:"competitors"`
			} `json:"competitions"`
		} `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		log.Debug().Err(err).Msg("ESPN scoreboard decode failed")
		return nil, nil
	}

	byTeams := make(map[string]string, len(payload.Events))
	results := make(map[string][]Result, len(payload.Events))
	for _, ev := range payload.Events {
		if len(ev.Competitions) == 0 {
			continue
		}
		var home, away string
		res := Result{Status: mapStatus(ev.Status.Type.Name), StartTime: parseEventTime(ev.Date)}
		for _, c := range ev.Competitions[0].Competitors {
			switch c.HomeAway {
			case "home":
				home = c.Team.DisplayName
				res.HomeScore, _ = strconv.Atoi(c.Score)
			case "away":
				away = c.Team.DisplayName
				res.AwayScore, _ = strconv.Atoi(c.Score)
			}
		}
		if home == "" || away == "" {
			continue
		}
		key := teamsKey(away, home)
		byTeams[key] = fmt.Sprintf("https://www.espn.com/%s/game/_/gameId/%s", lg.webPath, ev.ID)
		results[key] = append(results[key], res)
	}
	return byTeams, results
}

func mapStatus(name string) string {
	switch name {
	case "STATUS_FINAL":
		return ResultFinal
	case "STATUS_POSTPONED":
		return ResultPostponed
	case "STATUS_CANCELED", "STATUS_CANCELLED":
		return ResultCanceled
	default:
		return ResultPending
	}
}

// parseEventTime handles ESPN's minute-precision timestamps ("2026-07-08T22:40Z")
func parseEventTime(s string) time.Time {
	for _, layout := range []string{"2006-01-02T15:04Z", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
