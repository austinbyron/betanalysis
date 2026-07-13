package web

import (
	"math"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/austinbyron/betanalysis/internal/analysis"
	"github.com/austinbyron/betanalysis/internal/config"
	"github.com/austinbyron/betanalysis/internal/contenders"
	"github.com/austinbyron/betanalysis/pkg/types"
)

type fakeStore struct {
	portfolios      map[string]*types.Portfolio
	pending         []types.Bet
	settled         []types.Bet
	games           []types.Game
	odds            map[string][]types.GameOdds
	betExists       bool
	pendingPreviews []types.PreviewBet
	settledPreviews []types.PreviewBet
	quota           *types.APIQuota
}

func (f *fakeStore) GetPortfolio(id string) (*types.Portfolio, error) { return f.portfolios[id], nil }
func (f *fakeStore) GetPendingBets() ([]types.Bet, error)             { return f.pending, nil }
func (f *fakeStore) GetSettledBets() ([]types.Bet, error)             { return f.settled, nil }
func (f *fakeStore) GetUpcomingGames(string) ([]types.Game, error)    { return f.games, nil }
func (f *fakeStore) BetExists(string, string) (bool, error)           { return f.betExists, nil }
func (f *fakeStore) GetOddsForGame(id string) ([]types.GameOdds, error) {
	return f.odds[id], nil
}
func (f *fakeStore) GetGameByID(id string) (*types.Game, error) {
	for i := range f.games {
		if f.games[i].ID == id {
			return &f.games[i], nil
		}
	}
	return nil, nil
}
func (f *fakeStore) GetPendingPreviewBets() ([]types.PreviewBet, error) {
	return f.pendingPreviews, nil
}
func (f *fakeStore) GetSettledPreviewBets() ([]types.PreviewBet, error) {
	return f.settledPreviews, nil
}
func (f *fakeStore) GetAPIQuota() (*types.APIQuota, error) { return f.quota, nil }

type fixedStats struct{}

func (fixedStats) TeamRecord(string, string) (int, int) { return 0, 0 }

func f64(v float64) *float64 { return &v }

func testConfig() *config.Config {
	return &config.Config{
		Trading: config.TradingConfig{
			InitialBankroll:  1000,
			MinStake:         1,
			MaxStakeFraction: 0.05,
			KellyFraction:    0.5,
			MinOdds:          1.5,
			MinExpectedValue: 0.05,
			PortfolioID:      "default",
		},
		Analysis:  config.AnalysisConfig{ModelType: "historical", MarketWeight: 0},
		Server:    config.ServerConfig{Enabled: true, Port: 0},
		SportKeys: []string{"baseball_mlb"},
	}
}

func testSelector(name string) *analysis.Selector {
	return analysis.NewSelector(analysis.NewHistorical(fixedStats{}), name, 0, 1.5, 0.05)
}

func newTestServer(t *testing.T, store *fakeStore) *Server {
	t.Helper()
	return newTestServerWithLineup(t, store, []contenders.Contender{{
		Name: "historical", Portfolio: "default", Selector: testSelector(""),
	}})
}

