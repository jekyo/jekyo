package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/jekyo/jekyo/internal/dsl"
	"github.com/jekyo/jekyo/internal/skillpack"
	"github.com/jekyo/jekyo/internal/templates"
)

func newSkillCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Teach AI coding agents the JEKYO DSL and CLI",
	}
	var agentFlag string
	var global bool
	install := &cobra.Command{
		Use:   "install",
		Short: "Write instruction packs for coding agents (Claude Code, Codex/OpenCode, Cursor)",
		Long: `Project scope (default): packs for agents detected in the current directory.
Global scope (--global): user-level install — '/jekyo <request>' then works
in every Claude Code session, and Codex picks it up from ~/.codex/AGENTS.md.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			var agents []skillpack.Agent
			switch agentFlag {
			case "":
				if global {
					agents = []skillpack.Agent{skillpack.Claude, skillpack.Codex}
				} else {
					agents = skillpack.Detect(dir)
					if len(agents) == 0 {
						return fmt.Errorf("no agent config detected in %s (looked for .claude/, AGENTS.md, .cursor/) — pass --agent all|claude|codex|cursor, or --global for a user-level install", dir)
					}
				}
			case "all":
				agents = skillpack.All
			default:
				agents = []skillpack.Agent{skillpack.Agent(agentFlag)}
			}
			written, err := skillpack.Install(dir, agents, global)
			if err != nil {
				return err
			}
			for _, f := range written {
				cmd.Println("wrote", f)
			}
			if global {
				cmd.Println("Installed globally — try '/jekyo deploy this app' in any Claude Code session.")
			}
			cmd.Println("Re-run after upgrading jekyo to refresh the packs.")
			return nil
		},
	}
	install.Flags().StringVar(&agentFlag, "agent", "", "all | claude | codex | cursor (default: auto-detect)")
	install.Flags().BoolVar(&global, "global", false, "install user-level (all projects) instead of project-level")
	show := &cobra.Command{
		Use:   "show",
		Short: "Print the full DSL+CLI reference (for any AI agent to ingest)",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprint(cmd.OutOrStdout(), skillpack.Reference)
		},
	}
	cmd.AddCommand(install, show)
	return cmd
}

func newSchemaCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "schema",
		Short: "Print the JSON Schema for jekyo.yaml",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprint(cmd.OutOrStdout(), skillpack.Schema)
		},
	}
}

func newTemplatesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "templates",
		Short: "List available app templates (catalog + builtin)",
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, t := range templates.List() {
				cmd.Printf("%-14s %s\n", t.Name, t.Description)
			}
			cmd.Println("\nStart one with: jekyo init <name> (see requirements: jekyo templates inspect <name>)")
			return nil
		},
	}

	var output string
	inspect := &cobra.Command{
		Use:   "inspect <name>",
		Short: "Show a template's description and required inputs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := templates.Get(args[0])
			if err != nil {
				return err
			}
			meta, err := templates.ParseMeta(data)
			if err != nil {
				return err
			}
			if output == "json" {
				type inputView struct {
					Name     string `json:"name"`
					Kind     string `json:"kind"`
					Prompt   string `json:"prompt,omitempty"`
					Default  string `json:"default,omitempty"`
					Required bool   `json:"required"`
				}
				view := struct {
					Name        string      `json:"name"`
					App         string      `json:"app"`
					Description string      `json:"description"`
					Icon        string      `json:"icon,omitempty"`
					Inputs      []inputView `json:"inputs"`
				}{Name: args[0], App: meta.App, Description: meta.Description, Icon: meta.Icon, Inputs: []inputView{}}
				for name, in := range meta.Inputs {
					view.Inputs = append(view.Inputs, inputView{
						Name: name, Kind: in.Kind, Prompt: in.Prompt,
						Default: in.Default, Required: in.IsRequired(),
					})
				}
				sort.Slice(view.Inputs, func(i, j int) bool { return view.Inputs[i].Name < view.Inputs[j].Name })
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(view)
			}
			cmd.Printf("%s — %s\n", args[0], meta.Description)
			if len(meta.Inputs) == 0 {
				cmd.Println("No inputs — 'jekyo init " + args[0] + "' works as-is.")
				return nil
			}
			cmd.Println("\nInputs:")
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			var names []string
			for n := range meta.Inputs {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				in := meta.Inputs[n]
				req := "optional"
				if in.IsRequired() {
					req = "REQUIRED"
				}
				extra := in.Prompt
				if in.Default != "" {
					extra += " (default: " + in.Default + ")"
				}
				if in.Kind == "secret" {
					extra += " (auto-generated if omitted)"
				}
				fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", n, in.Kind, req, strings.TrimSpace(extra))
			}
			w.Flush()
			cmd.Println("\nNon-interactive: jekyo init " + args[0] + " --defaults --set NAME=value ...")
			return nil
		},
	}
	inspect.Flags().StringVarP(&output, "output", "o", "", "output format: json")
	cmd.AddCommand(inspect)
	return cmd
}

func newInitCmd() *cobra.Command {
	var force, useDefaults bool
	var sets []string
	cmd := &cobra.Command{
		Use:   "init [template]",
		Short: "Write a jekyo.yaml from a template (default: a minimal skeleton)",
		Long: `Templates may declare inputs (domains, secrets, sizes). Interactively you
are prompted for each; non-interactively (--defaults, for AI agents and
scripts) defaults are accepted, secrets are generated, and missing required
inputs fail with a list. Discover inputs first: jekyo templates inspect <name>.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := os.Stat("jekyo.yaml"); err == nil && !force {
				return fmt.Errorf("jekyo.yaml already exists (use --force to overwrite)")
			}
			if len(args) == 0 {
				data := []byte("app: myapp\nservices:\n  web:\n    build:\n      context: .\n    port: 8080\n    http:\n      domain: myapp.example.com\n")
				if err := os.WriteFile("jekyo.yaml", data, 0o644); err != nil {
					return err
				}
				cmd.Println("Wrote jekyo.yaml — edit it, then run: jekyo up")
				return nil
			}

			data, err := templates.Get(args[0])
			if err != nil {
				return err
			}
			meta, err := templates.ParseMeta(data)
			if err != nil {
				return err
			}
			set := map[string]string{}
			for _, kv := range sets {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return fmt.Errorf("--set %q: want NAME=value", kv)
				}
				set[strings.TrimSpace(k)] = v
			}

			var prompt templates.PromptFunc
			interactive := !useDefaults && term.IsTerminal(int(os.Stdin.Fd()))
			if interactive {
				reader := bufio.NewReader(cmd.InOrStdin())
				prompt = func(name string, in dsl.Input) (string, error) {
					label := in.Prompt
					if label == "" {
						label = name
					}
					hint := ""
					if in.Default != "" {
						hint = " [" + in.Default + "]"
					}
					if in.Kind == "secret" {
						hint = " [generate]"
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%s%s: ", label, hint)
					line, err := reader.ReadString('\n')
					if err != nil {
						return "", err
					}
					return strings.TrimSpace(line), nil
				}
			}
			res, err := templates.Resolve(meta.Inputs, set, useDefaults && prompt == nil, prompt)
			if err != nil {
				return err
			}
			yamlOut, envOut := templates.Apply(data, res)
			if err := os.WriteFile("jekyo.yaml", yamlOut, 0o644); err != nil {
				return err
			}
			cmd.Println("Wrote jekyo.yaml")
			if envOut != nil {
				if err := os.WriteFile(".env", envOut, 0o600); err != nil {
					return err
				}
				appendGitignore(".env")
				cmd.Println("Wrote .env with generated secrets (gitignored) — deploy with: jekyo up --env-file .env")
			} else {
				cmd.Println("Deploy with: jekyo up")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing jekyo.yaml")
	cmd.Flags().BoolVar(&useDefaults, "defaults", false, "non-interactive: accept defaults, generate secrets")
	cmd.Flags().StringArrayVar(&sets, "set", nil, "input value NAME=value (repeatable)")
	return cmd
}

// appendGitignore makes sure name is ignored when a .gitignore exists or a
// git repo is present.
func appendGitignore(name string) {
	data, err := os.ReadFile(".gitignore")
	if err == nil && strings.Contains(string(data), name) {
		return
	}
	if err != nil {
		if _, statErr := os.Stat(".git"); statErr != nil {
			return
		}
	}
	f, err := os.OpenFile(".gitignore", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, name)
}
