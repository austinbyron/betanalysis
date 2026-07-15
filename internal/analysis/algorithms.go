package analysis

import (
	"fmt"
	"math"
	"math/rand"

	"github.com/austinbyron/betanalysis/internal/config"
	"github.com/austinbyron/betanalysis/pkg/types"
	"gonum.org/v1/gonum/stat/distuv"
)

// StatsProvider supplies team win/loss records for probability estimation.
// Backed by the team_stats table in live trading (so learning persists across
// restarts) and by an in-memory replay in backtests (so there is no lookahead).
type StatsProvider interface {
	TeamRecord(teamName, sportKey string) (wins, losses int)
}

// Estimator estimates outcome probabilities for a game from team records.
// Estimators are stateless: all learning lives in the StatsProvider.
type Estimator interface {
	Name() string
	EstimateProbabilities(game types.Game) (homeProb, awayProb float64)
	// Confidence reports how much the estimator's probabilities deserve to
	// count, in [0,1]. Thin records return low confidence so the selector
	// can collapse the blend toward the market — an uninformed model pulled
	// to 50/50 otherwise inflates every underdog into a fake edge.
	Confidence(game types.Game) float64
}

// MeanEstimator reports the deterministic center of an estimator's
// probability estimate — EstimateProbabilities with all sampling and
// exploration noise removed. Display code uses it so pages stay stable
// across refreshes.
type MeanEstimator interface {
	MeanProbabilities(game types.Game) (homeProb, awayProb float64)
}

// MeanProbabilities returns the deterministic estimate for any estimator,
// falling back to EstimateProbabilities for estimators without noise.
func MeanProbabilities(e Estimator, game types.Game) (float64, float64) {
	if m, ok := e.(MeanEstimator); ok {
		return m.MeanProbabilities(game)
	}
	return e.EstimateProbabilities(game)
}

// PriorsProvider supplies per-team Beta pseudo-counts seeded from market
// expectations (e.g. preseason win totals), so a season doesn't start every
// team at 50/50.
type PriorsProvider interface {
	TeamPrior(teamName, sportKey string) (priorWins, priorLosses float64)
}

// priorStats layers seeded pseudo-games on top of real records
type priorStats struct {
	stats  StatsProvider
	priors PriorsProvider
}

// WithPriors wraps a StatsProvider so every record includes the team's
// seeded pseudo-counts. Because the warmup confidence ramp counts the same
// records, a market-seeded team also earns part of its blend voice up
// front — that is deliberate: the prior IS information, unlike a 0-0 start.
func WithPriors(stats StatsProvider, priors PriorsProvider) StatsProvider {
	return &priorStats{stats: stats, priors: priors}
}

// TeamRecord returns the real record plus rounded prior pseudo-counts
func (p *priorStats) TeamRecord(teamName, sportKey string) (wins, losses int) {
	wins, losses = p.stats.TeamRecord(teamName, sportKey)
	pw, pl := p.priors.TeamPrior(teamName, sportKey)
	return wins + int(math.Round(pw)), losses + int(math.Round(pl))
}

// recordConfidence ramps from 0 to 1 as the lesser-known team accumulates
// warmup games. warmup <= 0 disables the ramp.
func recordConfidence(stats StatsProvider, game types.Game, warmup int) float64 {
	if warmup <= 0 {
		return 1
	}
	hw, hl := stats.TeamRecord(game.HomeTeam, game.SportKey)
	aw, al := stats.TeamRecord(game.AwayTeam, game.SportKey)
	games := hw + hl
	if aw+al < games {
		games = aw + al
	}
	return math.Min(float64(games)/float64(warmup), 1)
}

// GameAdjuster refines an estimator's probabilities with game-specific
// context an estimator can't see (e.g. MLB starting pitchers). Adjusters
// must return the input unchanged for games they don't apply to.
type GameAdjuster interface {
	Name() string
	Adjust(game types.Game, homeProb, awayProb float64) (float64, float64)
}

