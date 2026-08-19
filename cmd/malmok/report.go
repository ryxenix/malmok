package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"platform.ryxen.dev/malmok/internal/report"
)

func newReportCmd() *cobra.Command {
	var (
		bundle string
		runID  string
		stdout bool
	)

	cmd := &cobra.Command{
		Use:   "report",
		Short: "Build the audit report and the DNS record sheet for a run",
		Long: `report reads a finished run directory and writes the two artifacts of
docs/00-architecture.md §7 into it.

Nothing is read from the cluster. A report generated from live state would say
something different every time it ran, and what a customer receives has to be
the record of what happened -- which is also why it can be produced again from
an old run, on a machine that never touched the cluster.`,
		Example: `  malmok report
  malmok report --run 01JBQ8F2K3M5N7P9R1S3T5V7W9
  malmok report --stdout | less`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := findRun(bundle, runID)
			if err != nil {
				return err
			}
			run, err := report.Load(dir)
			if err != nil {
				return err
			}

			if stdout {
				fmt.Fprint(cmd.OutOrStdout(), report.Audit(run))
				if sheet := report.DNSRecords(run); sheet != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "\n---\n\n%s", sheet)
				}
				return nil
			}

			written, err := report.Write(run)
			if err != nil {
				return err
			}
			for _, w := range written {
				fmt.Fprintln(cmd.OutOrStdout(), w)
			}
			if len(written) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "nothing to write")
			}
			return nil
		},
	}

	fl := cmd.Flags()
	fl.StringVar(&bundle, "bundle", "./out", "bundle path containing runs/")
	fl.StringVar(&runID, "run", "", "run id (default: the newest one)")
	fl.BoolVar(&stdout, "stdout", false, "print the report instead of writing it into the run")

	return cmd
}

// findRun locates a run directory, defaulting to the newest.
//
// Newest by name rather than by mtime: run ids are ULIDs, which sort
// chronologically, so the directory listing is already the answer and a
// touched file cannot change it.
func findRun(bundle, runID string) (string, error) {
	if runID != "" {
		dir := filepath.Join(bundle, "runs", runID)
		if _, err := os.Stat(dir); err != nil {
			return "", fmt.Errorf("no run %s under %s", runID, bundle)
		}
		return dir, nil
	}

	entries, err := os.ReadDir(filepath.Join(bundle, "runs"))
	if err != nil {
		return "", fmt.Errorf("no runs under %s: %w", bundle, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", errors.New("no run has been recorded under " + bundle)
	}
	sort.Strings(names)
	return filepath.Join(bundle, "runs", names[len(names)-1]), nil
}
