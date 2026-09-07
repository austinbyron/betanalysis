// Package contenders builds the model-race lineup: one selector per
// configured model, sharing expensive adjusters (one pitcher cache).
package contenders

import (
	"fmt"

	"github.com/austinbyron/betanalysis/internal/analysis"
	"github.com/austinbyron/betanalysis/internal/config"
	"github.com/austinbyron/betanalysis/internal/mlb"
)

// Contender is one racing model: a named selector bound to its own
// portfolio and, optionally, a subset of the configured sports.
type Contender struct {
	Name      string
	Portfolio string
	Sports    []string
	Selector  *analysis.Selector
}

// CoversSport reports whether this contender bets the given sport key.
func (c Contender) CoversSport(sport string) bool {
	if len(c.Sports) == 0 {
		return true
	}
	for _, s := range c.Sports {
		if s == sport {
			return true
		}
	}
	return false
}

// Build constructs the race lineup from cfg.Contenders(). Adjuster
// instances are shared across contenders so per-day caches are hit once.
// games feeds score-based models (elo); nil disables them.
func Build(cfg *config.Config, stats analysis.StatsProvider, games analysis.GamesProvider) ([]Contender, error) {
	var pitcher analysis.GameAdjuster // lazily built, shared

	seen := make(map[string]bool)
	var out []Contender
	for _, m := range cfg.Contenders() {
		if m.Name == "" {
			return nil, fmt.Errorf("model with empty name in analysis.models")
		}
		if seen[m.Name] {
			return nil, fmt.Errorf("duplicate model name %q", m.Name)
		}
		seen[m.Name] = true

		analysisCfg := cfg.Analysis
		analysisCfg.ModelType = m.ModelType
		estimator, err := analysis.NewEstimator(analysisCfg, stats, games)
		if err != nil {
			return nil, fmt.Errorf("model %q: %w", m.Name, err)
		}

		for _, name := range m.Adjusters {
			switch name {
			case "mlb_pitcher":
				if pitcher == nil {
					pitcher = mlb.NewPitcherAdjuster(mlb.NewClient())
				}
				estimator = analysis.WithAdjusters(estimator, pitcher)
			case "home_field":
				estimator = analysis.WithAdjusters(estimator, analysis.NewHomeFieldAdjuster(HomeFieldShift(cfg)))
			default:
				return nil, fmt.Errorf("model %q: unknown adjuster %q", m.Name, name)
			}
		}

		mw := cfg.Analysis.MarketWeight
		if m.MarketWeight != nil {
			mw = *m.MarketWeight
		}
		portfolio := m.Portfolio
		if portfolio == "" {
			portfolio = m.Name
		}

		selector := analysis.NewSelector(estimator, m.Name, mw, cfg.Trading.MinOdds, cfg.Trading.MinExpectedValue)
		if err := ApplyStrategy(selector, m); err != nil {
			return nil, fmt.Errorf("model %q: %w", m.Name, err)
		}

		out = append(out, Contender{
			Name:      m.Name,
			Portfolio: portfolio,
			Sports:    m.Sports,
			Selector:  selector,
		})
	}
	return out, nil
}

// HomeFieldShift returns the configured home_field adjuster shift or its default
func HomeFieldShift(cfg *config.Config) float64 {
	if cfg.Analysis.HomeFieldShift > 0 {
		return cfg.Analysis.HomeFieldShift
	}
	return config.DefaultHomeFieldShift
}

// ApplyStrategy configures the selector's selection rule from the model
// config, filling winner-strategy defaults. Shared with the backtest so a
// contender replays with the rule it trades live.
func ApplyStrategy(selector *analysis.Selector, m config.ModelConfig) error {
	switch m.Strategy {
	case "", analysis.StrategyEV:
		return nil
	case analysis.StrategyWinner:
		minWin := config.DefaultMinWinProb
		if m.MinWinProb != nil {
			minWin = *m.MinWinProb
		}
		if minWin < 0.5 || minWin >= 1 {
			return fmt.Errorf("min_win_prob %v must be in [0.5, 1)", minWin)
		}
		frac := config.DefaultStakeFraction
		if m.StakeFraction != nil {
			frac = *m.StakeFraction
		}
		if frac <= 0 || frac > 1 {
			return fmt.Errorf("stake_fraction %v must be in (0, 1]", frac)
		}
		selector.WithWinnerStrategy(minWin, frac)
		return nil
	default:
		return fmt.Errorf("unknown strategy %q (want ev or winner)", m.Strategy)
	}
}
