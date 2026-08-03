package event

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// maxLineBytes caps one JSONL line. Evidence can be long -- a probe may capture
// a whole command's output -- so the default bufio limit of 64 KiB is too small
// to rely on.
const maxLineBytes = 8 << 20

// ErrGap reports that a run's sequence numbers skipped. Per §5.6 a renderer
// treats this as "I lost lines" and re-reads the file rather than drawing a
// screen it knows is incomplete.
type ErrGap struct {
	Run      string
	Expected uint64
	Got      uint64
}

func (e *ErrGap) Error() string {
	return fmt.Sprintf("event: gap in run %s: expected seq %d, got %d", e.Run, e.Expected, e.Got)
}

// Scanner reads a JSONL event stream and verifies sequence continuity per run.
//
// Continuity is tracked per run because a fixed output.eventLog accumulates
// several runs in one file (§1.1) while seq only ever increases within a run.
type Scanner struct {
	sc   *bufio.Scanner
	last map[string]uint64
	ev   Event
	err  error
	line int
}

// NewScanner reads events from r.
func NewScanner(r io.Reader) *Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	return &Scanner{sc: sc, last: map[string]uint64{}}
}

// Scan advances to the next event. It returns false at end of input or on the
// first error; check Err.
//
// A gap sets Err and stops the scan. It is not skipped over: continuing would
// hand the caller a screen built from a stream it already knows is missing
// lines.
func (s *Scanner) Scan() bool {
	for s.sc.Scan() {
		s.line++
		raw := s.sc.Bytes()
		if len(raw) == 0 {
			continue
		}

		var e Event
		if err := json.Unmarshal(raw, &e); err != nil {
			s.err = fmt.Errorf("event: line %d: %w", s.line, err)
			return false
		}

		if prev, seen := s.last[e.Run]; seen && e.Seq != prev+1 {
			s.err = &ErrGap{Run: e.Run, Expected: prev + 1, Got: e.Seq}
			return false
		}
		s.last[e.Run] = e.Seq

		s.ev = e
		return true
	}
	if err := s.sc.Err(); err != nil {
		s.err = fmt.Errorf("event: read: %w", err)
	}
	return false
}

// Event returns the event read by the most recent Scan.
func (s *Scanner) Event() Event { return s.ev }

// Err returns the first error encountered, or nil at a clean end of input.
func (s *Scanner) Err() error { return s.err }

// LastSeq returns the highest sequence number seen for run.
func (s *Scanner) LastSeq(run string) uint64 { return s.last[run] }

// ---------------------------------------------------------------------------
// Replay
// ---------------------------------------------------------------------------

// ReadAll replays a whole stream. `attach` uses this to rebuild the screen
// before switching to tail (§1.2): the state file is a resume summary and holds
// neither log lines nor probe evidence, so it cannot reconstruct what the
// operator was looking at.
func ReadAll(r io.Reader) ([]Event, error) {
	sc := NewScanner(r)
	var out []Event
	for sc.Scan() {
		out = append(out, sc.Event())
	}
	return out, sc.Err()
}

// ReadRun replays only the events belonging to run.
//
// Sequence continuity is still checked across the whole stream, so a gap in
// another run is reported rather than hidden -- a truncated file is a fact
// about the file, not about one run in it.
func ReadRun(r io.Reader, run string) ([]Event, error) {
	sc := NewScanner(r)
	var out []Event
	for sc.Scan() {
		if e := sc.Event(); e.Run == run {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

// Runs lists the run ids present in a stream, in first-appearance order.
func Runs(r io.Reader) ([]string, error) {
	sc := NewScanner(r)
	var (
		out  []string
		seen = map[string]bool{}
	)
	for sc.Scan() {
		if run := sc.Event().Run; !seen[run] {
			seen[run] = true
			out = append(out, run)
		}
	}
	return out, sc.Err()
}

// IsGap reports whether err is a sequence gap.
func IsGap(err error) bool {
	var g *ErrGap
	return errors.As(err, &g)
}
