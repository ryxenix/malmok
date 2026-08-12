package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"platform.ryxen.dev/platformctl/internal/attach"
	"platform.ryxen.dev/platformctl/internal/tui"
)

// tuiFlags are shared by apply and attach.
type tuiFlags struct {
	enabled bool
	ascii   bool
	mono    bool
	lang    string
	rail    bool
}

func (f *tuiFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.BoolVar(&f.enabled, "tui", false, "draw a full-screen view instead of streaming lines")
	fl.BoolVar(&f.ascii, "ascii", false, "force the ASCII character set (default: auto-detect)")
	fl.StringVar(&f.lang, "lang", "en", "screen language: en | ko")
	fl.BoolVar(&f.mono, "mono", false, "drop colour (NO_COLOR is honoured too)")
	fl.BoolVar(&f.rail, "rail", true,
		"show the step list down the left; --rail=false is the Proxmox layout (toggle with s)")
}

func (f *tuiFlags) screen(ctx context.Context, runID, runDir string, preflight, install, upgrade tui.Work) (*tui.Screen, error) {
	// The bundle is where the menu looks for past runs. Derived from the run
	// directory rather than passed again: they are always <bundle>/runs/<id>.
	bundle := ""
	if runDir != "" {
		bundle = filepath.Dir(filepath.Dir(runDir))
	}
	ascii := f.ascii
	if !ascii {
		ascii = tui.DetectASCII(os.Getenv)
	}
	lang := tui.Lang(f.lang)
	if lang != tui.LangEN && lang != tui.LangKO {
		return nil, fmt.Errorf("unknown --lang %q: want en or ko", f.lang)
	}
	return tui.NewScreen(ctx, tui.Options{
		RunID: runID, RunDir: runDir, ASCII: ascii, Mono: f.mono || os.Getenv("NO_COLOR") != "",
		Lang: lang, HideRail: !f.rail, Bundle: bundle,
		Preflight: preflight, Install: install, Upgrade: upgrade,
	})
}

// runWithScreen follows path in the background while the screen owns the
// terminal, and closes the screen when work finishes.
//
// The screen is a consumer of the event file, exactly like `attach` from
// another terminal. Nothing renders from engine state directly, which is what
// keeps ADR-002 true in practice rather than only in the architecture test.
func runWithScreen(ctx context.Context, screen *tui.Screen, path, runID string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// The screen renders the event file, exactly as `attach` does from another
	// terminal. Nothing is drawn from engine state directly, which is what
	// keeps ADR-002 true in practice rather than only in the architecture test.
	screen.Sink().SetWorkContext(ctx)

	follower := &attach.Follower{Path: path, Run: runID, PollInterval: pollInterval}
	go func() { _ = follower.Follow(ctx, screen.Sink()) }()

	return screen.Run()
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
