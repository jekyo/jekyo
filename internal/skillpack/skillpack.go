// Package skillpack teaches AI coding agents the JEKYO DSL and CLI: it
// writes per-agent instruction files from one embedded reference, so agents
// stop hallucinating keys and use `jekyo render` to self-check. Content
// ships inside the binary, so it always matches the installed version.
package skillpack

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed reference.md
var Reference string

//go:embed schema.json
var Schema string

const (
	beginMarker = "<!-- jekyo:begin (managed by 'jekyo skill install') -->"
	endMarker   = "<!-- jekyo:end -->"
)

// Agent identifies a supported coding agent.
type Agent string

const (
	Claude Agent = "claude" // Claude Code: .claude/skills/jekyo/SKILL.md
	Codex  Agent = "codex"  // Codex / OpenCode: AGENTS.md managed section
	Cursor Agent = "cursor" // Cursor: .cursor/rules/jekyo.mdc
)

var All = []Agent{Claude, Codex, Cursor}

// Detect returns the agents configured in dir (by their conventional files).
func Detect(dir string) []Agent {
	var out []Agent
	if _, err := os.Stat(filepath.Join(dir, ".claude")); err == nil {
		out = append(out, Claude)
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); err == nil {
		out = append(out, Codex)
	}
	if _, err := os.Stat(filepath.Join(dir, ".cursor")); err == nil {
		out = append(out, Cursor)
	}
	return out
}

const skillFrontmatter = "---\nname: jekyo\ndescription: Deploy and operate apps on JEKYO k3s servers — write/edit jekyo.yaml files and drive the jekyo CLI. Use when the user mentions jekyo, deploying to their server, or a jekyo.yaml file, or invokes /jekyo.\n---\n\nYou operate JEKYO through its CLI. Workflow: `jekyo context show` to see the\ntarget server; write or edit jekyo.yaml; ALWAYS `jekyo render` to validate\nbefore `jekyo up`; then check with `jekyo ps <app>` / `jekyo status <app>`.\nRead surfaces support `-o json`. Full reference below (also available\nanywhere via `jekyo skill show`).\n\n"

// Install writes/refreshes the instruction pack for each agent.
// Project scope: files under dir. Global scope (global=true): user-level
// locations, so `/jekyo ...` works in every session, omarchy-style —
// ~/.claude/skills for Claude Code, ~/.codex/AGENTS.md for Codex.
// Returns the files written.
func Install(dir string, agents []Agent, global bool) ([]string, error) {
	var written []string
	for _, a := range agents {
		switch a {
		case Claude:
			base := filepath.Join(dir, ".claude")
			if global {
				home, err := os.UserHomeDir()
				if err != nil {
					return written, err
				}
				base = filepath.Join(home, ".claude")
			}
			path := filepath.Join(base, "skills", "jekyo", "SKILL.md")
			if err := writeFile(path, skillFrontmatter+Reference); err != nil {
				return written, err
			}
			written = append(written, path)
		case Codex:
			path := filepath.Join(dir, "AGENTS.md")
			if global {
				home, err := os.UserHomeDir()
				if err != nil {
					return written, err
				}
				path = filepath.Join(home, ".codex", "AGENTS.md")
			}
			if err := upsertSection(path, Reference); err != nil {
				return written, err
			}
			written = append(written, path)
		case Cursor:
			if global {
				continue // Cursor rules are project-scoped
			}
			path := filepath.Join(dir, ".cursor", "rules", "jekyo.mdc")
			content := "---\ndescription: JEKYO jekyo.yaml DSL and CLI reference\nglobs: [\"**/jekyo.yaml\"]\nalwaysApply: false\n---\n\n" + Reference
			if err := writeFile(path, content); err != nil {
				return written, err
			}
			written = append(written, path)
		default:
			return written, fmt.Errorf("unknown agent %q (valid: claude, codex, cursor)", a)
		}
	}
	return written, nil
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// upsertSection creates or replaces the marker-delimited JEKYO section in a
// shared file like AGENTS.md, preserving everything else.
func upsertSection(path, body string) error {
	section := beginMarker + "\n\n" + body + "\n" + endMarker
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return writeFile(path, section+"\n")
	}
	if err != nil {
		return err
	}
	s := string(data)
	if i := strings.Index(s, beginMarker); i >= 0 {
		if j := strings.Index(s, endMarker); j > i {
			s = s[:i] + section + s[j+len(endMarker):]
		} else {
			return fmt.Errorf("%s: begin marker without end marker — fix the file manually", path)
		}
	} else {
		s = strings.TrimRight(s, "\n") + "\n\n" + section + "\n"
	}
	return os.WriteFile(path, []byte(s), 0o644)
}
