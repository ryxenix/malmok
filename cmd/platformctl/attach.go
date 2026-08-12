package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"platform.ryxen.dev/platformctl/internal/attach"
	"platform.ryxen.dev/platformctl/internal/event"
)

func newAttachCmd() *cobra.Command {
	var (
		file    string
		bundle  string
		run     string
		follow  bool
		verbose bool
		output  string
		screen  tuiFlags
	)

	cmd := &cobra.Command{
		Use:   "attach",
		Short: "Follow a run's progress",
		Long: `attach replays a run's event stream to rebuild what has happened so far,
then follows it live.

The engine runs as its own process and outlives the renderer, so a dropped SSH
session costs nothing: reattach and the screen comes back. See
docs/11-execute.md §1.2.`,
		Example: `  # follow the newest run under the default bundle path
  platformctl attach

  # follow a specific event file, printing every log line
  platformctl attach --file ./out/events.jsonl --verbose

  # replay a finished run without waiting for more
  platformctl attach --run 01JBQ8F2K3M5N7P9R1S3T5V7W9 --follow=false`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := resolveEventFile(file, bundle)
			if err != nil {
				return err
			}
			if run == "" && follow {
				// Best effort: with no events yet there is no latest run, and
				// following every run in the file is the right default anyway.
				if latest, err := attach.LatestRun(path); err == nil {
					run = latest
				}
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			if screen.enabled {
				// Watching a run somebody else started: no work to drive, so
				// the wizard opens straight on the progress screen.
				sc, err := screen.screen(ctx, run, filepath.Dir(path), nil, nil, nil)
				if err != nil {
					return err
				}
				return runWithScreen(ctx, sc, path, run)
			}

			sink, finish, err := newSink(cmd.OutOrStdout(), output, verbose)
			if err != nil {
				return err
			}

			f := &attach.Follower{Path: path, Run: run, StopOnRunEnd: true}
			if !follow {
				// Replay only: cancel as soon as the file is exhausted.
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
			}

			err = f.Follow(ctx, sink)
			finish()

			switch {
			case err == nil, ctx.Err() != nil:
				return nil // finished, interrupted, or replay complete
			default:
				return err
			}
		},
	}

	fl := cmd.Flags()
	fl.StringVar(&file, "file", "", "event file to follow (default: newest run under --bundle)")
	fl.StringVar(&bundle, "bundle", "./out", "bundle path containing runs/")
	fl.StringVar(&run, "run", "", "run id to follow (default: the newest one)")
	fl.BoolVar(&follow, "follow", true, "keep following after the current end of file")
	fl.BoolVarP(&verbose, "verbose", "v", false, "include log lines")
	fl.StringVarP(&output, "output", "o", "text", "output format: text | json")
	screen.register(cmd)

	return cmd
}

// resolveEventFile picks the file to follow: an explicit --file, else the
// newest run directory under --bundle, else the bundle's shared event log.
func resolveEventFile(file, bundle string) (string, error) {
	if file != "" {
		return file, nil
	}
	if runDir, err := attach.LatestRunDir(bundle); err == nil {
		return filepath.Join(runDir, "events.jsonl"), nil
	}
	// A fixed output.eventLog keeps every run in one file (§1.1), in which case
	// there is no runs/ directory to find.
	shared := filepath.Join(bundle, "events.jsonl")
	if _, err := os.Stat(shared); err == nil {
		return shared, nil
	}
	return "", fmt.Errorf("no event file found: pass --file, or check that %s contains runs/ or events.jsonl", bundle)
}

// newSink builds the renderer and a function to call once following ends.
func newSink(w interface{ Write([]byte) (int, error) }, output string, verbose bool) (attach.Sink, func(), error) {
	switch output {
	case "text":
		r := attach.NewTextRenderer(w)
		r.Verbose = verbose
		return r, func() { fmt.Fprint(w, r.Summary()) }, nil
	case "json":
		// Pass-through. The engine already emits JSONL, so `--output json` is
		// the stream itself rather than a second serialisation of it.
		return &jsonSink{enc: json.NewEncoder(w)}, func() {}, nil
	default:
		return nil, nil, fmt.Errorf("unknown output format %q: want text or json", output)
	}
}

type jsonSink struct{ enc *json.Encoder }

func (s *jsonSink) Reset() {}

func (s *jsonSink) Handle(e event.Event) error { return s.enc.Encode(e) }
