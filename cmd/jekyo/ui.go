package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/jekyo/jekyo/internal/compile"
	"github.com/jekyo/jekyo/internal/contexts"
	"github.com/jekyo/jekyo/internal/deploy"
	"github.com/jekyo/jekyo/internal/sshx"
)

// ---- messages ----

type snapMsg struct {
	snap   *topSnapshot
	load   string // "3.89 4.14 4.49" or empty
	uptime string // "12d" or empty
	cores  []float64  // per-core utilization 0-100
	mounts []mountRow // real filesystems with usage
	tempC  int        // package temperature, 0 = unknown
	mem    memInfo
	gpu    gpuInfo
	io     ioRates
	swapT  int64
	swapU  int64
	updTotal int
	updSec   int
	reboot   bool
	sshFails int
}

type ioRates struct {
	readBps, writeBps float64
}

type gpuInfo struct {
	present                    bool
	probed                     bool
	util, memUsed, memTot, temp int
}

type memInfo struct {
	total, used, free, avail, cached int64
}

type mountRow struct {
	target     string
	size, used int64
}
type snapErrMsg struct{ err error }
type tickMsg struct{}
type logLineMsg struct {
	key  string
	line string
}
type logDoneMsg struct{ key string }
type eventsMsg struct {
	app   string
	lines []string
}
type actionMsg struct{ text string }

// uiRow is one line of the left pane tree.
type uiRow struct {
	app     string
	service string // empty for app header rows
	header  bool
}

type histPt struct {
	cpu int64
	mem int64
	rx  float64 // bytes/sec
	tx  float64
}

type nodeHistPt struct {
	cpuPct, memPct float64
	rx, tx         float64 // bytes/sec
}

const (
	tabLogs = iota
	tabStatus
)

const (
	logBuf  = 400
	histBuf = 240
)

type confirmState struct {
	prompt string
	action tea.Cmd
}

type uiModel struct {
	d       *deploy.Deployer
	ctxName string
	domain  string
	nerd    bool
	sshc    *sshx.Client // best-effort; load average and uptime

	width, height int
	snap          *topSnapshot
	rows          []uiRow
	cursor        int
	tab           int

	logs      map[string][]string
	logCh     chan tea.Msg
	logCancel context.CancelFunc
	logKey    string

	hist     map[string][]histPt
	nodeHist []nodeHistPt
	prevSnap *topSnapshot
	events   map[string][]string

	graphs   bool
	load     string
	uptime   string
	cores    []float64
	mounts   []mountRow
	tempC    int
	mem      memInfo
	gpu      gpuInfo
	io       ioRates
	updTotal int
	updSec   int
	reboot   bool
	sshFails int
	swapT    int64
	swapU    int64
	prevDisk [2]uint64 // cumulative sectors read/written
	prevDiskAt time.Time
	prevStat map[int][2]uint64 // per-core total/idle counters

	confirm *confirmState
	status  string // one-line footer notice
	err     string
}

// ---- data commands ----

