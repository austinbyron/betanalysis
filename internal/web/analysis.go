package web

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/austinbyron/betanalysis/internal/analysis"
	"github.com/austinbyron/betanalysis/pkg/types"
	"github.com/rs/zerolog/log"
)

// stripW is the drawable width of the 0–100% probability strips
const stripW = 640.0

// modelDot is one contender's home-win estimate positioned on a strip
type modelDot struct {
	Model string
	Slot  int // palette slot, lineup index + 1
	Prob  float64
	X     float64
}

// matchupRow is one upcoming game's model-vs-market strip
type matchupRow struct {
	Game           types.Game
	Sport          string
	MarketProb     float64 // de-vigged home probability, median across books
	MarketX        float64
	Dots           []modelDot
	SpanX1, SpanX2 float64 // min..max model estimate, scaled
	Gap            float64 // |mean model estimate − market|, the sort key
}

// teamStrengthRow is one team's Beta posterior summarized for the forest plot
type teamStrengthRow struct {
	Team            string
	Wins, Losses    int // priors-layered — exactly what the models see
	Mean, Lo, Hi    float64
	MeanX, LoX, HiX float64
}

// sportStrength groups the forest plot rows per sport
type sportStrength struct {
	Sport string
	Teams []teamStrengthRow
}

// eloBarRow is one team's current Elo standing plus recent movement
type eloBarRow struct {
	Team     string
	Rating   float64
	Delta14  float64 // rating change over the trailing 14 days
	BarX     float64 // scaled bar end for the rating
	Positive bool    // Delta14 >= 0
}

// sportElo groups the Elo landscape per sport
type sportElo struct {
	Sport      string
	Rows       []eloBarRow
	MinR, MaxR float64
}

type analysisData struct {
	Models      []string
	Matchups    []matchupRow
	Teams       []sportStrength
	Elo         []sportElo
	Diags       []modelDiag
	GeneratedAt time.Time
}

// calibW is the side of the square calibration chart
const calibW = 200.0

// calibDotView is one calibration bin scaled into chart coordinates
type calibDotView struct {
	X, Y  float64
	N     int
	Label string
}

// modelDiag is one contender's trust profile: calibration plus the odds
// and EV ranges it actually bets.
type modelDiag struct {
	Model  string
	Slot   int
	N      int
	Calib  []calibDotView
	OddsXs []float64
	EVXs   []float64
}

func (s *Server) handleAnalysis(w http.ResponseWriter, r *http.Request) {
	data := s.buildAnalysis()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "analysis.html", data); err != nil {
		log.Error().Err(err).Msg("Failed to render analysis page")
	}
}

func (s *Server) buildAnalysis() analysisData {
	now := time.Now()
	names := make([]string, len(s.lineup))
	for i, c := range s.lineup {
		names[i] = c.Name
	}
	return analysisData{
		Models:      names,
		Matchups:    s.buildMatchups(),
		Teams:       s.buildTeamStrength(),
		Elo:         s.buildEloLandscape(now),
		Diags:       s.buildDiagnostics(),
		GeneratedAt: now,
	}
}

// buildMatchups positions every contender's deterministic home-win
// estimate against the market for each upcoming game. Sorted by the
// model-vs-market gap so perceived edges float to the top.
func (s *Server) buildMatchups() []matchupRow {
	const maxGamesPerSport = 25
	var rows []matchupRow

	for _, sport := range s.cfg.Sports() {
		games, err := s.store.GetUpcomingGames(sport)
		if err != nil {
			log.Error().Err(err).Str("sport", sport).Msg("analysis: upcoming games failed")
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
			market, ok := medianMarketProb(odds)
			if !ok {
				continue
			}

			row := matchupRow{
				Game:       game,
				Sport:      sport,
				MarketProb: market,
				MarketX:    linScale(market, 0, 1, 0, stripW),
			}
			var sum float64
			minP, maxP := 1.0, 0.0
			for i, c := range s.lineup {
				if !c.CoversSport(sport) {
					continue
				}
				v := c.Selector.View(game)
				row.Dots = append(row.Dots, modelDot{
					Model: c.Name,
					Slot:  i + 1,
					Prob:  v.AdjHome,
					X:     linScale(v.AdjHome, 0, 1, 0, stripW),
				})
				sum += v.AdjHome
				minP = math.Min(minP, v.AdjHome)
				maxP = math.Max(maxP, v.AdjHome)
			}
			if len(row.Dots) == 0 {
				continue
			}
			row.SpanX1 = linScale(minP, 0, 1, 0, stripW)
			row.SpanX2 = linScale(maxP, 0, 1, 0, stripW)
			row.Gap = math.Abs(sum/float64(len(row.Dots)) - market)
			rows = append(rows, row)
		}
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Gap != rows[j].Gap {
			return rows[i].Gap > rows[j].Gap
		}
		return rows[i].Game.ID < rows[j].Game.ID
	})
	return rows
}

