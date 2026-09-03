package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ryxenix/malmok/api/v1alpha1"
	"github.com/ryxenix/malmok/internal/preflight"
	"github.com/ryxenix/malmok/internal/spec"
)

// preflightOptions are shared by `preflight` and `plan`, because plan has to
// run preflight to have anything to plan against.
type preflightOptions struct {
	specFile     string
	allowLiteral bool
	insecureHost bool
	timeout      time.Duration
	verbose      bool
}

func (o *preflightOptions) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVarP(&o.specFile, "file", "f", "", "cluster.yaml to read")
	fl.BoolVar(&o.allowLiteral, "allow-literal-secrets", false,
		"permit literal:// on secret fields (cluster.yaml is handed to customers)")
	fl.BoolVar(&o.insecureHost, "insecure-host-key", false,
		"accept any SSH host key; freshly installed nodes have no known_hosts entry yet")
	fl.DurationVar(&o.timeout, "timeout", 5*time.Minute, "give up on the whole run after this long")
	fl.BoolVarP(&o.verbose, "verbose", "v", false, "print every check, not only what needs attention")
}

// session builds a preflight session from a loaded document.
func (o *preflightOptions) session(doc *spec.Document) *preflight.Session {
	return &preflight.Session{
		Spec: doc.Spec,
		Dir:  doc.Dir(),
		Resolve: func(field string, ref v1alpha1.SourceRef, secret bool) ([]byte, error) {
			return doc.Resolve(field, ref, secret, o.allowLiteral)
		},
		SkipHostKeyCheck: o.insecureHost,
	}
}

// load reads and validates the document, which every consumer of preflight has
// to do first: probing nodes named by a document that does not parse wastes an
// operator's time on a problem they could have been told about immediately.
// The applied list is returned rather than discarded: `plan` prints which
// values the profile supplied, and re-applying the profile to find out would
// mean doing it twice and trusting the two runs to agree.
func (o *preflightOptions) load() (*spec.Document, []string, error) {
	if o.specFile == "" {
		return nil, nil, errors.New("pass -f cluster.yaml")
	}
	doc, err := spec.Load(o.specFile)
	if err != nil {
		return nil, nil, err
	}
	applied, err := doc.ApplyProfile()
	if err != nil {
		return nil, nil, err
	}
	if err := doc.Validate(o.allowLiteral); err != nil {
		return nil, nil, fmt.Errorf("%s is not valid:\n%w", o.specFile, err)
	}
	return doc, applied, nil
}

func newPreflightCmd() *cobra.Command {
	var o preflightOptions

	cmd := &cobra.Command{
		Use:   "preflight",
		Short: "Measure what the document asks for against what the nodes can actually do",
		Long: `preflight reads cluster.yaml and measures every claim it makes.

Nothing is changed. Every check reads, tests or dry-runs; where a value can only
be observed after loading a kernel module, the check reports that it could not
be measured rather than loading it.

Checks that need no node -- CIDR collisions, certificate material, the airgap
bundle -- run first, so a document written before the machines exist can still
be checked.`,
		Example: `  malmok preflight -f cluster.yaml
  malmok preflight -f cluster.yaml -v
  malmok preflight -f cluster.yaml --insecure-host-key`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			doc, _, err := o.load()
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), o.timeout)
			defer cancel()

			out := cmd.OutOrStdout()
			rep := o.session(doc).Run(ctx)
			printReport(out, rep, o.verbose)

			if blocking := rep.Blocking(); len(blocking) > 0 {
				return fmt.Errorf("%d checks block the install; nothing has been changed", len(blocking))
			}
			return nil
		},
	}

	o.register(cmd)
	return cmd
}

// printReport writes the run in the order an operator reads it: what is wrong
// first, then everything else only if asked.
func printReport(w io.Writer, rep preflight.Report, verbose bool) {
	for host, why := range rep.Unreachable {
		fmt.Fprintf(w, "%-4s %s could not be reached: %s\n", "FAIL", host, why)
	}

	printed := 0
	printed += printGroup(w, "document", rep.Document, verbose)
	for _, node := range rep.Nodes {
		title := node.Host
		if node.Hostname != "" && node.Hostname != node.Host {
			title = fmt.Sprintf("%s (%s)", node.Host, node.Hostname)
		}
		if node.OS.Family != "" {
			title += fmt.Sprintf("  %s %s  %s  %s", node.OS.Family, node.OS.Version, node.OS.Kernel, node.Arch)
		}
		printed += printGroup(w, title, preflight.SortedProbes(node), verbose)
	}

	if printed == 0 {
		fmt.Fprintln(w, "\nNothing needs attention. Pass -v to see every check.")
	}
	fmt.Fprintf(w, "\n%s\n", rep.Summary())
}

// printGroup writes one section, returning how many lines it printed.
func printGroup(w io.Writer, title string, results []preflight.ProbeResult, verbose bool) int {
	var lines []string
	for _, p := range results {
		mark := preflight.Mark(p)
		if !verbose && mark == "OK" {
			continue
		}
		// A skip is only worth printing unasked when it is a skip that hides
		// something: an unimplemented probe, not an inapplicable one.
		if !verbose && mark == "SKIP" && p.Code != "NOT_MEASURED" {
			continue
		}
		line := fmt.Sprintf("%-4s %s", mark, p.ID)
		if p.Code != "" {
			line += " [" + p.Code + "]"
		}
		lines = append(lines, line+"  "+p.Detail)
	}
	if len(lines) == 0 {
		return 0
	}
	fmt.Fprintf(w, "\n%s\n%s\n", title, strings.Repeat("-", len(title)))
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
	return len(lines)
}
