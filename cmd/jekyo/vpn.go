package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/jekyo/jekyo/internal/contexts"
	"github.com/jekyo/jekyo/internal/kube"
	"github.com/jekyo/jekyo/internal/vpn"
)

// vpnClient reaches wg-easy's admin API: via the public domain when there is
// one, otherwise by dialing the vpn-admin ClusterIP through SSH.
func vpnClient(m contexts.Meta, store *contexts.Store) (*vpn.Client, func(), error) {
	if m.VPN == nil {
		return nil, nil, fmt.Errorf("context %q has no VPN (installed with --no-vpn?)", m.Name)
	}
	cleanup := func() {}
	var transport http.RoundTripper
	base := m.VPN.AdminURL
	if base == "" {
		kc, err := kube.New(store.KubeconfigPath(m.Name))
		if err != nil {
			return nil, nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		svc, err := kc.Typed.CoreV1().Services("kube-system").Get(ctx, "vpn-admin", metav1.GetOptions{})
		if err != nil {
			return nil, nil, fmt.Errorf("finding vpn-admin service: %w", err)
		}
		addr := svc.Spec.ClusterIP + ":51821"
		ssh, err := dial(m.SSH)
		if err != nil {
			return nil, nil, err
		}
		cleanup = func() { ssh.Close() }
		transport = &http.Transport{
			DialContext: func(ctx context.Context, network, a string) (net.Conn, error) {
				return ssh.DialRemote(addr)
			},
		}
		base = "http://vpn-admin.internal" // host is irrelevant; the dialer decides
	}
	c, err := vpn.New(base, transport)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	if err := c.Login(m.VPN.Password); err != nil {
		cleanup()
		return nil, nil, err
	}
	return c, cleanup, nil
}

func newVPNCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vpn",
		Short: "Manage WireGuard access to the cluster network",
	}

	withClient := func(fn func(cmd *cobra.Command, c *vpn.Client, args []string) error) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			store, err := contexts.Open()
			if err != nil {
				return err
			}
			m, err := store.Resolve(contextFlag)
			if err != nil {
				return err
			}
			c, cleanup, err := vpnClient(m, store)
			if err != nil {
				return err
			}
			defer cleanup()
			return fn(cmd, c, args)
		}
	}

	peers := &cobra.Command{
		Use:     "peers",
		Aliases: []string{"ls"},
		Short:   "List VPN peers",
	}
	peers.RunE = withClient(func(cmd *cobra.Command, c *vpn.Client, args []string) error {
		list, err := c.Peers()
		if err != nil {
			return err
		}
		if len(list) == 0 {
			cmd.Println("No peers. Add one with: jekyo vpn add-peer <name>")
			return nil
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tADDRESS\tENABLED\tLAST HANDSHAKE")
		for _, p := range list {
			hs := "never"
			if p.LatestHandshakeAt != nil {
				hs = ago(*p.LatestHandshakeAt)
			}
			fmt.Fprintf(w, "%s\t%s\t%v\t%s\n", p.Name, p.Address, p.Enabled, hs)
		}
		return w.Flush()
	})

	var outFile string
	addPeer := &cobra.Command{
		Use:   "add-peer <name>",
		Short: "Create a peer and save its WireGuard config",
		Args:  cobra.ExactArgs(1),
	}
	addPeer.RunE = withClient(func(cmd *cobra.Command, c *vpn.Client, args []string) error {
		name := args[0]
		if err := c.AddPeer(name); err != nil {
			return err
		}
		list, err := c.Peers()
		if err != nil {
			return err
		}
		p, err := vpn.FindPeer(list, name)
		if err != nil {
			return err
		}
		cfg, err := c.Config(p.ID)
		if err != nil {
			return err
		}
		out := outFile
		if out == "" {
			out = name + ".conf"
		}
		if err := os.WriteFile(out, cfg, 0o600); err != nil {
			return err
		}
		cmd.Printf("Peer %s created (%s). Config written to %s\n", name, p.Address, out)
		cmd.Println("Import it into any WireGuard client to reach cluster services directly.")
		return nil
	})
	addPeer.Flags().StringVarP(&outFile, "output", "o", "", "config file path (default <name>.conf)")

	rmPeer := &cobra.Command{
		Use:   "rm-peer <name>",
		Short: "Delete a peer",
		Args:  cobra.ExactArgs(1),
	}
	rmPeer.RunE = withClient(func(cmd *cobra.Command, c *vpn.Client, args []string) error {
		name := args[0]
		list, err := c.Peers()
		if err != nil {
			return err
		}
		p, err := vpn.FindPeer(list, name)
		if err != nil {
			return err
		}
		if err := c.RemovePeer(p.ID); err != nil {
			return err
		}
		cmd.Println("Removed peer", name)
		return nil
	})

	var cfgOut string
	config := &cobra.Command{
		Use:   "config <peer>",
		Short: "Download a peer's WireGuard config",
		Args:  cobra.ExactArgs(1),
	}
	config.RunE = withClient(func(cmd *cobra.Command, c *vpn.Client, args []string) error {
		name := args[0]
		list, err := c.Peers()
		if err != nil {
			return err
		}
		p, err := vpn.FindPeer(list, name)
		if err != nil {
			return err
		}
		cfg, err := c.Config(p.ID)
		if err != nil {
			return err
		}
		if cfgOut == "" {
			cmd.OutOrStdout().Write(cfg)
			return nil
		}
		if err := os.WriteFile(cfgOut, cfg, 0o600); err != nil {
			return err
		}
		cmd.Println("Config written to", cfgOut)
		return nil
	})
	config.Flags().StringVarP(&cfgOut, "output", "o", "", "write to a file instead of stdout")

	cmd.AddCommand(peers, addPeer, rmPeer, config)
	return cmd
}