// medianMarketProb de-vigs every moneyline book and returns the median
// home probability — one robust market number per game.
func medianMarketProb(odds []types.GameOdds) (float64, bool) {
	var probs []float64
	for _, o := range odds {
		if o.MarketType != types.MarketMoneyline || o.HomeOdds == nil || o.AwayOdds == nil {
			continue
		}
		h, _ := analysis.Devig(*o.HomeOdds, *o.AwayOdds)
		probs = append(probs, h)
	}
	if len(probs) == 0 {
		return 0, false
	}
	sort.Float64s(probs)
	mid := len(probs) / 2
	if len(probs)%2 == 1 {
		return probs[mid], true
	}
	return (probs[mid-1] + probs[mid]) / 2, true
}

// buildTeamStrength summarizes every team's Beta posterior: the arms
// Thompson samples from. Wide interval = thin record = exploration zone.
func (s *Server) buildTeamStrength() []sportStrength {
	if s.stats == nil {
		return nil
	}
	alpha, beta := s.cfg.Analysis.ThompsonAlphaPrior, s.cfg.Analysis.ThompsonBetaPrior
	if alpha <= 0 {
		alpha = 1
	}
	if beta <= 0 {
		beta = 1
	}

	var out []sportStrength
	for _, sport := range s.cfg.Sports() {
		teams, err := s.store.GetAllTeamStats(sport)
		if err != nil {
			log.Error().Err(err).Str("sport", sport).Msg("analysis: team stats failed")
			continue
		}
		ss := sportStrength{Sport: sport}
		for _, t := range teams {
			w, l := s.stats.TeamRecord(t.TeamName, sport)
			mean, lo, hi := betaSummary(float64(w)+alpha, float64(l)+beta)
			ss.Teams = append(ss.Teams, teamStrengthRow{
				Team: t.TeamName, Wins: w, Losses: l,
				Mean: mean, Lo: lo, Hi: hi,
				MeanX: linScale(mean, 0, 1, 0, stripW),
				LoX:   linScale(lo, 0, 1, 0, stripW),
				HiX:   linScale(hi, 0, 1, 0, stripW),
			})
		}
		if len(ss.Teams) == 0 {
			continue
		}
		sort.Slice(ss.Teams, func(i, j int) bool {
			if ss.Teams[i].Mean != ss.Teams[j].Mean {
				return ss.Teams[i].Mean > ss.Teams[j].Mean
			}
			return ss.Teams[i].Team < ss.Teams[j].Team
		})
		out = append(out, ss)
	}
	return out
}

