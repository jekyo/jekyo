package provision

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jekyo/jekyo/internal/addons"
	"github.com/jekyo/jekyo/internal/sshx"
)

// DefaultK3sVersion is the pinned k3s release installed when --k3s-version
// is not given.
const DefaultK3sVersion = "v1.31.5+k3s1"

const (
	k3sManifestsDir = "/var/lib/rancher/k3s/server/manifests"
	registriesPath  = "/etc/rancher/k3s/registries.yaml"
	kubeconfigPath  = "/etc/rancher/k3s/k3s.yaml"
)

// Credentials are the secrets generated (or reused) during install.
type Credentials struct {
	RegistryUser     string
	RegistryPassword string
	VPNPassword      string
}

// Installer converges a server to a ready JEKYO cluster.
type Installer struct {
	SSH *sshx.Client
	Cfg Config
	Out io.Writer // human progress; step logs stream here
}

func (in *Installer) logf(format string, args ...any) {
	fmt.Fprintf(in.Out, format+"\n", args...)
}

func (in *Installer) step(name string, fn func() error) error {
	start := time.Now()
	fmt.Fprintf(in.Out, "→ %s ... ", name)
	if err := fn(); err != nil {
		fmt.Fprintln(in.Out, "FAILED")
		return fmt.Errorf("%s: %w", name, err)
	}
	fmt.Fprintf(in.Out, "done (%s)\n", time.Since(start).Round(time.Second))
	return nil
}

// GatherFacts runs the facts script remotely and parses the result.
func (in *Installer) GatherFacts() (Facts, error) {
	out, err := in.SSH.Run(factsScript(in.Cfg.StoragePath))
	if err != nil {
		return Facts{}, fmt.Errorf("gathering server facts: %w", err)
	}
	return ParseFacts(out), nil
}

// RemoveDocker purges the Docker engine. purgeData additionally deletes
// /var/lib/docker (images, volumes) — caller must have confirmed that.
func (in *Installer) RemoveDocker(purgeData bool) error {
	return in.step("removing docker", func() error {
		cmd := `systemctl stop docker docker.socket 2>/dev/null; ` +
			`DEBIAN_FRONTEND=noninteractive apt-get remove -y --purge docker.io docker-ce docker-ce-cli docker-buildx-plugin docker-compose-plugin containerd.io moby-engine 2>/dev/null; ` +
			`apt-get autoremove -y`
		if purgeData {
			cmd += ` && rm -rf /var/lib/docker /var/lib/containerd`
		}
		_, err := in.SSH.Run(cmd)
		return err
	})
}

