package addons

import (
	"strings"
	"testing"
)

func TestRenderFull(t *testing.T) {
	files, err := Render(Values{
		Domain:           "example.com",
		AcmeEmail:        "ops@example.com",
		RegistryUser:     "jekyo",
		RegistryHtpasswd: "$2a$10$hash",
		RegistrySize:     "100Gi",
		VPNHost:          "vpn.example.com",
		VPNPasswordHash:  "$2a$10$vpnhash",
		GPU:              true,
		VPN:              true,
	})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]string{}
	for _, f := range files {
		byName[f.Name] = string(f.Data)
	}
	for _, want := range []string{
		"jekyo-contour.yaml", "jekyo-contour-extras.yaml", "jekyo-nvidia.yaml",
		"jekyo-registry.yaml", "jekyo-vpn.yaml", "jekyo-kcert.yaml",
	} {
		if byName[want] == "" {
			t.Fatalf("missing %s (got %v)", want, keys(byName))
		}
	}
	if !strings.Contains(byName["jekyo-registry.yaml"], "registry.example.com") {
		t.Fatal("registry ingress missing domain")
	}
	if !strings.Contains(byName["jekyo-registry.yaml"], RegistryClusterIP) {
		t.Fatal("registry missing fixed ClusterIP")
	}
	if !strings.Contains(byName["jekyo-kcert.yaml"], "ops@example.com") {
		t.Fatal("kcert missing acme email")
	}
	if !strings.Contains(byName["jekyo-vpn.yaml"], "vpn.example.com") {
		t.Fatal("vpn missing host")
	}
	if strings.Contains(byName["jekyo-vpn.yaml"], "{{") {
		t.Fatal("unrendered template markers in vpn manifest")
	}
}

func TestRenderMinimal(t *testing.T) {
	files, err := Render(Values{
		RegistryUser:     "jekyo",
		RegistryHtpasswd: "$2a$10$hash",
		RegistrySize:     "100Gi",
		VPNHost:          "1.2.3.4",
		VPNPasswordHash:  "$2a$10$vpnhash",
		VPN:              true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Name == "jekyo-kcert.yaml" {
			t.Fatal("kcert must not render without a domain")
		}
		if f.Name == "jekyo-nvidia.yaml" {
			t.Fatal("nvidia must not render without GPU")
		}
		if strings.Contains(string(f.Data), "kind: Ingress\n") {
			t.Fatalf("%s renders an Ingress without a domain", f.Name)
		}
	}
}

func TestRegistriesYAML(t *testing.T) {
	out := RegistriesYAML("jekyo", "s3cr3t")
	for _, want := range []string{RegistryHost, RegistryClusterIP, "s3cr3t"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
