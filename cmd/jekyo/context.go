package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/jekyo/jekyo/internal/contexts"
)

func newContextCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "context",
		Aliases: []string{"ctx"},
		Short:   "Manage contexts (servers)",
	}
	cmd.AddCommand(
		newContextLsCmd(),
		newContextUseCmd(),
		newContextRmCmd(),
		newContextShowCmd(),
		newContextExportCmd(),
		newContextImportCmd(),
	)
	return cmd
}

// newContextExportCmd emits a context (meta + kubeconfig) as base64 for CI
// secrets; import restores it on the other side.
func newContextExportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "export [name]",
		Short: "Print a context as base64 (store it as a CI secret)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := contexts.Open()
			if err != nil {
				return err
			}
			name := contextFlag
			if len(args) == 1 {
				name = args[0]
			}
			m, err := store.Resolve(name)
			if err != nil {
				return err
			}
			blob, err := store.Export(m.Name)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), blob)
			return nil
		},
	}
}

func newContextImportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "import",
		Short: "Restore a context from stdin (output of 'context export')",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := contexts.Open()
			if err != nil {
				return err
			}
			data, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return err
			}
			name, err := store.Import(strings.TrimSpace(string(data)))
			if err != nil {
				return err
			}
			if err := store.SetCurrent(name); err != nil {
				return err
			}
			cmd.Println("Imported context:", name, "(now current)")
			return nil
		},
	}
}

func newContextLsCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List contexts",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := contexts.Open()
			if err != nil {
				return err
			}
			all, err := store.List()
			if err != nil {
				return err
			}
			if output == "json" {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(all)
			}
			if len(all) == 0 {
				cmd.Println("No contexts. Create one with 'jekyo server install <user@host>'.")
				return nil
			}
			current, _ := store.CurrentName()
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tIP\tDOMAIN\tAGE")
			for _, m := range all {
				name := m.Name
				if m.Name == current {
					name += " *"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", name, m.IP, orDash(m.Domain), age(m.CreatedAt))
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "output format: json")
	return cmd
}

func newContextUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Short: "Set the current context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := contexts.Open()
			if err != nil {
				return err
			}
			if err := store.SetCurrent(args[0]); err != nil {
				return err
			}
			cmd.Println("Current context:", args[0])
			return nil
		},
	}
}

func newContextRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <name>",
		Short: "Remove a context (local only; the server is untouched)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := contexts.Open()
			if err != nil {
				return err
			}
			if err := store.Remove(args[0]); err != nil {
				return err
			}
			cmd.Println("Removed context:", args[0])
			return nil
		},
	}
}

func newContextShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show [name]",
		Short: "Show a context's details",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := contexts.Open()
			if err != nil {
				return err
			}
			name := contextFlag
			if len(args) == 1 {
				name = args[0]
			}
			m, err := store.Resolve(name)
			if err != nil {
				return err
			}
			out, err := m.JSON()
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		},
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ago renders a past timestamp for prose ("5m ago", "just now").
func ago(t time.Time) string {
	if a := age(t); a != "just now" && a != "-" {
		return a + " ago"
	} else {
		return a
	}
}

func age(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
