package main

import (
	"os"

	"github.com/spf13/cobra"
)

// contextFlag is the global --context override; empty means "current".
var contextFlag string

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "jekyo",
		Short: "JEKYO, a personal PaaS on k3s",
		Long: `JEKYO turns a bare Ubuntu server into a batteries-included k3s cluster
and deploys apps described by a single jekyo.yaml.

Start with:
  jekyo server install user@host --ip 1.2.3.4 --storage /storage
  jekyo up`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	// cobra's Print helpers default to stderr; route normal output to
	// stdout so pipes and agents see it.
	root.SetOut(os.Stdout)

	root.PersistentFlags().StringVar(&contextFlag, "context", "", "context (server) to operate on; defaults to the current one")
	root.PersistentFlags().StringVar(&sshKeyFlag, "ssh-key", "", "SSH private key for server access (default: ssh-agent)")

	root.AddCommand(
		newVersionCmd(),
		newContextCmd(),
		newKubectlCmd(),
		newServerCmd(),
		newUpCmd(),
		newDownCmd(),
		newLsCmd(),
		newRenderCmd(),
		newBuildCmd(),
		newImagesCmd(),
		newPsCmd(),
		newLogsCmd(),
		newExecCmd(),
		newAttachCmd(),
		newTopCmd(),
		newRestartCmd(),
		newStatusCmd(),
		newHistoryCmd(),
		newRollbackCmd(),
		newRegistryCmd(),
		newSkillCmd(),
		newSchemaCmd(),
		newTemplatesCmd(),
		newInitCmd(),
		newVPNCmd(),
		newBackupCmd(),
	)
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the jekyo version",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Println("jekyo", version)
		},
	}
}