// buildEloLandscape replays each sport's history into current ratings
// ranked descending, with the trailing-14-day movement.
func (s *Server) buildEloLandscape(now time.Time) []sportElo {
	cutoff := now.Add(-14 * 24 * time.Hour)
	var out []sportElo

	for _, sport := range s.cfg.Sports() {
		games, err := s.store.FinishedGames(sport)
		if err != nil {
			log.Error().Err(err).Str("sport", sport).Msg("analysis: finished games failed")
			continue
		}
		hist := analysis.EloHistory(games)
		if len(hist) == 0 {
			continue
		}

		se := sportElo{Sport: sport, MinR: analysis.EloInitial, MaxR: analysis.EloInitial}
		for team, points := range hist {
			current := points[len(points)-1].Rating
			baseline := analysis.EloInitial
			for _, p := range points {
				if p.At.After(cutoff) {
					break
				}
				baseline = p.Rating
			}
			se.Rows = append(se.Rows, eloBarRow{
				Team: team, Rating: current,
				Delta14:  current - baseline,
				Positive: current-baseline >= 0,
			})
			se.MinR = math.Min(se.MinR, current)
			se.MaxR = math.Max(se.MaxR, current)
		}
		sort.Slice(se.Rows, func(i, j int) bool {
			if se.Rows[i].Rating != se.Rows[j].Rating {
				return se.Rows[i].Rating > se.Rows[j].Rating
			}
			return se.Rows[i].Team < se.Rows[j].Team
		})

		// Pad the domain so the weakest team still draws a visible bar
		span := se.MaxR - se.MinR
		if span < 1 {
			span = 1
		}
		lo := se.MinR - span*0.1
		for i := range se.Rows {
			se.Rows[i].BarX = linScale(se.Rows[i].Rating, lo, se.MaxR, 0, stripW)
		}
		out = append(out, se)
	}
	return out
}

// buildDiagnostics scores each contender's recorded probabilities against
// outcomes. Settled shadow previews count too — they were recorded at
// decision time, so they're honest samples of the model's calibration.
func (s *Server) buildDiagnostics() []modelDiag {
	settled, err := s.store.GetSettledBets()
	if err != nil {
		log.Error().Err(err).Msg("analysis: settled bets failed")
	}
	previews, err := s.store.GetSettledPreviewBets()
	if err != nil {
		log.Error().Err(err).Msg("analysis: settled previews failed")
	}
	pending, err := s.store.GetPendingBets()
	if err != nil {
		log.Error().Err(err).Msg("analysis: pending bets failed")
	}

	type sample struct {
		prob, odds, ev float64
		won, settledOK bool
	}
	byModel := make(map[string][]sample)
	for _, b := range settled {
		if b.Status != types.BetStatusWon && b.Status != types.BetStatusLost {
			continue // voids say nothing about calibration
		}
		byModel[b.ModelID] = append(byModel[b.ModelID], sample{
			prob: b.Probability, odds: b.Odds, ev: b.ExpectedValue,
			won: b.Status == types.BetStatusWon, settledOK: true,
		})
	}
	for _, p := range previews {
		if p.Status != types.BetStatusWon && p.Status != types.BetStatusLost {
			continue
		}
		byModel[p.ModelID] = append(byModel[p.ModelID], sample{
			prob: p.Probability, odds: p.Odds, ev: p.ExpectedValue,
			won: p.Status == types.BetStatusWon, settledOK: true,
		})
	}
	for _, b := range pending { // profile dots only, no outcome yet
		byModel[b.ModelID] = append(byModel[b.ModelID], sample{
			prob: b.Probability, odds: b.Odds, ev: b.ExpectedValue,
		})
	}

	var out []modelDiag
	for i, c := range s.lineup {
		samples := byModel[c.Name]
		if len(samples) == 0 {
			continue
		}
		d := modelDiag{Model: c.Name, Slot: i + 1}

		var preds []float64
		var wins []bool
		for _, sm := range samples {
			if sm.settledOK {
				preds = append(preds, sm.prob)
				wins = append(wins, sm.won)
			}
			d.OddsXs = append(d.OddsXs, linScale(sm.odds, 1, 4, 0, stripW))
			d.EVXs = append(d.EVXs, linScale(sm.ev, 0, 0.5, 0, stripW))
		}
		d.N = len(preds)

		for _, b := range calibrationBins(preds, wins, 0.1, 5) {
			d.Calib = append(d.Calib, calibDotView{
				X:     linScale(b.Predicted, 0, 1, 0, calibW),
				Y:     linScale(b.Actual, 0, 1, calibW, 0), // SVG y inverted
				N:     b.N,
				Label: fmt.Sprintf("predicted %.0f%% → won %.0f%% (n=%d)", b.Predicted*100, b.Actual*100, b.N),
			})
		}
		out = append(out, d)
	}
	return out
}
