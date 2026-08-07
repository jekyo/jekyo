package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/jekyo/jekyo/internal/compile"
	"github.com/jekyo/jekyo/internal/contexts"
	"github.com/jekyo/jekyo/internal/deploy"
)

// splitTarget parses "app" or "app/service".
func splitTarget(s string) (app, svc string) {
	app, svc, _ = strings.Cut(s, "/")
	svc, _, _ = strings.Cut(svc, "/")
	return app, svc
}

// splitTarget3 parses "app[/service[/container]]"; the third segment
// addresses a sidecar or init container (issue #19).
func splitTarget3(s string) (app, svc, container string) {
	parts := strings.SplitN(s, "/", 3)
	app = parts[0]
	if len(parts) > 1 {
		svc = parts[1]
	}
	if len(parts) > 2 {
		container = parts[2]
	}
	return
}

// pickContainer resolves which container a command targets: the explicit
// third segment, else the container named after the service, else the
// pod's first container. Unknown names error with the available list.
func pickContainer(pod *corev1.Pod, svc, explicit string) (string, error) {
	var names []string
	for _, c := range pod.Spec.Containers {
		names = append(names, c.Name)
	}
	want := explicit
	if want == "" {
		want = svc
	}
	for _, n := range names {
		if n == want {
			return n, nil
		}
	}
	if explicit != "" {
		return "", fmt.Errorf("no container %q in pod %s (available: %s)", explicit, pod.Name, strings.Join(names, ", "))
	}
	if len(names) > 0 {
		return names[0], nil
	}
	return "", fmt.Errorf("pod %s has no containers", pod.Name)
}

func podSelector(app, svc string) string {
	sel := compile.LabelApp + "=" + app
	if svc != "" {
		sel += "," + compile.LabelService + "=" + svc
	}
	return sel
}

// podRow is the JSON shape of one `jekyo ps -o json` entry.
type containerRow struct {
	Name     string `json:"name"`
	Ready    bool   `json:"ready"`
	Restarts int    `json:"restarts"`
}

type podRow struct {
	App        string         `json:"app"`
	Service    string         `json:"service"`
	Pod        string         `json:"pod"`
	Ready      string         `json:"ready"`
	Status     string         `json:"status"`
	Restarts   int            `json:"restarts"`
	AgeSec     int64          `json:"ageSeconds"`
	Containers []containerRow `json:"containers,omitempty"`
}

func newPsCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "ps [app]",
		Short: "List pods (all apps, or one app)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := newDeployer()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()

			sel := compile.LabelApp
			ns := ""
			if len(args) == 1 {
				app, svc := splitTarget(args[0])
				ns, sel = compile.NamespaceFor(app), podSelector(app, svc)
			}
			pods, err := d.Client.Typed.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: sel})
			if err != nil {
				return err
			}
			if len(pods.Items) == 0 {
				cmd.Println("No pods.")
				return nil
			}
			sort.Slice(pods.Items, func(i, j int) bool {
				a, b := pods.Items[i], pods.Items[j]
				if a.Namespace != b.Namespace {
					return a.Namespace < b.Namespace
				}
				return a.Name < b.Name
			})
			var rows []podRow
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
				row := podRow{
					App: p.Labels[compile.LabelApp], Service: p.Labels[compile.LabelService],
					Pod: p.Name, Ready: fmt.Sprintf("%d/%d", ready, total), Status: status,
					Restarts: restarts, AgeSec: int64(time.Since(p.CreationTimestamp.Time).Seconds()),
				}
				if total > 1 {
					for _, cs := range p.Status.ContainerStatuses {
						if cs.Name == row.Service {
							continue // the main container is the parent row
						}
						row.Containers = append(row.Containers, containerRow{Name: cs.Name, Ready: cs.Ready, Restarts: int(cs.RestartCount)})
					}
				}
				rows = append(rows, row)
			}
			if output == "json" {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(rows)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "APP\tSERVICE\tPOD\tREADY\tSTATUS\tRESTARTS\tAGE")
			for _, r := range rows {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
					r.App, r.Service, r.Pod, r.Ready, r.Status, r.Restarts,
					age(time.Now().Add(-time.Duration(r.AgeSec)*time.Second)))
				for _, c := range r.Containers {
					ready := "0/1"
					if c.Ready {
						ready = "1/1"
					}
					fmt.Fprintf(w, "%s\t └─ %s\t\t%s\t\t%d\t\n", r.App, c.Name, ready, c.Restarts)
				}
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "output format: json")
	return cmd
}

