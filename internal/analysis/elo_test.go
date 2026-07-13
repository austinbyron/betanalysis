package analysis

import (
	"testing"
	"time"

	"github.com/austinbyron/betanalysis/pkg/types"
)

// fakeGames serves a fixed slate of finished games and counts fetches
type fakeGames struct {
	games   []types.Game
	fetches int
}

func (f *fakeGames) FinishedGames(string) ([]types.Game, error) {
	f.fetches++
	return f.games, nil
}

func finished(id, home, away string, homeScore, awayScore int, day int) types.Game {
	return types.Game{
		ID: id, SportKey: "baseball_mlb", HomeTeam: home, AwayTeam: away,
		Status: "finished", HomeScore: &homeScore, AwayScore: &awayScore,
		CommenceTime: time.Date(2026, 6, day, 19, 0, 0, 0, time.UTC),
	}
}

func TestEloLearnsFromRepeatedWins(t *testing.T) {
	// Sharks beat Jets three times away, then host them: Sharks must be
	// favored even against home advantage.
	provider := &fakeGames{games: []types.Game{
		finished("g1", "Jets", "Sharks", 2, 7, 1),
		finished("g2", "Jets", "Sharks", 1, 5, 2),
		finished("g3", "Jets", "Sharks", 0, 9, 3),
	}}

	elo := NewElo(provider, 0)
	game := types.Game{SportKey: "baseball_mlb", HomeTeam: "Jets", AwayTeam: "Sharks"}
	homeProb, awayProb := elo.EstimateProbabilities(game)

	if awayProb <= homeProb {
		t.Errorf("Sharks (3-0, big margins) must be favored: home %v away %v", homeProb, awayProb)
	}
	if homeProb+awayProb < 0.999 || homeProb+awayProb > 1.001 {
		t.Errorf("probabilities must normalize: %v + %v", homeProb, awayProb)
	}
}

func TestEloHomeAdvantageTiltsEvenMatchup(t *testing.T) {
	provider := &fakeGames{} // no history: both teams at the initial rating

	elo := NewElo(provider, 0)
	homeProb, _ := elo.EstimateProbabilities(types.Game{SportKey: "baseball_mlb", HomeTeam: "A", AwayTeam: "B"})

	if homeProb <= 0.5 || homeProb > 0.6 {
		t.Errorf("even teams: home probability = %v, want a modest home edge (0.5, 0.6]", homeProb)
	}
}

func TestEloMarginScalesUpdate(t *testing.T) {
	blowout := &fakeGames{games: []types.Game{finished("g1", "A", "B", 0, 10, 1)}}
	squeaker := &fakeGames{games: []types.Game{finished("g1", "A", "B", 0, 1, 1)}}

	game := types.Game{SportKey: "baseball_mlb", HomeTeam: "A", AwayTeam: "B"}
	_, bigAway := NewElo(blowout, 0).EstimateProbabilities(game)
	_, smallAway := NewElo(squeaker, 0).EstimateProbabilities(game)

	if bigAway <= smallAway {
		t.Errorf("a 10-run win must move ratings more than a 1-run win: %v vs %v", bigAway, smallAway)
	}
}

func TestEloConfidenceCountsGamesSeen(t *testing.T) {
	provider := &fakeGames{games: []types.Game{
		finished("g1", "A", "B", 3, 2, 1),
		finished("g2", "A", "C", 4, 1, 2),
	}}

	elo := NewElo(provider, 2) // warmup 2 games
	if got := elo.Confidence(types.Game{SportKey: "baseball_mlb", HomeTeam: "A", AwayTeam: "B"}); got != 0.5 {
		t.Errorf("confidence = %v, want 0.5 (B has 1 of 2 warmup games)", got)
	}
	if got := elo.Confidence(types.Game{SportKey: "baseball_mlb", HomeTeam: "A", AwayTeam: "Unknown"}); got != 0 {
		t.Errorf("confidence = %v, want 0 for an unseen team", got)
	}
	if got := elo.Confidence(types.Game{SportKey: "baseball_mlb", HomeTeam: "A", AwayTeam: "A"}); got != 1 {
		t.Errorf("confidence = %v, want 1 past warmup", got)
	}
}

func TestEloCachesRatingsBetweenCalls(t *testing.T) {
	provider := &fakeGames{games: []types.Game{finished("g1", "A", "B", 3, 2, 1)}}

	elo := NewElo(provider, 0)
	game := types.Game{SportKey: "baseball_mlb", HomeTeam: "A", AwayTeam: "B"}
	elo.EstimateProbabilities(game)
	elo.EstimateProbabilities(game)
	elo.Confidence(game)

	if provider.fetches != 1 {
		t.Errorf("fetches = %d, want 1 (ratings cached within TTL)", provider.fetches)
	}
}

func TestEloName(t *testing.T) {
	if got := NewElo(&fakeGames{}, 0).Name(); got != "elo" {
		t.Errorf("Name = %q, want elo", got)
	}
}
