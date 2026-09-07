package books

import (
	"time"

	"github.com/austinbyron/betanalysis/pkg/types"
	"github.com/rs/zerolog/log"
)

// Store is the storage surface a poll needs
type Store interface {
	GetUpcomingGames(sportKey string) ([]types.Game, error)
	SaveOdds(odds []types.GameOdds) error
}

// PollResult summarizes one source x sport poll for logs and tests
type PollResult struct {
	Source, Sport string
	Lines         int
	Rows          int
	Unmatched     int
	Err           error
}

// Poll fetches every source for every sport that has upcoming games and
// stores the matched snapshots. Each source x sport fails independently.
func Poll(store Store, sources []Source, sports []string, now time.Time) []PollResult {
	var results []PollResult
	for _, sport := range sports {
		games, err := store.GetUpcomingGames(sport)
		if err != nil {
			log.Error().Err(err).Str("sport", sport).Msg("books: upcoming games failed")
			continue
		}
		if len(games) == 0 {
			continue // nothing to attach lines to; don't poll for its own sake
		}
		for _, src := range sources {
			r := PollResult{Source: src.Name(), Sport: sport}
			lines, err := src.Fetch(sport)
			if err != nil {
				r.Err = err
				results = append(results, r)
				log.Warn().Err(err).Str("source", src.Name()).Str("sport", sport).Msg("books: fetch failed")
				continue
			}
			odds, unmatched := Match(lines, games, src.Name(), now)
			r.Lines, r.Rows, r.Unmatched = len(lines), len(odds), unmatched
			if len(odds) > 0 {
				if err := store.SaveOdds(odds); err != nil {
					r.Err = err
					log.Error().Err(err).Str("source", src.Name()).Str("sport", sport).Msg("books: save failed")
				}
			}
			results = append(results, r)
			log.Info().Str("source", src.Name()).Str("sport", sport).
				Int("lines", r.Lines).Int("rows", r.Rows).Int("unmatched", r.Unmatched).Msg("books: polled")
		}
	}
	return results
}
