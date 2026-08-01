// Package build turns `build:` blocks into images the cluster can run:
// content-hash tagging (unchanged sources never rebuild), docker buildx
// targeting the server's architecture, and delivery over SSH — the built
// tar is streamed into the server's containerd and pushed from there into
// the in-cluster registry. The laptop's Docker daemon never needs to reach
// the registry, so no insecure-registry configuration is required.
package build

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jekyo/jekyo/internal/addons"
	"github.com/jekyo/jekyo/internal/dsl"
	"github.com/jekyo/jekyo/internal/registry"
)

// PlatformFor maps a server arch (uname -m) to a buildx platform.
func PlatformFor(arch string) string {
	switch arch {
	case "aarch64", "arm64":
		return "linux/arm64"
	default:
		return "linux/amd64"
	}
}

// Env is everything EnsureAll needs to check, deliver, and register images.
type Env struct {
	Registry *registry.Client // reachable view of the cluster registry
	// Deliver streams a docker-archive tar (tagged ref) to the cluster and
	// makes it available both to containerd and the in-cluster registry.
	Deliver  func(ref string, tar io.Reader, size int64) error
	Platform string
}

// Result records one built (or reused) service image.
type Result struct {
	Service string
	// Image is the in-cluster reference (registry.jekyo.local/...) used in
	// pod specs.
	Image  string
	Reused bool
}

// EnsureAll builds and delivers every build: service whose content hash the
// registry doesn't have yet, then rewrites those services to image:
// references. baseDir anchors relative build contexts (the jekyo.yaml dir).
func EnsureAll(app *dsl.App, baseDir string, env Env, log io.Writer) ([]Result, error) {
	var names []string
	for name, svc := range app.Services {
		if svc.Build != nil {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	sort.Strings(names)

	if _, err := exec.LookPath("docker"); err != nil {
		return nil, fmt.Errorf("docker is required to build images (services: %s)", strings.Join(names, ", "))
	}

	var results []Result
	for _, name := range names {
		svc := app.Services[name]
		repo := app.Name + "/" + name
		tag, err := contentHash(baseDir, svc.Build, env.Platform)
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", name, err)
		}
		ref := addons.RegistryHost + "/" + repo + ":" + tag

		exists, err := env.Registry.HasTag(repo, tag)
		if err != nil {
			return nil, fmt.Errorf("service %s: checking registry: %w", name, err)
		}
		if exists {
			fmt.Fprintf(log, "→ %s: unchanged (%s), skipping build\n", name, tag)
		} else {
			fmt.Fprintf(log, "→ building %s (%s, %s)\n", name, tag, env.Platform)
			tarPath, err := buildx(baseDir, svc.Build, env.Platform, ref, log)
			if err != nil {
				return nil, fmt.Errorf("service %s: %w", name, err)
			}
			f, err := os.Open(tarPath)
			if err != nil {
				return nil, err
			}
			st, _ := f.Stat()
			fmt.Fprintf(log, "→ shipping %s to cluster (%d MB)\n", name, st.Size()/1024/1024)
			err = env.Deliver(ref, f, st.Size())
			f.Close()
			os.Remove(tarPath)
			if err != nil {
				return nil, fmt.Errorf("service %s: delivering image: %w", name, err)
			}
		}

		svc.Image = ref
		svc.Build = nil
		app.Services[name] = svc
		results = append(results, Result{Service: name, Image: ref, Reused: exists})
	}
	return results, nil
}

// buildx runs docker buildx into a docker-archive tar tagged as ref, and
// returns the tar path (caller removes it).
func buildx(baseDir string, b *dsl.Build, platform, ref string, log io.Writer) (string, error) {
	ctxDir := b.Context
	if ctxDir == "" {
		ctxDir = "."
	}
	if !filepath.IsAbs(ctxDir) {
		ctxDir = filepath.Join(baseDir, ctxDir)
	}
	out, err := os.CreateTemp("", "jekyo-image-*.tar")
	if err != nil {
		return "", err
	}
	out.Close()

	args := []string{"buildx", "build",
		"--platform", platform,
		"-t", ref,
		"--output", "type=docker,dest=" + out.Name(),
		"--provenance=false",
	}
	if b.Inline != "" {
		tmp, err := os.CreateTemp("", "jekyo-dockerfile-*")
		if err != nil {
			return "", err
		}
		defer os.Remove(tmp.Name())
		if _, err := tmp.WriteString(b.Inline); err != nil {
			return "", err
		}
		tmp.Close()
		args = append(args, "-f", tmp.Name())
	} else if b.Dockerfile != "" {
		df := b.Dockerfile
		if !filepath.IsAbs(df) {
			df = filepath.Join(baseDir, df)
		}
		args = append(args, "-f", df)
	}
	for _, k := range sortedKeys(b.Args) {
		args = append(args, "--build-arg", k+"="+b.Args[k])
	}
	args = append(args, ctxDir)

	cmd := exec.Command("docker", args...)
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Run(); err != nil {
		os.Remove(out.Name())
		return "", err
	}
	return out.Name(), nil
}

// contentHash digests everything that determines the image: the build
// context files (respecting .dockerignore), the Dockerfile (inline or
// referenced), build args, and the platform. Same inputs → same tag →
// build skipped.
func contentHash(baseDir string, b *dsl.Build, platform string) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "platform=%s\n", platform)
	for _, k := range sortedKeys(b.Args) {
		fmt.Fprintf(h, "arg=%s=%s\n", k, b.Args[k])
	}

	ctxDir := b.Context
	if ctxDir == "" {
		ctxDir = "."
	}
	if !filepath.IsAbs(ctxDir) {
		ctxDir = filepath.Join(baseDir, ctxDir)
	}

	if b.Inline != "" {
		fmt.Fprintf(h, "dockerfile-inline\n%s\n", b.Inline)
	} else {
		df := b.Dockerfile
		if df == "" {
			df = "Dockerfile"
		}
		if !filepath.IsAbs(df) {
			df = filepath.Join(baseDir, df)
		}
		data, err := os.ReadFile(df)
		if err != nil {
			return "", fmt.Errorf("reading dockerfile: %w", err)
		}
		fmt.Fprintf(h, "dockerfile\n%s\n", data)
	}

	ignore := loadDockerignore(ctxDir)
	err := filepath.WalkDir(ctxDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(ctxDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || ignore.matches(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if ignore.matches(rel) {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		fmt.Fprintf(h, "file=%s\n", rel)
		_, err = io.Copy(h, f)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("hashing build context %s: %w", ctxDir, err)
	}
	return hex.EncodeToString(h.Sum(nil))[:12], nil
}

type ignoreList struct {
	patterns []string
}

// loadDockerignore supports the common .dockerignore subset: exact paths,
// * globs per path element, and directory prefixes. Negations are ignored.
func loadDockerignore(dir string) ignoreList {
	data, err := os.ReadFile(filepath.Join(dir, ".dockerignore"))
	if err != nil {
		return ignoreList{}
	}
	var pats []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		pats = append(pats, strings.TrimSuffix(line, "/"))
	}
	return ignoreList{patterns: pats}
}

func (l ignoreList) matches(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, p := range l.patterns {
		if ok, _ := filepath.Match(p, rel); ok {
			return true
		}
		if strings.HasPrefix(rel, p+"/") {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
