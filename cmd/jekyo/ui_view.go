package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	uiAmber  = lipgloss.Color("214")
	uiDim    = lipgloss.Color("240")
	uiFaint  = lipgloss.Color("236")
	uiGood   = lipgloss.Color("42")
	uiBad    = lipgloss.Color("196")
	uiText   = lipgloss.Color("252")
	uiAccent = lipgloss.NewStyle().Foreground(uiAmber)
	uiGreen  = lipgloss.NewStyle().Foreground(uiGood)
	uiRed    = lipgloss.NewStyle().Foreground(uiBad)

	uiHdrStyle = lipgloss.NewStyle().Foreground(uiAmber).Bold(true)
	uiDimStyle = lipgloss.NewStyle().Foreground(uiDim)
	uiSelStyle = lipgloss.NewStyle().Background(lipgloss.Color("238")).Foreground(lipgloss.Color("230")).Bold(true)

	uiPane = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(uiDim).Padding(0, 1)

	uiTabOn  = lipgloss.NewStyle().Foreground(uiAmber).Bold(true).Padding(0, 1).Border(lipgloss.RoundedBorder(), false, false, true, false).BorderForeground(uiAmber)
	uiTabOff = lipgloss.NewStyle().Foreground(uiDim).Padding(0, 1)

	uiKey   = lipgloss.NewStyle().Foreground(uiAmber)
	uiLabel = lipgloss.NewStyle().Foreground(uiDim)
)

// Nerd Font glyphs from the FontAwesome range; opt-in via --nerd since
// they need a patched terminal font.
var nfIcons = map[string]string{
	"server": "", // server rack
	"cpu":    "", // microchip
	"mem":    "", // memory
	"disk":   "", // hard drive
	"net":    "", // exchange arrows
	"app":    "", // cube
	"logs":   "", // file text
	"chart":  "", // bar chart
	"info":   "", // info circle
	"vol":    "", // database
	"ok":     "", // check
	"warn":   "", // warning triangle
}

// baseIcons render in every monospace font (plain geometric Unicode);
// this is the default so the UI never shows tofu boxes.
var baseIcons = map[string]string{
	"server": "⎈", "cpu": "⚙", "mem": "▦", "disk": "◫", "net": "⇅",
	"app": "▣", "logs": "≣", "chart": "∿", "info": "ⓘ", "vol": "▥",
	"ok": "✓", "warn": "▲",
}

func (m *uiModel) ic(name string) string {
	if m.nerd {
		return nfIcons[name]
	}
	return baseIcons[name]
}

// uiLogo is the 3-line JEKYO wordmark drawn with box glyphs.
var uiLogo = []string{
	" ┓ ┏┓ ┓┏ ┓┏ ┏┓",
	" ┃ ┣  ┣┫ ┗┫ ┃┃",
	"┗┛ ┗┛ ┛┗  ┛ ┗┛",
}

// uiSplash is the JEKYO logomark on a starfield, shown while the first
// snapshot loads.
var uiSplash = []string{
	` ...  ..      .   .. ..  .. . . . .  ...`,
	` .     .      .   . .   ..       .  ..  `,
	`     . ..... .      .    . . . ..      .`,
	`  ... .. .   ...      ..    ...       ..`,
	`  .   . . .   .  .   .    .  .  .   .  .`,
	`  . . .  . .000OOOO00000Q  QQQQL  .  .. `,
	` .   .  ..0OOOOOOO0000000 .QQQLL     .  `,
	`    .    OOOOOO0.   .. .                `,
	` ..     OOO0O   .. . QQ'...LLLLC  . . ..`,
	` .. . .0OOOO    ..  .QQLQL LCCCC  .     `,
	` ..   .O000O  ..    .0QLLLLLCCCJ    .   `,
	`  . .  O0000  . .  . .  QLCCCJJJ  . ... `,
	`  .. . .000QQ  .    ..  .CCCJJJU........`,
	` ...    QQQQQQL.     .CCJJJJJJUU.   .   `,
	`  .       QQQQLLLLLL CCCJJJJUUUY  .     `,
	` ..  . ..   QQLLLLCC JCJ(  YUYYX.  ..   `,
	`      .. .     .jLCJ      .YYYYY    .  .`,
	`  .   . .. .. ..  .      . ...      . . `,
	` .  .     .    .   . ... ..  .. .    .  `,
}

