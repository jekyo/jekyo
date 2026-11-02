package dsl

import (
	"strings"
	"testing"
)

// Issue #38: http hosts interpolate like env, and resolve before the DNS
// check runs — otherwise validation rejects the literal ${VAR}.
func TestInterpolateHTTPHosts(t *testing.T) {
	y := `
app: a
services:
  one:
    image: x
    port: 80
    http:
      domain: ${API_DOMAIN}
  many:
    image: x
    port: 80
    http:
      # ${VAR} must be quoted inside a flow list: bare "{" starts a
      # YAML flow mapping.
      domains: ["${WWW_DOMAIN}", static.example.com]
  go-www:
    http:
      domain: old.example.com
      redirect: ${WWW_DOMAIN}
`
	env := map[string]string{"API_DOMAIN": "api.example.com", "WWW_DOMAIN": "www.example.com"}
	app, err := Parse([]byte(y), env)
	if err != nil {
		t.Fatal(err)
	}
	if got := app.Services["one"].HTTP.Domain; got != "api.example.com" {
		t.Errorf("http.domain: got %q", got)
	}
	if got := app.Services["many"].HTTP.Domains; got[0] != "www.example.com" || got[1] != "static.example.com" {
		t.Errorf("http.domains: got %q", got)
	}
	if got := app.Services["go-www"].HTTP.Redirect; got != "www.example.com" {
		t.Errorf("http.redirect: got %q", got)
	}
}

// An unset variable must fail loudly here too: a domain that silently
// became "" would deploy something unreachable.
func TestInterpolateHTTPDomainUnset(t *testing.T) {
	y := "app: a\nservices:\n  s:\n    image: x\n    port: 80\n    http:\n      domain: ${NOPE_UNSET_DOMAIN}\n"
	_, err := Parse([]byte(y), nil)
	if err == nil || !strings.Contains(err.Error(), "NOPE_UNSET_DOMAIN") {
		t.Fatalf("want the variable named, got: %v", err)
	}
	// and it must not be reported as a bad DNS name (issue #38's complaint)
	if err != nil && strings.Contains(err.Error(), "not a valid domain") {
		t.Fatalf("error blames the domain, not the variable: %v", err)
	}
}

// Issue #34: build secrets interpolate and read from disk, and never
// appear as build args.
func TestBuildSecretsInterpolate(t *testing.T) {
	y := `
app: a
services:
  s:
    build:
      context: .
      secrets:
        netrc:
          from: ${NETRC}
`
	app, err := Parse([]byte(y), map[string]string{"NETRC": "machine github.com password tok"})
	if err != nil {
		t.Fatal(err)
	}
	got := app.Services["s"].Build.Secrets["netrc"]
	if got.Content != "machine github.com password tok" {
		t.Fatalf("content: %q", got.Content)
	}
	if len(app.Services["s"].Build.Args) != 0 {
		t.Fatal("secrets must not become build args")
	}
}

