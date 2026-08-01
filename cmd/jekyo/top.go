package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/jekyo/jekyo/internal/compile"
	"github.com/jekyo/jekyo/internal/contexts"
	"github.com/jekyo/jekyo/internal/deploy"
)

// The metrics API (metrics.k8s.io, served by k3s's bundled metrics-server)
// is queried through the dynamic client so no extra clientset is needed.
var (
	podMetricsGVR  = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}
	nodeMetricsGVR = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "nodes"}
)

type topPod struct {
	App           string  `json:"app"`
	Service       string  `json:"service"`
	Pod           string  `json:"pod"`
	Ready         string  `json:"ready"`
	Status        string  `json:"status"`
	Restarts      int     `json:"restarts"`
	AgeSec        int64   `json:"ageSeconds"`
	CPUMilli      int64   `json:"cpuMillicores"`
	MemBytes      int64   `json:"memoryBytes"`
	CPULimitMilli int64   `json:"cpuLimitMillicores,omitempty"`
	MemLimitBytes int64   `json:"memoryLimitBytes,omitempty"`
	CPUPct        float64 `json:"cpuPercentOfLimit,omitempty"`
	MemPct        float64 `json:"memoryPercentOfLimit,omitempty"`
}

type topNode struct {
	Name          string  `json:"name"`
	CPUMilli      int64   `json:"cpuMillicores"`
	CPUCapMilli   int64   `json:"cpuCapacityMillicores"`
	CPUPct        float64 `json:"cpuPercent"`
	MemBytes      int64   `json:"memoryBytes"`
	MemCapBytes   int64   `json:"memoryCapacityBytes"`
	MemPct        float64 `json:"memoryPercent"`
	PodCount      int     `json:"pods"`
	MetricsMissed bool    `json:"metricsUnavailable,omitempty"`
}

type topSnapshot struct {
	Context string   `json:"context"`
	Time    string   `json:"time"`
	Nodes   []topNode `json:"nodes"`
	Pods    []topPod  `json:"pods"`
}