func newTestServerWithLineup(t *testing.T, store *fakeStore, lineup []contenders.Contender) *Server {
	t.Helper()
	srv, err := NewServer(store, lineup, testConfig(), nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv
}

// fakeLinker links every game whose ID it knows
type fakeLinker map[string]string

func (f fakeLinker) GameURL(g types.Game) string { return f[g.ID] }

func TestDashboardLinksGamesToESPN(t *testing.T) {
	store := &fakeStore{
		portfolios: map[string]*types.Portfolio{
			"default": {ID: "default", Balance: 1000, InitialBankroll: 1000},
		},
		games: []types.Game{{ID: "g3", HomeTeam: "Padres", AwayTeam: "Dodgers", SportKey: "baseball_mlb",
			Status: "scheduled", CommenceTime: time.Now().Add(5 * time.Hour)}},
		odds: map[string][]types.GameOdds{
			"g3": {{GameID: "g3", Bookmaker: "draftkings", MarketType: types.MarketMoneyline,
				HomeOdds: f64(2.4), AwayOdds: f64(2.4), RetrievedAt: time.Now()}},
		},
		pending: []types.Bet{{
			ID: "b1", PortfolioID: "default", GameID: "g3", Selection: types.OutcomeHome,
			Odds: 2.4, Stake: 10, Status: types.BetStatusPending, CreatedAt: time.Now(),
		}},
	}
	lineup := []contenders.Contender{
		{Name: "hist-a", Portfolio: "default", Selector: testSelector("hist-a")},
	}
	linker := fakeLinker{"g3": "https://www.espn.com/mlb/game/_/gameId/12345"}
	srv, err := NewServer(store, lineup, testConfig(), linker)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	_, body := render(t, srv, "/")
	if got := strings.Count(body, `href="https://www.espn.com/mlb/game/_/gameId/12345"`); got < 2 {
		t.Errorf("expected matchup links in recommendations and active bets, found %d", got)
	}
}

func render(t *testing.T, srv *Server, path string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestDashboardRendersEmptyState(t *testing.T) {
	srv := newTestServer(t, &fakeStore{odds: map[string][]types.GameOdds{}})

	code, body := render(t, srv, "/")

	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	if !strings.Contains(body, "No portfolio yet") {
		t.Error("empty state should explain the missing portfolio")
	}
	if !strings.Contains(body, "Paper trading only") {
		t.Error("footer disclaimer missing")
	}
}

func TestDashboardShowsPortfolioRecommendationsAndResults(t *testing.T) {
	settledAt := time.Date(2026, 7, 4, 22, 0, 0, 0, time.UTC)
	won := 120.0
	store := &fakeStore{
		portfolios: map[string]*types.Portfolio{"default": {
			ID: "default", Name: "Default Portfolio",
			Balance: 1070, InitialBankroll: 1000,
			BetsPlaced: 2, BetsWon: 1, BetsLost: 0,
			TotalWagered: 100, TotalProfitLoss: 70,
			ActiveBetsCount: 1, ActiveBetsValue: 50,
		}},
		pending: []types.Bet{{
			ID: "b2", PortfolioID: "default", GameID: "g2", Selection: types.OutcomeHome,
			Odds: 2.1, Stake: 50, PotentialWin: 55, Probability: 0.55,
			Bookmaker: "draftkings", Status: types.BetStatusPending,
		}},
		settled: []types.Bet{{
			ID: "b1", PortfolioID: "default", GameID: "g1", Selection: types.OutcomeAway,
			Odds: 2.4, Stake: 50, Probability: 0.62, ActualWin: &won, SettledAt: &settledAt,
			Bookmaker: "fanduel", Status: types.BetStatusWon,
		}},
		games: []types.Game{
			{ID: "g1", HomeTeam: "Royals", AwayTeam: "Phillies", SportKey: "baseball_mlb", Status: "finished"},
			{ID: "g2", HomeTeam: "Tigers", AwayTeam: "Guardians", SportKey: "baseball_mlb", Status: "scheduled",
				CommenceTime: time.Now().Add(3 * time.Hour)},
			{ID: "g3", HomeTeam: "Padres", AwayTeam: "Dodgers", SportKey: "baseball_mlb", Status: "scheduled",
				CommenceTime: time.Now().Add(5 * time.Hour)},
		},
		odds: map[string][]types.GameOdds{
			// 2.4/2.4 at p=0.5 → EV 0.2, clears thresholds
			"g3": {{GameID: "g3", Bookmaker: "draftkings", MarketType: types.MarketMoneyline,
				HomeOdds: f64(2.4), AwayOdds: f64(2.4), RetrievedAt: time.Now().Add(-10 * time.Minute)}},
		},
	}

	srv := newTestServer(t, store)
	code, body := render(t, srv, "/")

	if code != 200 {
		t.Fatalf("status = %d", code)
	}

	for _, want := range []string{
		"$1070.00",           // balance tile
		"&#43;70.00",         // P/L tile (html/template escapes the plus)
		"Dodgers @ Padres",   // recommendation matchup
		"Guardians @ Tigers", // active bet matchup
		"Phillies @ Royals",  // settled matchup
		"62%",                // settled row shows the model's probability
		"Won ✓",              // result label, not color-alone
		"Bankroll",           // equity section renders with settled bets
		"data-label",         // chart points carry hover labels
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}

func TestLeaderboardRanksContenders(t *testing.T) {
	store := &fakeStore{portfolios: map[string]*types.Portfolio{
		"default":      {ID: "default", Name: "thompson-pitcher", Balance: 743, InitialBankroll: 1000, TotalProfitLoss: -8.16, BetsWon: 11, BetsLost: 12},
		"thompson-raw": {ID: "thompson-raw", Name: "thompson-raw", Balance: 1050, InitialBankroll: 1000, TotalProfitLoss: 50, BetsWon: 3, BetsLost: 1},
	}, odds: map[string][]types.GameOdds{}}

	lineup := []contenders.Contender{
		{Name: "thompson-pitcher", Portfolio: "default", Selector: testSelector("thompson-pitcher")},
		{Name: "thompson-raw", Portfolio: "thompson-raw", Selector: testSelector("thompson-raw")},
		{Name: "ghost", Portfolio: "ghost", Selector: testSelector("ghost")},
	}
	srv := newTestServerWithLineup(t, store, lineup)

	code, body := render(t, srv, "/")
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	for _, want := range []string{"Leaderboard", "thompson-pitcher", "thompson-raw", "$743.00", "$1050.00"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	// Scope to the leaderboard table: the filter chips above it list every
	// lineup model (ghost included), which is correct there.
	lbStart := strings.Index(body, "Leaderboard")
	lbEnd := strings.Index(body, "Recommendations")
	if lbStart < 0 || lbEnd < lbStart {
		t.Fatal("could not locate leaderboard section")
	}
	board := body[lbStart:lbEnd]
	if strings.Contains(board, "ghost") {
		t.Error("contender without a portfolio must not render a leaderboard row")
	}
	// profit-sorted: thompson-raw (+50) before thompson-pitcher (-8.16)
	if strings.Index(board, "thompson-raw") > strings.Index(board, "thompson-pitcher") {
		t.Error("leaderboard must sort by profit descending")
	}
}

func TestRecommendationsListEveryContender(t *testing.T) {
	store := &fakeStore{
		portfolios: map[string]*types.Portfolio{
			"default": {ID: "default", Balance: 1000, InitialBankroll: 1000},
		},
		games: []types.Game{{ID: "g3", HomeTeam: "Padres", AwayTeam: "Dodgers", SportKey: "baseball_mlb",
			Status: "scheduled", CommenceTime: time.Now().Add(5 * time.Hour)}},
		odds: map[string][]types.GameOdds{
			"g3": {{GameID: "g3", Bookmaker: "draftkings", MarketType: types.MarketMoneyline,
				HomeOdds: f64(2.4), AwayOdds: f64(2.4), RetrievedAt: time.Now()}},
		},
	}
	lineup := []contenders.Contender{
		{Name: "hist-a", Portfolio: "default", Selector: testSelector("hist-a")},
		{Name: "hist-b", Portfolio: "hist-b", Selector: testSelector("hist-b")},
		{Name: "hist-c", Portfolio: "hist-c", Selector: testSelector("hist-c")},
		{Name: "hist-d", Portfolio: "hist-d", Selector: testSelector("hist-d")},
	}
	srv := newTestServerWithLineup(t, store, lineup)

	_, body := render(t, srv, "/")
	for _, want := range []string{"hist-a", "hist-b", "hist-c", "hist-d"} {
		if strings.Count(body, want) < 1 {
			t.Errorf("recommendations missing contender %q", want)
		}
	}
	// Four identical estimators must agree -> strong consensus renders
	for _, want := range []string{"Consensus picks", "4/4", "badge-strong"} {
		if !strings.Contains(body, want) {
			t.Errorf("consensus section missing %q", want)
		}
	}
}

func TestWarmupPreviewShowsSuppressedBets(t *testing.T) {
	// Cold records + warmup gate: the blend collapses to the market, so no
	// bet clears — but the full-confidence pick must render as a preview.
	settledAt := time.Date(2026, 7, 6, 22, 0, 0, 0, time.UTC)
	wonPayout := 60.0
	lostPayout := 0.0

	store := &fakeStore{
		portfolios: map[string]*types.Portfolio{
			"default": {ID: "default", Balance: 1000, InitialBankroll: 1000},
		},
		games: []types.Game{{ID: "g1", HomeTeam: "Fav", AwayTeam: "Dog", SportKey: "baseball_mlb",
			Status: "scheduled", CommenceTime: time.Now().Add(5 * time.Hour)}},
		odds: map[string][]types.GameOdds{},
		pendingPreviews: []types.PreviewBet{{
			ID: "p1", GameID: "g1", ModelID: "cold-a", Selection: types.OutcomeAway,
			MarketType: types.MarketMoneyline, Bookmaker: "draftkings",
			Odds: 3.0, Stake: 20, Probability: 0.38, ExpectedValue: 0.15,
			Confidence: 0.25, Status: types.BetStatusPending,
		}},
		settledPreviews: []types.PreviewBet{
			{ID: "p2", GameID: "g0", ModelID: "cold-a", Selection: types.OutcomeHome,
				Odds: 3.0, Stake: 20, Status: types.BetStatusWon, Payout: &wonPayout, SettledAt: &settledAt},
			{ID: "p3", GameID: "g0", ModelID: "cold-b", Selection: types.OutcomeAway,
				Odds: 2.0, Stake: 15, Status: types.BetStatusLost, Payout: &lostPayout, SettledAt: &settledAt},
		},
	}
	lineup := []contenders.Contender{
		{Name: "cold-a", Portfolio: "default", Selector: testSelector("cold-a")},
		{Name: "cold-b", Portfolio: "cold-b", Selector: testSelector("cold-b")},
	}
	srv := newTestServerWithLineup(t, store, lineup)

	_, body := render(t, srv, "/")
	for _, want := range []string{
		"Warmup preview", // the section renders
		"badge-preview",  // rows carry the amber label
		"not placed",     // the label says so in words
		"25% conf",       // confidence stored at decision time shows
		"Dog @ Fav",      // the matchup renders from the game join
		"Shadow record",  // settled previews produce the per-model record
		"&#43;40.00",     // cold-a would-be P/L: won 60 on a 20 stake
		"-15.00",         // cold-b would-be P/L: lost its 15 stake
	} {
		if !strings.Contains(body, want) {
			t.Errorf("preview section missing %q", want)
		}
	}

	// Stored previews must not leak into the placeable sections.
	recStart := strings.Index(body, "Recommendations")
	prevStart := strings.Index(body, "Warmup preview")
	if recStart < 0 || prevStart < recStart {
		t.Fatal("expected the preview section after recommendations")
	}
	if !strings.Contains(body[recStart:prevStart], "No positive-EV opportunities") {
		t.Error("stored previews must not render as recommendations")
	}
}

func TestDashboardShowsAPIQuota(t *testing.T) {
	store := &fakeStore{
		odds:  map[string][]types.GameOdds{},
		quota: &types.APIQuota{RequestsRemaining: 412, RequestsUsed: 88, UpdatedAt: time.Now().Add(-2 * time.Hour)},
	}
	srv := newTestServer(t, store)
	_, body := render(t, srv, "/")
	for _, want := range []string{"Odds API credits", "412 remaining", "88 used", "2h ago"} {
		if !strings.Contains(body, want) {
			t.Errorf("quota footer missing %q", want)
		}
	}
	if strings.Contains(body, `class="bad"`) {
		t.Error("healthy quota must not render in the warning color")
	}

	store.quota.RequestsRemaining = 12
	_, body = render(t, srv, "/")
	if !strings.Contains(body, `class="bad"`) {
		t.Error("low quota must render in the warning color")
	}

	empty := newTestServer(t, &fakeStore{odds: map[string][]types.GameOdds{}})
	_, body = render(t, empty, "/")
	if strings.Contains(body, "Odds API credits") {
		t.Error("no quota row before the first API call")
	}
}

func TestDashboardAlwaysDarkAndMobileMarkup(t *testing.T) {
	srv := newTestServer(t, &fakeStore{odds: map[string][]types.GameOdds{}})
	_, body := render(t, srv, "/")

	if !strings.Contains(body, "color-scheme: dark") {
		t.Error("dashboard must declare the dark color scheme")
	}
	if strings.Contains(body, "prefers-color-scheme") {
		t.Error("dashboard must not switch themes with the OS setting")
	}
	for _, want := range []string{`data-th=`, "cell-wide", "pointermove"} {
		if !strings.Contains(body, want) {
			t.Errorf("mobile markup missing %q", want)
		}
	}
}

func TestDashboardCarriesFilterAndSortMarkup(t *testing.T) {
	store := &fakeStore{
		portfolios: map[string]*types.Portfolio{
			"default": {ID: "default", Balance: 1000, InitialBankroll: 1000},
		},
		games: []types.Game{{ID: "g3", HomeTeam: "Padres", AwayTeam: "Dodgers", SportKey: "baseball_mlb",
			Status: "scheduled", CommenceTime: time.Now().Add(5 * time.Hour)}},
		odds: map[string][]types.GameOdds{
			"g3": {{GameID: "g3", Bookmaker: "draftkings", MarketType: types.MarketMoneyline,
				HomeOdds: f64(2.4), AwayOdds: f64(2.4), RetrievedAt: time.Now()}},
		},
	}
	lineup := []contenders.Contender{
		{Name: "hist-a", Portfolio: "default", Selector: testSelector("hist-a")},
		{Name: "hist-b", Portfolio: "hist-b", Selector: testSelector("hist-b")},
	}
	srv := newTestServerWithLineup(t, store, lineup)

	_, body := render(t, srv, "/")
	for _, want := range []string{
		`id="model-filter"`,   // global filter chip row
		`data-model="hist-a"`, // chips + rows carry the model for filtering
		`data-model="hist-b"`,
		`data-model="all"`, // the All chip
		`class="top-grid"`, // leaderboard + chart share the top row
		`initTableSort`,    // sortable-header script shipped
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}

func TestBuildConsensus(t *testing.T) {
	g := types.Game{ID: "g1", HomeTeam: "H", AwayTeam: "A"}
	pick := func(model, sel string, prob, ev float64) modelPick {
		return modelPick{Model: model, Selection: sel, Prob: prob, EV: ev, Odds: 2.0, Bookmaker: "dk"}
	}

	games := map[string]gamePicks{
		"g1": {Game: g, Picks: []modelPick{ // 4/4 home, strong
			pick("a", "home", 0.60, 0.20), pick("b", "home", 0.58, 0.16),
			pick("c", "home", 0.62, 0.24), pick("d", "home", 0.55, 0.10),
		}},
		"g2": {Game: types.Game{ID: "g2"}, Picks: []modelPick{ // 3/4 home, not strong
			pick("a", "home", 0.60, 0.20), pick("b", "home", 0.58, 0.16),
			pick("c", "home", 0.62, 0.24), pick("d", "away", 0.55, 0.10),
		}},
		"g3": {Game: types.Game{ID: "g3"}, Picks: []modelPick{ // 2/4 votes, excluded
			pick("a", "home", 0.60, 0.20), pick("b", "home", 0.58, 0.16),
		}},
	}

	got := buildConsensus(games, 4, 0.05)
	if len(got) != 2 {
		t.Fatalf("consensus picks = %d, want 2", len(got))
	}
	if got[0].Game.ID != "g1" || got[0].Votes != 4 || !got[0].Strong {
		t.Errorf("g1 = %+v, want 4/4 strong", got[0])
	}
	if math.Abs(got[0].AvgProb-0.5875) > 1e-9 {
		t.Errorf("avg prob = %v, want 0.5875", got[0].AvgProb)
	}
	if got[1].Game.ID != "g2" || got[1].Votes != 3 || got[1].Strong {
		t.Errorf("g2 = %+v, want 3/4 not strong", got[1])
	}
	if got[1].Selection != "home" {
		t.Errorf("majority side = %q, want home", got[1].Selection)
	}
	if got[1].MinEV != 0.16 {
		t.Errorf("min EV = %v, want 0.16 (among agreeing picks)", got[1].MinEV)
	}
}

func TestHealthz(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	code, body := render(t, srv, "/healthz")
	if code != 200 || body != "ok" {
		t.Errorf("healthz = %d %q", code, body)
	}
}

func TestEquityChartTracksCashBalancePerContender(t *testing.T) {
	placed1 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	won1 := time.Date(2026, 7, 1, 20, 0, 0, 0, time.UTC)
	placed2 := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	lost2 := time.Date(2026, 7, 3, 20, 0, 0, 0, time.UTC)
	placed3 := time.Date(2026, 7, 3, 22, 0, 0, 0, time.UTC)
	placedRaw := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	winPayout := 120.0
	lossPayout := 0.0

	store := &fakeStore{
		portfolios: map[string]*types.Portfolio{
			"default": {ID: "default", InitialBankroll: 1000, Balance: 995},
			"raw":     {ID: "raw", InitialBankroll: 1000, Balance: 970},
		},
		settled: []types.Bet{
			{ID: "b1", PortfolioID: "default", GameID: "g1", Stake: 50, CreatedAt: placed1, ActualWin: &winPayout, SettledAt: &won1, Status: types.BetStatusWon},
			{ID: "b2", PortfolioID: "default", GameID: "g2", Stake: 30, CreatedAt: placed2, ActualWin: &lossPayout, SettledAt: &lost2, Status: types.BetStatusLost},
		},
		pending: []types.Bet{
			{ID: "b3", PortfolioID: "default", GameID: "g3", Stake: 45, CreatedAt: placed3, Status: types.BetStatusPending},
			{ID: "b4", PortfolioID: "raw", GameID: "g3", Stake: 30, CreatedAt: placedRaw, Status: types.BetStatusPending},
		},
	}
	lineup := []contenders.Contender{
		{Name: "champ", Portfolio: "default", Selector: testSelector("champ")},
		{Name: "raw", Portfolio: "raw", Selector: testSelector("raw")},
	}
	srv := newTestServerWithLineup(t, store, lineup)

	chart := srv.buildEquityChart()
	if chart == nil || len(chart.Series) != 2 {
		t.Fatalf("chart = %+v, want 2 series", chart)
	}
	champ, raw := chart.Series[0], chart.Series[1]
	if champ.Name != "champ" || champ.Slot != 1 || raw.Name != "raw" || raw.Slot != 2 {
		t.Errorf("series identity/slots wrong: %+v %+v", champ, raw)
	}

	// champ cash moves: -50, +120, -30, -45. Lost settlement adds no point.
	want := []string{"950.00", "1070.00", "1040.00", "995.00"}
	if len(champ.Points) != len(want) {
		t.Fatalf("champ points = %d, want %d", len(champ.Points), len(want))
	}
	for i, w := range want {
		if !strings.Contains(champ.Points[i].Label, w) {
			t.Errorf("champ point %d label = %q, want %s", i, champ.Points[i].Label, w)
		}
	}
	if !strings.Contains(champ.Points[0].Label, "champ") {
		t.Errorf("point label must name the model: %q", champ.Points[0].Label)
	}
	// The curve must end at each portfolio's actual cash balance
	if !strings.Contains(champ.Points[len(champ.Points)-1].Label, "995.00") {
		t.Errorf("champ final = %q, want 995", champ.Points[len(champ.Points)-1].Label)
	}
	if len(raw.Points) != 1 || !strings.Contains(raw.Points[0].Label, "970.00") {
		t.Errorf("raw points = %+v, want single 970", raw.Points)
	}
	// Points must advance left to right with time
	for i := 1; i < len(champ.Points); i++ {
		if champ.Points[i].X <= champ.Points[i-1].X {
			t.Errorf("champ point %d not right of predecessor", i)
		}
	}
	// Higher balance must be higher on screen (smaller Y)
	if champ.Points[1].Y >= champ.Points[0].Y {
		t.Error("higher balance must render with smaller Y")
	}

	if srv.buildEquityChart() == nil {
		t.Error("chart should be deterministic across calls")
	}

	empty := newTestServer(t, &fakeStore{})
	if empty.buildEquityChart() != nil {
		t.Error("no bets must mean no chart")
	}
}

func TestEquityChartSkipsContenderWithoutBetsKeepingSlots(t *testing.T) {
	placed := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{
		portfolios: map[string]*types.Portfolio{
			"quiet": {ID: "quiet", InitialBankroll: 1000, Balance: 1000},
			"busy":  {ID: "busy", InitialBankroll: 1000, Balance: 970},
		},
		pending: []types.Bet{
			{ID: "b1", PortfolioID: "busy", GameID: "g1", Stake: 30, CreatedAt: placed, Status: types.BetStatusPending},
		},
	}
	lineup := []contenders.Contender{
		{Name: "quiet", Portfolio: "quiet", Selector: testSelector("quiet")},
		{Name: "busy", Portfolio: "busy", Selector: testSelector("busy")},
	}
	srv := newTestServerWithLineup(t, store, lineup)

	chart := srv.buildEquityChart()
	if chart == nil || len(chart.Series) != 1 {
		t.Fatalf("chart = %+v, want 1 series", chart)
	}
	// Color follows the entity: busy keeps slot 2 even though quiet drew nothing
	if chart.Series[0].Name != "busy" || chart.Series[0].Slot != 2 {
		t.Errorf("series = %+v, want busy at slot 2", chart.Series[0])
	}
}

func TestDashboardDefinesNinePaletteSlots(t *testing.T) {
	srv := newTestServer(t, &fakeStore{odds: map[string][]types.GameOdds{}})
	_, body := render(t, srv, "/")

	// Slots 6-9 back the fall football contenders; values are the
	// dataviz-validated dark-surface palette — see the fall-expansion spec.
	for _, want := range []string{
		"--series-6:       #e66767",
		"--series-7:       #00a3c4",
		"--series-8:       #d55181",
		"--series-9:       #d95926",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard palette missing %q", want)
		}
	}
}
