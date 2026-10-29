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
	IP             string
	StoragePath    string
	Domain         string
	AcmeEmail      string
	InternalDomain string
	NoVPN          bool
	NoGPU          bool
	NoRegistry     bool
	Fix            bool
	RemoveDocker   bool
	K3sVersion     string
	// PidsLimit is the kubelet's per-pod PID ceiling (pod-max-pids); 0
	// keeps the kubelet default. Kubernetes has no per-container limit,
	// so this is cluster-wide (issue #4).
	PidsLimit int
}

// k3s-owned processes that legitimately hold our ports on an existing install.
var k3sProcesses = map[string]bool{
	"k3s-server": true, "k3s": true, "envoy": true, "kube-proxy": true,
	"svclb": true, "wg": true,
}

var testedUbuntu = map[string]bool{"20.04": true, "22.04": true, "24.04": true, "26.04": true}

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
	} else if !testedUbuntu[f.OSVersion] {
		add("os", Warn, fmt.Sprintf("untested Ubuntu %s (tested: 20.04/22.04/24.04/26.04)", f.OSVersion))
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

	if l := f.LVM; l != nil && l.VGFreeBytes >= 10<<30 {
		// Growing a disk is never bundled into --fix: the free extents may
		// be reserved for something else, and ext4 cannot shrink online.
		add("disk: volume group", Warn, fmt.Sprintf("%s is %d GB but its volume group has %d GB unallocated — grow it with: lvextend -r -l +100%%FREE %s",
			cfg.StoragePath, l.LVBytes>>30, l.VGFreeBytes>>30, l.Device))
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
	if cfg.IP != "" && len(f.Addrs) > 0 {
		local := false
		for _, a := range f.Addrs {
			local = local || a == cfg.IP
		}
		if local {
			add("node ip", Pass, cfg.IP+" is on a local interface")
		} else {
			// This becomes --advertise-address, which the apiserver has to
			// bind: with an address the node does not have, the in-cluster
			// kubernetes endpoint points nowhere, contour's certgen never
			// writes contourcert, and the install dies minutes later at
			// "waiting for contour" with nothing naming the real cause.
			// Blocking, so nothing is written to the server first. A NAT or
			// elastic address is refused too: it cannot work here today,
			// since --node-external-ip gets the same value (issue #32).
			add("node ip", Fail, fmt.Sprintf("--ip %s is not on any interface (have %s) — fix the address and re-run; k3s has to bind it, so a NAT or elastic address will not work either",
				cfg.IP, strings.Join(f.Addrs, ", ")))
		}
	}
	if len(f.AptMissing) > 0 {
		var files, fixes []string
		for _, a := range f.AptMissing {
			files = append(files, a.File+" → "+a.Path)
			fixes = append(fixes, "mv "+a.File+" "+a.File+".disabled")
		}
		// apt-get update fails outright on these; only the GPU path runs it
		st := Warn
		if !cfg.NoGPU && len(f.GPUs) > 0 {
			st = Fail
		}
		addFix("apt sources", st, "local repo missing, apt-get update will fail: "+strings.Join(files, "; ")+" (fix disables the source)",
			strings.Join(fixes, " && "))
	} else {
		add("apt sources", Pass, "no broken local repos")
	}
	if !cfg.NoVPN && f.WGQuickAppArmor {
		addFix("apparmor: wg-quick", Warn, "Ubuntu's wg-quick profile also confines the VPN container's wg-quick and it crash-loops (fix disables only that profile)",
			"ln -sf /etc/apparmor.d/wg-quick /etc/apparmor.d/disable/wg-quick && apparmor_parser -R /etc/apparmor.d/wg-quick")
	}
	if cfg.PidsLimit != 0 {
		// below this a pod's pause container, a shell and a probe can
		// already exhaust it, and failures look nothing like a limit
		if cfg.PidsLimit < 64 {
			add("pids limit", Fail, fmt.Sprintf("--pids-limit %d is too low to run a pod; use 64 or more", cfg.PidsLimit))
		} else {
			add("pids limit", Pass, fmt.Sprintf("%d processes per pod", cfg.PidsLimit))
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
