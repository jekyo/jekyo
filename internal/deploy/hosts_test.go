package deploy

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"

	"github.com/jekyo/jekyo/internal/compile"
	"github.com/jekyo/jekyo/internal/dsl"
)

func compiled(t *testing.T, y string) []runtime.Object {
	t.Helper()
	app, err := dsl.Parse([]byte(y), nil)
	if err != nil {
		t.Fatal(err)
	}
	objs, err := compile.Compile(app, compile.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return objs
}

func TestRouteHosts(t *testing.T) {
	objs := compiled(t, `
app: shop
services:
  web:
    image: x
    port: 80
    http:
      domains: [shop.com, api.shop.com]
  apex:
    http:
      domain: www.shop.com
      redirect: shop.com
`)
	got := routeHosts(objs)
	for _, h := range []string{"shop.com", "api.shop.com", "www.shop.com"} {
		if got[h] == "" {
			t.Fatalf("missing %s in %v", h, got)
		}
	}
	if len(got) != 3 {
		t.Fatalf("got %v", got)
	}
}

// A host served by another app (or by the server itself, e.g. the
// registry) is refused before anything is applied (issue #24).
func TestHostConflicts(t *testing.T) {
	mine := map[string]string{"shop.com": "ingress web", "api.shop.com": "ingress web"}
	owners := map[string]string{"api.shop.com": "app blog", "registry.shop.com": "kube-system/registry"}
	err := hostConflicts("shop", mine, owners)
	if err == nil || !strings.Contains(err.Error(), "api.shop.com") || !strings.Contains(err.Error(), "app blog") {
		t.Fatalf("got %v", err)
	}
	if err := hostConflicts("shop", mine, map[string]string{"other.com": "app blog"}); err != nil {
		t.Fatalf("no overlap: %v", err)
	}
}
