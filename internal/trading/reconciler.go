package trading

import (
	"fmt"
	"time"

	"github.com/austinbyron/betanalysis/internal/espn"
	"github.com/austinbyron/betanalysis/pkg/types"
	"github.com/rs/zerolog/log"
)

// The Odds API reissues event ids when a game is rescheduled and drops
// them when it is postponed, so a bet's game can stay 'scheduled' forever:
// the scores endpoint only reports current ids, and only 3 days back. The
// Reconciler resolves such orphaned bets against ESPN instead.
const (
	// reconcileGrace leaves freshly-commenced games to the normal
	// scores-driven settlement path.
	reconcileGrace = 12 * time.Hour
	// reconcileVoidAfter voids bets on games ESPN has no record of either
	// once they are this stale.
	reconcileVoidAfter = 72 * time.Hour
)

// ResultSource resolves a game's real-world outcome independently of the
// odds feed. *espn.Linker implements it.
type ResultSource interface {
	GameResult(game types.Game) (espn.Result, bool)
}

// Reconciler settles bets whose games the odds feed abandoned
type Reconciler struct {
	store   Store
	results ResultSource
}

// NewReconciler creates a new reconciler
func NewReconciler(store Store, results ResultSource) *Reconciler {
	return &Reconciler{store: store, results: results}
}

// ReconcileStaleBets resolves pending bets and previews on games that are
// long past commence time yet still 'scheduled'. A final ESPN score
// settles them on the real outcome and marks the game 'superseded' — NOT
// 'finished', because estimators replay finished games and the real result
// already exists under the replacement event id. Postponed, canceled, or
// vanished games void their bets: stake refunded, record untouched.
func (r *Reconciler) ReconcileStaleBets() error {
	// commence_time is stored as a UTC wall clock; pass a UTC cutoff so the
	// timestamp column comparison isn't skewed by the server's local zone.
	games, err := r.store.GetStaleScheduledGames(time.Now().UTC().Add(-reconcileGrace))
	if err != nil {
		return fmt.Errorf("failed to get stale scheduled games: %w", err)
	}
	if len(games) == 0 {
		return nil
	}

	log.Info().Int("games", len(games)).Msg("Reconciling bets on abandoned games")

	for _, game := range games {
		res, ok := r.results.GameResult(game)
		switch {
		case ok && res.Status == espn.ResultFinal:
			r.resolve(game, res)
		case ok && (res.Status == espn.ResultPostponed || res.Status == espn.ResultCanceled):
			r.void(game, res.Status)
		case time.Since(game.CommenceTime) > reconcileVoidAfter:
			log.Warn().Str("game", game.ID).Str("matchup", game.AwayTeam+" @ "+game.HomeTeam).
				Msg("Game vanished from both feeds past cutoff — voiding its bets")
			r.void(game, "vanished")
		default:
			// No verdict yet (ESPN unreachable, no match inside the cutoff,
			// or still in progress) — retry on the next settlement run.
		}
	}
	return nil
}

// resolve settles the game's bets against the real ESPN outcome
func (r *Reconciler) resolve(game types.Game, res espn.Result) {
	decided := game
	decided.HomeScore, decided.AwayScore = &res.HomeScore, &res.AwayScore

	settled := 0
	for _, bet := range r.pendingBets(game.ID) {
		settledBet, err := settleAgainst(r.store, bet, &decided)
		if err != nil {
			log.Error().Err(err).Str("bet", bet.ID).Msg("Failed to settle reconciled bet")
			continue
		}
		log.Info().Str("bet", settledBet.ID).Str("status", settledBet.Status).
			Float64("payout", *settledBet.ActualWin).Msg("Reconciled bet settled on ESPN result")
		settled++
	}
	for _, pb := range r.pendingPreviews(game.ID) {
		if err := settlePreviewAgainst(r.store, pb, &decided); err != nil {
			log.Error().Err(err).Str("preview", pb.ID).Msg("Failed to settle reconciled preview")
		}
	}

	if err := r.store.UpdateGameScores(game.ID, res.HomeScore, res.AwayScore, "superseded"); err != nil {
		log.Error().Err(err).Str("game", game.ID).Msg("Failed to mark game superseded")
		return
	}
	log.Info().Str("game", game.ID).Int("bets", settled).
		Int("home", res.HomeScore).Int("away", res.AwayScore).
		Msg("Abandoned game superseded by ESPN result")
}

// void refunds the game's bets and retires the game row
func (r *Reconciler) void(game types.Game, reason string) {
	for _, bet := range r.pendingBets(game.ID) {
		refund := bet.Stake
		now := time.Now()
		bet.Status = types.BetStatusVoid
		bet.ActualWin = &refund
		bet.SettledAt = &now

		delta := types.PortfolioDelta{
			Balance:         refund,
			TotalWagered:    -bet.Stake,
			ActiveBetsCount: -1,
			ActiveBetsValue: -bet.Stake,
		}
		if err := r.store.SettleBet(bet, delta); err != nil {
			log.Error().Err(err).Str("bet", bet.ID).Msg("Failed to void bet")
			continue
		}
		log.Info().Str("bet", bet.ID).Str("reason", reason).Float64("refund", refund).Msg("Bet voided")
	}

	for _, pb := range r.pendingPreviews(game.ID) {
		refund := pb.Stake // would-be P/L of a void is zero
		now := time.Now()
		pb.Status = types.BetStatusVoid
		pb.Payout = &refund
		pb.SettledAt = &now
		if err := r.store.SettlePreviewBet(pb); err != nil {
			log.Error().Err(err).Str("preview", pb.ID).Msg("Failed to void preview")
		}
	}

	if err := r.store.UpdateGameScores(game.ID, 0, 0, "postponed"); err != nil {
		log.Error().Err(err).Str("game", game.ID).Msg("Failed to mark game postponed")
	}
}

func (r *Reconciler) pendingBets(gameID string) []types.Bet {
	all, err := r.store.GetPendingBets()
	if err != nil {
		log.Error().Err(err).Msg("Failed to get pending bets")
		return nil
	}
	var out []types.Bet
	for _, b := range all {
		if b.GameID == gameID {
			out = append(out, b)
		}
	}
	return out
}

func (r *Reconciler) pendingPreviews(gameID string) []types.PreviewBet {
	all, err := r.store.GetPendingPreviewBets()
	if err != nil {
		log.Error().Err(err).Msg("Failed to get pending preview bets")
		return nil
	}
	var out []types.PreviewBet
	for _, pb := range all {
		if pb.GameID == gameID {
			out = append(out, pb)
		}
	}
	return out
}
