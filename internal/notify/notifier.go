package notify

import (
	"time"

	"github.com/rs/zerolog/log"

	"github.com/austinbyron/betanalysis/internal/consensus"
	"github.com/austinbyron/betanalysis/pkg/types"
)

// Store reads and appends the notification dedup ledger.
type Store interface {
	GetConsensusNotifications(gameID string) ([]types.ConsensusNotification, error)
	SaveConsensusNotification(gameID, selection string, strong bool) error
}

// Sender delivers one embed; *Discord in production.
type Sender interface {
	Send(Embed) error
}

// Notifier turns consensus picks into at most two Discord messages per
// game: the first qualifying pick, and one follow-up if that same pick
// later upgrades to Strong. Everything else is silent.
type Notifier struct {
	store   Store
	sender  Sender
	baseURL string
	now     func() time.Time // seam for tests
}

// New builds a Notifier with the production clock.
func New(store Store, sender Sender, baseURL string) *Notifier {
	return &Notifier{store: store, sender: sender, baseURL: baseURL, now: time.Now}
}

// Process applies the pre-game filter and dedup rules to one cycle's
// consensus picks. Failures are logged; a failed send writes no ledger
// row, so the next cycle retries naturally.
func (n *Notifier) Process(picks []consensus.Pick) {
	for _, p := range picks {
		// Pre-game only: commence_time is a UTC wall clock.
		if !p.Game.CommenceTime.After(n.now().UTC()) {
			continue
		}

		rows, err := n.store.GetConsensusNotifications(p.Game.ID)
		if err != nil {
			log.Error().Err(err).Str("game", p.Game.ID).Msg("notify: ledger read failed")
			continue
		}

		var notified, strongNotified bool
		var firstSelection string
		for _, r := range rows {
			if !notified {
				firstSelection = r.Selection // rows come oldest-first
			}
			notified = true
			if r.Strong {
				strongNotified = true
			}
		}

		var upgrade bool
		switch {
		case strongNotified:
			continue
		case !notified:
			upgrade = false
		case p.Strong && p.Selection == firstSelection:
			upgrade = true
		default:
			continue
		}

		if err := n.sender.Send(BuildEmbed(p, upgrade, n.baseURL)); err != nil {
			log.Error().Err(err).Str("game", p.Game.ID).Msg("notify: discord send failed")
			continue
		}
		if err := n.store.SaveConsensusNotification(p.Game.ID, p.Selection, p.Strong || upgrade); err != nil {
			// Worst case: a duplicate message next cycle. Log loudly.
			log.Error().Err(err).Str("game", p.Game.ID).Msg("notify: ledger write failed after send")
			continue
		}
		log.Info().Str("game", p.Game.ID).Str("selection", p.Selection).
			Int("votes", p.Votes).Bool("strong", p.Strong).Bool("upgrade", upgrade).
			Msg("notify: consensus pick sent")
	}
}
