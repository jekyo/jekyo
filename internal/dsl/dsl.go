// Package dsl defines and parses jekyo.yaml — one file describing an app's
// services end to end: image or build steps, runtime config, HTTP routing,
// volumes, GPU. Compose-inspired, Kubernetes-honest (see SPEC.md §3).
package dsl

import (
	"fmt"
)

// App is the root of a jekyo.yaml.
type App struct {
	Name string `yaml:"app"`
	// Description and Icon are catalog/dashboard metadata (SPEC §4.1).
	Description string             `yaml:"description"`
	Icon        string             `yaml:"icon"`
	Services    map[string]Service `yaml:"services"`
	Volumes     map[string]Volume  `yaml:"volumes"`
	// Inputs exist only in templates; `jekyo init` resolves and strips
	// them. A deployable app must not carry unresolved inputs.
	Inputs map[string]Input `yaml:"inputs"`
}

// Input is a template parameter (SPEC §4.1).
type Input struct {
	Kind    string `yaml:"kind"` // domain | secret | string | size
	Prompt  string `yaml:"prompt"`
	Default string `yaml:"default"`
	// Required defaults to true, except for secrets (auto-generated) and
	// inputs with a default.
	Required *bool `yaml:"required"`
}

// IsRequired resolves the Required default.
func (i Input) IsRequired() bool {
	if i.Required != nil {
		return *i.Required
	}
	return i.Kind != "secret" && i.Default == ""
}

type Service struct {
	Image   string   `yaml:"image"`
	Build   *Build   `yaml:"build"`
	Command []string `yaml:"command"`
	Args    []string `yaml:"args"`

	Env  map[string]string `yaml:"env"`
	Port int               `yaml:"port"`
	// Ports lists container ports when a service exposes several; Port is
	// sugar for a one-element list. The first port is the "main" port used
	// as the default HTTP/probe target.
	Ports []int `yaml:"ports"`

	HTTP      *HTTP     `yaml:"http"`
	Expose    []Expose  `yaml:"expose"`
	Resources Resources `yaml:"resources"`
	// Schedule turns the service into a CronJob (cron syntax, e.g.
	// "0 3 * * *"): image/build + command run on that schedule. Mutually
	// exclusive with http, replicas, and volumes.
	Schedule string `yaml:"schedule"`
	Replicas  *int      `yaml:"replicas"`
	Stateful  *bool     `yaml:"stateful"`
	Health    *Health   `yaml:"health"`
	GPU       GPU       `yaml:"gpu"`
	// Volumes maps a volume name (declared top-level) to a mount: either
	// a plain path string or {path, subpath} to share one volume between
	// services.
	Volumes map[string]VolumeMount `yaml:"volumes"`
}

// VolumeMount accepts a string (the mount path) or a mapping with an
// optional subpath, letting several services share one volume.
type VolumeMount struct {
	Path    string `yaml:"path"`
	Subpath string `yaml:"subpath"`
}

func (v *VolumeMount) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err == nil {
		v.Path = s
		return nil
	}
	type raw VolumeMount
	var r raw
	if err := unmarshal(&r); err != nil {
		return fmt.Errorf("volume mount: expected a path or {path, subpath}: %w", err)
	}
	*v = VolumeMount(r)
	return nil
}

type Build struct {
	Context    string            `yaml:"context"`
	Dockerfile string            `yaml:"dockerfile"`
	Inline     string            `yaml:"inline"`
	Args       map[string]string `yaml:"args"`
}

type HTTP struct {
	Domain string `yaml:"domain"`
	Path   string `yaml:"path"`
	// TLS defaults to true; certificates are issued automatically when the
	// server was installed with a domain.
	TLS  *bool  `yaml:"tls"`
	Auth string `yaml:"auth"`
	// Redirect makes this a pure routing rule: requests to Domain are
	// answered by the ingress with a permanent redirect to this host (or
	// URL). A redirect service runs no container and defines nothing else.
	Redirect string `yaml:"redirect"`
}

// Expose publishes a raw TCP/UDP port on the node (NodePort).
type Expose struct {
	Port     int    `yaml:"port"`     // container port
	Node     int    `yaml:"node"`     // node port (30000-32767)
	Protocol string `yaml:"protocol"` // tcp (default) | udp
}

// Resources uses flat keys: cpu/memory are guaranteed (requests),
// cpu-max/memory-max are hard caps (limits). Unset means unset.
type Resources struct {
	CPU       string `yaml:"cpu"`
	Memory    string `yaml:"memory"`
	CPUMax    string `yaml:"cpu-max"`
	MemoryMax string `yaml:"memory-max"`
}

type Health struct {
	Path string `yaml:"path"`
	Port int    `yaml:"port"`
	// Command runs an exec probe instead of an HTTP one.
	Command []string `yaml:"command"`
}

// GPU accepts either a count (`gpu: 1`) or a mapping
// (`gpu: {count: 2, devices: "0,2"}`).
type GPU struct {
	Count   int    `yaml:"count"`
	Devices string `yaml:"devices"`
}

func (g *GPU) UnmarshalYAML(unmarshal func(any) error) error {
	var n int
	if err := unmarshal(&n); err == nil {
		g.Count = n
		return nil
	}
	type raw GPU // avoid recursion
	var r raw
	if err := unmarshal(&r); err != nil {
		return fmt.Errorf("gpu: expected a count or {count, devices}: %w", err)
	}
	*g = GPU(r)
	return nil
}

func (g GPU) Enabled() bool { return g.Count > 0 || g.Devices != "" }

type Volume struct {
	Size  string `yaml:"size"`
	Class string `yaml:"class"`
	// Backup schedules restic snapshots of this volume to the cluster's
	// configured S3 target (jekyo backup config).
	Backup *VolumeBackup `yaml:"backup"`
}

// VolumeBackup configures scheduled backups for one volume.
type VolumeBackup struct {
	Schedule string `yaml:"schedule"` // cron, e.g. "0 3 * * *"
	Keep     int    `yaml:"keep"`     // snapshots to retain (default 7)
}

// KeepCount defaults to 7.
func (b VolumeBackup) KeepCount() int {
	if b.Keep <= 0 {
		return 7
	}
	return b.Keep
}

// MainPort is the service's primary container port (Port, or the first of
// Ports), 0 when none.
func (s Service) MainPort() int {
	if s.Port != 0 {
		return s.Port
	}
	if len(s.Ports) > 0 {
		return s.Ports[0]
	}
	return 0
}

// AllPorts is the deduplicated union of Port and Ports, in declaration order.
func (s Service) AllPorts() []int {
	var out []int
	seen := map[int]bool{}
	for _, p := range append([]int{s.Port}, s.Ports...) {
		if p != 0 && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// IsStateful reports whether the service compiles to a StatefulSet: explicit
// stateful: true, or implied by mounting volumes.
func (s Service) IsStateful() bool {
	if s.Stateful != nil {
		return *s.Stateful
	}
	return len(s.Volumes) > 0
}

// ReplicaCount defaults to 1.
func (s Service) ReplicaCount() int {
	if s.Replicas != nil {
		return *s.Replicas
	}
	return 1
}
