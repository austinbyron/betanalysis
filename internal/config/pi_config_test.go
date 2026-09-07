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
	if len(models) != 13 {
		t.Fatalf("contenders = %d, want 13", len(models))
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
		"winner-nfl":     "americanfootball_nfl",
		"elo-ncaaf":      "americanfootball_ncaaf",
		"thompson-ncaaf": "americanfootball_ncaaf",
		"winner-ncaaf":   "americanfootball_ncaaf",
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
		// A 17-game season never reaches the MLB-sized global warmup
		if m.WarmupGames == nil || *m.WarmupGames > 8 {
			t.Errorf("%s needs a short warmup_games override (got %v)", name, m.WarmupGames)
		}
	}
	for _, name := range []string{"winner-nfl", "winner-ncaaf"} {
		if byName[name].Strategy != "winner" {
			t.Errorf("%s strategy = %q, want winner", name, byName[name].Strategy)
		}
	}
	if cfg.OddsAPI.QuotaFloor <= 0 {
		t.Error("pi config: odds_api.quota_floor must be set (free tier needs the guard)")
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

func TestPiConfigWinnerContenders(t *testing.T) {
	cfg := loadPiConfig(t)
	byName := map[string]ModelConfig{}
	for _, m := range cfg.Contenders() {
		byName[m.Name] = m
	}
	for _, name := range []string{"winner-historical", "winner-elo"} {
		m, ok := byName[name]
		if !ok {
			t.Errorf("missing winner contender %q", name)
			continue
		}
		if m.Strategy != "winner" {
			t.Errorf("%s strategy = %q, want winner", name, m.Strategy)
		}
		if len(m.Sports) != 1 || m.Sports[0] != "baseball_mlb" {
			t.Errorf("%s sports = %v, want [baseball_mlb]", name, m.Sports)
		}
	}
	// Record-based winner model needs the venue correction; elo has its own
	if adj := byName["winner-historical"].Adjusters; len(adj) != 2 || adj[1] != "home_field" {
		t.Errorf("winner-historical adjusters = %v, want [mlb_pitcher home_field]", adj)
	}
	for _, m := range cfg.Contenders() {
		if m.Strategy == "" && m.Name != "winner-historical" && m.Name != "winner-elo" {
			continue
		}
		if m.Strategy != "" && m.Strategy != "winner" {
			t.Errorf("%s has unexpected strategy %q", m.Name, m.Strategy)
		}
	}
}

func TestPiConfigNotifyEnabled(t *testing.T) {
	cfg := loadPiConfig(t)
	if !cfg.Notify.Enabled {
		t.Error("pi config: notify.enabled = false, want true")
	}
	if cfg.Notify.BaseURL != "http://betanalysis.homelab" {
		t.Errorf("pi config: notify.base_url = %q", cfg.Notify.BaseURL)
	}
}
