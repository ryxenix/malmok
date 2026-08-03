// Package attach follows a run's event stream: replay what already happened,
// then tail what happens next. See docs/11-execute.md §1.2 and §5.6.
//
// WHY REPLAY RATHER THAN READ THE STATE FILE
//
//	state.json is a resume summary. It holds no log lines and no probe
//	evidence, so it cannot reconstruct the screen an operator was looking at
//	before the SSH session dropped. The event file can, because it is the whole
//	history.
//
// Nothing here talks to the engine. A follower is a reader of a file, which is
// what makes "the renderer died, the engine did not" work at all (ADR-002).
package attach

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"platform.ryxen.dev/platformctl/internal/event"
)

// DefaultPollInterval is how often a follower looks for new bytes. Fast enough
// that a human reads it as live, slow enough that a three-hour install does not
// spin a core.
const DefaultPollInterval = 200 * time.Millisecond

// Sink consumes a followed stream.
//
// Reset means "discard everything you have accumulated; a full replay is about
// to follow". A follower calls it before the initial replay and again whenever
// it has to re-read the file, so a Sink can be a plain accumulator rather than
// something that reasons about duplicates.
type Sink interface {
	Reset()
	Handle(event.Event) error
}

// GapReporter is an optional Sink extension. A follower calls Gap when a
// sequence gap survives a full re-read, meaning the lines really are absent
// from the file rather than merely unread.
//
// The engine guarantees gapless numbering within a run, so this is evidence of
// something outside it: a rotated or hand-edited file. A renderer should say so
// rather than quietly drawing an incomplete history.
type GapReporter interface {
	Gap(run string, expected, got uint64)
}

// Follower tails one event file.
type Follower struct {
	// Path is the event file. It need not exist yet: a follower started at the
	// same moment as the engine waits for it.
	Path string

	// Run filters to one run id. Empty follows every run in the file, which is
	// what a fixed output.eventLog holds (§1.1).
	Run string

	// PollInterval defaults to DefaultPollInterval.
	PollInterval time.Duration

	// StopOnRunEnd ends Follow when the followed run emits a terminal
	// kind=run event. Without it Follow only returns on context cancellation.
	StopOnRunEnd bool

	offset  int64
	lastSeq map[string]uint64

	// tolerated remembers gaps that already survived one full re-read, so the
	// follower stops re-reading for them. Without it a file with a real gap
	// makes the follower loop forever: every pass finds the same hole and asks
	// to start over again.
	tolerated map[string]bool
}

// Follow replays the file and then tails it until the context is cancelled or,
// with StopOnRunEnd, until the run finishes.
func (f *Follower) Follow(ctx context.Context, sink Sink) error {
	interval := f.PollInterval
	if interval <= 0 {
		interval = DefaultPollInterval
	}

	if f.tolerated == nil {
		f.tolerated = map[string]bool{}
	}

	file, err := f.waitForFile(ctx)
	if err != nil {
		return err
	}
	defer file.Close()

	if err := f.restart(file, sink); err != nil {
		return err
	}
	done, err := f.drain(file, sink)
	if err != nil {
		return err
	}
	if done && f.StopOnRunEnd {
		return nil
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}

		// A file shorter than our offset was replaced or rotated. Whatever we
		// are pointing at is not the stream we were reading.
		info, err := file.Stat()
		if err != nil {
			return fmt.Errorf("attach: stat %s: %w", f.Path, err)
		}
		if info.Size() < f.offset {
			if err := f.restart(file, sink); err != nil {
				return err
			}
		}

		done, err := f.drain(file, sink)
		if err != nil {
			return err
		}
		if done && f.StopOnRunEnd {
			return nil
		}
	}
}

// restart rewinds to the beginning and tells the sink to drop what it has.
func (f *Follower) restart(file *os.File, sink Sink) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("attach: rewind %s: %w", f.Path, err)
	}
	f.offset = 0
	f.lastSeq = map[string]uint64{}
	sink.Reset()
	return nil
}

