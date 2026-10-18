package deploy

import (
	"context"
	"fmt"
	"sort"
	"strings"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/jekyo/jekyo/internal/compile"
)

var httpProxyGVR = schema.GroupVersionResource{Group: "projectcontour.io", Version: "v1", Resource: "httpproxies"}

// routeHosts maps every host the rendered objects route to the object
// that routes it: Ingress rules and redirect HTTPProxy virtualhosts.
func routeHosts(objs []runtime.Object) map[string]string {
	hosts := map[string]string{}
	for _, o := range objs {
		switch v := o.(type) {
		case *networkingv1.Ingress:
			for _, r := range v.Spec.Rules {
				if r.Host != "" {
					hosts[r.Host] = "ingress " + v.Name
				}
			}
		case *unstructured.Unstructured:
			if v.GetKind() == "HTTPProxy" {
				if fqdn, _, _ := unstructured.NestedString(v.Object, "spec", "virtualhost", "fqdn"); fqdn != "" {
					hosts[fqdn] = "httpproxy " + v.GetName()
				}
			}
		}
	}
	return hosts
}

// hostOwners lists the hosts already routed on the cluster by anything
// but appName: other apps ("app blog") and the server's own routes, such
// as the registry ("kube-system/registry").
func (d *Deployer) hostOwners(ctx context.Context, appName string) (map[string]string, error) {
	owners := map[string]string{}
	owner := func(m metav1.Object) (string, bool) {
		if a := m.GetLabels()[compile.LabelApp]; a != "" {
			return "app " + a, a != appName
		}
		return m.GetNamespace() + "/" + m.GetName(), true
	}
	ings, err := d.Client.Typed.NetworkingV1().Ingresses("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing ingresses: %w", err)
	}
	for i := range ings.Items {
		if who, other := owner(&ings.Items[i]); other {
			for _, r := range ings.Items[i].Spec.Rules {
				if r.Host != "" {
					owners[r.Host] = who
				}
			}
		}
	}
	proxies, err := d.Client.Dynamic.Resource(httpProxyGVR).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return nil, fmt.Errorf("listing httpproxies: %w", err)
	}
	if proxies != nil {
		for i := range proxies.Items {
			p := &proxies.Items[i]
			if who, other := owner(p); other {
				if fqdn, _, _ := unstructured.NestedString(p.Object, "spec", "virtualhost", "fqdn"); fqdn != "" {
					owners[fqdn] = who
				}
			}
		}
	}
	return owners, nil
}

// hostConflicts refuses hosts another owner already routes: Contour would
// accept both and the two apps would fight over traffic and certificates
// (issue #24).
func hostConflicts(appName string, mine, owners map[string]string) error {
	var clash []string
	for h := range mine {
		if who, ok := owners[h]; ok {
			clash = append(clash, fmt.Sprintf("%s is already served by %s", h, who))
		}
	}
	if len(clash) == 0 {
		return nil
	}
	sort.Strings(clash)
	return fmt.Errorf("app %s cannot take over a domain:\n  %s\nremove it from the other app first, or pick another domain",
		appName, strings.Join(clash, "\n  "))
}
