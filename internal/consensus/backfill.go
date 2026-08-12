package consensus

import (
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/austinbyron/betanalysis/pkg/types"
)

// BackfillStore is the storage surface Backfill needs: the Discord
// ledger to replay, historical odds to price each pick, and the record
// writes shared with the live Recorder.
type BackfillStore interface {
	RecordStore
	GetAllConsensusNotifications() ([]types.ConsensusNotification, error)
	GetGameByID(id string) (*types.Game, error)
	GetOddsForGameAt(gameID string, asOf time.Time) ([]types.GameOdds, error)
}

// Backfill replays the Discord notification ledger into the consensus
// record — the one-time seed for picks that predate live recording. Each
// game's first ledger row becomes a pick priced at the best moneyline
// found as of that ping; a later strong row on the same side becomes the
// upgrade stamp. Ledger rows never stored votes or probabilities, so
// those stay NULL and the row is marked backfilled. Idempotent: the
// store's first-write-wins insert makes re-runs harmless.
func Backfill(store BackfillStore, stake float64) (created, upgraded, skipped int, err error) {
	rows, err := store.GetAllConsensusNotifications()
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to read notification ledger: %w", err)
	}

	seen := make(map[string]string) // game id -> first selection this run
	for _, row := range rows {
		if sel, ok := seen[row.GameID]; ok {
			// Second ledger row for a game is the strong upgrade — the
			// store ignores side flips and strong-at-capture rows.
			if row.Strong && row.Selection == sel {
				updated, err := store.UpgradeConsensusPickStrong(row.GameID, row.Selection, row.SentAt)
				if err != nil {
					log.Error().Err(err).Str("game", row.GameID).Msg("backfill: upgrade failed")
					continue
				}
				if updated {
					upgraded++
				}
			}
			continue
		}

		// Leaving the game out of seen on either skip below is deliberate: a
		// later strong row for the same game then falls through as if it
		// were first sight, capturing the pick at the strong ping's
		// time/odds rather than losing it entirely.
		if game, err := store.GetGameByID(row.GameID); err != nil || game == nil {
			log.Warn().Str("game", row.GameID).Msg("backfill: game row missing, skipping")
			skipped++
			continue
		}

		bestOdds, bestBook, ok := bestMoneylineAt(store, row.GameID, row.Selection, row.SentAt)
		if !ok {
			log.Warn().Str("game", row.GameID).Time("sent_at", row.SentAt).
				Msg("backfill: no moneyline odds as of ping, skipping")
			skipped++
			continue
		}

		cp := types.ConsensusPick{
			GameID:     row.GameID,
			Selection:  row.Selection,
			Strong:     row.Strong,
			BestOdds:   bestOdds,
			BestBook:   bestBook,
			Stake:      stake,
			Status:     types.BetStatusPending,
			Backfilled: true,
			CreatedAt:  row.SentAt,
		}
		inserted, err := store.RecordConsensusPick(cp)
		if err != nil {
			log.Error().Err(err).Str("game", row.GameID).Msg("backfill: record failed")
			continue
		}
		if inserted {
			created++
		}
		// Whether or not this insert actually landed a row (the daemon may
		// have live-recorded the game before backfill ran), the game is now
		// on record — route any later strong row through the upgrade path
		// instead of a second create attempt.
		seen[row.GameID] = row.Selection
	}

	return created, upgraded, skipped, nil
}

// bestMoneylineAt finds the best price for a selection across bookmakers
// as of a moment, from the append-only odds history.
func bestMoneylineAt(store BackfillStore, gameID, selection string, asOf time.Time) (float64, string, bool) {
	odds, err := store.GetOddsForGameAt(gameID, asOf)
	if err != nil {
		log.Error().Err(err).Str("game", gameID).Msg("backfill: odds lookup failed")
		return 0, "", false
	}

	var best float64
	var book string
	for _, o := range odds {
		if o.MarketType != types.MarketMoneyline {
			continue
		}
		var price *float64
		switch selection {
		case types.OutcomeHome:
			price = o.HomeOdds
		case types.OutcomeAway:
			price = o.AwayOdds
		}
		if price != nil && *price > best {
			best, book = *price, o.Bookmaker
		}
	}
	return best, book, best > 0
}
