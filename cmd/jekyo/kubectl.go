package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/jekyo/jekyo/internal/contexts"
)

// newKubectlCmd passes everything after `jekyo kubectl` to the local kubectl
// binary with KUBECONFIG pointed at the resolved context. Escape hatch: JEKYO
// never wraps or reinterprets the arguments.
func newKubectlCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "kubectl [-- args...]",
		Short:              "Run kubectl against the current context",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && args[0] == "--" {
				args = args[1:]
			}
			store, err := contexts.Open()
			if err != nil {
				return err
			}
			m, err := store.Resolve(contextFlag)
			if err != nil {
				return err
			}
			kubeconfig := store.KubeconfigPath(m.Name)
			if _, err := os.Stat(kubeconfig); err != nil {
				return fmt.Errorf("context %q has no kubeconfig — re-run 'jekyo server install' to repair", m.Name)
			}

			path, err := exec.LookPath("kubectl")
			if err != nil {
				return fmt.Errorf("kubectl not found in PATH — install it or use the jekyo commands directly")
			}
			c := exec.Command(path, args...)
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			c.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
			if err := c.Run(); err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					os.Exit(exitErr.ExitCode())
				}
				return err
			}
			return nil
		},
	}
}
