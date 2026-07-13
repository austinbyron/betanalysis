package config

import (
	"testing"

	"github.com/spf13/viper"
)

// loadPiConfig parses deploy/config.pi.yaml — the file the Aug-31 flip
// copies onto the Pi — so tests can pin its invariants.
func loadPiConfig(t *testing.T) *Config {
	t.Helper()
	v := viper.New()
	v.SetConfigFile("../../deploy/config.pi.yaml")
	if err := v.ReadInConfig(); err != nil {
		t.Fatalf("read pi config: %v", err)
	}
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		t.Fatalf("unmarshal pi config: %v", err)
	}
	return &cfg
}

func TestPiConfigFallLineup(t *testing.T) {
	cfg := loadPiConfig(t)

	wantSports := map[string]bool{
		"baseball_mlb":           true,
		"americanfootball_nfl":   true,
		"americanfootball_ncaaf": true,
	}
	if len(cfg.Sports()) != len(wantSports) {
		t.Fatalf("sports = %v, want exactly %d", cfg.Sports(), len(wantSports))
	}
	for _, s := range cfg.Sports() {
		if !wantSports[s] {
			t.Errorf("unexpected sport %q", s)
		}
		if _, ok := cfg.Scheduler.CollectCrons[s]; !ok {
			t.Errorf("sport %q has no collect_crons entry (default cadence would blow the free tier)", s)
		}
	}

	models := cfg.Contenders()
	if len(models) != 9 {
		t.Fatalf("contenders = %d, want 9", len(models))
	}
	byName := map[string]ModelConfig{}
	for _, m := range models {
		byName[m.Name] = m
		// Every contender must be sport-scoped: an unpinned contender
		// bets every collected sport with one bankroll.
		if len(m.Sports) == 0 {
			t.Errorf("contender %q has no sports filter", m.Name)
		}
	}
	football := map[string]string{
		"elo-nfl":        "americanfootball_nfl",
		"thompson-nfl":   "americanfootball_nfl",
		"elo-ncaaf":      "americanfootball_ncaaf",
		"thompson-ncaaf": "americanfootball_ncaaf",
	}
	for name, sport := range football {
		m, ok := byName[name]
		if !ok {
			t.Errorf("missing football contender %q", name)
			continue
		}
		if len(m.Sports) != 1 || m.Sports[0] != sport {
			t.Errorf("%s sports = %v, want [%s]", name, m.Sports, sport)
		}
		if len(m.Adjusters) != 0 {
			t.Errorf("%s has adjusters %v; mlb_pitcher is MLB-only", name, m.Adjusters)
		}
	}
	for _, name := range []string{"thompson-pitcher", "thompson-raw", "historical-pitcher", "epsilon-pitcher", "elo-pitcher"} {
		m, ok := byName[name]
		if !ok {
			t.Errorf("missing MLB contender %q", name)
			continue
		}
		if len(m.Sports) != 1 || m.Sports[0] != "baseball_mlb" {
			t.Errorf("%s sports = %v, want [baseball_mlb]", name, m.Sports)
		}
	}
}
