package consensus

import (
	"testing"
	"time"

	"github.com/austinbyron/betanalysis/pkg/types"
)

// fakeBackfillStore reuses fakeRecordStore's write half and serves the
// ledger + historical odds
type fakeBackfillStore struct {
	*fakeRecordStore
	ledger []types.ConsensusNotification
	games  map[string]*types.Game
	odds   map[string][]types.GameOdds // by game, returned regardless of asOf
	asOfs  []time.Time                 // records what Backfill asked for
}

func (f *fakeBackfillStore) GetAllConsensusNotifications() ([]types.ConsensusNotification, error) {
	return f.ledger, nil
}
func (f *fakeBackfillStore) GetGameByID(id string) (*types.Game, error) { return f.games[id], nil }
func (f *fakeBackfillStore) GetOddsForGameAt(gameID string, asOf time.Time) ([]types.GameOdds, error) {
	f.asOfs = append(f.asOfs, asOf)
	return f.odds[gameID], nil
}

func fp(v float64) *float64 { return &v }

func TestBackfillCreatesPickWithBestOddsAsOfPing(t *testing.T) {
	sent := time.Date(2026, 7, 27, 15, 0, 0, 0, time.UTC)
	store := &fakeBackfillStore{
		fakeRecordStore: newFakeRecordStore(),
		ledger: []types.ConsensusNotification{
			{ID: 1, GameID: "g1", Selection: "home", Strong: false, SentAt: sent},
		},
		games: map[string]*types.Game{"g1": {ID: "g1"}},
		odds: map[string][]types.GameOdds{"g1": {
			{GameID: "g1", Bookmaker: "dk", MarketType: types.MarketMoneyline, HomeOdds: fp(2.10), AwayOdds: fp(1.75)},
			{GameID: "g1", Bookmaker: "fd", MarketType: types.MarketMoneyline, HomeOdds: fp(2.30), AwayOdds: fp(1.65)},
			{GameID: "g1", Bookmaker: "dk", MarketType: types.MarketSpread, HomeOdds: fp(9.99)}, // wrong market, ignored
		}},
	}

	created, upgraded, skipped, err := Backfill(store, 5.0)
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if created != 1 || upgraded != 0 || skipped != 0 {
		t.Fatalf("counts = %d/%d/%d, want 1/0/0", created, upgraded, skipped)
	}

	cp := store.picks["g1"]
	if cp.BestOdds != 2.30 || cp.BestBook != "fd" {
		t.Errorf("best odds = %v @ %s, want 2.30 @ fd", cp.BestOdds, cp.BestBook)
	}
	if !cp.Backfilled || cp.Votes != nil || cp.AvgProb != nil {
		t.Errorf("backfilled row shape wrong: %+v", cp)
	}
	if !cp.CreatedAt.Equal(sent) {
		t.Errorf("created_at = %v, want ledger sent_at %v", cp.CreatedAt, sent)
	}
	if cp.Stake != 5.0 || cp.Status != types.BetStatusPending {
		t.Errorf("stake/status wrong: %+v", cp)
	}
	if len(store.asOfs) != 1 || !store.asOfs[0].Equal(sent) {
		t.Errorf("odds asked as-of %v, want %v", store.asOfs, sent)
	}
}

