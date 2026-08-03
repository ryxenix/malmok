package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"platform.ryxen.dev/platformctl/internal/event"
	"platform.ryxen.dev/platformctl/internal/state"
)

// DefaultMaxAttempts is the retry budget from docs/11-execute.md §6.
const DefaultMaxAttempts = 3

// Runner executes phases, emits events and records progress.
//
// It is deliberately ignorant of what a step does. Ansible, Helm and kubectl
// all arrive as Step implementations; the runner only knows the contract —
// observe, apply, observe again — and that is what makes §7's A and B criteria
// testable without a cluster.
type Runner struct {
	// Events receives every emission. Required.
	Events *event.Writer

	// State records progress. Required.
	State *state.State

	// StatePath persists State after each step. Empty keeps it in memory,
	// which is only useful in tests: without a file there is nothing to
	// resume from.
	StatePath string

	// Resume controls how already-recorded steps are treated (§4.2).
	Resume state.Options

	// Now defaults to time.Now.
	Now func() time.Time

	// Backoff is the delay before attempt n (n starting at 2). Defaults to
	// exponential from one second.
	Backoff func(attempt int) time.Duration

	// MaxAttempts is the default retry budget for steps that do not set one.
	MaxAttempts int
}

// ErrHalted reports that the run stopped at a failure. The cause is wrapped.
type ErrHalted struct {
	Phase string
	Node  string
	Step  string
	Code  string
	Err   error
}

func (e *ErrHalted) Error() string {
	where := e.Phase
	if e.Step != "" {
		where += "/" + e.Step
	}
	if e.Node != "" {
		where += " on " + e.Node
	}
	return fmt.Sprintf("%s halted at %s: %v", e.Code, where, e.Err)
}

func (e *ErrHalted) Unwrap() error { return e.Err }

// ErrNeedsConfirmation reports a one-shot step the runner refuses to decide
// about on its own (§3.3, §4.2).
type ErrNeedsConfirmation struct {
	Phase  string
	Node   string
	Step   string
	Reason string
}

func (e *ErrNeedsConfirmation) Error() string {
	return fmt.Sprintf("EX-005 %s/%s needs confirmation: %s", e.Phase, e.Step, e.Reason)
}

// ---------------------------------------------------------------------------
// Run
// ---------------------------------------------------------------------------

// Run executes phases in order and stops at the first failure.
//
// Later phases are not entered (§6). This is not conservatism for its own sake:
// installing L2 add-ons onto a control plane that never came up produces a
// second failure that hides the first.
func (r *Runner) Run(ctx context.Context, phases []Phase) error {
	r.applyDefaults()

	if _, err := r.emit(event.Event{
		Kind: event.KindRun, Status: event.StatusRunning,
		Detail: fmt.Sprintf("run started with %d phases", len(phases)),
	}); err != nil {
		return err
	}

	// Announce the whole tree up front so a renderer can draw it immediately
	// instead of growing it a line at a time (the "L2 addons  pending" row in
	// the ADR-002 mockup).
	for _, p := range phases {
		if _, err := r.emit(event.Event{
			Kind: event.KindPhase, Phase: p.ID, Status: event.StatusPending,
		}); err != nil {
			return err
		}
	}

	for _, p := range phases {
		err := r.runPhase(ctx, p)
		if err == nil {
			continue
		}
		return r.finish(err)
	}
	return r.finish(nil)
}

// finish emits the terminal run event and returns the run's error.
func (r *Runner) finish(cause error) error {
	ev := event.Event{Kind: event.KindRun, Status: event.StatusOK, Detail: "run completed"}

	switch {
	case cause == nil:
	case errors.Is(cause, context.Canceled), errors.Is(cause, context.DeadlineExceeded):
		ev.Status, ev.Code, ev.Detail = event.StatusFailed, "EX-104", "run cancelled"
	default:
		ev.Status, ev.Code, ev.Detail = event.StatusFailed, "EX-103", "run halted; later phases were not entered"
	}

	if _, err := r.emit(ev); err != nil && cause == nil {
		return err
	}
	if err := r.save(); err != nil && cause == nil {
		return err
	}
	return cause
}

