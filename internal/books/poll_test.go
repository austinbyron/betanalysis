package books

import (
	"errors"
	"testing"
	"time"

	"github.com/austinbyron/betanalysis/pkg/types"
)

type memStore struct {
	games map[string][]types.Game
	saved []types.GameOdds
}

func (m *memStore) GetUpcomingGames(sport string) ([]types.Game, error) { return m.games[sport], nil }
func (m *memStore) SaveOdds(o []types.GameOdds) error                   { m.saved = append(m.saved, o...); return nil }

type stubSource struct {
	name  string
	lines map[string][]Line
	err   error
	calls []string
}

func (s *stubSource) Name() string { return s.name }
func (s *stubSource) Fetch(sport string) ([]Line, error) {
	s.calls = append(s.calls, sport)
	return s.lines[sport], s.err
}

func TestPollStoresMatchedLinesAndSkipsIdleSports(t *testing.T) {
	kick := time.Date(2026, 9, 10, 0, 20, 0, 0, time.UTC)
	store := &memStore{games: map[string][]types.Game{
		"americanfootball_nfl": {{ID: "g1", SportKey: "americanfootball_nfl", HomeTeam: "Seattle Seahawks", AwayTeam: "New England Patriots", CommenceTime: kick}},
		"baseball_mlb":         {},
	}}
	good := &stubSource{name: "bovada", lines: map[string][]Line{
		"americanfootball_nfl": {{Home: "Seattle Seahawks", Away: "New England Patriots", Commence: kick, Bookmaker: "bovada", HomeML: f64(1.5), AwayML: f64(2.6)}},
	}}
	bad := &stubSource{name: "draftkings", err: errors.New("403")}

	results := Poll(store, []Source{good, bad}, []string{"americanfootball_nfl", "baseball_mlb"}, kick)

	if len(store.saved) != 1 || store.saved[0].GameID != "g1" || store.saved[0].Source != "bovada" {
		t.Errorf("saved = %+v", store.saved)
	}
	if len(good.calls) != 1 || good.calls[0] != "americanfootball_nfl" {
		t.Errorf("idle sport must not be fetched: calls = %v", good.calls)
	}
	if len(results) != 2 || results[1].Err == nil || results[0].Rows != 1 {
		t.Errorf("results = %+v", results)
	}
}
