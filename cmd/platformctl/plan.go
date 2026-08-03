package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"platform.ryxen.dev/platformctl/api/v1alpha1"
	"platform.ryxen.dev/platformctl/internal/plan"
	"platform.ryxen.dev/platformctl/internal/spec"
)

func newPlanCmd() *cobra.Command {
	var (
		specFile     string
		allowLiteral bool
		approve      bool
		validateOnly bool
	)

	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Read cluster.yaml, apply the profile, and show what would be installed",
		Long: `plan reads cluster.yaml, fills in the profile baseline, validates it, and
prints the configuration that would actually be installed -- including anything
that had to be downgraded and why.

Nothing is changed. Every value the profile supplied is listed, so the audit
trail can distinguish what the operator chose from what the tool did.`,
		Example: `  platformctl plan -f cluster.yaml
  platformctl plan -f cluster.yaml --validate-only`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if specFile == "" {
				return errors.New("pass -f cluster.yaml")
			}
			doc, err := spec.Load(specFile)
			if err != nil {
				return err
			}

			applied, err := doc.ApplyProfile()
			if err != nil {
				return err
			}
			if err := doc.Validate(allowLiteral); err != nil {
				return fmt.Errorf("%s is not valid:\n%w", specFile, err)
			}

			out := cmd.OutOrStdout()
			printResolved(out, doc, applied)
			if validateOnly {
				fmt.Fprintf(out, "\n%s is valid.\n", specFile)
				return nil
			}

			// Preflight has not been implemented yet, so there are no
			// capabilities to plan against. Saying so beats printing a plan
			// derived from assumptions about nodes nobody has looked at.
			return errors.New(
				"preflight is not implemented yet, so there is nothing to plan against: " +
					"the probes need an SSH executor. Use --validate-only to check the document")
		},
	}

	fl := cmd.Flags()
	fl.StringVarP(&specFile, "file", "f", "", "cluster.yaml to read")
	fl.BoolVar(&allowLiteral, "allow-literal-secrets", false,
		"permit literal:// on secret fields (cluster.yaml is handed to customers)")
	fl.BoolVar(&approve, "approve", false, "accept any downgrade the plan requires")
	fl.BoolVar(&validateOnly, "validate-only", false, "check the document and stop")

	return cmd
}

// printResolved shows the document after the profile has been applied.
//
// The applied list is the point: six months later nobody remembers which
// settings were chosen and which were inherited, and the audit report has to be
// able to say.
func printResolved(w io.Writer, doc *spec.Document, applied []string) {
	s := &doc.Spec

	fmt.Fprintf(w, "cluster    %s\n", s.Metadata.Name)
	profile := string(s.Metadata.Profile)
	if profile == "" || profile == string(v1alpha1.ProfileCustom) {
		profile = "custom (Tier-3, unvalidated)"
	}
	fmt.Fprintf(w, "profile    %s\n", profile)
	fmt.Fprintf(w, "nodes      %d server, %d agent\n\n",
		len(s.Topology.Servers), len(s.Topology.Agents))

	// The key is the path ApplyProfile records, so the two cannot drift.
	rows := [][2]string{
		{"os.family", string(s.OS.Family)},
		{"network.mode", string(s.Network.Mode)},
		{"network.routing", string(s.Network.Routing)},
		{"kubernetes.version", s.Kubernetes.Version},
		{"kubernetes.dataplane.preset", string(s.Kubernetes.Dataplane.Preset)},
		{"kubernetes.dataplane.fallback", string(s.Kubernetes.Dataplane.Fallback)},
		{"kubernetes.dataplane.downgradePolicy", string(s.Kubernetes.Dataplane.DowngradePolicy)},
		{"pki.mode", string(s.PKI.Mode)},
		{"storage.driver", string(s.Storage.Driver)},
		{"registry.mode", string(s.Registry.Mode)},
		{"platform.gitops.source", string(s.Platform.GitOps.Source)},
	}
	fromProfile := map[string]bool{}
	for _, a := range applied {
		fromProfile[a] = true
	}
	for _, r := range rows {
		note := ""
		if fromProfile[r[0]] {
			note = "   (from profile)"
		}
		fmt.Fprintf(w, "  %-38s %s%s\n", r[0], r[1], note)
	}

	if len(applied) > 0 {
		fmt.Fprintf(w, "\n%d value(s) supplied by the profile\n", len(applied))
	}
}

// printPlan renders a generated plan. Kept here so `plan` and the wizard show
// the same thing.
func printPlan(w io.Writer, p *plan.Plan) {
	fmt.Fprintf(w, "\ndataplane  %s", p.Actual.Dataplane)
	if p.Actual.Dataplane != p.Requested.Dataplane {
		fmt.Fprintf(w, "   (requested %s)", p.Requested.Dataplane)
	}
	fmt.Fprintf(w, "\nstorage    %s", p.Actual.Storage)
	if p.Actual.Storage != p.Requested.Storage {
		fmt.Fprintf(w, "   (requested %s)", p.Requested.Storage)
	}
	fmt.Fprintln(w)

	for _, d := range p.Downgrades {
		fmt.Fprintf(w, "\n%s  %s -> %s\n", d.Code, d.From, d.To)
		fmt.Fprintf(w, "  triggered by  %s\n", strings.Join(d.TriggeredBy, ", "))
		fmt.Fprintf(w, "  nodes         %s\n", strings.Join(d.Nodes, ", "))
		fmt.Fprintf(w, "  %s\n", d.Detail)
	}
	for _, e := range p.Excluded {
		fmt.Fprintf(w, "\nexcluded  %s  (%s)\n  %s\n",
			e.Node, strings.Join(e.TriggeredBy, ", "), e.Detail)
	}
	for _, f := range p.Warnings {
		fmt.Fprintf(w, "\n%s  %s\n", f.Code, f.Detail)
	}
}
