package provision

import (
	"fmt"
	"strings"
)

type Status string

const (
	Pass Status = "PASS"
	Warn Status = "WARN"
	Fail Status = "FAIL"
)

// CheckResult is one preflight finding. Fixable results are remediated by
// `install --fix`; FixCmd is the remote command that does it.
type CheckResult struct {
	Name    string
	Status  Status
	Detail  string
	Fixable bool
	FixCmd  string
}

// Config carries the install parameters preflight needs.
type Config struct {
	IP           string
	StoragePath  string
	Domain       string
	AcmeEmail    string
	NoVPN        bool
	NoGPU        bool
	NoRegistry   bool
	Fix          bool
	RemoveDocker bool
	K3sVersion   string
}

// k3s-owned processes that legitimately hold our ports on an existing install.
var k3sProcesses = map[string]bool{
	"k3s-server": true, "k3s": true, "envoy": true, "kube-proxy": true,
	"svclb": true, "wg": true,
}

// Preflight evaluates all checks against gathered facts. Pure function:
// easy to unit-test with synthetic facts.
func Preflight(f Facts, cfg Config) []CheckResult {
	var res []CheckResult
	add := func(name string, st Status, detail string) {
		res = append(res, CheckResult{Name: name, Status: st, Detail: detail})
	}
	addFix := func(name string, st Status, detail, fix string) {
		res = append(res, CheckResult{Name: name, Status: st, Detail: detail, Fixable: true, FixCmd: fix})
	}

	// OS / privileges / capacity.
	if f.OSID != "ubuntu" {
		add("os", Fail, fmt.Sprintf("Ubuntu required, found %q", f.OSID))
	} else if f.OSVersion != "20.04" && f.OSVersion != "22.04" && f.OSVersion != "24.04" {
		add("os", Warn, fmt.Sprintf("untested Ubuntu %s (tested: 20.04/22.04/24.04)", f.OSVersion))
	} else {
		add("os", Pass, "Ubuntu "+f.OSVersion)
	}
	switch f.Arch {
	case "x86_64":
		add("arch", Pass, "amd64")
	case "aarch64":
		add("arch", Warn, "arm64 — works, but JEKYO builds default to linux/amd64 images")
	default:
		add("arch", Fail, "unsupported architecture "+f.Arch)
	}
	// Facts gathering already ran as root (directly or via sudo -n);
	// reaching this point proves privileged execution works.
	add("privileges", Pass, "root or passwordless sudo")
	if f.MemMB > 0 && f.MemMB < 1800 {
		add("memory", Fail, fmt.Sprintf("%d MB RAM; at least 2 GB required", f.MemMB))
	} else {
		add("memory", Pass, fmt.Sprintf("%d MB RAM", f.MemMB))
	}
	if f.DiskFreeMB > 0 && f.DiskFreeMB < 10*1024 {
		// A fresh install needs headroom for k3s + addon images; an existing
		// cluster that already consumed it must stay repairable — warn only.
		if f.K3sVersion != "" {
			add("disk", Warn, fmt.Sprintf("%d MB free at %s — low, consider growing the disk", f.DiskFreeMB, cfg.StoragePath))
		} else {
			add("disk", Fail, fmt.Sprintf("%d MB free at %s; at least 10 GB required", f.DiskFreeMB, cfg.StoragePath))
		}
	} else {
		add("disk", Pass, fmt.Sprintf("%d GB free at %s", f.DiskFreeMB/1024, cfg.StoragePath))
	}

	// Ports.
	required := []Listener{{Proto: "tcp", Port: 80}, {Proto: "tcp", Port: 443}, {Proto: "tcp", Port: 6443}}
	if !cfg.NoVPN {
		required = append(required, Listener{Proto: "udp", Port: 31820})
	}
	for _, want := range required {
		holder := ""
		for _, l := range f.Listeners {
			if l.Proto == want.Proto && l.Port == want.Port {
				holder = l.Process
			}
		}
		name := fmt.Sprintf("port %d/%s", want.Port, want.Proto)
		switch {
		case holder == "":
			add(name, Pass, "free")
		case k3sProcesses[holder] || strings.HasPrefix(holder, "svclb"):
			add(name, Pass, "held by existing k3s ("+holder+") — will converge")
		case holder == "docker-proxy" || holder == "dockerd":
			add(name, Fail, "held by a Docker container — stop it or pass --remove-docker")
		default:
			add(name, Fail, "held by "+holder)
		}
	}

	// Other cluster software.
	if len(f.OtherK8s) > 0 {
		add("other kubernetes", Fail, strings.Join(f.OtherK8s, ", ")+" detected — remove manually, JEKYO won't touch it")
	} else {
		add("other kubernetes", Pass, "none")
	}
	if f.K3sVersion != "" {
		add("existing k3s", Warn, f.K3sVersion+" present — install converges it")
	}

	// Docker.
	if f.Docker != nil {
		detail := fmt.Sprintf("installed, %d running container(s)", f.Docker.Containers)
		if cfg.RemoveDocker {
			add("docker", Warn, detail+" — will be REMOVED (--remove-docker)")
		} else {
			add("docker", Warn, detail+" — harmless to k3s, but wastes resources; pass --remove-docker to purge")
		}
	} else {
		add("docker", Pass, "not installed")
	}

	// Networking / system services.
	if f.UFWActive {
		addFix("firewall", Warn, "ufw active — k3s ports and pod/service CIDRs must be allowed",
			"ufw allow 80/tcp && ufw allow 443/tcp && ufw allow 6443/tcp && ufw allow 31820/udp && ufw allow from 10.42.0.0/16 && ufw allow from 10.43.0.0/16")
	} else {
		add("firewall", Pass, "ufw inactive")
	}
	if f.NMActive {
		addFix("networkmanager", Warn, "NetworkManager active — must ignore CNI interfaces",
			`mkdir -p /etc/NetworkManager/conf.d && printf '[keyfile]\nunmanaged-devices=interface-name:cni*;interface-name:flannel*;interface-name:veth*\n' > /etc/NetworkManager/conf.d/jekyo-cni.conf && systemctl reload NetworkManager`)
	} else {
		add("networkmanager", Pass, "not active")
	}
	if !f.ClockSynced {
		addFix("clock sync", Warn, "clock not NTP-synced — TLS/ACME breaks with skew",
			"timedatectl set-ntp true")
	} else {
		add("clock sync", Pass, "NTP synced")
	}
	if !cfg.NoVPN {
		if f.Wireguard {
			add("wireguard module", Pass, "available")
		} else {
			add("wireguard module", Warn, "unavailable — VPN addon will be skipped")
		}
	}
	if f.SwapActive {
		add("swap", Warn, "enabled — k3s tolerates it, memory limits get fuzzy")
	} else {
		add("swap", Pass, "off")
	}

	// GPU path.
	if !cfg.NoGPU && len(f.GPUs) > 0 {
		add("gpu", Pass, fmt.Sprintf("%d NVIDIA device(s) detected", len(f.GPUs)))
		if f.SecureBoot {
			add("gpu: secure boot", Warn, "enabled — unsigned NVIDIA DKMS modules won't load; enroll MOK or disable Secure Boot")
		}
		if f.Nouveau {
			addFix("gpu: nouveau", Warn, "nouveau driver loaded — conflicts with NVIDIA driver (fix blacklists it; reboot required)",
				`printf 'blacklist nouveau\noptions nouveau modeset=0\n' > /etc/modprobe.d/jekyo-blacklist-nouveau.conf && update-initramfs -u`)
		}
	}

	return res
}

// Blocking reports whether any check failed.
func Blocking(results []CheckResult) bool {
	for _, r := range results {
		if r.Status == Fail {
			return true
		}
	}
	return false
}

// Fixables returns the checks that --fix would remediate.
func Fixables(results []CheckResult) []CheckResult {
	var out []CheckResult
	for _, r := range results {
		if r.Fixable {
			out = append(out, r)
		}
	}
	return out
}
