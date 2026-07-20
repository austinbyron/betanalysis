package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/austinbyron/betanalysis/internal/analysis"
	"github.com/austinbyron/betanalysis/internal/api"
	"github.com/austinbyron/betanalysis/internal/config"
	"github.com/austinbyron/betanalysis/internal/espn"
	"github.com/austinbyron/betanalysis/internal/mlb"
	"github.com/austinbyron/betanalysis/internal/priors"
	"github.com/austinbyron/betanalysis/internal/scheduler"
	"github.com/austinbyron/betanalysis/internal/storage"
	"github.com/austinbyron/betanalysis/internal/trading"
	"github.com/austinbyron/betanalysis/pkg/types"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/urfave/cli/v2"
)

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	app := &cli.App{
		Name:  "betanalysis",
		Usage: "Sports betting data analysis and paper trading",
		Commands: []*cli.Command{
			{
				Name:  "collect",
				Usage: "Collect games and odds from The Odds API",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:  "sport",
						Usage: "Sport key (e.g., americanfootball_nfl)",
						Value: "americanfootball_nfl",
					},
					&cli.BoolFlag{
						Name:  "all",
						Usage: "Collect for all configured sports",
					},
				},
				Action: runCollect,
			},
			{
				Name:  "scores",
				Usage: "Fetch recent scores and update team records",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:  "sport",
						Usage: "Sport key",
					},
				},
				Action: runScores,
			},
			{
				Name:  "sim",
				Usage: "Run one paper trading cycle",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:  "sport",
						Usage: "Sport key",
						Value: "americanfootball_nfl",
					},
					&cli.StringFlag{
						Name:  "model",
						Usage: "Model type (thompson, epsilon_greedy, historical)",
					},
				},
				Action: runSimulation,
			},
			{
				Name:  "backtest",
				Usage: "Backtest a model against stored historical games and odds",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:  "sport",
						Usage: "Sport key",
						Value: "americanfootball_nfl",
					},
					&cli.StringFlag{
						Name:  "model",
						Usage: "Model type (thompson, epsilon_greedy, historical)",
					},
					&cli.IntFlag{
						Name:  "days",
						Usage: "How many days of history to replay",
						Value: 365,
					},
					&cli.Float64Flag{
						Name:  "bankroll",
						Usage: "Starting bankroll",
						Value: 1000,
					},
				},
				Action: runBacktest,
			},
			{
				Name:   "settle",
				Usage:  "Settle completed bets",
				Action: runSettle,
			},
			{
				Name: "seed-priors",
				Usage: "Seed per-team Beta priors — from last season's ESPN standings " +
					"(default; the daemon also does this automatically for cold sports) " +
					"or from a win-totals JSON file (object of team name -> projected wins)",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "sport",
						Usage:    "Sport key (e.g., americanfootball_nfl)",
						Required: true,
					},
					&cli.StringFlag{
						Name:  "file",
						Usage: "JSON file mapping team name to projected season wins (overrides the standings source)",
					},
					&cli.IntFlag{
						Name:  "season",
						Usage: "Season year to pull standings from (default: last year)",
					},
					&cli.Float64Flag{
						Name:  "season-games",
						Usage: "Games in a season (17 NFL, 162 MLB) — required with --file",
					},
					&cli.Float64Flag{
						Name:  "pseudo-games",
						Usage: "Prior strength in pseudo-games per team",
						Value: priors.DefaultPseudoGames,
					},
				},
				Action: runSeedPriors,
			},
			{
				Name:   "report",
				Usage:  "Show portfolio performance",
				Action: runReport,
			},
			{
				Name:   "serve",
				Usage:  "Run the background scheduler",
				Action: runServe,
			},
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatal().Err(err).Msg("Application failed")
	}
}

// newClient builds an Odds API client that persists the credit counters it
// sees on every response, so the dashboard quota stays fresh no matter which
// process last talked to the API.
func newClient(cfg *config.Config, db *storage.PostgresDB) *api.Client {
	client := api.NewClient(cfg.OddsAPI)
	client.SetQuotaHook(func(remaining, used float64) {
		if err := db.SaveAPIQuota(remaining, used); err != nil {
			log.Error().Err(err).Msg("Failed to save API quota")
		}
	})
	return client
}

