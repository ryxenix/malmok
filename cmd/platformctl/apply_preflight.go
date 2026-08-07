package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"platform.ryxen.dev/platformctl/api/v1alpha1"
	"platform.ryxen.dev/platformctl/internal/event"
	"platform.ryxen.dev/platformctl/internal/preflight"
	"platform.ryxen.dev/platformctl/internal/tui"
)

// runRealPreflight is what `apply -f cluster.yaml` does today.
//
// The checks are real and the phases that build a cluster are not, so the
// command runs the checks, prints them, and then says exactly what is missing.
// That is more use to an operator than a refusal, and it is honest in a way
// that a command pretending to install is not.
func runRealPreflight(cmd *cobra.Command, specFile string, allowLiteral, insecureHost bool, timeout time.Duration, verbose bool) error {
	o := preflightOptions{
		specFile:     specFile,
		allowLiteral: allowLiteral,
		insecureHost: insecureHost,
		timeout:      timeout,
		verbose:      verbose,
	}
	doc, _, err := o.load()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()

	out := cmd.OutOrStdout()
	rep := o.session(doc).Run(ctx)
	printReport(out, rep, verbose)

	if blocking := rep.Blocking(); len(blocking) > 0 {
		return fmt.Errorf("%d checks block the install; nothing has been changed", len(blocking))
	}
	return errNoInstaller
}

// errNoInstaller is the same sentence wherever the tool runs out of road, so an
// operator who meets it twice is not left wondering whether the two are
// different problems.
var errNoInstaller = errors.New(
	"the checks pass, and installing is not implemented yet: the phase catalogue in " +
		"docs/11-execute.md needs the L0/L1 Ansible roles and the L2 Helm and ArgoCD steps. " +
		"Use --demo to exercise the engine, or `platformctl preflight` to re-run the checks alone")

// runWizardPreflight runs the checks against the nodes the operator typed in.
//
// The credentials are taken from the wizard rather than from the document
// because cluster.yaml is an audit artifact handed to customers, and a
// plaintext password in one is the liability the schema exists to prevent.
func runWizardPreflight(ctx context.Context, cfg tui.Config, w *event.Writer, insecureHost bool) error {
	spec := cfg.ToSpec()
	if len(spec.Topology.Servers) == 0 && len(spec.Topology.Agents) == 0 {
		return nil
	}

	emitter := preflight.Emitter{Writer: w}
	session := &preflight.Session{
		Spec:             spec,
		SSHPassword:      cfg.SSHPassword,
		SkipHostKeyCheck: insecureHost,
		Emit:             emitter.Emit,
		Resolve: func(string, v1alpha1.SourceRef, bool) ([]byte, error) {
			// The wizard builds its document in memory; there is nothing on
			// disk for a SourceRef to point at.
			return nil, nil
		},
	}

	rep := session.Run(ctx)
	for host, why := range rep.Unreachable {
		_, _ = w.Emit(event.Event{
			Kind: event.KindProbe, Phase: preflight.PhasePreflight,
			Step: "connect", Node: host, Status: event.StatusBlocked,
			Level: event.LevelError, Detail: "could not be reached: " + why,
		})
	}

	if n := len(rep.Blocking()) + len(rep.Unreachable); n > 0 {
		return fmt.Errorf("%d checks block the install", n)
	}
	return nil
}
