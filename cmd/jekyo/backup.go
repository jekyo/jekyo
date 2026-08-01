package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/jekyo/jekyo/internal/compile"
	"github.com/jekyo/jekyo/internal/deploy"
	"github.com/jekyo/jekyo/internal/provision"
)

var patchTypeMerge = types.StrategicMergePatchType

func newBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Back up and restore volumes (restic to any S3-compatible storage)",
	}
	cmd.AddCommand(
		newBackupConfigCmd(),
		newBackupNowCmd(),
		newBackupLsCmd(),
		newBackupRestoreCmd(),
	)
	return cmd
}

func newBackupConfigCmd() *cobra.Command {
	var endpoint, bucket, accessKey, secretKey string
	var local bool
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Set the cluster's backup target (once per cluster)",
		Long: `Stores the backup target and an encryption password in the cluster. Every
volume with a backup: schedule uses it. Works with AWS S3, Cloudflare R2,
Backblaze B2, MinIO, or any S3-compatible storage, or with --local, a
directory on the server itself (` + compile.LocalBackupHostPath + `).
Mount a dedicated disk there; a backup on the disk that dies with the
data protects against mistakes, not hardware.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if local && (endpoint != "" || bucket != "" || accessKey != "" || secretKey != "") {
				return fmt.Errorf("--local and the S3 flags are mutually exclusive")
			}
			if !local && (endpoint == "" || bucket == "" || accessKey == "" || secretKey == "") {
				return fmt.Errorf("all of --endpoint, --bucket, --access-key, --secret-key are required (or use --local)")
			}
			d, err := newDeployer()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()

			repoBase := "s3:" + strings.TrimSuffix(endpoint, "/") + "/" + bucket
			if local {
				// backup pods always mount the local directory at /repo
				repoBase = "/repo"
			}
			data := map[string][]byte{
				"repo-base":  []byte(repoBase),
				"access-key": []byte(accessKey),
				"secret-key": []byte(secretKey),
			}
			// The restic password encrypts every repository. Generate it once
			// and NEVER rotate silently: a new password would orphan every
			// existing backup.
			existing, err := d.Client.Typed.CoreV1().Secrets("kube-system").Get(ctx, compile.BackupSecretName, metav1.GetOptions{})
			if err == nil && len(existing.Data["restic-password"]) > 0 {
				data["restic-password"] = existing.Data["restic-password"]
			} else {
				pw, err := provision.NewPassword()
				if err != nil {
					return err
				}
				data["restic-password"] = []byte(pw)
			}

			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: compile.BackupSecretName, Namespace: "kube-system"},
				Data:       data,
			}
			if err == nil {
				secret.ResourceVersion = existing.ResourceVersion
				_, err = d.Client.Typed.CoreV1().Secrets("kube-system").Update(ctx, secret, metav1.UpdateOptions{})
			} else {
				_, err = d.Client.Typed.CoreV1().Secrets("kube-system").Create(ctx, secret, metav1.CreateOptions{})
			}
			if err != nil {
				return err
			}
			target := repoBase
			if local {
				target = compile.LocalBackupHostPath + " on the server"
			}
			cmd.Println("Backup target configured:", target)
			cmd.Println("Volumes with a backup: schedule will use it on their next 'jekyo up'.")
			cmd.Println("IMPORTANT: the encryption password lives only in this cluster. Export a copy:")
			cmd.Println("  jekyo kubectl -- get secret -n kube-system jekyo-backup -o yaml > backup-secret.yaml")
			return nil
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "S3 endpoint, e.g. https://s3.eu-central-1.amazonaws.com or http://minio.example.com:9000")
	cmd.Flags().StringVar(&bucket, "bucket", "", "bucket name (must exist)")
	cmd.Flags().StringVar(&accessKey, "access-key", "", "S3 access key")
	cmd.Flags().StringVar(&secretKey, "secret-key", "", "S3 secret key")
	cmd.Flags().BoolVar(&local, "local", false, "store backups on the server at "+compile.LocalBackupHostPath)
	return cmd
}

// backupTarget resolves <app>/<volume> and the volume's PVC claim name from
// the deployed release manifest.
func backupTarget(ctx context.Context, d *deploy.Deployer, arg string) (appName, volName, claimName string, err error) {
	appName, volName = splitTarget(arg)
	if volName == "" {
		return "", "", "", fmt.Errorf("target must be <app>/<volume>")
	}
	ns := compile.NamespaceFor(appName)
	// The scheduled CronJob (if any) knows the claim; otherwise fall back to
	// PVCs present in the namespace.
	cj, err := d.Client.Typed.BatchV1().CronJobs(ns).Get(ctx, "backup-"+volName, metav1.GetOptions{})
	if err == nil {
		for _, v := range cj.Spec.JobTemplate.Spec.Template.Spec.Volumes {
			if v.PersistentVolumeClaim != nil {
				return appName, volName, v.PersistentVolumeClaim.ClaimName, nil
			}
		}
	}
	pvcs, lerr := d.Client.Typed.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{})
	if lerr != nil {
		return "", "", "", lerr
	}
	for _, p := range pvcs.Items {
		if p.Name == volName || strings.HasPrefix(p.Name, volName+"-") {
			return appName, volName, p.Name, nil
		}
	}
	return "", "", "", fmt.Errorf("no volume %q found in app %q (is the app deployed?)", volName, appName)
}

// runBackupJob creates a one-off restic job, waits for it, and returns its
// logs.
func runBackupJob(ctx context.Context, cmd *cobra.Command, d *deploy.Deployer, appName, volName, claimName string, command []string, readWrite bool, timeout time.Duration) (string, error) {
	ns := compile.NamespaceFor(appName)
	if err := d.EnsureBackupSecret(ctx, appName); err != nil {
		return "", err
	}
	spec := compile.BackupPodSpec(appName, volName, claimName, command, readWrite)
	backoff := int32(0)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "jekyo-" + volName + "-",
			Namespace:    ns,
			Labels:       map[string]string{compile.LabelApp: appName, "jekyo.io/backup": "true"},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{compile.LabelApp: appName, "jekyo.io/backup": "true"}},
				Spec:       spec,
			},
		},
	}
	created, err := d.Client.Typed.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}
	defer func() {
		policy := metav1.DeletePropagationBackground
		_ = d.Client.Typed.BatchV1().Jobs(ns).Delete(context.Background(), created.Name, metav1.DeleteOptions{PropagationPolicy: &policy})
	}()

	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timed out waiting for %s", created.Name)
		}
		j, err := d.Client.Typed.BatchV1().Jobs(ns).Get(ctx, created.Name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		if j.Status.Succeeded > 0 || j.Status.Failed > 0 {
			logs := jobLogs(ctx, d, ns, created.Name)
			if j.Status.Failed > 0 {
				return logs, fmt.Errorf("backup job failed:\n%s", lastLines(logs, 15))
			}
			return logs, nil
		}
		time.Sleep(3 * time.Second)
	}
}

func jobLogs(ctx context.Context, d *deploy.Deployer, ns, jobName string) string {
	pods, err := d.Client.Typed.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + jobName})
	if err != nil || len(pods.Items) == 0 {
		return ""
	}
	var out strings.Builder
	for _, p := range pods.Items {
		data, err := d.Client.Typed.CoreV1().Pods(ns).GetLogs(p.Name, &corev1.PodLogOptions{}).DoRaw(ctx)
		if err == nil {
			out.Write(data)
		}
	}
	return out.String()
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func newBackupNowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "now <app>/<volume>",
		Short: "Take a backup immediately",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := newDeployer()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			appName, volName, claim, err := backupTarget(ctx, d, args[0])
			if err != nil {
				return err
			}
			cmd.Printf("Backing up %s/%s (claim %s)...\n", appName, volName, claim)
			script := "restic snapshots >/dev/null 2>&1 || restic init; restic backup /data --tag jekyo --tag manual"
			logs, err := runBackupJob(ctx, cmd, d, appName, volName, claim, []string{"/bin/sh", "-c", script}, false, 15*time.Minute)
			if err != nil {
				return err
			}
			cmd.Println(lastLines(logs, 6))
			cmd.Println("Backup complete. List snapshots with: jekyo backup ls", args[0])
			return nil
		},
	}
}

func newBackupLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls <app>/<volume>",
		Short: "List snapshots for a volume",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := newDeployer()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			appName, volName, claim, err := backupTarget(ctx, d, args[0])
			if err != nil {
				return err
			}
			logs, err := runBackupJob(ctx, cmd, d, appName, volName, claim, []string{"restic", "snapshots", "--json"}, false, 5*time.Minute)
			if err != nil {
				return err
			}
			// restic --json prints a single JSON array on the last line.
			var snaps []struct {
				ShortID string    `json:"short_id"`
				Time    time.Time `json:"time"`
				Tags    []string  `json:"tags"`
			}
			for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
				if strings.HasPrefix(line, "[") {
					if err := json.Unmarshal([]byte(line), &snaps); err == nil {
						break
					}
				}
			}
			if len(snaps) == 0 {
				cmd.Println("No snapshots yet. Create one with: jekyo backup now", args[0])
				return nil
			}
			sort.Slice(snaps, func(i, j int) bool { return snaps[i].Time.Before(snaps[j].Time) })
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "SNAPSHOT\tCREATED\tTAGS")
			for _, s := range snaps {
				fmt.Fprintf(w, "%s\t%s\t%s\n", s.ShortID, ago(s.Time), strings.Join(s.Tags, ","))
			}
			w.Flush()
			cmd.Println("\nRestore with: jekyo backup restore", args[0], "<snapshot|latest>")
			return nil
		},
	}
}

func newBackupRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restore <app>/<volume> [snapshot]",
		Short: "Restore a volume from a snapshot (stops the app during restore)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			snapshot := "latest"
			if len(args) == 2 {
				snapshot = args[1]
			}
			d, err := newDeployer()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			appName, volName, claim, err := backupTarget(ctx, d, args[0])
			if err != nil {
				return err
			}
			if !confirm(cmd, fmt.Sprintf("Restore %s/%s from snapshot %q? Current volume contents will be REPLACED and the app briefly stopped.", appName, volName, snapshot)) {
				return fmt.Errorf("aborted")
			}

			ns := compile.NamespaceFor(appName)
			cmd.Println("Stopping workloads that mount the volume...")
			restored, err := scaleMounters(ctx, d, ns, claim, 0)
			if err != nil {
				return err
			}
			defer func() {
				cmd.Println("Starting workloads again...")
				for name, replicas := range restored {
					_ = scaleWorkload(context.Background(), d, ns, name, replicas)
				}
			}()
			if err := waitNoPodsWithClaim(ctx, d, ns, claim, 3*time.Minute); err != nil {
				return err
			}

			cmd.Println("Restoring", snapshot, "...")
			script := fmt.Sprintf("restic restore %s --target / --include /data --delete", snapshot)
			logs, err := runBackupJob(ctx, cmd, d, appName, volName, claim, []string{"/bin/sh", "-c", script}, true, 30*time.Minute)
			if err != nil {
				return err
			}
			cmd.Println(lastLines(logs, 4))
			cmd.Println("Restore complete.")
			return nil
		},
	}
}

// scaleMounters scales every Deployment/StatefulSet whose pod template
// mounts claim to the given replica count, returning the previous counts.
func scaleMounters(ctx context.Context, d *deploy.Deployer, ns, claim string, to int32) (map[string]int32, error) {
	prev := map[string]int32{}
	deps, err := d.Client.Typed.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return nil, err
	}
	for _, dep := range deps.Items {
		if podMountsClaim(dep.Spec.Template.Spec, claim) {
			prev["deploy/"+dep.Name] = *dep.Spec.Replicas
			if err := scaleWorkload(ctx, d, ns, "deploy/"+dep.Name, to); err != nil {
				return nil, err
			}
		}
	}
	stss, err := d.Client.Typed.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return nil, err
	}
	for _, sts := range stss.Items {
		mounts := podMountsClaim(sts.Spec.Template.Spec, claim)
		for _, v := range sts.Spec.VolumeClaimTemplates {
			if strings.HasPrefix(claim, v.Name+"-"+sts.Name) {
				mounts = true
			}
		}
		if mounts {
			prev["sts/"+sts.Name] = *sts.Spec.Replicas
			if err := scaleWorkload(ctx, d, ns, "sts/"+sts.Name, to); err != nil {
				return nil, err
			}
		}
	}
	return prev, nil
}

func podMountsClaim(spec corev1.PodSpec, claim string) bool {
	for _, v := range spec.Volumes {
		if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == claim {
			return true
		}
	}
	return false
}

func scaleWorkload(ctx context.Context, d *deploy.Deployer, ns, ref string, replicas int32) error {
	kind, name, _ := strings.Cut(ref, "/")
	patch := []byte(fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas))
	switch kind {
	case "deploy":
		_, err := d.Client.Typed.AppsV1().Deployments(ns).Patch(ctx, name, patchTypeMerge, patch, metav1.PatchOptions{})
		return err
	case "sts":
		_, err := d.Client.Typed.AppsV1().StatefulSets(ns).Patch(ctx, name, patchTypeMerge, patch, metav1.PatchOptions{})
		return err
	}
	return fmt.Errorf("unknown workload kind %q", kind)
}

func waitNoPodsWithClaim(ctx context.Context, d *deploy.Deployer, ns, claim string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		pods, err := d.Client.Typed.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}
		holding := 0
		for _, p := range pods.Items {
			if podMountsClaim(p.Spec, claim) && p.DeletionTimestamp == nil && p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed {
				holding++
			}
			if podMountsClaim(p.Spec, claim) && p.DeletionTimestamp != nil {
				holding++
			}
		}
		if holding == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("pods still mounting the volume after %s", timeout)
		}
		time.Sleep(3 * time.Second)
	}
}
