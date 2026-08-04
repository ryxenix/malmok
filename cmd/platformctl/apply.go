package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"platform.ryxen.dev/platformctl/internal/attach"
	"platform.ryxen.dev/platformctl/internal/demo"
	"platform.ryxen.dev/platformctl/internal/engine"
	"platform.ryxen.dev/platformctl/internal/event"
	"platform.ryxen.dev/platformctl/internal/state"
	"platform.ryxen.dev/platformctl/internal/tui"
)

func newApplyCmd() *cobra.Command {
	var (
		specFile string
		bundle   string
		isDemo   bool
		speed    float64
		failAt   string
		flakyAt  string
		recheck  bool
		resume   string
		quiet    bool
		verbose  bool
		screen   tuiFlags
	)

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Run the phases that build a cluster",
		Long: `apply runs the phase catalogue. Every phase is idempotent and every run is
resumable: an interrupted run continues from where it stopped rather than
starting over. See docs/11-execute.md.`,
		Example: `  # simulated run -- no node is contacted, nothing is installed
  platformctl apply --demo

  # simulated run that fails, to look at the failure path
  platformctl apply --demo --fail-at l1-bootstrap/rke2-server-ready

  # resume an interrupted run
  platformctl apply --demo --resume 01JBQ8F2K3M5N7P9R1S3T5V7W9`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !isDemo {
				if specFile == "" {
					return errors.New("pass -f cluster.yaml, or --demo for a simulated run")
				}
				// Naming what is missing beats a command that appears to work.
				return fmt.Errorf(
					"reading %s is not implemented yet: the cluster.yaml loader, profile "+
						"defaults and the plan generator are still to be written. "+
						"Use --demo to exercise the engine in the meantime", specFile)
			}

			opts := demo.Options{Speed: speed, FailAt: failAt, FlakyAt: flakyAt}
			if err := demo.Validate(opts); err != nil {
				return err
			}

			runDir, st, err := openRun(bundle, resume)
			if err != nil {
				return err
			}

			events, err := event.OpenFile(filepath.Join(runDir, "events.jsonl"), st.Run)
			if err != nil {
				return err
			}
			defer events.Close()

			runner := &engine.Runner{
				Events:    events.Writer,
				State:     st,
				StatePath: state.Path(runDir),
				Resume:    state.Options{Recheck: recheck},
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			eventPath := filepath.Join(runDir, "events.jsonl")
			phases := demo.Phases(opts)

			if screen.enabled {
				// The wizard drives the work: preflight first so its findings
				// can be reviewed, then the remaining phases. Both go through
				// the same runner and state file, so resume treats them as one
				// run.
				preflight := func(c context.Context, _ tui.Config) error {
					return runner.Run(c, phases[:1])
				}
				install := func(c context.Context, _ tui.Config) error {
					return runner.Run(c, phases[1:])
				}
				sc, err := screen.screen(ctx, st.Run, runDir, preflight, install)
				if err != nil {
					return err
				}
				if err := runWithScreen(ctx, sc, eventPath, st.Run); err != nil {
					return err
				}
				if sc.Aborted() {
					return errors.New("aborted")
				}
				return nil
			}

			// Render as we go. The renderer is a consumer of the stream, never a
			// participant: the same file drives `attach` from another terminal,
			// and the run survives this process losing its terminal.
			var sink attach.Sink
			var summary func()
			if quiet {
				sink, summary = &attach.Collector{}, func() {}
			} else {
				r := attach.NewTextRenderer(cmd.OutOrStdout())
				r.Verbose = verbose
				sink, summary = r, func() { fmt.Fprint(cmd.OutOrStdout(), r.Summary()) }
			}
			defer summary()

			fmt.Fprintf(cmd.ErrOrStderr(), "run %s\n  %s\n\n", st.Run, runDir)

			done := followRun(ctx, eventPath, st.Run, sink)
			runErr := runner.Run(ctx, phases)
			<-done

			return runErr
		},
	}

	fl := cmd.Flags()
	fl.StringVarP(&specFile, "file", "f", "", "cluster.yaml to apply")
	fl.StringVar(&bundle, "bundle", "./out", "bundle path; runs are written under <bundle>/runs/")
	fl.BoolVar(&isDemo, "demo", false, "simulate a run without contacting any node")
	fl.Float64Var(&speed, "speed", 1, "scale simulated delays; higher is faster")
	fl.StringVar(&failAt, "fail-at", "", "make this step id fail permanently")
	fl.StringVar(&flakyAt, "flaky-at", "", "make this step id fail twice, then succeed")
	fl.BoolVar(&recheck, "recheck", false, "re-observe steps already recorded as done")
	fl.StringVar(&resume, "resume", "", "resume the run with this id")
	fl.BoolVar(&quiet, "quiet", false, "emit events to the file only")
	fl.BoolVarP(&verbose, "verbose", "v", false, "include log lines")
	screen.register(cmd)

	return cmd
}

// openRun creates a run directory, or reopens one to resume.
func openRun(bundle, resumeID string) (string, *state.State, error) {
	if resumeID != "" {
		dir := filepath.Join(bundle, "runs", resumeID)
		st, err := state.Load(state.Path(dir))
		if err != nil {
			return "", nil, err
		}
		return dir, st, nil
	}

	id := newRunID()
	dir := filepath.Join(bundle, "runs", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, fmt.Errorf("create run directory: %w", err)
	}
	// The demo has no cluster.yaml, so the digest covers what shapes the run.
	st := state.New(id, state.Digest([]byte("demo")), time.Now())
	return dir, st, nil
}

// newRunID returns a lexically sortable, time-ordered identifier.
//
// Crockford base32 over a millisecond timestamp and random tail: the same
// ordering property a ULID gives, without a dependency for one function.
func newRunID() string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

	ms := uint64(time.Now().UTC().UnixMilli())
	buf := make([]byte, 26)
	for i := 9; i >= 0; i-- {
		buf[i] = alphabet[ms&0x1f]
		ms >>= 5
	}
	for i := 10; i < 26; i++ {
		buf[i] = alphabet[rand.IntN(32)]
	}
	return string(buf)
}

// followRun tails the run's own event file so the terminal shows what the file
// records, rather than a second rendering path that could disagree with it.
func followRun(ctx context.Context, path, run string, sink attach.Sink) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		f := &attach.Follower{
			Path: path, Run: run, StopOnRunEnd: true,
			PollInterval: 50 * time.Millisecond,
		}
		_ = f.Follow(ctx, sink)
	}()
	return done
}
