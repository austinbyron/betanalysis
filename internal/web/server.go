package web

import (
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/austinbyron/betanalysis/internal/analysis"
	"github.com/austinbyron/betanalysis/internal/config"
	"github.com/austinbyron/betanalysis/internal/contenders"
	"github.com/austinbyron/betanalysis/pkg/types"
	"github.com/rs/zerolog/log"
)

//go:embed templates/*.html
var templateFS embed.FS

// Store is the storage surface the dashboard needs
type Store interface {
	GetPortfolio(id string) (*types.Portfolio, error)
	GetPendingBets() ([]types.Bet, error)
	GetSettledBets() ([]types.Bet, error)
	GetUpcomingGames(sportKey string) ([]types.Game, error)
	GetOddsForGame(gameID string) ([]types.GameOdds, error)
	GetGameByID(id string) (*types.Game, error)
	BetExists(portfolioID, gameID string) (bool, error)
	GetPendingPreviewBets() ([]types.PreviewBet, error)
	GetSettledPreviewBets() ([]types.PreviewBet, error)
	GetAPIQuota() (*types.APIQuota, error)
}

// GameLinker resolves a game to an external detail page (ESPN gamecast).
// Implementations are best-effort; "" means no link.
type GameLinker interface {
	GameURL(game types.Game) string
}

// Server renders the paper trading dashboard
type Server struct {
	store   Store
	lineup  []contenders.Contender
	linker  GameLinker // may be nil
	cfg     *config.Config
	tmpl    *template.Template
	httpSrv *http.Server
}

// NewServer creates the dashboard server for a model-race lineup. linker
// may be nil to render without game links.
func NewServer(store Store, lineup []contenders.Contender, cfg *config.Config, linker GameLinker) (*Server, error) {
	funcs := template.FuncMap{
		"inc":    func(i int) int { return i + 1 },
		"money":  func(v float64) string { return fmt.Sprintf("$%.2f", v) },
		"signed": func(v float64) string { return fmt.Sprintf("%+.2f", v) },
		"pct":    func(v float64) string { return fmt.Sprintf("%.1f%%", v) },
		"odds":   func(v float64) string { return fmt.Sprintf("%.2f", v) },
		"prob":   func(v float64) string { return fmt.Sprintf("%.0f%%", v*100) },
		"title": func(s string) string {
			words := strings.Fields(strings.ReplaceAll(s, "_", " "))
			for i, w := range words {
				words[i] = strings.ToUpper(w[:1]) + w[1:]
			}
			return strings.Join(words, " ")
		},
	}

	tmpl, err := template.New("dashboard.html").Funcs(funcs).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("failed to parse templates: %w", err)
	}

	s := &Server{
		store:  store,
		lineup: lineup,
		linker: linker,
		cfg:    cfg,
		tmpl:   tmpl,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleDashboard)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	s.httpSrv = &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	return s, nil
}