// ApplyFixes runs the FixCmd of every fixable result.
func (in *Installer) ApplyFixes(results []CheckResult) error {
	for _, r := range Fixables(results) {
		if err := in.step("fix: "+r.Name, func() error {
			_, err := in.SSH.Run(r.FixCmd)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// Install runs the full convergence. Facts must be post-preflight (and
// post-fix) so decisions like GPU/VPN reflect the final server state.
func (in *Installer) Install(f Facts, creds Credentials) error {
	cfg := in.Cfg
	gpu := len(f.GPUs) > 0 && !cfg.NoGPU
	vpn := !cfg.NoVPN && f.Wireguard

	// containerd registry config must exist before k3s starts.
	if err := in.step("configuring containerd registry mirror", func() error {
		return in.SSH.WriteFile(registriesPath, []byte(addons.RegistriesYAML(creds.RegistryUser, creds.RegistryPassword)), "0600")
	}); err != nil {
		return err
	}

	version := cfg.K3sVersion
	if version == "" {
		version = DefaultK3sVersion
	}
	verb := "installing"
	if f.K3sVersion != "" {
		verb = "converging"
	}
	// Always run the installer script — it is idempotent and re-running is
	// the only way flag changes converge on an existing install.
	if err := in.step(fmt.Sprintf("%s k3s %s", verb, version), func() error {
		flags := []string{
			"--disable=traefik",
			"--disable=servicelb", // envoy's DaemonSet binds hostPorts 80/443 itself
			"--advertise-address " + cfg.IP,
			"--node-external-ip " + cfg.IP,
			"--default-local-storage-path " + cfg.StoragePath,
			"--kubelet-arg=allowed-unsafe-sysctls=net.*",
			"--write-kubeconfig-mode 640",
		}
		if cfg.InternalDomain != "" && cfg.InternalDomain != "cluster.local" {
			flags = append(flags, "--cluster-domain "+cfg.InternalDomain)
		}
		cmd := fmt.Sprintf(
			"curl -sfL https://get.k3s.io | INSTALL_K3S_VERSION=%q sh -s - server %s",
			version, strings.Join(flags, " "))
		_, err := in.SSH.Run(cmd)
		return err
	}); err != nil {
		return err
	}

	if err := in.step("waiting for node ready", func() error {
		// On a fresh install the node object may not be registered yet and
		// kubectl wait fails fast on an empty match — poll for it first.
		if _, err := in.SSH.Run("for i in $(seq 1 100); do [ \"$(k3s kubectl get nodes --no-headers 2>/dev/null | wc -l)\" -gt 0 ] && exit 0; sleep 3; done; echo 'node never registered'; exit 1"); err != nil {
			return err
		}
		_, err := in.SSH.Run("k3s kubectl wait --for=condition=Ready node --all --timeout=300s")
		return err
	}); err != nil {
		return err
	}

	if gpu {
		if err := in.installGPU(); err != nil {
			return err
		}
	}

	// Render and push addon manifests; k3s applies and reconciles them.
	regHash, err := BcryptHash(creds.RegistryPassword)
	if err != nil {
		return err
	}
	vpnHash, err := BcryptHash(creds.VPNPassword)
	if err != nil {
		return err
	}
	vpnHost := cfg.IP
	if cfg.Domain != "" {
		vpnHost = "vpn." + cfg.Domain
	}
	files, err := addons.Render(addons.Values{
		Domain:           cfg.Domain,
		AcmeEmail:        cfg.AcmeEmail,
		RegistryUser:     creds.RegistryUser,
		RegistryHtpasswd: regHash,
		RegistrySize:     "100Gi",
		VPNHost:          vpnHost,
		VPNPasswordHash:  vpnHash,
		GPU:              gpu,
		VPN:              vpn,
	})
	if err != nil {
		return err
	}
	if err := in.step(fmt.Sprintf("deploying %d addon manifests", len(files)), func() error {
		for _, file := range files {
			if err := in.SSH.WriteFile(k3sManifestsDir+"/"+file.Name, file.Data, "0600"); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}

	type wait struct{ name, ns, resource, cmd string }
	waits := []wait{
		{"contour", "projectcontour", "deploy/contour", "kubectl -n projectcontour wait --for=condition=Available deploy/contour --timeout=420s"},
		{"registry", "kube-system", "statefulset/registry", "kubectl -n kube-system rollout status statefulset/registry --timeout=300s"},
	}
	if cfg.Domain != "" {
		waits = append(waits, wait{"kcert", "kube-system", "deploy/kcert", "kubectl -n kube-system wait --for=condition=Available deploy/kcert --timeout=300s"})
	}
	if vpn {
		waits = append(waits, wait{"vpn", "kube-system", "statefulset/vpn", "kubectl -n kube-system rollout status statefulset/vpn --timeout=300s"})
	}
	for _, w := range waits {
		if err := in.step("waiting for "+w.name, func() error {
			// The k3s addon controller applies manifests asynchronously;
			// poll for existence first, since kubectl wait fails fast on
			// resources that don't exist yet.
			exist := fmt.Sprintf(
				"for i in $(seq 1 60); do k3s kubectl -n %s get %s >/dev/null 2>&1 && exit 0; sleep 3; done; echo 'timed out waiting for %s to appear'; exit 1",
				w.ns, w.resource, w.resource)
			if _, err := in.SSH.Run(exist); err != nil {
				return err
			}
			_, err := in.SSH.Run("k3s " + w.cmd)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// installGPU installs NVIDIA drivers (if missing) and the container toolkit,
// then restarts k3s so its containerd picks up the nvidia runtime.
func (in *Installer) installGPU() error {
	out, _ := in.SSH.Run("command -v nvidia-smi >/dev/null && nvidia-smi --query-gpu=name --format=csv,noheader | head -3; true")
	if strings.TrimSpace(out) == "" {
		if err := in.step("installing NVIDIA driver (this can take several minutes)", func() error {
			_, err := in.SSH.Run("apt-get update -q && DEBIAN_FRONTEND=noninteractive apt-get install -y -q ubuntu-drivers-common && ubuntu-drivers install")
			return err
		}); err != nil {
			return err
		}
		if out, _ := in.SSH.Run("nvidia-smi -L; true"); !strings.Contains(out, "GPU") {
			in.logf("  NOTE: driver installed but not active — a reboot is likely required; re-run install afterwards")
		}
	} else {
		in.logf("→ NVIDIA driver present: %s", strings.Split(out, "\n")[0])
	}

	return in.step("installing nvidia-container-toolkit", func() error {
		cmd := `test -f /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg || ` +
			`(curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey | gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg); ` +
			`curl -fsSL https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list | ` +
			`sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' > /etc/apt/sources.list.d/nvidia-container-toolkit.list && ` +
			`apt-get update -q && DEBIAN_FRONTEND=noninteractive apt-get install -y -q nvidia-container-toolkit && ` +
			`systemctl restart k3s`
		_, err := in.SSH.Run(cmd)
		return err
	})
}

// FetchKubeconfig returns the cluster kubeconfig rewritten to the public IP.
func (in *Installer) FetchKubeconfig() ([]byte, error) {
	data, err := in.SSH.ReadFile(kubeconfigPath)
	if err != nil {
		return nil, err
	}
	out := strings.ReplaceAll(string(data), "https://127.0.0.1:6443", "https://"+in.Cfg.IP+":6443")
	out = strings.ReplaceAll(out, ": default", ": jekyo") // context/cluster/user names
	return []byte(out), nil
}

// Uninstall removes k3s per https://docs.k3s.io/installation/uninstall.
// purgeStorage additionally wipes the storage path — caller confirms.
func (in *Installer) Uninstall(purgeStorage bool) error {
	if err := in.step("uninstalling k3s", func() error {
		_, err := in.SSH.Run("if [ -x /usr/local/bin/k3s-uninstall.sh ]; then /usr/local/bin/k3s-uninstall.sh; fi; if [ -x /usr/local/bin/k3s-agent-uninstall.sh ]; then /usr/local/bin/k3s-agent-uninstall.sh; fi")
		return err
	}); err != nil {
		return err
	}
	if err := in.step("removing jekyo config", func() error {
		_, err := in.SSH.Run("rm -rf /etc/rancher/k3s")
		return err
	}); err != nil {
		return err
	}
	if purgeStorage && in.Cfg.StoragePath != "" && in.Cfg.StoragePath != "/" {
		return in.step("purging storage at "+in.Cfg.StoragePath, func() error {
			_, err := in.SSH.Run("rm -rf " + shellQuote(in.Cfg.StoragePath))
			return err
		})
	}
	return nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
