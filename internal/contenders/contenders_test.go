package contenders

import (
	"strings"
	"testing"

	"github.com/austinbyron/betanalysis/internal/config"
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
