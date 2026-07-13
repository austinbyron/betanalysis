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

		out = append(out, Contender{
			Name:      m.Name,
			Portfolio: portfolio,
			Sports:    m.Sports,
			Selector:  analysis.NewSelector(estimator, m.Name, mw, cfg.Trading.MinOdds, cfg.Trading.MinExpectedValue),
		})
	}
	return out, nil
}