// Start blocks serving HTTP until the server is closed
func (s *Server) Start() error {
	log.Info().Str("addr", s.httpSrv.Addr).Msg("Dashboard server started")
	err := s.httpSrv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Close shuts the server down
func (s *Server) Close() error {
	return s.httpSrv.Close()
}

// Handler exposes the mux for tests
func (s *Server) Handler() http.Handler {
	return s.httpSrv.Handler
}

type recommendation struct {
	Model      string
	Game       types.Game
	GameURL    string
	Bet        types.Bet
	Stake      float64
	AlreadyBet bool
	OddsAge    string
}

// previewRow is one persisted warmup-suppressed pick, joined to its game
type previewRow struct {
	P       types.PreviewBet
	Game    *types.Game
	GameURL string
	Matchup string
}

// Pick names the selected team when the game is known
func (r previewRow) Pick() string {
	if r.Game == nil {
		return strings.ToUpper(r.P.Selection[:1]) + r.P.Selection[1:]
	}
	if r.P.Selection == types.OutcomeHome {
		return r.Game.HomeTeam
	}
	return r.Game.AwayTeam
}

// shadowRow is one model's settled-preview track record — how the model
// would have done had the warmup gate not held it back
type shadowRow struct {
	Model          string
	Won            int
	Lost           int
	Pending        int
	Profit         float64 // would-be P/L across settled previews
	ProfitPositive bool
}

// gameURL resolves a game's external link, tolerating a nil linker
func (s *Server) gameURL(game types.Game) string {
	if s.linker == nil {
		return ""
	}
	return s.linker.GameURL(game)
}

// modelPick is one contender's opinion on one game, input to consensus
type modelPick struct {
	Model     string
	Selection string
	Prob      float64
	EV        float64
	Odds      float64
	Bookmaker string
}

// gamePicks collects every contender's pick for one game
type gamePicks struct {
	Game  types.Game
	Picks []modelPick
}

// consensusPick is a game where most of the lineup backs the same side —
// the informational shortlist for occasional real-money bets. The paper
// engines bet independently; this is a lens, not a fifth bettor.
type consensusPick struct {
	Game      types.Game
	GameURL   string
	Selection string
	Votes     int
	Total     int     // lineup size
	AvgProb   float64 // mean probability among agreeing picks
	MinEV     float64 // worst EV among agreeing picks
	BestOdds  float64 // best price among agreeing picks
	BestBook  string
	Picks     []modelPick // the agreeing picks
	Strong    bool        // every contender agrees and MinEV clears the threshold
}

// buildConsensus filters games where at least 3 contenders back the same
// side. Sorted by votes then average probability, both descending.
func buildConsensus(games map[string]gamePicks, total int, minEV float64) []consensusPick {
	var out []consensusPick
	for _, gp := range games {
		bySide := make(map[string][]modelPick)
		for _, p := range gp.Picks {
			bySide[p.Selection] = append(bySide[p.Selection], p)
		}

		var side string
		var agreeing []modelPick
		for sel, ps := range bySide {
			if len(ps) > len(agreeing) {
				side, agreeing = sel, ps
			}
		}
		if len(agreeing) < 3 {
			continue
		}

		cp := consensusPick{
			Game:      gp.Game,
			Selection: side,
			Votes:     len(agreeing),
			Total:     total,
			MinEV:     agreeing[0].EV,
			Picks:     agreeing,
		}
		var probSum float64
		for _, p := range agreeing {
			probSum += p.Prob
			if p.EV < cp.MinEV {
				cp.MinEV = p.EV
			}
			if p.Odds > cp.BestOdds {
				cp.BestOdds = p.Odds
				cp.BestBook = p.Bookmaker
			}
		}
		cp.AvgProb = probSum / float64(len(agreeing))
		cp.Strong = cp.Votes == total && cp.MinEV >= minEV
		out = append(out, cp)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Votes != out[j].Votes {
			return out[i].Votes > out[j].Votes
		}
		return out[i].AvgProb > out[j].AvgProb
	})
	return out
}

type betRow struct {
	Bet     types.Bet
	Game    *types.Game
	GameURL string
	Matchup string
	Won     bool
}

type equityPoint struct {
	X, Y  float64
	Label string
}

type gridLine struct {
	Y     float64
	Label string
}

// equitySeries is one contender's cash-balance line. Slot is the 1-based
// palette slot, fixed by lineup order — color follows the entity even when
// other contenders have no series yet.
type equitySeries struct {
	Name   string
	Slot   int
	Path   string
	Points []equityPoint
	EndX   float64
	EndY   float64
}

type equityChart struct {
	Series    []equitySeries
	Grid      []gridLine
	BaselineY float64
	Width     float64
	Height    float64
}

// leaderRow is one contender's standing on the leaderboard
type leaderRow struct {
	Name           string
	Portfolio      *types.Portfolio
	ProfitPositive bool
}

type dashboardData struct {
	Models          []string // lineup names in slot order, for the filter chips
	Leaderboard     []leaderRow
	Consensus       []consensusPick
	Recommendations []recommendation
	Previews        []previewRow // warmup-suppressed picks, never placed
	Shadow          []shadowRow  // settled-preview records per model
	ActiveBets      []betRow
	SettledBets     []betRow
	Equity          *equityChart
	Quota           *types.APIQuota
	QuotaAge        string
	QuotaLow        bool
	ModelName       string
	Sports          []string
	GeneratedAt     time.Time
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	data := s.buildDashboard()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "dashboard.html", data); err != nil {
		log.Error().Err(err).Msg("Failed to render dashboard")
	}
}

