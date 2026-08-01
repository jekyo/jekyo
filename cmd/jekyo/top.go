package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
	// cumulative since pod start; diff two snapshots for rates
	NetRxBytes int64 `json:"networkRxBytes,omitempty"`
	NetTxBytes int64 `json:"networkTxBytes,omitempty"`
}

type topVolume struct {
	App      string  `json:"app"`
	Claim    string  `json:"claim"`
	Used     int64   `json:"usedBytes"`
	Capacity int64   `json:"capacityBytes"`
	Pct      float64 `json:"percentUsed"`
}

type topNode struct {
	Name          string  `json:"name"`
	CPUMilli      int64   `json:"cpuMillicores"`
	CPUCapMilli   int64   `json:"cpuCapacityMillicores"`
	CPUPct        float64 `json:"cpuPercent"`
	MemBytes      int64   `json:"memoryBytes"`
	MemCapBytes   int64   `json:"memoryCapacityBytes"`
	MemPct        float64 `json:"memoryPercent"`
	DiskUsed      int64   `json:"diskUsedBytes,omitempty"`
	DiskCap       int64   `json:"diskCapacityBytes,omitempty"`
	DiskPct       float64 `json:"diskPercent,omitempty"`
	NetRxBytes    int64   `json:"networkRxBytes,omitempty"` // cumulative
	NetTxBytes    int64   `json:"networkTxBytes,omitempty"` // cumulative
	PodCount      int     `json:"pods"`
	MetricsMissed bool    `json:"metricsUnavailable,omitempty"`
}

type topSnapshot struct {
	Context string      `json:"context"`
	Time    string      `json:"time"`
	Nodes   []topNode   `json:"nodes"`
	Pods    []topPod    `json:"pods"`
	Volumes []topVolume `json:"volumes,omitempty"`
	takenAt time.Time
}

