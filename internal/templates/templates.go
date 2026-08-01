// Package templates powers `jekyo init` and `jekyo templates`: ready-made
// jekyo.yaml files from a GitHub-hosted catalog (updatable without a CLI
// release), with a small embedded set as offline fallback. Templates are
// ordinary jekyo.yaml files whose description:/icon: metadata renders the
// catalog listing.
package templates

import (
	"embed"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

//go:embed builtin
var builtin embed.FS

// CatalogURL is the raw base of the template repo; override with
// JEKYO_TEMPLATES_URL (e.g. a fork or an internal mirror).
const CatalogURL = "https://raw.githubusercontent.com/jekyo/templates/main"

type Template struct {
	Name        string
	Description string
	Source      string // "catalog" or "builtin"
}

type index struct {
	Templates []struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	} `yaml:"templates"`
}

func baseURL() string {
	if u := os.Getenv("JEKYO_TEMPLATES_URL"); u != "" {
		return strings.TrimSuffix(u, "/")
	}
	return CatalogURL
}

var httpClient = &http.Client{Timeout: 10 * time.Second}

// List returns catalog templates when reachable, merged over the builtins
// (catalog wins on name conflicts).
func List() []Template {
	byName := map[string]Template{}
	entries, _ := builtin.ReadDir("builtin")
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".yaml")
		desc, _ := descriptionOf(mustRead(path.Join("builtin", e.Name())))
		byName[name] = Template{Name: name, Description: desc, Source: "builtin"}
	}
	if resp, err := httpClient.Get(baseURL() + "/index.yaml"); err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			if data, err := io.ReadAll(resp.Body); err == nil {
				var idx index
				if yaml.Unmarshal(data, &idx) == nil {
					for _, t := range idx.Templates {
						byName[t.Name] = Template{Name: t.Name, Description: t.Description, Source: "catalog"}
					}
				}
			}
		}
	}
	out := make([]Template, 0, len(byName))
	for _, t := range byName {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get fetches a template's jekyo.yaml: catalog first, builtin fallback.
func Get(name string) ([]byte, error) {
	if resp, err := httpClient.Get(baseURL() + "/" + name + "/jekyo.yaml"); err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return io.ReadAll(resp.Body)
		}
	}
	data, err := builtin.ReadFile(path.Join("builtin", name+".yaml"))
	if err != nil {
		return nil, fmt.Errorf("template %q not found (see 'jekyo templates')", name)
	}
	return data, nil
}

func descriptionOf(data []byte) (string, error) {
	var meta struct {
		Description string `yaml:"description"`
	}
	err := yaml.Unmarshal(data, &meta)
	return meta.Description, err
}

func mustRead(p string) []byte {
	data, _ := builtin.ReadFile(p)
	return data
}