// splash colorizes the logomark amber over a dim starfield and centers
// it with a caption.
func (m *uiModel) splash() string {
	const dim, amber, reset = "\033[38;5;240m", "\033[38;5;214m", "\033[0m"
	var art strings.Builder
	for _, line := range uiSplash {
		for _, r := range line {
			if r == '.' || r == ' ' {
				art.WriteString(dim + string(r))
			} else {
				art.WriteString(amber + string(r))
			}
		}
		art.WriteString(reset + "\n")
	}
	caption := lipgloss.JoinVertical(lipgloss.Center,
		art.String(),
		uiHdrStyle.Render("JEKYO"),
		uiDimStyle.Render("connecting to "+m.ctxName+"..."),
	)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, caption)
}

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
	// first snapshot still loading: show the logomark splash if it fits
	if len(m.snap.Nodes) == 0 && len(m.rows) == 0 && m.err == "" && m.height >= len(uiSplash)+4 {
		return m.splash()
	}
	leftW := 36
	if m.width < 90 {
		leftW = m.width / 3
	}
	rightW := m.width - leftW - 6
	headerH := 5 // 3 content lines + border
	bodyH := m.height - headerH - 3

	header := m.viewHeader()
	left := uiPane.Width(leftW).Height(bodyH).Render(m.viewTree(leftW, bodyH))
	right := uiPane.Width(rightW).Height(bodyH).Render(m.viewRight(rightW-2, bodyH))
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	return header + "\n" + body + "\n" + m.viewFooter()
}