func gatherTop(ctx context.Context, d *deploy.Deployer, contextName, app string) (*topSnapshot, error) {
	sel, ns := compile.LabelApp, ""
	if app != "" {
		ns, sel = compile.NamespaceFor(app), podSelector(app, "")
	}
	pods, err := d.Client.Typed.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return nil, err
	}

	// usage by namespace/pod; metrics lag pod creation by up to a scrape
	// interval, so missing entries render as zero rather than failing.
	usage := map[string][2]int64{}
	metricsOK := true
	pm, err := d.Client.Dynamic.Resource(podMetricsGVR).Namespace(ns).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		metricsOK = false
	} else {
		for _, item := range pm.Items {
			cpu, mem := sumContainerUsage(item)
			usage[item.GetNamespace()+"/"+item.GetName()] = [2]int64{cpu, mem}
		}
	}

	snap := &topSnapshot{Context: contextName, Time: time.Now().UTC().Format(time.RFC3339)}
	for _, p := range pods.Items {
		ready, total, restarts := 0, len(p.Spec.Containers), 0
		for _, cs := range p.Status.ContainerStatuses {
			if cs.Ready {
				ready++
			}
			restarts += int(cs.RestartCount)
		}
		status := string(p.Status.Phase)
		for _, cs := range p.Status.ContainerStatuses {
			if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
				status = cs.State.Waiting.Reason
			}
		}
		var limCPU, limMem int64
		for _, c := range p.Spec.Containers {
			if v, ok := c.Resources.Limits[corev1.ResourceCPU]; ok {
				limCPU += v.MilliValue()
			}
			if v, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
				limMem += v.Value()
			}
		}
		u := usage[p.Namespace+"/"+p.Name]
		row := topPod{
			App: p.Labels[compile.LabelApp], Service: p.Labels[compile.LabelService],
			Pod: p.Name, Ready: fmt.Sprintf("%d/%d", ready, total), Status: status,
			Restarts: restarts, AgeSec: int64(time.Since(p.CreationTimestamp.Time).Seconds()),
			CPUMilli: u[0], MemBytes: u[1], CPULimitMilli: limCPU, MemLimitBytes: limMem,
		}
		if limCPU > 0 {
			row.CPUPct = 100 * float64(u[0]) / float64(limCPU)
		}
		if limMem > 0 {
			row.MemPct = 100 * float64(u[1]) / float64(limMem)
		}
		snap.Pods = append(snap.Pods, row)
	}
	sort.Slice(snap.Pods, func(i, j int) bool {
		a, b := snap.Pods[i], snap.Pods[j]
		if a.App != b.App {
			return a.App < b.App
		}
		return a.Pod < b.Pod
	})

	nodes, err := d.Client.Typed.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	nodeUsage := map[string][2]int64{}
	if nm, err := d.Client.Dynamic.Resource(nodeMetricsGVR).List(ctx, metav1.ListOptions{}); err == nil {
		for _, item := range nm.Items {
			cpu, mem := parseUsage(item.Object["usage"])
			nodeUsage[item.GetName()] = [2]int64{cpu, mem}
		}
	} else {
		metricsOK = false
	}
	allPods, _ := d.Client.Typed.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	for _, n := range nodes.Items {
		u := nodeUsage[n.Name]
		node := topNode{
			Name: n.Name, CPUMilli: u[0], MemBytes: u[1],
			CPUCapMilli:   n.Status.Allocatable.Cpu().MilliValue(),
			MemCapBytes:   n.Status.Allocatable.Memory().Value(),
			MetricsMissed: !metricsOK,
		}
		if node.CPUCapMilli > 0 {
			node.CPUPct = 100 * float64(node.CPUMilli) / float64(node.CPUCapMilli)
		}
		if node.MemCapBytes > 0 {
			node.MemPct = 100 * float64(node.MemBytes) / float64(node.MemCapBytes)
		}
		if allPods != nil {
			for _, p := range allPods.Items {
				if p.Spec.NodeName == n.Name && p.Status.Phase == corev1.PodRunning {
					node.PodCount++
				}
			}
		}
		snap.Nodes = append(snap.Nodes, node)
	}
	return snap, nil
}

func sumContainerUsage(item unstructured.Unstructured) (cpuMilli, memBytes int64) {
	containers, _, _ := unstructured.NestedSlice(item.Object, "containers")
	for _, c := range containers {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		cpu, mem := parseUsage(m["usage"])
		cpuMilli += cpu
		memBytes += mem
	}
	return
}

func parseUsage(v any) (cpuMilli, memBytes int64) {
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	if s, ok := m["cpu"].(string); ok {
		if q, err := resource.ParseQuantity(s); err == nil {
			cpuMilli = q.MilliValue()
		}
	}
	if s, ok := m["memory"].(string); ok {
		if q, err := resource.ParseQuantity(s); err == nil {
			memBytes = q.Value()
		}
	}
	return
}

// bar renders a btop-style usage bar: [██████░░░░░░] with thresholds.
func bar(pct float64, width int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(pct/100*float64(width) + 0.5)
	color := "\033[32m" // green
	if pct >= 85 {
		color = "\033[31m" // red
	} else if pct >= 60 {
		color = "\033[33m" // yellow
	}
	return color + strings.Repeat("█", filled) + "\033[2m" + strings.Repeat("░", width-filled) + "\033[0m"
}

func fmtMem(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1fGi", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0fMi", float64(b)/(1<<20))
	default:
		return fmt.Sprintf("%.0fKi", float64(b)/(1<<10))
	}
}

func fmtCPU(m int64) string {
	if m >= 1000 {
		return fmt.Sprintf("%.2f", float64(m)/1000)
	}
	return fmt.Sprintf("%dm", m)
}

