package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	uiAmber    = lipgloss.Color("214")
	uiDim      = lipgloss.Color("240")
	uiGood     = lipgloss.Color("42")
	uiBad      = lipgloss.Color("196")
	uiSelStyle = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(uiAmber).Bold(true)
	uiHdrStyle = lipgloss.NewStyle().Foreground(uiAmber).Bold(true)
	uiDimStyle = lipgloss.NewStyle().Foreground(uiDim)
	uiTabOn    = lipgloss.NewStyle().Foreground(uiAmber).Bold(true).Underline(true)
	uiTabOff   = uiDimStyle
	uiPane     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(uiDim)
)

var sparkBlocks = []rune("▁▂▃▄▅▆▇█")

// sparkline renders values scaled to their own max into width runes,
// keeping the most recent samples.
func sparkline(vals []int64, width int) string {
	if width <= 0 {
		return ""
	}
	if len(vals) > width {
		vals = vals[len(vals)-width:]
	}
	var max int64 = 1
	for _, v := range vals {
		if v > max {
			max = v
		}
	}
	var b strings.Builder
	for i := 0; i < width-len(vals); i++ {
		b.WriteRune(' ')
	}
	for _, v := range vals {
		idx := int(float64(v) / float64(max) * float64(len(sparkBlocks)-1))
		b.WriteRune(sparkBlocks[idx])
	}
	return b.String()
}

func (m *uiModel) View() string {
	if m.width == 0 {
		return "loading..."
	}
	leftW := 34
	if m.width < 80 {
		leftW = m.width / 3
	}
	rightW := m.width - leftW - 6
	bodyH := m.height - 8 // header(3) + tabs(1) + footer(1) + borders

	header := m.viewHeader()
	left := uiPane.Width(leftW).Height(bodyH).Render(m.viewTree(leftW, bodyH))
	right := uiPane.Width(rightW).Height(bodyH).Render(m.viewRight(rightW, bodyH))
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	return header + "\n" + body + "\n" + m.viewFooter()
}

// viewHeader is the btop-style server strip: gauges for cpu/mem/disk and
// live network rates, each with a short history sparkline.
func (m *uiModel) viewHeader() string {
	title := uiHdrStyle.Render("JEKYO") + uiDimStyle.Render("  context ") + m.ctxName
	if len(m.snap.Nodes) == 0 {
		return title + "\n"
	}
	n := m.snap.Nodes[0]
	var cpus, mems, rxs, txs []int64
	var curRx, curTx float64
	for _, h := range m.nodeHist {
		cpus = append(cpus, int64(h.cpuPct*100))
		mems = append(mems, int64(h.memPct*100))
		rxs = append(rxs, int64(h.rx))
		txs = append(txs, int64(h.tx))
	}
	if len(m.nodeHist) > 0 {
		curRx, curTx = m.nodeHist[len(m.nodeHist)-1].rx, m.nodeHist[len(m.nodeHist)-1].tx
	}
	sw := 18
	amber := lipgloss.NewStyle().Foreground(uiAmber)
	green := lipgloss.NewStyle().Foreground(uiGood)
	line2 := fmt.Sprintf("  cpu %s %5.1f%% %s   mem %s %5.1f%% %s",
		bar(n.CPUPct, 14), n.CPUPct, amber.Render(sparkline(cpus, sw)),
		bar(n.MemPct, 14), n.MemPct, green.Render(sparkline(mems, sw)))
	line3 := fmt.Sprintf("  dsk %s %5.1f%%  (%s/%s)   net ↓ %-8s %s ↑ %-8s %s",
		bar(n.DiskPct, 14), n.DiskPct, fmtMem(n.DiskUsed), fmtMem(n.DiskCap),
		fmtRate(curRx), amber.Render(sparkline(rxs, 12)),
		fmtRate(curTx), green.Render(sparkline(txs, 12)))
	return title + uiDimStyle.Render(fmt.Sprintf("   %s · %d pods", n.Name, n.PodCount)) + "\n" + line2 + "\n" + line3
}

func (m *uiModel) viewTree(w, h int) string {
	if len(m.rows) == 0 {
		return uiDimStyle.Render("no apps deployed")
	}
	byKey := map[string]topPod{}
	for _, p := range m.snap.Pods {
		k := p.App + "/" + p.Service
		agg := byKey[k]
		agg.CPUMilli += p.CPUMilli
		agg.MemBytes += p.MemBytes
		agg.Status = p.Status
		agg.Ready = p.Ready
		byKey[k] = agg
	}
	var b strings.Builder
	lines := 0
	for i, r := range m.rows {
		if lines >= h-1 {
			break
		}
		if r.header {
			b.WriteString(uiHdrStyle.Render("▾ " + r.app))
		} else {
			p := byKey[r.app+"/"+r.service]
			dot := lipgloss.NewStyle().Foreground(uiGood).Render("●")
			if p.Status != "Running" {
				dot = lipgloss.NewStyle().Foreground(uiBad).Render("●")
			}
			line := fmt.Sprintf("  %s %-13s %5s %6s", dot, truncate(r.service, 13), fmtCPU(p.CPUMilli), fmtMem(p.MemBytes))
			if i == m.cursor {
				line = uiSelStyle.Render(line)
			}
			b.WriteString(line)
		}
		b.WriteString("\n")
		lines++
	}
	return b.String()
}