func newLogsCmd() *cobra.Command {
	var follow bool
	var since string
	var tail int64
	var timestamps bool
	cmd := &cobra.Command{
		Use:   "logs <app>[/<service>]",
		Short: "Show (or follow) logs for an app or one service",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			appName, svc, container := splitTarget3(args[0])
			d, err := newDeployer()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			pods, err := d.Client.Typed.CoreV1().Pods(compile.NamespaceFor(appName)).List(ctx, metav1.ListOptions{LabelSelector: podSelector(appName, svc)})
			if err != nil {
				return err
			}
			if len(pods.Items) == 0 {
				return fmt.Errorf("no pods for %s", args[0])
			}

			opts := &corev1.PodLogOptions{Follow: follow, Timestamps: timestamps}
			if tail > 0 {
				opts.TailLines = &tail
			}
			if since != "" {
				dur, err := time.ParseDuration(since)
				if err != nil {
					return fmt.Errorf("--since: %w", err)
				}
				secs := int64(dur.Seconds())
				opts.SinceSeconds = &secs
			}

			// one stream per container so multi-container pods just work
			// (issue #19); a third target segment narrows to one container
			type streamT struct{ label, pod, container string }
			var streams []streamT
			for _, p := range pods.Items {
				for _, c := range p.Spec.Containers {
					if container != "" && c.Name != container {
						continue
					}
					label := p.Name
					if len(p.Spec.Containers) > 1 {
						label = p.Name + "/" + c.Name
					}
					streams = append(streams, streamT{label: label, pod: p.Name, container: c.Name})
				}
			}
			if len(streams) == 0 {
				return fmt.Errorf("no container %q in the pods of %s/%s", container, appName, svc)
			}
			prefix := len(streams) > 1
			var wg sync.WaitGroup
			for _, st := range streams {
				o := *opts
				o.Container = st.container
				rc, err := d.Client.Typed.CoreV1().Pods(compile.NamespaceFor(appName)).GetLogs(st.pod, &o).Stream(ctx)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "%s: %v\n", st.label, err)
					continue
				}
				wg.Add(1)
				go func(name string, rc io.ReadCloser) {
					defer wg.Done()
					defer rc.Close()
					sc := bufio.NewScanner(rc)
					sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
					for sc.Scan() {
						if prefix {
							fmt.Fprintf(cmd.OutOrStdout(), "[%s] %s\n", name, sc.Text())
						} else {
							fmt.Fprintln(cmd.OutOrStdout(), sc.Text())
						}
					}
				}(st.label, rc)
			}
			wg.Wait()
			return nil
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "stream new log lines")
	cmd.Flags().StringVar(&since, "since", "", "only logs newer than a duration (e.g. 1h)")
	cmd.Flags().Int64Var(&tail, "tail", 200, "lines of recent logs per pod (0 = all)")
	cmd.Flags().BoolVarP(&timestamps, "timestamps", "t", false, "prefix each line with its RFC3339 timestamp")
	return cmd
}

func newAttachCmd() *cobra.Command {
	var stdin bool
	cmd := &cobra.Command{
		Use:   "attach <app>/<service>[/<container>]",
		Short: "Attach to the running process of a service (Ctrl+C detaches)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			appName, svc, containerArg := splitTarget3(args[0])
			if svc == "" {
				return fmt.Errorf("target must be <app>/<service>[/<container>]")
			}
			d, err := newDeployer()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			pods, err := d.Client.Typed.CoreV1().Pods(compile.NamespaceFor(appName)).List(ctx, metav1.ListOptions{LabelSelector: podSelector(appName, svc)})
			if err != nil {
				return err
			}
			var pod *corev1.Pod
			for i := range pods.Items {
				if pods.Items[i].Status.Phase == corev1.PodRunning {
					pod = &pods.Items[i]
					break
				}
			}
			if pod == nil {
				return fmt.Errorf("no running pod for %s", args[0])
			}

			attachContainer, err := pickContainer(pod, svc, containerArg)
			if err != nil {
				return err
			}
			tty := stdin && term.IsTerminal(int(os.Stdin.Fd()))
			req := d.Client.Typed.CoreV1().RESTClient().Post().
				Resource("pods").Namespace(compile.NamespaceFor(appName)).Name(pod.Name).SubResource("attach").
				VersionedParams(&corev1.PodAttachOptions{
					Container: attachContainer,
					Stdin:     stdin,
					Stdout:    true,
					Stderr:    true,
					TTY:       tty,
				}, scheme.ParameterCodec)
			executor, err := remotecommand.NewSPDYExecutor(d.Client.Config, "POST", req.URL())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Attached to %s (pod %s). Ctrl+C detaches without stopping it.\n", args[0], pod.Name)
			streamOpts := remotecommand.StreamOptions{Stdout: os.Stdout, Stderr: os.Stderr}
			if stdin {
				if tty {
					oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
					if err == nil {
						defer term.Restore(int(os.Stdin.Fd()), oldState)
					}
					streamOpts.Tty = true
				}
				streamOpts.Stdin = os.Stdin
			}
			return executor.StreamWithContext(ctx, streamOpts)
		},
	}
	cmd.Flags().BoolVarP(&stdin, "stdin", "i", false, "forward stdin to the process (needs a TTY-enabled service)")
	return cmd
}

func newExecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec <app>/<service>[/<container>] [-- command...]",
		Short: "Run a command in a service's pod (default: interactive shell)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			appName, svc, containerArg := splitTarget3(args[0])
			if svc == "" {
				return fmt.Errorf("target must be <app>/<service>[/<container>]")
			}
			command := args[1:]
			if len(command) == 0 {
				command = []string{"/bin/sh", "-c", "command -v bash >/dev/null && exec bash || exec sh"}
			}
			d, err := newDeployer()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			pods, err := d.Client.Typed.CoreV1().Pods(compile.NamespaceFor(appName)).List(ctx, metav1.ListOptions{LabelSelector: podSelector(appName, svc)})
			if err != nil {
				return err
			}
			var pod *corev1.Pod
			for i := range pods.Items {
				if pods.Items[i].Status.Phase == corev1.PodRunning {
					pod = &pods.Items[i]
					break
				}
			}
			if pod == nil {
				return fmt.Errorf("no running pod for %s", args[0])
			}

			execContainer, err := pickContainer(pod, svc, containerArg)
			if err != nil {
				return err
			}
			interactive := term.IsTerminal(int(os.Stdin.Fd()))
			req := d.Client.Typed.CoreV1().RESTClient().Post().
				Resource("pods").Namespace(compile.NamespaceFor(appName)).Name(pod.Name).SubResource("exec").
				VersionedParams(&corev1.PodExecOptions{
					Container: execContainer,
					Command:   command,
					Stdin:     interactive,
					Stdout:    true,
					Stderr:    true,
					TTY:       interactive,
				}, scheme.ParameterCodec)
			executor, err := remotecommand.NewSPDYExecutor(d.Client.Config, "POST", req.URL())
			if err != nil {
				return err
			}
			streamOpts := remotecommand.StreamOptions{Stdout: os.Stdout, Stderr: os.Stderr}
			if interactive {
				oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
				if err == nil {
					defer term.Restore(int(os.Stdin.Fd()), oldState)
				}
				streamOpts.Stdin = os.Stdin
				streamOpts.Tty = true
			}
			return executor.StreamWithContext(ctx, streamOpts)
		},
	}
	return cmd
}

