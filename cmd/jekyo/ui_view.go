package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	reflowtrunc "github.com/muesli/reflow/truncate"
)

var (
	uiAmber  = lipgloss.Color("214")
	uiDim    = lipgloss.Color("240")
	uiGood   = lipgloss.Color("42")
	uiBad    = lipgloss.Color("196")
	uiText   = lipgloss.Color("252")
	uiAccent = lipgloss.NewStyle().Foreground(uiAmber)
	uiGreen  = lipgloss.NewStyle().Foreground(uiGood)
	uiRed    = lipgloss.NewStyle().Foreground(uiBad)

	uiHdrStyle = lipgloss.NewStyle().Foreground(uiAmber).Bold(true)
	// labels must stay readable on dark terminals; 240 was too faint
	uiDimStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("246"))
	uiBorder   = lipgloss.NewStyle().Foreground(uiDim)
	uiSelStyle = lipgloss.NewStyle().Background(uiAmber).Foreground(lipgloss.Color("233")).Bold(true)

	uiPane = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(uiDim).Padding(0, 1)

	uiTabOn = lipgloss.NewStyle().Foreground(uiAmber).Bold(true).Padding(0, 1).Border(lipgloss.RoundedBorder(), false, false, true, false).BorderForeground(uiAmber)

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

// barCalm renders a meter without alarm thresholds, for metrics where a
// high value is healthy (Available, Free, Cached).
func barCalm(pctV float64, width int) string {
	if width <= 0 {
		return ""
	}
	if pctV < 0 {
		pctV = 0
	}
	if pctV > 100 {
		pctV = 100
	}
	filled := int(pctV/100*float64(width) + 0.5)
	return "\033[38;5;108m" + strings.Repeat("▄", filled) + "\033[38;5;238m" + strings.Repeat("▄", width-filled) + "\033[0m"
}

var sparkBlocks = []rune("▁▂▃▄▅▆▇█")

// brailleGrid is btop's graph texture: cell = left sample x right sample,
// each quantized to dot heights 0-4.
var brailleGrid = []rune(" ⢀⢠⢰⢸⡀⣀⣠⣰⣸⡄⣄⣤⣴⣼⡆⣆⣦⣶⣾⡇⣇⣧⣷⣿")

// braille renders vals as a btop-style density graph, two samples per
// character, scaled to the window's own max.
func braille(vals []int64, width int) string {
	if width <= 0 {
		return ""
	}
	need := width * 2
	if len(vals) > need {
		vals = vals[len(vals)-need:]
	}
	var max int64 = 1
	for _, v := range vals {
		if v > max {
			max = v
		}
	}
	lvl := func(v int64) int {
		l := int(float64(v)/float64(max)*4 + 0.5)
		if v > 0 && l == 0 {
			l = 1
		}
		if l > 4 {
			l = 4
		}
		return l
	}
	var b strings.Builder
	pairs := (len(vals) + 1) / 2
	for i := 0; i < width-pairs; i++ {
		b.WriteRune(' ')
	}
	for i := 0; i < len(vals); i += 2 {
		l := lvl(vals[i])
		r := 0
		if i+1 < len(vals) {
			r = lvl(vals[i+1])
		}
		b.WriteRune(brailleGrid[l*5+r])
	}
	return b.String()
}

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

// minimum size below which the layout stops making sense, btop-style
const minCols, minRows = 90, 24

func (m *uiModel) View() string {
	if m.width == 0 {
		return "loading..."
	}
	if m.width < minCols || m.height < minRows {
		msg := lipgloss.JoinVertical(lipgloss.Center,
			uiHdrStyle.Render("Terminal too small"),
			uiDimStyle.Render(fmt.Sprintf("need %dx%d, have %dx%d", minCols, minRows, m.width, m.height)),
			uiDimStyle.Render("resize the window or zoom out"),
		)
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, msg)
	}
	// first snapshot still loading: show the logomark splash if it fits
	if len(m.snap.Nodes) == 0 && len(m.rows) == 0 && m.err == "" && m.height >= len(uiSplash)+4 {
		return m.splash()
	}
	leftW := 36
	if m.width < 90 {
		leftW = m.width / 3
	}
	rightW := m.width - leftW - 7
	header := m.viewHeader()
	bodyH := m.height - lipgloss.Height(header) - 3
	if m.picker != nil {
		modal := lipgloss.Place(m.width, bodyH+2, lipgloss.Center, lipgloss.Center, m.viewPicker())
		return header + "\n" + modal + "\n" + m.viewFooter()
	}
	left := uiPane.Width(leftW).Height(bodyH).Render(m.viewTree(leftW, bodyH))
	right := uiPane.Width(rightW).Height(bodyH).Render(m.viewRight(rightW-2, bodyH))
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
	return header + "\n" + body + "\n" + m.viewFooter()
}