// ---------------------------------------------------------------------------
// Phase
// ---------------------------------------------------------------------------

func (r *Runner) runPhase(ctx context.Context, p Phase) error {
	// A phase already finished in a previous attempt is not re-entered.
	if rec, found := r.State.PhaseState(p.ID); found &&
		(rec.Status == event.StatusOK || rec.Status == event.StatusSkipped) &&
		!r.Resume.Recheck {
		_, err := r.emit(event.Event{
			Kind: event.KindPhase, Phase: p.ID, Status: event.StatusSkipped,
			Detail: "phase completed in a previous attempt",
		})
		return err
	}

	if _, err := r.emit(event.Event{
		Kind: event.KindPhase, Phase: p.ID, Status: event.StatusRunning,
		Detail: fmt.Sprintf("grade=%s traversal=%s", p.Grade, p.Traversal),
	}); err != nil {
		return err
	}
	r.State.SetPhase(p.ID, event.StatusRunning, r.Now())

	var err error
	if p.Traversal == TraversalParallel {
		err = r.runNodesParallel(ctx, p)
	} else {
		err = r.runNodesSequential(ctx, p)
	}

	if err != nil {
		r.State.SetPhase(p.ID, event.StatusFailed, r.Now())
		code := "EX-101"
		if p.Traversal != TraversalCluster {
			code = "EX-102"
		}
		if _, e := r.emit(event.Event{
			Kind: event.KindPhase, Phase: p.ID, Status: event.StatusFailed, Code: code,
		}); e != nil {
			return e
		}
		return err
	}

	r.State.SetPhase(p.ID, event.StatusOK, r.Now())
	if _, e := r.emit(event.Event{
		Kind: event.KindPhase, Phase: p.ID, Status: event.StatusOK,
	}); e != nil {
		return e
	}
	return r.save()
}

// runNodesSequential visits one node at a time and returns at the first
// failure, leaving the remaining nodes with no record at all (§4.3, criterion
// B4).
func (r *Runner) runNodesSequential(ctx context.Context, p Phase) error {
	for _, node := range p.nodeTargets() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.runSteps(ctx, p, node); err != nil {
			return err
		}
	}
	return nil
}

// runNodesParallel visits every node at once, cancelling the rest on the first
// failure so nodes that have not started do not start.
func (r *Runner) runNodesParallel(ctx context.Context, p Phase) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
	)
	for _, node := range p.nodeTargets() {
		wg.Add(1)
		go func(node string) {
			defer wg.Done()
			if err := r.runSteps(ctx, p, node); err != nil {
				mu.Lock()
				if first == nil {
					first = err
					cancel()
				}
				mu.Unlock()
			}
		}(node)
	}
	wg.Wait()
	return first
}

// ---------------------------------------------------------------------------
// Step
// ---------------------------------------------------------------------------

