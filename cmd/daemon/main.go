package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/austinbyron/betanalysis/internal/analysis"
	"github.com/austinbyron/betanalysis/internal/api"
	"github.com/austinbyron/betanalysis/internal/config"
	"github.com/austinbyron/betanalysis/internal/contenders"
	"github.com/austinbyron/betanalysis/internal/espn"
	"github.com/austinbyron/betanalysis/internal/priors"
	"github.com/austinbyron/betanalysis/internal/scheduler"
	"github.com/austinbyron/betanalysis/internal/storage"
	"github.com/austinbyron/betanalysis/internal/trading"
	"github.com/austinbyron/betanalysis/internal/web"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	log.Info().Msg("Starting BetAnalysis Daemon")

	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to load configuration")
	}

	db, err := storage.NewPostgres(cfg.Database)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to connect to database")
	}
	defer db.Close()

	client := api.NewClient(cfg.OddsAPI)
	client.SetQuotaHook(func(remaining, used float64) {
		if err := db.SaveAPIQuota(remaining, used); err != nil {
			log.Error().Err(err).Msg("Failed to save API quota")
		}
	})

	// Cold sports (added to config before their season starts) get priors
	// from last season's standings automatically — no manual seeding.
	priors.AutoSeed(db, espn.NewStandingsClient(), cfg.Sports(), time.Now())

	// Strategies read team records from Postgres, so everything learned
	// survives restarts. Seeded market priors layer on top as pseudo-games.
	teamService := analysis.NewTeamStatsService(db, client)
	stats := analysis.WithPriors(teamService, db)
	lineup, err := contenders.Build(cfg, stats, db)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to build model lineup")
	}

	engines := make([]contenderEngine, 0, len(lineup))
	for _, c := range lineup {
		tradingCfg := cfg.Trading
		tradingCfg.PortfolioID = c.Portfolio
		tradingCfg.ModelName = c.Name
		engines = append(engines, contenderEngine{c: c, engine: trading.NewEngine(db, c.Selector, tradingCfg)})
	}

	sched := scheduler.NewScheduler(db, client, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sched.Start()

	go runTradingCycles(ctx, engines, cfg)
	go runSettlementCycles(ctx, db)

	var dashboard *web.Server
	if cfg.Server.Enabled {
		dashboard, err = web.NewServer(db, lineup, cfg, espn.NewLinker())
		if err != nil {
			log.Fatal().Err(err).Msg("Failed to create dashboard server")
		}
		go func() {
			if err := dashboard.Start(); err != nil {
				log.Error().Err(err).Msg("Dashboard server failed")
			}
		}()
	}

	log.Info().
		Int("models", len(lineup)).
		Strs("sports", cfg.Sports()).
		Msg("Daemon started. Press Ctrl+C to stop.")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Info().Msg("Shutting down...")
	sched.Stop()
	if dashboard != nil {
		dashboard.Close()
	}
	cancel()
	time.Sleep(2 * time.Second)
	log.Info().Msg("Daemon stopped")
}

// contenderEngine pairs a racing contender with its trading engine
type contenderEngine struct {
	c      contenders.Contender
	engine *trading.Engine
}

func runTradingCycles(ctx context.Context, engines []contenderEngine, cfg *config.Config) {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()

	runCycle(engines, cfg)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runCycle(engines, cfg)
		}
	}
}

func runCycle(engines []contenderEngine, cfg *config.Config) {
	for _, sport := range cfg.Sports() {
		for _, ce := range engines {
			if !ce.c.CoversSport(sport) {
				continue
			}
			if err := ce.engine.RunTradingCycle(sport); err != nil {
				log.Error().Err(err).Str("sport", sport).Str("model", ce.c.Name).Msg("Trading cycle failed")
			}
		}
	}
}

func runSettlementCycles(ctx context.Context, db *storage.PostgresDB) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			settler := trading.NewSettler(db)
			if err := settler.SettleBets(); err != nil {
				log.Error().Err(err).Msg("Settlement cycle failed")
			}
		}
	}
}
