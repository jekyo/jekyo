// Package contexts manages JEKYO contexts: one directory per server under
// ~/.jekyo/contexts/<name>/ holding the kubeconfig and metadata, plus a
// config file recording which context is current. The cluster itself is the
// source of truth for apps; contexts only store how to reach it.
package contexts

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/goccy/go-yaml"
)

// Meta describes one context (one server).
type Meta struct {
	Name      string    `json:"name" yaml:"name"`
	SSH       string    `json:"ssh" yaml:"ssh"` // user@host[:port] used at install time
	IP        string    `json:"ip" yaml:"ip"`   // public IP the cluster advertises
	Domain    string    `json:"domain,omitempty" yaml:"domain,omitempty"`
	Storage   string    `json:"storage,omitempty" yaml:"storage,omitempty"` // local-path root on the server
	Arch      string    `json:"arch,omitempty" yaml:"arch,omitempty"`       // x86_64 | aarch64; picks the build platform
	CreatedAt time.Time `json:"createdAt" yaml:"createdAt"`

	Registry *Registry `json:"registry,omitempty" yaml:"registry,omitempty"`
	VPN      *VPN      `json:"vpn,omitempty" yaml:"vpn,omitempty"`
	// Logins are external private registries (jekyo registry login),
	// keyed by host; compiled into imagePullSecrets when referenced.
	Logins map[string]Login `json:"logins,omitempty" yaml:"logins,omitempty"`
}

// Login is a credential for an external registry host.
type Login struct {
	Username string `json:"username" yaml:"username"`
	Password string `json:"password" yaml:"password"`
}

// Registry holds how to reach the cluster's registry from outside.
type Registry struct {
	Host     string `json:"host,omitempty" yaml:"host,omitempty"` // e.g. registry.example.com; empty = port-forward
	Username string `json:"username,omitempty" yaml:"username,omitempty"`
	Password string `json:"password,omitempty" yaml:"password,omitempty"`
}

// VPN holds wg-easy access details.
type VPN struct {
	AdminURL string `json:"adminUrl,omitempty" yaml:"adminUrl,omitempty"`
	Password string `json:"password,omitempty" yaml:"password,omitempty"`
}

type config struct {
	Current string `yaml:"current"`
}

// Store reads and writes contexts under a root directory
// ($JEKYO_HOME or ~/.jekyo).
type Store struct {
	root string
}

func Open() (*Store, error) {
	root := os.Getenv("JEKYO_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolving home directory: %w", err)
		}
		root = filepath.Join(home, ".jekyo")
	}
	if err := os.MkdirAll(filepath.Join(root, "contexts"), 0o700); err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

func (s *Store) Root() string { return s.root }

func (s *Store) dir(name string) string {
	return filepath.Join(s.root, "contexts", name)
}

// KubeconfigPath is where the context's kubeconfig lives; the file is written
// by `server install`.
func (s *Store) KubeconfigPath(name string) string {
	return filepath.Join(s.dir(name), "kubeconfig")
}

func (s *Store) metaPath(name string) string {
	return filepath.Join(s.dir(name), "meta.yaml")
}

func (s *Store) Get(name string) (Meta, error) {
	data, err := os.ReadFile(s.metaPath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return Meta{}, fmt.Errorf("context %q not found (run 'jekyo context ls')", name)
		}
		return Meta{}, err
	}
	var m Meta
	if err := yaml.Unmarshal(data, &m); err != nil {
		return Meta{}, fmt.Errorf("parsing %s: %w", s.metaPath(name), err)
	}
	return m, nil
}

// Save writes the context metadata, creating the context if new.
func (s *Store) Save(m Meta) error {
	if m.Name == "" {
		return fmt.Errorf("context name is empty")
	}
	if err := os.MkdirAll(s.dir(m.Name), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(s.metaPath(m.Name), data, 0o600)
}

func (s *Store) Remove(name string) error {
	if _, err := s.Get(name); err != nil {
		return err
	}
	if err := os.RemoveAll(s.dir(name)); err != nil {
		return err
	}
	if cur, _ := s.CurrentName(); cur == name {
		return s.setCurrent("")
	}
	return nil
}

func (s *Store) List() ([]Meta, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "contexts"))
	if err != nil {
		return nil, err
	}
	var out []Meta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := s.Get(e.Name())
		if err != nil {
			continue // skip broken entries rather than failing the listing
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) configPath() string { return filepath.Join(s.root, "config.yaml") }

// CurrentName returns the selected context name, or "" if none.
func (s *Store) CurrentName() (string, error) {
	data, err := os.ReadFile(s.configPath())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var c config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return "", err
	}
	return c.Current, nil
}

func (s *Store) SetCurrent(name string) error {
	if _, err := s.Get(name); err != nil {
		return err
	}
	return s.setCurrent(name)
}

func (s *Store) setCurrent(name string) error {
	data, err := yaml.Marshal(config{Current: name})
	if err != nil {
		return err
	}
	return os.WriteFile(s.configPath(), data, 0o600)
}

// Resolve picks the context to operate on: the --context flag value if set,
// otherwise the current context. Errors guide the user to a fix.
func (s *Store) Resolve(flagValue string) (Meta, error) {
	name := flagValue
	if name == "" {
		var err error
		name, err = s.CurrentName()
		if err != nil {
			return Meta{}, err
		}
	}
	if name == "" {
		all, _ := s.List()
		if len(all) == 0 {
			return Meta{}, fmt.Errorf("no contexts exist yet — create one with 'jekyo server install <user@host>'")
		}
		return Meta{}, fmt.Errorf("no current context — pick one with 'jekyo context use <name>'")
	}
	return s.Get(name)
}

// MarshalJSON keeps time formatting stable for -o json output.
func (m Meta) JSON() ([]byte, error) { return json.MarshalIndent(m, "", "  ") }

type exportBlob struct {
	Meta       Meta   `json:"meta"`
	Kubeconfig []byte `json:"kubeconfig"`
}

// Export packs a context (meta + kubeconfig) into a base64 string suitable
// for a CI secret.
func (s *Store) Export(name string) (string, error) {
	m, err := s.Get(name)
	if err != nil {
		return "", err
	}
	kc, err := os.ReadFile(s.KubeconfigPath(name))
	if err != nil {
		return "", fmt.Errorf("context %q has no kubeconfig: %w", name, err)
	}
	data, err := json.Marshal(exportBlob{Meta: m, Kubeconfig: kc})
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

// Import restores an exported context and returns its name.
func (s *Store) Import(blob string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return "", fmt.Errorf("invalid context blob (expected 'jekyo context export' output): %w", err)
	}
	var e exportBlob
	if err := json.Unmarshal(data, &e); err != nil {
		return "", fmt.Errorf("invalid context blob: %w", err)
	}
	if err := s.Save(e.Meta); err != nil {
		return "", err
	}
	if err := os.WriteFile(s.KubeconfigPath(e.Meta.Name), e.Kubeconfig, 0o600); err != nil {
		return "", err
	}
	return e.Meta.Name, nil
}
