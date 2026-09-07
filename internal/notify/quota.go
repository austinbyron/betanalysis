package notify

import (
	"fmt"
	"sync"

	"github.com/rs/zerolog/log"
)

// QuotaWatch turns the Odds API credit counters into one Discord alert per
// dip below the floor: it fires when remaining first crosses under, stays
// quiet while it remains under, and re-arms once remaining recovers (the
// monthly reset). A nil sender only logs.
type QuotaWatch struct {
	sender Sender
	floor  float64

	mu      sync.Mutex
	alerted bool
}

// NewQuotaWatch creates a watch; floor <= 0 disables it.
func NewQuotaWatch(sender Sender, floor float64) *QuotaWatch {
	return &QuotaWatch{sender: sender, floor: floor}
}

// Observe feeds one counter reading (the api client's quota hook).
func (w *QuotaWatch) Observe(remaining, used float64) {
	if w == nil || w.floor <= 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	if remaining >= w.floor {
		w.alerted = false
		return
	}
	if w.alerted {
		return
	}
	w.alerted = true

	log.Warn().Float64("remaining", remaining).Float64("used", used).Float64("floor", w.floor).
		Msg("Odds API quota below floor — collection paused until it recovers")
	if w.sender == nil {
		return
	}
	embed := Embed{
		Title: fmt.Sprintf("Odds API quota low: %.0f credits remaining", remaining),
		Color: 0xd03b3b,
		Fields: []EmbedField{
			{Name: "Remaining", Value: fmt.Sprintf("%.0f", remaining), Inline: true},
			{Name: "Used", Value: fmt.Sprintf("%.0f", used), Inline: true},
			{Name: "Floor", Value: fmt.Sprintf("%.0f", w.floor), Inline: true},
			{Name: "Effect", Value: "Odds/scores collection is paused below the floor; " +
				"one probe per day checks for the monthly reset.", Inline: false},
		},
	}
	if err := w.sender.Send(embed); err != nil {
		log.Error().Err(err).Msg("Odds API quota alert failed to send")
	}
}
