package provision

import (
	"strings"
	"testing"
)

const sampleFactsOutput = `===JEKYO===os
PRETTY_NAME="Ubuntu 24.04.1 LTS"
ID=ubuntu
VERSION_ID="24.04"
===JEKYO===arch
x86_64
===JEKYO===uid
0
===JEKYO===mem
MemTotal:        8123456 kB
===JEKYO===disk
/dev/vda1  50000000  10000000  38000000  21% /
===JEKYO===listeners
tcp   LISTEN 0      4096         0.0.0.0:22        0.0.0.0:*    users:(("sshd",pid=700,fd=3))
tcp   LISTEN 0      511          0.0.0.0:80        0.0.0.0:*    users:(("nginx",pid=812,fd=6))
udp   UNCONN 0      0            0.0.0.0:68        0.0.0.0:*    users:(("systemd-network",pid=500,fd=19))
===JEKYO===k3s
===JEKYO===otherk8s
===JEKYO===docker
installed
3
===JEKYO===ufw
Status: active
===JEKYO===nm
inactive
===JEKYO===clock
yes
===JEKYO===wireguard
ok
===JEKYO===swap
0
===JEKYO===gpu
01:00.0 VGA compatible controller: NVIDIA Corporation GA102 [GeForce RTX 3090] (rev a1)
02:00.0 3D controller: NVIDIA Corporation GA102 [GeForce RTX 3090] (rev a1)
===JEKYO===secureboot
SecureBoot enabled
===JEKYO===nouveau
1
===JEKYO===end
`

func TestParseFacts(t *testing.T) {
	f := ParseFacts(sampleFactsOutput)
	if f.OSID != "ubuntu" || f.OSVersion != "24.04" {
		t.Fatalf("os: %+v", f)
	}
	if f.Arch != "x86_64" || !f.IsRoot {
		t.Fatalf("arch/root: %+v", f)
	}
	if f.MemMB != 7933 {
		t.Fatalf("mem: %d", f.MemMB)
	}
	if f.DiskFreeMB != 37109 {
		t.Fatalf("disk: %d", f.DiskFreeMB)
	}
	if len(f.Listeners) != 3 || f.Listeners[1].Port != 80 || f.Listeners[1].Process != "nginx" {
		t.Fatalf("listeners: %+v", f.Listeners)
	}
	if f.Docker == nil || f.Docker.Containers != 3 {
		t.Fatalf("docker: %+v", f.Docker)
	}
	if !f.UFWActive || f.NMActive || !f.ClockSynced || !f.Wireguard || f.SwapActive {
		t.Fatalf("services: %+v", f)
	}
	if len(f.GPUs) != 2 || !f.SecureBoot || !f.Nouveau {
		t.Fatalf("gpu: %+v", f)
	}
	if f.K3sVersion != "" {
		t.Fatalf("k3s should be empty: %q", f.K3sVersion)
	}
}

func TestParseK3sVersion(t *testing.T) {
	if v := parseK3sVersion("k3s version v1.31.5+k3s1 (9b586704)"); v != "v1.31.5+k3s1" {
		t.Fatalf("got %q", v)
	}
}

func findCheck(t *testing.T, results []CheckResult, name string) CheckResult {
	t.Helper()
	for _, r := range results {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("check %q not found in %+v", name, results)
	return CheckResult{}
}

func TestPreflight(t *testing.T) {
	f := ParseFacts(sampleFactsOutput)
	cfg := Config{StoragePath: "/storage"}
	results := Preflight(f, cfg)

	if findCheck(t, results, "os").Status != Pass {
		t.Fatal("os should pass")
	}
	// nginx holds port 80 -> hard failure.
	if r := findCheck(t, results, "port 80/tcp"); r.Status != Fail || !strings.Contains(r.Detail, "nginx") {
		t.Fatalf("port 80: %+v", r)
	}
	if findCheck(t, results, "port 443/tcp").Status != Pass {
		t.Fatal("port 443 should pass")
	}
	// Docker installed but not holding required ports -> warn only.
	if r := findCheck(t, results, "docker"); r.Status != Warn {
		t.Fatalf("docker: %+v", r)
	}
	if r := findCheck(t, results, "firewall"); r.Status != Warn || !r.Fixable {
		t.Fatalf("firewall: %+v", r)
	}
	if r := findCheck(t, results, "gpu: secure boot"); r.Status != Warn {
		t.Fatalf("secure boot: %+v", r)
	}
	if r := findCheck(t, results, "gpu: nouveau"); !r.Fixable {
		t.Fatalf("nouveau: %+v", r)
	}
	if !Blocking(results) {
		t.Fatal("expected blocking (port 80)")
	}
	if len(Fixables(results)) != 2 { // ufw + nouveau
		t.Fatalf("fixables: %+v", Fixables(results))
	}
}

func TestPreflightConvergeExistingK3s(t *testing.T) {
	out := strings.ReplaceAll(sampleFactsOutput,
		`users:(("nginx",pid=812,fd=6))`, `users:(("k3s-server",pid=812,fd=6))`)
	out = strings.Replace(out, "===JEKYO===k3s\n", "===JEKYO===k3s\nk3s version v1.31.5+k3s1 (9b586704)\n", 1)
	f := ParseFacts(out)
	results := Preflight(f, Config{StoragePath: "/storage"})
	if r := findCheck(t, results, "port 80/tcp"); r.Status != Pass {
		t.Fatalf("k3s-held port should converge: %+v", r)
	}
	if r := findCheck(t, results, "existing k3s"); r.Status != Warn {
		t.Fatalf("existing k3s: %+v", r)
	}
	if Blocking(results) {
		t.Fatal("converge case must not block")
	}
}