// adjustedEstimator applies adjusters in order on top of an estimator
type adjustedEstimator struct {
	Estimator
	adjusters []GameAdjuster
}

// WithAdjusters wraps an estimator so each adjuster refines its output
func WithAdjusters(inner Estimator, adjusters ...GameAdjuster) Estimator {
	if len(adjusters) == 0 {
		return inner
	}
	return &adjustedEstimator{Estimator: inner, adjusters: adjusters}
}

// EstimateProbabilities runs the inner estimator then each adjuster
func (a *adjustedEstimator) EstimateProbabilities(game types.Game) (float64, float64) {
	homeProb, awayProb := a.Estimator.EstimateProbabilities(game)
	for _, adj := range a.adjusters {
		homeProb, awayProb = adj.Adjust(game, homeProb, awayProb)
	}
	return homeProb, awayProb
}

// MeanProbabilities runs the inner estimator's mean then each adjuster
func (a *adjustedEstimator) MeanProbabilities(game types.Game) (float64, float64) {
	homeProb, awayProb := MeanProbabilities(a.Estimator, game)
	for _, adj := range a.adjusters {
		homeProb, awayProb = adj.Adjust(game, homeProb, awayProb)
	}
	return homeProb, awayProb
}

// NewEstimator constructs the estimator selected by cfg.ModelType. games
// is only required for score-based models (elo); pass nil otherwise.
func NewEstimator(cfg config.AnalysisConfig, stats StatsProvider, games GamesProvider) (Estimator, error) {
	switch cfg.ModelType {
	case "", "thompson":
		return NewThompsonSampling(cfg, stats), nil
	case "epsilon_greedy":
		return NewEpsilonGreedy(cfg, stats), nil
	case "historical":
		h := NewHistorical(stats)
		h.warmupGames = cfg.WarmupGames
		return h, nil
	case "elo":
		if games == nil {
			return nil, fmt.Errorf("model type elo requires a games provider")
		}
		return NewElo(games, cfg.WarmupGames), nil
	default:
		return nil, fmt.Errorf("unknown model type: %s", cfg.ModelType)
	}
}

// ThompsonSampling samples win probabilities from a Beta posterior over each
// team's persistent win/loss record. The sampling adds exploration: strong
// records produce confident draws, thin records produce noisy ones.
type ThompsonSampling struct {
	alphaPrior  float64
	betaPrior   float64
	stats       StatsProvider
	warmupGames int
}

// NewThompsonSampling creates a new Thompson Sampling estimator
func NewThompsonSampling(cfg config.AnalysisConfig, stats StatsProvider) *ThompsonSampling {
	alpha, beta := cfg.ThompsonAlphaPrior, cfg.ThompsonBetaPrior
	if alpha <= 0 {
		alpha = 1
	}
	if beta <= 0 {
		beta = 1
	}
	return &ThompsonSampling{alphaPrior: alpha, betaPrior: beta, stats: stats, warmupGames: cfg.WarmupGames}
}

// Confidence ramps with the lesser-known team's games played
func (t *ThompsonSampling) Confidence(game types.Game) float64 {
	return recordConfidence(t.stats, game, t.warmupGames)
}

// Name returns the estimator name
func (t *ThompsonSampling) Name() string { return "thompson" }

// EstimateProbabilities samples each team's win probability from its Beta
// posterior and normalizes the pair.
func (t *ThompsonSampling) EstimateProbabilities(game types.Game) (float64, float64) {
	homeProb := t.sample(game.HomeTeam, game.SportKey)
	awayProb := t.sample(game.AwayTeam, game.SportKey)
	return normalizePair(homeProb, awayProb)
}

func (t *ThompsonSampling) sample(team, sportKey string) float64 {
	wins, losses := t.stats.TeamRecord(team, sportKey)
	dist := distuv.Beta{
		Alpha: float64(wins) + t.alphaPrior,
		Beta:  float64(losses) + t.betaPrior,
	}
	return dist.Rand()
}

