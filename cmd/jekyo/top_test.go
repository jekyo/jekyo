package main

import (
	"strings"
	"testing"
	"time"
)

func TestRenderTop(t *testing.T) {
	snap := &topSnapshot{
		Context: "test",
		Nodes: []topNode{{
			Name: "node1", CPUMilli: 500, CPUCapMilli: 4000, CPUPct: 12.5,
			MemBytes: 2 << 30, MemCapBytes: 8 << 30, MemPct: 25, PodCount: 3,
		}},
		Pods: []topPod{
			{App: "acme", Service: "api", Ready: "1/1", Status: "Running", CPUMilli: 120, MemBytes: 256 << 20, CPULimitMilli: 1000, CPUPct: 12, MemLimitBytes: 512 << 20, MemPct: 50},
			{App: "acme", Service: "db", Ready: "0/1", Status: "CrashLoopBackOff", CPUMilli: 0, MemBytes: 0},
		},
	}
	out := renderTop(snap, 2*time.Second)
	for _, want := range []string{"node1", "acme", "CrashLoopBackOff", "120m", "256Mi", "12.5%"} {
		if !strings.Contains(out, want) {
			t.Errorf("renderTop output missing %q", want)
		}
	}
}

func TestBarBounds(t *testing.T) {
	for _, pct := range []float64{-5, 0, 50, 100, 250} {
		out := bar(pct, 10)
		if n := strings.Count(out, "█") + strings.Count(out, "░"); n != 10 {
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