// viewHeader is the boxed btop-style server strip: the JEKYO wordmark on
// the left, then identity, cpu/mem, and dsk/net gauge rows with history
// sparklines.
func (m *uiModel) viewHeader() string {
	w := m.width - 4
	logo := uiAccent.Bold(true).Render(strings.Join(uiLogo, "\n"))

	var info string
	if len(m.snap.Nodes) == 0 {
		info = m.ic("server") + " " + m.ctxName + "\n" + uiDimStyle.Render("connecting to the cluster...")
	} else {
		n := m.snap.Nodes[0]
		title := m.ic("server") + " " + uiHdrStyle.Render(m.ctxName) +
			uiDimStyle.Render(fmt.Sprintf("  ·  %s  ·  %d pods", n.Name, n.PodCount))
		if n.MetricsMissed {
			title += "   " + uiRed.Render(m.ic("warn") + " metrics warming up")
		}

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

		gap := strings.Repeat(" ", 6)
		sw := 16
		line2 := fmt.Sprintf("%s cpu %s %5.1f%%  %s%s%s mem %s %5.1f%%  %s",
			m.ic("cpu"), bar(n.CPUPct, 14), n.CPUPct, uiAccent.Render(sparkline(cpus, sw)),
			gap,
			m.ic("mem"), bar(n.MemPct, 14), n.MemPct, uiGreen.Render(sparkline(mems, sw)))
		line3 := fmt.Sprintf("%s dsk %s %5.1f%%  %-17s%s%s net ↓ %-8s %s  ↑ %-8s %s",
			m.ic("disk"), bar(n.DiskPct, 14), n.DiskPct,
			fmt.Sprintf("(%s/%s)", fmtMem(n.DiskUsed), fmtMem(n.DiskCap)),
			gap,
			m.ic("net"), fmtRate(curRx), uiAccent.Render(sparkline(rxs, 10)),
			fmtRate(curTx), uiGreen.Render(sparkline(txs, 10)))
		info = title + "\n" + line2 + "\n" + line3
	}
	inner := lipgloss.JoinHorizontal(lipgloss.Center, logo, "    ", info)
	return uiPane.Width(w).Render(inner)
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
	inner := w - 2
	var b strings.Builder
	// column header aligned with the row layout below
	b.WriteString(uiDimStyle.Render(fmt.Sprintf("    %-14s %6s %7s", "SERVICE", "CPU", "MEM")) + "\n")
	lines := 1
	for i, r := range m.rows {
		if lines >= h-1 {
			break
		}
		if r.header {
			if i > 0 {
				b.WriteString("\n")
				lines++
			}
			b.WriteString(uiHdrStyle.Render(m.ic("app")+" "+r.app) + "\n")
		} else {
			p := byKey[r.app+"/"+r.service]
			dot := uiGreen.Render("●")
			if p.Status != "Running" {
				dot = uiRed.Render("●")
			}
			body := fmt.Sprintf("%-14s %6s %7s ", truncate(r.service, 14), fmtCPU(p.CPUMilli), fmtMem(p.MemBytes))
			pad := inner - lipgloss.Width(body) - 4
			if pad > 0 {
				body += strings.Repeat(" ", pad)
			}
			if i == m.cursor {
				// pointer + highlight band makes the selection unmissable
				b.WriteString(uiKey.Render("❯") + " " + dot + " " + uiSelStyle.Render(body) + "\n")
			} else {
				b.WriteString("  " + dot + " " + lipgloss.NewStyle().Foreground(uiText).Render(body) + "\n")
			}
		}
		lines++
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *uiModel) viewRight(w, h int) string {
	app, svc, ok := m.selected()
	key := app + "/" + svc

	tabs := []struct {
		icon, label, key string
		id               int
	}{
		{m.ic("logs"), "logs", "l", tabLogs},
		{m.ic("chart"), "metrics", "m", tabMetrics},
		{m.ic("info"), "status", "s", tabStatus},
	}
	var tb []string
	for _, t := range tabs {
		if t.id == m.tab {
			tb = append(tb, uiTabOn.Render(t.icon+" "+t.label))
		} else {
			// show the key on inactive tabs so the jump is discoverable
			tb = append(tb, uiTabOff.Render(t.icon+" "+t.label+" ")+uiKey.Render(t.key))
		}
	}
	title := lipgloss.JoinHorizontal(lipgloss.Bottom, tb...)
	if ok {
		title = lipgloss.JoinHorizontal(lipgloss.Center, title, uiDimStyle.Render("  ·  "), uiHdrStyle.Render(key))
	}

	avail := h - 3 // tab bar takes two rows
	content := ""
	switch m.tab {
	case tabLogs:
		lines := m.logs[key]
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
		content = m.viewMetrics(key, w-2)
	case tabStatus:
		content = m.viewStatus(app, w)
	}
	return title + "\n" + content
}

func (m *uiModel) viewStatus(app string, w int) string {
	var b strings.Builder
	for _, p := range m.snap.Pods {
		if p.App != app {
			continue
		}
		mark := uiGreen.Render(m.ic("ok"))
		if p.Status != "Running" {
			mark = uiRed.Render(m.ic("warn"))
		}
		b.WriteString(fmt.Sprintf("%s %-14s %-6s %-16s restarts %d   cpu %s   mem %s\n",
			mark, p.Service, p.Ready, p.Status, p.Restarts, fmtCPU(p.CPUMilli), fmtMem(p.MemBytes)))
	}
	seen := map[string]bool{}
	wroteVolHdr := false
	for _, v := range m.snap.Volumes {
		if v.App != app || seen[v.Claim] || v.Capacity == 0 {
			continue
		}
		seen[v.Claim] = true
		if !wroteVolHdr {
			b.WriteString("\n" + uiHdrStyle.Render(m.ic("vol")+" Volumes") + "\n")
			wroteVolHdr = true
		}
		b.WriteString(fmt.Sprintf("%-24s %s %5.1f%%  (%s/%s)\n",
			truncate(v.Claim, 24), bar(v.Pct, 16), v.Pct, fmtMem(v.Used), fmtMem(v.Capacity)))
	}
	b.WriteString("\n" + uiHdrStyle.Render(m.ic("info")+" Recent events") + "\n")
	if len(m.events[app]) == 0 {
		b.WriteString(uiDimStyle.Render("loading events...") + "\n")
	}
	for _, e := range m.events[app] {
		b.WriteString(truncate(e, w-2) + "\n")
	}
	return b.String()
}

func (m *uiModel) viewMetrics(key string, w int) string {
	h := m.hist[key]
	if len(h) == 0 {
		return uiDimStyle.Render("collecting samples...")
	}
	cpus := make([]int64, len(h))
	mems := make([]int64, len(h))
	rxs := make([]int64, len(h))
	txs := make([]int64, len(h))
	var maxC, maxM int64
	var maxRx, maxTx float64
	for i, pt := range h {
		cpus[i], mems[i] = pt.cpu, pt.mem
		rxs[i], txs[i] = int64(pt.rx), int64(pt.tx)
		if pt.cpu > maxC {
			maxC = pt.cpu
		}
		if pt.mem > maxM {
			maxM = pt.mem
		}
		if pt.rx > maxRx {
			maxRx = pt.rx
		}
		if pt.tx > maxTx {
			maxTx = pt.tx
		}
	}
	cur := h[len(h)-1]
	sw := w - 2
	if sw > 100 {
		sw = 100
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s cpu   now %-8s peak %-8s %s\n", m.ic("cpu"), fmtCPU(cur.cpu), fmtCPU(maxC),
		uiDimStyle.Render(fmt.Sprintf("window %ds", len(h)*2)))
	b.WriteString(uiAccent.Render(sparkline(cpus, sw)) + "\n\n")
	fmt.Fprintf(&b, "%s mem   now %-8s peak %-8s\n", m.ic("mem"), fmtMem(cur.mem), fmtMem(maxM))
	b.WriteString(uiGreen.Render(sparkline(mems, sw)) + "\n\n")
	fmt.Fprintf(&b, "%s net ↓ now %-9s peak %-9s\n", m.ic("net"), fmtRate(cur.rx), fmtRate(maxRx))
	b.WriteString(uiAccent.Render(sparkline(rxs, sw)) + "\n")
	fmt.Fprintf(&b, "%s net ↑ now %-9s peak %-9s\n", m.ic("net"), fmtRate(cur.tx), fmtRate(maxTx))
	b.WriteString(uiGreen.Render(sparkline(txs, sw)) + "\n")
	return b.String()
}

func (m *uiModel) viewFooter() string {
	if m.confirm != nil {
		return lipgloss.NewStyle().Background(uiAmber).Foreground(lipgloss.Color("232")).Bold(true).
			Padding(0, 1).Render(m.confirm.prompt)
	}
	keys := []struct{ k, label string }{
		{"j/k", "move"}, {"l", "logs"}, {"m", "metrics"}, {"s", "status"},
		{"r", "restart"}, {"b", "rollback"}, {"e", "exec"}, {"a", "attach"}, {"q", "quit"},
	}
	parts := make([]string, len(keys))
	for i, kv := range keys {
		parts[i] = uiKey.Render(kv.k) + uiLabel.Render(" "+kv.label)
	}
	line := " " + strings.Join(parts, uiLabel.Render("  ·  "))
	if m.status != "" {
		line += "   " + uiHdrStyle.Render(m.status)
	}
	if m.err != "" {
		line += "   " + uiRed.Render(m.ic("warn")+" "+m.err)
	}
	return line
}
