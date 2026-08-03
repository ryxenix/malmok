package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"platform.ryxen.dev/platformctl/internal/attach"
	"platform.ryxen.dev/platformctl/internal/tui"
)

// tuiFlags are shared by apply and attach.
type tuiFlags struct {
	enabled bool
	ascii   bool
	lang    string
}

func (f *tuiFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.BoolVar(&f.enabled, "tui", false, "draw a full-screen view instead of streaming lines")
	fl.BoolVar(&f.ascii, "ascii", false, "force the ASCII character set (default: auto-detect)")
	fl.StringVar(&f.lang, "lang", "en", "screen language: en | ko")
}

func (f *tuiFlags) screen(ctx context.Context, runID string, mode tui.Mode) (*tui.Screen, error) {
	ascii := f.ascii
	if !ascii {
		ascii = tui.DetectASCII(os.Getenv)
	}
	lang := tui.Lang(f.lang)
	if lang != tui.LangEN && lang != tui.LangKO {
		return nil, fmt.Errorf("unknown --lang %q: want en or ko", f.lang)
	}
	return tui.NewScreen(ctx, tui.Options{
		RunID: runID, Mode: mode, ASCII: ascii, Lang: lang,
	})
}

// runWithScreen follows path in the background while the screen owns the
// terminal, and closes the screen when work finishes.
//
// The screen is a consumer of the event file, exactly like `attach` from
// another terminal. Nothing renders from engine state directly, which is what
// keeps ADR-002 true in practice rather than only in the architecture test.
func runWithScreen(
	ctx context.Context,
	screen *tui.Screen,
	path, runID string,
	work func(context.Context) error,
) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	follower := &attach.Follower{Path: path, Run: runID, PollInterval: pollInterval}
	go func() { _ = follower.Follow(ctx, screen.Sink()) }()

	workErr := make(chan error, 1)
	go func() {
		err := work(ctx)
		// Let the last events reach the screen before it closes; otherwise the
		// final phase transition is drawn after the terminal is restored, which
		// looks like the run ended one step early.
		drain(ctx)
		workErr <- err
		screen.Quit()
	}()

	if err := screen.Run(); err != nil {
		cancel()
		return err
	}
	// The operator quit first: cancel the work and take whatever it reports.
	cancel()
	return <-workErr
}

// pollInterval is how often a follower looks for new events while a screen is
// up. Fast enough to read as live without spinning a core on a long install.
const pollInterval = 50 * time.Millisecond

// drain gives the follower a moment to deliver what the engine just wrote.
func drain(ctx context.Context) {
	t := time.NewTimer(3 * pollInterval)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