func renderTop(snap *topSnapshot, interval time.Duration) string {
	var b strings.Builder
	b.WriteString("\033[H\033[2J") // home + clear
	fmt.Fprintf(&b, "\033[1mJEKYO\033[0m  context \033[36m%s\033[0m  %s  (refresh %s, Ctrl+C to quit)\n\n",
		snap.Context, time.Now().Format("15:04:05"), interval)

	for _, n := range snap.Nodes {
		if n.MetricsMissed {
			fmt.Fprintf(&b, "  \033[33mmetrics-server not responding yet; usage shows 0\033[0m\n")
		}
		fmt.Fprintf(&b, "  %-12s cpu %s %5.1f%%  (%s/%s)\n", n.Name, bar(n.CPUPct, 24), n.CPUPct, fmtCPU(n.CPUMilli), fmtCPU(n.CPUCapMilli))
		fmt.Fprintf(&b, "  %-12s mem %s %5.1f%%  (%s/%s)  %d pods\n\n", "", bar(n.MemPct, 24), n.MemPct, fmtMem(n.MemBytes), fmtMem(n.MemCapBytes), n.PodCount)
	}

	if len(snap.Pods) == 0 {
		b.WriteString("  No app pods.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "  \033[1m%-14s %-12s %-7s %-16s %4s %8s %9s %-14s %-14s\033[0m\n",
		"APP", "SERVICE", "READY", "STATUS", "RST", "CPU", "MEM", "CPU/LIMIT", "MEM/LIMIT")
	for _, p := range snap.Pods {
		// pad before coloring so ANSI codes don't break column alignment
		status := fmt.Sprintf("%-16s", p.Status)
		if p.Status != "Running" {
			status = "\033[31m" + status + "\033[0m"
		}
		cpuBar, memBar := strings.Repeat(" ", 14), strings.Repeat(" ", 14)
		if p.CPULimitMilli > 0 {
			cpuBar = bar(p.CPUPct, 8) + fmt.Sprintf(" %3.0f%%", p.CPUPct)
		}
		if p.MemLimitBytes > 0 {
			memBar = bar(p.MemPct, 8) + fmt.Sprintf(" %3.0f%%", p.MemPct)
		}
		fmt.Fprintf(&b, "  %-14s %-12s %-7s %s %4d %8s %9s %s %s\n",
			p.App, p.Service, p.Ready, status, p.Restarts, fmtCPU(p.CPUMilli), fmtMem(p.MemBytes), cpuBar, memBar)
	}
	return b.String()
}

func newTopCmd() *cobra.Command {
	var jsonOut bool
	var interval time.Duration
	cmd := &cobra.Command{
		Use:   "top [app]",
		Short: "Live resource dashboard for apps (CPU, memory, restarts)",
		Long: "Live btop-style view of app pods and node capacity, refreshed in place.\n" +
			"With --json it prints one machine-readable snapshot and exits, which is\n" +
			"the form AI agents should use.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := ""
			if len(args) == 1 {
				app = args[0]
			}
			d, err := newDeployer()
			if err != nil {
				return err
			}
			name := "?"
			if store, err := contexts.Open(); err == nil {
				if m, err := store.Resolve(contextFlag); err == nil {
					name = m.Name
				}
			}

			if jsonOut || !term.IsTerminal(int(os.Stdout.Fd())) {
				ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
				defer cancel()
				snap, err := gatherTop(ctx, d, name, app)
				if err != nil {
					return err
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(snap)
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				snap, err := gatherTop(ctx, d, name, app)
				if err != nil {
					if ctx.Err() != nil {
						fmt.Fprint(cmd.OutOrStdout(), "\033[0m\n")
						return nil
					}
					return err
				}
				fmt.Fprint(cmd.OutOrStdout(), renderTop(snap, interval))
				select {
				case <-ctx.Done():
					fmt.Fprint(cmd.OutOrStdout(), "\033[0m\n")
					return nil
				case <-ticker.C:
				}
			}
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print one snapshot as JSON and exit")
	cmd.Flags().DurationVar(&interval, "interval", 2*time.Second, "refresh interval")
	return cmd
}