func TestBuildSSHFlag(t *testing.T) {
	y := "app: a\nservices:\n  s:\n    build:\n      context: .\n      ssh: true\n"
	app, err := Parse([]byte(y), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !app.Services["s"].Build.SSH {
		t.Fatal("build.ssh not parsed")
	}
}

// Issue #35: a CronJob's pod can mount a PVC, so schedule+volumes is
// allowed while the identity-bound fields stay excluded.
func TestScheduleAllowsVolumes(t *testing.T) {
	y := `
app: a
services:
  feeder:
    image: alpine
    schedule: "0 3 * * *"
    volumes:
      state: /var/lib/feeder
volumes:
  state:
    size: 1Gi
`
	if _, err := Parse([]byte(y), nil); err != nil {
		t.Fatalf("schedule with volumes must be accepted: %v", err)
	}
}

func TestScheduleStillExcludesIdentityFields(t *testing.T) {
	cases := []struct{ name, yaml string }{
		{"http", "app: a\nservices:\n  s:\n    image: x\n    port: 80\n    schedule: \"0 3 * * *\"\n    http:\n      domain: a.com\n"},
		{"replicas", "app: a\nservices:\n  s:\n    image: x\n    schedule: \"0 3 * * *\"\n    replicas: 2\n"},
		{"expose", "app: a\nservices:\n  s:\n    image: x\n    schedule: \"0 3 * * *\"\n    expose:\n      - port: 5432\n        node: 30432\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml), nil)
			if err == nil || !strings.Contains(err.Error(), "schedule is mutually exclusive") {
				t.Fatalf("got: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "volumes") {
				t.Fatalf("volumes must no longer be listed: %v", err)
			}
		})
	}
}

// Issue #36: replicas > 1 needs per-replica volumes, and says so.
func TestReplicasNeedPerReplicaVolumes(t *testing.T) {
	y := "app: a\nservices:\n  s:\n    image: x\n    replicas: 3\n    volumes:\n      d: /data\nvolumes:\n  d:\n    size: 1Gi\n"
	_, err := Parse([]byte(y), nil)
	if err == nil || !strings.Contains(err.Error(), "per-replica: true") {
		t.Fatalf("want the remedy named, got: %v", err)
	}
}

func TestReplicasWithPerReplicaVolumes(t *testing.T) {
	y := "app: a\nservices:\n  s:\n    image: x\n    replicas: 3\n    volumes:\n      d: /data\nvolumes:\n  d:\n    size: 1Gi\n    per-replica: true\n"
	app, err := Parse([]byte(y), nil)
	if err != nil {
		t.Fatalf("per-replica volumes must allow replicas > 1: %v", err)
	}
	if !app.Volumes["d"].PerReplica {
		t.Fatal("per-replica not parsed")
	}
}

// Issue #33: apparmor accepts exactly the three Kubernetes profile types.
func TestAppArmorValues(t *testing.T) {
	for _, v := range []string{"runtime-default", "unconfined", "localhost/jekyo-postgres"} {
		y := "app: a\nservices:\n  s:\n    image: x\n    security:\n      apparmor: " + v + "\n"
		if _, err := Parse([]byte(y), nil); err != nil {
			t.Errorf("apparmor %q rejected: %v", v, err)
		}
	}
	for _, v := range []string{"off", "localhost/", "Unconfined", "runtime_default"} {
		y := "app: a\nservices:\n  s:\n    image: x\n    security:\n      apparmor: \"" + v + "\"\n"
		if _, err := Parse([]byte(y), nil); err == nil {
			t.Errorf("apparmor %q accepted", v)
		}
	}
}

func TestAppArmorOnSidecarAndInit(t *testing.T) {
	y := `
app: a
services:
  s:
    image: x
    init:
      migrate:
        command: ["true"]
        security:
          apparmor: nonsense
`
	_, err := Parse([]byte(y), nil)
	if err == nil || !strings.Contains(err.Error(), "init migrate security.apparmor") {
		t.Fatalf("got: %v", err)
	}
}

func TestBuildSecretsErrors(t *testing.T) {
	cases := []struct{ name, yaml, want string }{
		{"no source", "app: a\nservices:\n  s:\n    build:\n      context: .\n      secrets:\n        tok: {}\n", "needs a local path or from:"},
		{"both sources", "app: a\nservices:\n  s:\n    build:\n      context: .\n      secrets:\n        tok:\n          path: ./t\n          from: ${X}\n", "mutually exclusive"},
		{"bad id", "app: a\nservices:\n  s:\n    build:\n      context: .\n      secrets:\n        \"bad id!\":\n          from: v\n", "BuildKit secret id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml), map[string]string{"X": "v"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got: %v", tc.want, err)
			}
		})
	}
}

// gpu: 1 and gpu: 2 must be distinguishable: the count was inert before
// issue #37.
func TestGPUCountOrOne(t *testing.T) {
	cases := []struct {
		gpu  GPU
		want int
	}{
		{GPU{Count: 2}, 2},
		{GPU{Count: 1}, 1},
		{GPU{Devices: "1"}, 1},
		{GPU{}, 1},
	}
	for _, tc := range cases {
		if got := tc.gpu.CountOrOne(); got != tc.want {
			t.Errorf("%+v: got %d want %d", tc.gpu, got, tc.want)
		}
	}
}