// drain reads everything available, re-reading from the start at most once per
// distinct gap.
//
// §5.6 says a gap means lost lines and the file should be re-read. That
// recovers lines this reader missed; it cannot conjure lines the file does not
// contain. So the first sighting of a gap triggers a re-read, and a gap that
// survives it is reported and accepted — otherwise a rotated file would put the
// follower in a loop it never leaves.
func (f *Follower) drain(file *os.File, sink Sink) (runEnded bool, err error) {
	for {
		ended, gap, err := f.readAvailable(file, sink)
		if err != nil {
			return false, err
		}
		if gap == nil {
			return ended, nil
		}
		f.tolerated[gapKey(gap)] = true
		if err := f.restart(file, sink); err != nil {
			return false, err
		}
	}
}

func gapKey(g *event.ErrGap) string {
	return fmt.Sprintf("%s:%d->%d", g.Run, g.Expected, g.Got)
}

// readAvailable reads every complete line after the current offset. It returns
// a non-nil gap when it hits an untolerated one, having consumed nothing past
// it.
//
// Only complete lines advance the offset. The engine appends, so the tail of
// the file is routinely a line still being written; consuming it would deliver
// a truncated event and then skip the real one.
func (f *Follower) readAvailable(file *os.File, sink Sink) (runEnded bool, gap *event.ErrGap, err error) {
	if _, err := file.Seek(f.offset, io.SeekStart); err != nil {
		return false, nil, fmt.Errorf("attach: seek %s: %w", f.Path, err)
	}
	r := bufio.NewReader(file)

	for {
		line, readErr := r.ReadBytes('\n')
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return runEnded, nil, nil // partial tail: offset stays before it
			}
			return runEnded, nil, fmt.Errorf("attach: read %s: %w", f.Path, readErr)
		}

		var e event.Event
		if jsonErr := json.Unmarshal(line, &e); jsonErr != nil {
			return runEnded, nil, fmt.Errorf("attach: %s: %w", f.Path, jsonErr)
		}

		if prev, seen := f.lastSeq[e.Run]; seen && e.Seq != prev+1 {
			g := &event.ErrGap{Run: e.Run, Expected: prev + 1, Got: e.Seq}
			if !f.tolerated[gapKey(g)] {
				return runEnded, g, nil
			}
			// Survived a re-read: the lines are genuinely absent.
			if reporter, ok := sink.(GapReporter); ok {
				reporter.Gap(g.Run, g.Expected, g.Got)
			}
		}
		f.lastSeq[e.Run] = e.Seq
		f.offset += int64(len(line))

		if f.Run != "" && e.Run != f.Run {
			continue
		}
		if herr := sink.Handle(e); herr != nil {
			return runEnded, nil, herr
		}
		if e.Kind == event.KindRun && e.Status.Terminal() {
			runEnded = true
		}
	}
}

// waitForFile opens Path, waiting for it to appear. Attaching at the same
// moment the engine starts is normal, not a race to lose.
func (f *Follower) waitForFile(ctx context.Context) (*os.File, error) {
	interval := f.PollInterval
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	for {
		file, err := os.Open(f.Path)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("attach: open %s: %w", f.Path, err)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("attach: %s never appeared: %w", f.Path, ctx.Err())
		case <-time.After(interval):
		}
	}
}

// ---------------------------------------------------------------------------
// Run discovery
// ---------------------------------------------------------------------------

// LatestRun returns the last run id appearing in an event file. With a fixed
// output.eventLog several runs share the file, and "attach with no arguments"
// means the most recent one.
func LatestRun(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("attach: open %s: %w", path, err)
	}
	defer f.Close()

	runs, err := event.Runs(f)
	if err != nil {
		return "", err
	}
	if len(runs) == 0 {
		return "", fmt.Errorf("attach: %s contains no events", path)
	}
	return runs[len(runs)-1], nil
}

// LatestRunDir returns the newest run directory under <bundlePath>/runs.
//
// Run ids are ULIDs, so lexical order is chronological order and listing the
// directory is enough -- no timestamps to parse and no clock to trust.
func LatestRunDir(bundlePath string) (string, error) {
	runsDir := filepath.Join(bundlePath, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return "", fmt.Errorf("attach: read %s: %w", runsDir, err)
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("attach: no runs under %s", runsDir)
	}
	sort.Strings(names)
	return filepath.Join(runsDir, names[len(names)-1]), nil
}