func (s *Server) buildDashboard() dashboardData {
	names := make([]string, len(s.lineup))
	for i, c := range s.lineup {
		names[i] = c.Name
	}
	data := dashboardData{
		Models:      names,
		ModelName:   strings.Join(names, ", "),
		Sports:      s.cfg.Sports(),
		GeneratedAt: time.Now(),
	}

	for _, c := range s.lineup {
		portfolio, err := s.store.GetPortfolio(c.Portfolio)
		if err != nil {
			log.Error().Err(err).Str("model", c.Name).Msg("dashboard: portfolio lookup failed")
			continue
		}
		if portfolio == nil {
			continue // created on the contender's first trading cycle
		}
		data.Leaderboard = append(data.Leaderboard, leaderRow{
			Name:           c.Name,
			Portfolio:      portfolio,
			ProfitPositive: portfolio.TotalProfitLoss >= 0,
		})
	}
	sort.Slice(data.Leaderboard, func(i, j int) bool {
		return data.Leaderboard[i].Portfolio.TotalProfitLoss > data.Leaderboard[j].Portfolio.TotalProfitLoss
	})

	portfolios := make(map[string]*types.Portfolio, len(data.Leaderboard))
	for _, row := range data.Leaderboard {
		portfolios[row.Name] = row.Portfolio
	}

	var picks map[string]gamePicks
	data.Recommendations, picks = s.buildRecommendations(portfolios)
	// Consensus needs a real panel — with fewer than 3 contenders the
	// section stays hidden.
	if len(s.lineup) >= 3 {
		data.Consensus = buildConsensus(picks, len(s.lineup), s.cfg.Trading.MinExpectedValue)
		for i := range data.Consensus {
			data.Consensus[i].GameURL = s.gameURL(data.Consensus[i].Game)
		}
	}
	data.Previews, data.Shadow = s.buildPreviews()
	data.ActiveBets, data.SettledBets = s.buildBetRows()
	data.Equity = s.buildEquityChart()

	if quota, err := s.store.GetAPIQuota(); err != nil {
		log.Error().Err(err).Msg("dashboard: api quota lookup failed")
	} else if quota != nil {
		data.Quota = quota
		data.QuotaAge = formatAge(time.Since(quota.UpdatedAt))
		data.QuotaLow = quota.RequestsRemaining < 50
	}

	return data
}

// buildPreviews reads the persisted warmup previews: pending picks for the
// preview table plus each model's settled shadow record. Previews are
// recorded by the trading cycle at decision time — unlike the
// recommendations table they don't re-roll stochastic models per page load,
// which is what makes the shadow record scoreable.
func (s *Server) buildPreviews() ([]previewRow, []shadowRow) {
	pending, err := s.store.GetPendingPreviewBets()
	if err != nil {
		log.Error().Err(err).Msg("dashboard: pending preview bets failed")
	}
	settled, err := s.store.GetSettledPreviewBets()
	if err != nil {
		log.Error().Err(err).Msg("dashboard: settled preview bets failed")
	}

	rows := make([]previewRow, 0, len(pending))
	for _, pb := range pending {
		row := previewRow{P: pb, Matchup: pb.GameID}
		if game, err := s.store.GetGameByID(pb.GameID); err == nil && game != nil {
			row.Game = game
			row.GameURL = s.gameURL(*game)
			row.Matchup = fmt.Sprintf("%s @ %s", game.AwayTeam, game.HomeTeam)
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].P.ExpectedValue > rows[j].P.ExpectedValue
	})

	byModel := make(map[string]*shadowRow)
	for _, pb := range settled {
		row := byModel[pb.ModelID]
		if row == nil {
			row = &shadowRow{Model: pb.ModelID}
			byModel[pb.ModelID] = row
		}
		if pb.Status == types.BetStatusWon {
			row.Won++
		} else {
			row.Lost++
		}
		row.Profit += pb.Profit()
	}
	for _, pb := range pending {
		if row := byModel[pb.ModelID]; row != nil {
			row.Pending++
		} else {
			byModel[pb.ModelID] = &shadowRow{Model: pb.ModelID, Pending: 1}
		}
	}

	// Lineup order keeps shadow rows aligned with the palette slots
	shadow := make([]shadowRow, 0, len(byModel))
	for _, c := range s.lineup {
		if row, ok := byModel[c.Name]; ok {
			row.ProfitPositive = row.Profit >= 0
			shadow = append(shadow, *row)
			delete(byModel, c.Name)
		}
	}
	for _, row := range byModel { // retired models keep their history
		row.ProfitPositive = row.Profit >= 0
		shadow = append(shadow, *row)
	}

	return rows, shadow
}

