// Package deploy applies compiled app objects to the cluster: server-side
// apply with field manager "jekyo", pruning of labeled leftovers, and
// helm-style release records in Secrets so history/rollback need no local
// state.
package deploy

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/jekyo/jekyo/internal/compile"
	"github.com/jekyo/jekyo/internal/kube"
)

const (
	fieldManager = "jekyo"
	releaseLabel = "jekyo.io/release"
)

type Deployer struct {
	Client *kube.Client
}

// Apply server-side-applies all objects in order (namespace first), prunes
// stale labeled resources, and records a release. Returns the new revision.
func (d *Deployer) Apply(ctx context.Context, appName string, objs []runtime.Object) (int, error) {
	applied := map[string]bool{} // "resource/namespace/name"

	for _, obj := range objs {
		u, err := toUnstructured(obj)
		if err != nil {
			return 0, err
		}
		gvr, err := kube.GVR(u.GroupVersionKind())
		if err != nil {
			return 0, err
		}
		ns := u.GetNamespace()
		ri := d.Client.Dynamic.Resource(gvr).Namespace(ns)
		if ns == "" {
			ri = d.Client.Dynamic.Resource(gvr)
		}
		if _, err := ri.Apply(ctx, u.GetName(), u, metav1.ApplyOptions{FieldManager: fieldManager, Force: true}); err != nil {
			return 0, fmt.Errorf("applying %s %s: %w", u.GetKind(), u.GetName(), err)
		}
		applied[gvr.Resource+"/"+ns+"/"+u.GetName()] = true
	}

	if err := d.prune(ctx, appName, applied); err != nil {
		return 0, err
	}
	return d.recordRelease(ctx, appName, objs)
}

// prune deletes resources labeled for this app that the current render no
// longer produces (e.g. a service removed from jekyo.yaml).
func (d *Deployer) prune(ctx context.Context, appName string, applied map[string]bool) error {
	sel := compile.LabelApp + "=" + appName
	ns := compile.NamespaceFor(appName)
	for _, gvr := range kube.PrunableGVRs() {
		list, err := d.Client.Dynamic.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{LabelSelector: sel})
		if err != nil {
			if errors.IsNotFound(err) {
				continue // CRD (e.g. HTTPProxy) not installed on this cluster
			}
			return fmt.Errorf("prune: listing %s: %w", gvr.Resource, err)
		}
		for _, item := range list.Items {
			key := gvr.Resource + "/" + item.GetNamespace() + "/" + item.GetName()
			if !applied[key] {
				if err := d.Client.Dynamic.Resource(gvr).Namespace(ns).Delete(ctx, item.GetName(), metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
					return fmt.Errorf("prune: deleting %s %s: %w", gvr.Resource, item.GetName(), err)
				}
			}
		}
	}
	return nil
}

// recordRelease stores the rendered manifest as revision N+1 in a Secret.
func (d *Deployer) recordRelease(ctx context.Context, appName string, objs []runtime.Object) (int, error) {
	manifest, err := RenderYAML(objs)
	if err != nil {
		return 0, err
	}
	revs, err := d.Releases(ctx, appName)
	if err != nil {
		return 0, err
	}
	next := 1
	if len(revs) > 0 {
		next = revs[len(revs)-1].Revision + 1
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("jekyo-release-v%d", next),
			Namespace: compile.NamespaceFor(appName),
			Labels: map[string]string{
				compile.LabelApp: appName,
				releaseLabel:     strconv.Itoa(next),
			},
			Annotations: map[string]string{"jekyo.io/deployed-at": time.Now().UTC().Format(time.RFC3339)},
		},
		Type: "jekyo.io/release",
		Data: map[string][]byte{"manifest.yaml": []byte(manifest)},
	}
	if _, err := d.Client.Typed.CoreV1().Secrets(compile.NamespaceFor(appName)).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		return 0, err
	}
	// Keep the last 10 revisions.
	for len(revs) >= 10 {
		if err := d.Client.Typed.CoreV1().Secrets(compile.NamespaceFor(appName)).Delete(ctx, revs[0].Name, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
			return 0, err
		}
		revs = revs[1:]
	}
	return next, nil
}

// Release is one recorded deployment of an app.
type Release struct {
	Name       string
	Revision   int
	DeployedAt time.Time
	Manifest   []byte
}

