// Package engine runs phases. It is the only place that changes a cluster.
//
// ARCHITECTURAL CONTRACT
//
//	internal/engine MUST NOT import internal/tui. The engine emits events and
//	knows nothing about rendering (ADR-002); a renderer subscribes to the
//	stream. Any code path here that needs a terminal is a defect.
//
// Everything the runner does is driven by docs/11-execute.md: phases from §2,
// the idempotency contract from §3, resume from §4, events from §5 and retry
// policy from §6.
package engine

import (
	"context"
	"errors"
	"fmt"
)

// Observation is what a step sees when it looks at the world.
//
// It merges the document's Observe/Satisfied pair into one return value. Keeping
// them separate would need a per-step state type, which makes a heterogeneous
// []Step impossible without `any` — and the contract is the same either way:
// look without changing anything, then say whether the target state holds.
type Observation struct {
	// Satisfied means the target state already holds and Apply must not run.
	Satisfied bool

	// Detail is one English line for the event stream.
	Detail string

	// Evidence is raw output kept for the audit report. Strip terminal control
	// sequences here: event.Validate rejects them (§5.3).
	Evidence string
}

// Step is one unit of work. See docs/11-execute.md §3.1.
//
// A step that cannot be observed is a defect, not an inconvenience. Resume
// depends on it: a process killed mid-step leaves a record saying `running`,
// and the only way back is to look at the world again (§4.2 rule 4). This is
// why `curl -sfL https://get.rke2.io | sh` cannot be a step — it has to be
// restated as an observable target such as "this unit exists at this version".
type Step interface {
	ID() string

	// Observe reports the current state. It MUST NOT change anything.
	Observe(context.Context) (Observation, error)

	// Apply moves toward the target state. The runner calls it only when
	// Observe reported Satisfied == false, and re-observes afterwards.
	Apply(context.Context) error
}

// OneShotStep marks work that cannot honestly claim idempotency — an etcd
// restore, a CA replacement (§3.3). The runner never re-runs one on its own;
// left in a non-successful state it stops and asks.
type OneShotStep interface {
	Step
	OneShot() bool
}

// RetryableStep overrides the runner's default attempt budget.
type RetryableStep interface {
	Step
	MaxAttempts() int
}

func isOneShot(s Step) bool {
	o, ok := s.(OneShotStep)
	return ok && o.OneShot()
}

func maxAttempts(s Step, def int) int {
	if r, ok := s.(RetryableStep); ok {
		if n := r.MaxAttempts(); n > 0 {
			return n
		}
	}
	return def
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

// Error is a step failure carrying the diagnostic code that will appear in the
// event stream and the audit report.
//
// §6 requires a code on every failure event. A step that knows a more specific
// one should supply it; the runner falls back to EX-002 when it does not, which
// means "this failed and nobody said why more precisely".
type Error struct {
	// Code must be registered in internal/codes.
	Code string

	// Fatal suppresses retries. Use it when the situation cannot change by
	// trying again — a block-severity probe result, a failed verification.
	Fatal bool

	// Evidence is the raw output behind the failure, kept whole for the run's
	// event file while the detail line stays readable.
	Evidence string

	Err error
}

func (e *Error) Error() string {
	if e.Code == "" {
		return e.Err.Error()
	}
	return fmt.Sprintf("%s: %v", e.Code, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// evidenceOf returns the raw output a failure carried, if any.
func evidenceOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Evidence
	}
	return ""
}

// Fail returns a retryable step failure carrying code.
func Fail(code string, err error) error { return &Error{Code: code, Err: err} }

// FailWith is Fail with the raw output attached. The detail line is clipped
// for readability, and the clip used to promise "the full output is in the
// run's event file" while attaching nothing -- so the one place an operator
// was told to look was the one place it was not. Evidence is that file.
func FailWith(code, evidence string, err error) error {
	return &Error{Code: code, Evidence: evidence, Err: err}
}

// FailFatal returns a step failure that must not be retried.
func FailFatal(code string, err error) error { return &Error{Code: code, Fatal: true, Err: err} }

// codeOf extracts the diagnostic code from err, falling back to def.
func codeOf(err error, def string) string {
	var e *Error
	if errors.As(err, &e) && e.Code != "" {
		return e.Code
	}
	return def
}

// isFatal reports whether err forbids retrying.
func isFatal(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Fatal
}
