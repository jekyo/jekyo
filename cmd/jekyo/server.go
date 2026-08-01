package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/jekyo/jekyo/internal/contexts"
	"github.com/jekyo/jekyo/internal/provision"
	"github.com/jekyo/jekyo/internal/sshx"
)

func newServerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Install, inspect, and uninstall JEKYO servers",
	}
	cmd.AddCommand(
		newServerPreflightCmd(),
		newServerInstallCmd(),
		newServerUninstallCmd(),
		newServerInfoCmd(),
	)
	return cmd
}

var sshKeyFlag string

func dial(target string) (*sshx.Client, error) {
	return sshx.Dial(target, sshx.Options{KeyPath: sshKeyFlag})
}

func printPreflight(cmd *cobra.Command, results []provision.CheckResult) {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
	for _, r := range results {
		note := ""
		if r.Fixable {
			note = " (fixable with --fix)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s%s\n", r.Status, r.Name, r.Detail, note)
	}
	w.Flush()
}

func newServerPreflightCmd() *cobra.Command {
	var storage string
	cmd := &cobra.Command{
		Use:   "preflight <user@host>",
		Short: "Check a server without changing anything",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ssh, err := dial(args[0])
			if err != nil {
				return err
			}
			defer ssh.Close()
			in := &provision.Installer{SSH: ssh, Cfg: provision.Config{StoragePath: storage}, Out: cmd.OutOrStdout()}
			facts, err := in.GatherFacts()
			if err != nil {
				return err
			}
			results := provision.Preflight(facts, in.Cfg)
			printPreflight(cmd, results)
			if provision.Blocking(results) {
				return fmt.Errorf("preflight has failures")
			}
			cmd.Println("\nServer is installable.")
			return nil
		},
	}
	cmd.Flags().StringVar(&storage, "storage", "/storage", "planned storage path (for the disk-space check)")
	return cmd
}

