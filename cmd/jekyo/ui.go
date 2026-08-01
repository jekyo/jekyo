package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/jekyo/jekyo/internal/compile"
	"github.com/jekyo/jekyo/internal/contexts"
	"github.com/jekyo/jekyo/internal/deploy"
)

// ---- messages ----

type snapMsg struct{ snap *topSnapshot }
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
	tabMetrics
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
		return snapMsg{snap}
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
			m.tab = tabMetrics
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
	return &cobra.Command{
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
			return runUI(d, name)
		},
	}
}

// runUI starts the dashboard; jekyo top in a terminal lands here too.
func runUI(d *deploy.Deployer, ctxName string) error {
	m := &uiModel{
		d: d, ctxName: ctxName,
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