// MeanProbabilities uses each Beta posterior's mean instead of a sample
func (t *ThompsonSampling) MeanProbabilities(game types.Game) (float64, float64) {
	return normalizePair(t.mean(game.HomeTeam, game.SportKey), t.mean(game.AwayTeam, game.SportKey))
}

func (t *ThompsonSampling) mean(team, sportKey string) float64 {
	wins, losses := t.stats.TeamRecord(team, sportKey)
	return (float64(wins) + t.alphaPrior) / (float64(wins+losses) + t.alphaPrior + t.betaPrior)
}

// EpsilonGreedy uses smoothed win rates, occasionally perturbing them to
// explore selections the raw record wouldn't pick.
type EpsilonGreedy struct {
	epsilon     float64
	stats       StatsProvider
	rng         *rand.Rand
	warmupGames int
}

// NewEpsilonGreedy creates a new Epsilon-Greedy estimator
// Confidence ramps with the lesser-known team's games played
func (e *EpsilonGreedy) Confidence(game types.Game) float64 {
	return recordConfidence(e.stats, game, e.warmupGames)
}

func NewEpsilonGreedy(cfg config.AnalysisConfig, stats StatsProvider) *EpsilonGreedy {
	return &EpsilonGreedy{
		epsilon:     cfg.Epsilon,
		stats:       stats,
		rng:         rand.New(rand.NewSource(rand.Int63())),
		warmupGames: cfg.WarmupGames,
	}
}

// Name returns the estimator name
func (e *EpsilonGreedy) Name() string { return "epsilon_greedy" }

// EstimateProbabilities returns Laplace-smoothed win rates, with noise added
// on exploration rounds.
func (e *EpsilonGreedy) EstimateProbabilities(game types.Game) (float64, float64) {
	homeProb := smoothedWinRate(e.stats, game.HomeTeam, game.SportKey)
	awayProb := smoothedWinRate(e.stats, game.AwayTeam, game.SportKey)

	if e.rng.Float64() < e.epsilon {
		homeProb += (e.rng.Float64() - 0.5) * 0.2
		awayProb += (e.rng.Float64() - 0.5) * 0.2
		homeProb = clamp(homeProb, 0.01, 0.99)
		awayProb = clamp(awayProb, 0.01, 0.99)
	}

	return normalizePair(homeProb, awayProb)
}

// MeanProbabilities is the smoothed win-rate estimate without exploration noise
func (e *EpsilonGreedy) MeanProbabilities(game types.Game) (float64, float64) {
	return normalizePair(
		smoothedWinRate(e.stats, game.HomeTeam, game.SportKey),
		smoothedWinRate(e.stats, game.AwayTeam, game.SportKey))
}

// Historical uses win rates shrunk toward the league average — teams with few
// games are pulled strongly toward 0.5.
type Historical struct {
	stats       StatsProvider
	shrinkage   float64 // weight given to the league average
	warmupGames int
}

// NewHistorical creates a new Historical estimator. The warmup ramp stays
// disabled here; NewEstimator wires it from config for production use.
func NewHistorical(stats StatsProvider) *Historical {
	return &Historical{stats: stats, shrinkage: 0.3}
}

// Confidence ramps with the lesser-known team's games played
func (h *Historical) Confidence(game types.Game) float64 {
	return recordConfidence(h.stats, game, h.warmupGames)
}

// Name returns the estimator name
func (h *Historical) Name() string { return "historical" }

// EstimateProbabilities returns shrunk win rates normalized to a pair
func (h *Historical) EstimateProbabilities(game types.Game) (float64, float64) {
	const leagueAvg = 0.5
	homeProb := (1-h.shrinkage)*smoothedWinRate(h.stats, game.HomeTeam, game.SportKey) + h.shrinkage*leagueAvg
	awayProb := (1-h.shrinkage)*smoothedWinRate(h.stats, game.AwayTeam, game.SportKey) + h.shrinkage*leagueAvg
	return normalizePair(homeProb, awayProb)
}

