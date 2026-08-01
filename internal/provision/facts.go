package provision

import (
	"strconv"
	"strings"
)

// Facts is everything preflight needs to know about the server, gathered in
// a single SSH round-trip by factsScript and parsed by ParseFacts. Parsing
// is separated from gathering so checks are unit-testable against captured
// output.
type Facts struct {
	OSID        string // "ubuntu"
	OSVersion   string // "24.04"
	Arch        string // "x86_64" | "aarch64"
	IsRoot      bool
	MemMB       int
	DiskFreeMB  int // at the storage path (or its closest existing parent)
	Listeners   []Listener
	K3sVersion  string // empty when not installed
	OtherK8s    []string
	Docker      *DockerFacts
	UFWActive   bool
	NMActive    bool
	ClockSynced bool
	Wireguard   bool
	SwapActive  bool
	GPUs        []string // lspci lines
	SecureBoot  bool
	Nouveau     bool
}

type DockerFacts struct {
	Running    bool
	Containers int
}

type Listener struct {
	Proto   string // tcp | udp
	Port    int
	Process string
}

const factsSep = "===JEKYO==="

// factsScript emits sections separated by factsSep markers; keep in sync
// with ParseFacts. It must succeed on a bare Ubuntu server, so every probe
// tolerates missing tools.
func factsScript(storagePath string) string {
	q := "'" + strings.ReplaceAll(storagePath, "'", `'\''`) + "'"
	return `
p() { echo "` + factsSep + `$1"; }
p os; cat /etc/os-release 2>/dev/null
p arch; uname -m
p uid; id -u
p mem; grep MemTotal /proc/meminfo
p disk; d=` + q + `; while [ ! -d "$d" ] && [ "$d" != "/" ]; do d=$(dirname "$d"); done; df -Pk "$d" | tail -1
p listeners; ss -Hlntup 2>/dev/null
p k3s; /usr/local/bin/k3s --version 2>/dev/null | head -1
p otherk8s; [ -d /etc/kubernetes/manifests ] && echo kubeadm; command -v microk8s >/dev/null && echo microk8s; command -v minikube >/dev/null && echo minikube; true
p docker; if command -v docker >/dev/null; then echo installed; docker ps -q 2>/dev/null | wc -l; else echo absent; fi
p ufw; ufw status 2>/dev/null | head -1
p nm; systemctl is-active NetworkManager 2>/dev/null; true
p clock; timedatectl show -p NTPSynchronized --value 2>/dev/null
p wireguard; modprobe -n wireguard >/dev/null 2>&1 && echo ok
p swap; swapon --noheadings 2>/dev/null | wc -l
p gpu; lspci 2>/dev/null | grep -i 'nvidia' | grep -iE 'vga|3d'; true
p secureboot; mokutil --sb-state 2>/dev/null; true
p nouveau; lsmod | grep -c '^nouveau '; true
p end
true`
}

// ParseFacts turns factsScript output into Facts.
func ParseFacts(out string) Facts {
	sections := map[string]string{}
	var name string
	var buf []string
	flush := func() {
		if name != "" {
			sections[name] = strings.TrimSpace(strings.Join(buf, "\n"))
		}
		buf = nil
	}
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), factsSep); ok {
			flush()
			name = rest
			continue
		}
		buf = append(buf, line)
	}
	flush()

	f := Facts{}
	for _, l := range strings.Split(sections["os"], "\n") {
		if v, ok := strings.CutPrefix(l, "ID="); ok {
			f.OSID = strings.Trim(v, `"`)
		}
		if v, ok := strings.CutPrefix(l, "VERSION_ID="); ok {
			f.OSVersion = strings.Trim(v, `"`)
		}
	}
	f.Arch = sections["arch"]
	f.IsRoot = sections["uid"] == "0"
	if fields := strings.Fields(sections["mem"]); len(fields) >= 2 {
		kb, _ := strconv.Atoi(fields[1])
		f.MemMB = kb / 1024
	}
	if fields := strings.Fields(sections["disk"]); len(fields) >= 4 {
		kb, _ := strconv.Atoi(fields[3])
		f.DiskFreeMB = kb / 1024
	}
	f.Listeners = parseListeners(sections["listeners"])
	f.K3sVersion = parseK3sVersion(sections["k3s"])
	if s := sections["otherk8s"]; s != "" {
		f.OtherK8s = strings.Fields(strings.ReplaceAll(s, "\n", " "))
	}
	if lines := strings.Split(sections["docker"], "\n"); lines[0] == "installed" {
		d := &DockerFacts{Running: true}
		if len(lines) > 1 {
			d.Containers, _ = strconv.Atoi(strings.TrimSpace(lines[1]))
		}
		f.Docker = d
	}
	f.UFWActive = strings.Contains(sections["ufw"], "active") && !strings.Contains(sections["ufw"], "inactive")
	f.NMActive = sections["nm"] == "active"
	f.ClockSynced = sections["clock"] == "yes"
	f.Wireguard = sections["wireguard"] == "ok"
	if n, _ := strconv.Atoi(sections["swap"]); n > 0 {
		f.SwapActive = true
	}
	if s := sections["gpu"]; s != "" {
		f.GPUs = strings.Split(s, "\n")
	}
	f.SecureBoot = strings.Contains(sections["secureboot"], "SecureBoot enabled")
	if n, _ := strconv.Atoi(sections["nouveau"]); n > 0 {
		f.Nouveau = true
	}
	return f
}

// parseK3sVersion extracts "v1.31.5+k3s1" from "k3s version v1.31.5+k3s1 (...)".
func parseK3sVersion(s string) string {
	fields := strings.Fields(s)
	for i, w := range fields {
		if w == "version" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

// parseListeners parses `ss -Hlntup` output lines like:
//
//	tcp LISTEN 0 4096 0.0.0.0:80 0.0.0.0:* users:(("nginx",pid=123,fd=6))
//	udp UNCONN 0 0    0.0.0.0:31820 0.0.0.0:* users:(("wg",pid=9,fd=3))
func parseListeners(out string) []Listener {
	var res []Listener
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		proto := fields[0]
		if proto != "tcp" && proto != "udp" {
			continue
		}
		addr := fields[4]
		i := strings.LastIndex(addr, ":")
		if i < 0 {
			continue
		}
		port, err := strconv.Atoi(addr[i+1:])
		if err != nil {
			continue
		}
		proc := ""
		if j := strings.Index(line, `users:(("`); j >= 0 {
			rest := line[j+9:]
			if k := strings.Index(rest, `"`); k > 0 {
				proc = rest[:k]
			}
		}
		res = append(res, Listener{Proto: proto, Port: port, Process: proc})
	}
	return res
}
