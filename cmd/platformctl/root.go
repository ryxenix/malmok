package main

import (
	"github.com/spf13/cobra"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "platformctl",
		Short: "Build and operate RKE2 platforms",
		Long: `platformctl builds and operates RKE2 platforms across homelab,
production and customer sites, including DMZ and air-gapped installations.

The engine runs from cluster.yaml alone. The TUI drives an install and renders
its progress, but owns no installation logic — see docs/00-architecture.md
ADR-002.`,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(newApplyCmd())
	root.AddCommand(newAttachCmd())
	return root
}
