package books

import (
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/austinbyron/betanalysis/pkg/types"
	"github.com/rs/zerolog/log"
)

// matchWindow is how far a book's start time may sit from the stored
// commence time — books round kickoffs differently and reschedules drift.
const matchWindow = 3 * time.Hour

// placeholderWindow is the fallback for slates whose aggregator time is a
// TBD placeholder (college Saturdays arrive as 16:00 UTC until the TV
// window is set). A same-day match is accepted only when the team pair is
// unique inside it, so MLB doubleheaders can never be confused.
const placeholderWindow = 24 * time.Hour

// Match links book lines to known games (same sport, compatible team
// names, closest start within the window) and expands each into the odds
// rows the engine reads: one per market offered. Lines with no home are
// counted, not stored — the aggregator is the slate authority.
func Match(lines []Line, games []types.Game, source string, now time.Time) (odds []types.GameOdds, unmatched int) {
	for _, l := range lines {
		g, ok := bestGame(l, games)
		if !ok {
			unmatched++
			log.Debug().Str("book", l.Bookmaker).Str("away", l.Away).Str("home", l.Home).
				Time("start", l.Commence).Msg("books: line matched no upcoming game")
			continue
		}
		base := types.GameOdds{GameID: g.ID, Bookmaker: l.Bookmaker, Source: source, LastUpdate: now, RetrievedAt: now}

		if l.HomeML != nil && l.AwayML != nil {
			o := base
			o.MarketType = types.MarketMoneyline
			o.HomeOdds, o.AwayOdds = l.HomeML, l.AwayML
			odds = append(odds, o)
		}
		if l.HomeSpread != nil && l.AwaySpread != nil && l.HomeSpreadOdds != nil && l.AwaySpreadOdds != nil {
			o := base
			o.MarketType = types.MarketSpread
			o.HomeSpread, o.AwaySpread = l.HomeSpread, l.AwaySpread
			o.HomeOdds, o.AwayOdds = l.HomeSpreadOdds, l.AwaySpreadOdds
			odds = append(odds, o)
		}
		if l.Total != nil && l.OverOdds != nil && l.UnderOdds != nil {
			o := base
			o.MarketType = types.MarketTotals
			o.OverUnder, o.OverOdds, o.UnderOdds = l.Total, l.OverOdds, l.UnderOdds
			odds = append(odds, o)
		}
	}
	return odds, unmatched
}

// bestGame picks the closest-start compatible game; on equal gaps (a
// Saturday college slate shares kickoffs) the more specific name match
// wins — fewer tokens left over after the book's spelling.
func bestGame(l Line, games []types.Game) (types.Game, bool) {
	if g, ok := closestWithin(l, games, matchWindow); ok {
		return g, true
	}
	// Placeholder-time fallback: unique pair within a day
	var only types.Game
	n := 0
	for _, g := range games {
		gap := math.Abs(g.CommenceTime.Sub(l.Commence).Seconds())
		if gap > placeholderWindow.Seconds() {
			continue
		}
		if teamsCompatible(l.Home, g.HomeTeam) && teamsCompatible(l.Away, g.AwayTeam) {
			only = g
			n++
		}
	}
	return only, n == 1
}

func closestWithin(l Line, games []types.Game, window time.Duration) (types.Game, bool) {
	var best types.Game
	bestGap, bestLeft := math.MaxFloat64, math.MaxInt
	for _, g := range games {
		gap := math.Abs(g.CommenceTime.Sub(l.Commence).Seconds())
		if gap > window.Seconds() || gap > bestGap {
			continue
		}
		if !teamsCompatible(l.Home, g.HomeTeam) || !teamsCompatible(l.Away, g.AwayTeam) {
			continue
		}
		left := leftover(l.Home, g.HomeTeam) + leftover(l.Away, g.AwayTeam)
		if gap < bestGap || left < bestLeft {
			best, bestGap, bestLeft = g, gap, left
		}
	}
	return best, bestGap < math.MaxFloat64
}

func leftover(a, b string) int {
	d := len(teamTokens(a)) - len(teamTokens(b))
	if d < 0 {
		d = -d
	}
	return d
}

var (
	parenRe = regexp.MustCompile(`\(([^)]*)\)`)
	punctRe = regexp.MustCompile(`[^a-z0-9 ]`)
	rankRe  = regexp.MustCompile(`\(#\d+\)`)
)

// aliases rewrite a book's school spelling to the aggregator's, matched
// as a whole-token prefix of the normalized name.
var aliases = map[string]string{
	"central florida":        "ucf",
	"connecticut":            "uconn",
	"ul lafayette":           "louisiana",
	"louisiana lafayette":    "louisiana",
	"ulm":                    "ul monroe",
	"louisiana monroe":       "ul monroe",
	"southern miss":          "southern mississippi",
	"fiu":                    "florida international",
	"middle tennessee state": "middle tennessee",
	"sam houston":            "sam houston state",
	"app state":              "appalachian state",
	"pitt":                   "pittsburgh",
	"usf":                    "south florida",
	"miami fl":               "miami",
	"miami florida":          "miami",
	"miami ohio":             "miami oh",
}

// schoolQualifiers are second tokens that make a lone first token a
// different school: "Florida" is not "Florida State", "Miami" is not
// "Miami (OH)", "Texas" is not "Texas Tech" or "Texas A&M".
var schoolQualifiers = map[string]bool{"state": true, "st": true, "tech": true, "am": true, "oh": true, "southern": true}

func teamTokens(name string) []string {
	s := strings.ToLower(rankRe.ReplaceAllString(name, " "))
	s = parenRe.ReplaceAllString(s, " $1 ") // keep "(OH)" as a token
	s = punctRe.ReplaceAllString(s, "")
	s = strings.Join(strings.Fields(s), " ")
	for from, to := range aliases {
		if s == from || strings.HasPrefix(s, from+" ") {
			s = to + s[len(from):]
			break
		}
	}
	return strings.Fields(s)
}

// teamsCompatible decides whether two spellings name the same team:
// either one is a prefix of the other ("Florida State" / "Florida State
// Seminoles", "Alabama" / "Alabama Crimson Tide" — but a lone token can't
// claim a qualified school, so "Florida" never matches Florida State), or
// they share a nickname and the shorter city reads as an abbreviation of
// the longer ("NE Patriots" / "New England Patriots", "STL Cardinals" /
// "St. Louis Cardinals" — not "Georgia Bulldogs" / "Mississippi State
// Bulldogs").
func teamsCompatible(a, b string) bool {
	ta, tb := teamTokens(a), teamTokens(b)
	if len(ta) == 0 || len(tb) == 0 {
		return false
	}
	short, long := ta, tb
	if len(short) > len(long) {
		short, long = long, short
	}
	if isPrefix(short, long) {
		if len(short) == 1 && len(long) > 1 && schoolQualifiers[long[1]] {
			return false
		}
		return true
	}
	if short[len(short)-1] != long[len(long)-1] {
		return false
	}
	cityShort := strings.Join(short[:len(short)-1], "")
	cityLong := strings.Join(long[:len(long)-1], "")
	if cityShort == "" || cityLong == "" {
		return true
	}
	return isSubsequence(cityShort, cityLong)
}

func isPrefix(short, long []string) bool {
	if len(short) > len(long) {
		return false
	}
	for i := range short {
		if short[i] != long[i] {
			return false
		}
	}
	return true
}

func isSubsequence(sub, s string) bool {
	i := 0
	for j := 0; j < len(s) && i < len(sub); j++ {
		if s[j] == sub[i] {
			i++
		}
	}
	return i == len(sub)
}
