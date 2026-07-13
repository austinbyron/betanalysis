// Package priors seeds per-team Beta priors from last season's final
// standings, so a new season doesn't start every team at 50/50 and the
// warmup gate doesn't hold every model silent for weeks.
package priors

import (
	"fmt"
	"math"
	"time"

	"github.com/austinbyron/betanalysis/internal/espn"
	"github.com/austinbyron/betanalysis/pkg/types"
	"github.com/rs/zerolog/log"
)

// DefaultPseudoGames is the prior strength: a seeded team starts as if it
// had already played this many games at its regressed win rate.
const DefaultPseudoGames = 16.0

// regression pulls last season's win rate 1/3 of the way back to .500 —
// rosters turn over, luck doesn't repeat.
const regression = 1.0 / 3.0

// Store is the storage surface seeding needs
type Store interface {
	SeedTeamPrior(prior types.TeamPrior) error
	CountFinishedGames(sportKey string) (int, error)
	HasTeamPriors(sportKey string) (bool, error)
}

// StandingsProvider supplies a season's final records (ESPN in production)
type StandingsProvider interface {
	Standings(sportKey string, season int) ([]espn.TeamRecord, error)
}

// SeedFromSeason seeds priors for a sport from one season's standings:
// p = 0.5*r + winPct*(1-r) with r = 1/3, then alpha = p*K, beta = (1-p)*K.
// Returns the number of teams seeded. Reseeding upserts.
func SeedFromSeason(store Store, standings StandingsProvider, sportKey string, season int, pseudoGames float64) (int, error) {
	if pseudoGames <= 0 {
		pseudoGames = DefaultPseudoGames
	}

	records, err := standings.Standings(sportKey, season)
	if err != nil {
		return 0, err
	}

	source := fmt.Sprintf("espn-standings-%d", season)
	seeded := 0
	for _, r := range records {
		winPct := float64(r.Wins) / float64(r.Wins+r.Losses)
		p := 0.5*regression + winPct*(1-regression)
		p = math.Max(0.05, math.Min(0.95, p))

		prior := types.TeamPrior{
			TeamName:    r.Name,
			SportKey:    sportKey,
			PriorWins:   p * pseudoGames,
			PriorLosses: (1 - p) * pseudoGames,
			Source:      source,
		}
		if err := store.SeedTeamPrior(prior); err != nil {
			return seeded, fmt.Errorf("failed to seed %s: %w", r.Name, err)
		}
		seeded++
	}

	return seeded, nil
}

// AutoSeed seeds every configured sport that is cold — no finished games
// recorded and no priors yet. This is the season-expansion hook: add a
// sport to config, deploy, and its teams start at last season's regressed
// strength instead of 50/50. Sports already playing (or already seeded)
// are left alone, and every failure is logged and skipped — a dead ESPN
// endpoint must never stop the daemon.
func AutoSeed(store Store, standings StandingsProvider, sports []string, now time.Time) {
	season := now.Year() - 1 // the most recently completed season

	for _, sport := range sports {
		games, err := store.CountFinishedGames(sport)
		if err != nil {
			log.Error().Err(err).Str("sport", sport).Msg("Prior auto-seed: game count failed")
			continue
		}
		if games > 0 {
			continue // the sport has real records; priors would be noise
		}
		has, err := store.HasTeamPriors(sport)
		if err != nil {
			log.Error().Err(err).Str("sport", sport).Msg("Prior auto-seed: priors check failed")
			continue
		}
		if has {
			continue
		}

		seeded, err := SeedFromSeason(store, standings, sport, season, DefaultPseudoGames)
		if err != nil {
			log.Warn().Err(err).Str("sport", sport).Msg("Prior auto-seed failed; models start cold")
			continue
		}
		log.Info().Str("sport", sport).Int("teams", seeded).Int("season", season).
			Msg("Auto-seeded team priors from last season's standings")
	}
}
