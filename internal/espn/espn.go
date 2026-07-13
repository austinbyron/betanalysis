// Package espn resolves games to ESPN gamecast links via the free public
// scoreboard API. Best-effort: any miss or failure yields no link.
package espn

import (
	"encoding/json"
	"fmt"
	"net/http"
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
const retryEmptyAfter = 15 * time.Minute

type dayEvents struct {
	byTeams   map[string]string // "away|home" (lowercase) -> gamecast URL
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

	date := game.CommenceTime.In(l.loc).Format("20060102")
	key := game.SportKey + "|" + date

	l.mu.Lock()
	day, ok := l.cache[key]
	if !ok || (len(day.byTeams) == 0 && time.Since(day.fetchedAt) > retryEmptyAfter) {
		day = dayEvents{byTeams: l.fetchDay(lg, date), fetchedAt: time.Now()}
		l.cache[key] = day
	}
	l.mu.Unlock()

	return day.byTeams[teamsKey(game.AwayTeam, game.HomeTeam)]
}

func teamsKey(away, home string) string {
	return strings.ToLower(away) + "|" + strings.ToLower(home)
}

// fetchDay pulls one day's scoreboard and indexes gamecast URLs by matchup
func (l *Linker) fetchDay(lg league, date string) map[string]string {
	url := fmt.Sprintf("%s/apis/site/v2/sports/%s/scoreboard?dates=%s", l.baseURL, lg.apiPath, date)
	resp, err := l.client.Get(url)
	if err != nil {
		log.Debug().Err(err).Str("date", date).Msg("ESPN scoreboard fetch failed")
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Debug().Int("status", resp.StatusCode).Str("date", date).Msg("ESPN scoreboard fetch failed")
		return nil
	}

	var payload struct {
		Events []struct {
			ID           string `json:"id"`
			Competitions []struct {
				Competitors []struct {
					HomeAway string `json:"homeAway"`
					Team     struct {
						DisplayName string `json:"displayName"`
					} `json:"team"`
				} `json:"competitors"`
			} `json:"competitions"`
		} `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		log.Debug().Err(err).Msg("ESPN scoreboard decode failed")
		return nil
	}

	byTeams := make(map[string]string, len(payload.Events))
	for _, ev := range payload.Events {
		if len(ev.Competitions) == 0 {
			continue
		}
		var home, away string
		for _, c := range ev.Competitions[0].Competitors {
			switch c.HomeAway {
			case "home":
				home = c.Team.DisplayName
			case "away":
				away = c.Team.DisplayName
			}
		}
		if home == "" || away == "" {
			continue
		}
		byTeams[teamsKey(away, home)] = fmt.Sprintf("https://www.espn.com/%s/game/_/gameId/%s", lg.webPath, ev.ID)
	}
	return byTeams
}
