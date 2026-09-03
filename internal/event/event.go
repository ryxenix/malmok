// Package event defines the JSONL stream the engine emits and the renderers
// consume. It is the entire interface between them: see docs/11-execute.md §5
// and ADR-002.
//
// THE ENGINE DOES NOT KNOW WHAT A RENDERER IS
//
//	No colour, no width, no progress-bar glyphs, no alignment. Progress is
//	{done, total}, never a run of block characters. Validate enforces this
//	rather than leaving it to discipline, because the moment a rendered string
//	appears in the stream, `--output json` starts carrying terminal output and
//	machine consumers have to parse a screen.
//
// Detail text is English by policy (CLAUDE.md). A renderer that needs Korean
// looks up Code in an i18n catalogue -- which is why a failure event without a
// Code is untranslatable, and why Validate rejects one.
package event

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/ryxenix/malmok/internal/codes"
)

// ---------------------------------------------------------------------------
// Enumerations
// ---------------------------------------------------------------------------

// Kind selects which fields of an Event carry meaning. See §5.2.
type Kind string

const (
	KindRun      Kind = "run"      // run started or finished
	KindPhase    Kind = "phase"    // phase state transition
	KindStep     Kind = "step"     // step state transition
	KindProbe    Kind = "probe"    // one PF-/PV- result
	KindLog      Kind = "log"      // one line of remote command output
	KindDecision Kind = "decision" // a judgement, e.g. a downgrade (DG-xxx)
	KindArtifact Kind = "artifact" // an output file was produced
)

var validKinds = map[Kind]bool{
	KindRun: true, KindPhase: true, KindStep: true, KindProbe: true,
	KindLog: true, KindDecision: true, KindArtifact: true,
}

// Status is the lifecycle of a run, phase or step.
type Status string

const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusOK      Status = "ok"
	StatusSkipped Status = "skipped" // idempotency: the target state already held
	StatusFailed  Status = "failed"
	StatusBlocked Status = "blocked" // cannot proceed; not retryable
)

var validStatuses = map[Status]bool{
	StatusPending: true, StatusRunning: true, StatusOK: true,
	StatusSkipped: true, StatusFailed: true, StatusBlocked: true,
}

// Terminal reports whether s is an end state that will not change again.
func (s Status) Terminal() bool {
	return s == StatusOK || s == StatusSkipped || s == StatusFailed || s == StatusBlocked
}

// Level applies to KindLog only.
type Level string

const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

var validLevels = map[Level]bool{
	LevelDebug: true, LevelInfo: true, LevelWarn: true, LevelError: true,
}

// ---------------------------------------------------------------------------
// Event
// ---------------------------------------------------------------------------

// Progress is a fraction, not a rendering. See §5.3.
type Progress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

// Event is one line of the stream. Field names and semantics are fixed by
// docs/11-execute.md §5.1; changing them is a breaking change for every
// renderer and for any archived event file a customer still holds.
type Event struct {
	TS   Timestamp `json:"ts"`
	Run  string    `json:"run"`
	Seq  uint64    `json:"seq"`
	Kind Kind      `json:"kind"`

	Phase string `json:"phase,omitempty"`
	Step  string `json:"step,omitempty"`
	Node  string `json:"node,omitempty"`

	Status Status `json:"status,omitempty"`

	Attempt     int       `json:"attempt,omitempty"`
	MaxAttempts int       `json:"maxAttempts,omitempty"`
	Progress    *Progress `json:"progress,omitempty"`

	// Code is a registry identifier: PF-204, PV-002, DG-001. Validate rejects
	// anything absent from internal/codes, so a typo cannot reach an audit
	// report.
	Code string `json:"code,omitempty"`

	// Detail is one human-readable line, English, no newlines.
	Detail string `json:"detail,omitempty"`

	// Evidence is raw captured output kept for the audit trail. May be
	// multi-line; must be stripped of terminal control sequences at capture
	// time.
	Evidence string `json:"evidence,omitempty"`

	Level Level `json:"level,omitempty"`
}

// ---------------------------------------------------------------------------
// Timestamp
// ---------------------------------------------------------------------------

// Timestamp renders as RFC3339 with exactly millisecond precision in UTC.
// time.Time's default marshalling emits variable-width nanoseconds, which makes
// event files noisy to diff and awkward to read at a customer site.
type Timestamp struct{ time.Time }

const tsLayout = "2006-01-02T15:04:05.000Z"

func NewTimestamp(t time.Time) Timestamp { return Timestamp{t.UTC().Truncate(time.Millisecond)} }