func (m *uiModel) gatherCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		snap, err := gatherTop(ctx, m.d, m.ctxName, "")
		if err != nil {
			return snapErrMsg{err}
		}
		msg := snapMsg{snap: snap}
		if m.sshc != nil {
			out, err := m.sshc.Run(
				"cat /proc/loadavg /proc/uptime 2>/dev/null; echo @@@; cat /proc/stat; echo @@@; cat /proc/meminfo; echo @@@; " +
					"df -B1 -x tmpfs -x devtmpfs -x overlay --output=target,size,used 2>/dev/null | tail -n +2; echo @@@; " +
					"cat /sys/class/hwmon/hwmon*/temp1_input 2>/dev/null | sort -rn | head -1; echo @@@; " +
					"nvidia-smi --query-gpu=utilization.gpu,memory.used,memory.total,temperature.gpu --format=csv,noheader,nounits 2>/dev/null || echo none; echo @@@; cat /proc/diskstats; echo @@@; " +
					"cat /var/lib/update-notifier/updates-available 2>/dev/null; [ -f /var/run/reboot-required ] && echo REBOOT-REQUIRED; echo @@@; " +
					"sudo -n grep -c 'Failed password' /var/log/auth.log 2>/dev/null || echo -1")
			if err == nil {
				parts := strings.Split(out, "@@@")
				if len(parts) > 0 {
					lines := strings.Split(strings.TrimSpace(parts[0]), "\n")
					if len(lines) > 0 {
						if f := strings.Fields(lines[0]); len(f) >= 3 {
							msg.load = f[0] + " " + f[1] + " " + f[2]
						}
					}
					if len(lines) > 1 {
						if f := strings.Fields(lines[1]); len(f) > 0 {
							var secs float64
							fmt.Sscanf(f[0], "%f", &secs)
							d := time.Duration(secs) * time.Second
							switch {
							case d >= 48*time.Hour:
								msg.uptime = fmt.Sprintf("%dd", int(d.Hours()/24))
							case d >= 2*time.Hour:
								msg.uptime = fmt.Sprintf("%dh", int(d.Hours()))
							default:
								msg.uptime = fmt.Sprintf("%dm", int(d.Minutes()))
							}
						}
					}
				}
				if len(parts) > 1 {
					msg.cores = m.perCore(parts[1])
				}
				if len(parts) > 2 {
					mi := map[string]int64{}
					for _, l := range strings.Split(parts[2], "\n") {
						f := strings.Fields(l)
						if len(f) >= 2 {
							var kb int64
							fmt.Sscanf(f[1], "%d", &kb)
							mi[strings.TrimSuffix(f[0], ":")] = kb * 1024
						}
					}
					msg.mem = memInfo{
						total: mi["MemTotal"], free: mi["MemFree"],
						avail: mi["MemAvailable"], cached: mi["Cached"],
					}
					msg.mem.used = msg.mem.total - msg.mem.avail
					msg.swapT = mi["SwapTotal"]
					msg.swapU = mi["SwapTotal"] - mi["SwapFree"]
				}
				if len(parts) > 3 {
					for _, l := range strings.Split(strings.TrimSpace(parts[3]), "\n") {
						f := strings.Fields(l)
						if len(f) != 3 {
							continue
						}
						var size, used int64
						fmt.Sscanf(f[1], "%d", &size)
						fmt.Sscanf(f[2], "%d", &used)
						// only real filesystems worth showing
						if size < 1<<30 || strings.Contains(f[0], "efivars") {
							continue
						}
						msg.mounts = append(msg.mounts, mountRow{target: f[0], size: size, used: used})
					}
				}
				if len(parts) > 4 {
					var milli int
					fmt.Sscanf(strings.TrimSpace(parts[4]), "%d", &milli)
					if milli > 1000 {
						msg.tempC = milli / 1000
					}
				}
				if len(parts) > 6 {
					var rd, wr uint64
					for _, l := range strings.Split(strings.TrimSpace(parts[6]), "\n") {
						f := strings.Fields(l)
						if len(f) < 10 {
							continue
						}
						name := f[2]
						whole := false
						if strings.HasPrefix(name, "nvme") && strings.Contains(name, "n") && !strings.Contains(name, "p") {
							whole = true
						} else if (strings.HasPrefix(name, "sd") || strings.HasPrefix(name, "vd")) && name[len(name)-1] >= 'a' && name[len(name)-1] <= 'z' {
							whole = true
						}
						if !whole {
							continue
						}
						var r, w uint64
						fmt.Sscanf(f[5], "%d", &r)
						fmt.Sscanf(f[9], "%d", &w)
						rd += r
						wr += w
					}
					now := time.Now()
					if !m.prevDiskAt.IsZero() && rd >= m.prevDisk[0] {
						dt := now.Sub(m.prevDiskAt).Seconds()
						if dt > 0 {
							msg.io = ioRates{
								readBps:  float64(rd-m.prevDisk[0]) * 512 / dt,
								writeBps: float64(wr-m.prevDisk[1]) * 512 / dt,
							}
						}
					}
					m.prevDisk = [2]uint64{rd, wr}
					m.prevDiskAt = now
				}
				if len(parts) > 7 {
					up := parts[7]
					for _, l := range strings.Split(up, "\n") {
						l = strings.TrimSpace(l)
						var n int
						if _, err := fmt.Sscanf(l, "%d updates can be applied", &n); err == nil {
							msg.updTotal = n
						}
						if _, err := fmt.Sscanf(l, "%d of these updates is a standard security update", &n); err == nil {
							msg.updSec += n
						}
						if _, err := fmt.Sscanf(l, "%d of these updates are standard security updates", &n); err == nil {
							msg.updSec += n
						}
					}
					msg.reboot = strings.Contains(up, "REBOOT-REQUIRED")
				}
				if len(parts) > 8 {
					msg.sshFails = -1
					fmt.Sscanf(strings.TrimSpace(parts[8]), "%d", &msg.sshFails)
				}
				if len(parts) > 5 {
					g := strings.TrimSpace(parts[5])
					msg.gpu.probed = true
					if g != "" && g != "none" {
						f := strings.Split(strings.Split(g, "\n")[0], ",")
						if len(f) >= 4 {
							msg.gpu.present = true
							fmt.Sscanf(strings.TrimSpace(f[0]), "%d", &msg.gpu.util)
							fmt.Sscanf(strings.TrimSpace(f[1]), "%d", &msg.gpu.memUsed)
							fmt.Sscanf(strings.TrimSpace(f[2]), "%d", &msg.gpu.memTot)
							fmt.Sscanf(strings.TrimSpace(f[3]), "%d", &msg.gpu.temp)
						}
					}
				}
			}
		}
		return msg
	}
}

