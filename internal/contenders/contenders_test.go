package contenders

import (
	"strings"
	"testing"

	"github.com/austinbyron/betanalysis/internal/analysis"
	"github.com/austinbyron/betanalysis/internal/config"
	"github.com/austinbyron/betanalysis/pkg/types"
)

type fakeStats struct{}

func (fakeStats) TeamRecord(string, string) (int, int) { return 0, 0 }

func raceConfig(models ...config.ModelConfig) *config.Config {
	return &config.Config{
		Analysis:  config.AnalysisConfig{ModelType: "thompson", MarketWeight: 0.7, Models: models},
		Trading:   config.TradingConfig{PortfolioID: "default", MinOdds: 1.5, MinExpectedValue: 0.05},
		SportKeys: []string{"baseball_mlb"},
	}
}

func TestBuildConstructsLineup(t *testing.T) {
	cfg := raceConfig(
		config.ModelConfig{Name: "thompson-pitcher", ModelType: "thompson", Adjusters: []string{"mlb_pitcher"}, Portfolio: "default"},
		config.ModelConfig{Name: "thompson-raw", ModelType: "thompson"},
	)

	cs, err := Build(cfg, fakeStats{}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(cs) != 2 {
		t.Fatalf("contenders = %d, want 2", len(cs))
	}
	if cs[0].Portfolio != "default" {
		t.Errorf("explicit portfolio = %q, want default", cs[0].Portfolio)
	}
	if cs[1].Portfolio != "thompson-raw" {
		t.Errorf("defaulted portfolio = %q, want contender name", cs[1].Portfolio)
	}
	if !cs[0].CoversSport("baseball_mlb") {
		t.Error("no sports filter must cover all configured sports")
	}
}

func TestBuildLegacySingleModel(t *testing.T) {
	cfg := raceConfig() // no models list -> synthesized thompson w/ pitcher
	cs, err := Build(cfg, fakeStats{}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(cs) != 1 || cs[0].Name != "thompson" || cs[0].Portfolio != "default" {
		t.Fatalf("legacy contender = %+v", cs)
	}
}

func TestBuildRejectsBadConfig(t *testing.T) {
	cases := []struct {
		name   string
		models []config.ModelConfig
		want   string
	}{
		{"empty name", []config.ModelConfig{{Name: "", ModelType: "thompson"}}, "name"},
		{"duplicate", []config.ModelConfig{{Name: "a", ModelType: "thompson"}, {Name: "a", ModelType: "historical"}}, "duplicate"},
		{"unknown model", []config.ModelConfig{{Name: "a", ModelType: "oracle"}}, "unknown model type"},
		{"unknown adjuster", []config.ModelConfig{{Name: "a", ModelType: "thompson", Adjusters: []string{"vibes"}}}, "unknown adjuster"},
	}
	for _, tc := range cases {
		if _, err := Build(raceConfig(tc.models...), fakeStats{}, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.want)
		}
	}
}

func TestCoversSportFilter(t *testing.T) {
	c := Contender{Sports: []string{"americanfootball_nfl"}}
	if c.CoversSport("baseball_mlb") || !c.CoversSport("americanfootball_nfl") {
		t.Error("sports filter not honored")
	}
}

func TestBuildWiresWinnerStrategy(t *testing.T) {
	cfg := raceConfig(
		config.ModelConfig{Name: "winner-historical", ModelType: "historical", Strategy: "winner", Adjusters: []string{"home_field"}},
		config.ModelConfig{Name: "ev-historical", ModelType: "historical"},
	)
	cs, err := Build(cfg, fakeStats{}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := cs[0].Selector.Strategy(); got != analysis.StrategyWinner {
		t.Errorf("strategy = %q, want winner", got)
	}
	if got := cs[1].Selector.Strategy(); got != analysis.StrategyEV {
		t.Errorf("default strategy = %q, want ev", got)
	}
	// home_field with the default shift moves a 50/50 record toward home
	v := cs[0].Selector.View(types.Game{HomeTeam: "H", AwayTeam: "A"})
	if !v.HasAdjusters || v.AdjHome <= v.RawHome {
		t.Errorf("home_field adjuster not applied: %+v", v)
	}
}

func TestBuildRejectsUnknownStrategy(t *testing.T) {
	cfg := raceConfig(config.ModelConfig{Name: "a", ModelType: "historical", Strategy: "yolo"})
	if _, err := Build(cfg, fakeStats{}, nil); err == nil || !strings.Contains(err.Error(), "unknown strategy") {
		t.Fatalf("err = %v, want unknown strategy", err)
	}
}

func TestBuildRejectsBadWinnerOverrides(t *testing.T) {
	bad := 1.5
	cfg := raceConfig(config.ModelConfig{Name: "a", ModelType: "historical", Strategy: "winner", MinWinProb: &bad})
	if _, err := Build(cfg, fakeStats{}, nil); err == nil || !strings.Contains(err.Error(), "min_win_prob") {
		t.Fatalf("err = %v, want min_win_prob complaint", err)
	}
}
