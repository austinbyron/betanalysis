// Chart geometry helpers for the analysis pages. Pure math: inputs in
// data units, outputs ready for SVG templates. Rendering stays in the
// templates, matching how the bankroll chart is built.
package web

import (
	"fmt"
	"math"
	"strings"

	"gonum.org/v1/gonum/stat/distuv"
)

// xyPoint is one sample of a server-rendered curve
type xyPoint struct{ X, Y float64 }

// betaCurve samples the Beta(alpha, beta) density at n evenly spaced
// points across (0,1). Endpoints are excluded — the density diverges
// there for shape parameters below 1.
func betaCurve(alpha, beta float64, n int) []xyPoint {
	dist := distuv.Beta{Alpha: alpha, Beta: beta}
	pts := make([]xyPoint, 0, n)
	for i := 1; i <= n; i++ {
		x := float64(i) / float64(n+1)
		pts = append(pts, xyPoint{X: x, Y: dist.Prob(x)})
	}
	return pts
}

// betaSummary returns the posterior mean and 95% credible interval
func betaSummary(alpha, beta float64) (mean, lo, hi float64) {
	dist := distuv.Beta{Alpha: alpha, Beta: beta}
	return dist.Mean(), dist.Quantile(0.025), dist.Quantile(0.975)
}

// calibBin is one calibration bucket: predictions in [k·w, (k+1)·w) vs reality
type calibBin struct {
	Predicted float64 // mean predicted probability in the bin
	Actual    float64 // observed win rate
	N         int
}

// calibrationBins buckets predictions into fixed-width bins and reports
// each bin's mean prediction against its actual win rate. Bins under
// minN are dropped — a two-bet bin is noise, not signal.
func calibrationBins(preds []float64, wins []bool, binWidth float64, minN int) []calibBin {
	nBins := int(math.Ceil(1 / binWidth))
	sums := make([]float64, nBins)
	won := make([]int, nBins)
	count := make([]int, nBins)
	for i, p := range preds {
		b := int(p / binWidth)
		if b >= nBins {
			b = nBins - 1
		}
		sums[b] += p
		count[b]++
		if wins[i] {
			won[b]++
		}
	}
	var out []calibBin
	for b := 0; b < nBins; b++ {
		if count[b] < minN {
			continue
		}
		out = append(out, calibBin{
			Predicted: sums[b] / float64(count[b]),
			Actual:    float64(won[b]) / float64(count[b]),
			N:         count[b],
		})
	}
	return out
}

// linScale maps v from [lo,hi] to [outLo,outHi], clamping to the range
func linScale(v, lo, hi, outLo, outHi float64) float64 {
	if hi <= lo {
		return outLo
	}
	t := (v - lo) / (hi - lo)
	t = math.Max(0, math.Min(1, t))
	return outLo + t*(outHi-outLo)
}

// svgPath renders points as an SVG path, one decimal like the equity chart
func svgPath(pts []xyPoint) string {
	var b strings.Builder
	for i, p := range pts {
		if i == 0 {
			fmt.Fprintf(&b, "M%.1f %.1f", p.X, p.Y)
		} else {
			fmt.Fprintf(&b, " L%.1f %.1f", p.X, p.Y)
		}
	}
	return b.String()
}