func uiTick() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

func waitForLog(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

// startLogStream follows logs of every pod of app/svc into m.logCh.
func (m *uiModel) startLogStream(app, svc string) tea.Cmd {
	if m.logCancel != nil {
		m.logCancel()
	}
	key := app + "/" + svc
	m.logKey = key
	ctx, cancel := context.WithCancel(context.Background())
	m.logCancel = cancel
	ch := make(chan tea.Msg, 64)
	m.logCh = ch

	go func() {
		defer func() { ch <- logDoneMsg{key} }()
		ns := compile.NamespaceFor(app)
		pods, err := m.d.Client.Typed.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: podSelector(app, svc)})
		if err != nil || len(pods.Items) == 0 {
			return
		}
		tail := int64(120)
		for _, p := range pods.Items {
			opts := &corev1.PodLogOptions{Follow: true, TailLines: &tail}
			rc, err := m.d.Client.Typed.CoreV1().Pods(ns).GetLogs(p.Name, opts).Stream(ctx)
			if err != nil {
				continue
			}
			go func() {
				defer rc.Close()
				sc := bufio.NewScanner(rc)
				sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
				for sc.Scan() {
					select {
					case ch <- logLineMsg{key, sc.Text()}:
					case <-ctx.Done():
						return
					}
				}
			}()
		}
		<-ctx.Done()
	}()
	return waitForLog(ch)
}

func (m *uiModel) eventsCmd(app string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		evs, err := m.d.Client.Typed.CoreV1().Events(compile.NamespaceFor(app)).List(ctx, metav1.ListOptions{Limit: 40})
		if err != nil {
			return eventsMsg{app, []string{"events: " + err.Error()}}
		}
		items := evs.Items
		sort.Slice(items, func(i, j int) bool { return items[i].LastTimestamp.After(items[j].LastTimestamp.Time) })
		if len(items) > 15 {
			items = items[:15]
		}
		var lines []string
		for _, e := range items {
			lines = append(lines, fmt.Sprintf("%-7s %-22s %s  (%s ago)", e.Type, e.Reason,
				truncate(e.Message, 80), age(e.LastTimestamp.Time)))
		}
		if len(lines) == 0 {
			lines = []string{"No recent events."}
		}
		return eventsMsg{app, lines}
	}
}

