// Package notify pushes consensus picks to a Discord webhook, at most
// twice per game (initial + strong upgrade). The webhook URL is secret:
// it reaches the daemon only via the BETANALYSIS_DISCORD_WEBHOOK env var.
package notify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/austinbyron/betanalysis/internal/consensus"
	"github.com/austinbyron/betanalysis/pkg/types"
)

const (
	colorGreen  = 0x2ECC71
	colorOrange = 0xE67E22
)

// Embed is the subset of a Discord message embed we use.
type Embed struct {
	Title  string       `json:"title"`
	URL    string       `json:"url,omitempty"`
	Color  int          `json:"color"`
	Fields []EmbedField `json:"fields,omitempty"`
}

// EmbedField is one name/value row inside an embed.
type EmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

// Discord posts embeds to a single webhook URL.
type Discord struct {
	url    string
	client *http.Client
}

// NewDiscord wraps a webhook URL. The URL is never logged.
func NewDiscord(url string) *Discord {
	return &Discord{url: url, client: &http.Client{Timeout: 15 * time.Second}}
}

// Send posts one embed; any non-2xx response is an error.
func (d *Discord) Send(e Embed) error {
	body, err := json.Marshal(struct {
		Embeds []Embed `json:"embeds"`
	}{Embeds: []Embed{e}})
	if err != nil {
		return err
	}
	resp, err := d.client.Post(d.url, "application/json", bytes.NewReader(body))
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err // drop the URL, keep the cause
		}
		return fmt.Errorf("discord webhook post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("discord webhook status %d", resp.StatusCode)
	}
	return nil
}

var sportEmoji = map[string]string{
	"baseball_mlb":           "⚾",
	"americanfootball_nfl":   "🏈",
	"americanfootball_ncaaf": "🏈",
	"basketball_nba":         "🏀",
	"basketball_ncaab":       "🏀",
}

// eastern mirrors the dashboard's display timezone; fall back to UTC if
// the zone db is missing (stripped containers).
var eastern = func() *time.Location {
	if loc, err := time.LoadLocation("America/New_York"); err == nil {
		return loc
	}
	return time.UTC
}()

// BuildEmbed renders one consensus pick as a Discord embed. upgrade marks
// the strong-upgrade follow-up message.
func BuildEmbed(p consensus.Pick, upgrade bool, baseURL string) Embed {
	emoji := sportEmoji[p.Game.SportKey]
	if emoji == "" {
		emoji = "🎯"
	}

	side := "Away ML"
	if p.Selection == types.OutcomeHome {
		side = "Home ML"
	}
	matchup := fmt.Sprintf("%s @ %s: %s", p.Game.AwayTeam, p.Game.HomeTeam, side)

	var title string
	color := colorGreen
	switch {
	case upgrade:
		title = fmt.Sprintf("⬆️ Upgraded to STRONG %d/%d — %s", p.Votes, p.Total, matchup)
		color = colorOrange
	case p.Strong:
		title = fmt.Sprintf("🔥 STRONG %d/%d — %s %s", p.Votes, p.Total, emoji, matchup)
		color = colorOrange
	default:
		title = fmt.Sprintf("%s %d/%d consensus — %s", emoji, p.Votes, p.Total, matchup)
	}

	models := ""
	for i, mp := range p.Picks {
		if i > 0 {
			models += ", "
		}
		models += mp.Model
	}

	return Embed{
		Title: title,
		URL:   fmt.Sprintf("%s/analysis/game/%s", baseURL, p.Game.ID),
		Color: color,
		Fields: []EmbedField{
			{Name: "Avg model prob", Value: fmt.Sprintf("%.0f%%", p.AvgProb*100), Inline: true},
			{Name: "Best odds", Value: fmt.Sprintf("%.2f (%s)", p.BestOdds, p.BestBook), Inline: true},
			{Name: "Min EV", Value: fmt.Sprintf("%+.2f", p.MinEV), Inline: true},
			{Name: "Start", Value: p.Game.CommenceTime.In(eastern).Format("Mon Jan 2 3:04 PM") + " ET", Inline: true},
			{Name: "Models", Value: models, Inline: false},
		},
	}
}
