package web

import (
	"fmt"
	"math"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/austinbyron/betanalysis/internal/analysis"
	"github.com/austinbyron/betanalysis/internal/contenders"
	"github.com/austinbyron/betanalysis/pkg/types"
)

// recordStats is a fixed win/loss table for deterministic strips
type recordStats map[string][2]int

func (r recordStats) TeamRecord(team, _ string) (int, int) {
	rec := r[team]
	return rec[0], rec[1]
}

func analysisFixture() *fakeStore {
	return &fakeStore{
		portfolios: map[string]*types.Portfolio{
			"default": {ID: "default", Balance: 1000, InitialBankroll: 1000},
		},
		games: []types.Game{
			{ID: "g1", HomeTeam: "Padres", AwayTeam: "Dodgers", SportKey: "baseball_mlb",
				Status: "scheduled", CommenceTime: time.Now().Add(5 * time.Hour)},
			{ID: "g2", HomeTeam: "Mets", AwayTeam: "Braves", SportKey: "baseball_mlb",
				Status: "scheduled", CommenceTime: time.Now().Add(6 * time.Hour)},
		},
		odds: map[string][]types.GameOdds{
			"g1": {{GameID: "g1", Bookmaker: "draftkings", MarketType: types.MarketMoneyline,
				HomeOdds: f64(2.0), AwayOdds: f64(2.0), RetrievedAt: time.Now()}},
			"g2": {{GameID: "g2", Bookmaker: "draftkings", MarketType: types.MarketMoneyline,
				HomeOdds: f64(2.0), AwayOdds: f64(2.0), RetrievedAt: time.Now()}},
		},
	}
}

// strengthLineup gives Padres a strong record so g1 has a big model-market gap
func strengthLineup() []contenders.Contender {
	stats := recordStats{
		"Padres": {30, 10}, "Dodgers": {10, 30},
		"Mets": {20, 20}, "Braves": {20, 20},
	}
	return []contenders.Contender{{
		Name: "historical", Portfolio: "default",
		Selector: analysis.NewSelector(analysis.NewHistorical(stats), "historical", 0, 1.5, 0.05),
	}}
}

func TestAnalysisPageRendersMatchupStrips(t *testing.T) {
	srv := newTestServerWithLineup(t, analysisFixture(), strengthLineup())
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/analysis", nil))
	body := rec.Body.String()

	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	for _, want := range []string{`href="/analysis/game/g1"`, `href="/analysis/game/g2"`,
		"Dodgers @ Padres", "historical"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in analysis page", want)
		}
	}
	// g1's model-market gap is bigger than g2's, so g1 must come first
	if strings.Index(body, "g1") > strings.Index(body, "g2") {
		t.Fatal("matchups must sort by model-market gap, largest first")
	}
}

func TestBuildMatchupsGapAndMarket(t *testing.T) {
	srv := newTestServerWithLineup(t, analysisFixture(), strengthLineup())
	rows := srv.buildMatchups()
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	top := rows[0]
	if top.Game.ID != "g1" {
		t.Fatalf("largest gap first, got %s", top.Game.ID)
	}
	if math.Abs(top.MarketProb-0.5) > 1e-9 {
		t.Fatalf("even odds de-vig to 0.5, got %v", top.MarketProb)
	}
	if len(top.Dots) != 1 || top.Dots[0].Slot != 1 {
		t.Fatalf("one contender dot on slot 1, got %+v", top.Dots)
	}
	if top.Gap <= rows[1].Gap {
		t.Fatalf("sort key broken: %v <= %v", top.Gap, rows[1].Gap)
	}
}

func TestMedianMarketProb(t *testing.T) {
	odds := []types.GameOdds{
		{MarketType: types.MarketMoneyline, HomeOdds: f64(2.0), AwayOdds: f64(2.0)},
		{MarketType: types.MarketMoneyline, HomeOdds: f64(1.5), AwayOdds: f64(3.0)},
		{MarketType: types.MarketMoneyline, HomeOdds: f64(1.5), AwayOdds: f64(3.0)},
		{MarketType: "spreads", HomeOdds: f64(9.9), AwayOdds: f64(9.9)}, // ignored
	}
	got, ok := medianMarketProb(odds)
	if !ok {
		t.Fatal("expected a market prob")
	}
	want, _ := analysis.Devig(1.5, 3.0) // the median book
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("median = %v, want %v", got, want)
	}
	if _, ok := medianMarketProb(nil); ok {
		t.Fatal("no moneyline odds must report !ok")
	}
}