func (m *uiModel) restartCmd(app, svc string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := restartWorkloads(ctx, m.d, app, svc); err != nil {
			return actionMsg{"restart failed: " + err.Error()}
		}
		return actionMsg{fmt.Sprintf("restarted %s/%s", app, svc)}
	}
}

// execSelf suspends the TUI and runs this same binary with args.
func execSelf(args ...string) tea.Cmd {
	if contextFlag != "" {
		args = append(args, "--context", contextFlag)
	}
	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}
	c := exec.Command(self, args...)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return actionMsg{err.Error()}
		}
		return actionMsg{""}
	})
}

// perCore turns /proc/stat cpuN counters into utilization percentages
// using the previous sample's counters.
func (m *uiModel) perCore(stat string) []float64 {
	if m.prevStat == nil {
		m.prevStat = map[int][2]uint64{}
	}
	var out []float64
	for _, l := range strings.Split(stat, "\n") {
		if !strings.HasPrefix(l, "cpu") || len(l) < 4 || l[3] == ' ' {
			continue
		}
		f := strings.Fields(l)
		var n int
		if _, err := fmt.Sscanf(f[0], "cpu%d", &n); err != nil {
			continue
		}
		var total, idle uint64
		for i, v := range f[1:] {
			var x uint64
			fmt.Sscanf(v, "%d", &x)
			total += x
			if i == 3 || i == 4 { // idle + iowait
				idle += x
			}
		}
		prev := m.prevStat[n]
		dTotal, dIdle := total-prev[0], idle-prev[1]
		pct := 0.0
		if prev[0] > 0 && dTotal > 0 {
			pct = 100 * float64(dTotal-dIdle) / float64(dTotal)
		}
		m.prevStat[n] = [2]uint64{total, idle}
		for len(out) <= n {
			out = append(out, 0)
		}
		out[n] = pct
	}
	return out
}

// ---- model ----

func (m *uiModel) Init() tea.Cmd {
	return tea.Batch(m.gatherCmd(), uiTick())
}

func (m *uiModel) selected() (app, svc string, ok bool) {
	if m.cursor >= 0 && m.cursor < len(m.rows) && !m.rows[m.cursor].header {
		return m.rows[m.cursor].app, m.rows[m.cursor].service, true
	}
	return "", "", false
}

func (m *uiModel) rebuildRows() {
	sel, selSvc, hadSel := m.selected()
	byApp := map[string][]string{}
	seen := map[string]bool{}
	for _, p := range m.snap.Pods {
		k := p.App + "/" + p.Service
		if !seen[k] {
			seen[k] = true
			byApp[p.App] = append(byApp[p.App], p.Service)
		}
	}
	apps := make([]string, 0, len(byApp))
	for a := range byApp {
		apps = append(apps, a)
	}
	sort.Strings(apps)
	m.rows = m.rows[:0]
	for _, a := range apps {
		m.rows = append(m.rows, uiRow{app: a, header: true})
		sort.Strings(byApp[a])
		for _, s := range byApp[a] {
			m.rows = append(m.rows, uiRow{app: a, service: s})
		}
	}
	// keep the selection stable across refreshes
	m.cursor = -1
	for i, r := range m.rows {
		if hadSel && r.app == sel && r.service == selSvc {
			m.cursor = i
			break
		}
	}
	if m.cursor == -1 {
		for i, r := range m.rows {
			if !r.header {
				m.cursor = i
				break
			}
		}
	}
}

func (m *uiModel) move(delta int) {
	if len(m.rows) == 0 {
		return
	}
	i := m.cursor
	for {
		i += delta
		if i < 0 || i >= len(m.rows) {
			return
		}
		if !m.rows[i].header {
			m.cursor = i
			return
		}
	}
}

