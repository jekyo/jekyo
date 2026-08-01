package main

import (
	"strings"
	"testing"
)

func TestSparkline(t *testing.T) {
	out := sparkline([]int64{0, 50, 100}, 3)
	r := []rune(out)
	if len(r) != 3 {
		t.Fatalf("want 3 runes, got %d (%q)", len(r), out)
	}
	if r[2] != '█' {
		t.Errorf("max value should render full block, got %q", r[2])
	}
	// shorter history left-pads with spaces
	if got := []rune(sparkline([]int64{5}, 4)); len(got) != 4 || got[0] != ' ' {
		t.Errorf("padding wrong: %q", string(got))
	}
}

func TestUIViewSmoke(t *testing.T) {
	m := &uiModel{
		ctxName: "test",
		width:   150, height: 46,
		snap: &topSnapshot{
			Nodes: []topNode{{Name: "n1", CPUPct: 10, MemPct: 20, PodCount: 2}},
			Pods: []topPod{
				{App: "acme", Service: "api", Ready: "1/1", Status: "Running", CPUMilli: 10, MemBytes: 1 << 20},
				{App: "acme", Service: "db", Ready: "0/1", Status: "Pending"},
			},
		},
		logs:   map[string][]string{"acme/api": {"hello log"}},
		hist:   map[string][]histPt{"acme/api": {{cpu: 5, mem: 1 << 20}, {cpu: 10, mem: 2 << 20}}},
		events: map[string][]string{},
	}
	m.rebuildRows()
	if app, svc, ok := m.selected(); !ok || app != "acme" || svc != "api" {
		t.Fatalf("selection: %s/%s ok=%v", app, svc, ok)
	}
	for _, tab := range []int{tabLogs, tabStatus} {
		m.tab = tab
		out := m.View()
		if !strings.Contains(out, "acme") {
			t.Errorf("tab %d: output missing app name", tab)
		}
	}
	m.tab = tabLogs
	if !strings.Contains(m.View(), "hello log") {
		t.Error("logs tab missing log line")
	}
	m.graphs = true
	m.tab = tabLogs
	if !strings.Contains(m.View(), "peak") {
		t.Error("metrics strip missing peak stats")
	}
	// nav skips app header rows
	m.move(1)
	if _, svc, _ := m.selected(); svc != "db" {
		t.Errorf("move landed on %q, want db", svc)
	}
}