func TestBuildTeamStrengthSortsAndBounds(t *testing.T) {
	store := analysisFixture()
	store.teamStats = map[string][]types.TeamStats{
		"baseball_mlb": {
			{TeamName: "Padres", SportKey: "baseball_mlb"},
			{TeamName: "Dodgers", SportKey: "baseball_mlb"},
		},
	}
	srv := newTestServerWithLineupAndStats(t, store, strengthLineup(), recordStats{
		"Padres": {30, 10}, "Dodgers": {10, 30},
	})

	sports := srv.buildTeamStrength()
	if len(sports) != 1 || len(sports[0].Teams) != 2 {
		t.Fatalf("want 1 sport / 2 teams, got %+v", sports)
	}
	rows := sports[0].Teams
	if rows[0].Team != "Padres" {
		t.Fatalf("strongest mean first, got %s", rows[0].Team)
	}
	for _, r := range rows {
		if !(r.Lo < r.Mean && r.Mean < r.Hi) {
			t.Fatalf("interval ordering broken: %+v", r)
		}
		if r.Lo < 0 || r.Hi > 1 {
			t.Fatalf("interval out of [0,1]: %+v", r)
		}
	}
	// The 30-10 team must be tighter than a hypothetical thin record; at
	// minimum its interval must not be inverted relative to its wins.
	if rows[0].Wins != 30 || rows[0].Losses != 10 {
		t.Fatalf("layered record surfaced wrong: %+v", rows[0])
	}
}

func TestBuildTeamStrengthBreaksTiesByName(t *testing.T) {
	store := analysisFixture()
	store.teamStats = map[string][]types.TeamStats{
		"baseball_mlb": {
			{TeamName: "Zebras", SportKey: "baseball_mlb"},
			{TeamName: "Aardvarks", SportKey: "baseball_mlb"},
		},
	}
	srv := newTestServerWithLineupAndStats(t, store, strengthLineup(), recordStats{
		"Zebras": {10, 10}, "Aardvarks": {10, 10},
	})
	sports := srv.buildTeamStrength()
	if len(sports) != 1 || len(sports[0].Teams) != 2 {
		t.Fatalf("want 1 sport / 2 teams, got %+v", sports)
	}
	if sports[0].Teams[0].Team != "Aardvarks" {
		t.Fatalf("equal means must order by name, got %s first", sports[0].Teams[0].Team)
	}
}

func TestAnalysisPageRendersTeamStrength(t *testing.T) {
	store := analysisFixture()
	store.teamStats = map[string][]types.TeamStats{
		"baseball_mlb": {{TeamName: "Padres", SportKey: "baseball_mlb"}},
	}
	srv := newTestServerWithLineupAndStats(t, store, strengthLineup(), recordStats{"Padres": {30, 10}})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/analysis", nil))
	if !strings.Contains(rec.Body.String(), "team-strength") {
		t.Fatal("team strength section missing")
	}
}

func TestBuildEloLandscape(t *testing.T) {
	hs, as := 8, 2
	hs2, as2 := 1, 6
	now := time.Now()
	store := analysisFixture()
	store.finished = map[string][]types.Game{
		"baseball_mlb": {
			// >14d ago: establishes an old baseline for the delta
			{HomeTeam: "Padres", AwayTeam: "Dodgers", SportKey: "baseball_mlb", Status: "finished",
				CommenceTime: now.Add(-20 * 24 * time.Hour), HomeScore: &hs, AwayScore: &as},
			// recent: moves ratings inside the delta window
			{HomeTeam: "Dodgers", AwayTeam: "Padres", SportKey: "baseball_mlb", Status: "finished",
				CommenceTime: now.Add(-2 * 24 * time.Hour), HomeScore: &hs2, AwayScore: &as2},
		},
	}
	srv := newTestServerWithLineup(t, store, strengthLineup())

	sports := srv.buildEloLandscape(now)
	if len(sports) != 1 || len(sports[0].Rows) != 2 {
		t.Fatalf("want 1 sport / 2 teams, got %+v", sports)
	}
	rows := sports[0].Rows
	if rows[0].Team != "Padres" {
		t.Fatalf("Padres won both, must rank first: %+v", rows)
	}
	if rows[0].Rating <= rows[1].Rating {
		t.Fatal("ranked by rating descending")
	}
	// Padres won the recent away game too, so their 14-day delta is positive
	if rows[0].Delta14 <= 0 || !rows[0].Positive {
		t.Fatalf("recent winner needs positive delta: %+v", rows[0])
	}
	if rows[1].Delta14 >= 0 {
		t.Fatalf("recent loser needs negative delta: %+v", rows[1])
	}
}

