package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

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
			ctxName, args := splitKubectlArgs(args)
			if ctxName == "" {
				ctxName = contextFlag
			}
			store, err := contexts.Open()
			if err != nil {
				return err
			}
			m, err := store.Resolve(ctxName)
			if err != nil {
				return err
			}
			kubeconfig := store.KubeconfigPath(m.Name)
			if _, err := os.Stat(kubeconfig); err != nil {
				return fmt.Errorf("context %q has no kubeconfig; re-run 'jekyo server install' to repair", m.Name)
			}

			path, err := exec.LookPath("kubectl")
			if err != nil {
				return fmt.Errorf("kubectl not found in PATH; install it or use the jekyo commands directly")
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

// splitKubectlArgs takes jekyo's global flags (--context, --ssh-key) out
// of the arguments kubectl would otherwise receive: flag parsing is off
// for this command, so `jekyo --context gpu kubectl get pods` arrives as
// [--context gpu get pods] (issue #27). Everything after the first "--"
// belongs to kubectl untouched.
func splitKubectlArgs(args []string) (context string, rest []string) {
	rest = []string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return context, append(rest, args[i+1:]...)
		case a == "--context" || a == "--ssh-key":
			if i+1 < len(args) {
				if a == "--context" {
					context = args[i+1]
				}
				i++
			}
		case strings.HasPrefix(a, "--context="):
			context = strings.TrimPrefix(a, "--context=")
		case strings.HasPrefix(a, "--ssh-key="):
		default:
			rest = append(rest, a)
		}
	}
	return context, rest
}