// buildRecommendations runs every contender read-only over upcoming games —
// what each model would bet right now, without placing anything. Odds are
// fetched once per game and shared across the lineup. The returned picks
// (per game, per model) feed the consensus section. Warmup-suppressed picks
// deliberately stay out of both: the preview section reads the persisted
// rows the trading cycle recorded, and the real-money consensus shortlist
// only ever reflects models trusted enough to bet.
func (s *Server) buildRecommendations(portfolios map[string]*types.Portfolio) ([]recommendation, map[string]gamePicks) {
	const maxGamesPerSport = 25
	var recs []recommendation
	picks := make(map[string]gamePicks)

	for _, sport := range s.cfg.Sports() {
		games, err := s.store.GetUpcomingGames(sport)
		if err != nil {
			log.Error().Err(err).Str("sport", sport).Msg("dashboard: upcoming games failed")
			continue
		}
		if len(games) > maxGamesPerSport {
			games = games[:maxGamesPerSport]
		}

		for _, game := range games {
			odds, err := s.store.GetOddsForGame(game.ID)
			if err != nil || len(odds) == 0 {
				continue
			}

			var oddsAge string
			var newest time.Time
			for _, o := range odds {
				if o.RetrievedAt.After(newest) {
					newest = o.RetrievedAt
				}
			}
			if !newest.IsZero() {
				oddsAge = formatAge(time.Since(newest))
			}

			gameURL := s.gameURL(game)

			for _, c := range s.lineup {
				if !c.CoversSport(sport) {
					continue
				}

				bet := c.Selector.RecommendBet(game, odds)
				if bet == nil {
					continue
				}

				bankroll := s.cfg.Trading.InitialBankroll
				if p := portfolios[c.Name]; p != nil {
					bankroll = p.Balance
				}
				stake := analysis.KellyStake(bet.Probability, bet.Odds, bankroll,
					s.cfg.Trading.KellyFraction, s.cfg.Trading.MaxStakeFraction)
				if stake < s.cfg.Trading.MinStake {
					continue
				}

				alreadyBet, _ := s.store.BetExists(c.Portfolio, game.ID)

				recs = append(recs, recommendation{
					Model:      c.Name,
					Game:       game,
					GameURL:    gameURL,
					Bet:        *bet,
					Stake:      stake,
					AlreadyBet: alreadyBet,
					OddsAge:    oddsAge,
				})

				gp := picks[game.ID]
				gp.Game = game
				gp.Picks = append(gp.Picks, modelPick{
					Model:     c.Name,
					Selection: bet.Selection,
					Prob:      bet.Probability,
					EV:        bet.ExpectedValue,
					Odds:      bet.Odds,
					Bookmaker: bet.Bookmaker,
				})
				picks[game.ID] = gp
			}
		}
	}

	sort.Slice(recs, func(i, j int) bool {
		return recs[i].Bet.ExpectedValue > recs[j].Bet.ExpectedValue
	})

	return recs, picks
}

func (s *Server) buildBetRows() (active, settled []betRow) {
	pending, err := s.store.GetPendingBets()
	if err != nil {
		log.Error().Err(err).Msg("dashboard: pending bets failed")
	}
	for _, bet := range pending {
		active = append(active, s.toBetRow(bet))
	}

	all, err := s.store.GetSettledBets()
	if err != nil {
		log.Error().Err(err).Msg("dashboard: settled bets failed")
	}
	// Most recent 20 for the table
	for i := len(all) - 1; i >= 0 && len(settled) < 20; i-- {
		settled = append(settled, s.toBetRow(all[i]))
	}

	return active, settled
}

func (s *Server) toBetRow(bet types.Bet) betRow {
	row := betRow{Bet: bet, Won: bet.Status == types.BetStatusWon}
	game, err := s.store.GetGameByID(bet.GameID)
	if err == nil && game != nil {
		row.Game = game
		row.GameURL = s.gameURL(*game)
		row.Matchup = fmt.Sprintf("%s @ %s", game.AwayTeam, game.HomeTeam)
	} else {
		row.Matchup = bet.GameID
	}
	return row
}

