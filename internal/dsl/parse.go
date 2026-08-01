package dsl

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Parse reads a jekyo.yaml. env provides values for ${VAR} interpolation
// (interpolation applies to env: values only — not to commands or inline
// Dockerfiles, which legitimately contain ${...} of their own).
func Parse(data []byte, env map[string]string) (*App, error) {
	var app App
	if err := yaml.UnmarshalWithOptions(data, &app, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("jekyo.yaml: %w", err)
	}
	if len(app.Inputs) > 0 {
		// Template with unresolved inputs: skip interpolation so validate
		// reports the real problem (use jekyo init) instead of a missing
		// variable.
		return nil, validate(&app)
	}
	for name, svc := range app.Services {
		for k, v := range svc.Env {
			iv, err := interpolate(v, env)
			if err != nil {
				return nil, fmt.Errorf("service %s: env %s: %w", name, k, err)
			}
			svc.Env[k] = iv
		}
	}
	if err := validate(&app); err != nil {
		return nil, err
	}
	return &app, nil
}

// ParseFile is Parse over a file, with the process environment (plus
// extraEnv overrides) available for interpolation.
func ParseFile(path string, extraEnv map[string]string) (*App, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	for k, v := range extraEnv {
		env[k] = v
	}
	return Parse(data, env)
}

var interpRe = regexp.MustCompile(`\$\$|\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// interpolate expands ${VAR}; $$ escapes a literal $. Unset variables are a
// hard error — silent empty strings hide misconfiguration.
func interpolate(s string, env map[string]string) (string, error) {
	var missing []string
	out := interpRe.ReplaceAllStringFunc(s, func(m string) string {
		if m == "$$" {
			return "$"
		}
		name := m[2 : len(m)-1]
		v, ok := env[name]
		if !ok {
			missing = append(missing, name)
			return m
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("undefined variable ${%s} (set it in the environment or --env-file)", strings.Join(missing, "}, ${"))
	}
	return out, nil
}

var nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

func validate(app *App) error {
	var errs []string
	bad := func(format string, args ...any) {
		errs = append(errs, fmt.Sprintf(format, args...))
	}

	if app.Name == "" {
		bad("app: name is required")
	} else if !nameRe.MatchString(app.Name) {
		bad("app: %q must be lowercase alphanumeric/dashes (max 40 chars)", app.Name)
	}
	if len(app.Services) == 0 {
		bad("services: at least one service is required")
	}
	if len(app.Inputs) > 0 {
		bad("inputs: this file is a template with unresolved inputs — start from it with 'jekyo init'")
	}

	for _, name := range sortedKeys(app.Services) {
		svc := app.Services[name]
		p := "service " + name
		if !nameRe.MatchString(name) {
			bad("%s: name must be lowercase alphanumeric/dashes", p)
		}
		if svc.Image == "" && svc.Build == nil {
			bad("%s: needs image: or build:", p)
		}
		if svc.Image != "" && svc.Build != nil {
			bad("%s: image: and build: are mutually exclusive", p)
		}
		if b := svc.Build; b != nil {
			if b.Dockerfile != "" && b.Inline != "" {
				bad("%s: build.dockerfile and build.inline are mutually exclusive", p)
			}
		}
		if svc.HTTP != nil {
			if svc.HTTP.Domain == "" {
				bad("%s: http.domain is required", p)
			}
			if svc.MainPort() == 0 {
				bad("%s: http requires a port", p)
			}
			if svc.HTTP.Auth != "" && svc.HTTP.Auth != "basic" {
				bad("%s: http.auth must be \"basic\"", p)
			}
		}
		for _, e := range svc.Expose {
			if e.Port == 0 {
				bad("%s: expose.port is required", p)
			}
			if e.Node != 0 && (e.Node < 30000 || e.Node > 32767) {
				bad("%s: expose.node %d outside NodePort range 30000-32767", p, e.Node)
			}
			if e.Protocol != "" && e.Protocol != "tcp" && e.Protocol != "udp" {
				bad("%s: expose.protocol must be tcp or udp", p)
			}
		}
		for _, q := range []struct{ key, val string }{
			{"cpu", svc.Resources.CPU}, {"cpu-max", svc.Resources.CPUMax},
			{"memory", svc.Resources.Memory}, {"memory-max", svc.Resources.MemoryMax},
		} {
			if q.val == "" {
				continue
			}
			if _, err := resource.ParseQuantity(q.val); err != nil {
				bad("%s: resources.%s: invalid quantity %q", p, q.key, q.val)
			}
		}
		if svc.Replicas != nil && *svc.Replicas > 1 && svc.IsStateful() {
			bad("%s: replicas > 1 with volumes needs per-replica volumes (not yet supported)", p)
		}
		for vol := range svc.Volumes {
			if _, ok := app.Volumes[vol]; !ok {
				bad("%s: volume %q is not declared in the top-level volumes: block", p, vol)
			}
		}
		if svc.Health != nil && svc.Health.Path == "" && len(svc.Health.Command) == 0 {
			bad("%s: health needs path (HTTP probe) or command (exec probe)", p)
		}
		if svc.Health != nil && svc.Health.Path != "" && len(svc.Health.Command) > 0 {
			bad("%s: health.path and health.command are mutually exclusive", p)
		}
		for vol, vm := range svc.Volumes {
			if vm.Path == "" {
				bad("%s: volume %s needs a mount path", p, vol)
			}
		}
		if svc.Schedule != "" {
			if svc.HTTP != nil || svc.Replicas != nil || len(svc.Volumes) > 0 || len(svc.Expose) > 0 {
				bad("%s: schedule is mutually exclusive with http, replicas, volumes, and expose", p)
			}
			if len(strings.Fields(svc.Schedule)) != 5 {
				bad("%s: schedule %q is not a 5-field cron expression", p, svc.Schedule)
			}
		}
	}

	for _, name := range sortedKeys(app.Volumes) {
		v := app.Volumes[name]
		if v.Size == "" {
			bad("volume %s: size is required", name)
		} else if _, err := resource.ParseQuantity(v.Size); err != nil {
			bad("volume %s: invalid size %q", name, v.Size)
		}
		used := false
		for _, svc := range app.Services {
			if _, ok := svc.Volumes[name]; ok {
				used = true
			}
		}
		if !used {
			bad("volume %s: declared but not mounted by any service", name)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("invalid jekyo.yaml:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