func TestBackfillMergesStrongUpgradeRow(t *testing.T) {
	first := time.Date(2026, 7, 27, 15, 0, 0, 0, time.UTC)
	second := first.Add(2 * time.Hour)
	store := &fakeBackfillStore{
		fakeRecordStore: newFakeRecordStore(),
		ledger: []types.ConsensusNotification{
			{ID: 1, GameID: "g1", Selection: "home", Strong: false, SentAt: first},
			{ID: 2, GameID: "g1", Selection: "home", Strong: true, SentAt: second},
		},
		games: map[string]*types.Game{"g1": {ID: "g1"}},
		odds: map[string][]types.GameOdds{"g1": {
			{GameID: "g1", Bookmaker: "dk", MarketType: types.MarketMoneyline, HomeOdds: fp(2.10), AwayOdds: fp(1.75)},
		}},
	}

	created, upgraded, skipped, err := Backfill(store, 5.0)
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if created != 1 || upgraded != 1 || skipped != 0 {
		t.Fatalf("counts = %d/%d/%d, want 1/1/0", created, upgraded, skipped)
	}
	if store.picks["g1"].Strong {
		t.Error("initial capture was not strong; upgrade must not rewrite it")
	}
	if len(store.upgrades) != 1 || store.upgrades[0].selection != "home" || !store.upgrades[0].at.Equal(second) {
		t.Errorf("upgrade call = %+v", store.upgrades)
	}
}

func TestBackfillStrongAtFirstSight(t *testing.T) {
	sent := time.Date(2026, 7, 27, 15, 0, 0, 0, time.UTC)
	store := &fakeBackfillStore{
		fakeRecordStore: newFakeRecordStore(),
		ledger: []types.ConsensusNotification{
			{ID: 1, GameID: "g1", Selection: "away", Strong: true, SentAt: sent},
		},
		games: map[string]*types.Game{"g1": {ID: "g1"}},
		odds: map[string][]types.GameOdds{"g1": {
			{GameID: "g1", Bookmaker: "dk", MarketType: types.MarketMoneyline, HomeOdds: fp(2.10), AwayOdds: fp(1.75)},
		}},
	}

	created, upgraded, skipped, err := Backfill(store, 5.0)
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if created != 1 || upgraded != 0 || skipped != 0 {
		t.Fatalf("counts = %d/%d/%d, want 1/0/0", created, upgraded, skipped)
	}
	cp := store.picks["g1"]
	if !cp.Strong || cp.BestOdds != 1.75 {
		t.Errorf("strong-at-first-sight pick: %+v", cp)
	}
}

func TestBackfillIgnoresFlippedSideStrongRow(t *testing.T) {
	first := time.Date(2026, 7, 27, 15, 0, 0, 0, time.UTC)
	second := first.Add(2 * time.Hour)
	store := &fakeBackfillStore{
		fakeRecordStore: newFakeRecordStore(),
		ledger: []types.ConsensusNotification{
			{ID: 1, GameID: "g1", Selection: "home", Strong: false, SentAt: first},
			{ID: 2, GameID: "g1", Selection: "away", Strong: true, SentAt: second}, // flipped side
		},
		games: map[string]*types.Game{"g1": {ID: "g1"}},
		odds: map[string][]types.GameOdds{"g1": {
			{GameID: "g1", Bookmaker: "dk", MarketType: types.MarketMoneyline, HomeOdds: fp(2.10), AwayOdds: fp(1.75)},
		}},
	}

	created, upgraded, skipped, err := Backfill(store, 5.0)
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if created != 1 || upgraded != 0 || skipped != 0 {
		t.Fatalf("counts = %d/%d/%d, want 1/0/0", created, upgraded, skipped)
	}
	if len(store.upgrades) != 0 {
		t.Errorf("flipped-side strong row must not upgrade: %+v", store.upgrades)
	}
}

func TestBackfillSkipsWhenNoOddsSurvive(t *testing.T) {
	sent := time.Date(2026, 7, 27, 15, 0, 0, 0, time.UTC)
	store := &fakeBackfillStore{
		fakeRecordStore: newFakeRecordStore(),
		ledger: []types.ConsensusNotification{
			{ID: 1, GameID: "g1", Selection: "home", Strong: false, SentAt: sent},
		},
		games: map[string]*types.Game{"g1": {ID: "g1"}},
		odds:  map[string][]types.GameOdds{}, // nothing retrieved before the ping
	}

	created, upgraded, skipped, err := Backfill(store, 5.0)
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if created != 0 || upgraded != 0 || skipped != 1 {
		t.Fatalf("counts = %d/%d/%d, want 0/0/1", created, upgraded, skipped)
	}
}

