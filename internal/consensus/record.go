package consensus

import (
	"time"

	"github.com/rs/zerolog/log"

	"github.com/austinbyron/betanalysis/pkg/types"
)

// RecordStore is the storage surface the Recorder writes.
type RecordStore interface {
	RecordConsensusPick(cp types.ConsensusPick) (bool, error)
	UpgradeConsensusPickStrong(gameID, selection string, at time.Time) (bool, error)
}

// Recorder persists the shortlist's track record: each game's consensus
// pick at first appearance, graded later at a flat notional stake. The
// store's first-write-wins insert makes re-recording every cycle safe.
type Recorder struct {
	store RecordStore
	stake float64
	now   func() time.Time // seam for tests
}

// NewRecorder builds a Recorder with the production clock.
func NewRecorder(store RecordStore, stake float64) *Recorder {
	return &Recorder{store: store, stake: stake, now: time.Now}
}

// Record persists one cycle's consensus picks. Pre-game only, mirroring
// the notifier: commence_time is a UTC wall clock. Failures are logged;
// the next cycle retries naturally.
func (r *Recorder) Record(picks []Pick) {
	for _, p := range picks {
		if !p.Game.CommenceTime.After(r.now().UTC()) {
			continue
		}

		votes, total := p.Votes, p.Total
		avgProb, minEV := p.AvgProb, p.MinEV
		cp := types.ConsensusPick{
			GameID:    p.Game.ID,
			Selection: p.Selection,
			Votes:     &votes,
			Total:     &total,
			Strong:    p.Strong,
			AvgProb:   &avgProb,
			MinEV:     &minEV,
			BestOdds:  p.BestOdds,
			BestBook:  p.BestBook,
			Stake:     r.stake,
			Status:    types.BetStatusPending,
			CreatedAt: r.now().UTC(),
		}
		if _, err := r.store.RecordConsensusPick(cp); err != nil {
			log.Error().Err(err).Str("game", p.Game.ID).Msg("consensus: record failed")
			continue
		}
		// A strong pick may be an upgrade of an earlier regular capture on
		// the same side — the store stamps it exactly once and ignores
		// strong-at-capture rows, settled rows, and side flips.
		if p.Strong {
			if _, err := r.store.UpgradeConsensusPickStrong(p.Game.ID, p.Selection, r.now().UTC()); err != nil {
				log.Error().Err(err).Str("game", p.Game.ID).Msg("consensus: strong upgrade failed")
			}
		}
	}
}
