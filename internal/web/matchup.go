package web

import (
	"fmt"
	"net/http"
	"time"

	"github.com/austinbyron/betanalysis/internal/analysis"
	"github.com/austinbyron/betanalysis/pkg/types"
	"github.com/rs/zerolog/log"
)

// oddsRow is one bookmaker's current moneyline with de-vigged probabilities
type oddsRow struct {
	Book               string
	HomeOdds, AwayOdds float64
	HomeProb, AwayProb float64
	Age                string
}

// betaDuel is both teams' Beta posteriors as SVG geometry — the Thompson
// arms this matchup samples from.
type betaDuel struct {
	Width, Height        float64
	HomePath, AwayPath   string
	HomeMeanX, AwayMeanX float64
	HomeLabel, AwayLabel string
}

// boardRow is one contender's deterministic verdict on this game
type boardRow struct {
	Model      string
	Slot       int
	RawHome    float64
	AdjHome    float64
	Adjusted   bool
	Confidence float64
	BlendHome  float64 // confidence-scaled blend vs the median market
	Pick       string  // team name, or "no edge"
	PickEV     float64
	PickOdds   float64
	PickBook   string
}

// eloPanel is both teams' rating trajectories plus the implied probability
type eloPanel struct {
	Width, Height          float64
	HomePath, AwayPath     string
	HomeRating, AwayRating float64
	ImpliedHome            float64
}

type matchupData struct {
	Game        types.Game
	GameURL     string
	MarketProb  float64
	Odds        []oddsRow
	Duel        *betaDuel
	Board       []boardRow
	Elo         *eloPanel
	Bets        []betRow
	Previews    []previewRow
	GeneratedAt time.Time
}

func (s *Server) handleMatchup(w http.ResponseWriter, r *http.Request) {
	game, err := s.store.GetGameByID(r.PathValue("id"))
	if err != nil {
		log.Error().Err(err).Msg("matchup: game lookup failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if game == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><title>betanalysis</title><p style="font-family:system-ui;padding:40px;color:#c3c2b7;background:#0d0d0d">Game not found. <a style="color:inherit" href="/analysis">Back to analysis</a>.</p>`)
		return
	}

	data := s.buildMatchup(*game)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "matchup.html", data); err != nil {
		log.Error().Err(err).Msg("Failed to render matchup page")
	}
}

func (s *Server) buildMatchup(game types.Game) matchupData {
	data := matchupData{
		Game:        game,
		GameURL:     s.gameURL(game),
		GeneratedAt: time.Now(),
	}

	odds, err := s.store.GetOddsForGame(game.ID)
	if err != nil {
		log.Error().Err(err).Msg("matchup: odds lookup failed")
	}
	for _, o := range odds {
		if o.MarketType != types.MarketMoneyline || o.HomeOdds == nil || o.AwayOdds == nil {
			continue
		}
		h, a := analysis.Devig(*o.HomeOdds, *o.AwayOdds)
		data.Odds = append(data.Odds, oddsRow{
			Book: o.Bookmaker, HomeOdds: *o.HomeOdds, AwayOdds: *o.AwayOdds,
			HomeProb: h, AwayProb: a, Age: formatAge(time.Since(o.RetrievedAt)),
		})
	}
	data.MarketProb, _ = medianMarketProb(odds)

	data.Duel = s.buildBetaDuel(game)
	data.Board = s.buildBoard(game, odds, data.MarketProb)
	data.Elo = s.buildEloPanel(game)
	data.Bets, data.Previews = s.buildPositions(game.ID)
	return data
}

// buildBetaDuel renders both teams' Beta posteriors — the records the
// models actually see, priors included.
func (s *Server) buildBetaDuel(game types.Game) *betaDuel {
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

	const width, height = 640.0, 180.0
	hw, hl := s.stats.TeamRecord(game.HomeTeam, game.SportKey)
	aw, al := s.stats.TeamRecord(game.AwayTeam, game.SportKey)

	homeCurve := betaCurve(float64(hw)+alpha, float64(hl)+beta, 101)
	awayCurve := betaCurve(float64(aw)+alpha, float64(al)+beta, 101)

	maxY := 0.0
	for _, p := range append(append([]xyPoint{}, homeCurve...), awayCurve...) {
		if p.Y > maxY {
			maxY = p.Y
		}
	}
	if maxY == 0 {
		maxY = 1
	}
	scale := func(pts []xyPoint) []xyPoint {
		out := make([]xyPoint, len(pts))
		for i, p := range pts {
			out[i] = xyPoint{
				X: linScale(p.X, 0, 1, 0, width),
				Y: linScale(p.Y, 0, maxY, height-6, 6), // inverted: SVG y grows down
			}
		}
		return out
	}

	hMean, _, _ := betaSummary(float64(hw)+alpha, float64(hl)+beta)
	aMean, _, _ := betaSummary(float64(aw)+alpha, float64(al)+beta)

	return &betaDuel{
		Width: width, Height: height,
		HomePath:  svgPath(scale(homeCurve)),
		AwayPath:  svgPath(scale(awayCurve)),
		HomeMeanX: linScale(hMean, 0, 1, 0, width),
		AwayMeanX: linScale(aMean, 0, 1, 0, width),
		HomeLabel: fmt.Sprintf("%s — %d–%d incl. priors — mean %.0f%%", game.HomeTeam, hw, hl, hMean*100),
		AwayLabel: fmt.Sprintf("%s — %d–%d incl. priors — mean %.0f%%", game.AwayTeam, aw, al, aMean*100),
	}
}

