package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"platform.ryxen.dev/platformctl/internal/attach"
	"platform.ryxen.dev/platformctl/internal/engine"
	"platform.ryxen.dev/platformctl/internal/event"
	"platform.ryxen.dev/platformctl/internal/preflight"
	"platform.ryxen.dev/platformctl/internal/rke2"
	"platform.ryxen.dev/platformctl/internal/spec"
	"platform.ryxen.dev/platformctl/internal/state"
	"platform.ryxen.dev/platformctl/internal/upgrade"
)

// upgradeFlags are what `upgrade` was invoked with.
type upgradeFlags struct {
	specFile string
	to       string
	bundle   string
	resume   string

	allowLiteral bool
	insecureHost bool
	approve      bool
	force        bool

	timeout time.Duration
	quiet   bool
	verbose bool
}

func newUpgradeCmd() *cobra.Command {
	var f upgradeFlags

	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Move an existing cluster to a new version",
		Long: `Move a cluster this tool built to a new RKE2 version.

Servers first and one node at a time, because a kubelet must never lead its API
server and a control plane that restarts two members at once loses quorum. Each
node is drained before its kubelet restarts and uncordoned once the cluster
agrees it is back on the new version.

Nothing is touched until the preconditions pass: the control plane moves one
minor version at a time, there is no downgrade, and every node has to be Ready
before the first one goes down.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE:         func(cmd *cobra.Command, _ []string) error { return runUpgrade(cmd, f) },
	}

	cmd.Flags().StringVarP(&f.specFile, "file", "f", "cluster.yaml", "the document naming the cluster")
	cmd.Flags().StringVar(&f.to, "to", "", "the RKE2 version to move to, e.g. v1.35.7+rke2r1")
	cmd.Flags().StringVar(&f.bundle, "bundle", "", "where the run directory goes")
	cmd.Flags().StringVar(&f.resume, "resume", "", "resume the run with this id")
	cmd.Flags().BoolVar(&f.allowLiteral, "allow-literal-secrets", false, "accept literal:// secrets")
	cmd.Flags().BoolVar(&f.insecureHost, "insecure-host-key", false, "accept any SSH host key")
	cmd.Flags().BoolVar(&f.approve, "approve", false, "proceed; every node restarts")
	cmd.Flags().BoolVar(&f.force, "force", false,
		"drain past a PodDisruptionBudget that cannot be satisfied")
	cmd.Flags().DurationVar(&f.timeout, "timeout", 2*time.Hour, "overall deadline")
	cmd.Flags().BoolVarP(&f.quiet, "quiet", "q", false, "no progress output")
	cmd.Flags().BoolVarP(&f.verbose, "verbose", "v", false, "show checks that passed")

	_ = cmd.MarkFlagRequired("to")
	return cmd
}

// runUpgrade is what `platformctl upgrade --to vX` does.
//
// Measure, decide, move. The same shape as a build and for the same reason: the
// decision is a function of what the nodes turned out to be running, not of
// what the document says they should be.
func runUpgrade(cmd *cobra.Command, f upgradeFlags) error {
	o := preflightOptions{
		specFile:     f.specFile,
		allowLiteral: f.allowLiteral,
		insecureHost: f.insecureHost,
		timeout:      f.timeout,
		verbose:      f.verbose,
	}
	doc, _, err := o.load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	out := cmd.OutOrStdout()

	b, err := o.connect(ctx, doc)
	if err != nil {
		return fmt.Errorf("could not reach every node: %w", err)
	}
	defer b.close()

	// --- 1. measure --------------------------------------------------------
	st := upgrade.Read(ctx, doc.Spec, b.runners.ByHost, b.runners.Control)
	printVersions(out, st)

	// --- 2. decide ---------------------------------------------------------
	results := upgrade.Check(st, f.to)
	fmt.Fprintln(out)
	printGroup(out, "upgrade preconditions", results, f.verbose)

	if blocking := upgrade.Blocking(results); len(blocking) > 0 {
		return fmt.Errorf("%d precondition(s) stop the upgrade; nothing has been changed", len(blocking))
	}

	phases, err := upgrade.Phases(doc.Spec, f.to, upgrade.Runners{
		ByHost: b.runners.ByHost, Control: b.runners.Control,
	}, upgrade.Options{
		// The same timeouts `apply` uses, and no artifact path for the same
		// reason it has none: nothing wires one yet, and an airgap source that
		// only the upgrade honoured would be a difference nobody asked for.
		RKE2: rke2.Options{
			InstallTimeout: 20 * time.Minute,
			ReadyTimeout:   15 * time.Minute,
		},
		DrainTimeout: 10 * time.Minute,
		Force:        f.force,
		SingleNode:   len(st.Nodes) < 2,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "\nphases\n%s\n", phaseGrades(phases))
	if !f.approve {
		return fmt.Errorf("\nevery node restarts, one at a time, and its workloads move. " +
			"Re-run with --approve once that is acceptable")
	}

	// --- 3. move -----------------------------------------------------------
	runDir, runState, err := openRun(f.bundle, f.resume)
	if err != nil {
		return err
	}
	// The document is snapshotted with the version it is moving to already in
	// it, so the run directory describes the cluster the run produced rather
	// than the one it started from.
	moved := doc.Spec
	moved.Kubernetes.Version = f.to
	if err := spec.Snapshot(runDir, moved); err != nil {
		return err
	}

	eventPath := filepath.Join(runDir, "events.jsonl")
	events, err := event.OpenFile(eventPath, runState.Run)
	if err != nil {
		return err
	}
	defer events.Close()

	fmt.Fprintf(cmd.ErrOrStderr(), "\nrun %s\n  %s\n\n", runState.Run, runDir)

	// The preconditions belong in the run's own event file: the audit report is
	// built from the file, and a finding that scrolled past is one nobody can
	// produce six months later.
	emitter := preflight.Emitter{Writer: events.Writer}
	for _, r := range results {
		emitter.Emit(r)
	}

	runner := &engine.Runner{
		Events:    events.Writer,
		State:     runState,
		StatePath: state.Path(runDir),
	}

	var sink attach.Sink
	var summary func()
	if f.quiet {
		sink, summary = &attach.Collector{}, func() {}
	} else {
		r := attach.NewTextRenderer(out)
		r.Verbose = f.verbose
		sink, summary = r, func() { fmt.Fprint(out, r.Summary()) }
	}
	defer summary()

	done := followRun(ctx, eventPath, runState.Run, sink)
	runErr := runner.Run(ctx, phases)
	<-done

	if runErr != nil {
		return runErr
	}

	fmt.Fprintf(out, "\n%s is on %s. The run is in %s\n",
		doc.Spec.Metadata.Name, f.to, runDir)
	fmt.Fprintf(out, "Update %s so the next apply agrees: kubernetes.version: %s\n", f.specFile, f.to)
	return nil
}

// printVersions says what is running before saying what is allowed.
func printVersions(w io.Writer, st upgrade.State) {
	fmt.Fprintln(w, "current")
	for _, n := range st.Nodes {
		role := "server"
		if n.Agent {
			role = "agent"
		}
		version := "unknown"
		if n.Version.Known() {
			version = n.Version.String()
		}
		// A node whose binary is not what it is running is a node part-way
		// through an upgrade, and saying so is the difference between a refusal
		// that makes sense and one that looks wrong.
		if n.Installed.Known() && n.Installed.Compare(n.Version) != 0 {
			version += " (" + n.Installed.String() + " installed)"
		}
		ready := "Ready"
		if !n.Ready {
			ready = "NOT Ready"
		}
		fmt.Fprintf(w, "  %-16s %-7s %-40s %s\n", n.Host, role, version, ready)
	}
}
