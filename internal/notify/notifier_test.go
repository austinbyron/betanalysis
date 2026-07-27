package notify

import (
	"errors"
	"testing"
	"time"

	"github.com/austinbyron/betanalysis/internal/consensus"
	"github.com/austinbyron/betanalysis/pkg/types"
)

type memStore struct {
	rows    map[string][]types.ConsensusNotification
	saveErr error
}

func newMemStore() *memStore {
	return &memStore{rows: make(map[string][]types.ConsensusNotification)}
}

func (m *memStore) GetConsensusNotifications(gameID string) ([]types.ConsensusNotification, error) {
	return m.rows[gameID], nil
}

func (m *memStore) SaveConsensusNotification(gameID, selection string, strong bool) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.rows[gameID] = append(m.rows[gameID], types.ConsensusNotification{
		GameID: gameID, Selection: selection, Strong: strong, SentAt: time.Now().UTC(),
	})
	return nil
}

type memSender struct {
	sent    []Embed
	sendErr error
}

func (m *memSender) Send(e Embed) error {
	if m.sendErr != nil {
		return m.sendErr
	}
	m.sent = append(m.sent, e)
	return nil
}

var frozen = time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

func notifierPick(gameID, sel string, strong bool, commence time.Time) consensus.Pick {
	return consensus.Pick{
		Game: types.Game{ID: gameID, SportKey: "baseball_mlb", HomeTeam: "H", AwayTeam: "A",
			CommenceTime: commence},
		Selection: sel, Votes: 3, Total: 5, Strong: strong,
	}
}

func testNotifier(store Store, sender Sender) *Notifier {
	n := New(store, sender, "http://x")
	n.now = func() time.Time { return frozen }
	return n
}

func TestProcessDedup(t *testing.T) {
	future := frozen.Add(4 * time.Hour)
	tests := []struct {
		name      string
		existing  []types.ConsensusNotification
		pick      consensus.Pick
		wantSends int
		wantSaved int // rows in store for g1 after Process
	}{
		{"first qualifying pick notifies",
			nil, notifierPick("g1", "home", false, future), 1, 1},
		{"first pick already strong notifies once with strong row",
			nil, notifierPick("g1", "home", true, future), 1, 1},
		{"repeat pick is silent",
			[]types.ConsensusNotification{{GameID: "g1", Selection: "home", Strong: false}},
			notifierPick("g1", "home", false, future), 0, 1},
		{"upgrade to strong on same side sends follow-up",
			[]types.ConsensusNotification{{GameID: "g1", Selection: "home", Strong: false}},
			notifierPick("g1", "home", true, future), 1, 2},
		{"strong on flipped side is silent",
			[]types.ConsensusNotification{{GameID: "g1", Selection: "home", Strong: false}},
			notifierPick("g1", "away", true, future), 0, 1},
		{"already strong-notified stays silent forever",
			[]types.ConsensusNotification{
				{GameID: "g1", Selection: "home", Strong: false},
				{GameID: "g1", Selection: "home", Strong: true}},
			notifierPick("g1", "home", true, future), 0, 2},
		{"started game is silent even if never notified",
			nil, notifierPick("g1", "home", false, frozen.Add(-10*time.Minute)), 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMemStore()
			store.rows["g1"] = tt.existing
			sender := &memSender{}
			testNotifier(store, sender).Process([]consensus.Pick{tt.pick})
			if len(sender.sent) != tt.wantSends {
				t.Errorf("sends = %d, want %d", len(sender.sent), tt.wantSends)
			}
			if len(store.rows["g1"]) != tt.wantSaved {
				t.Errorf("rows = %d, want %d", len(store.rows["g1"]), tt.wantSaved)
			}
		})
	}
}

func TestProcessFailedSendLeavesNoRow(t *testing.T) {
	store := newMemStore()
	sender := &memSender{sendErr: errors.New("boom")}
	testNotifier(store, sender).Process(
		[]consensus.Pick{notifierPick("g1", "home", false, frozen.Add(time.Hour))})
	if len(store.rows["g1"]) != 0 {
		t.Errorf("rows = %d, want 0 after failed send", len(store.rows["g1"]))
	}
}

func TestProcessUpgradeEmbedIsMarked(t *testing.T) {
	store := newMemStore()
	store.rows["g1"] = []types.ConsensusNotification{{GameID: "g1", Selection: "home", Strong: false}}
	sender := &memSender{}
	testNotifier(store, sender).Process(
		[]consensus.Pick{notifierPick("g1", "home", true, frozen.Add(time.Hour))})
	if len(sender.sent) != 1 || sender.sent[0].Title[:1] != "⬆"[:1] {
		t.Fatalf("sent = %+v, want one upgrade embed", sender.sent)
	}
}