func (r *Runner) runSteps(ctx context.Context, p Phase, node string) error {
	steps := p.stepsFor(node)
	for i, s := range steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		progress := &event.Progress{Done: i, Total: len(steps)}
		if err := r.runStep(ctx, p, node, s, progress); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) runStep(ctx context.Context, p Phase, node string, s Step, progress *event.Progress) error {
	base := event.Event{
		Kind: event.KindStep, Phase: p.ID, Step: s.ID(), Node: node, Progress: progress,
	}

	switch action, reason := r.State.Decide(p.ID, s.ID(), node, r.Resume); action {
	case state.ActionSkip:
		r.State.SetStep(p.ID, s.ID(), node,
			state.Step{Status: event.StatusSkipped, OneShot: isOneShot(s)}, r.Now())
		ev := base
		ev.Status, ev.Detail = event.StatusSkipped, reason
		if _, err := r.emit(ev); err != nil {
			return err
		}
		return r.save()

	case state.ActionConfirm:
		ev := base
		ev.Status, ev.Code, ev.Detail = event.StatusBlocked, "EX-005", reason
		if _, err := r.emit(ev); err != nil {
			return err
		}
		return &ErrNeedsConfirmation{Phase: p.ID, Node: node, Step: s.ID(), Reason: reason}
	}

	budget := maxAttempts(s, r.MaxAttempts)
	var last error

	for attempt := 1; attempt <= budget; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(r.Backoff(attempt)):
			}
		}

		r.State.SetStep(p.ID, s.ID(), node,
			state.Step{Status: event.StatusRunning, Attempt: attempt, OneShot: isOneShot(s)}, r.Now())
		if err := r.save(); err != nil {
			return err
		}

		ev := base
		ev.Status, ev.Attempt, ev.MaxAttempts = event.StatusRunning, attempt, budget
		if _, err := r.emit(ev); err != nil {
			return err
		}

		done, err := r.attempt(ctx, p, node, s, base, attempt, budget)
		if done {
			return nil
		}
		last = err
		if err == nil || isFatal(err) || ctx.Err() != nil {
			break
		}
	}

	if last == nil {
		last = errors.New("step did not complete")
	}
	code := codeOf(last, "EX-004")
	r.State.SetStep(p.ID, s.ID(), node,
		state.Step{Status: event.StatusFailed, Attempt: budget, Code: code, OneShot: isOneShot(s)}, r.Now())
	if err := r.save(); err != nil {
		return err
	}

	ev := base
	ev.Status, ev.Code, ev.Attempt, ev.MaxAttempts = event.StatusFailed, code, budget, budget
	ev.Detail = last.Error()
	if _, err := r.emit(ev); err != nil {
		return err
	}
	return &ErrHalted{Phase: p.ID, Node: node, Step: s.ID(), Code: code, Err: last}
}

// attempt runs one observe / apply / re-observe cycle.
//
// The re-observation is the point of the whole contract. A command's exit code
// is not evidence that the target state holds (§3.2 rule 3), and every "the
// script said it worked" incident starts by believing it.
func (r *Runner) attempt(
	ctx context.Context, p Phase, node string, s Step,
	base event.Event, attempt, budget int,
) (done bool, err error) {
	obs, err := s.Observe(ctx)
	if err != nil {
		return false, &Error{Code: codeOf(err, "EX-001"), Fatal: true, Err: err}
	}
	if obs.Satisfied {
		r.State.SetStep(p.ID, s.ID(), node,
			state.Step{Status: event.StatusSkipped, Attempt: attempt, OneShot: isOneShot(s)}, r.Now())
		ev := base
		ev.Status, ev.Detail, ev.Evidence = event.StatusSkipped, observedDetail(obs), obs.Evidence
		if _, e := r.emit(ev); e != nil {
			return false, e
		}
		return true, r.save()
	}

	if err := s.Apply(ctx); err != nil {
		return false, err
	}

	after, err := s.Observe(ctx)
	if err != nil {
		return false, &Error{Code: codeOf(err, "EX-001"), Fatal: true, Err: err}
	}
	if !after.Satisfied {
		return false, &Error{
			Code: "EX-003",
			Err:  errors.New("applied without error but the target state was not reached"),
		}
	}

	r.State.SetStep(p.ID, s.ID(), node,
		state.Step{Status: event.StatusOK, Attempt: attempt, OneShot: isOneShot(s)}, r.Now())
	ev := base
	ev.Status, ev.Attempt, ev.MaxAttempts = event.StatusOK, attempt, budget
	ev.Detail, ev.Evidence = after.Detail, after.Evidence
	if _, e := r.emit(ev); e != nil {
		return false, e
	}
	return true, r.save()
}

func observedDetail(o Observation) string {
	if o.Detail != "" {
		return o.Detail
	}
	return "already satisfied"
}

// ---------------------------------------------------------------------------
// Plumbing
// ---------------------------------------------------------------------------

func (r *Runner) applyDefaults() {
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.MaxAttempts <= 0 {
		r.MaxAttempts = DefaultMaxAttempts
	}
	if r.Backoff == nil {
		r.Backoff = func(attempt int) time.Duration {
			return time.Duration(1<<uint(attempt-2)) * time.Second
		}
	}
}

func (r *Runner) emit(e event.Event) (event.Event, error) {
	return r.Events.Emit(e)
}

func (r *Runner) save() error {
	if r.StatePath == "" {
		return nil
	}
	return r.State.Save(r.StatePath)
}
