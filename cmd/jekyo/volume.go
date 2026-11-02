package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/jekyo/jekyo/internal/compile"
	"github.com/jekyo/jekyo/internal/contexts"
	"github.com/jekyo/jekyo/internal/deploy"
	"github.com/jekyo/jekyo/internal/kube"
	"github.com/jekyo/jekyo/internal/sshx"
)

func newVolumeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "volume",
		Short: "Move volume data between servers",
	}
	cmd.AddCommand(newVolumeCopyCmd())
	return cmd
}

// volumeEnd is one side of a copy: the context, its API client and the
// node path its PersistentVolume is backed by.
type volumeEnd struct {
	meta     contexts.Meta
	deployer *deploy.Deployer
	claim    string
	hostPath string
}

func newVolumeCopyCmd() *cobra.Command {
	var from, to string
	var yes bool
	cmd := &cobra.Command{
		Use:   "copy <app>/<volume>[/<replica>]",
		Short: "Copy a volume's data from one server to another",
		Long: `Copies a volume's contents between two servers, for moving a stateful app
between them. Both sides must already have the volume: deploy the app on
the destination first ('jekyo --context <to> up'), which creates an empty
claim.

Nothing may be writing to either side, so every workload mounting the
volume must be stopped first ('jekyo --context <ctx> down <app>' keeps the
volume). The copy is refused while a pod still mounts it, because a
half-written database is worse than no copy.

Data moves node to node over SSH as a tar stream, preserving owners and
modes, and every file's SHA-256 is compared afterwards on both sides. The
transfer is not resumable: an interrupted copy has to be rerun.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if from == "" || to == "" {
				return fmt.Errorf("both --from and --to are required")
			}
			if from == to {
				return fmt.Errorf("--from and --to are the same context (%s)", from)
			}
			appName, volName, _ := splitTarget3(args[0])
			if volName == "" {
				return fmt.Errorf("target must be <app>/<volume>[/<replica>]")
			}
			store, err := contexts.Open()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 6*time.Hour)
			defer cancel()

			src, err := resolveVolumeEnd(ctx, store, from, args[0])
			if err != nil {
				return fmt.Errorf("--from %s: %w", from, err)
			}
			dst, err := resolveVolumeEnd(ctx, store, to, args[0])
			if err != nil {
				return fmt.Errorf("--to %s: %w", to, err)
			}

			cmd.Printf("Source:      %s  %s  %s\n", src.meta.Name, src.claim, src.hostPath)
			cmd.Printf("Destination: %s  %s  %s\n", dst.meta.Name, dst.claim, dst.hostPath)
			if !yes && !confirm(cmd, fmt.Sprintf("Replace %s/%s on %s with the copy from %s?", appName, volName, dst.meta.Name, src.meta.Name)) {
				return fmt.Errorf("aborted")
			}

			srcSSH, err := dial(src.meta.SSH)
			if err != nil {
				return fmt.Errorf("connecting to %s: %w", src.meta.Name, err)
			}
			defer srcSSH.Close()
			dstSSH, err := dial(dst.meta.SSH)
			if err != nil {
				return fmt.Errorf("connecting to %s: %w", dst.meta.Name, err)
			}
			defer dstSSH.Close()

			cmd.Println("Copying...")
			if err := streamVolume(srcSSH, dstSSH, src.hostPath, dst.hostPath); err != nil {
				return err
			}

			cmd.Println("Verifying checksums...")
			srcSum, err := treeChecksum(srcSSH, src.hostPath)
			if err != nil {
				return err
			}
			dstSum, err := treeChecksum(dstSSH, dst.hostPath)
			if err != nil {
				return err
			}
			if srcSum != dstSum {
				return fmt.Errorf("checksum mismatch after copy: source %s, destination %s — the destination volume is NOT a faithful copy", srcSum, dstSum)
			}
			cmd.Printf("Checksums match (%s).\n", srcSum)
			cmd.Printf("Copied %s/%s to %s. Start it with: jekyo --context %s up\n", appName, volName, dst.meta.Name, dst.meta.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "source context")
	cmd.Flags().StringVar(&to, "to", "", "destination context")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

// resolveVolumeEnd finds the claim behind <app>/<volume>[/<replica>] in one
// context, checks nothing mounts it, and resolves the node directory its
// PersistentVolume is backed by.
func resolveVolumeEnd(ctx context.Context, store *contexts.Store, ctxName, target string) (*volumeEnd, error) {
	m, err := store.Get(ctxName)
	if err != nil {
		return nil, err
	}
	client, err := kube.New(store.KubeconfigPath(m.Name))
	if err != nil {
		return nil, err
	}
	d := &deploy.Deployer{Client: client}
	appName, volName, claim, _, err := backupTarget(ctx, d, target)
	if err != nil {
		return nil, err
	}
	ns := compile.NamespaceFor(appName)

	pods, err := d.Client.Typed.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var mounting []string
	for _, p := range pods.Items {
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		for _, v := range p.Spec.Volumes {
			if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == claim {
				mounting = append(mounting, p.Name)
			}
		}
	}
	if len(mounting) > 0 {
		return nil, fmt.Errorf("pod(s) %s still mount %s: stop the app first with 'jekyo --context %s down %s' (the volume is kept)",
			strings.Join(mounting, ", "), claim, m.Name, appName)
	}

	pvc, err := d.Client.Typed.CoreV1().PersistentVolumeClaims(ns).Get(ctx, claim, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if pvc.Spec.VolumeName == "" {
		return nil, fmt.Errorf("claim %s is not bound yet (deploy the app once so the volume is created)", claim)
	}
	pv, err := d.Client.Typed.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	path := pvHostPath(pv)
	if path == "" {
		return nil, fmt.Errorf("volume %s is not node-backed (%s): only local-path/hostPath volumes can be copied this way", volName, pvc.Spec.VolumeName)
	}
	_ = appName
	return &volumeEnd{meta: m, deployer: d, claim: claim, hostPath: path}, nil
}

// pvHostPath returns the node directory a PersistentVolume is backed by;
// "" for anything not on the node's filesystem. k3s's local-path
// provisioner uses hostPath, static local volumes use local.
func pvHostPath(pv *corev1.PersistentVolume) string {
	if pv.Spec.HostPath != nil {
		return pv.Spec.HostPath.Path
	}
	if pv.Spec.Local != nil {
		return pv.Spec.Local.Path
	}
	return ""
}

// streamVolume pipes a tar of srcPath on one server into dstPath on
// another, through this machine. Stdout is kept clean of stderr so the
// archive is not corrupted, and --delete-on-destination semantics come
// from emptying dstPath first: a copy must not leave stale files behind.
func streamVolume(src, dst *sshx.Client, srcPath, dstPath string) error {
	if _, err := dst.Run(fmt.Sprintf("test -d %s && find %s -mindepth 1 -delete", shQuote(dstPath), shQuote(dstPath))); err != nil {
		return fmt.Errorf("clearing the destination volume: %w", err)
	}
	pr, pw := io.Pipe()
	errc := make(chan error, 1)
	go func() {
		_, err := src.StreamStdout(fmt.Sprintf("tar -C %s --numeric-owner -cf - .", shQuote(srcPath)), pw)
		// Closing with the error makes the receiving tar fail too, rather
		// than silently unpacking a truncated archive.
		pw.CloseWithError(err)
		errc <- err
	}()
	_, rxErr := dst.RunWithStdin(fmt.Sprintf("tar -C %s --numeric-owner -xf -", shQuote(dstPath)), pr)
	txErr := <-errc
	if txErr != nil {
		return fmt.Errorf("reading the source volume: %w", txErr)
	}
	if rxErr != nil {
		return fmt.Errorf("writing the destination volume: %w", rxErr)
	}
	return nil
}

// treeChecksum is one digest over every file's path and contents, so the
// two sides can be compared with a single value.
func treeChecksum(c *sshx.Client, path string) (string, error) {
	out, err := c.Run(fmt.Sprintf("cd %s && find . -type f -print0 | sort -z | xargs -0 -r sha256sum | sha256sum | cut -d' ' -f1", shQuote(path)))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