// buildBoard evaluates every covering contender deterministically: the
// same blend-and-threshold logic the selector bets with, but on posterior
// means so the page is stable across refreshes.
func (s *Server) buildBoard(game types.Game, odds []types.GameOdds, market float64) []boardRow {
	var rows []boardRow
	for i, c := range s.lineup {
		if !c.CoversSport(game.SportKey) {
			continue
		}
		v := c.Selector.View(game)
		minOdds, minEV := c.Selector.Thresholds()
		modelWeight := (1 - c.Selector.MarketWeight()) * v.Confidence

		row := boardRow{
			Model: c.Name, Slot: i + 1,
			RawHome: v.RawHome, AdjHome: v.AdjHome,
			Adjusted: v.HasAdjusters, Confidence: v.Confidence,
			BlendHome: (1-modelWeight)*market + modelWeight*v.AdjHome,
			Pick:      "no edge",
		}

		bestEV := minEV
		for _, o := range odds {
			if o.MarketType != types.MarketMoneyline || o.HomeOdds == nil || o.AwayOdds == nil {
				continue
			}
			mh, ma := analysis.Devig(*o.HomeOdds, *o.AwayOdds)
			pHome := (1-modelWeight)*mh + modelWeight*v.AdjHome
			pAway := (1-modelWeight)*ma + modelWeight*v.AdjAway
			for _, cand := range []struct {
				team, book string
				odds, prob float64
			}{
				{game.HomeTeam, o.Bookmaker, *o.HomeOdds, pHome},
				{game.AwayTeam, o.Bookmaker, *o.AwayOdds, pAway},
			} {
				if cand.odds < minOdds {
					continue
				}
				if ev := analysis.ExpectedValue(cand.prob, cand.odds); ev > bestEV {
					bestEV = ev
					row.Pick, row.PickEV, row.PickOdds, row.PickBook = cand.team, ev, cand.odds, cand.book
				}
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// buildEloPanel replays the sport's game history into both teams' rating
// trajectories. Nil when there is no scored history yet.
func (s *Server) buildEloPanel(game types.Game) *eloPanel {
	games, err := s.store.FinishedGames(game.SportKey)
	if err != nil || len(games) == 0 {
		return nil
	}
	hist := analysis.EloHistory(games)
	home, away := hist[game.HomeTeam], hist[game.AwayTeam]
	if len(home) == 0 && len(away) == 0 {
		return nil
	}

	const width, height = 640.0, 160.0
	minT, maxT := time.Now(), time.Time{}
	minR, maxR := analysis.EloInitial, analysis.EloInitial
	for _, series := range [][]analysis.RatingPoint{home, away} {
		for _, p := range series {
			if p.At.Before(minT) {
				minT = p.At
			}
			if p.At.After(maxT) {
				maxT = p.At
			}
			if p.Rating < minR {
				minR = p.Rating
			}
			if p.Rating > maxR {
				maxR = p.Rating
			}
		}
	}
	if maxR-minR < 1 {
		maxR = minR + 1
	}
	span := maxT.Sub(minT).Seconds()
	if span < 1 {
		span = 1
	}
	toPts := func(series []analysis.RatingPoint) []xyPoint {
		pts := make([]xyPoint, len(series))
		for i, p := range series {
			pts[i] = xyPoint{
				X: (p.At.Sub(minT).Seconds() / span) * width,
				Y: linScale(p.Rating, minR, maxR, height-6, 6),
			}
		}
		return pts
	}
	rating := func(series []analysis.RatingPoint) float64 {
		if len(series) == 0 {
			return analysis.EloInitial
		}
		return series[len(series)-1].Rating
	}

	hr, ar := rating(home), rating(away)
	return &eloPanel{
		Width: width, Height: height,
		HomePath: svgPath(toPts(home)), AwayPath: svgPath(toPts(away)),
		HomeRating: hr, AwayRating: ar,
		ImpliedHome: analysis.EloProbability(hr, ar),
	}
}

// buildPositions collects every bet and preview recorded on this game
func (s *Server) buildPositions(gameID string) ([]betRow, []previewRow) {
	var bets []betRow
	pending, err := s.store.GetPendingBets()
	if err != nil {
		log.Error().Err(err).Msg("matchup: pending bets failed")
	}
	settled, err := s.store.GetSettledBets()
	if err != nil {
		log.Error().Err(err).Msg("matchup: settled bets failed")
	}
	for _, bet := range append(append([]types.Bet{}, pending...), settled...) {
		if bet.GameID == gameID {
			bets = append(bets, s.toBetRow(bet))
		}
	}

	var previews []previewRow
	pendingP, err := s.store.GetPendingPreviewBets()
	if err != nil {
		log.Error().Err(err).Msg("matchup: pending previews failed")
	}
	settledP, err := s.store.GetSettledPreviewBets()
	if err != nil {
		log.Error().Err(err).Msg("matchup: settled previews failed")
	}
	for _, pb := range append(append([]types.PreviewBet{}, pendingP...), settledP...) {
		if pb.GameID == gameID {
			previews = append(previews, previewRow{P: pb, Matchup: pb.GameID})
		}
	}
	return bets, previews
}
