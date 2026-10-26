package main

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/jekyo/jekyo/internal/contexts"
	"github.com/jekyo/jekyo/internal/kube"
)

// The server keeps the secrets it cannot recover on its own (wg-easy only
// stores a bcrypt hash; external registry logins exist only client-side) in
// one Secret, so `jekyo context add` on a new machine gets a working
// context. The kubeconfig is cluster-admin already: this widens nothing.
const (
	credsNamespace = "kube-system"
	credsSecret    = "jekyo-credentials"
	credsVPNKey    = "vpn-password"
	credsLoginsKey = "logins"
)

// clusterCreds is what the server-side Secret holds.
type clusterCreds struct {
	VPNPassword string
	Logins      map[string]contexts.Login
}

// readClusterCreds returns the server's copy; a server without the Secret
// (installed before it existed) yields an empty one.
func readClusterCreds(ctx context.Context, c kubernetes.Interface) (clusterCreds, error) {
	sec, err := c.CoreV1().Secrets(credsNamespace).Get(ctx, credsSecret, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return clusterCreds{}, nil
	}
	if err != nil {
		return clusterCreds{}, err
	}
	cc := clusterCreds{VPNPassword: string(sec.Data[credsVPNKey])}
	if raw, ok := sec.Data[credsLoginsKey]; ok {
		if err := json.Unmarshal(raw, &cc.Logins); err != nil {
			return clusterCreds{}, fmt.Errorf("decoding %s/%s: %w", credsNamespace, credsSecret, err)
		}
	}
	return cc, nil
}

// writeClusterCreds stores the context's VPN password and registry logins
// on the server, replacing what was there.
func writeClusterCreds(ctx context.Context, c kubernetes.Interface, m contexts.Meta) error {
	logins := m.Logins
	if logins == nil {
		logins = map[string]contexts.Login{}
	}
	raw, err := json.Marshal(logins)
	if err != nil {
		return err
	}
	data := map[string][]byte{credsLoginsKey: raw}
	if m.VPN != nil && m.VPN.Password != "" {
		data[credsVPNKey] = []byte(m.VPN.Password)
	}
	secrets := c.CoreV1().Secrets(credsNamespace)
	sec, err := secrets.Get(ctx, credsSecret, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = secrets.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: credsSecret, Namespace: credsNamespace,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "jekyo"}},
			Type: corev1.SecretTypeOpaque,
			Data: data,
		}, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	sec.Data = data
	_, err = secrets.Update(ctx, sec, metav1.UpdateOptions{})
	return err
}

// mergeClusterCreds reconciles a local context with the server's copy and
// reports whether the local one changed (save it) and whether the server's
// lacks something only the local one has (write it back). The server's VPN
// password wins; logins are a union where the server wins per host, so a
// machine that never synced cannot wipe another's logins (the cost: a
// logout only sticks once every machine holding that login has dropped it).
func mergeClusterCreds(m *contexts.Meta, cc clusterCreds) (localChanged, push bool) {
	if m.VPN != nil {
		switch {
		case cc.VPNPassword != "" && cc.VPNPassword != m.VPN.Password:
			m.VPN.Password = cc.VPNPassword
			localChanged = true
		case cc.VPNPassword == "" && m.VPN.Password != "":
			push = true
		}
	}
	merged := maps.Clone(m.Logins)
	for host, l := range cc.Logins {
		if merged == nil {
			merged = map[string]contexts.Login{}
		}
		merged[host] = l
	}
	if !maps.Equal(merged, m.Logins) {
		m.Logins = merged
		localChanged = true
	}
	if !maps.Equal(merged, cc.Logins) {
		push = true
	}
	return localChanged, push
}

// syncCreds pulls the server's credentials into the local context (saving
// it when they differ) and backfills the server from the local context when
// it lacks them. Failures are returned for the caller to warn about: a
// missing Secret must never block a deploy.
func syncCreds(store *contexts.Store, m *contexts.Meta) error {
	kc, err := kube.New(store.KubeconfigPath(m.Name))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cc, err := readClusterCreds(ctx, kc.Typed)
	if err != nil {
		return err
	}
	changed, push := mergeClusterCreds(m, cc)
	if changed {
		if err := store.Save(*m); err != nil {
			return err
		}
	}
	if push {
		return writeClusterCreds(ctx, kc.Typed, *m)
	}
	return nil
}

// pushCreds overwrites the server's copy with the local context, after a
// local change (registry login/logout, install).
func pushCreds(store *contexts.Store, m contexts.Meta) error {
	kc, err := kube.New(store.KubeconfigPath(m.Name))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return writeClusterCreds(ctx, kc.Typed, m)
}
