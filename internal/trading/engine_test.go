package trading

import (
	"testing"
	"time"

	"github.com/austinbyron/betanalysis/internal/analysis"
	"github.com/austinbyron/betanalysis/internal/config"
	"github.com/austinbyron/betanalysis/pkg/types"
)

// fakeStore is an in-memory Store
type fakeStore struct {
	games      []types.Game
	odds       map[string][]types.GameOdds
	portfolios map[string]types.Portfolio
	bets       []types.Bet
	previews   []types.PreviewBet
	settled    int
	onGetOdds  func() // runs mid-cycle, to interleave concurrent work
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		odds:       make(map[string][]types.GameOdds),
		portfolios: make(map[string]types.Portfolio),
	}
}

func (f *fakeStore) GetUpcomingGames(string) ([]types.Game, error) { return f.games, nil }

func (f *fakeStore) GetGameByID(id string) (*types.Game, error) {
	for i := range f.games {
		if f.games[i].ID == id {
			return &f.games[i], nil
		}
	}
	return nil, nil
}

func (f *fakeStore) GetOddsForGame(gameID string) ([]types.GameOdds, error) {
	if f.onGetOdds != nil {
		f.onGetOdds()
	}
	return f.odds[gameID], nil
}

func (f *fakeStore) GetPortfolio(id string) (*types.Portfolio, error) {
	if p, ok := f.portfolios[id]; ok {
		return &p, nil
	}
	return nil, nil
}

func (f *fakeStore) CreatePortfolio(p types.Portfolio) error {
	f.portfolios[p.ID] = p
	return nil
}

func (f *fakeStore) ApplyPortfolioDelta(id string, d types.PortfolioDelta) error {
	p := f.portfolios[id]
	p.Balance += d.Balance
	p.BetsPlaced += d.BetsPlaced
	p.BetsWon += d.BetsWon
	p.BetsLost += d.BetsLost
	p.TotalWagered += d.TotalWagered
	p.TotalProfitLoss += d.TotalProfitLoss
	p.ActiveBetsCount += d.ActiveBetsCount
	p.ActiveBetsValue += d.ActiveBetsValue
	f.portfolios[id] = p
	return nil
}

func (f *fakeStore) PlaceBet(b types.Bet) error {
	f.bets = append(f.bets, b)
	return nil
}