func (m *uiModel) appendHist() {
	dt := 0.0
	prevPods := map[string]topPod{}
	prevNodes := map[string]topNode{}
	if m.prevSnap != nil {
		dt = m.snap.takenAt.Sub(m.prevSnap.takenAt).Seconds()
		for _, p := range m.prevSnap.Pods {
			prevPods[p.Pod] = p
		}
		for _, n := range m.prevSnap.Nodes {
			prevNodes[n.Name] = n
		}
	}

	perSvc := map[string]histPt{}
	for _, p := range m.snap.Pods {
		k := p.App + "/" + p.Service
		pt := perSvc[k]
		pt.cpu += p.CPUMilli
		pt.mem += p.MemBytes
		if pp, ok := prevPods[p.Pod]; ok {
			pt.rx += rate(pp.NetRxBytes, p.NetRxBytes, dt)
			pt.tx += rate(pp.NetTxBytes, p.NetTxBytes, dt)
		}
		perSvc[k] = pt
	}
	for k, pt := range perSvc {
		h := append(m.hist[k], pt)
		if len(h) > histBuf {
			h = h[len(h)-histBuf:]
		}
		m.hist[k] = h
	}

	// whole-server history for the header sparklines
	var np nodeHistPt
	for _, n := range m.snap.Nodes {
		np.cpuPct += n.CPUPct
		np.memPct += n.MemPct
		if pn, ok := prevNodes[n.Name]; ok {
			np.rx += rate(pn.NetRxBytes, n.NetRxBytes, dt)
			np.tx += rate(pn.NetTxBytes, n.NetTxBytes, dt)
		}
	}
	if c := len(m.snap.Nodes); c > 1 {
		np.cpuPct /= float64(c)
		np.memPct /= float64(c)
	}
	m.nodeHist = append(m.nodeHist, np)
	if len(m.nodeHist) > histBuf {
		m.nodeHist = m.nodeHist[len(m.nodeHist)-histBuf:]
	}
	m.prevSnap = m.snap
}

func (m *uiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		return m, tea.Batch(m.gatherCmd(), uiTick())

	case snapMsg:
		m.snap = msg.snap
		if msg.load != "" {
			m.load = msg.load
		}
		if msg.uptime != "" {
			m.uptime = msg.uptime
		}
		if len(msg.cores) > 0 {
			m.cores = msg.cores
		}
		if len(msg.mounts) > 0 {
			m.mounts = msg.mounts
		}
		if msg.tempC > 0 {
			m.tempC = msg.tempC
		}
		if msg.mem.total > 0 {
			m.mem = msg.mem
		}
		if msg.gpu.probed {
			m.gpu = msg.gpu
		}
		m.io = msg.io
		m.swapT, m.swapU = msg.swapT, msg.swapU
		m.updTotal, m.updSec, m.reboot, m.sshFails = msg.updTotal, msg.updSec, msg.reboot, msg.sshFails
		m.err = ""
		m.rebuildRows()
		m.appendHist()
		var cmds []tea.Cmd
		if app, svc, ok := m.selected(); ok && m.logKey != app+"/"+svc {
			cmds = append(cmds, m.startLogStream(app, svc))
		}
		return m, tea.Batch(cmds...)

	case snapErrMsg:
		m.err = msg.err.Error()
		return m, nil

	case logLineMsg:
		if msg.key == m.logKey {
			l := append(m.logs[msg.key], msg.line)
			if len(l) > logBuf {
				l = l[len(l)-logBuf:]
			}
			m.logs[msg.key] = l
		}
		return m, waitForLog(m.logCh)

	case logDoneMsg:
		return m, nil

	case eventsMsg:
		m.events[msg.app] = msg.lines
		return m, nil

	case actionMsg:
		m.status = msg.text
		return m, m.gatherCmd()

	case tea.KeyMsg:
		if m.confirm != nil {
			c := m.confirm
			m.confirm = nil
			if msg.String() == "y" || msg.String() == "Y" {
				m.status = "working..."
				return m, c.action
			}
			m.status = "cancelled"
			return m, nil
		}
		switch msg.String() {
		case "q", "ctrl+c":
			if m.logCancel != nil {
				m.logCancel()
			}
			return m, tea.Quit
		case "j", "down":
			m.move(1)
			if app, svc, ok := m.selected(); ok && m.logKey != app+"/"+svc {
				return m, m.startLogStream(app, svc)
			}
		case "k", "up":
			m.move(-1)
			if app, svc, ok := m.selected(); ok && m.logKey != app+"/"+svc {
				return m, m.startLogStream(app, svc)
			}
		case "l", "1":
			m.tab = tabLogs
		case "m", "2":
			m.graphs = !m.graphs
		case "s", "3":
			m.tab = tabStatus
			if app, _, ok := m.selected(); ok {
				return m, m.eventsCmd(app)
			}
		case "r":
			if app, svc, ok := m.selected(); ok {
				m.confirm = &confirmState{
					prompt: fmt.Sprintf("Restart %s/%s? (y/n)", app, svc),
					action: m.restartCmd(app, svc),
				}
			}
		case "b":
			if app, _, ok := m.selected(); ok {
				m.confirm = &confirmState{
					prompt: fmt.Sprintf("Rollback app %s to the previous revision? (y/n)", app),
					action: execSelf("rollback", app),
				}
			}
		case "e":
			if app, svc, ok := m.selected(); ok {
				return m, execSelf("exec", app+"/"+svc)
			}
		case "a":
			if app, svc, ok := m.selected(); ok {
				return m, execSelf("attach", app+"/"+svc)
			}
		}
	}
	return m, nil
}

