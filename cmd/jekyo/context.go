package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/jekyo/jekyo/internal/contexts"
	"github.com/jekyo/jekyo/internal/provision"
)

func newContextCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "context",
		Aliases: []string{"ctx"},
		Short:   "Manage contexts (servers)",
	}
	cmd.AddCommand(
		newContextAddCmd(),
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

// unitArg extracts the value following a flag in a k3s systemd unit,
// where every argument sits on its own quoted, backslash-continued line.
func unitArg(unit, flag string) string {
	fields := strings.FieldsFunc(unit, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\t' || r == '\\' || r == '\''
	})
	for i, f := range fields {
		if f == flag && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

// newContextAddCmd adopts an existing JEKYO server: it discovers
// everything a context needs from the server itself, so no export blob
// from another machine is required.
func newContextAddCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "add <user@host>",
		Short: "Add a context for a server that already runs JEKYO",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			ssh, err := dial(target)
			if err != nil {
				return err
			}
			defer ssh.Close()

			if _, err := ssh.Run("test -f /etc/rancher/k3s/k3s.yaml"); err != nil {
				return fmt.Errorf("no JEKYO installation found on %s; set one up with: jekyo server install %s", target, target)
			}

			// everything a context holds is recoverable from the server;
			// the unit file is parsed client-side, no shell quoting games
			unit, err := ssh.Run("cat /etc/systemd/system/k3s.service")
			if err != nil {
				return fmt.Errorf("reading the k3s unit: %w", err)
			}
			ip := unitArg(unit, "--advertise-address")
			storage := unitArg(unit, "--default-local-storage-path")

			out, err := ssh.Run(
				"uname -m; " +
					"k3s kubectl get ingress -n kube-system registry -o jsonpath='{.spec.rules[0].host}' 2>/dev/null; echo; " +
					"test -f /var/lib/rancher/k3s/server/manifests/jekyo-vpn.yaml && echo vpn=yes || echo vpn=no; " +
					"grep -A2 'auth:' /etc/rancher/k3s/registries.yaml | grep -E 'username|password' | head -2")
			if err != nil {
				return fmt.Errorf("inspecting the server: %w", err)
			}
			var arch, regHost, regUser, regPass string
			vpnPresent := false
			for _, line := range strings.Split(out, "\n") {
				line = strings.TrimSpace(line)
				switch {
				case line == "x86_64" || line == "aarch64" || line == "arm64" || line == "amd64":
					arch = line
				case strings.HasPrefix(line, "registry."):
					regHost = line
				case line == "vpn=yes":
					vpnPresent = true
				case strings.HasPrefix(line, "username:"):
					regUser = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "username:")), `"`)
				case strings.HasPrefix(line, "password:"):
					regPass = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "password:")), `"`)
				}
			}
			if ip == "" {
				return fmt.Errorf("could not determine the server's public IP from its k3s unit (read %d bytes, head: %.120s); is this a JEKYO install?", len(unit), unit)
			}
			domain := strings.TrimPrefix(regHost, "registry.")

			if name == "" {
				name = strings.TrimPrefix(domain, "www.")
				if name == "" {
					name = strings.ReplaceAll(ip, ".", "-")
				}
				if i := strings.Index(name, "."); i > 0 {
					name = name[:i]
				}
			}
			store, err := contexts.Open()
			if err != nil {
				return err
			}
			if _, err := store.Get(name); err == nil {
				return fmt.Errorf("context %q already exists; pick another with --name", name)
			}

			in := &provision.Installer{SSH: ssh, Cfg: provision.Config{IP: ip}}
			kubeconfig, err := in.FetchKubeconfig()
			if err != nil {
				return err
			}

			meta := contexts.Meta{
				Name: name, SSH: target, IP: ip, Domain: domain,
				Storage: storage, Arch: arch, CreatedAt: time.Now(),
			}
			if regUser != "" {
				meta.Registry = &contexts.Registry{Host: regHost, Username: regUser, Password: regPass}
			}
			if vpnPresent {
				meta.VPN = &contexts.VPN{}
				if domain != "" {
					meta.VPN.AdminURL = "https://vpn." + domain
				}
			}
			if err := store.Save(meta); err != nil {
				return err
			}
			if err := os.WriteFile(store.KubeconfigPath(name), kubeconfig, 0o600); err != nil {
				return err
			}
			if err := store.SetCurrent(name); err != nil {
				return err
			}
			cmd.Printf("Context %q added (now current): %s", name, ip)
			if domain != "" {
				cmd.Printf(" · %s", domain)
			}
			cmd.Println()
			if vpnPresent {
				cmd.Println("Note: the VPN admin password cannot be recovered from the server; it stays on the machine that installed it (jekyo context export there, import here, if you need it).")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "context name (default: derived from the domain or IP)")
	return cmd
}