// statsSummary is the subset of the kubelet stats summary API we read
// (network counters, node filesystem, and per-PVC volume usage).
type statsSummary struct {
	Node struct {
		Network struct {
			RxBytes    *int64 `json:"rxBytes"`
			TxBytes    *int64 `json:"txBytes"`
			Interfaces []struct {
				Name    string `json:"name"`
				RxBytes *int64 `json:"rxBytes"`
				TxBytes *int64 `json:"txBytes"`
			} `json:"interfaces"`
		} `json:"network"`
		Fs struct {
			UsedBytes     *int64 `json:"usedBytes"`
			CapacityBytes *int64 `json:"capacityBytes"`
		} `json:"fs"`
	} `json:"node"`
	Pods []struct {
		PodRef struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"podRef"`
		Network struct {
			RxBytes *int64 `json:"rxBytes"`
			TxBytes *int64 `json:"txBytes"`
		} `json:"network"`
		Volume []struct {
			UsedBytes     *int64 `json:"usedBytes"`
			CapacityBytes *int64 `json:"capacityBytes"`
			PVCRef        *struct {
				Name string `json:"name"`
			} `json:"pvcRef"`
		} `json:"volume"`
	} `json:"pods"`
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func pct(used, cap int64) float64 {
	if cap <= 0 {
		return 0
	}
	return 100 * float64(used) / float64(cap)
}

// fmtRate renders bytes/sec compactly (12K/s, 3.4M/s).
func fmtRate(bps float64) string {
	switch {
	case bps >= 1<<20:
		return fmt.Sprintf("%.1fM/s", bps/(1<<20))
	case bps >= 1<<10:
		return fmt.Sprintf("%.0fK/s", bps/(1<<10))
	default:
		return fmt.Sprintf("%.0fB/s", bps)
	}
}

// rate turns two cumulative byte counters into bytes/sec; counters reset
// when a pod restarts, so negative deltas clamp to zero.
func rate(prev, cur int64, dt float64) float64 {
	if dt <= 0 || cur < prev {
		return 0
	}
	return float64(cur-prev) / dt
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

	snap := &topSnapshot{Context: contextName, Time: time.Now().UTC().Format(time.RFC3339), takenAt: time.Now()}

	// kubelet stats summary: network counters, node disk, PVC usage.
	podNet := map[string][2]int64{}
	sums := map[string]statsSummary{}
	if nodeList, err := d.Client.Typed.CoreV1().Nodes().List(ctx, metav1.ListOptions{}); err == nil {
		for _, n := range nodeList.Items {
			raw, err := d.Client.Typed.CoreV1().RESTClient().Get().
				AbsPath("/api/v1/nodes/" + n.Name + "/proxy/stats/summary").DoRaw(ctx)
			if err != nil {
				continue
			}
			var sum statsSummary
			if json.Unmarshal(raw, &sum) == nil {
				sums[n.Name] = sum
				nodeFsCap := deref(sum.Node.Fs.CapacityBytes)
				for _, p := range sum.Pods {
					podNet[p.PodRef.Namespace+"/"+p.PodRef.Name] = [2]int64{deref(p.Network.RxBytes), deref(p.Network.TxBytes)}
					for _, v := range p.Volume {
						if v.PVCRef == nil || !strings.HasPrefix(p.PodRef.Namespace, "jekyo-") {
							continue
						}
						capB := deref(v.CapacityBytes)
						// local-path volumes report the node filesystem, not
						// their own usage; that row carries no information
						if nodeFsCap > 0 && capB > nodeFsCap-nodeFsCap/100 && capB < nodeFsCap+nodeFsCap/100 {
							continue
						}
						snap.Volumes = append(snap.Volumes, topVolume{
							App:      strings.TrimPrefix(p.PodRef.Namespace, "jekyo-"),
							Claim:    v.PVCRef.Name,
							Used:     deref(v.UsedBytes),
							Capacity: capB,
							Pct:      pct(deref(v.UsedBytes), capB),
						})
					}
				}
			}
		}
	}
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
		nets := podNet[p.Namespace+"/"+p.Name]
		row := topPod{
			App: p.Labels[compile.LabelApp], Service: p.Labels[compile.LabelService],
			Pod: p.Name, Ready: fmt.Sprintf("%d/%d", ready, total), Status: status,
			Restarts: restarts, AgeSec: int64(time.Since(p.CreationTimestamp.Time).Seconds()),
			CPUMilli: u[0], MemBytes: u[1], CPULimitMilli: limCPU, MemLimitBytes: limMem,
			NetRxBytes: nets[0], NetTxBytes: nets[1],
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
		if s, ok := sums[n.Name]; ok {
			node.DiskUsed = deref(s.Node.Fs.UsedBytes)
			node.DiskCap = deref(s.Node.Fs.CapacityBytes)
			node.DiskPct = pct(node.DiskUsed, node.DiskCap)
			node.NetRxBytes = deref(s.Node.Network.RxBytes)
			node.NetTxBytes = deref(s.Node.Network.TxBytes)
			// some kubelets omit the default-interface rollup; use the
			// busiest interface as the uplink proxy
			if node.NetRxBytes == 0 && node.NetTxBytes == 0 {
				for _, iface := range s.Node.Network.Interfaces {
					if deref(iface.RxBytes) > node.NetRxBytes {
						node.NetRxBytes = deref(iface.RxBytes)
						node.NetTxBytes = deref(iface.TxBytes)
					}
				}
			}
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

func newTopCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "top [app]",
		Short: "One resource snapshot as JSON (in a terminal, opens jekyo ui)",
		Long: "Prints one machine-readable snapshot: per-pod CPU, memory, network\n" +
			"counters and restarts, node capacity, disk, and volume usage. This is\n" +
			"the form AI agents and scripts should consume. Run interactively in a\n" +
			"terminal it opens the jekyo ui dashboard instead.",
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

			if term.IsTerminal(int(os.Stdout.Fd())) && !cmd.Flags().Changed("json") {
				return runUI(d, name, os.Getenv("JEKYO_NERD") != "")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			snap, err := gatherTop(ctx, d, name, app)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(snap)
		},
	}
	cmd.Flags().Bool("json", false, "print the snapshot even in a terminal")
	return cmd
}
