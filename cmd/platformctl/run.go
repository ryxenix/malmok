package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"platform.ryxen.dev/platformctl/internal/attach"
	"platform.ryxen.dev/platformctl/internal/catalogue"
	"platform.ryxen.dev/platformctl/internal/dataplane"
	"platform.ryxen.dev/platformctl/internal/engine"
	"platform.ryxen.dev/platformctl/internal/event"
	"platform.ryxen.dev/platformctl/internal/gateway"
	"platform.ryxen.dev/platformctl/internal/plan"
	"platform.ryxen.dev/platformctl/internal/preflight"
	"platform.ryxen.dev/platformctl/internal/report"
	"platform.ryxen.dev/platformctl/internal/rke2"
	"platform.ryxen.dev/platformctl/internal/spec"
	"platform.ryxen.dev/platformctl/internal/state"
)

// buildFlags are what `apply -f` was invoked with.
type buildFlags struct {
	specFile string
	bundle   string
	resume   string
	recheck  bool

	allowLiteral bool
	insecureHost bool
	approve      bool

	timeout time.Duration
	quiet   bool
	verbose bool

	screen *tuiFlags
}

// runBuild is what `platformctl apply -f cluster.yaml` does.
//
// The order is the order of docs/11-execute.md §2 and it is not negotiable:
// measure, decide, build, verify. Preflight comes first because a plan is a
// function of what the nodes turned out to be; verify comes last because it
// asks what a client receives, which cannot be known until there is something
// to receive it from.
func runBuild(cmd *cobra.Command, f buildFlags) error {
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

	// Every connection up front. A build that discovers on its third phase that
	// it cannot reach a node has already changed the first two.
	b, err := o.connect(ctx, doc)
	if err != nil {
		return fmt.Errorf("could not reach every node: %w", err)
	}
	defer b.close()

	// --- 1. preflight ------------------------------------------------------
	rep := preflightRun(ctx, o, doc, b)
	printReport(out, rep, f.verbose)

	if blocking := rep.Blocking(); len(blocking) > 0 {
		return fmt.Errorf("%d checks block the install; nothing has been changed", len(blocking))
	}
	if len(rep.Nodes) == 0 {
		return errors.New("no node was reached, so there is nothing to build")
	}

	// --- 2. plan -----------------------------------------------------------
	p, err := plan.Generate(doc.Spec, rep.Nodes, plan.Options{})
	if err != nil {
		return err
	}
	printPlan(out, p)
	if p.Downgraded() && !f.approve {
		return errors.New("the plan downgrades what the document asked for; " +
			"re-run with --approve once the downgrades above are acceptable")
	}

	// --- 3. build ----------------------------------------------------------
	phases, err := catalogue.Build(doc.Spec, b.runners, b.material, catalogue.Options{
		RKE2:      rke2.Options{InstallTimeout: 20 * time.Minute, ReadyTimeout: 15 * time.Minute},
		Dataplane: dataplane.Options{Timeout: 15 * time.Minute},
		Gateway:   gateway.Options{Timeout: 10 * time.Minute},
	})
	if err != nil {
		return err
	}

	// A phase that reconfigures a running cluster is not something to discover
	// during a maintenance window, so the grades are shown before anything runs.
	fmt.Fprintf(out, "\nphases\n%s\n", phaseGrades(phases))
	if hasDisruptive(phases) && !f.approve {
		return errors.New("\nsomething here reconfigures a running cluster rather than adding to it. " +
			"Re-run with --approve once that is acceptable")
	}

	runDir, st, err := openRun(f.bundle, f.resume)
	if err != nil {
		return err
	}
	// The run keeps the document it was built from, so it can be re-run,
	// resumed or handed over on its own (§1.1).
	if err := spec.Snapshot(runDir, doc.Spec); err != nil {
		return err
	}
	// The plan is kept beside the document because the audit report's downgrade
	// section is built from it, and six months later nobody remembers which
	// settings were chosen and which the tool decided.
	if err := report.SavePlan(runDir, p); err != nil {
		return err
	}

	eventPath := filepath.Join(runDir, "events.jsonl")
	events, err := event.OpenFile(eventPath, st.Run)
	if err != nil {
		return err
	}
	defer events.Close()

	fmt.Fprintf(cmd.ErrOrStderr(), "\nrun %s\n  %s\n\n", st.Run, runDir)

	// The preflight findings belong in the run's own event file, not only on
	// the terminal: the audit report is built from the file, and a finding that
	// scrolled past is a finding nobody can produce six months later.
	emitter := preflight.Emitter{Writer: events.Writer}
	for _, r := range rep.Document {
		emitter.Emit(r)
	}
	for _, n := range rep.Nodes {
		for _, r := range preflight.SortedProbes(n) {
			r.Node = n.Host
			emitter.Emit(r)
		}
	}

	runner := &engine.Runner{
		Events:    events.Writer,
		State:     st,
		StatePath: state.Path(runDir),
		Resume:    state.Options{Recheck: f.recheck},
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

	done := followRun(ctx, eventPath, st.Run, sink)
	runErr := runner.Run(ctx, phases)
	<-done

	if runErr != nil {
		return runErr
	}

	// --- 4. verify ---------------------------------------------------------
	//
	// PF-9xx validated the files before anything was installed. This asks what
	// a client actually receives, which is the check ADR-010 exists for.
	wire := verifyGateways(ctx, doc, b.material)
	for _, r := range wire {
		emitter.Emit(r)
	}
	if len(wire) > 0 {
		fmt.Fprintln(out)
		printGroup(out, "wire verification", wire, f.verbose)
	}
	for _, r := range wire {
		if r.Failed() && r.Severity == "block" {
			return fmt.Errorf("the cluster is built and %s fails: %s", r.ID, r.Detail)
		}
	}

	// --- 5. report ---------------------------------------------------------
	//
	// Built from the run directory rather than from the cluster: what a customer
	// receives has to be the record of what happened, not a view of what is
	// true at the moment somebody asks.
	artifacts, err := writeArtifacts(runDir)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "\n%s is built. The run is in %s\n", doc.Spec.Metadata.Name, runDir)
	for _, a := range artifacts {
		fmt.Fprintf(out, "  %s\n", a)
	}
	return nil
}

// writeArtifacts produces the audit report and the DNS record sheet.
func writeArtifacts(runDir string) ([]string, error) {
	run, err := report.Load(runDir)
	if err != nil {
		return nil, err
	}
	return report.Write(run)
}

// preflightRun measures every node using the connections the build already
// holds, rather than opening its own.
func preflightRun(ctx context.Context, o preflightOptions, doc *spec.Document, b *nodeSession) preflight.Report {
	s := o.session(doc)
	s.Dial = b.dialer()
	return s.Run(ctx)
}

func hasDisruptive(phases []engine.Phase) bool {
	for _, p := range phases {
		if p.Grade == engine.GradeDisruptive {
			return true
		}
	}
	return false
}
