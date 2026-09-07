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

	// Seeded priors: a team's first appearance starts from its prior
	// rating (see EloSeedRating) and its pseudo-games count toward warmup,
	// the same "a prior is information" stance the record models take.
	sportKey string
	priors   PriorsProvider
	seeded   map[string]bool
	priorN   map[string]int
	mu       sync.Mutex // guards lazy seeding after the table is published
}

// Elo rates teams from stored game results with a margin-of-victory
// multiplier. Ratings rebuild from history on demand (cached ~10 min), so
// they survive restarts without any schema — a 17-game football season
// never converges on win/loss records, but points margins do.
type Elo struct {
	games       GamesProvider
	warmupGames int
	priors      PriorsProvider // optional

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

// WithPriors seeds each team's starting rating and warmup count from its
// stored pseudo-record (see EloSeedRating). Returns e for chaining.
func (e *Elo) WithPriors(priors PriorsProvider) *Elo {
	e.priors = priors
	e.cache = make(map[string]*eloRatings)
	return e
}

// EloSeedRating converts a prior pseudo-record to a starting rating by
// inverting the win-probability curve: a .750 prior sits 191 points above
// initial. Empty or even priors map to EloInitial.
func EloSeedRating(priorWins, priorLosses float64) float64 {
	n := priorWins + priorLosses
	if n <= 0 {
		return EloInitial
	}
	p := clamp(priorWins/n, 0.05, 0.95)
	return EloInitial + 400*math.Log10(p/(1-p))
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
	seen := r.seen(game.HomeTeam)
	if a := r.seen(game.AwayTeam); a < seen {
		seen = a
	}
	return math.Min(float64(seen)/float64(e.warmupGames), 1)
}

// rating returns a team's current rating, seeding it from priors on first
// sight so an unplayed team still carries last season's information.
func (r *eloRatings) rating(team string) float64 {
	r.seed(team)
	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok := r.ratings[team]; ok {
		return v
	}
	return EloInitial
}

// seen returns games played plus seeded pseudo-games, for the warmup ramp
func (r *eloRatings) seen(team string) int {
	r.seed(team)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.gamesSeen[team] + r.priorN[team]
}

// seed looks a team's prior up once; no-op without a priors source
func (r *eloRatings) seed(team string) {
	if r.priors == nil {
		return
	}
	r.mu.Lock()
	if r.seeded[team] {
		r.mu.Unlock()
		return
	}
	r.seeded[team] = true
	r.mu.Unlock()

	pw, pl := r.priors.TeamPrior(team, r.sportKey) // outside the lock: may hit the DB

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.ratings[team]; !ok && pw+pl > 0 {
		r.ratings[team] = EloSeedRating(pw, pl)
	}
	r.priorN[team] = int(math.Round(pw + pl))
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

	r.mu.Lock()
	defer r.mu.Unlock()
	r.ratings[g.HomeTeam] = home + delta
	r.ratings[g.AwayTeam] = away - delta
	r.gamesSeen[g.HomeTeam]++
	r.gamesSeen[g.AwayTeam]++
}

// newEloRatings creates an empty, optionally prior-seeded, rating table
func newEloRatings(sportKey string, priors PriorsProvider) *eloRatings {
	return &eloRatings{
		ratings:   make(map[string]float64),
		gamesSeen: make(map[string]int),
		builtAt:   time.Now(),
		sportKey:  sportKey,
		priors:    priors,
		seeded:    make(map[string]bool),
		priorN:    make(map[string]int),
	}
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
	return EloHistoryFrom(games, nil)
}

// EloSeeder returns a team's starting rating; nil means EloInitial for all
type EloSeeder func(team string) float64

// EloSeederFromPriors adapts a priors source to an EloSeeder for one sport
func EloSeederFromPriors(priors PriorsProvider, sportKey string) EloSeeder {
	if priors == nil {
		return nil
	}
	return func(team string) float64 {
		return EloSeedRating(priors.TeamPrior(team, sportKey))
	}
}

// EloHistoryFrom is EloHistory with seeded starting ratings, so a display
// built from it matches a prior-seeded estimator.
func EloHistoryFrom(games []types.Game, seed EloSeeder) map[string][]RatingPoint {
	r := newEloRatings("", nil)
	if seed != nil {
		for _, g := range games {
			for _, team := range []string{g.HomeTeam, g.AwayTeam} {
				if _, ok := r.ratings[team]; !ok {
					r.ratings[team] = seed(team)
				}
			}
		}
	}
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

	r := newEloRatings(sportKey, e.priors)

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
