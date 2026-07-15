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

// EloInitial is the rating assigned to a team with no game history
const EloInitial = 1500.0

const (
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
	homeProb := EloProbability(r.rating(game.HomeTeam), r.rating(game.AwayTeam))
	return homeProb, 1 - homeProb
}

// MeanProbabilities equals EstimateProbabilities — Elo is deterministic
func (e *Elo) MeanProbabilities(game types.Game) (float64, float64) {
	return e.EstimateProbabilities(game)
}

// EloProbability converts two ratings into the home team's win probability
// with the standard logistic curve, including home advantage.
func EloProbability(homeRating, awayRating float64) float64 {
	return 1 / (1 + math.Pow(10, (awayRating-(homeRating+eloHomeAdvantage))/400))
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
	return EloInitial
}

// applyGame folds one finished game into the ratings; no-op without scores
func (r *eloRatings) applyGame(g types.Game) {
	if g.HomeScore == nil || g.AwayScore == nil {
		return
	}
	home := r.rating(g.HomeTeam)
	away := r.rating(g.AwayTeam)

	expectedHome := EloProbability(home, away)
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

// RatingPoint is one team's Elo rating immediately after a game
type RatingPoint struct {
	At     time.Time
	Rating float64
}

// EloHistory replays finished games (oldest first, as FinishedGames
// returns them) and records each team's rating after every game it
// played — the same math the elo estimator uses.
func EloHistory(games []types.Game) map[string][]RatingPoint {
	r := &eloRatings{ratings: make(map[string]float64), gamesSeen: make(map[string]int)}
	out := make(map[string][]RatingPoint)
	for _, g := range games {
		if g.HomeScore == nil || g.AwayScore == nil {
			continue
		}
		r.applyGame(g)
		out[g.HomeTeam] = append(out[g.HomeTeam], RatingPoint{At: g.CommenceTime, Rating: r.ratings[g.HomeTeam]})
		out[g.AwayTeam] = append(out[g.AwayTeam], RatingPoint{At: g.CommenceTime, Rating: r.ratings[g.AwayTeam]})
	}
	return out
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
		r.applyGame(g)
	}

	e.cache[sportKey] = r
	return r
}