// viewHeader composes btop-style titled boxes: a cpu box with the core
// grid, then mem, disks, and net boxes side by side.
func (m *uiModel) viewHeader() string {
	w := m.width - 2
	identity := uiHdrStyle.Render(" " + m.ic("server") + " " + m.ctxName)
	if len(m.snap.Nodes) == 0 {
		return boxWithTitle(identity+" ", "", []string{uiDimStyle.Render("connecting to the cluster...")}, w)
	}
	n := m.snap.Nodes[0]
	// the node usually shares the context's name; only show it when it adds
	// information
	right := " "
	if n.Name != m.ctxName {
		right += uiDimStyle.Render(n.Name + " · ")
	}
	right += uiDimStyle.Render(fmt.Sprintf("%d pods", n.PodCount))
	if m.uptime != "" {
		right += uiDimStyle.Render(" · up " + m.uptime)
	}
	right += " "

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

	// cpu box: summary row plus the per-core grid
	sum := fmt.Sprintf("%s %s %5.1f%%  %s",
		uiDimStyle.Render("total"), bar(n.CPUPct, 16), n.CPUPct, uiAccent.Render(braille(cpus, 16)))
	if n.MetricsMissed {
		sum += "  " + uiRed.Render(m.ic("warn")+" metrics warming up")
	}
	cpuRows := []string{sum}
	if len(m.cores) > 1 {
		cell := 16
		ncols := (w - 4) / cell
		if ncols < 1 {
			ncols = 1
		}
		if ncols > 8 {
			ncols = 8
		}
		nrows := (len(m.cores) + ncols - 1) / ncols
		for r := 0; r < nrows; r++ {
			var line strings.Builder
			for c := 0; c < ncols; c++ {
				i := c*nrows + r
				if i >= len(m.cores) {
					continue
				}
				line.WriteString(fmt.Sprintf("%s %s %3.0f%%  ",
					uiDimStyle.Render(fmt.Sprintf("C%-2d", i)), bar(m.cores[i], 6), m.cores[i]))
			}
			cpuRows = append(cpuRows, strings.TrimRight(line.String(), " "))
		}
	}
	platW := 38
	cpuW := w - platW - 1
	failing := 0
	for _, p := range m.snap.Pods {
		if p.Status != "Running" {
			failing++
		}
	}
	apps := map[string]bool{}
	for _, p := range m.snap.Pods {
		apps[p.App] = true
	}
	podsLine := fmt.Sprintf("%s %d  %s %d", uiDimStyle.Render("apps"), len(apps), uiDimStyle.Render("pods"), n.PodCount)
	if failing > 0 {
		podsLine += "  " + uiRed.Render(fmt.Sprintf("%s %d failing", m.ic("warn"), failing))
	} else {
		podsLine += "  " + uiGreen.Render(m.ic("ok")+" all healthy")
	}
	platRows := []string{
		podsLine,
		fmt.Sprintf("%s %d %s %s %d", uiDimStyle.Render("services"), m.snap.Services,
			uiDimStyle.Render("·"), uiDimStyle.Render("domains"), m.snap.Domains),
	}
	if m.domain != "" {
		platRows = append(platRows, uiDimStyle.Render("domain   ")+uiHdrStyle.Render(m.domain))
	}
	platRows = append(platRows,
		uiDimStyle.Render("k8s ")+n.K8sVersion+uiDimStyle.Render("  jekyo ")+version)

	bline := uiDimStyle.Render("backups  ")
	if len(m.snap.Backups) == 0 {
		bline += uiDimStyle.Render("none configured")
	} else {
		overdue := 0
		for _, b := range m.snap.Backups {
			if b.Overdue {
				overdue++
			}
		}
		if overdue > 0 {
			bline += uiRed.Render(fmt.Sprintf("%s %d of %d overdue", m.ic("warn"), overdue, len(m.snap.Backups)))
		} else {
			bline += uiGreen.Render(fmt.Sprintf("%s %d volumes fresh", m.ic("ok"), len(m.snap.Backups)))
		}
	}
	platRows = append(platRows, bline)

	upd := uiDimStyle.Render("updates  ")
	switch {
	case m.updSec > 0:
		upd += uiAccent.Render(fmt.Sprintf("%d pending (%d security)", m.updTotal, m.updSec))
	case m.updTotal > 0:
		upd += fmt.Sprintf("%d pending", m.updTotal)
	default:
		upd += uiGreen.Render("up to date")
	}
	secRows := []string{upd}
	var alerts []string
	if m.reboot {
		alerts = append(alerts, uiRed.Render(m.ic("warn")+" reboot required"))
	}
	if m.sshFails > 20 {
		alerts = append(alerts, uiAccent.Render(fmt.Sprintf("ssh fails %d", m.sshFails)))
	} else if m.sshFails >= 0 {
		alerts = append(alerts, uiDimStyle.Render("ssh fails ")+fmt.Sprintf("%d", m.sshFails))
	}
	if len(alerts) > 0 {
		secRows = append(secRows, strings.Join(alerts, uiDimStyle.Render(" · ")))
	}

	// the server column stacks two boxes; keep it flush with the cpu box
	colH := len(platRows) + 2 + len(secRows) + 2
	for len(cpuRows)+2 < colH {
		cpuRows = append(cpuRows, "")
	}
	for colH < len(cpuRows)+2 {
		secRows = append(secRows, "")
		colH++
	}
	// brand box: who we are, which version, whether a newer one exists;
	// narrow terminals get the data, not the branding
	brandW := 24
	showBrand := m.width >= 132
	if showBrand {
		cpuW -= brandW + 1
	}
	center := func(str string) string {
		pad := (brandW - 4 - lipgloss.Width(str)) / 2
		if pad < 0 {
			pad = 0
		}
		return strings.Repeat(" ", pad) + str
	}
	brandRows := []string{
		"",
		center(uiHdrStyle.Render("Stop doing ops.")),
		"",
		center(uiDimStyle.Render("v") + version),
	}
	if m.latest != "" && m.latest != "v"+version && version != "dev" {
		brandRows = append(brandRows,
			center(uiAccent.Render(m.latest+" available")),
			center(uiAccent.Render("run: jekyo update")))
	}

	platCol := boxWithTitle(btopTitle("⁰", "server"), "", platRows, platW) + "\n" +
		boxWithTitle(btopTitle("⁶", "security"), "", secRows, platW)
	colTarget := len(cpuRows) + 2
	if len(brandRows)+2 > colTarget {
		colTarget = len(brandRows) + 2
	}
	for len(cpuRows)+2 < colTarget {
		cpuRows = append(cpuRows, "")
	}
	for len(brandRows)+2 < colTarget {
		brandRows = append(brandRows, "")
	}
	parts := []string{}
	if showBrand {
		parts = append(parts, boxWithTitle(uiHdrStyle.Render(" JEKYO "), "", brandRows, brandW), " ")
	}
	parts = append(parts,
		boxWithTitle(btopTitle("¹", "cpu")+uiDimStyle.Render("─· ")+identity+" ", right, cpuRows, cpuW),
		" ", platCol)
	cpuBox := lipgloss.JoinHorizontal(lipgloss.Top, parts...)

	// bottom row: mem, disks, net side by side
	memW := (w - 2) * 30 / 100
	netW := (w - 2) * 34 / 100
	dskW := w - 2 - memW - netW
	var memRows []string
	if m.mem.total > 0 {
		mr := func(label string, v int64, calm bool) string {
			pv := pct(v, m.mem.total)
			meter := bar(pv, memW-30)
			if calm {
				meter = barCalm(pv, memW-30)
			}
			return fmt.Sprintf("%s %8s %s %3.0f%%",
				uiDimStyle.Render(fmt.Sprintf("%-10s", label+":")), fmtMem(v), meter, pv)
		}
		memRows = []string{
			uiDimStyle.Render(fmt.Sprintf("%-10s", "Total:")) + fmt.Sprintf(" %8s ", fmtMem(m.mem.total)) + uiGreen.Render(braille(mems, memW-24)),
			mr("Used", m.mem.used, false),
			mr("Available", m.mem.avail, true),
			mr("Cached", m.mem.cached, true),
			mr("Free", m.mem.free, true),
		}
		if m.swapT > 0 {
			sp := pct(m.swapU, m.swapT)
			memRows = append(memRows, fmt.Sprintf("%s %8s %s %3.0f%%",
				uiDimStyle.Render(fmt.Sprintf("%-10s", "Swap:")), fmtMem(m.swapU), bar(sp, memW-30), sp))
		}
	} else {
		memRows = []string{
			fmt.Sprintf("%s %5.1f%%", bar(n.MemPct, memW-12), n.MemPct),
			uiDimStyle.Render(fmt.Sprintf("%s / %s  ", fmtMem(n.MemBytes), fmtMem(n.MemCapBytes))) + uiGreen.Render(sparkline(mems, 10)),
		}
	}
	var dskRows []string
	if len(m.mounts) > 0 {
		for _, mt := range m.mounts {
			pv := pct(mt.used, mt.size)
			dskRows = append(dskRows,
				fmt.Sprintf("%s %s", uiHdrStyle.Render(truncateStr(mt.target, 18)), uiDimStyle.Render(fmtMem(mt.size))),
				fmt.Sprintf("%s %s %3.0f%% %s", uiDimStyle.Render("Used:"), bar(pv, dskW-26), pv, fmtMem(mt.used)))
		}
	} else {
		dskRows = append(dskRows, fmt.Sprintf("%s %5.1f%% %s", bar(n.DiskPct, dskW-18), n.DiskPct,
			uiDimStyle.Render(fmt.Sprintf("%s/%s", fmtMem(n.DiskUsed), fmtMem(n.DiskCap)))))
	}
	netRows := []string{
		fmt.Sprintf("%s %-9s %s", uiAccent.Render("▼"), fmtRate(curRx), uiAccent.Render(braille(rxs, netW-30))+uiDimStyle.Render(" Total: ")+fmtMem(n.NetRxBytes)),
		fmt.Sprintf("%s %-9s %s", uiGreen.Render("▲"), fmtRate(curTx), uiGreen.Render(braille(txs, netW-30))+uiDimStyle.Render(" Total: ")+fmtMem(n.NetTxBytes)),
	}
	// gpu shares the net column; both are short
	var gpuRows []string
	if m.gpu.present {
		gp := float64(m.gpu.util)
		gpuRows = []string{fmt.Sprintf("%s %3.0f%%  %s  %s",
			bar(gp, netW-32), gp,
			uiDimStyle.Render(fmt.Sprintf("%dMi/%dMi", m.gpu.memUsed, m.gpu.memTot)),
			uiDimStyle.Render(fmt.Sprintf("%d°C", m.gpu.temp)))}
	} else {
		gpuRows = []string{uiDimStyle.Render("no GPU available")}
	}

	// flush bottom edge: pad the shorter columns
	memH := len(memRows) + 2
	dskH := len(dskRows) + 2
	netColH := len(netRows) + 2 + len(gpuRows) + 2
	target := memH
	if dskH > target {
		target = dskH
	}
	if netColH > target {
		target = netColH
	}
	for len(memRows)+2 < target {
		memRows = append(memRows, "")
	}
	for len(dskRows)+2 < target {
		dskRows = append(dskRows, "")
	}
	for len(netRows)+2+len(gpuRows)+2 < target {
		gpuRows = append(gpuRows, "")
	}
	memBox := boxWithTitle(btopTitle("²", "mem"), "", memRows, memW)
	ioTitle := uiDimStyle.Render(fmt.Sprintf(" io r %s w %s ", fmtRate(m.io.readBps), fmtRate(m.io.writeBps)))
	dskBox := boxWithTitle(btopTitle("³", "disks"), ioTitle, dskRows, dskW)
	netCol := boxWithTitle(btopTitle("⁴", "net"), "", netRows, netW) + "\n" +
		boxWithTitle(btopTitle("⁵", "gpu"), "", gpuRows, netW)
	return cpuBox + "\n" + lipgloss.JoinHorizontal(lipgloss.Top, memBox, " ", dskBox, " ", netCol)
}