func TestBuildDiagnosticsCalibration(t *testing.T) {
	store := analysisFixture()
	// 6 settled bets at ~0.6 predicted, 3 won: one calibration bin at (0.6, 0.5)
	for i := 0; i < 6; i++ {
		status := types.BetStatusWon
		if i >= 3 {
			status = types.BetStatusLost
		}
		store.settled = append(store.settled, types.Bet{
			ID: fmt.Sprintf("b%d", i), PortfolioID: "default", GameID: "g1",
			Selection: "home", Odds: 2.0, Stake: 10, Probability: 0.6,
			ModelID: "historical", Status: status,
		})
	}
	srv := newTestServerWithLineup(t, store, strengthLineup())

	diags := srv.buildDiagnostics()
	if len(diags) != 1 {
		t.Fatalf("one contender, got %d", len(diags))
	}
	d := diags[0]
	if d.Model != "historical" || d.N != 6 {
		t.Fatalf("diag = %+v", d)
	}
	if len(d.Calib) != 1 {
		t.Fatalf("want 1 calibration dot, got %d", len(d.Calib))
	}
	if len(d.OddsXs) != 6 || len(d.EVXs) != 6 {
		t.Fatalf("profile dots: odds=%d ev=%d, want 6 each", len(d.OddsXs), len(d.EVXs))
	}
}

func TestDiagnosticsIncludeShadowPreviews(t *testing.T) {
	store := analysisFixture()
	for i := 0; i < 5; i++ {
		store.settledPreviews = append(store.settledPreviews, types.PreviewBet{
			ID: fmt.Sprintf("p%d", i), GameID: "g1", ModelID: "historical",
			Selection: "home", Odds: 2.0, Probability: 0.55, Status: types.BetStatusWon,
		})
	}
	srv := newTestServerWithLineup(t, store, strengthLineup())
	diags := srv.buildDiagnostics()
	if len(diags) != 1 || diags[0].N != 5 {
		t.Fatalf("previews must feed calibration: %+v", diags)
	}
}

func TestAnalysisPageRendersAllSections(t *testing.T) {
	hs, as := 5, 3
	store := analysisFixture()
	store.finished = map[string][]types.Game{"baseball_mlb": {
		{HomeTeam: "Padres", AwayTeam: "Dodgers", SportKey: "baseball_mlb", Status: "finished",
			CommenceTime: time.Now().Add(-48 * time.Hour), HomeScore: &hs, AwayScore: &as},
	}}
	store.teamStats = map[string][]types.TeamStats{"baseball_mlb": {
		{TeamName: "Padres", SportKey: "baseball_mlb"}, {TeamName: "Dodgers", SportKey: "baseball_mlb"},
	}}
	for i := 0; i < 6; i++ {
		store.settled = append(store.settled, types.Bet{
			ID: fmt.Sprintf("s%d", i), PortfolioID: "default", GameID: "g1", Selection: "home",
			Odds: 2.0, Stake: 10, Probability: 0.6, ModelID: "historical", Status: types.BetStatusWon,
		})
	}
	srv := newTestServerWithLineupAndStats(t, store, strengthLineup(),
		recordStats{"Padres": {30, 10}, "Dodgers": {10, 30}})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/analysis", nil))
	body := rec.Body.String()
	for _, section := range []string{"/analysis/game/g1", "team-strength", "elo-landscape", "diagnostics"} {
		if !strings.Contains(body, section) {
			t.Fatalf("full page missing %q", section)
		}
	}
}