// buildEquityChart renders one cash-balance series per contender as SVG
// geometry. Each series tracks cash: down by the stake when a bet is
// placed, up by the payout when one settles, so every curve ends at its
// portfolio's current balance. Lost settlements move no cash and add no
// point. A contender with no bets draws no series but keeps its palette
// slot — color follows the entity.
func (s *Server) buildEquityChart() *equityChart {
	settled, err := s.store.GetSettledBets()
	if err != nil {
		return nil
	}
	pending, err := s.store.GetPendingBets()
	if err != nil {
		return nil
	}

	const width, height = 800.0, 240.0
	const padLeft, padRight, padTop, padBottom = 56.0, 112.0, 14.0, 26.0

	byPortfolio := make(map[string][]types.Bet)
	for _, bet := range append(append([]types.Bet{}, settled...), pending...) {
		byPortfolio[bet.PortfolioID] = append(byPortfolio[bet.PortfolioID], bet)
	}

	type event struct {
		at    time.Time
		delta float64
	}
	type sample struct {
		at    time.Time
		value float64
	}
	type seriesData struct {
		name    string
		slot    int
		samples []sample
	}

	// Build every series' samples first: axis scales span all of them.
	var all []seriesData
	var minY, maxY float64
	var startT, endT time.Time
	first := true

	extend := func(v float64) {
		if v < minY {
			minY = v
		}
		if v > maxY {
			maxY = v
		}
	}

	baseline := s.cfg.Trading.InitialBankroll
	for i, c := range s.lineup {
		portfolio, err := s.store.GetPortfolio(c.Portfolio)
		if err != nil || portfolio == nil {
			continue
		}
		if i == 0 {
			baseline = portfolio.InitialBankroll
		}

		events := []event{}
		for _, bet := range byPortfolio[c.Portfolio] {
			if !bet.CreatedAt.IsZero() {
				events = append(events, event{at: bet.CreatedAt, delta: -bet.Stake})
			}
			if bet.SettledAt != nil && bet.ActualWin != nil && *bet.ActualWin > 0 {
				events = append(events, event{at: *bet.SettledAt, delta: *bet.ActualWin})
			}
		}
		if len(events) == 0 {
			continue
		}
		sort.Slice(events, func(a, b int) bool { return events[a].at.Before(events[b].at) })

		bankroll := portfolio.InitialBankroll
		samples := make([]sample, 0, len(events))
		for _, ev := range events {
			bankroll += ev.delta
			samples = append(samples, sample{at: ev.at, value: bankroll})
		}

		if first {
			minY, maxY = portfolio.InitialBankroll, portfolio.InitialBankroll
			startT, endT = samples[0].at, samples[len(samples)-1].at
			first = false
		}
		extend(portfolio.InitialBankroll)
		for _, p := range samples {
			extend(p.value)
		}
		if samples[0].at.Before(startT) {
			startT = samples[0].at
		}
		if samples[len(samples)-1].at.After(endT) {
			endT = samples[len(samples)-1].at
		}

		all = append(all, seriesData{name: c.Name, slot: i + 1, samples: samples})
	}
	if len(all) == 0 {
		return nil
	}

	span := maxY - minY
	if span < 1 {
		span = 1
	}
	minY -= span * 0.1
	maxY += span * 0.1

	timeSpan := endT.Sub(startT).Seconds()
	if timeSpan < 1 {
		timeSpan = 1
	}

	plotW := width - padLeft - padRight
	plotH := height - padTop - padBottom

	toX := func(t time.Time) float64 {
		return padLeft + (t.Sub(startT).Seconds()/timeSpan)*plotW
	}
	toY := func(v float64) float64 {
		return padTop + (1-(v-minY)/(maxY-minY))*plotH
	}

	series := make([]equitySeries, 0, len(all))
	for _, sd := range all {
		var path strings.Builder
		points := make([]equityPoint, 0, len(sd.samples))
		for i, p := range sd.samples {
			x, y := toX(p.at), toY(p.value)
			if i == 0 {
				fmt.Fprintf(&path, "M%.1f %.1f", x, y)
			} else {
				fmt.Fprintf(&path, " L%.1f %.1f", x, y)
			}
			points = append(points, equityPoint{
				X:     x,
				Y:     y,
				Label: fmt.Sprintf("%s — %s — $%.2f", sd.name, p.at.Format("Jan 2 15:04"), p.value),
			})
		}
		last := points[len(points)-1]
		series = append(series, equitySeries{
			Name:   sd.name,
			Slot:   sd.slot,
			Path:   path.String(),
			Points: points,
			// Labels live in the right gutter so long names never clip
			EndX: width - padRight + 4,
			EndY: last.Y,
		})
	}

	// Stagger end labels: names anchored to line ends must not overlap.
	order := make([]int, len(series))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return series[order[a]].EndY < series[order[b]].EndY })
	const labelGap = 14.0
	prev := -labelGap
	for _, idx := range order {
		if series[idx].EndY < prev+labelGap {
			series[idx].EndY = prev + labelGap
		}
		prev = series[idx].EndY
	}

	grid := make([]gridLine, 0, 4)
	for i := 0; i <= 3; i++ {
		v := minY + (maxY-minY)*float64(i)/3
		grid = append(grid, gridLine{Y: toY(v), Label: fmt.Sprintf("$%.0f", v)})
	}

	return &equityChart{
		Series:    series,
		Grid:      grid,
		BaselineY: toY(baseline),
		Width:     width,
		Height:    height,
	}
}

func formatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