// doRestart rolling-restarts the workloads of app (optionally one service).
// Shared by the restart command and the TUI.
func doRestart(ctx context.Context, d *deploy.Deployer, appName, svc string) error {
	patch := []byte(fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{"jekyo.io/restarted-at":%q}}}}}`,
		time.Now().UTC().Format(time.RFC3339)))
	restarted := 0
	sel := metav1.ListOptions{LabelSelector: podSelector(appName, svc)}
	deps, err := d.Client.Typed.AppsV1().Deployments(compile.NamespaceFor(appName)).List(ctx, sel)
	if err != nil {
		return err
	}
	for _, dep := range deps.Items {
		if _, err := d.Client.Typed.AppsV1().Deployments(compile.NamespaceFor(appName)).Patch(ctx, dep.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
			return err
		}
		restarted++
	}
	stss, err := d.Client.Typed.AppsV1().StatefulSets(compile.NamespaceFor(appName)).List(ctx, sel)
	if err != nil {
		return err
	}
	for _, sts := range stss.Items {
		if _, err := d.Client.Typed.AppsV1().StatefulSets(compile.NamespaceFor(appName)).Patch(ctx, sts.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
			return err
		}
		restarted++
	}
	if restarted == 0 {
		target := appName
		if svc != "" {
			target += "/" + svc
		}
		return fmt.Errorf("nothing to restart for %s", target)
	}
	return nil
}

func newRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart <app>[/<service>]",
		Short: "Rolling-restart an app or one service",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			appName, svc := splitTarget(args[0])
			d, err := newDeployer()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			if err := doRestart(ctx, d, appName, svc); err != nil {
				return err
			}
			cmd.Printf("Restarted. Watch with: jekyo ps %s\n", appName)
			return nil
		},
	}
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <app>",
		Short: "Rollout state, endpoints, certificates, recent events",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			appName := args[0]
			d, err := newDeployer()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			out := cmd.OutOrStdout()

			revs, err := d.Releases(ctx, appName)
			if err != nil || len(revs) == 0 {
				return fmt.Errorf("app %q is not deployed", appName)
			}
			last := revs[len(revs)-1]
			fmt.Fprintf(out, "App:      %s (revision %d, deployed %s)\n", appName, last.Revision, ago(last.DeployedAt))

			w := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
			deps, _ := d.Client.Typed.AppsV1().Deployments(compile.NamespaceFor(appName)).List(ctx, metav1.ListOptions{LabelSelector: compile.LabelApp})
			for _, dep := range deps.Items {
				fmt.Fprintf(w, "  %s\tdeployment\t%d/%d ready\n", dep.Name, dep.Status.ReadyReplicas, dep.Status.Replicas)
			}
			stss, _ := d.Client.Typed.AppsV1().StatefulSets(compile.NamespaceFor(appName)).List(ctx, metav1.ListOptions{LabelSelector: compile.LabelApp})
			for _, sts := range stss.Items {
				fmt.Fprintf(w, "  %s\tstatefulset\t%d/%d ready\n", sts.Name, sts.Status.ReadyReplicas, sts.Status.Replicas)
			}
			cjs, _ := d.Client.Typed.BatchV1().CronJobs(compile.NamespaceFor(appName)).List(ctx, metav1.ListOptions{LabelSelector: compile.LabelApp})
			for _, cj := range cjs.Items {
				lastRun := "never"
				if cj.Status.LastScheduleTime != nil {
					lastRun = ago(cj.Status.LastScheduleTime.Time)
				}
				fmt.Fprintf(w, "  %s\tcronjob\t%s, last run %s\n", cj.Name, cj.Spec.Schedule, lastRun)
			}
			w.Flush()

			ings, _ := d.Client.Typed.NetworkingV1().Ingresses(compile.NamespaceFor(appName)).List(ctx, metav1.ListOptions{LabelSelector: compile.LabelApp})
			for _, ing := range ings.Items {
				for _, r := range ing.Spec.Rules {
					tls := "http"
					for _, t := range ing.Spec.TLS {
						for _, h := range t.Hosts {
							if h == r.Host {
								tls = "https"
								if sec, err := d.Client.Typed.CoreV1().Secrets(compile.NamespaceFor(appName)).Get(ctx, t.SecretName, metav1.GetOptions{}); err != nil || len(sec.Data["tls.crt"]) == 0 {
									tls = "https (certificate pending)"
								}
							}
						}
					}
					fmt.Fprintf(out, "URL:      %s://%s (%s)\n", strings.Split(tls, " ")[0], r.Host, tls)
				}
			}

			events, _ := d.Client.Typed.CoreV1().Events(compile.NamespaceFor(appName)).List(ctx, metav1.ListOptions{})
			var warnings []corev1.Event
			for _, e := range events.Items {
				if e.Type == corev1.EventTypeWarning {
					warnings = append(warnings, e)
				}
			}
			if len(warnings) > 0 {
				sort.Slice(warnings, func(i, j int) bool {
					return warnings[i].LastTimestamp.Time.After(warnings[j].LastTimestamp.Time)
				})
				fmt.Fprintln(out, "Warnings:")
				for i, e := range warnings {
					if i >= 5 {
						break
					}
					fmt.Fprintf(out, "  %s  %s: %s\n", ago(e.LastTimestamp.Time), e.InvolvedObject.Name, e.Message)
				}
			}
			return nil
		},
	}
}

func newHistoryCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "history <app>",
		Short: "List an app's release revisions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := newDeployer()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			revs, err := d.Releases(ctx, args[0])
			if err != nil {
				return err
			}
			if len(revs) == 0 {
				return fmt.Errorf("app %q has no releases", args[0])
			}
			if output == "json" {
				type rev struct {
					Revision   int       `json:"revision"`
					DeployedAt time.Time `json:"deployedAt"`
				}
				out := make([]rev, 0, len(revs))
				for _, r := range revs {
					out = append(out, rev{r.Revision, r.DeployedAt})
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "REVISION\tDEPLOYED")
			for _, r := range revs {
				fmt.Fprintf(w, "v%d\t%s\n", r.Revision, ago(r.DeployedAt))
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "output format: json")
	return cmd
}

func newRollbackCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rollback <app> [revision]",
		Short: "Re-apply a previous revision (default: the one before current)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			appName := args[0]
			d, err := newDeployer()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), deployTimeout)
			defer cancel()
			revs, err := d.Releases(ctx, appName)
			if err != nil {
				return err
			}
			if len(revs) < 2 && len(args) == 1 {
				return fmt.Errorf("app %q has no previous revision", appName)
			}
			target := 0
			if len(args) == 2 {
				if _, err := fmt.Sscanf(args[1], "v%d", &target); err != nil {
					if _, err := fmt.Sscanf(args[1], "%d", &target); err != nil {
						return fmt.Errorf("revision must be a number, got %q", args[1])
					}
				}
			} else {
				target = revs[len(revs)-2].Revision
			}
			var manifest []byte
			for _, r := range revs {
				if r.Revision == target {
					manifest = r.Manifest
				}
			}
			if manifest == nil {
				return fmt.Errorf("revision v%d not found (see 'jekyo history %s')", target, appName)
			}
			rev, err := d.ApplyManifest(ctx, appName, manifest)
			if err != nil {
				return err
			}
			cmd.Printf("Rolled back %s to v%d (recorded as new revision v%d)\n", appName, target, rev)
			return nil
		},
	}
}

func newRegistryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "registry",
		Short: "External private registries (image pulls)",
	}
	login := &cobra.Command{
		Use:   "login <host>",
		Short: "Store credentials for an external registry (e.g. ghcr.io)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := contexts.Open()
			if err != nil {
				return err
			}
			m, err := store.Resolve(contextFlag)
			if err != nil {
				return err
			}
			user, _ := cmd.Flags().GetString("username")
			pass, _ := cmd.Flags().GetString("password")
			reader := bufio.NewReader(cmd.InOrStdin())
			if user == "" {
				fmt.Fprint(cmd.OutOrStdout(), "Username: ")
				u, _ := reader.ReadString('\n')
				user = strings.TrimSpace(u)
			}
			if pass == "" {
				fmt.Fprint(cmd.OutOrStdout(), "Password/token: ")
				if term.IsTerminal(int(os.Stdin.Fd())) {
					b, err := term.ReadPassword(int(os.Stdin.Fd()))
					fmt.Fprintln(cmd.OutOrStdout())
					if err != nil {
						return err
					}
					pass = string(b)
				} else {
					p, _ := reader.ReadString('\n')
					pass = strings.TrimSpace(p)
				}
			}
			if m.Logins == nil {
				m.Logins = map[string]contexts.Login{}
			}
			m.Logins[args[0]] = contexts.Login{Username: user, Password: pass}
			if err := store.Save(m); err != nil {
				return err
			}
			cmd.Printf("Stored login for %s in context %s. Apps using %s/... images get pull access on next 'jekyo up'.\n", args[0], m.Name, args[0])
			return nil
		},
	}
	login.Flags().StringP("username", "u", "", "registry username")
	login.Flags().StringP("password", "p", "", "registry password or token")

	logout := &cobra.Command{
		Use:   "logout <host>",
		Short: "Remove stored credentials for a registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := contexts.Open()
			if err != nil {
				return err
			}
			m, err := store.Resolve(contextFlag)
			if err != nil {
				return err
			}
			if _, ok := m.Logins[args[0]]; !ok {
				return fmt.Errorf("no login stored for %s", args[0])
			}
			delete(m.Logins, args[0])
			if err := store.Save(m); err != nil {
				return err
			}
			cmd.Println("Removed login for", args[0])
			return nil
		},
	}
	cmd.AddCommand(login, logout)
	return cmd
}
