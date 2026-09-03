package main

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/ryxenix/malmok/internal/tools"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

// versionLine says what the binary is and what it carries.
//
// An airgap build is the same program with helm and k9s embedded, and the two
// are told apart by their filename alone -- which survives exactly as long as
// nobody renames the file. Asking the binary is the way to be sure at a
// customer site, where the wrong one means the tools are simply absent.
func versionLine() string {
	carried := tools.Carried()
	if len(carried) == 0 {
		return version
	}
	return version + " (airgap payload: " + strings.Join(carried, ", ") + ")"
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "malmok",
		Short: "Build and operate RKE2 platforms",
		Long: `malmok builds and operates RKE2 platforms across homelab,
production and customer sites, including DMZ and air-gapped installations.

The engine runs from cluster.yaml alone. The TUI drives an install and renders
its progress, but owns no installation logic — see docs/00-architecture.md
ADR-002.`,
		Version:       versionLine(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(newApplyCmd())
	root.AddCommand(newPreflightCmd())
	root.AddCommand(newPlanCmd())
	root.AddCommand(newAttachCmd())
	root.AddCommand(newUpgradeCmd())
	root.AddCommand(newReportCmd())
	return root
}