func (m *uiModel) viewRight(w, h int) string {
	tabs := []string{"logs (l)", "metrics (m)", "status (s)"}
	var tb strings.Builder
	for i, t := range tabs {
		if i == m.tab {
			tb.WriteString(uiTabOn.Render(t))
		} else {
			tb.WriteString(uiTabOff.Render(t))
		}
		tb.WriteString("  ")
	}
	app, svc, ok := m.selected()
	key := app + "/" + svc
	title := tb.String()
	if ok {
		title += uiDimStyle.Render(" · ") + uiHdrStyle.Render(key)
	}

	content := ""
	switch m.tab {
	case tabLogs:
		lines := m.logs[key]
		avail := h - 2
		if len(lines) > avail {
			lines = lines[len(lines)-avail:]
		}
		if len(lines) == 0 {
			content = uiDimStyle.Render("waiting for logs...")
		} else {
			var b strings.Builder
			for _, l := range lines {
				b.WriteString(truncate(l, w-2) + "\n")
			}
			content = b.String()
		}
	case tabMetrics:
		content = m.viewMetrics(key, w-4)
	case tabStatus:
		var b strings.Builder
		for _, p := range m.snap.Pods {
			if p.App != app {
				continue
			}
			b.WriteString(fmt.Sprintf("%-14s %-6s %-16s restarts %d  cpu %s  mem %s\n",
				p.Service, p.Ready, p.Status, p.Restarts, fmtCPU(p.CPUMilli), fmtMem(p.MemBytes)))
		}
		seen := map[string]bool{}
		wroteVolHdr := false
		for _, v := range m.snap.Volumes {
			if v.App != app || seen[v.Claim] || v.Capacity == 0 {
				continue
			}
			seen[v.Claim] = true
			if !wroteVolHdr {
				b.WriteString("\n" + uiHdrStyle.Render("Volumes") + "\n")
				wroteVolHdr = true
			}
			b.WriteString(fmt.Sprintf("%-24s %s %5.1f%%  (%s/%s)\n",
				truncate(v.Claim, 24), bar(v.Pct, 16), v.Pct, fmtMem(v.Used), fmtMem(v.Capacity)))
		}
		b.WriteString("\n" + uiHdrStyle.Render("Recent events") + "\n")
		for _, e := range m.events[app] {
			b.WriteString(truncate(e, w-2) + "\n")
		}
		content = b.String()
	}
	return title + "\n" + content
}

func (m *uiModel) viewMetrics(key string, w int) string {
	h := m.hist[key]
	if len(h) == 0 {
		return uiDimStyle.Render("collecting samples...")
	}
	cpus := make([]int64, len(h))
	mems := make([]int64, len(h))
	var maxC, maxM int64
	for i, pt := range h {
		cpus[i], mems[i] = pt.cpu, pt.mem
		if pt.cpu > maxC {
			maxC = pt.cpu
		}
		if pt.mem > maxM {
			maxM = pt.mem
		}
	}
	cur := h[len(h)-1]
	sw := w - 2
	if sw > 100 {
		sw = 100
	}
	rxs := make([]int64, len(h))
	txs := make([]int64, len(h))
	var maxRx, maxTx float64
	for i, pt := range h {
		rxs[i], txs[i] = int64(pt.rx), int64(pt.tx)
		if pt.rx > maxRx {
			maxRx = pt.rx
		}
		if pt.tx > maxTx {
			maxTx = pt.tx
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "cpu  now %-8s peak %-8s (window %ds)\n", fmtCPU(cur.cpu), fmtCPU(maxC), len(h)*2)
	b.WriteString(lipgloss.NewStyle().Foreground(uiAmber).Render(sparkline(cpus, sw)) + "\n\n")
	fmt.Fprintf(&b, "mem  now %-8s peak %-8s\n", fmtMem(cur.mem), fmtMem(maxM))
	b.WriteString(lipgloss.NewStyle().Foreground(uiGood).Render(sparkline(mems, sw)) + "\n\n")
	fmt.Fprintf(&b, "net  ↓ now %-9s peak %-9s\n", fmtRate(cur.rx), fmtRate(maxRx))
	b.WriteString(lipgloss.NewStyle().Foreground(uiAmber).Render(sparkline(rxs, sw)) + "\n")
	fmt.Fprintf(&b, "net  ↑ now %-9s peak %-9s\n", fmtRate(cur.tx), fmtRate(maxTx))
	b.WriteString(lipgloss.NewStyle().Foreground(uiGood).Render(sparkline(txs, sw)) + "\n")
	return b.String()
}

func (m *uiModel) viewFooter() string {
	if m.confirm != nil {
		return uiHdrStyle.Render(" " + m.confirm.prompt)
	}
	keys := " j/k move · l logs · m metrics · s status · r restart · b rollback · e exec · a attach · q quit"
	line := uiDimStyle.Render(keys)
	if m.status != "" {
		line += "  " + uiHdrStyle.Render(m.status)
	}
	if m.err != "" {
		line += "  " + lipgloss.NewStyle().Foreground(uiBad).Render(m.err)
	}
	return line
}
