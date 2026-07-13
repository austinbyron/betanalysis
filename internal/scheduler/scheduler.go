package scheduler

import (
	"fmt"
	"time"

	"github.com/austinbyron/betanalysis/internal/analysis"
	"github.com/austinbyron/betanalysis/internal/api"
	"github.com/austinbyron/betanalysis/internal/config"
	"github.com/austinbyron/betanalysis/internal/storage"
	"github.com/austinbyron/betanalysis/internal/trading"
	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog/log"
)

// Scheduler manages cron-based job scheduling
type Scheduler struct {
	cron        *cron.Cron
	storage     *storage.PostgresDB
	apiClient   *api.Client
	teamService *analysis.TeamStatsService
	config      *config.Config
	running     bool
}

// NewScheduler creates a new scheduler
func NewScheduler(db *storage.PostgresDB, client *api.Client, cfg *config.Config) *Scheduler {
	loc := time.Local
	if cfg.Scheduler.Timezone != "" {
		if parsed, err := time.LoadLocation(cfg.Scheduler.Timezone); err == nil {
			loc = parsed
		} else {
			log.Warn().Str("timezone", cfg.Scheduler.Timezone).Msg("Invalid timezone, using local")
		}
	}

	return &Scheduler{
		cron:        cron.New(cron.WithLocation(loc)),
		storage:     db,
		apiClient:   client,
		teamService: analysis.NewTeamStatsService(db, client),
		config:      cfg,
	}
}

// Start begins the scheduler. Specs are standard 5-field cron (minute hour
// dom month dow) — robfig/cron v3's default parser has no seconds field.
func (s *Scheduler) Start() {
	if !s.config.Scheduler.Enabled {
		log.Warn().Msg("Scheduler is disabled in configuration")
		return
	}

	configured := make(map[string]bool)
	for _, sport := range s.config.Sports() {
		configured[sport] = true
	}

	// Default collection schedules per sport (game windows, scheduler
	// timezone). config scheduler.collect_crons overrides per sport —
	// that's the quota throttle on paid vs free API tiers.
	collectSpecs := map[string]string{
		"americanfootball_nfl":   "*/15 12-23 * * 0,1,4",
		"americanfootball_ncaaf": "*/15 11-23 * * 6",
		"basketball_nba":         "*/10 17-23 * * *",
		"basketball_ncaab":       "*/15 17-23 * * *",
		"baseball_mlb":           "*/15 12-23 * * *",
	}
	for sport, spec := range s.config.Scheduler.CollectCrons {
		collectSpecs[sport] = spec
	}

	statsSpec := "0 * * * *"
	if s.config.Scheduler.StatsCron != "" {
		statsSpec = s.config.Scheduler.StatsCron
	}

	type job struct {
		name  string
		spec  string
		sport string // collection jobs run only when their sport is configured
		fn    func()
	}

	var jobs []job
	for _, sport := range s.config.Sports() {
		spec, ok := collectSpecs[sport]
		if !ok {
			log.Warn().Str("sport", sport).Msg("No collection schedule for sport; add scheduler.collect_crons entry")
			continue
		}
		sport := sport
		jobs = append(jobs, job{"collect_" + sport, spec, sport, func() { s.collectOdds(sport) }})
	}
	jobs = append(jobs,
		job{"update_stats", statsSpec, "", s.updateTeamStats},
		// Bet settlement nightly at 11:30 PM
		job{"settle", "30 23 * * *", "", s.settleBets},
		// Weekly report Monday 8 AM
		job{"report", "0 8 * * 1", "", s.generateReport},
	)

	added := 0
	for _, job := range jobs {
		if job.sport != "" && !configured[job.sport] {
			continue
		}
		if _, err := s.cron.AddFunc(job.spec, job.fn); err != nil {
			log.Error().Err(err).Str("job", job.name).Str("spec", job.spec).Msg("Failed to add job")
			continue
		}
		added++
	}

	s.cron.Start()
	s.running = true

	log.Info().Int("jobs", added).Strs("sports", s.config.Sports()).Msg("Scheduler started")
}

// Stop halts the scheduler
func (s *Scheduler) Stop() {
	if !s.running {
		return
	}

	ctx := s.cron.Stop()
	<-ctx.Done()
	s.running = false

	log.Info().Msg("Scheduler stopped")
}

// IsRunning returns whether the scheduler is running
func (s *Scheduler) IsRunning() bool {
	return s.running
}

// collectOdds fetches and stores games and their odds for a sport. Games are
// saved first: game_odds has a foreign key to games.
func (s *Scheduler) collectOdds(sport string) {
	log.Info().Str("sport", sport).Msg("Collecting odds")

	games, odds, err := s.apiClient.GetOdds(sport)
	if err != nil {
		log.Error().Err(err).Str("sport", sport).Msg("Failed to collect odds")
		return
	}

	if err := s.storage.SaveGames(games); err != nil {
		log.Error().Err(err).Str("sport", sport).Msg("Failed to save games")
		return
	}

	if err := s.storage.SaveOdds(odds); err != nil {
		log.Error().Err(err).Str("sport", sport).Msg("Failed to save odds")
		return
	}

	log.Info().Str("sport", sport).Int("games", len(games)).Int("odds", len(odds)).Msg("Odds collected")
}

// updateTeamStats fetches scores and updates team statistics
func (s *Scheduler) updateTeamStats() {
	log.Info().Msg("Updating team statistics")

	for _, sport := range s.config.Sports() {
		if err := s.teamService.UpdateTeamStats(sport); err != nil {
			log.Error().Err(err).Str("sport", sport).Msg("Failed to update team stats")
		}
	}
}

// settleBets settles all completed bets
func (s *Scheduler) settleBets() {
	log.Info().Msg("Running scheduled bet settlement")

	settler := trading.NewSettler(s.storage)
	if err := settler.SettleBets(); err != nil {
		log.Error().Err(err).Msg("Scheduled settlement failed")
	}
}

// generateReport generates weekly performance report
func (s *Scheduler) generateReport() {
	log.Info().Msg("Generating weekly report")

	portfolio, err := s.storage.GetPortfolio(s.config.Trading.PortfolioID)
	if err != nil {
		log.Error().Err(err).Msg("Failed to get portfolio for report")
		return
	}

	if portfolio == nil {
		log.Warn().Msg("No portfolio found for report")
		return
	}

	log.Info().
		Float64("balance", portfolio.Balance).
		Float64("roi", portfolio.ROI()).
		Int("bets_placed", portfolio.BetsPlaced).
		Float64("profit_loss", portfolio.TotalProfitLoss).
		Msg("Weekly Performance Report")
}

// RunOnce runs a specific job once (for testing/manual runs)
func (s *Scheduler) RunOnce(job string) error {
	switch job {
	case "collect_nfl":
		s.collectOdds("americanfootball_nfl")
	case "collect_ncaaf":
		s.collectOdds("americanfootball_ncaaf")
	case "collect_nba":
		s.collectOdds("basketball_nba")
	case "collect_ncaab":
		s.collectOdds("basketball_ncaab")
	case "collect_mlb":
		s.collectOdds("baseball_mlb")
	case "update_stats":
		s.updateTeamStats()
	case "settle":
		s.settleBets()
	case "report":
		s.generateReport()
	default:
		return fmt.Errorf("unknown job: %s", job)
	}

	return nil
}