// btopTitle renders a border-fused box label like btop's ¹cpu.
func btopTitle(sup, label string) string {
	return uiAccent.Render(sup) + uiHdrStyle.Render(label)
}

// boxWithTitle draws a rounded box with the title embedded in the top
// border, btop-style; right lands at the border's right end.
func boxWithTitle(title, right string, rows []string, w int) string {
	inner := w - 2
	tw := lipgloss.Width(title)
	rw := lipgloss.Width(right)
	rest := inner - tw - rw - 2
	if rest < 0 {
		rest = 0
	}
	bs := uiBorder
	var b strings.Builder
	b.WriteString(bs.Render("╭─") + title + bs.Render(strings.Repeat("─", rest)) + right + bs.Render("─╮") + "\n")
	for _, r := range rows {
		if lipgloss.Width(r) > inner-1 {
			r = reflowtrunc.String(r, uint(inner-1))
		}
		pad := inner - lipgloss.Width(r) - 1
		if pad < 0 {
			pad = 0
		}
		b.WriteString(bs.Render("│") + " " + r + strings.Repeat(" ", pad) + bs.Render("│") + "\n")
	}
	b.WriteString(bs.Render("╰" + strings.Repeat("─", inner) + "╯"))
	return b.String()
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

	strip := ""
	stripH := 0
	if m.graphs && ok && h >= 12 {
		strip = m.viewMetricStrip(key, w)
		stripH = lipgloss.Height(strip)
	}

	label := m.ic("logs") + " logs"
	if m.tab == tabStatus {
		label = m.ic("info") + " status"
	}
	title := uiTabOn.Render(label)
	if ok {
		title = lipgloss.JoinHorizontal(lipgloss.Center, title, uiDimStyle.Render("  ·  "), uiHdrStyle.Render(key))
	}

	avail := h - stripH - 3
	content := ""
	switch m.tab {
	case tabStatus:
		content = m.viewStatus(app, w)
	default:
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
	}
	if strip != "" {
		return strip + "\n" + title + "\n" + content
	}
	return title + "\n" + content
}