// Releases lists an app's releases, oldest first.
func (d *Deployer) Releases(ctx context.Context, appName string) ([]Release, error) {
	list, err := d.Client.Typed.CoreV1().Secrets(compile.NamespaceFor(appName)).List(ctx, metav1.ListOptions{LabelSelector: releaseLabel})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Release
	for _, s := range list.Items {
		rev, err := strconv.Atoi(s.Labels[releaseLabel])
		if err != nil {
			continue
		}
		at, _ := time.Parse(time.RFC3339, s.Annotations["jekyo.io/deployed-at"])
		out = append(out, Release{Name: s.Name, Revision: rev, DeployedAt: at, Manifest: s.Data["manifest.yaml"]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Revision < out[j].Revision })
	return out, nil
}

// AppInfo summarizes one deployed app for `jekyo ls`.
type AppInfo struct {
	Name      string
	Revision  int
	Services  []string
	PodsReady int
	PodsTotal int
	Domains   []string
	Age       time.Duration
}

// List finds all JEKYO apps (namespaces carrying the app label).
func (d *Deployer) List(ctx context.Context) ([]AppInfo, error) {
	nss, err := d.Client.Typed.CoreV1().Namespaces().List(ctx, metav1.ListOptions{LabelSelector: compile.LabelApp})
	if err != nil {
		return nil, err
	}
	var out []AppInfo
	for _, ns := range nss.Items {
		appName := ns.Labels[compile.LabelApp]
		if appName == "" {
			continue
		}
		info := AppInfo{Name: appName, Age: time.Since(ns.CreationTimestamp.Time)}

		pods, err := d.Client.Typed.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{LabelSelector: compile.LabelApp})
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, p := range pods.Items {
			info.PodsTotal++
			if isReady(p) {
				info.PodsReady++
			}
			if svc := p.Labels[compile.LabelService]; svc != "" && !seen[svc] {
				seen[svc] = true
				info.Services = append(info.Services, svc)
			}
		}
		sort.Strings(info.Services)

		ings, err := d.Client.Typed.NetworkingV1().Ingresses(ns.Name).List(ctx, metav1.ListOptions{LabelSelector: compile.LabelApp})
		if err == nil {
			for _, ing := range ings.Items {
				for _, r := range ing.Spec.Rules {
					if r.Host != "" {
						info.Domains = append(info.Domains, r.Host)
					}
				}
			}
		}
		if revs, err := d.Releases(ctx, appName); err == nil && len(revs) > 0 {
			info.Revision = revs[len(revs)-1].Revision
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Down removes an app's workloads. Volumes (PVCs) and the namespace survive
// unless withVolumes is set, in which case the whole namespace goes.
func (d *Deployer) Down(ctx context.Context, appName string, withVolumes bool) error {
	if withVolumes {
		err := d.Client.Typed.CoreV1().Namespaces().Delete(ctx, compile.NamespaceFor(appName), metav1.DeleteOptions{})
		if errors.IsNotFound(err) {
			return fmt.Errorf("app %q is not deployed", appName)
		}
		return err
	}
	return d.prune(ctx, appName, map[string]bool{})
}

func isReady(p corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func toUnstructured(obj runtime.Object) (*unstructured.Unstructured, error) {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}
	u := &unstructured.Unstructured{Object: m}
	// Strip null creationTimestamp noise so renders stay clean.
	unstructured.RemoveNestedField(u.Object, "metadata", "creationTimestamp")
	unstructured.RemoveNestedField(u.Object, "spec", "template", "metadata", "creationTimestamp")
	unstructured.RemoveNestedField(u.Object, "status")
	return u, nil
}

// EnsureBackupSecret copies the cluster backup target (kube-system) into
// the app's namespace so backup pods can reference it.
func (d *Deployer) EnsureBackupSecret(ctx context.Context, appName string) error {
	src, err := d.Client.Typed.CoreV1().Secrets("kube-system").Get(ctx, compile.BackupSecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("no backup target configured; run 'jekyo backup config' first (%w)", err)
	}
	ns := compile.NamespaceFor(appName)
	dst := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: compile.BackupSecretName, Namespace: ns,
			Labels: map[string]string{compile.LabelApp: appName}},
		Data: src.Data,
	}
	existing, err := d.Client.Typed.CoreV1().Secrets(ns).Get(ctx, compile.BackupSecretName, metav1.GetOptions{})
	if err == nil {
		dst.ResourceVersion = existing.ResourceVersion
		_, err = d.Client.Typed.CoreV1().Secrets(ns).Update(ctx, dst, metav1.UpdateOptions{})
		return err
	}
	_, err = d.Client.Typed.CoreV1().Secrets(ns).Create(ctx, dst, metav1.CreateOptions{})
	return err
}

// ApplyManifest re-applies a previously rendered manifest (rollback):
// server-side apply of each document, prune, and a new release record.
func (d *Deployer) ApplyManifest(ctx context.Context, appName string, manifest []byte) (int, error) {
	var objs []runtime.Object
	for _, doc := range strings.Split(string(manifest), "\n---\n") {
		doc = strings.TrimSpace(doc)
		if doc == "" {
			continue
		}
		var m map[string]any
		if err := sigsyaml.Unmarshal([]byte(doc), &m); err != nil {
			return 0, fmt.Errorf("parsing stored manifest: %w", err)
		}
		if len(m) == 0 {
			continue
		}
		objs = append(objs, &unstructured.Unstructured{Object: m})
	}
	if len(objs) == 0 {
		return 0, fmt.Errorf("stored manifest is empty")
	}
	return d.Apply(ctx, appName, objs)
}

// RenderYAML serializes objects as a multi-document YAML stream.
func RenderYAML(objs []runtime.Object) (string, error) {
	var docs []string
	for _, obj := range objs {
		u, err := toUnstructured(obj)
		if err != nil {
			return "", err
		}
		data, err := sigsyaml.Marshal(u.Object)
		if err != nil {
			return "", err
		}
		docs = append(docs, string(data))
	}
	return strings.Join(docs, "---\n"), nil
}