func (f *fakeStore) BetExists(portfolioID, gameID string) (bool, error) {
	for _, b := range f.bets {
		if b.PortfolioID == portfolioID && b.GameID == gameID {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeStore) GetPendingBets() ([]types.Bet, error) {
	var pending []types.Bet
	for _, b := range f.bets {
		if b.Status == types.BetStatusPending {
			pending = append(pending, b)
		}
	}
	return pending, nil
}

func (f *fakeStore) SettleBet(bet types.Bet, delta types.PortfolioDelta) error {
	for i := range f.bets {
		if f.bets[i].ID == bet.ID || (f.bets[i].GameID == bet.GameID && f.bets[i].PortfolioID == bet.PortfolioID) {
			f.bets[i] = bet
		}
	}
	if err := f.ApplyPortfolioDelta(bet.PortfolioID, delta); err != nil {
		return err
	}
	f.settled++
	return nil
}

func (f *fakeStore) RecordPreviewBet(pb types.PreviewBet) error {
	for _, existing := range f.previews {
		if existing.ModelID == pb.ModelID && existing.GameID == pb.GameID {
			return nil // first write wins, like the ON CONFLICT DO NOTHING
		}
	}
	f.previews = append(f.previews, pb)
	return nil
}

func (f *fakeStore) GetPendingPreviewBets() ([]types.PreviewBet, error) {
	var pending []types.PreviewBet
	for _, pb := range f.previews {
		if pb.Status == types.BetStatusPending {
			pending = append(pending, pb)
		}
	}
	return pending, nil
}

func (f *fakeStore) SettlePreviewBet(pb types.PreviewBet) error {
	for i := range f.previews {
		if f.previews[i].ModelID == pb.ModelID && f.previews[i].GameID == pb.GameID {
			f.previews[i] = pb
		}
	}
	return nil
}

// fixedStats always reports the given record for every team
type fixedStats struct{ wins, losses int }

func (s fixedStats) TeamRecord(string, string) (int, int) { return s.wins, s.losses }

func f64(v float64) *float64 { return &v }

func tradingConfig() config.TradingConfig {
	return config.TradingConfig{
		InitialBankroll:  1000,
		MinStake:         1,
		MaxStakeFraction: 0.05,
		KellyFraction:    0.5,
		MinOdds:          1.5,
		MinExpectedValue: 0.05,
		PortfolioID:      "default",
	}
}

func newTestEngine(store Store, stats analysis.StatsProvider, cfg config.TradingConfig) *Engine {
	estimator := analysis.NewHistorical(stats)
	// marketWeight 0 keeps the test deterministic on the model probability
	selector := analysis.NewSelector(estimator, "", 0, cfg.MinOdds, cfg.MinExpectedValue)
	return NewEngine(store, selector, cfg)
}

func TestTradingCyclePlacesBetOncePerGame(t *testing.T) {
	store := newFakeStore()
	store.games = []types.Game{{ID: "g1", HomeTeam: "Home", AwayTeam: "Away", Status: "scheduled", CommenceTime: time.Now().Add(time.Hour)}}
	store.odds["g1"] = []types.GameOdds{
		{GameID: "g1", Bookmaker: "book", MarketType: types.MarketMoneyline, HomeOdds: f64(2.4), AwayOdds: f64(2.4)},
	}

	cfg := tradingConfig()
	engine := newTestEngine(store, fixedStats{0, 0}, cfg) // 0-0 → p=0.5, EV at 2.4 = 0.2

	if err := engine.RunTradingCycle("test"); err != nil {
		t.Fatalf("RunTradingCycle: %v", err)
	}

	if len(store.bets) != 1 {
		t.Fatalf("bets placed = %d, want 1", len(store.bets))
	}

	bet := store.bets[0]
	// Kelly at p=0.5, odds 2.4: edge (1.4*0.5-0.5)/1.4 ≈ 0.357 kelly → half
	// Kelly ≈ 0.179 → capped at 5% of 1000 = 50
	if bet.Stake != 50 {
		t.Errorf("stake = %v, want 50 (max fraction cap)", bet.Stake)
	}
	if bet.Probability != 0.5 {
		t.Errorf("bet probability = %v, want 0.5", bet.Probability)
	}

	portfolio := store.portfolios[cfg.PortfolioID]
	if portfolio.Balance != 950 {
		t.Errorf("balance = %v, want 950", portfolio.Balance)
	}
	if portfolio.BetsPlaced != 1 || portfolio.ActiveBetsCount != 1 {
		t.Errorf("portfolio counters wrong: %+v", portfolio)
	}

	// Second cycle must not double-bet the same game
	if err := engine.RunTradingCycle("test"); err != nil {
		t.Fatalf("second RunTradingCycle: %v", err)
	}
	if len(store.bets) != 1 {
		t.Errorf("second cycle re-bet the same game: %d bets", len(store.bets))
	}
}

func TestTradingCycleSkipsMarginalEdge(t *testing.T) {
	store := newFakeStore()
	store.games = []types.Game{{ID: "g1", HomeTeam: "Home", AwayTeam: "Away", Status: "scheduled"}}
	// EV = 0.5*2.15-1 = 0.075 clears minEV, but Kelly stake is small
	store.odds["g1"] = []types.GameOdds{
		{GameID: "g1", Bookmaker: "book", MarketType: types.MarketMoneyline, HomeOdds: f64(2.15), AwayOdds: f64(2.15)},
	}

	cfg := tradingConfig()
	cfg.MinStake = 40 // above what Kelly recommends here
	engine := newTestEngine(store, fixedStats{0, 0}, cfg)

	if err := engine.RunTradingCycle("test"); err != nil {
		t.Fatalf("RunTradingCycle: %v", err)
	}

	// The old engine bumped marginal stakes up to a default; the fix skips
	if len(store.bets) != 0 {
		t.Errorf("marginal-edge bet should be skipped, got %d bets with stake %v", len(store.bets), store.bets[0].Stake)
	}
}

func TestSettlementPaysStakePlusProfit(t *testing.T) {
	store := newFakeStore()
	homeScore, awayScore := 30, 10
	store.games = []types.Game{{
		ID: "g1", HomeTeam: "Home", AwayTeam: "Away",
		Status: "finished", HomeScore: &homeScore, AwayScore: &awayScore,
	}}
	store.portfolios["default"] = types.Portfolio{
		ID: "default", Balance: 950, TotalWagered: 50,
		ActiveBetsCount: 1, ActiveBetsValue: 50, BetsPlaced: 1,
	}
	store.bets = []types.Bet{{
		ID: "b1", PortfolioID: "default", GameID: "g1",
		Selection: types.OutcomeHome, MarketType: types.MarketMoneyline,
		Odds: 2.4, Stake: 50, PotentialWin: 70, Status: types.BetStatusPending,
	}}

	settler := NewSettler(store)
	if err := settler.SettleBets(); err != nil {
		t.Fatalf("SettleBets: %v", err)
	}

	if store.settled != 1 {
		t.Fatalf("settled = %d, want 1", store.settled)
	}

	bet := store.bets[0]
	if bet.Status != types.BetStatusWon {
		t.Errorf("bet status = %s, want won", bet.Status)
	}
	// A winning bet returns stake + profit; the old code paid profit only,
	// so winning bets netted zero.
	if bet.ActualWin == nil || *bet.ActualWin != 120 {
		t.Errorf("payout = %v, want 120 (50 stake + 70 profit)", bet.ActualWin)
	}

	portfolio := store.portfolios["default"]
	if portfolio.Balance != 1070 {
		t.Errorf("balance = %v, want 1070", portfolio.Balance)
	}
	if portfolio.TotalProfitLoss != 70 {
		t.Errorf("profit/loss = %v, want 70", portfolio.TotalProfitLoss)
	}
	if portfolio.BetsWon != 1 || portfolio.ActiveBetsCount != 0 {
		t.Errorf("portfolio counters wrong: %+v", portfolio)
	}
}

func TestTradingCycleDoesNotClobberConcurrentSettlement(t *testing.T) {
	store := newFakeStore()
	homeScore, awayScore := 3, 7
	store.games = []types.Game{
		{ID: "g1", HomeTeam: "Home", AwayTeam: "Away", Status: "scheduled", CommenceTime: time.Now().Add(time.Hour)},
		{ID: "g0", HomeTeam: "Old Home", AwayTeam: "Old Away", Status: "finished", HomeScore: &homeScore, AwayScore: &awayScore},
	}
	store.odds["g1"] = []types.GameOdds{
		{GameID: "g1", Bookmaker: "book", MarketType: types.MarketMoneyline, HomeOdds: f64(2.4), AwayOdds: f64(2.4)},
	}
	store.portfolios["default"] = types.Portfolio{
		ID: "default", Balance: 950, TotalWagered: 50,
		ActiveBetsCount: 1, ActiveBetsValue: 50, BetsPlaced: 1,
	}
	store.bets = []types.Bet{{
		ID: "b0", PortfolioID: "default", GameID: "g0",
		Selection: types.OutcomeHome, MarketType: types.MarketMoneyline,
		Odds: 2.0, Stake: 50, PotentialWin: 50, Status: types.BetStatusPending,
	}}

	// In the daemon the hourly settler and the 30-minute trading cycle fire
	// on the same tick. Settle the finished game after the cycle has read
	// the portfolio but before it persists its own changes.
	store.onGetOdds = func() {
		if err := NewSettler(store).SettleBets(); err != nil {
			t.Fatalf("SettleBets: %v", err)
		}
	}

	engine := newTestEngine(store, fixedStats{0, 0}, tradingConfig())
	if err := engine.RunTradingCycle("test"); err != nil {
		t.Fatalf("RunTradingCycle: %v", err)
	}
	if store.settled != 1 {
		t.Fatalf("settled = %d, want 1", store.settled)
	}

	// The lost settlement (pnl −50, active bet released) and the cycle's new
	// bet (stake 47.5 = 5% of 950) must both be reflected.
	p := store.portfolios["default"]
	if p.TotalProfitLoss != -50 {
		t.Errorf("profit/loss = %v, want -50 (lost settlement overwritten)", p.TotalProfitLoss)
	}
	if p.ActiveBetsValue != 47.5 {
		t.Errorf("active bets value = %v, want 47.5", p.ActiveBetsValue)
	}
	if p.ActiveBetsCount != 1 {
		t.Errorf("active bets count = %v, want 1", p.ActiveBetsCount)
	}
	if p.BetsLost != 1 {
		t.Errorf("bets lost = %v, want 1", p.BetsLost)
	}
	if p.Balance != 902.5 {
		t.Errorf("balance = %v, want 902.5", p.Balance)
	}
	if p.TotalWagered != 97.5 {
		t.Errorf("total wagered = %v, want 97.5", p.TotalWagered)
	}
}

func TestSettlementIgnoresUnfinishedGames(t *testing.T) {
	store := newFakeStore()
	store.games = []types.Game{{ID: "g1", HomeTeam: "Home", AwayTeam: "Away", Status: "scheduled"}}
	store.bets = []types.Bet{{
		ID: "b1", PortfolioID: "default", GameID: "g1",
		Selection: types.OutcomeHome, Odds: 2.0, Stake: 10, Status: types.BetStatusPending,
	}}

	settler := NewSettler(store)
	if err := settler.SettleBets(); err != nil {
		t.Fatalf("SettleBets: %v", err)
	}

	if store.settled != 0 {
		t.Errorf("unfinished game must not settle, settled = %d", store.settled)
	}
	if store.bets[0].Status != types.BetStatusPending {
		t.Errorf("bet status = %s, want pending", store.bets[0].Status)
	}
}

func TestTradingCycleCreatesPortfolioNamedForModel(t *testing.T) {
	store := newFakeStore()
	cfg := tradingConfig()
	cfg.PortfolioID = "thompson-raw"
	cfg.ModelName = "thompson-raw"

	engine := newTestEngine(store, fixedStats{0, 0}, cfg)
	if err := engine.RunTradingCycle("test"); err != nil {
		t.Fatalf("RunTradingCycle: %v", err)
	}

	p := store.portfolios["thompson-raw"]
	if p.Name != "thompson-raw" {
		t.Errorf("portfolio name = %q, want thompson-raw", p.Name)
	}
	if p.InitialBankroll != 1000 {
		t.Errorf("initial bankroll = %v, want 1000", p.InitialBankroll)
	}
}

// warmupEngine builds an engine whose estimator is confidence-gated: with
// 0-0 records the blend collapses to the market and no bet clears, but the
// full-confidence preview still exists.
func warmupEngine(t *testing.T, store Store) *Engine {
	t.Helper()
	est, err := analysis.NewEstimator(config.AnalysisConfig{ModelType: "historical", WarmupGames: 20}, fixedStats{}, nil)
	if err != nil {
		t.Fatalf("NewEstimator: %v", err)
	}
	selector := analysis.NewSelector(est, "cold", 0.7, 1.5, 0.05)
	return NewEngine(store, selector, tradingConfig())
}

func TestTradingCycleRecordsPreviewWhenWarmupGates(t *testing.T) {
	store := newFakeStore()
	store.games = []types.Game{{ID: "g1", HomeTeam: "Fav", AwayTeam: "Dog", Status: "scheduled"}}
	// Devigged 66.7%/33.3%: the 50/50 cold model at full weight inflates
	// the dog into a +EV pick, but the gated blend sees no edge.
	store.odds["g1"] = []types.GameOdds{{GameID: "g1", Bookmaker: "book", MarketType: types.MarketMoneyline,
		HomeOdds: f64(1.5), AwayOdds: f64(3.0)}}

	engine := warmupEngine(t, store)
	if err := engine.RunTradingCycle("nfl"); err != nil {
		t.Fatalf("RunTradingCycle: %v", err)
	}

	if len(store.bets) != 0 {
		t.Fatalf("warmup gate must place no bets, got %d", len(store.bets))
	}
	if len(store.previews) != 1 {
		t.Fatalf("previews = %d, want 1", len(store.previews))
	}
	pb := store.previews[0]
	if pb.ModelID != "cold" || pb.Selection != types.OutcomeAway || pb.Status != types.BetStatusPending {
		t.Errorf("preview = %+v, want pending cold/away", pb)
	}
	if pb.Stake < 1 {
		t.Errorf("preview stake = %v, must clear the minimum like a real bet", pb.Stake)
	}
	if pb.Confidence != 0 {
		t.Errorf("confidence = %v, want 0 for unseen teams", pb.Confidence)
	}

	// Cycles repeat every 30 minutes; the shadow record must not churn.
	if err := engine.RunTradingCycle("nfl"); err != nil {
		t.Fatalf("second cycle: %v", err)
	}
	if len(store.previews) != 1 {
		t.Errorf("previews after second cycle = %d, want 1 (first write wins)", len(store.previews))
	}

	// The portfolio must be untouched: previews move no money.
	p, _ := store.GetPortfolio("default")
	if p.Balance != 1000 || p.BetsPlaced != 0 {
		t.Errorf("portfolio touched by preview: %+v", p)
	}
}

func TestSettlerSettlesPreviewsWithoutTouchingPortfolio(t *testing.T) {
	home5, away3 := 5, 3
	store := newFakeStore()
	store.portfolios["default"] = types.Portfolio{ID: "default", Balance: 1000}
	store.games = []types.Game{
		{ID: "g1", HomeTeam: "Fav", AwayTeam: "Dog", Status: "finished", HomeScore: &home5, AwayScore: &away3},
		{ID: "g2", HomeTeam: "A", AwayTeam: "B", Status: "scheduled"},
	}
	store.previews = []types.PreviewBet{
		{ID: "p1", GameID: "g1", ModelID: "cold", Selection: types.OutcomeHome, Odds: 1.5, Stake: 20, Status: types.BetStatusPending},
		{ID: "p2", GameID: "g1", ModelID: "colder", Selection: types.OutcomeAway, Odds: 3.0, Stake: 20, Status: types.BetStatusPending},
		{ID: "p3", GameID: "g2", ModelID: "cold", Selection: types.OutcomeHome, Odds: 2.0, Stake: 20, Status: types.BetStatusPending},
	}

	if err := NewSettler(store).SettleBets(); err != nil {
		t.Fatalf("SettleBets: %v", err)
	}

	won := store.previews[0]
	if won.Status != types.BetStatusWon || won.Payout == nil || *won.Payout != 30 {
		t.Errorf("home preview = %+v, want won with 30 payout (stake x odds)", won)
	}
	lost := store.previews[1]
	if lost.Status != types.BetStatusLost || lost.Payout == nil || *lost.Payout != 0 {
		t.Errorf("away preview = %+v, want lost with 0 payout", lost)
	}
	if store.previews[2].Status != types.BetStatusPending {
		t.Errorf("unfinished game's preview must stay pending, got %+v", store.previews[2])
	}

	p, _ := store.GetPortfolio("default")
	if p.Balance != 1000 || p.BetsWon != 0 || p.BetsLost != 0 {
		t.Errorf("portfolio touched by preview settlement: %+v", p)
	}
}
