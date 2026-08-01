package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/jekyo/jekyo/internal/addons"
	"github.com/jekyo/jekyo/internal/compile"
	"github.com/jekyo/jekyo/internal/contexts"
	"github.com/jekyo/jekyo/internal/deploy"
	"github.com/jekyo/jekyo/internal/dsl"
	"github.com/jekyo/jekyo/internal/kube"
)

const deployTimeout = 2 * time.Minute

// loadApp parses the jekyo.yaml at path with --env-file overlays applied.
func loadApp(path string, envFiles []string) (*dsl.App, error) {
	extra := map[string]string{}
	for _, f := range envFiles {
		if err := readEnvFile(f, extra); err != nil {
			return nil, err
		}
	}
	app, err := dsl.ParseFile(path, extra)
	if err != nil {
		return nil, err
	}
	return app, nil
}

func readEnvFile(path string, into map[string]string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("%s: malformed line %q (want KEY=VALUE)", path, line)
		}
		into[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"`)
	}
	return sc.Err()
}

// pullSecrets converts stored registry logins into compiler options.
func pullSecrets(m contexts.Meta) []compile.PullSecret {
	var out []compile.PullSecret
	for host, l := range m.Logins {
		out = append(out, compile.PullSecret{Host: host, Username: l.Username, Password: l.Password})
	}
	return out
}

func newDeployer() (*deploy.Deployer, error) {
	store, err := contexts.Open()
	if err != nil {
		return nil, err
	}
	m, err := store.Resolve(contextFlag)
	if err != nil {
		return nil, err
	}
	client, err := kube.New(store.KubeconfigPath(m.Name))
	if err != nil {
		return nil, err
	}
	return &deploy.Deployer{Client: client}, nil
}

func newUpCmd() *cobra.Command {
	var file string
	var envFiles []string
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Deploy the app described by jekyo.yaml",
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
			if err := ensureBuilt(cmd, app, file, m); err != nil {
				return err
			}
			objs, err := compile.Compile(app, compile.Options{PullSecrets: pullSecrets(m)})
			if err != nil {
				return err
			}
			d, err := newDeployer()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), deployTimeout)
			defer cancel()
			rev, err := d.Apply(ctx, app.Name, objs)
			if err != nil {
				return err
			}
			for _, v := range app.Volumes {
				if v.Backup != nil {
					if err := d.EnsureBackupSecret(ctx, app.Name); err != nil {
						return err
					}
					break
				}
			}
			cmd.Printf("App %s deployed (revision %d, %d services)\n", app.Name, rev, len(app.Services))
			for _, name := range sortedServiceNames(app) {
				svc := app.Services[name]
				if svc.HTTP != nil {
					scheme := "https"
					if svc.HTTP.TLS != nil && !*svc.HTTP.TLS {
						scheme = "http"
					}
					cmd.Printf("  %s: %s://%s\n", name, scheme, svc.HTTP.Domain)
				}
			}
			cmd.Println("Watch progress with: jekyo ps", app.Name)
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "jekyo.yaml", "path to the app definition")
	cmd.Flags().StringArrayVar(&envFiles, "env-file", nil, "env file(s) for ${VAR} interpolation")
	return cmd
}

func newDownCmd() *cobra.Command {
	var file string
	var withVolumes bool
	cmd := &cobra.Command{
		Use:   "down [app]",
		Short: "Remove an app's workloads (volumes survive without --volumes)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = args[0]
			} else {
				app, err := loadApp(file, nil)
				if err != nil {
					return fmt.Errorf("no app name given and no readable jekyo.yaml: %w", err)
				}
				name = app.Name
			}
			if withVolumes && !confirm(cmd, fmt.Sprintf("Delete app %q INCLUDING volumes (unrecoverable)?", name)) {
				return fmt.Errorf("aborted")
			}
			d, err := newDeployer()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), deployTimeout)
			defer cancel()
			volumesKept, err := d.Down(ctx, name, withVolumes)
			if err != nil {
				return err
			}
			switch {
			case withVolumes:
				cmd.Println("App removed, volumes deleted:", name)
			case volumesKept:
				cmd.Println("App removed (volumes kept; 'jekyo down", name, "--volumes' deletes them)")
			default:
				cmd.Println("App removed:", name)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "jekyo.yaml", "path to the app definition")
	cmd.Flags().BoolVar(&withVolumes, "volumes", false, "also delete volumes and the namespace")
	return cmd
}

func newLsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List deployed apps",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := newDeployer()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			apps, err := d.List(ctx)
			if err != nil {
				return err
			}
			if len(apps) == 0 {
				cmd.Println("No apps deployed. Run 'jekyo up' in a directory with a jekyo.yaml.")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "APP\tREV\tSERVICES\tPODS\tDOMAINS\tAGE")
			for _, a := range apps {
				fmt.Fprintf(w, "%s\tv%d\t%s\t%d/%d\t%s\t%s\n",
					a.Name, a.Revision, strings.Join(a.Services, ","),
					a.PodsReady, a.PodsTotal, orDash(strings.Join(a.Domains, ",")), age(time.Now().Add(-a.Age)))
			}
			return w.Flush()
		},
	}
	return cmd
}

func newRenderCmd() *cobra.Command {
	var file string
	var envFiles []string
	cmd := &cobra.Command{
		Use:   "render",
		Short: "Print the Kubernetes YAML jekyo.yaml compiles to",
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := loadApp(file, envFiles)
			if err != nil {
				return err
			}
			var opts compile.Options
			if store, err := contexts.Open(); err == nil {
				if m, err := store.Resolve(contextFlag); err == nil {
					opts.PullSecrets = pullSecrets(m)
				}
			}
			// Render doesn't build; show build: services with the image
			// reference they'll get (tag is the content hash at up-time).
			for name, svc := range app.Services {
				if svc.Build != nil {
					svc.Image = addons.RegistryHost + "/" + app.Name + "/" + name + ":<content-hash>"
					svc.Build = nil
					app.Services[name] = svc
				}
			}
			objs, err := compile.Compile(app, opts)
			if err != nil {
				return err
			}
			out, err := deploy.RenderYAML(objs)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), out)
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "jekyo.yaml", "path to the app definition")
	cmd.Flags().StringArrayVar(&envFiles, "env-file", nil, "env file(s) for ${VAR} interpolation")
	return cmd
}

func sortedServiceNames(app *dsl.App) []string {
	var names []string
	for n := range app.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
