package main

import (
	"context"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/jekyo/jekyo/internal/contexts"
)

func TestMergeClusterCreds(t *testing.T) {
	ghcr := contexts.Login{Username: "u", Password: "p"}
	quay := contexts.Login{Username: "q", Password: "qp"}
	cases := []struct {
		name               string
		local              contexts.Meta
		cluster            clusterCreds
		wantVPN            string
		wantLogins         int
		wantChanged, wantP bool
	}{
		{"new machine gets everything",
			contexts.Meta{VPN: &contexts.VPN{}},
			clusterCreds{VPNPassword: "s3", Logins: map[string]contexts.Login{"ghcr.io": ghcr}},
			"s3", 1, true, false},
		{"old server is backfilled",
			contexts.Meta{VPN: &contexts.VPN{Password: "s3"}, Logins: map[string]contexts.Login{"ghcr.io": ghcr}},
			clusterCreds{},
			"s3", 1, false, true},
		{"in sync",
			contexts.Meta{VPN: &contexts.VPN{Password: "s3"}, Logins: map[string]contexts.Login{"ghcr.io": ghcr}},
			clusterCreds{VPNPassword: "s3", Logins: map[string]contexts.Login{"ghcr.io": ghcr}},
			"s3", 1, false, false},
		{"server password wins",
			contexts.Meta{VPN: &contexts.VPN{Password: "old"}},
			clusterCreds{VPNPassword: "new"},
			"new", 0, true, false},
		{"logins are a union",
			contexts.Meta{VPN: &contexts.VPN{Password: "s3"}, Logins: map[string]contexts.Login{"quay.io": quay}},
			clusterCreds{VPNPassword: "s3", Logins: map[string]contexts.Login{"ghcr.io": ghcr}},
			"s3", 2, true, true},
		{"no vpn, nothing anywhere",
			contexts.Meta{},
			clusterCreds{},
			"", 0, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.local
			changed, push := mergeClusterCreds(&m, tc.cluster)
			if changed != tc.wantChanged || push != tc.wantP {
				t.Fatalf("changed=%v push=%v, want %v %v", changed, push, tc.wantChanged, tc.wantP)
			}
			if m.VPN != nil && m.VPN.Password != tc.wantVPN {
				t.Errorf("vpn password %q, want %q", m.VPN.Password, tc.wantVPN)
			}
			if len(m.Logins) != tc.wantLogins {
				t.Errorf("logins %v, want %d", m.Logins, tc.wantLogins)
			}
		})
	}
}

func TestClusterCredsRoundTrip(t *testing.T) {
	ctx := context.Background()
	c := fake.NewSimpleClientset()
	if cc, err := readClusterCreds(ctx, c); err != nil || cc.VPNPassword != "" || cc.Logins != nil {
		t.Fatalf("missing secret: %+v %v", cc, err)
	}
	m := contexts.Meta{VPN: &contexts.VPN{Password: "s3"}, Logins: map[string]contexts.Login{"ghcr.io": {Username: "u", Password: "p"}}}
	for range 2 { // create, then update in place
		if err := writeClusterCreds(ctx, c, m); err != nil {
			t.Fatal(err)
		}
	}
	cc, err := readClusterCreds(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if cc.VPNPassword != "s3" || cc.Logins["ghcr.io"].Password != "p" {
		t.Fatalf("round trip: %+v", cc)
	}
}

func TestParseServerCreds(t *testing.T) {
	out := "      username: jekyo\n      password: \"ABC\"\nvpn-password: XYZ\n"
	sc := parseServerCreds(out)
	if sc.RegistryUser != "jekyo" || sc.RegistryPassword != "ABC" || sc.VPNPassword != "XYZ" {
		t.Fatalf("%+v", sc)
	}
	// a server without the Secret echoes an empty value
	if sc := parseServerCreds("vpn-password:\n"); sc.VPNPassword != "" {
		t.Fatalf("%+v", sc)
	}
}
