package analysis

import (
	"math"
	"sync"
	"time"

	"github.com/austinbyron/betanalysis/pkg/types"
)

// GamesProvider supplies finished games, ordered oldest first
type GamesProvider interface {
	FinishedGames(sportKey string) ([]types.Game, error)
}

const (
	eloInitial       = 1500.0
	eloK             = 20.0
	eloHomeAdvantage = 50.0 // rating points, applied when predicting and updating
	eloCacheTTL      = 10 * time.Minute
)

type eloRatings struct {
	ratings   map[string]float64
	gamesSeen map[string]int
	builtAt   time.Time
}

// Elo rates teams from stored game results with a margin-of-victory
// multiplier. Ratings rebuild from history on demand (cached ~10 min), so
// they survive restarts without any schema — a 17-game football season
// never converges on win/loss records, but points margins do.
type Elo struct {
	games       GamesProvider
	warmupGames int

	mu    sync.Mutex
	cache map[string]*eloRatings // per sport key
}

// NewElo creates an Elo estimator over a finished-games source
func NewElo(games GamesProvider, warmupGames int) *Elo {
	return &Elo{
		games:       games,
		warmupGames: warmupGames,
		cache:       make(map[string]*eloRatings),
	}
}

// Name returns the estimator name
func (e *Elo) Name() string { return "elo" }

// EstimateProbabilities converts the rating gap (plus home advantage) to a
// win probability with the standard logistic curve.
func (e *Elo) EstimateProbabilities(game types.Game) (float64, float64) {
	r := e.ratingsFor(game.SportKey)
	home := r.rating(game.HomeTeam) + eloHomeAdvantage
	away := r.rating(game.AwayTeam)

	homeProb := 1 / (1 + math.Pow(10, (away-home)/400))
	return homeProb, 1 - homeProb
}

// Confidence ramps with the lesser-known team's rated games
func (e *Elo) Confidence(game types.Game) float64 {
	if e.warmupGames <= 0 {
		return 1
	}
	r := e.ratingsFor(game.SportKey)
	seen := r.gamesSeen[game.HomeTeam]
	if a := r.gamesSeen[game.AwayTeam]; a < seen {
		seen = a
	}
	return math.Min(float64(seen)/float64(e.warmupGames), 1)
}

func (r *eloRatings) rating(team string) float64 {
	if v, ok := r.ratings[team]; ok {
		return v
	}
	return eloInitial
}

// ratingsFor returns the sport's ratings, rebuilding from stored games when
// the cache has expired. Rebuilds are cheap: one pass over game history.
func (e *Elo) ratingsFor(sportKey string) *eloRatings {
	e.mu.Lock()
	defer e.mu.Unlock()

	if r, ok := e.cache[sportKey]; ok && time.Since(r.builtAt) < eloCacheTTL {
		return r
	}

	r := &eloRatings{
		ratings:   make(map[string]float64),
		gamesSeen: make(map[string]int),
		builtAt:   time.Now(),
	}

	games, err := e.games.FinishedGames(sportKey)
	if err != nil {
		// Fail open: everyone at the initial rating, zero confidence games.
		// Don't cache the failure past the TTL retry.
		e.cache[sportKey] = r
		return r
	}

	for _, g := range games {
		if g.HomeScore == nil || g.AwayScore == nil {
			continue
		}
		home := r.rating(g.HomeTeam)
		away := r.rating(g.AwayTeam)

		expectedHome := 1 / (1 + math.Pow(10, (away-(home+eloHomeAdvantage))/400))
		outcome := 0.5
		switch {
		case *g.HomeScore > *g.AwayScore:
			outcome = 1
		case *g.HomeScore < *g.AwayScore:
			outcome = 0
		}

		margin := math.Abs(float64(*g.HomeScore - *g.AwayScore))
		delta := eloK * math.Log(margin+1) * (outcome - expectedHome)

		r.ratings[g.HomeTeam] = home + delta
		r.ratings[g.AwayTeam] = away - delta
		r.gamesSeen[g.HomeTeam]++
		r.gamesSeen[g.AwayTeam]++
	}

	e.cache[sportKey] = r
	return r
}
