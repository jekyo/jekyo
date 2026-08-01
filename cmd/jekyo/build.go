package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/jekyo/jekyo/internal/addons"
	"github.com/jekyo/jekyo/internal/build"
	"github.com/jekyo/jekyo/internal/contexts"
	"github.com/jekyo/jekyo/internal/dsl"
	"github.com/jekyo/jekyo/internal/registry"
)

// buildEnv wires the build package to a cluster: the registry API is
// reached by dialing the registry ClusterIP through the SSH connection, and
// images are delivered by streaming the tar into the server's containerd,
// then pushed into the registry from the server side.
func buildEnv(m contexts.Meta) (build.Env, func(), error) {
	if m.Registry == nil {
		return build.Env{}, nil, fmt.Errorf("context %q has no registry — re-run 'jekyo server install'", m.Name)
	}
	ssh, err := dial(m.SSH)
	if err != nil {
		return build.Env{}, nil, err
	}
	registryAddr := addons.RegistryClusterIP + ":5000"
	reg := registry.New("http://"+registryAddr, m.Registry.Username, m.Registry.Password)
	reg.HTTP = &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return ssh.DialRemote(registryAddr)
			},
		},
	}

	deliver := func(ref string, tar io.Reader, size int64) error {
		// Import into the k8s.io namespace so kubelet sees it immediately;
		// then push into the registry so it survives image GC and future
		// nodes can pull it.
		if _, err := ssh.RunWithStdin("k3s ctr -n k8s.io images import -", tar); err != nil {
			return err
		}
		pushRef := registryAddr + strings.TrimPrefix(ref, addons.RegistryHost)
		cmds := fmt.Sprintf(
			"k3s ctr -n k8s.io images tag --force %q %q && k3s ctr -n k8s.io images push --plain-http --user %q %q && k3s ctr -n k8s.io images rm %q",
			ref, pushRef, m.Registry.Username+":"+m.Registry.Password, pushRef, pushRef)
		_, err := ssh.Run(cmds)
		return err
	}

	env := build.Env{
		Registry: reg,
		Deliver:  deliver,
		Platform: build.PlatformFor(m.Arch),
	}
	return env, func() { ssh.Close() }, nil
}

// ensureBuilt builds/delivers all build: services and rewrites them to
// image: references. No-op for apps without builds.
func ensureBuilt(cmd *cobra.Command, app *dsl.App, file string, m contexts.Meta) error {
	hasBuilds := false
	for _, svc := range app.Services {
		if svc.Build != nil {
			hasBuilds = true
		}
	}
	if !hasBuilds {
		return nil
	}
	env, cleanup, err := buildEnv(m)
	if err != nil {
		return err
	}
	defer cleanup()
	_, err = build.EnsureAll(app, filepath.Dir(file), env, cmd.OutOrStdout())
	return err
}

func newBuildCmd() *cobra.Command {
	var file string
	var envFiles []string
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Build and push images for build: services (without deploying)",
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := loadApp(file, envFiles)
			if err != nil {
				return err
			}
			store, err := contexts.Open()
			if err != nil {
				return err
			}
			m, err := store.Resolve(contextFlag)
			if err != nil {
				return err
			}
			env, cleanup, err := buildEnv(m)
			if err != nil {
				return err
			}
			defer cleanup()
			results, err := build.EnsureAll(app, filepath.Dir(file), env, cmd.OutOrStdout())
			if err != nil {
				return err
			}
			if len(results) == 0 {
				cmd.Println("No build: services in", file)
				return nil
			}
			for _, r := range results {
				cmd.Printf("  %s -> %s\n", r.Service, r.Image)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "jekyo.yaml", "path to the app definition")
	cmd.Flags().StringArrayVar(&envFiles, "env-file", nil, "env file(s) for ${VAR} interpolation")
	return cmd
}

func newImagesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "images",
		Short: "List images in the cluster registry",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := contexts.Open()
			if err != nil {
				return err
			}
			m, err := store.Resolve(contextFlag)
			if err != nil {
				return err
			}
			env, cleanup, err := buildEnv(m)
			if err != nil {
				return err
			}
			defer cleanup()
			repos, err := env.Registry.Repositories()
			if err != nil {
				return err
			}
			if len(repos) == 0 {
				cmd.Println("Registry is empty. 'jekyo up' with a build: service pushes here.")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "REPOSITORY\tTAGS")
			for _, repo := range repos {
				tags, err := env.Registry.Tags(repo)
				if err != nil {
					return err
				}
				fmt.Fprintf(w, "%s/%s\t%s\n", addons.RegistryHost, repo, joinMax(tags, 5))
			}
			return w.Flush()
		},
	}
	return cmd
}

func joinMax(items []string, n int) string {
	if len(items) > n {
		return fmt.Sprintf("%s, ... (%d total)", strings.Join(items[:n], ", "), len(items))
	}
	return strings.Join(items, ", ")
}
