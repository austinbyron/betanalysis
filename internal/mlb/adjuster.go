package mlb

import (
	"math"

	"github.com/austinbyron/betanalysis/pkg/types"
	"github.com/rs/zerolog/log"
)

// Win-probability model: a starter's quality (runs per 9 vs league average)
// scaled by the share of the game a starter typically covers, converted to
// win probability at ~0.11 wins per run of per-game margin (the pythagorean
// slope around a 4.5-run environment). An ace (+1.2 runs/9) over an average
// starter moves his team about +8.6% — matching observed moneyline swings.
const (
	starterInningsShare = 0.65
	runsToWinProb       = 0.11
	minProb             = 0.05
	maxProb             = 0.95
)

// PitcherAdjuster refines MLB game probabilities with probable starter
// quality. Non-MLB games and any lookup failure pass through unchanged —
// the adjuster must never block a trading cycle.
type PitcherAdjuster struct {
	client *Client
}

// NewPitcherAdjuster creates a pitcher adjuster
func NewPitcherAdjuster(client *Client) *PitcherAdjuster {
	return &PitcherAdjuster{client: client}
}

// Name identifies the adjuster
func (p *PitcherAdjuster) Name() string { return "mlb_pitchers" }

// Adjust shifts the home win probability by the starter quality differential
func (p *PitcherAdjuster) Adjust(game types.Game, homeProb, awayProb float64) (float64, float64) {
	if game.SportKey != types.SportMLB {
		return homeProb, awayProb
	}

	homeStarter, awayStarter, err := p.client.StartersForGame(game.HomeTeam, game.AwayTeam, game.CommenceTime)
	if err != nil {
		log.Debug().Err(err).Str("game", game.ID).Msg("Pitcher lookup failed; probabilities unadjusted")
		return homeProb, awayProb
	}

	homeDelta := p.starterDelta(homeStarter, game.ID)
	awayDelta := p.starterDelta(awayStarter, game.ID)
	if homeDelta == 0 && awayDelta == 0 {
		return homeProb, awayProb
	}

	adjustedHome := clamp(homeProb+homeDelta-awayDelta, minProb, maxProb)

	log.Debug().
		Str("game", game.ID).
		Str("home_sp", starterName(homeStarter)).
		Str("away_sp", starterName(awayStarter)).
		Float64("home_prob_before", homeProb).
		Float64("home_prob_after", adjustedHome).
		Msg("Pitcher adjustment applied")

	return adjustedHome, 1 - adjustedHome
}

// starterDelta converts one starter's quality into a win probability delta
// for his own team. No announced probable = no adjustment for that side.
func (p *PitcherAdjuster) starterDelta(starter *Starter, gameID string) float64 {
	if starter == nil {
		return 0
	}
	quality, err := p.client.PitcherQuality(starter.ID)
	if err != nil {
		log.Debug().Err(err).Str("game", gameID).Str("pitcher", starter.Name).Msg("Pitcher quality lookup failed")
		return 0
	}
	return runsToWinProb * starterInningsShare * quality
}

func starterName(s *Starter) string {
	if s == nil {
		return "unannounced"
	}
	return s.Name
}

func clamp(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}