func TestBackfillDoesNotCountConflictAsCreated(t *testing.T) {
	// g1 was already live-recorded by the daemon before backfill ran (a
	// restarted daemon records today's picks first). g2 is genuinely new.
	first := time.Date(2026, 7, 27, 15, 0, 0, 0, time.UTC)
	second := first.Add(2 * time.Hour)
	preseeded := newFakeRecordStore()
	preseeded.picks["g1"] = types.ConsensusPick{
		GameID:    "g1",
		Selection: "home",
		Strong:    false,
		CreatedAt: first,
	}
	store := &fakeBackfillStore{
		fakeRecordStore: preseeded,
		ledger: []types.ConsensusNotification{
			{ID: 1, GameID: "g1", Selection: "home", Strong: false, SentAt: first},
			{ID: 2, GameID: "g1", Selection: "home", Strong: true, SentAt: second},
			{ID: 3, GameID: "g2", Selection: "away", Strong: false, SentAt: first},
		},
		games: map[string]*types.Game{
			"g1": {ID: "g1"},
			"g2": {ID: "g2"},
		},
		odds: map[string][]types.GameOdds{
			"g1": {{GameID: "g1", Bookmaker: "dk", MarketType: types.MarketMoneyline, HomeOdds: fp(2.10), AwayOdds: fp(1.75)}},
			"g2": {{GameID: "g2", Bookmaker: "dk", MarketType: types.MarketMoneyline, HomeOdds: fp(2.10), AwayOdds: fp(1.75)}},
		},
	}

	created, upgraded, skipped, err := Backfill(store, 5.0)
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if created != 1 || upgraded != 1 || skipped != 0 {
		t.Fatalf("counts = %d/%d/%d, want 1/1/0 (g1 conflict must not count as created)", created, upgraded, skipped)
	}
	if len(store.upgrades) != 1 || store.upgrades[0].gameID != "g1" || !store.upgrades[0].at.Equal(second) {
		t.Errorf("upgrade call = %+v, want strong row on g1 at %v", store.upgrades, second)
	}
	if _, ok := store.picks["g2"]; !ok {
		t.Error("g2 pick not recorded")
	}
}

func TestBackfillIdempotentOnMultipleStrongRows(t *testing.T) {
	first := time.Date(2026, 7, 27, 15, 0, 0, 0, time.UTC)
	second := first.Add(2 * time.Hour)
	third := first.Add(4 * time.Hour)
	store := &fakeBackfillStore{
		fakeRecordStore: newFakeRecordStore(),
		ledger: []types.ConsensusNotification{
			{ID: 1, GameID: "g1", Selection: "home", Strong: false, SentAt: first},
			{ID: 2, GameID: "g1", Selection: "home", Strong: true, SentAt: second},
			{ID: 3, GameID: "g1", Selection: "home", Strong: true, SentAt: third}, // duplicate strong row
		},
		games: map[string]*types.Game{"g1": {ID: "g1"}},
		odds: map[string][]types.GameOdds{"g1": {
			{GameID: "g1", Bookmaker: "dk", MarketType: types.MarketMoneyline, HomeOdds: fp(2.10), AwayOdds: fp(1.75)},
		}},
	}

	created, upgraded, skipped, err := Backfill(store, 5.0)
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if created != 1 || upgraded != 1 || skipped != 0 {
		t.Fatalf("counts = %d/%d/%d, want 1/1/0", created, upgraded, skipped)
	}
	if len(store.upgrades) != 1 {
		t.Errorf("upgrade calls = %d, want 1 (idempotent)", len(store.upgrades))
	}
	if store.upgrades[0].at != second {
		t.Errorf("upgrade call at = %v, want %v", store.upgrades[0].at, second)
	}
}