func (t Timestamp) MarshalJSON() ([]byte, error) {
	return []byte(`"` + t.UTC().Format(tsLayout) + `"`), nil
}

func (t *Timestamp) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	parsed, err := time.Parse(tsLayout, s)
	if err != nil {
		// Accept general RFC3339 on the way in so that files written by other
		// tooling still replay; we only insist on the narrow form on output.
		parsed, err = time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return fmt.Errorf("event: bad timestamp %q: %w", s, err)
		}
	}
	t.Time = parsed.UTC()
	return nil
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// Validate reports every problem with e at once. It encodes the acceptance
// criteria in docs/11-execute.md §7 group C, so the tests that assert those
// criteria and the engine that emits events share one implementation.
func (e Event) Validate() []error {
	var errs []error
	bad := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if e.Run == "" {
		bad("run is required")
	}
	if e.Seq == 0 {
		bad("seq is required and starts at 1")
	}
	if e.TS.IsZero() {
		bad("ts is required")
	}
	if !validKinds[e.Kind] {
		bad("unknown kind %q", e.Kind)
	}
	if e.Status != "" && !validStatuses[e.Status] {
		bad("unknown status %q", e.Status)
	}

	// C5: detail is ASCII English on one line.
	if e.Detail != "" {
		if !isPrintableASCII(e.Detail) {
			bad("detail must be printable ASCII on a single line")
		}
	}
	// C3: no terminal rendering anywhere in the stream.
	for field, s := range map[string]string{"detail": e.Detail, "evidence": e.Evidence} {
		if strings.ContainsRune(s, 0x1b) {
			bad("%s contains an ANSI escape; strip control sequences at capture time", field)
		}
		if hasBlockGlyph(s) {
			bad("%s contains progress-bar glyphs; emit Progress{done,total} instead", field)
		}
	}

	if e.Progress != nil {
		switch {
		case e.Progress.Total <= 0:
			bad("progress.total must be positive")
		case e.Progress.Done < 0 || e.Progress.Done > e.Progress.Total:
			bad("progress.done %d out of range 0..%d", e.Progress.Done, e.Progress.Total)
		}
	}

	if e.Attempt < 0 || e.MaxAttempts < 0 {
		bad("attempt and maxAttempts must not be negative")
	}
	if e.MaxAttempts > 0 && e.Attempt > e.MaxAttempts {
		bad("attempt %d exceeds maxAttempts %d", e.Attempt, e.MaxAttempts)
	}

	// C4: a code must exist in the registry. A typo here surfaces months later
	// in an audit report nobody can trace.
	if e.Code != "" {
		if _, ok := codes.Lookup(e.Code); !ok {
			bad("code %q is not registered in internal/codes", e.Code)
		}
	}

	errs = append(errs, e.validateKind()...)
	return errs
}

// validateKind holds the per-kind requirements from §5.2 and §6.
func (e Event) validateKind() []error {
	var errs []error
	bad := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	switch e.Kind {
	case KindLog:
		if !validLevels[e.Level] {
			bad("kind=log requires a valid level, got %q", e.Level)
		}
		if e.Status != "" {
			bad("kind=log must not carry a status")
		}
	case KindProbe:
		if e.Code == "" {
			bad("kind=probe requires a code")
		}
	case KindDecision:
		if e.Code == "" {
			bad("kind=decision requires a code")
		}
	case KindArtifact:
		if e.Detail == "" {
			bad("kind=artifact requires detail to carry the path")
		}
	case KindRun, KindPhase, KindStep:
		if e.Status == "" {
			bad("kind=%s requires a status", e.Kind)
		}
	}

	if e.Kind != KindLog && e.Level != "" {
		bad("level applies to kind=log only, got kind=%s", e.Kind)
	}

	// §6: a failure without a code cannot be traced in the audit report and
	// cannot be translated by a renderer.
	if (e.Status == StatusFailed || e.Status == StatusBlocked) && e.Code == "" {
		bad("status=%s requires a code; add it to internal/codes first", e.Status)
	}

	if e.Kind == KindStep && e.Step == "" {
		bad("kind=step requires step")
	}
	if e.Kind == KindPhase && e.Phase == "" {
		bad("kind=phase requires phase")
	}
	return errs
}

func isPrintableASCII(s string) bool {
	for _, r := range s {
		if r > unicode.MaxASCII || r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// hasBlockGlyph reports whether s contains Block Elements (U+2580..U+259F).
// That range is where progress bars are drawn from; catching it keeps §5.3
// enforceable instead of aspirational.
func hasBlockGlyph(s string) bool {
	for _, r := range s {
		if r >= 0x2580 && r <= 0x259f {
			return true
		}
	}
	return false
}
