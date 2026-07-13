package priors

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/austinbyron/betanalysis/internal/espn"
	"github.com/austinbyron/betanalysis/pkg/types"
)

type fakeStore struct {
	seeded        map[string][]types.TeamPrior // by sport
	finishedGames map[string]int
	hasPriors     map[string]bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		seeded:        make(map[string][]types.TeamPrior),
		finishedGames: make(map[string]int),
		hasPriors:     make(map[string]bool),
	}
}

func (f *fakeStore) SeedTeamPrior(p types.TeamPrior) error {
	f.seeded[p.SportKey] = append(f.seeded[p.SportKey], p)
	return nil
}
func (f *fakeStore) CountFinishedGames(sport string) (int, error) { return f.finishedGames[sport], nil }
func (f *fakeStore) HasTeamPriors(sport string) (bool, error)     { return f.hasPriors[sport], nil }

type fakeStandings struct {
	records map[string][]espn.TeamRecord
	seasons []int
}

func (f *fakeStandings) Standings(sport string, season int) ([]espn.TeamRecord, error) {
	f.seasons = append(f.seasons, season)
	if r, ok := f.records[sport]; ok {
		return r, nil
	}
	return nil, fmt.Errorf("no standings for %s", sport)
}

func TestSeedFromSeasonRegressesRecords(t *testing.T) {
	store := newFakeStore()
	standings := &fakeStandings{records: map[string][]espn.TeamRecord{
		"americanfootball_nfl": {
			{Name: "Patriots", Wins: 14, Losses: 3},
			{Name: "Jets", Wins: 4, Losses: 13},
		},
	}}

	seeded, err := SeedFromSeason(store, standings, "americanfootball_nfl", 2025, 16)
	if err != nil {
		t.Fatalf("SeedFromSeason: %v", err)
	}
	if seeded != 2 {
		t.Fatalf("seeded = %d, want 2", seeded)
	}

	pats := store.seeded["americanfootball_nfl"][0]
	// 14/17 = .8235 regressed 1/3 to .500 → .7157 → 11.45 of 16 pseudo-wins
	wantP := 0.5/3 + (14.0/17)*(2.0/3)
	if math.Abs(pats.PriorWins-wantP*16) > 1e-9 {
		t.Errorf("Patriots prior wins = %v, want %v", pats.PriorWins, wantP*16)
	}
	if math.Abs(pats.PriorWins+pats.PriorLosses-16) > 1e-9 {
		t.Errorf("pseudo-games must total 16, got %v", pats.PriorWins+pats.PriorLosses)
	}
	if pats.Source != "espn-standings-2025" {
		t.Errorf("source = %q", pats.Source)
	}
}

func TestAutoSeedOnlySeedsColdSports(t *testing.T) {
	store := newFakeStore()
	store.finishedGames["baseball_mlb"] = 120 // mid-season, leave alone
	store.hasPriors["basketball_nba"] = true  // already seeded, leave alone
	standings := &fakeStandings{records: map[string][]espn.TeamRecord{
		"americanfootball_nfl": {{Name: "Patriots", Wins: 14, Losses: 3}},
	}}

	now := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	sports := []string{"baseball_mlb", "basketball_nba", "americanfootball_nfl", "americanfootball_ncaaf"}
	AutoSeed(store, standings, sports, now)

	if len(store.seeded["baseball_mlb"]) != 0 {
		t.Error("a sport with recorded games must not be seeded")
	}
	if len(store.seeded["basketball_nba"]) != 0 {
		t.Error("a sport with existing priors must not be reseeded")
	}
	if len(store.seeded["americanfootball_nfl"]) != 1 {
		t.Errorf("cold NFL must be seeded, got %d", len(store.seeded["americanfootball_nfl"]))
	}
	// NCAAF standings errored (no fixture) — logged and skipped, no panic
	if len(store.seeded["americanfootball_ncaaf"]) != 0 {
		t.Error("failed standings fetch must seed nothing")
	}
	for _, season := range standings.seasons {
		if season != 2025 {
			t.Errorf("season = %d, want 2025 (last completed before Aug 2026)", season)
		}
	}
}