// viewMetricStrip is the always-visible btop row for the selected
// service: cpu, mem, and traffic boxes with braille history.
func (m *uiModel) viewMetricStrip(key string, w int) string {
	h := m.hist[key]
	boxW := (w - 4) / 3
	if len(h) == 0 {
		return boxWithTitle(btopTitle("∿", " collecting"), "", []string{uiDimStyle.Render("sampling " + key + "...")}, w-2)
	}
	cpus := make([]int64, len(h))
	mems := make([]int64, len(h))
	rxs := make([]int64, len(h))
	txs := make([]int64, len(h))
	var maxC, maxM int64
	for i, pt := range h {
		cpus[i], mems[i] = pt.cpu, pt.mem
		rxs[i], txs[i] = int64(pt.rx), int64(pt.tx)
		if pt.cpu > maxC {
			maxC = pt.cpu
		}
		if pt.mem > maxM {
			maxM = pt.mem
		}
	}
	cur := h[len(h)-1]
	gw := boxW - 4
	stat := func(nowV, peakV string) string {
		if boxW >= 26 {
			return fmt.Sprintf("%s %-8s %s %-8s", uiDimStyle.Render("now"), nowV, uiDimStyle.Render("peak"), peakV)
		}
		return uiDimStyle.Render("now ") + nowV
	}
	cpuRows := []string{
		uiAccent.Render(braille(cpus, gw)),
		stat(fmtCPU(cur.cpu), fmtCPU(maxC)),
	}
	memRows := []string{
		uiGreen.Render(braille(mems, gw)),
		stat(fmtMem(cur.mem), fmtMem(maxM)),
	}
	netRows := []string{
		uiAccent.Render(braille(rxs, gw)),
		fmt.Sprintf("%s %-9s %s %-9s", uiAccent.Render("▼"), fmtRate(cur.rx), uiGreen.Render("▲"), fmtRate(cur.tx)),
	}
	_ = txs
	return lipgloss.JoinHorizontal(lipgloss.Top,
		boxWithTitle(btopTitle("", m.ic("cpu")+" cpu"), "", cpuRows, boxW), " ",
		boxWithTitle(btopTitle("", m.ic("mem")+" mem"), "", memRows, boxW), " ",
		boxWithTitle(btopTitle("", m.ic("net")+" net"), "", netRows, w-4-2*boxW),
	)
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

func (m *uiModel) viewFooter() string {
	if m.confirm != nil {
		return lipgloss.NewStyle().Background(uiAmber).Foreground(lipgloss.Color("232")).Bold(true).
			Padding(0, 1).Render(m.confirm.prompt)
	}
	keys := []struct{ k, label string }{
		{"j/k", "move"}, {"l", "logs"}, {"s", "status"}, {"m", "graphs"},
		{"r", "restart"}, {"b", "rollback"}, {"e", "exec"}, {"a", "attach"}, {"q", "quit"},
	}
	parts := make([]string, len(keys))
	for i, kv := range keys {
		parts[i] = uiKey.Render(kv.k) + uiLabel.Render(":"+kv.label)
	}
	line := " " + strings.Join(parts, "  ")
	if m.status != "" {
		line += "   " + uiHdrStyle.Render(m.status)
	}
	if m.err != "" {
		line += "   " + uiRed.Render(m.ic("warn")+" "+m.err)
	}
	var vitals []string
	if m.tempC > 0 {
		vitals = append(vitals, uiLabel.Render("temp ")+fmt.Sprintf("%d°C", m.tempC))
	}
	if m.load != "" {
		vitals = append(vitals, uiLabel.Render("load ")+m.load)
	}
	if m.snap != nil && len(m.snap.Nodes) > 0 {
		vitals = append(vitals, uiLabel.Render("api ")+fmt.Sprintf("%dms", m.snap.APIMs))
		if m.snap.CertDays >= 0 {
			c := uiLabel.Render("certs ") + fmt.Sprintf("%dd", m.snap.CertDays)
			if m.snap.CertDays < 14 {
				c = uiRed.Render(fmt.Sprintf("%s certs %dd", m.ic("warn"), m.snap.CertDays))
			}
			vitals = append(vitals, c)
		}
	}
	if len(vitals) > 0 {
		right := strings.Join(vitals, "    ")
		pad := m.width - lipgloss.Width(line) - lipgloss.Width(right) - 2
		if pad > 1 {
			line += strings.Repeat(" ", pad) + right
		}
	}
	return line
}

// truncateStr is truncate with a different name kept for box labels.
func truncateStr(s string, n int) string { return truncate(s, n) }

// viewPicker is the rollback revision chooser: newest first, the current
// revision marked, enter confirms.
func (m *uiModel) viewPicker() string {
	pk := m.picker
	var rows []string
	for i, r := range pk.revs {
		age := age(r.DeployedAt)
		line := fmt.Sprintf(" v%-3d %-10s ", r.Revision, age+" ago")
		if i == 0 {
			line += uiGreen.Render("current ")
		} else {
			line += "        "
		}
		if i == pk.idx {
			line = uiKey.Render("❯") + uiSelStyle.Render(line)
		} else {
			line = " " + line
		}
		rows = append(rows, line)
	}
	rows = append(rows, "", uiDimStyle.Render(" enter roll back · j/k move · esc cancel"))
	return boxWithTitle(uiHdrStyle.Render(" ↺ rollback "+pk.app+" "), "", rows, 48)
}