// MeanProbabilities equals EstimateProbabilities — Historical is deterministic
func (h *Historical) MeanProbabilities(game types.Game) (float64, float64) {
	return h.EstimateProbabilities(game)
}

// smoothedWinRate returns a Laplace-smoothed win rate so teams with no record
// estimate to 0.5 instead of 0.
func smoothedWinRate(stats StatsProvider, team, sportKey string) float64 {
	wins, losses := stats.TeamRecord(team, sportKey)
	return (float64(wins) + 1) / (float64(wins+losses) + 2)
}

func normalizePair(a, b float64) (float64, float64) {
	total := a + b
	if total <= 0 {
		return 0.5, 0.5
	}
	return a / total, b / total
}

func clamp(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}

// Selector turns an estimator's probabilities into a bet recommendation. The
// model probability is blended with the de-vigged market probability — the
// market is a strong predictor, so the blend keeps naive win-rate models from
// hallucinating edges against sharp lines.
type Selector struct {
	estimator    Estimator
	modelID      string  // contender name stamped on recommended bets
	marketWeight float64 // weight given to the de-vigged market probability
	minOdds      float64
	minEV        float64
}

// NewSelector creates a bet selector for an estimator. modelID names the
// contender for bet attribution; empty falls back to the estimator name.
func NewSelector(estimator Estimator, modelID string, marketWeight, minOdds, minEV float64) *Selector {
	if modelID == "" {
		modelID = estimator.Name()
	}
	return &Selector{
		estimator:    estimator,
		modelID:      modelID,
		marketWeight: clamp(marketWeight, 0, 1),
		minOdds:      minOdds,
		minEV:        minEV,
	}
}

// RecommendBet returns the highest-EV moneyline bet across bookmakers that
// clears the odds and EV thresholds, or nil. The returned bet carries the
// blended probability so stake sizing uses the same number as the EV.
func (s *Selector) RecommendBet(game types.Game, odds []types.GameOdds) *types.Bet {
	bet, _ := s.RecommendBoth(game, odds)
	return bet
}

// RecommendBoth evaluates one probability sample twice: bet uses the
// confidence-scaled blend (what the engine actually places), preview uses
// the full model weight (what the model would bet with a mature record).
// preview is non-nil only when the confidence gate is what suppressed the
// bet — it exists for display and must never be placed.
func (s *Selector) RecommendBoth(game types.Game, odds []types.GameOdds) (bet, preview *types.Bet) {
	modelHome, modelAway := s.estimator.EstimateProbabilities(game)

	// Scale the model's share by its confidence: with thin records the
	// blend collapses to the market and no edge survives — an uninformed
	// 50/50 model would otherwise inflate every underdog into a fake edge.
	fullWeight := 1 - s.marketWeight
	confidence := s.estimator.Confidence(game)

	bet = s.bestBet(game, odds, modelHome, modelAway, fullWeight*confidence)
	if bet != nil || confidence >= 1 {
		return bet, nil
	}
	return nil, s.bestBet(game, odds, modelHome, modelAway, fullWeight)
}

// Confidence exposes the estimator's record confidence for this game so the
// dashboard can label warmup previews.
func (s *Selector) Confidence(game types.Game) float64 {
	return s.estimator.Confidence(game)
}

// ModelView is a Selector's deterministic opinion on one game, for display.
// Thompson rows show posterior means here; live betting samples around them.
type ModelView struct {
	RawHome, RawAway float64 // estimator mean before adjusters
	AdjHome, AdjAway float64 // after adjusters; equals raw without any
	Confidence       float64
	HasAdjusters     bool
}

// View reports the selector's deterministic probabilities for a game
func (s *Selector) View(game types.Game) ModelView {
	v := ModelView{Confidence: s.estimator.Confidence(game)}
	if a, ok := s.estimator.(*adjustedEstimator); ok {
		v.HasAdjusters = true
		v.RawHome, v.RawAway = MeanProbabilities(a.Estimator, game)
		v.AdjHome, v.AdjAway = a.MeanProbabilities(game)
		return v
	}
	v.RawHome, v.RawAway = MeanProbabilities(s.estimator, game)
	v.AdjHome, v.AdjAway = v.RawHome, v.RawAway
	return v
}

