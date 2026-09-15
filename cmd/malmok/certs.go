package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ryxenix/malmok/internal/event"
	"github.com/ryxenix/malmok/internal/expiry"
	"github.com/ryxenix/malmok/internal/preflight"
	"github.com/ryxenix/malmok/internal/spec"
)

// Exit statuses `certs` can end with.
//
// Separate numbers because a script that runs this from cron has to tell three
// things apart, and the usual two cannot: everything was measured and is fine,
// something is expiring, and the scan did not see everything. Collapsing the
// last into the first is how a cron job reports health for a node it never
// reached.
const (
	exitExpiring    = 2
	exitNotMeasured = 3
)

func newCertsCmd() *cobra.Command {
	var (
		o      preflightOptions
		bundle string
		output string
	)

	cmd := &cobra.Command{
		Use:   "certs",
		Short: "Measure when the cluster's certificates expire",
		Long: `certs reads the certificates RKE2 wrote on each server and reports when
they stop working.

Nothing is changed. Only certificate files are read: the private keys sitting
in the same directory are never opened.

This is a measurement taken now, not monitoring. It answers "when do these
end", which a handover has to state and which nothing else in this tool
measures -- preflight gates the certificates an operator supplies, and the wire
checks read what a listener serves.

Exit status is 0 when everything was measured and nothing has crossed an alert
step, 2 when something is expiring or expired, and 3 when something could not
be measured -- which is not the same as nothing being wrong.`,
		Example: `  malmok certs -f cluster.yaml
  malmok certs -f cluster.yaml -o json
  malmok certs -f cluster.yaml -v`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCerts(cmd, &o, bundle, output)
		},
	}

	o.register(cmd)
	fl := cmd.Flags()
	fl.StringVar(&bundle, "bundle", "./out", "bundle path the run is recorded under")
	fl.StringVarP(&output, "output", "o", "text", "output format: text | json")
	return cmd
}

// scan is one node's answer plus what it means.
type scan struct {
	Node     string           `json:"node"`
	Findings []expiry.Finding `json:"findings"`
	Problems []expiry.Problem `json:"problems"`
}

func runCerts(cmd *cobra.Command, o *preflightOptions, bundle, output string) error {
	if output != "text" && output != "json" {
		return fmt.Errorf("unknown output format %q; use text or json", output)
	}

	doc, _, err := o.load()
	if err != nil {
		return err
	}
	if len(doc.Spec.Topology.Servers) == 0 {
		return fmt.Errorf("the document names no server, and only servers hold the certificates this reads")
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()

	b, err := o.connect(ctx, doc)
	if err != nil {
		return fmt.Errorf("could not reach every node: %w", err)
	}
	defer b.close()

	// The clock is read once. Two certificates measured a second apart must
	// not report days remaining against different moments, or a report can
	// contradict itself by one day for no reason a reader can see.
	now := time.Now()

	var scans []scan
	for _, n := range doc.Spec.Topology.Servers {
		runner, ok := b.runners.ByHost[n.Host]
		if !ok {
			return fmt.Errorf("no connection was opened to %s", n.Host)
		}
		inv := expiry.Scan(ctx, runner)

		// Empty rather than absent. A nil slice marshals to null, and a
		// consumer iterating problems then fails on the one document it most
		// needs to read: the scan where nothing went wrong.
		findings := expiry.Assess(inv, now)
		if findings == nil {
			findings = []expiry.Finding{}
		}
		problems := inv.Problems
		if problems == nil {
			problems = []expiry.Problem{}
		}
		scans = append(scans, scan{Node: n.Host, Findings: findings, Problems: problems})
	}

	if err := recordCerts(cmd, bundle, doc, scans); err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if output == "json" {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(scans); err != nil {
			return err
		}
	} else {
		printCerts(out, scans, o.verbose)
	}

	// The text path has already printed the summary; letting the exit error
	// carry it too prints it twice.
	return certsExit(scans, output == "text")
}

// recordCerts writes the scan into a run directory.
//
// The audit report is built from events.jsonl and reads nothing from a
// cluster, deliberately: a report generated from live state says something
// different every time it runs, and what a customer receives has to be the
// record of what was measured. So the measurement becomes events, and the
// report picks it up without either side learning about the other.
func recordCerts(cmd *cobra.Command, bundle string, doc *spec.Document, scans []scan) error {
	runDir, st, err := openRun(bundle, "")
	if err != nil {
		return err
	}
	if err := spec.Snapshot(runDir, doc.Spec); err != nil {
		return err
	}

	events, err := event.OpenFile(filepath.Join(runDir, "events.jsonl"), st.Run)
	if err != nil {
		return err
	}
	defer events.Close()

	emitter := preflight.Emitter{Writer: events.Writer, Phase: "certs"}
	// A scan whose findings did not reach the file is not a scan with fewer
	// findings, which is the same distinction the scan itself draws between a
	// certificate that is fine and one nobody could read.
	defer func() {
		if n, err := emitter.Lost(); n > 0 {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"\n%d item(s) never reached the run's event file, so its audit report is incomplete: %v\n",
				n, err)
		}
	}()
	for _, s := range scans {
		for _, f := range s.Findings {
			// No severity is set. MC codes are registered without one on
			// purpose -- the verdict is graded at runtime against thresholds
			// agreed per contract -- and an empty severity maps a failing
			// result onto "worth reading" rather than onto "blocking".
			status := preflight.StatusPass
			if f.Alarming() {
				status = preflight.StatusFail
			}
			emitter.Emit(preflight.ProbeResult{
				ID: f.Code, Status: status, Detail: f.Line(), Node: f.Node,
				// The sentence is for a person; this is for the next tool.
				// handoff.json exists so an inventory does not have to parse
				// prose, and a date it had to recover with a regular
				// expression would be exactly that.
				Evidence: certEvidence(f),
			})
		}
		for _, p := range s.Problems {
			status := preflight.StatusFail
			if !p.Applicable() {
				status = preflight.StatusSkip
			}
			emitter.Emit(preflight.ProbeResult{
				ID: p.Item(), Status: status, Code: "NOT_MEASURED",
				Detail: p.Line(), Node: nodeOf(p, s.Node),
			})
		}
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "run %s\n  %s\n\n", st.Run, runDir)
	return nil
}

