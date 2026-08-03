package attach

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"platform.ryxen.dev/platformctl/internal/event"
)

// TextRenderer turns a followed stream into lines for a terminal.
//
// THIS IS WHERE GLYPHS LIVE
//
//	The engine emits Progress{Done, Total}. The bar below is drawn here, by the
//	consumer, and never appears in an event -- Event.Validate rejects one that
//	contains block characters (§5.3). If that inverted, `--output json` would
//	start carrying somebody's terminal width.
//
// It is deliberately plain: no cursor movement, no redraw, one line per event.
// A TUI is a different Sink over the same stream (ADR-002), not a fancier
// version of this one.
type TextRenderer struct {
	mu sync.Mutex
	w  io.Writer

	// Verbose includes log-kind events. Off by default: a real install emits
	// thousands and they bury the state transitions.
	Verbose bool

	phases map[string]event.Status
	order  []string
	failed []event.Event
}

// NewTextRenderer writes to w.
func NewTextRenderer(w io.Writer) *TextRenderer {
	return &TextRenderer{w: w, phases: map[string]event.Status{}}
}

// Reset drops accumulated state ahead of a replay.
func (r *TextRenderer) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phases = map[string]event.Status{}
	r.order = nil
	r.failed = nil
}

// Handle renders one event.
func (r *TextRenderer) Handle(e event.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if e.Kind == event.KindPhase {
		if _, seen := r.phases[e.Phase]; !seen {
			r.order = append(r.order, e.Phase)
		}
		r.phases[e.Phase] = e.Status
	}
	if e.Status == event.StatusFailed || e.Status == event.StatusBlocked {
		r.failed = append(r.failed, e)
	}
	if e.Kind == event.KindLog && !r.Verbose {
		return nil
	}

	_, err := fmt.Fprintln(r.w, Line(e))
	return err
}

// Summary prints the end-of-run rollup: phase states, then anything that failed.
//
// Failures go last so they are what remains on screen. Burying them above a
// wall of phase output is how a report becomes decorative.
func (r *TextRenderer) Summary() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var b strings.Builder
	b.WriteString("\nphases\n")
	for _, name := range r.order {
		fmt.Fprintf(&b, "  %-16s %s\n", name, r.phases[name])
	}
	if len(r.failed) == 0 {
		return b.String()
	}
	fmt.Fprintf(&b, "\n%d failure(s)\n", len(r.failed))
	for _, e := range r.failed {
		where := e.Phase
		if e.Node != "" {
			where += " on " + e.Node
		}
		fmt.Fprintf(&b, "  %-10s %-28s %s\n", e.Code, where, e.Detail)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Line formatting
// ---------------------------------------------------------------------------

// Line formats one event. Exported so a test can assert the §5.4 mapping
// without owning a renderer.
func Line(e event.Event) string {
	var b strings.Builder

	b.WriteString(e.TS.Format("15:04:05.000"))
	b.WriteString(" ")
	b.WriteString(marker(e))
	b.WriteString(" ")

	switch e.Kind {
	case event.KindLog:
		fmt.Fprintf(&b, "%-24s %s", scope(e), e.Detail)
	case event.KindProbe, event.KindDecision:
		fmt.Fprintf(&b, "%-10s %-24s %s", e.Code, scope(e), e.Detail)
	default:
		fmt.Fprintf(&b, "%-10s %-24s %s", string(e.Kind), scope(e), e.Detail)
	}

	if e.Progress != nil {
		fmt.Fprintf(&b, "  %s %d/%d", Bar(e.Progress.Done, e.Progress.Total, 10),
			e.Progress.Done, e.Progress.Total)
	}
	if e.MaxAttempts > 1 {
		fmt.Fprintf(&b, "  retry %d/%d", e.Attempt, e.MaxAttempts)
	}
	return strings.TrimRight(b.String(), " ")
}

func marker(e event.Event) string {
	switch e.Status {
	case event.StatusOK:
		return "OK "
	case event.StatusSkipped:
		return "-- "
	case event.StatusFailed, event.StatusBlocked:
		return "XX "
	case event.StatusRunning:
		return ".. "
	case event.StatusPending:
		return "   "
	}
	if e.Level == event.LevelError || e.Level == event.LevelWarn {
		return "!  "
	}
	return "   "
}

func scope(e event.Event) string {
	parts := make([]string, 0, 3)
	for _, s := range []string{e.Phase, e.Step, e.Node} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "/")
}

// Bar draws a progress bar. The only place block glyphs may appear.
func Bar(done, total, width int) string {
	if total <= 0 || width <= 0 {
		return ""
	}
	if done < 0 {
		done = 0
	}
	if done > total {
		done = total
	}
	filled := done * width / total
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}

// ---------------------------------------------------------------------------
// Collector
// ---------------------------------------------------------------------------

// Collector is a Sink that keeps every event. Tests and `attach --output json`
// use it; it is also the simplest possible demonstration that replay
// reconstructs a run (§7 C2).
type Collector struct {
	mu     sync.Mutex
	events []event.Event
	resets int
	gaps   []string
}

// Gap records a sequence gap that survived a re-read.
func (c *Collector) Gap(run string, expected, got uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gaps = append(c.gaps, fmt.Sprintf("%s:%d->%d", run, expected, got))
}

// Gaps returns the gaps reported so far.
func (c *Collector) Gaps() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.gaps...)
}

func (c *Collector) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = nil
	c.resets++
}

func (c *Collector) Handle(e event.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
	return nil
}

// Events returns a copy of what has been collected since the last Reset.
func (c *Collector) Events() []event.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]event.Event(nil), c.events...)
}

// Resets counts how many times the follower had to start over.
func (c *Collector) Resets() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resets
}

// Runs lists the run ids collected, sorted.
func (c *Collector) Runs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	seen := map[string]bool{}
	var out []string
	for _, e := range c.events {
		if !seen[e.Run] {
			seen[e.Run] = true
			out = append(out, e.Run)
		}
	}
	sort.Strings(out)
	return out
}
