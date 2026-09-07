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
	"github.com/austinbyron/betanalysis/internal/consensus"
	"github.com/austinbyron/betanalysis/internal/contenders"
	"github.com/austinbyron/betanalysis/internal/espn"
	"github.com/austinbyron/betanalysis/internal/notify"
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
	// Quota guard: prime from the persisted reading so a restart can't
	// spend credits below the floor; alert Discord once per dip.
	client.SetQuotaFloor(cfg.OddsAPI.QuotaFloor)
	if q, err := db.GetAPIQuota(); err == nil && q != nil {
		client.SeedQuota(q.RequestsRemaining, q.UpdatedAt)
	}
	var quotaSender notify.Sender
	if webhook := os.Getenv("BETANALYSIS_DISCORD_WEBHOOK"); webhook != "" {
		quotaSender = notify.NewDiscord(webhook)
	}
	quotaWatch := notify.NewQuotaWatch(quotaSender, cfg.OddsAPI.QuotaFloor)
	client.SetQuotaHook(func(remaining, used float64) {
		if err := db.SaveAPIQuota(remaining, used); err != nil {
			log.Error().Err(err).Msg("Failed to save API quota")
		}
		quotaWatch.Observe(remaining, used)
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

	// The consensus cycle always runs: it records the shortlist's track
	// record even when Discord notifications are off.
	var notifier *notify.Notifier
	if cfg.Notify.Enabled {
		if webhook := os.Getenv("BETANALYSIS_DISCORD_WEBHOOK"); webhook != "" {
			notifier = notify.New(db, notify.NewDiscord(webhook), cfg.Notify.BaseURL)
		} else {
			log.Warn().Msg("notify.enabled is set but BETANALYSIS_DISCORD_WEBHOOK is empty — notifications off")
		}
	}
	go runConsensusCycles(ctx, notifier, db, lineup, cfg)

	var dashboard *web.Server
	if cfg.Server.Enabled {
		dashboard, err = web.NewServer(db, lineup, cfg, espn.NewLinker(), stats)
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

// runConsensusCycles computes the consensus shortlist every 30 minutes,
// records each pick's first appearance for the track record, and — when a
// notifier is configured — pushes new picks to Discord. One Compute call
// feeds both so they can never disagree.
func runConsensusCycles(ctx context.Context, n *notify.Notifier, db *storage.PostgresDB,
	lineup []contenders.Contender, cfg *config.Config) {
	recorder := consensus.NewRecorder(db, cfg.Consensus.Stake)

	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()

	run := func() {
		picks := consensus.Compute(db, lineup, cfg)
		recorder.Record(picks)
		if n != nil {
			n.Process(picks)
		}
	}
	run()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
