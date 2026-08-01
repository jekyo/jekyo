package main

import (
	"strings"
	"testing"
)

func TestRateAndPct(t *testing.T) {
	if got := rate(100, 300, 2); got != 100 {
		t.Errorf("rate = %v, want 100", got)
	}
	// counter reset (pod restart) must clamp to zero, not go negative
	if got := rate(300, 100, 2); got != 0 {
		t.Errorf("rate after reset = %v, want 0", got)
	}
	if got := pct(50, 200); got != 25 {
		t.Errorf("pct = %v, want 25", got)
	}
	if got := pct(1, 0); got != 0 {
		t.Errorf("pct with zero capacity = %v, want 0", got)
	}
	if got := fmtRate(3 << 20); got != "3.0M/s" {
		t.Errorf("fmtRate = %q", got)
	}
}

func TestBarNegativeWidth(t *testing.T) {
	// regression: narrow terminals fed negative widths into strings.Repeat
	if bar(50, -1) != "" || bar(50, 0) != "" {
		t.Error("bar must render empty at non-positive widths")
	}
	if barCalm(50, -2) != "" {
		t.Error("barCalm must render empty at non-positive widths")
	}
	if sparkline([]int64{1}, 0) != "" || braille([]int64{1}, -1) != "" {
		t.Error("graphs must render empty at non-positive widths")
	}
}

func TestBarBounds(t *testing.T) {
	for _, pct := range []float64{-5, 0, 50, 100, 250} {
		out := bar(pct, 10)
		if n := strings.Count(out, "▄"); n != 10 {
			t.Errorf("bar(%v) rendered %d cells, want 10", pct, n)
		}
	}
}

func TestFmtUnits(t *testing.T) {
	if got := fmtCPU(1500); got != "1.50" {
		t.Errorf("fmtCPU(1500) = %q", got)
	}
	if got := fmtMem(3 << 30); got != "3.0Gi" {
		t.Errorf("fmtMem = %q", got)
	}
}