// restartWorkloads is the API-side restart used by both the CLI command and
// the TUI.
func restartWorkloads(ctx context.Context, d *deploy.Deployer, app, svc string) error {
	return doRestart(ctx, d, app, svc)
}

func newUICmd() *cobra.Command {
	var nerd bool
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Interactive terminal UI: apps, live logs, metrics, one-key actions",
		Long: "A lazydocker-style terminal UI. Navigate apps and services, watch live\n" +
			"logs and resource graphs, and act with one key: restart, rollback, exec,\n" +
			"attach. Agents should use the JSON commands instead (ps, status, top).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !term.IsTerminal(int(os.Stdout.Fd())) {
				return fmt.Errorf("jekyo ui needs a terminal; agents should use 'jekyo top --json' and friends")
			}
			d, err := newDeployer()
			if err != nil {
				return err
			}
			name := "?"
			if store, err := contexts.Open(); err == nil {
				if meta, err := store.Resolve(contextFlag); err == nil {
					name = meta.Name
				}
			}
			return runUI(d, name, nerd)
		},
	}
	cmd.Flags().BoolVar(&nerd, "nerd", false, "Nerd Font icons (needs a patched terminal font)")
	return cmd
}

// runUI starts the dashboard; jekyo top in a terminal lands here too.
// Default icons are plain Unicode that render in any monospace font;
// --nerd or JEKYO_NERD=1 switches to Nerd Font glyphs.
func runUI(d *deploy.Deployer, ctxName string, nerd bool) error {
	// best-effort SSH for load average and uptime; the UI works without it
	var sshc *sshx.Client
	domain := ""
	if store, err := contexts.Open(); err == nil {
		if meta, err := store.Resolve(contextFlag); err == nil {
			domain = meta.Domain
			if c, err := sshx.Dial(meta.SSH, sshx.Options{KeyPath: sshKeyFlag}); err == nil {
				sshc = c
			}
		}
	}
	m := &uiModel{
		d: d, ctxName: ctxName, domain: domain, sshc: sshc,
		graphs: true,
		nerd:   nerd || os.Getenv("JEKYO_NERD") != "",
		snap:   &topSnapshot{},
		logs:   map[string][]string{},
		hist:   map[string][]histPt{},
		events: map[string][]string{},
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