// certEvidence carries the measurement itself alongside the sentence.
//
// Evidence rather than detail: the schema holds detail to printable ASCII on
// one line, and a certificate subject is written by whoever issued it. A
// private CA with a Korean organisation name in its DN is ordinary, and the
// audit trail should keep what it actually said.
func certEvidence(f expiry.Finding) string {
	b, err := json.Marshal(struct {
		Subject  string    `json:"subject"`
		NotAfter time.Time `json:"notAfter"`
		Days     int       `json:"days"`
		Series   string    `json:"series"`
		Path     string    `json:"path"`
	}{f.Subject, f.NotAfter, f.Days, string(f.Series), f.Path})
	if err != nil {
		// Nothing here can fail to marshal, and an error would be a
		// programming fault rather than a measurement. Losing the structured
		// copy must not cost the finding.
		return ""
	}
	return string(b)
}

// nodeOf keeps a problem attributed to a machine even when the problem is
// about the node itself rather than about one of its files.
func nodeOf(p expiry.Problem, fallback string) string {
	if p.Node != "" {
		return p.Node
	}
	return fallback
}

// printCerts writes the table an operator reads.
func printCerts(w io.Writer, scans []scan, verbose bool) {
	shown := 0
	for _, s := range scans {
		var rows []expiry.Finding
		for _, f := range s.Findings {
			if verbose || f.Alarming() {
				rows = append(rows, f)
			}
		}
		if len(rows) == 0 && len(s.Problems) == 0 {
			continue
		}

		fmt.Fprintf(w, "\n%s\n%s\n", s.Node, dashes(len(s.Node)))
		for _, f := range rows {
			mark := "OK"
			if f.Alarming() {
				mark = "WARN"
			}
			fmt.Fprintf(w, "%-4s %-7s %s  %s\n",
				mark, f.Code, f.NotAfter.UTC().Format("2006-01-02"), f.Line())
			shown++
		}
		for _, p := range s.Problems {
			mark := "FAIL"
			if !p.Applicable() {
				mark = "SKIP"
			}
			fmt.Fprintf(w, "%-4s %-7s %-10s  %s\n", mark, p.Item(), "-", p.Line())
			shown++
		}
	}

	if shown == 0 {
		fmt.Fprintln(w, "\nEvery certificate was measured and none is near expiry. Pass -v to see them all.")
	}
	fmt.Fprintln(w, "\n"+certsSummary(scans))
}

// certsSummary is the one line that survives being pasted into a ticket.
func certsSummary(scans []scan) string {
	var measured, alarming, unmeasured int
	for _, s := range scans {
		measured += len(s.Findings)
		for _, f := range s.Findings {
			if f.Alarming() {
				alarming++
			}
		}
		for _, p := range s.Problems {
			if p.Applicable() {
				unmeasured++
			}
		}
	}
	switch {
	case unmeasured > 0 && alarming > 0:
		return fmt.Sprintf("%d item(s) need attention, and %d could not be measured.", alarming, unmeasured)
	case unmeasured > 0:
		return fmt.Sprintf("%d item(s) could not be measured, so this is not a clean bill of health.", unmeasured)
	case alarming > 0:
		return fmt.Sprintf("%d of %d item(s) have crossed an alert step.", alarming, measured)
	}
	return fmt.Sprintf("%d item(s) measured, none near expiry.", measured)
}

// certsExit turns the scan into the process's exit status.
//
// Not measured outranks expiring. A scan that did not see everything cannot
// assert that the things it did see are the only ones in trouble.
func certsExit(scans []scan, summarised bool) error {
	// A non-zero exit still has to say why when nothing else did -- a JSON
	// consumer reads the document, but a person watching a scripted run sees
	// only what reached the terminal.
	msg := ""
	if !summarised {
		msg = certsSummary(scans)
	}

	var alarming, unmeasured int
	for _, s := range scans {
		for _, f := range s.Findings {
			if f.Alarming() {
				alarming++
			}
		}
		for _, p := range s.Problems {
			if p.Applicable() {
				unmeasured++
			}
		}
	}
	switch {
	case unmeasured > 0:
		return &exitError{code: exitNotMeasured, msg: msg}
	case alarming > 0:
		return &exitError{code: exitExpiring, msg: msg}
	}
	return nil
}

func dashes(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = '-'
	}
	return string(b)
}
