package analysis

import (
	"github.com/austinbyron/betanalysis/internal/api"
	"github.com/austinbyron/betanalysis/internal/storage"
	"github.com/austinbyron/betanalysis/pkg/types"
	"github.com/rs/zerolog/log"
)

// TeamStatsService maintains persistent team records in the team_stats table
// and serves them to estimators. Because the records live in Postgres,
// everything the strategies "learn" survives process restarts.
type TeamStatsService struct {
	storage *storage.PostgresDB
	api     *api.Client
}

// NewTeamStatsService creates a new team stats service
func NewTeamStatsService(db *storage.PostgresDB, client *api.Client) *TeamStatsService {
	return &TeamStatsService{
		storage: db,
		api:     client,
	}
}

// UpdateTeamStats fetches recent scores, marks games finished, and updates
// each team's persistent record.
func (s *TeamStatsService) UpdateTeamStats(sportKey string) error {
	log.Info().Str("sport", sportKey).Msg("Updating team statistics")

	scores, err := s.api.GetScores(sportKey, 3)
	if err != nil {
		return err
	}

	updated := 0
	for _, score := range scores {
		if !score.Completed {
			continue
		}

		// The scores endpoint returns games completed up to 3 days back and
		// this runs hourly — only count each game's result once.
		existing, err := s.storage.GetGameByID(score.GameID)
		if err != nil {
			log.Error().Err(err).Str("game", score.GameID).Msg("Failed to look up game")
			continue
		}
		if existing != nil && existing.IsFinished() {
			continue
		}
		if existing == nil {
			// Never collected (e.g. odds were fetched after kickoff) — store
			// it so the historical record is complete.
			game := types.Game{
				ID:           score.GameID,
				SportKey:     sportKey,
				CommenceTime: score.CommenceTime,
				HomeTeam:     score.HomeTeam,
				AwayTeam:     score.AwayTeam,
				Status:       "scheduled",
			}
			if err := s.storage.SaveGames([]types.Game{game}); err != nil {
				log.Error().Err(err).Str("game", score.GameID).Msg("Failed to save game")
				continue
			}
		}

		if err := s.storage.UpdateGameScores(score.GameID, score.HomeScore, score.AwayScore, "finished"); err != nil {
			log.Error().Err(err).Str("game", score.GameID).Msg("Failed to update game scores")
			continue
		}

		homeWon := score.HomeScore > score.AwayScore
		awayWon := score.AwayScore > score.HomeScore

		if err := s.storage.IncrementTeamStats(score.HomeTeam, sportKey, homeWon, true, float64(score.HomeScore), float64(score.AwayScore)); err != nil {
			log.Error().Err(err).Str("team", score.HomeTeam).Msg("Failed to update home team stats")
		}

		if err := s.storage.IncrementTeamStats(score.AwayTeam, sportKey, awayWon, false, float64(score.AwayScore), float64(score.HomeScore)); err != nil {
			log.Error().Err(err).Str("team", score.AwayTeam).Msg("Failed to update away team stats")
		}

		updated++
	}

	log.Info().Str("sport", sportKey).Int("games_updated", updated).Msg("Team stats updated")

	return nil
}

// TeamRecord implements StatsProvider from the team_stats table
func (s *TeamStatsService) TeamRecord(teamName, sportKey string) (wins, losses int) {
	stats, err := s.storage.GetTeamStats(teamName, sportKey)
	if err != nil {
		log.Error().Err(err).Str("team", teamName).Msg("Failed to get team stats")
		return 0, 0
	}
	if stats == nil {
		return 0, 0
	}
	return stats.Wins, stats.Losses
}