// MarketWeight exposes the market share of the blend for display math
func (s *Selector) MarketWeight() float64 { return s.marketWeight }

// Thresholds exposes the bet gates for display math
func (s *Selector) Thresholds() (minOdds, minEV float64) { return s.minOdds, s.minEV }

// bestBet returns the highest-EV moneyline bet across bookmakers for one
// market blend weight, or nil when nothing clears the thresholds.
func (s *Selector) bestBet(game types.Game, odds []types.GameOdds, modelHome, modelAway, modelWeight float64) *types.Bet {
	bestEV := s.minEV
	var bestBet *types.Bet

	for _, o := range odds {
		if o.MarketType != types.MarketMoneyline || o.HomeOdds == nil || o.AwayOdds == nil {
			continue
		}

		marketHome, marketAway := Devig(*o.HomeOdds, *o.AwayOdds)
		pHome := (1-modelWeight)*marketHome + modelWeight*modelHome
		pAway := (1-modelWeight)*marketAway + modelWeight*modelAway

		candidates := []struct {
			selection string
			odds      float64
			prob      float64
		}{
			{types.OutcomeHome, *o.HomeOdds, pHome},
			{types.OutcomeAway, *o.AwayOdds, pAway},
		}

		for _, c := range candidates {
			if c.odds < s.minOdds {
				continue
			}
			ev := ExpectedValue(c.prob, c.odds)
			if ev > bestEV {
				bestEV = ev
				bestBet = &types.Bet{
					GameID:        game.ID,
					Selection:     c.selection,
					MarketType:    types.MarketMoneyline,
					Odds:          c.odds,
					ExpectedValue: ev,
					Probability:   c.prob,
					ModelID:       s.modelID,
					Bookmaker:     o.Bookmaker,
					Status:        types.BetStatusPending,
				}
			}
		}
	}

	return bestBet
}

// Devig removes the bookmaker overround from a two-way market by normalizing
// the implied probabilities to sum to 1.
func Devig(homeOdds, awayOdds float64) (homeProb, awayProb float64) {
	if homeOdds <= 1 || awayOdds <= 1 {
		return 0.5, 0.5
	}
	impHome := 1 / homeOdds
	impAway := 1 / awayOdds
	return normalizePair(impHome, impAway)
}

// ExpectedValue calculates expected value per unit staked
func ExpectedValue(probability, odds float64) float64 {
	return (probability * odds) - 1
}

// KellyStake returns the recommended stake for a bankroll using fractional
// Kelly, capped at maxFraction of the bankroll. Returns 0 when there is no
// edge — callers should skip the bet, never bump the stake up.
func KellyStake(probability, odds, bankroll, kellyFraction, maxFraction float64) float64 {
	b := odds - 1
	if b <= 0 || probability <= 0 || probability >= 1 || bankroll <= 0 {
		return 0
	}

	kelly := (b*probability - (1 - probability)) / b
	if kelly <= 0 {
		return 0
	}

	stake := kelly * kellyFraction * bankroll
	if maxFraction > 0 {
		if maxStake := bankroll * maxFraction; stake > maxStake {
			stake = maxStake
		}
	}

	return math.Round(stake*100) / 100
}

// SharpeRatio calculates the Sharpe ratio for a series of returns
func SharpeRatio(returns []float64, riskFreeRate float64) float64 {
	if len(returns) < 2 {
		return 0
	}

	mean := 0.0
	for _, r := range returns {
		mean += r
	}
	mean /= float64(len(returns))

	variance := 0.0
	for _, r := range returns {
		variance += (r - mean) * (r - mean)
	}
	stdDev := math.Sqrt(variance / float64(len(returns)-1))

	// Guard float dust from identical returns, not just exact zero
	if stdDev < 1e-9 {
		return 0
	}

	return (mean - riskFreeRate) / stdDev
}
