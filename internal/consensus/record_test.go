package consensus

import (
	"testing"
	"time"

	"github.com/austinbyron/betanalysis/pkg/types"
)

type upgradeCall struct {
	gameID, selection string
	at                time.Time
}

// fakeRecordStore emulates the DB's first-write-wins semantics
type fakeRecordStore struct {
	picks         map[string]types.ConsensusPick // by game_id
	upgrades      []upgradeCall
	upgradedGames map[string]bool // track which games have been upgraded
}

func newFakeRecordStore() *fakeRecordStore {
	return &fakeRecordStore{
		picks:         make(map[string]types.ConsensusPick),
		upgradedGames: make(map[string]bool),
	}
}

func (f *fakeRecordStore) RecordConsensusPick(cp types.ConsensusPick) (bool, error) {
	if _, exists := f.picks[cp.GameID]; exists {
		return false, nil // ON CONFLICT DO NOTHING
	}
	f.picks[cp.GameID] = cp
	return true, nil
}

func (f *fakeRecordStore) UpgradeConsensusPickStrong(gameID, selection string, at time.Time) (bool, error) {
	// Guard: pick must exist, have same selection, not be strong, and not already upgraded
	pick, exists := f.picks[gameID]
	if !exists || pick.Selection != selection || pick.Strong || f.upgradedGames[gameID] {
		return false, nil
	}
	// Mark as upgraded and record the call
	f.upgradedGames[gameID] = true
	f.upgrades = append(f.upgrades, upgradeCall{gameID, selection, at})
	return true, nil
}

func fixedNow() time.Time {
	return time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
}

func testPick(gameID, side string, votes int, strong bool) Pick {
	return Pick{
		Game: types.Game{
			ID:           gameID,
			HomeTeam:     "Home",
			AwayTeam:     "Away",
			CommenceTime: fixedNow().Add(3 * time.Hour),
		},
		Selection: side,
		Votes:     votes,
		Total:     5,
		AvgProb:   0.41,
		MinEV:     0.12,
		BestOdds:  3.01,
		BestBook:  "betonlineag",
		Strong:    strong,
	}
}

func TestRecorderPersistsFirstAppearance(t *testing.T) {
	store := newFakeRecordStore()
	r := NewRecorder(store, 5.0)
	r.now = fixedNow

	r.Record([]Pick{testPick("g1", "home", 4, false)})

	cp, ok := store.picks["g1"]
	if !ok {
		t.Fatal("pick not recorded")
	}
	if cp.Selection != "home" || cp.Stake != 5.0 || cp.Status != types.BetStatusPending {
		t.Errorf("got %+v", cp)
	}
	if cp.Votes == nil || *cp.Votes != 4 || cp.Total == nil || *cp.Total != 5 {
		t.Errorf("votes/total not captured: %+v", cp)
	}
	if cp.AvgProb == nil || *cp.AvgProb != 0.41 || cp.MinEV == nil || *cp.MinEV != 0.12 {
		t.Errorf("avg_prob/min_ev not captured: %+v", cp)
	}
	if cp.BestOdds != 3.01 || cp.BestBook != "betonlineag" {
		t.Errorf("odds not captured: %+v", cp)
	}
	if cp.Backfilled {
		t.Error("live capture must not be marked backfilled")
	}
	if !cp.CreatedAt.Equal(fixedNow()) {
		t.Errorf("created_at = %v, want %v", cp.CreatedAt, fixedNow())
	}
	if len(store.upgrades) != 0 {
		t.Errorf("non-strong pick attempted upgrade: %+v", store.upgrades)
	}
}

func TestRecorderStrongPickAttemptsUpgrade(t *testing.T) {
	store := newFakeRecordStore()
	r := NewRecorder(store, 5.0)
	r.now = fixedNow

	// Cycle 1: 4/5 regular. Cycle 2: same side, now strong.
	r.Record([]Pick{testPick("g1", "home", 4, false)})
	r.Record([]Pick{testPick("g1", "home", 5, true)})

	if got := store.picks["g1"]; got.Strong || got.Votes == nil || *got.Votes != 4 {
		t.Errorf("first write must win: %+v", got)
	}
	if len(store.upgrades) != 1 {
		t.Fatalf("upgrades = %d, want 1", len(store.upgrades))
	}
	up := store.upgrades[0]
	if up.gameID != "g1" || up.selection != "home" || !up.at.Equal(fixedNow()) {
		t.Errorf("upgrade call = %+v", up)
	}
}

func TestRecorderSkipsStartedGames(t *testing.T) {
	store := newFakeRecordStore()
	r := NewRecorder(store, 5.0)
	r.now = fixedNow

	p := testPick("g1", "home", 4, false)
	p.Game.CommenceTime = fixedNow().Add(-time.Minute)
	r.Record([]Pick{p})

	if len(store.picks) != 0 {
		t.Error("in-play pick must not be recorded")
	}
}
