package web

import (
	"math"
	"strings"
	"testing"
)

func TestBetaCurveSymmetricAndPeaked(t *testing.T) {
	pts := betaCurve(5, 5, 101)
	if len(pts) != 101 {
		t.Fatalf("want 101 samples, got %d", len(pts))
	}
	mid := pts[50]
	if math.Abs(mid.X-0.5) > 1e-9 {
		t.Fatalf("middle sample at x=%v, want 0.5", mid.X)
	}
	for _, p := range pts {
		if p.Y > mid.Y+1e-9 {
			t.Fatalf("Beta(5,5) must peak at 0.5; %v > %v at x=%v", p.Y, mid.Y, p.X)
		}
		if p.X <= 0 || p.X >= 1 {
			t.Fatalf("endpoints must be excluded, got x=%v", p.X)
		}
	}
	if math.Abs(pts[25].Y-pts[75].Y) > 1e-6 {
		t.Fatalf("symmetric density: %v vs %v", pts[25].Y, pts[75].Y)
	}
}

func TestBetaSummaryOrdering(t *testing.T) {
	mean, lo, hi := betaSummary(31, 11) // 30-10 record, 1-1 prior
	if !(lo < mean && mean < hi) {
		t.Fatalf("want lo < mean < hi, got %v %v %v", lo, mean, hi)
	}
	if math.Abs(mean-31.0/42.0) > 1e-9 {
		t.Fatalf("mean = %v, want %v", mean, 31.0/42.0)
	}
	if lo < 0 || hi > 1 {
		t.Fatalf("interval must stay in [0,1]: %v %v", lo, hi)
	}
}

func TestCalibrationBins(t *testing.T) {
	// 6 bets at ~0.55 (4 won), 2 bets at ~0.75 (dropped: under minN)
	preds := []float64{0.54, 0.55, 0.55, 0.56, 0.57, 0.53, 0.74, 0.76}
	wins := []bool{true, true, true, true, false, false, true, false}
	bins := calibrationBins(preds, wins, 0.1, 5)
	if len(bins) != 1 {
		t.Fatalf("want 1 surviving bin, got %d: %+v", len(bins), bins)
	}
	b := bins[0]
	if b.N != 6 {
		t.Fatalf("bin n = %d, want 6", b.N)
	}
	if math.Abs(b.Actual-4.0/6.0) > 1e-9 {
		t.Fatalf("actual = %v, want %v", b.Actual, 4.0/6.0)
	}
	if b.Predicted < 0.5 || b.Predicted > 0.6 {
		t.Fatalf("predicted = %v, want in bin [0.5,0.6)", b.Predicted)
	}
}

func TestCalibrationBinsClampsTopEdge(t *testing.T) {
	preds := []float64{1.0, 1.0, 1.0, 1.0, 1.0}
	wins := []bool{true, true, true, true, true}
	bins := calibrationBins(preds, wins, 0.1, 5)
	if len(bins) != 1 || bins[0].N != 5 {
		t.Fatalf("p=1.0 must land in the top bin, got %+v", bins)
	}
}

func TestLinScaleClamps(t *testing.T) {
	if got := linScale(0.5, 0, 1, 0, 100); got != 50 {
		t.Fatalf("midpoint = %v", got)
	}
	if got := linScale(2, 0, 1, 0, 100); got != 100 {
		t.Fatalf("above range must clamp: %v", got)
	}
	if got := linScale(-1, 0, 1, 0, 100); got != 0 {
		t.Fatalf("below range must clamp: %v", got)
	}
	if got := linScale(5, 3, 3, 0, 100); got != 0 {
		t.Fatalf("degenerate domain returns outLo: %v", got)
	}
}

func TestSvgPath(t *testing.T) {
	p := svgPath([]xyPoint{{X: 1, Y: 2}, {X: 3.25, Y: 4}})
	if !strings.HasPrefix(p, "M1.0 2.0") || !strings.Contains(p, "L3.2 4.0") {
		t.Fatalf("path = %q", p)
	}
	if svgPath(nil) != "" {
		t.Fatal("empty input renders empty path")
	}
}
