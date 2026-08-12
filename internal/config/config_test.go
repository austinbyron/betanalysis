package config

import "testing"

func TestContendersSynthesizesSingleModelWhenListAbsent(t *testing.T) {
	cfg := &Config{
		Analysis:  AnalysisConfig{ModelType: "thompson", MarketWeight: 0.7},
		Trading:   TradingConfig{PortfolioID: "default"},
		SportKeys: []string{"baseball_mlb"},
	}

	got := cfg.Contenders()
	if len(got) != 1 {
		t.Fatalf("contenders = %d, want 1", len(got))
	}
	c := got[0]
	if c.Name != "thompson" || c.Portfolio != "default" {
		t.Errorf("synthesized contender = %+v", c)
	}
	if len(c.Adjusters) != 1 || c.Adjusters[0] != "mlb_pitcher" {
		t.Errorf("MLB configured, want mlb_pitcher adjuster, got %v", c.Adjusters)
	}
}

func TestContendersSynthesisSkipsPitcherWithoutMLB(t *testing.T) {
	cfg := &Config{
		Analysis:  AnalysisConfig{ModelType: "historical"},
		Trading:   TradingConfig{PortfolioID: "default"},
		SportKeys: []string{"basketball_nba"},
	}
	got := cfg.Contenders()
	if len(got) != 1 || len(got[0].Adjusters) != 0 {
		t.Fatalf("no MLB configured, want no adjusters, got %+v", got)
	}
	if got[0].Name != "historical" {
		t.Errorf("name = %q, want model type", got[0].Name)
	}
}

func TestContendersReturnsConfiguredList(t *testing.T) {
	mw := 0.5
	cfg := &Config{
		Analysis: AnalysisConfig{Models: []ModelConfig{
			{Name: "thompson-pitcher", ModelType: "thompson", Adjusters: []string{"mlb_pitcher"}, Portfolio: "default"},
			{Name: "thompson-raw", ModelType: "thompson", MarketWeight: &mw},
		}},
	}
	got := cfg.Contenders()
	if len(got) != 2 || got[1].Name != "thompson-raw" {
		t.Fatalf("contenders = %+v", got)
	}
}

func TestLoadNotifyDefaults(t *testing.T) {
	t.Setenv("ODDS_API_KEY", "test-key")
	t.Setenv("HOME", t.TempDir()) // keep Load() away from ~/.betanalysis
	t.Chdir(t.TempDir())          // no config.yaml -> pure defaults

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Notify.Enabled {
		t.Error("notify.enabled default = true, want false")
	}
	if cfg.Notify.BaseURL != "http://betanalysis.homelab" {
		t.Errorf("notify.base_url default = %q", cfg.Notify.BaseURL)
	}
}

func TestConsensusStakeDefault(t *testing.T) {
	t.Setenv("ODDS_API_KEY", "test-key")
	// Run from a directory with no config.yaml so defaults apply.
	t.Chdir(t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Consensus.Stake != 5.0 {
		t.Errorf("consensus.stake default = %v, want 5.0", cfg.Consensus.Stake)
	}
}
