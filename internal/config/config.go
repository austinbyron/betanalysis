package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Database  DatabaseConfig  `mapstructure:"database"`
	OddsAPI   OddsAPIConfig   `mapstructure:"odds_api"`
	Trading   TradingConfig   `mapstructure:"trading"`
	Analysis  AnalysisConfig  `mapstructure:"analysis"`
	Scheduler SchedulerConfig `mapstructure:"scheduler"`
	Server    ServerConfig    `mapstructure:"server"`
	SportKeys []string        `mapstructure:"sports"`
}

// Sports returns the configured sport keys
func (c *Config) Sports() []string {
	return c.SportKeys
}

type DatabaseConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Name     string `mapstructure:"name"`
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password"`
	SSLMode  string `mapstructure:"ssl_mode"`
}

type OddsAPIConfig struct {
	APIKey       string  `mapstructure:"api_key"`
	RateLimitRPS float64 `mapstructure:"rate_limit_rps"`
	Regions      string  `mapstructure:"regions"`
	DefaultSport string  `mapstructure:"default_sport"`
	BaseURL      string  `mapstructure:"base_url"`
}

type TradingConfig struct {
	InitialBankroll  float64 `mapstructure:"initial_bankroll"`
	MinStake         float64 `mapstructure:"min_stake"`
	MaxStakeFraction float64 `mapstructure:"max_stake_fraction"`
	KellyFraction    float64 `mapstructure:"kelly_fraction"`
	MinOdds          float64 `mapstructure:"min_odds"`
	MinExpectedValue float64 `mapstructure:"min_expected_value"`
	PortfolioID      string  `mapstructure:"portfolio_id"`
	// ModelName is set programmatically per contender by the daemon — never
	// from yaml. It names the bootstrap portfolio and log context.
	ModelName string `mapstructure:"-"`
}

type AnalysisConfig struct {
	ModelType          string  `mapstructure:"model_type"`
	MarketWeight       float64 `mapstructure:"market_weight"`
	Epsilon            float64 `mapstructure:"epsilon"`
	ThompsonAlphaPrior float64 `mapstructure:"thompson_alpha_prior"`
	ThompsonBetaPrior  float64 `mapstructure:"thompson_beta_prior"`
	// WarmupGames scales the model's share of the market blend by
	// min(games_played/warmup, 1) so thin records can't inflate underdogs
	// into fake edges. 0 disables.
	WarmupGames int           `mapstructure:"warmup_games"`
	Models      []ModelConfig `mapstructure:"models"`
}

// ModelConfig defines one contender in the model race: an estimator plus its
// adjuster stack, portfolio, and optional overrides. See docs/superpowers/
// specs/2026-07-07-model-race-design.md.
type ModelConfig struct {
	Name         string   `mapstructure:"name"`
	ModelType    string   `mapstructure:"model_type"`
	Adjusters    []string `mapstructure:"adjusters"`
	Portfolio    string   `mapstructure:"portfolio"`
	MarketWeight *float64 `mapstructure:"market_weight"`
	Sports       []string `mapstructure:"sports"`
}

// Contenders returns the configured model race lineup. With no models list
// it synthesizes the single pre-race contender from the legacy analysis.*
// keys, including the MLB pitcher adjuster when MLB is configured — so old
// configs behave exactly as before.
func (c *Config) Contenders() []ModelConfig {
	if len(c.Analysis.Models) > 0 {
		return c.Analysis.Models
	}
	mt := c.Analysis.ModelType
	if mt == "" {
		mt = "thompson"
	}
	var adjusters []string
	for _, sport := range c.Sports() {
		if sport == "baseball_mlb" {
			adjusters = []string{"mlb_pitcher"}
			break
		}
	}
	return []ModelConfig{{
		Name:      mt,
		ModelType: mt,
		Adjusters: adjusters,
		Portfolio: c.Trading.PortfolioID,
	}}
}

type SchedulerConfig struct {
	Enabled  bool   `mapstructure:"enabled"`
	Timezone string `mapstructure:"timezone"`
	// CollectCrons overrides the default per-sport collection schedule
	// (sport key -> 5-field cron spec). Each collection call costs
	// markets x regions credits, so this is the quota throttle.
	CollectCrons map[string]string `mapstructure:"collect_crons"`
	// StatsCron overrides the scores/team-stats update schedule
	// (1 credit per sport per run).
	StatsCron string `mapstructure:"stats_cron"`
}

type ServerConfig struct {
	Enabled bool `mapstructure:"enabled"`
	Port    int  `mapstructure:"port"`
}

func Load() (*Config, error) {
	v := viper.New()

	// Set defaults
	v.SetDefault("database.host", "localhost")
	v.SetDefault("database.port", 5432)
	v.SetDefault("database.name", "betanalysis")
	v.SetDefault("database.user", "betuser")
	v.SetDefault("database.password", "betpass")
	v.SetDefault("database.ssl_mode", "disable")

	v.SetDefault("odds_api.base_url", "https://api.the-odds-api.com/v4")
	v.SetDefault("odds_api.rate_limit_rps", 0.5)
	v.SetDefault("odds_api.regions", "us")
	v.SetDefault("odds_api.default_sport", "americanfootball_nfl")

	v.SetDefault("trading.initial_bankroll", 1000.0)
	v.SetDefault("trading.min_stake", 1.0)
	v.SetDefault("trading.max_stake_fraction", 0.05)
	v.SetDefault("trading.kelly_fraction", 0.5)
	v.SetDefault("trading.min_odds", 1.5)
	v.SetDefault("trading.min_expected_value", 0.05)
	v.SetDefault("trading.portfolio_id", "default")

	v.SetDefault("analysis.model_type", "thompson")
	v.SetDefault("analysis.market_weight", 0.7)
	v.SetDefault("analysis.epsilon", 0.1)
	v.SetDefault("analysis.warmup_games", 20)
	v.SetDefault("analysis.thompson_alpha_prior", 1.0)
	v.SetDefault("analysis.thompson_beta_prior", 1.0)

	v.SetDefault("scheduler.enabled", true)
	v.SetDefault("scheduler.timezone", "America/New_York")

	v.SetDefault("server.enabled", true)
	v.SetDefault("server.port", 8090)

	v.SetDefault("sports", []string{
		"americanfootball_nfl",
		"americanfootball_ncaaf",
		"basketball_nba",
		"basketball_ncaab",
		"baseball_mlb",
	})

	// Support config.yaml and environment variables
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")
	v.AddConfigPath("$HOME/.betanalysis")
	v.AddConfigPath("/etc/betanalysis")

	// Read config file
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
	}

	// Environment variables override config file values, e.g.
	// BETANALYSIS_DATABASE_HOST, BETANALYSIS_TRADING_KELLY_FRACTION
	v.SetEnvPrefix("BETANALYSIS")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// ODDS_API_KEY keeps its unprefixed name for convenience
	if apiKey := os.Getenv("ODDS_API_KEY"); apiKey != "" {
		v.Set("odds_api.api_key", apiKey)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Validate required fields
	if cfg.OddsAPI.APIKey == "" {
		return nil, fmt.Errorf("odds_api.api_key is required. Set via config.yaml or ODDS_API_KEY env var")
	}

	return &cfg, nil
}

// DSN returns PostgreSQL connection string
func (c *DatabaseConfig) DSN() string {
	return fmt.Sprintf("host=%s port=%d dbname=%s user=%s password=%s sslmode=%s",
		c.Host, c.Port, c.Name, c.User, c.Password, c.SSLMode)
}
