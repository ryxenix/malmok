package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"platform.ryxen.dev/platformctl/internal/event"
)

// Reading past runs back.
//
// The event file is the only record of a build that survives the terminal it
// was watched in, and `attach` has always been able to read it. Reaching it
// from the menu means an operator who opened the tool to find out what happened
// last week does not have to know that command exists.

// BundlePath is where runs are looked for. Set by the caller from the same flag
// the rest of the tool uses.
func (w *Wizard) loadRuns() []RunEntry {
	root := w.bundle
	if root == "" {
		root = "./out"
	}
	entries, err := os.ReadDir(filepath.Join(root, "runs"))
	if err != nil {
		return nil
	}

	var out []RunEntry
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, "runs", e.Name())
		path := filepath.Join(dir, "events.jsonl")
		info, err := os.Stat(path)
		if err != nil {
			// A directory with no event file is a run that never started
			// writing. Listing it would offer something that opens to nothing.
			continue
		}
		out = append(out, RunEntry{
			ID:      e.Name(),
			Dir:     dir,
			When:    info.ModTime().Local().Format("2006-01-02 15:04"),
			Summary: summarise(path),
		})
	}

	// Newest first. Run ids are ULIDs, so the name already sorts
	// chronologically and a touched file cannot change the order.
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// summarise reads a run's outcome without loading all of it into the screen.
func summarise(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	var phases, failed, blocked int
	outcome := ""
	sc := event.NewScanner(f)
	for sc.Scan() {
		e := sc.Event()
		switch {
		case e.Kind == event.KindPhase && e.Status == event.StatusOK:
			phases++
		case e.Kind == event.KindStep && e.Status == event.StatusFailed:
			failed++
		case e.Status == event.StatusBlocked:
			blocked++
		case e.Kind == event.KindRun:
			outcome = string(e.Status)
		}
	}

	parts := []string{}
	if phases > 0 {
		parts = append(parts, itoa(phases)+" phases")
	}
	if failed > 0 {
		parts = append(parts, itoa(failed)+" failed")
	}
	if blocked > 0 {
		parts = append(parts, itoa(blocked)+" blocked")
	}
	if outcome != "" {
		parts = append(parts, outcome)
	}
	return strings.Join(parts, ", ")
}

// openRun replays a run into the install screen.
//
// The screen is already a renderer of the event stream, so replaying is the
// same code path as watching one live -- which is the point of ADR-002 showing
// up as a feature rather than as an architecture diagram.
func (w *Wizard) openRun(r RunEntry) tea.Cmd {
	// Cleared inline rather than through Reset: that posts a message into the
	// loop we are already inside, and the events arriving next would fold into
	// a view the reset had not reached yet.
	w.order, w.phases = nil, map[string]*phaseView{}
	w.logs, w.failures = nil, nil

	w.step = StepInstall
	w.busy = false
	w.replaying = true
	w.runDir, w.runID = r.Dir, r.ID
	w.enter()

	return func() tea.Msg {
		f, err := os.Open(filepath.Join(r.Dir, "events.jsonl"))
		if err != nil {
			return nil
		}
		defer f.Close()

		var events []event.Event
		sc := event.NewScanner(f)
		for sc.Scan() {
			events = append(events, sc.Event())
		}
		return replayMsg{events: events}
	}
}

// replayMsg carries a finished run's events into the model.
//
// Delivered as one message rather than streamed: the run is over, and pacing a
// replay to look live would be an animation of something that already happened.
type replayMsg struct{ events []event.Event }
