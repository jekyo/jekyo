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

// A sidecar row shows its own container's logs and stays selected across
// the 2s refresh instead of snapping back to its service row.
func TestUISidecarRowLogs(t *testing.T) {
	m := &uiModel{
		ctxName: "test",
		width:   150, height: 46,
		snap: &topSnapshot{
			Pods: []topPod{{App: "vatra", Service: "api", Ready: "2/2", Status: "Running",
				Containers: []topContainer{{Name: "api"}, {Name: "unbound"}}}},
		},
		logs:   map[string][]string{"vatra/api": {"service line"}, "vatra/api/unbound": {"sidecar line"}},
		hist:   map[string][]histPt{},
		events: map[string][]string{},
	}
	m.rebuildRows()
	m.move(1)
	if target, _ := m.selectedTarget(); target != "vatra/api/unbound" {
		t.Fatalf("selected %q, want the sidecar row", target)
	}
	m.rebuildRows()
	if target, _ := m.selectedTarget(); target != "vatra/api/unbound" {
		t.Fatalf("refresh moved selection to %q", target)
	}
	m.tab = tabLogs
	if out := m.View(); !strings.Contains(out, "sidecar line") || strings.Contains(out, "service line") {
		t.Error("sidecar row should show the sidecar's logs only")
	}
}

// nvidia-smi prints one line per GPU; all of them are kept (an 8x V100
// box showed only GPU 0).
func TestParseGPUs(t *testing.T) {
	out := "0, 512, 32768, 34\n87, 30000, 32768, 61\n"
	g := parseGPUs(out)
	if len(g) != 2 || g[0].util != 0 || g[1].util != 87 || g[1].memUsed != 30000 || g[1].memTot != 32768 || g[1].temp != 61 {
		t.Fatalf("got %+v", g)
	}
	if g := parseGPUs("none"); len(g) != 0 {
		t.Fatalf("no GPU: %+v", g)
	}

	m := &uiModel{ctxName: "test", width: 150, height: 46,
		snap: &topSnapshot{Nodes: []topNode{{Name: "n1"}}},
		logs: map[string][]string{}, hist: map[string][]histPt{}, events: map[string][]string{},
		gpus: parseGPUs(out), gpuProbed: true}
	m.rebuildRows()
	if v := m.View(); !strings.Contains(v, "30000Mi/32768Mi") || !strings.Contains(v, "512Mi/32768Mi") {
		t.Error("view should render every GPU")
	}
}
