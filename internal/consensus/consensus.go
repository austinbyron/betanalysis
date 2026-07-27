// Package consensus computes the cross-model shortlist: games where most
// of the racing lineup backs the same side. The dashboard renders it and
// the notifier pushes it to Discord — both call into here so they can
// never disagree.
package consensus

import (
	"sort"

	"github.com/rs/zerolog/log"

	"github.com/austinbyron/betanalysis/internal/analysis"
	"github.com/austinbyron/betanalysis/internal/config"
	"github.com/austinbyron/betanalysis/internal/contenders"
	"github.com/austinbyron/betanalysis/pkg/types"
)

// ModelPick is one contender's opinion on one game, input to consensus
type ModelPick struct {
	Model     string
	Selection string
	Prob      float64
	EV        float64
	Odds      float64
	Bookmaker string
}

// GamePicks collects every contender's pick for one game
type GamePicks struct {
	Game  types.Game
	Picks []ModelPick
}

// Pick is a game where most of the lineup backs the same side —
// the informational shortlist for occasional real-money bets. The paper
// engines bet independently; this is a lens, not a fifth bettor.
type Pick struct {
	Game      types.Game
	GameURL   string
	Selection string
	Votes     int
	Total     int     // lineup size
	AvgProb   float64 // mean probability among agreeing picks
	MinEV     float64 // worst EV among agreeing picks
	BestOdds  float64 // best price among agreeing picks
	BestBook  string
	Picks     []ModelPick // the agreeing picks
	Strong    bool        // every contender agrees and MinEV clears the threshold
}

// Build filters games where at least 3 contenders back the same
// side. Sorted by votes then average probability, both descending.
func Build(games map[string]GamePicks, total int, minEV float64) []Pick {
	var out []Pick
	for _, gp := range games {
		bySide := make(map[string][]ModelPick)
		for _, p := range gp.Picks {
			bySide[p.Selection] = append(bySide[p.Selection], p)
		}

		var side string
		var agreeing []ModelPick
		for sel, ps := range bySide {
			if len(ps) > len(agreeing) {
				side, agreeing = sel, ps
			}
		}
		if len(agreeing) < 3 {
			continue
		}

		cp := Pick{
			Game:      gp.Game,
			Selection: side,
			Votes:     len(agreeing),
			Total:     total,
			MinEV:     agreeing[0].EV,
			Picks:     agreeing,
		}
		var probSum float64
		for _, p := range agreeing {
			probSum += p.Prob
			if p.EV < cp.MinEV {
				cp.MinEV = p.EV
			}
			if p.Odds > cp.BestOdds {
				cp.BestOdds = p.Odds
				cp.BestBook = p.Bookmaker
			}
		}
		cp.AvgProb = probSum / float64(len(agreeing))
		cp.Strong = cp.Votes == total && cp.MinEV >= minEV
		out = append(out, cp)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Votes != out[j].Votes {
			return out[i].Votes > out[j].Votes
		}
		return out[i].AvgProb > out[j].AvgProb
	})
	return out
}

// Store is the storage surface Compute reads.
type Store interface {
	GetUpcomingGames(sportKey string) ([]types.Game, error)
	GetOddsForGame(gameID string) ([]types.GameOdds, error)
	GetPortfolio(id string) (*types.Portfolio, error)
}

// Compute gathers each contender's posterior-mean pick per upcoming game
// and runs the consensus filter — the same pick set (stake gate included)
// the dashboard's recommendations table feeds into its consensus section.
// Per-sport read errors are logged and skipped, matching the dashboard.
func Compute(store Store, lineup []contenders.Contender, cfg *config.Config) []Pick {
	const maxGamesPerSport = 25

	bankrolls := make(map[string]float64, len(lineup))
	for _, c := range lineup {
		bankrolls[c.Name] = cfg.Trading.InitialBankroll
		if p, err := store.GetPortfolio(c.Portfolio); err == nil && p != nil {
			bankrolls[c.Name] = p.Balance
		}
	}

	picks := make(map[string]GamePicks)
	for _, sport := range cfg.Sports() {
		games, err := store.GetUpcomingGames(sport)
		if err != nil {
			log.Error().Err(err).Str("sport", sport).Msg("consensus: upcoming games failed")
			continue
		}
		if len(games) > maxGamesPerSport {
			games = games[:maxGamesPerSport]
		}
		for _, game := range games {
			odds, err := store.GetOddsForGame(game.ID)
			if err != nil || len(odds) == 0 {
				continue
			}
			for _, c := range lineup {
				if !c.CoversSport(sport) {
					continue
				}
				bet := c.Selector.RecommendMeanBet(game, odds)
				if bet == nil {
					continue
				}
				stake := analysis.KellyStake(bet.Probability, bet.Odds, bankrolls[c.Name],
					cfg.Trading.KellyFraction, cfg.Trading.MaxStakeFraction)
				if stake < cfg.Trading.MinStake {
					continue
				}
				gp := picks[game.ID]
				gp.Game = game
				gp.Picks = append(gp.Picks, ModelPick{
					Model:     c.Name,
					Selection: bet.Selection,
					Prob:      bet.Probability,
					EV:        bet.ExpectedValue,
					Odds:      bet.Odds,
					Bookmaker: bet.Bookmaker,
				})
				picks[game.ID] = gp
			}
		}
	}
	return Build(picks, len(lineup), cfg.Trading.MinExpectedValue)
}
