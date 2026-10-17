package provision

import (
	"errors"
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
===JEKYO===aptmissing
===JEKYO===apparmor
===JEKYO===addrs
127.0.0.1
203.0.113.7
===JEKYO===lvm
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

// gotchas swaps sections of the sample for the failures met installing
// real servers: a stale local apt repo, Ubuntu 26.04's wg-quick AppArmor
// profile, a root LV far smaller than its volume group.
func gotchas(t *testing.T) Facts {
	t.Helper()
	out := strings.Replace(sampleFactsOutput, "===JEKYO===aptmissing\n",
		"===JEKYO===aptmissing\n/etc/apt/sources.list.d/nvidia-driver-local.list /var/nvidia-driver-local-repo\n", 1)
	out = strings.Replace(out, "===JEKYO===apparmor\n", "===JEKYO===apparmor\nwg-quick (enforce)\n", 1)
	out = strings.Replace(out, "===JEKYO===lvm\n",
		"===JEKYO===lvm\n/dev/mapper/ubuntu--vg-ubuntu--lv 107374182400 1846835822592\n", 1)
	out = strings.Replace(out, `VERSION_ID="24.04"`, `VERSION_ID="26.04"`, 1)
	return ParseFacts(out)
}

func TestParseFactsGotchas(t *testing.T) {
	f := gotchas(t)
	if len(f.AptMissing) != 1 || f.AptMissing[0].File != "/etc/apt/sources.list.d/nvidia-driver-local.list" ||
		f.AptMissing[0].Path != "/var/nvidia-driver-local-repo" {
		t.Fatalf("apt: %+v", f.AptMissing)
	}
	if !f.WGQuickAppArmor {
		t.Fatal("wg-quick apparmor profile not parsed")
	}
	if len(f.Addrs) != 2 || f.Addrs[1] != "203.0.113.7" {
		t.Fatalf("addrs: %v", f.Addrs)
	}
	if f.LVM == nil || f.LVM.Device != "/dev/mapper/ubuntu--vg-ubuntu--lv" ||
		f.LVM.LVBytes != 107374182400 || f.LVM.VGFreeBytes != 1846835822592 {
		t.Fatalf("lvm: %+v", f.LVM)
	}
	clean := ParseFacts(sampleFactsOutput)
	if len(clean.AptMissing) != 0 || clean.WGQuickAppArmor || clean.LVM != nil {
		t.Fatalf("clean sample parsed gotchas: %+v", clean)
	}
}

func TestPreflightGotchas(t *testing.T) {
	results := Preflight(gotchas(t), Config{IP: "203.0.113.7", StoragePath: "/storage"})

	if r := findCheck(t, results, "os"); r.Status != Pass {
		t.Fatalf("26.04 installs cleanly once the apparmor fix runs: %+v", r)
	}
	// apt-get update fails on the missing repo, and the GPU path runs it
	r := findCheck(t, results, "apt sources")
	if r.Status != Fail || !r.Fixable || !strings.Contains(r.Detail, "nvidia-driver-local.list") ||
		r.FixCmd != "mv /etc/apt/sources.list.d/nvidia-driver-local.list /etc/apt/sources.list.d/nvidia-driver-local.list.disabled" {
		t.Fatalf("apt sources: %+v", r)
	}
	if r := findCheck(t, results, "apparmor: wg-quick"); r.Status != Warn || !r.Fixable ||
		!strings.Contains(r.FixCmd, "apparmor_parser -R /etc/apparmor.d/wg-quick") {
		t.Fatalf("apparmor: %+v", r)
	}
	// growing a disk is never bundled into --fix: report the command only
	if r := findCheck(t, results, "disk: volume group"); r.Status != Warn || r.Fixable ||
		!strings.Contains(r.Detail, "lvextend -r -l +100%FREE /dev/mapper/ubuntu--vg-ubuntu--lv") {
		t.Fatalf("lvm: %+v", r)
	}
	if r := findCheck(t, results, "node ip"); r.Status != Pass {
		t.Fatalf("node ip on an interface: %+v", r)
	}
}

func TestPreflightGotchasScoped(t *testing.T) {
	// no GPU work: apt is not run, so a broken source only warns; no VPN:
	// the wg-quick profile is irrelevant
	results := Preflight(gotchas(t), Config{IP: "198.51.100.9", StoragePath: "/storage", NoGPU: true, NoVPN: true})
	if r := findCheck(t, results, "apt sources"); r.Status != Warn {
		t.Fatalf("apt sources without GPU: %+v", r)
	}
	for _, r := range results {
		if r.Name == "apparmor: wg-quick" {
			t.Fatalf("apparmor check without VPN: %+v", r)
		}
	}
	// k3s validates the node IP against local interfaces (the trixy outage)
	if r := findCheck(t, results, "node ip"); r.Status != Warn || !strings.Contains(r.Detail, "198.51.100.9") {
		t.Fatalf("node ip: %+v", r)
	}
}

func TestSudoError(t *testing.T) {
	err := sudoError("jekyo", "193.168.140.82", errors.New("remote command failed: true: sudo: interactive authentication is required"))
	for _, want := range []string{"jekyo@193.168.140.82", "passwordless sudo", "NOPASSWD", "root@"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q in: %v", want, err)
		}
	}
	other := errors.New("remote command failed: connection reset")
	if got := sudoError("jekyo", "h", other); got != other {
		t.Fatalf("non-sudo errors pass through, got %v", got)
	}
}
