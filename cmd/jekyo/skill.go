package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

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
	return &cobra.Command{
		Use:   "templates",
		Short: "List available app templates (catalog + builtin)",
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, t := range templates.List() {
				cmd.Printf("%-14s %s\n", t.Name, t.Description)
			}
			cmd.Println("\nStart one with: jekyo init <name>")
			return nil
		},
	}
}

func newInitCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init [template]",
		Short: "Write a jekyo.yaml from a template (default: a minimal skeleton)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := os.Stat("jekyo.yaml"); err == nil && !force {
				return fmt.Errorf("jekyo.yaml already exists (use --force to overwrite)")
			}
			var data []byte
			if len(args) == 1 {
				var err error
				if data, err = templates.Get(args[0]); err != nil {
					return err
				}
			} else {
				data = []byte("app: myapp\nservices:\n  web:\n    build:\n      context: .\n    port: 8080\n    http:\n      domain: myapp.example.com\n")
			}
			if err := os.WriteFile("jekyo.yaml", data, 0o644); err != nil {
				return err
			}
			cmd.Println("Wrote jekyo.yaml — edit it, then run: jekyo up")
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing jekyo.yaml")
	return cmd
}