func newServerInstallCmd() *cobra.Command {
	var cfg provision.Config
	var name string
	var purgeDockerData bool
	cmd := &cobra.Command{
		Use:   "install <user@host>",
		Short: "Turn an Ubuntu server into a JEKYO cluster (idempotent)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			if cfg.IP == "" {
				return fmt.Errorf("--ip is required (the public IP the cluster advertises)")
			}
			if cfg.Domain != "" && cfg.AcmeEmail == "" {
				return fmt.Errorf("--acme-email is required with --domain (Let's Encrypt registration)")
			}
			if name == "" {
				name = strings.ReplaceAll(cfg.IP, ".", "-")
			}
			store, err := contexts.Open()
			if err != nil {
				return err
			}

			ssh, err := dial(target)
			if err != nil {
				return err
			}
			defer ssh.Close()
			in := &provision.Installer{SSH: ssh, Cfg: cfg, Out: cmd.OutOrStdout()}

			facts, err := in.GatherFacts()
			if err != nil {
				return err
			}
			if cfg.RemoveDocker && facts.Docker != nil {
				if purgeDockerData {
					if !confirm(cmd, fmt.Sprintf("Also DELETE /var/lib/docker on %s (images and volumes, unrecoverable)?", target)) {
						purgeDockerData = false
					}
				}
				if err := in.RemoveDocker(purgeDockerData); err != nil {
					return err
				}
				if facts, err = in.GatherFacts(); err != nil {
					return err
				}
			}
			results := provision.Preflight(facts, cfg)
			printPreflight(cmd, results)
			if cfg.Fix && len(provision.Fixables(results)) > 0 {
				if err := in.ApplyFixes(results); err != nil {
					return err
				}
				if facts, err = in.GatherFacts(); err != nil {
					return err
				}
				results = provision.Preflight(facts, cfg)
			}
			if provision.Blocking(results) {
				return fmt.Errorf("preflight failed — fix the FAIL items above and re-run")
			}

			// Reuse credentials on converge so re-installs don't rotate secrets.
			creds := provision.Credentials{RegistryUser: "jekyo"}
			if prev, err := store.Get(name); err == nil {
				if prev.Registry != nil {
					creds.RegistryUser = prev.Registry.Username
					creds.RegistryPassword = prev.Registry.Password
				}
				if prev.VPN != nil {
					creds.VPNPassword = prev.VPN.Password
				}
			}
			if creds.RegistryPassword == "" {
				if creds.RegistryPassword, err = provision.NewPassword(); err != nil {
					return err
				}
			}
			if creds.VPNPassword == "" {
				if creds.VPNPassword, err = provision.NewPassword(); err != nil {
					return err
				}
			}

			if err := in.Install(facts, creds); err != nil {
				return err
			}

			kubeconfig, err := in.FetchKubeconfig()
			if err != nil {
				return err
			}
			meta := contexts.Meta{
				Name:      name,
				SSH:       target,
				IP:        cfg.IP,
				Domain:    cfg.Domain,
				Storage:   cfg.StoragePath,
				Arch:      facts.Arch,
				CreatedAt: time.Now(),
				Registry:  &contexts.Registry{Username: creds.RegistryUser, Password: creds.RegistryPassword},
			}
			if cfg.Domain != "" {
				meta.Registry.Host = "registry." + cfg.Domain
			}
			if !cfg.NoVPN {
				meta.VPN = &contexts.VPN{Password: creds.VPNPassword}
				if cfg.Domain != "" {
					meta.VPN.AdminURL = "https://vpn." + cfg.Domain
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

			cmd.Println("\nCluster ready. Context:", name, "(now current)")
			cmd.Println("  kubectl:  jekyo kubectl -- get pods -A")
			if meta.Registry.Host != "" {
				cmd.Printf("  registry: https://%s (user %s, password in 'jekyo context show')\n", meta.Registry.Host, creds.RegistryUser)
			}
			if meta.VPN != nil {
				cmd.Printf("  vpn:      admin UI %s (password in 'jekyo context show')\n", orDash(meta.VPN.AdminURL))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cfg.IP, "ip", "", "public IP of the server (required)")
	cmd.Flags().StringVar(&cfg.StoragePath, "storage", "/storage", "persisted local volume path")
	cmd.Flags().StringVar(&cfg.Domain, "domain", "", "base domain (enables TLS certs, registry.<domain>, vpn.<domain>)")
	cmd.Flags().StringVar(&cfg.AcmeEmail, "acme-email", "", "Let's Encrypt account email (required with --domain)")
	cmd.Flags().StringVar(&name, "name", "", "context name (default: derived from --ip)")
	cmd.Flags().StringVar(&cfg.K3sVersion, "k3s-version", "", "k3s version (default "+provision.DefaultK3sVersion+")")
	cmd.Flags().BoolVar(&cfg.NoVPN, "no-vpn", false, "skip the WireGuard VPN addon")
	cmd.Flags().BoolVar(&cfg.NoGPU, "no-gpu", false, "skip NVIDIA setup even if a GPU is present")
	cmd.Flags().BoolVar(&cfg.Fix, "fix", false, "auto-remediate fixable preflight warnings")
	cmd.Flags().BoolVar(&cfg.RemoveDocker, "remove-docker", false, "purge a detected Docker engine before installing")
	cmd.Flags().BoolVar(&purgeDockerData, "purge-docker-data", false, "with --remove-docker: also delete /var/lib/docker (asks confirmation)")
	return cmd
}

func newServerUninstallCmd() *cobra.Command {
	var purgeStorage bool
	cmd := &cobra.Command{
		Use:   "uninstall <context>",
		Short: "Remove k3s from the server and delete the context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := contexts.Open()
			if err != nil {
				return err
			}
			m, err := store.Get(args[0])
			if err != nil {
				return err
			}
			if !confirm(cmd, fmt.Sprintf("Uninstall k3s and all apps from %s (%s)?", m.Name, m.SSH)) {
				return fmt.Errorf("aborted")
			}
			if purgeStorage {
				if !confirm(cmd, fmt.Sprintf("Also DELETE all data under %s on the server (unrecoverable)?", m.Storage)) {
					purgeStorage = false
				}
			}
			ssh, err := dial(m.SSH)
			if err != nil {
				return err
			}
			defer ssh.Close()
			in := &provision.Installer{SSH: ssh, Cfg: provision.Config{StoragePath: m.Storage}, Out: cmd.OutOrStdout()}
			if err := in.Uninstall(purgeStorage); err != nil {
				return err
			}
			if err := store.Remove(m.Name); err != nil {
				return err
			}
			cmd.Println("Server clean. Context removed.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&purgeStorage, "purge-storage", false, "also wipe the storage path (asks confirmation)")
	return cmd
}

func newServerInfoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "info",
		Short: "Show cluster health for the current context",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := contexts.Open()
			if err != nil {
				return err
			}
			m, err := store.Resolve(contextFlag)
			if err != nil {
				return err
			}
			ssh, err := dial(m.SSH)
			if err != nil {
				return err
			}
			defer ssh.Close()
			out, err := ssh.Run(
				`k3s --version | head -1; ` +
					`k3s kubectl get nodes -o wide --no-headers; ` +
					`echo "---not-running---"; ` +
					`k3s kubectl get pods -A --no-headers --field-selector=status.phase!=Running,status.phase!=Succeeded; ` +
					`echo "---disk---"; df -h ` + m.Storage + ` 2>/dev/null | tail -1; ` +
					`nvidia-smi -L 2>/dev/null; true`)
			if err != nil {
				return err
			}
			cmd.Println(out)
			return nil
		},
	}
	return cmd
}

// confirmScanner is shared across confirm calls: a fresh bufio.Scanner per
// call would read ahead and swallow answers meant for later prompts.
var confirmScanner *bufio.Scanner

func confirm(cmd *cobra.Command, question string) bool {
	fmt.Fprintf(cmd.OutOrStdout(), "%s [y/N]: ", question)
	if confirmScanner == nil {
		confirmScanner = bufio.NewScanner(cmd.InOrStdin())
	}
	if !confirmScanner.Scan() {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(confirmScanner.Text()), "y")
}
