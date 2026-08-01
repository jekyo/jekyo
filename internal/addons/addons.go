// Package addons renders the embedded, pinned addon manifests that `server
// install` pushes to the k3s auto-deploy directory
// (/var/lib/rancher/k3s/server/manifests). k3s applies and reconciles files
// in that directory itself, so installation needs no helm and no client-side
// apply machinery.
package addons

import (
	"bytes"
	"embed"
	"fmt"
	"strings"
	"text/template"
)

//go:embed manifests
var manifests embed.FS

// RegistryClusterIP is the fixed ClusterIP of the registry Service. It is
// fixed so /etc/rancher/k3s/registries.yaml (written before the cluster
// exists) can point containerd at it. It must be inside k3s's default
// service CIDR (10.43.0.0/16) and out of the way of allocation.
const RegistryClusterIP = "10.43.0.100"

// RegistryHost is the name every pod spec and docker tag uses for the
// in-cluster registry; containerd rewrites it via registries.yaml.
const RegistryHost = "registry.jekyo.local"

// Values parameterizes the addon templates.
type Values struct {
	Domain    string // base domain; empty disables kcert + external ingresses
	AcmeEmail string
	AcmeDirURL string // Let's Encrypt production by default; staging for tests

	RegistryUser      string
	RegistryHtpasswd  string // bcrypt hash of the registry password
	RegistrySize      string // PVC size, e.g. "100Gi"
	RegistryClusterIP string

	VPNHost         string // WG_HOST: vpn.<domain> or the raw public IP
	VPNPasswordHash string // bcrypt hash for the wg-easy admin UI
	ClusterDNS      string // CoreDNS ClusterIP, 10.43.0.10 on stock k3s

	GPU bool // render the nvidia RuntimeClass
	VPN bool
}

// File is one rendered manifest destined for the k3s manifests directory.
type File struct {
	Name string // file name, jekyo- prefixed except vendored contour
	Data []byte
}

// Render produces all addon manifests for the given values.
func Render(v Values) ([]File, error) {
	if v.RegistryClusterIP == "" {
		v.RegistryClusterIP = RegistryClusterIP
	}
	if v.ClusterDNS == "" {
		v.ClusterDNS = "10.43.0.10"
	}
	if v.AcmeDirURL == "" {
		v.AcmeDirURL = "https://acme-v02.api.letsencrypt.org/directory"
	}

	var files []File

	static := map[string]string{
		"contour.yaml":        "jekyo-contour.yaml",
		"contour-extras.yaml": "jekyo-contour-extras.yaml",
	}
	if v.GPU {
		static["nvidia.yaml"] = "jekyo-nvidia.yaml"
	}
	for src, dst := range static {
		data, err := manifests.ReadFile("manifests/" + src)
		if err != nil {
			return nil, err
		}
		files = append(files, File{Name: dst, Data: data})
	}

	templated := map[string]string{
		"registry.yaml.tmpl": "jekyo-registry.yaml",
	}
	if v.VPN {
		templated["vpn.yaml.tmpl"] = "jekyo-vpn.yaml"
	}
	if v.Domain != "" {
		templated["kcert.yaml.tmpl"] = "jekyo-kcert.yaml"
	}
	for src, dst := range templated {
		data, err := renderTemplate(src, v)
		if err != nil {
			return nil, err
		}
		files = append(files, File{Name: dst, Data: data})
	}
	return files, nil
}

func renderTemplate(name string, v Values) ([]byte, error) {
	raw, err := manifests.ReadFile("manifests/" + name)
	if err != nil {
		return nil, err
	}
	t, err := template.New(name).Option("missingkey=error").Parse(string(raw))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, v); err != nil {
		return nil, fmt.Errorf("rendering %s: %w", name, err)
	}
	return buf.Bytes(), nil
}

// RegistriesYAML is the k3s containerd registry configuration
// (/etc/rancher/k3s/registries.yaml): every node resolves RegistryHost to the
// in-cluster registry with credentials, so pods in any namespace can pull
// from it without imagePullSecrets.
func RegistriesYAML(user, password string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "mirrors:\n")
	fmt.Fprintf(&b, "  %q:\n", RegistryHost)
	fmt.Fprintf(&b, "    endpoint:\n      - \"http://%s:5000\"\n", RegistryClusterIP)
	fmt.Fprintf(&b, "configs:\n")
	fmt.Fprintf(&b, "  %q:\n", RegistryHost)
	fmt.Fprintf(&b, "    auth:\n      username: %q\n      password: %q\n", user, password)
	return b.String()
}
