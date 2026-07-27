package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/austinbyron/betanalysis/internal/consensus"
	"github.com/austinbyron/betanalysis/pkg/types"
)

func testPick(strong bool) consensus.Pick {
	return consensus.Pick{
		Game: types.Game{ID: "g1", SportKey: "baseball_mlb", HomeTeam: "Pirates", AwayTeam: "Cubs",
			CommenceTime: time.Date(2026, 7, 27, 23, 41, 0, 0, time.UTC)},
		Selection: types.OutcomeHome,
		Votes:     4, Total: 5, AvgProb: 0.58, MinEV: 0.08,
		BestOdds: 2.10, BestBook: "draftkings",
		Picks: []consensus.ModelPick{
			{Model: "thompson-raw"}, {Model: "elo-pitcher"},
			{Model: "epsilon-pitcher"}, {Model: "historical-pitcher"},
		},
		Strong: strong,
	}
}

func TestBuildEmbed(t *testing.T) {
	e := BuildEmbed(testPick(false), false, "http://betanalysis.homelab")

	if want := "⚾ 4/5 consensus — Cubs @ Pirates: Home ML"; e.Title != want {
		t.Errorf("title = %q, want %q", e.Title, want)
	}
	if want := "http://betanalysis.homelab/analysis/game/g1"; e.URL != want {
		t.Errorf("url = %q, want %q", e.URL, want)
	}
	if e.Color != colorGreen {
		t.Errorf("color = %#x, want green", e.Color)
	}
	joined := ""
	for _, f := range e.Fields {
		joined += f.Name + "=" + f.Value + ";"
	}
	for _, want := range []string{"58%", "2.10 (draftkings)", "+0.08", "thompson-raw",
		"Jul 27", "ET"} {
		if !strings.Contains(joined, want) {
			t.Errorf("fields %q missing %q", joined, want)
		}
	}
}

func TestBuildEmbedStrongAndUpgrade(t *testing.T) {
	strong := BuildEmbed(testPick(true), false, "http://x")
	if !strings.HasPrefix(strong.Title, "🔥 STRONG") || strong.Color != colorOrange {
		t.Errorf("strong embed = %q color %#x", strong.Title, strong.Color)
	}
	up := BuildEmbed(testPick(true), true, "http://x")
	if !strings.HasPrefix(up.Title, "⬆️ Upgraded to STRONG") {
		t.Errorf("upgrade title = %q", up.Title)
	}
}

func TestDiscordSend(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent) // Discord returns 204
	}))
	defer srv.Close()

	d := NewDiscord(srv.URL)
	if err := d.Send(Embed{Title: "t"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	var payload struct {
		Embeds []Embed `json:"embeds"`
	}
	if err := json.Unmarshal(got, &payload); err != nil || len(payload.Embeds) != 1 || payload.Embeds[0].Title != "t" {
		t.Errorf("payload = %s (err %v)", got, err)
	}
}

func TestDiscordSendNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	if err := NewDiscord(srv.URL).Send(Embed{Title: "t"}); err == nil {
		t.Error("want error on 429, got nil")
	}
}

func TestDiscordSendErrorOmitsURL(t *testing.T) {
	d := NewDiscord("http://127.0.0.1:1/secret-webhook-token")
	err := d.Send(Embed{Title: "t"})
	if err == nil {
		t.Fatal("want error from unreachable host")
	}
	if strings.Contains(err.Error(), "secret-webhook-token") {
		t.Errorf("error leaks webhook URL: %v", err)
	}
}
