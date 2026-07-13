package mlb

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/austinbyron/betanalysis/pkg/types"
)

func almostEqual(a, b, tol float64) bool { return math.Abs(a-b) < tol }

func TestParseInnings(t *testing.T) {
	// The decimal digit is outs, not tenths: 117.1 = 117⅓
	tests := map[string]float64{
		"117.0": 117.0,
		"117.1": 117 + 1.0/3,
		"117.2": 117 + 2.0/3,
		"0.2":   2.0 / 3,
		"":      0,
		"bad":   0,
	}
	for in, want := range tests {
		if got := parseInnings(in); !almostEqual(got, want, 1e-9) {
			t.Errorf("parseInnings(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestQualityFromComponents(t *testing.T) {
	// Cristopher Sánchez 2026 live-probed line: 136K/23BB/2HBP/8HR in 117 IP
	// → FIP ≈ 2.31, regressed ≈ 2.78 → about 1.4 runs/9 better than average
	quality := qualityFromComponents(117, 136, 23, 2, 8)
	if quality < 1.0 || quality > 1.8 {
		t.Errorf("ace quality = %v, want roughly 1.0–1.8 runs/9", quality)
	}

	// League-average components → quality ≈ 0
	// FIP = (13*13 + 3*(35+5) - 2*95)/100 + 3.10 = 4.09 ≈ league
	avg := qualityFromComponents(100, 95, 35, 5, 13)
	if !almostEqual(avg, 0, 0.15) {
		t.Errorf("average pitcher quality = %v, want ~0", avg)
	}

	// Tiny sample regresses hard: brilliant 10 innings shouldn't read as an ace
	small := qualityFromComponents(10, 20, 1, 0, 0)
	full := qualityFromComponents(150, 300, 15, 0, 0)
	if small >= full/2 {
		t.Errorf("10-IP sample (%v) must regress far more than 150-IP (%v)", small, full)
	}

	if got := qualityFromComponents(0, 5, 1, 0, 0); got != 0 {
		t.Errorf("zero innings = %v, want 0", got)
	}
}

// statsAPIStub serves schedule + pitcher stats fixtures and counts requests
func statsAPIStub(t *testing.T, gameTime time.Time, scheduleHits, statsHits *int) *httptest.Server {
	t.Helper()
	schedule := fmt.Sprintf(`{
	  "dates": [{"games": [
	    {
	      "gameDate": %q,
	      "teams": {
	        "home": {"team": {"name": "Kansas City Royals"}, "probablePitcher": {"id": 1, "fullName": "Average Arm"}},
	        "away": {"team": {"name": "Philadelphia Phillies"}, "probablePitcher": {"id": 2, "fullName": "Ace Starter"}}
	      }
	    },
	    {
	      "gameDate": %q,
	      "teams": {
	        "home": {"team": {"name": "Detroit Tigers"}},
	        "away": {"team": {"name": "Cleveland Guardians"}, "probablePitcher": {"id": 2, "fullName": "Ace Starter"}}
	      }
	    }
	  ]}]
	}`, gameTime.Format(time.RFC3339), gameTime.Add(2*time.Hour).Format(time.RFC3339))

	// id 1: league average; id 2: ace (Sánchez-like line)
	stats := map[string]string{
		"/people/1/stats": `{"stats":[{"splits":[{"stat":{"inningsPitched":"100.0","strikeOuts":95,"baseOnBalls":35,"hitByPitch":5,"homeRuns":13}}]}]}`,
		"/people/2/stats": `{"stats":[{"splits":[{"stat":{"inningsPitched":"117.0","strikeOuts":136,"baseOnBalls":23,"hitByPitch":2,"homeRuns":8}}]}]}`,
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/schedule"):
			*scheduleHits++
			w.Write([]byte(schedule))
		case strings.Contains(r.URL.Path, "/people/"):
			*statsHits++
			for path, body := range stats {
				if strings.Contains(r.URL.Path, strings.TrimSuffix(path, "/stats")) {
					w.Write([]byte(body))
					return
				}
			}
			w.Write([]byte(`{"stats":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestStartersForGameAndCaching(t *testing.T) {
	gameTime := time.Date(2026, 7, 6, 20, 10, 0, 0, time.UTC)
	var scheduleHits, statsHits int
	server := statsAPIStub(t, gameTime, &scheduleHits, &statsHits)
	defer server.Close()

	client := NewClientWithBaseURL(server.URL)

	home, away, err := client.StartersForGame("Kansas City Royals", "Philadelphia Phillies", gameTime)
	if err != nil {
		t.Fatalf("StartersForGame: %v", err)
	}
	if home == nil || home.Name != "Average Arm" || away == nil || away.Name != "Ace Starter" {
		t.Errorf("starters = %+v / %+v", home, away)
	}

	// Unannounced probable comes back nil without error
	h2, a2, err := client.StartersForGame("Detroit Tigers", "Cleveland Guardians", gameTime.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("StartersForGame tigers: %v", err)
	}
	if h2 != nil || a2 == nil {
		t.Errorf("expected nil home starter and announced away, got %+v / %+v", h2, a2)
	}

	if scheduleHits != 1 {
		t.Errorf("schedule fetched %d times for same day, want 1 (cache)", scheduleHits)
	}

	if _, _, err := client.StartersForGame("Nobody", "Nowhere", gameTime); err == nil {
		t.Error("unknown matchup should error")
	}
}

func TestPitcherAdjuster(t *testing.T) {
	gameTime := time.Date(2026, 7, 6, 20, 10, 0, 0, time.UTC)
	var scheduleHits, statsHits int
	server := statsAPIStub(t, gameTime, &scheduleHits, &statsHits)
	defer server.Close()

	adjuster := NewPitcherAdjuster(NewClientWithBaseURL(server.URL))

	game := types.Game{
		ID:           "g1",
		SportKey:     types.SportMLB,
		HomeTeam:     "Kansas City Royals",
		AwayTeam:     "Philadelphia Phillies",
		CommenceTime: gameTime,
	}

	// Away has the ace: home probability must drop from the 50/50 baseline
	home, away := adjuster.Adjust(game, 0.5, 0.5)
	if home >= 0.5 {
		t.Errorf("home prob = %v, want < 0.5 when the away side has the ace", home)
	}
	if !almostEqual(home+away, 1.0, 1e-9) {
		t.Errorf("probabilities must stay a pair summing to 1, got %v", home+away)
	}
	// Ace ≈ +1.4 quality → roughly 0.11*0.65*1.4 ≈ 10 points vs an average arm
	if home < 0.35 {
		t.Errorf("home prob = %v — adjustment larger than the model should ever produce", home)
	}

	// Non-MLB games pass through untouched
	nfl := types.Game{ID: "g2", SportKey: types.SportNFL, HomeTeam: "Lions", AwayTeam: "Packers", CommenceTime: gameTime}
	if h, a := adjuster.Adjust(nfl, 0.6, 0.4); h != 0.6 || a != 0.4 {
		t.Errorf("non-MLB game adjusted: %v/%v", h, a)
	}

	// Unknown matchup (lookup failure) passes through untouched
	unknown := types.Game{ID: "g3", SportKey: types.SportMLB, HomeTeam: "Nobody", AwayTeam: "Nowhere", CommenceTime: gameTime}
	if h, a := adjuster.Adjust(unknown, 0.55, 0.45); h != 0.55 || a != 0.45 {
		t.Errorf("failed lookup must not adjust: %v/%v", h, a)
	}
}

func TestAdjusterClampsExtremes(t *testing.T) {
	gameTime := time.Date(2026, 7, 6, 20, 10, 0, 0, time.UTC)
	var scheduleHits, statsHits int
	server := statsAPIStub(t, gameTime, &scheduleHits, &statsHits)
	defer server.Close()

	adjuster := NewPitcherAdjuster(NewClientWithBaseURL(server.URL))
	game := types.Game{
		ID: "g1", SportKey: types.SportMLB,
		HomeTeam: "Kansas City Royals", AwayTeam: "Philadelphia Phillies",
		CommenceTime: gameTime,
	}

	// Already-extreme probability stays inside the clamp band
	home, away := adjuster.Adjust(game, 0.06, 0.94)
	if home < minProb || home > maxProb || away < minProb || away > maxProb {
		t.Errorf("clamp violated: %v/%v", home, away)
	}
}
