package state

import (
	"fmt"

	"platform.ryxen.dev/malmok/internal/event"
)

// Action is what resume decides to do with one step. See docs/11-execute.md §4.2.
type Action string

const (
	// ActionRun executes the step. Idempotency means this is always safe; a
	// step that is not safe to run is marked OneShot instead.
	ActionRun Action = "run"

	// ActionSkip leaves the step alone and emits StatusSkipped.
	ActionSkip Action = "skip"

	// ActionConfirm stops and asks. Reserved for OneShot work left in a
	// non-successful state, where re-running is neither obviously safe nor
	// obviously wrong (§3.3).
	ActionConfirm Action = "confirm"
)

// Options tunes the resume decision.
type Options struct {
	// Recheck re-observes steps already recorded as done. Slower, and the
	// idempotency contract says the outcome must be identical -- which is what
	// makes it a useful audit of that contract rather than busywork.
	Recheck bool
}

// Decide answers whether a step should run, given what the state file records.
//
// The reason string is written into the skip event, so an operator reading the
// stream can tell "we did this last time" from "nothing to do".
func (s *State) Decide(phase, step, node string, opts Options) (Action, string) {
	rec, found := s.StepState(phase, step, node)
	if !found {
		return ActionRun, "no record of this step in the current run"
	}

	// A step still marked running belonged to a process that died. Where it
	// died is unknowable, so its own claim about itself is worthless and the
	// step is observed again from scratch (§4.2 rule 4).
	//
	// This is why an unobservable step is a defect (§3.2 rule 1): without
	// Observe there is no way to recover from this state at all.
	if rec.Status == event.StatusRunning {
		if rec.OneShot {
			return ActionConfirm, "one-shot step was interrupted mid-execution; re-running it is not automatically safe"
		}
		return ActionRun, "step was interrupted while running; observing again"
	}

	switch rec.Status {
	case event.StatusOK, event.StatusSkipped:
		if rec.OneShot {
			return ActionSkip, "one-shot step already completed"
		}
		if opts.Recheck {
			return ActionRun, "recheck requested; re-observing a completed step"
		}
		return ActionSkip, "already satisfied in a previous attempt"

	case event.StatusFailed, event.StatusBlocked:
		if rec.OneShot {
			return ActionConfirm, "one-shot step failed previously; re-running it is not automatically safe"
		}
		return ActionRun, "previous attempt failed"

	default: // pending, or an unrecognised value written by a newer version
		return ActionRun, "step has not completed"
	}
}

// ---------------------------------------------------------------------------
// Resumability
// ---------------------------------------------------------------------------

// ErrSpecChanged reports that the cluster.yaml behind a run no longer matches
// the one the run started from.
type ErrSpecChanged struct {
	Recorded string
	Current  string
}

func (e *ErrSpecChanged) Error() string {
	return fmt.Sprintf(
		"state: cluster.yaml changed since this run started (recorded %s, current %s); "+
			"start a new run, or pass --force-resume to continue against the recorded spec",
		short(e.Recorded), short(e.Current))
}

func short(digest string) string {
	if len(digest) > 19 { // "sha256:" + 12 hex chars
		return digest[:19]
	}
	return digest
}

// CheckResumable reports whether this run may continue against the given spec.
//
// Refusing by default is the point. A resumed run whose input has changed
// produces a cluster that matches neither the recorded spec nor the current
// one, and the state file then misdescribes what is actually deployed -- the
// exact class of failure ADR-001 rejected a second state store to avoid.
func (s *State) CheckResumable(specDigest string, force bool) error {
	s.mu.RLock()
	recorded := s.SpecDigest
	s.mu.RUnlock()

	if recorded == specDigest || force {
		return nil
	}
	return &ErrSpecChanged{Recorded: recorded, Current: specDigest}
}

// PendingPhases returns the phases from order that still need work, preserving
// the caller's ordering.
//
// A phase is done only when it is ok or skipped. Everything else -- pending,
// running, failed -- is work, because a phase left running was interrupted and
// a failed phase blocks the ones after it (§6).
func (s *State) PendingPhases(order []string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []string
	for _, name := range order {
		p, ok := s.Phases[name]
		if !ok || (p.Status != event.StatusOK && p.Status != event.StatusSkipped) {
			out = append(out, name)
		}
	}
	return out
}

// ResumePoint names the first phase that still needs work, and the step within
// it that failed if there was one.
func (s *State) ResumePoint(order []string) (phase, step string, ok bool) {
	pending := s.PendingPhases(order)
	if len(pending) == 0 {
		return "", "", false
	}
	phase = pending[0]

	s.mu.RLock()
	defer s.mu.RUnlock()
	if p := s.Phases[phase]; p != nil {
		step = p.FailedStep
	}
	return phase, step, true
}