func runCollect(c *cli.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	db, err := storage.NewPostgres(cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()

	client := newClient(cfg, db)

	sports := []string{c.String("sport")}
	if c.Bool("all") {
		sports = cfg.Sports()
	}

	for _, sport := range sports {
		games, odds, err := client.GetOdds(sport)
		if err != nil {
			log.Error().Err(err).Str("sport", sport).Msg("Failed to collect odds")
			continue
		}

		// Games first: game_odds has a foreign key to games
		if err := db.SaveGames(games); err != nil {
			log.Error().Err(err).Str("sport", sport).Msg("Failed to save games")
			continue
		}

		if err := db.SaveOdds(odds); err != nil {
			log.Error().Err(err).Str("sport", sport).Msg("Failed to save odds")
			continue
		}

		log.Info().Str("sport", sport).Int("games", len(games)).Int("odds", len(odds)).Msg("Collected")
	}

	return nil
}

func runScores(c *cli.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	db, err := storage.NewPostgres(cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()

	client := newClient(cfg, db)
	teamService := analysis.NewTeamStatsService(db, client)

	sports := cfg.Sports()
	if s := c.String("sport"); s != "" {
		sports = []string{s}
	}

	for _, sport := range sports {
		if err := teamService.UpdateTeamStats(sport); err != nil {
			log.Error().Err(err).Str("sport", sport).Msg("Failed to update team stats")
		}
	}

	return nil
}

func buildEngine(cfg *config.Config, db *storage.PostgresDB, client *api.Client, modelOverride string) (*trading.Engine, error) {
	analysisCfg := cfg.Analysis
	if modelOverride != "" {
		analysisCfg.ModelType = modelOverride
	}

	teamService := analysis.NewTeamStatsService(db, client)
	stats := analysis.WithPriors(teamService, db)
	estimator, err := analysis.NewEstimator(analysisCfg, stats, db)
	if err != nil {
		return nil, err
	}
	for _, sport := range cfg.Sports() {
		if sport == types.SportMLB {
			estimator = analysis.WithAdjusters(estimator, mlb.NewPitcherAdjuster(mlb.NewClient()))
			break
		}
	}

	selector := analysis.NewSelector(estimator, "", analysisCfg.MarketWeight, cfg.Trading.MinOdds, cfg.Trading.MinExpectedValue)
	return trading.NewEngine(db, selector, cfg.Trading), nil
}

func runSimulation(c *cli.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	db, err := storage.NewPostgres(cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()

	client := newClient(cfg, db)
	engine, err := buildEngine(cfg, db, client, c.String("model"))
	if err != nil {
		return err
	}

	if err := engine.RunTradingCycle(c.String("sport")); err != nil {
		return err
	}

	return printPortfolio(db, cfg.Trading.PortfolioID)
}

func runBacktest(c *cli.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	db, err := storage.NewPostgres(cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()

	analysisCfg := cfg.Analysis
	if model := c.String("model"); model != "" {
		analysisCfg.ModelType = model
	}

	// UTC to match commence_time's UTC wall clock in the range query.
	end := time.Now().UTC()
	start := end.AddDate(0, 0, -c.Int("days"))

	result, err := analysis.RunBacktest(db, analysisCfg, cfg.Trading, c.String("sport"), start, end, c.Float64("bankroll"))
	if err != nil {
		return err
	}

	fmt.Printf("Model:          %s\n", result.ModelType)
	fmt.Printf("Bets:           %d (%d won, %d lost)\n", result.TotalBets, result.Wins, result.Losses)
	fmt.Printf("Win rate:       %.1f%%\n", result.WinRate)
	fmt.Printf("Total staked:   %.2f\n", result.TotalStaked)
	fmt.Printf("Profit:         %.2f\n", result.TotalProfit)
	fmt.Printf("ROI:            %.2f%%\n", result.ROI)
	fmt.Printf("Final bankroll: %.2f\n", result.FinalBankroll)
	fmt.Printf("Max drawdown:   %.1f%%\n", result.MaxDrawdown)
	fmt.Printf("Sharpe:         %.2f\n", result.SharpeRatio)

	return nil
}

func runSettle(c *cli.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	db, err := storage.NewPostgres(cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()

	settler := trading.NewSettler(db)
	if err := settler.SettleBets(); err != nil {
		return err
	}

	// Heal bets whose odds-feed event vanished (reschedule id churn,
	// postponements) — the scores path above can never settle those.
	reconciler := trading.NewReconciler(db, espn.NewLinker())
	return reconciler.ReconcileStaleBets()
}

// runSeedPriors seeds per-team Beta pseudo-counts. Default source is last
// season's ESPN standings (regressed 1/3 to the mean — the same seeding the
// daemon runs automatically for cold sports). A win-totals JSON file
// (p = wins/season_games) overrides it for anyone wanting sharper
// market-derived priors; The Odds API itself has no win-total futures
// (checked 2026-07: only championship-winner markets).
func runSeedPriors(c *cli.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	db, err := storage.NewPostgres(cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()

	sport := c.String("sport")
	pseudoGames := c.Float64("pseudo-games")
	if pseudoGames <= 0 {
		return fmt.Errorf("pseudo-games must be positive")
	}

	if c.String("file") == "" {
		season := c.Int("season")
		if season == 0 {
			season = time.Now().Year() - 1
		}
		seeded, err := priors.SeedFromSeason(db, espn.NewStandingsClient(), sport, season, pseudoGames)
		if err != nil {
			return err
		}
		fmt.Printf("Seeded %d team priors for %s from ESPN %d standings (%g pseudo-games each)\n",
			seeded, sport, season, pseudoGames)
		return nil
	}

	raw, err := os.ReadFile(c.String("file"))
	if err != nil {
		return fmt.Errorf("failed to read priors file: %w", err)
	}
	var winTotals map[string]float64
	if err := json.Unmarshal(raw, &winTotals); err != nil {
		return fmt.Errorf("failed to parse priors file (want {\"Team Name\": projected_wins}): %w", err)
	}
	if len(winTotals) == 0 {
		return fmt.Errorf("priors file has no teams")
	}

	seasonGames := c.Float64("season-games")
	if seasonGames <= 0 {
		return fmt.Errorf("--season-games is required with --file (17 NFL, 162 MLB)")
	}
	source := filepath.Base(c.String("file"))

	seeded := 0
	for team, wins := range winTotals {
		p := math.Max(0.05, math.Min(0.95, wins/seasonGames))
		prior := types.TeamPrior{
			TeamName:    team,
			SportKey:    sport,
			PriorWins:   p * pseudoGames,
			PriorLosses: (1 - p) * pseudoGames,
			Source:      source,
		}
		if err := db.SeedTeamPrior(prior); err != nil {
			return fmt.Errorf("failed to seed %s: %w", team, err)
		}
		seeded++
	}

	fmt.Printf("Seeded %d team priors for %s (%g pseudo-games each) from %s\n",
		seeded, sport, pseudoGames, source)
	fmt.Println("Note: team names must match The Odds API exactly (e.g. \"Kansas City Chiefs\").")
	return nil
}

func runReport(c *cli.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	db, err := storage.NewPostgres(cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()

	return printPortfolio(db, cfg.Trading.PortfolioID)
}

func printPortfolio(db *storage.PostgresDB, portfolioID string) error {
	portfolio, err := db.GetPortfolio(portfolioID)
	if err != nil {
		return err
	}
	if portfolio == nil {
		return fmt.Errorf("no portfolio found — run a trading cycle first")
	}

	fmt.Printf("Portfolio:     %s\n", portfolio.Name)
	fmt.Printf("Balance:       %.2f (started %.2f)\n", portfolio.Balance, portfolio.InitialBankroll)
	fmt.Printf("Bets placed:   %d (%d won, %d lost, %d active)\n",
		portfolio.BetsPlaced, portfolio.BetsWon, portfolio.BetsLost, portfolio.ActiveBetsCount)
	fmt.Printf("Total wagered: %.2f\n", portfolio.TotalWagered)
	fmt.Printf("Profit/loss:   %.2f\n", portfolio.TotalProfitLoss)
	fmt.Printf("ROI:           %.2f%%\n", portfolio.ROI())
	fmt.Printf("Win rate:      %.1f%%\n", portfolio.WinRate())

	return nil
}

func runServe(c *cli.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	db, err := storage.NewPostgres(cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()

	client := newClient(cfg, db)

	sched := scheduler.NewScheduler(db, client, cfg)
	sched.Start()
	defer sched.Stop()

	log.Info().Msg("Scheduler started. Press Ctrl+C to stop.")
	select {}
}
