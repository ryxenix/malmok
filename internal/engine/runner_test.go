package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ryxenix/malmok/internal/event"
	"github.com/ryxenix/malmok/internal/state"
)

const testRun = "01JBQ8F2K3M5N7P9R1S3T5V7W9"

func fixedClock() func() time.Time {
	t := time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

// fakeStep is a Step whose behaviour a test dictates. Node access is exactly
// what the runner must not need in order to be testable, so there is none.
type fakeStep struct {
	id string

	mu       sync.Mutex
	applies  int
	observes int

	// satisfiedAfter makes Observe report satisfied once Apply has run this
	// many times. Zero means "already satisfied before we touched anything".
	satisfiedAfter int

	applyErr    error // returned by every Apply
	observeErr  error
	failApplies int // fail this many Applies, then succeed
	oneShot     bool
	maxAttempts int
}

func (s *fakeStep) ID() string { return s.id }

func (s *fakeStep) Observe(context.Context) (Observation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observes++
	if s.observeErr != nil {
		return Observation{}, s.observeErr
	}
	return Observation{
		Satisfied: s.applies >= s.satisfiedAfter,
		Detail:    fmt.Sprintf("%s observed after %d applies", s.id, s.applies),
	}, nil
}

func (s *fakeStep) Apply(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applies++
	if s.failApplies > 0 {
		s.failApplies--
		return Fail("EX-002", errors.New("transient failure"))
	}
	return s.applyErr
}

func (s *fakeStep) counts() (applies, observes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applies, s.observes
}

type oneShotStep struct{ *fakeStep }

func (s oneShotStep) OneShot() bool { return true }

type limitedStep struct {
	*fakeStep
	n int
}

func (s limitedStep) MaxAttempts() int { return s.n }

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type harness struct {
	t         *testing.T
	buf       *bytes.Buffer
	runner    *Runner
	statePath string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	buf := &bytes.Buffer{}
	dir := t.TempDir()
	st := state.New(testRun, state.Digest([]byte("cluster: test")), fixedClock()())

	return &harness{
		t:         t,
		buf:       buf,
		statePath: filepath.Join(dir, "state.json"),
		runner: &Runner{
			Events:    event.NewWriter(buf, testRun, event.WithClock(fixedClock())),
			State:     st,
			StatePath: filepath.Join(dir, "state.json"),
			Now:       fixedClock(),
			Backoff:   func(int) time.Duration { return 0 },
		},
	}
}

// resume builds a second runner over the same state file, as a restarted
// process would.
func (h *harness) resume() *harness {
	h.t.Helper()
	st, err := state.Load(h.statePath)
	if err != nil {
		h.t.Fatal(err)
	}
	buf := &bytes.Buffer{}
	return &harness{
		t: h.t, buf: buf, statePath: h.statePath,
		runner: &Runner{
			Events:    event.NewWriter(buf, testRun, event.WithClock(fixedClock())),
			State:     st,
			StatePath: h.statePath,
			Now:       fixedClock(),
			Backoff:   func(int) time.Duration { return 0 },
		},
	}
}

func (h *harness) events() []event.Event {
	h.t.Helper()
	evs, err := event.ReadAll(bytes.NewReader(h.buf.Bytes()))
	if err != nil {
		h.t.Fatalf("event stream is invalid: %v", err)
	}
	return evs
}

func (h *harness) statuses(kind event.Kind, id string) []event.Status {
	var out []event.Status
	for _, e := range h.events() {
		if e.Kind != kind {
			continue
		}
		if (kind == event.KindPhase && e.Phase == id) || (kind == event.KindStep && e.Step == id) {
			out = append(out, e.Status)
		}
	}
	return out
}

func clusterPhase(id string, steps ...Step) Phase {
	return Phase{
		ID: id, Grade: GradeAdditive, Traversal: TraversalCluster,
		Steps: func(string) []Step { return steps },
	}
}

// ---------------------------------------------------------------------------
// A. Idempotency
// ---------------------------------------------------------------------------

// A1: running the same phases twice leaves everything skipped the second time.
func TestSecondRunSkipsEverything(t *testing.T) {
	h := newHarness(t)
	s1 := &fakeStep{id: "install", satisfiedAfter: 1}
	s2 := &fakeStep{id: "configure", satisfiedAfter: 1}
	phases := []Phase{clusterPhase("l0-node-prep", s1, s2)}

	if err := h.runner.Run(context.Background(), phases); err != nil {
		t.Fatalf("first run: %v", err)
	}
	appliesAfterFirst, _ := s1.counts()
	if appliesAfterFirst != 1 {
		t.Fatalf("first run applied %d times, want 1", appliesAfterFirst)
	}

	second := h.resume()
	if err := second.runner.Run(context.Background(), phases); err != nil {
		t.Fatalf("second run: %v", err)
	}

	if applies, _ := s1.counts(); applies != 1 {
		t.Errorf("second run applied again: %d applies total", applies)
	}
	for _, st := range second.statuses(event.KindPhase, "l0-node-prep") {
		if st == event.StatusRunning {
			t.Error("a completed phase was re-entered on the second run")
		}
	}
}

// A step whose target state already holds is skipped without applying.
func TestAlreadySatisfiedStepIsSkipped(t *testing.T) {
	h := newHarness(t)
	s := &fakeStep{id: "install", satisfiedAfter: 0}

	if err := h.runner.Run(context.Background(), []Phase{clusterPhase("l0", s)}); err != nil {
		t.Fatal(err)
	}
	if applies, _ := s.counts(); applies != 0 {
		t.Errorf("Apply ran %d times on an already-satisfied step", applies)
	}
	got := h.statuses(event.KindStep, "install")
	if len(got) == 0 || got[len(got)-1] != event.StatusSkipped {
		t.Errorf("step statuses = %v, want the last to be skipped", got)
	}
}

// A2: recheck re-observes completed steps and changes nothing.
func TestRecheckReobservesWithoutApplying(t *testing.T) {
	h := newHarness(t)
	s := &fakeStep{id: "install", satisfiedAfter: 1}
	phases := []Phase{clusterPhase("l0", s)}

	if err := h.runner.Run(context.Background(), phases); err != nil {
		t.Fatal(err)
	}
	applies, observes := s.counts()

	second := h.resume()
	second.runner.Resume = state.Options{Recheck: true}
	if err := second.runner.Run(context.Background(), phases); err != nil {
		t.Fatal(err)
	}

	applies2, observes2 := s.counts()
	if applies2 != applies {
		t.Errorf("recheck applied again: %d -> %d", applies, applies2)
	}
	if observes2 <= observes {
		t.Error("recheck did not re-observe")
	}
}

// §3.2 rule 3: an Apply that returns nil but does not reach the target state is
// a failure, not a success.
func TestApplyWithoutReachingTargetStateFails(t *testing.T) {
	h := newHarness(t)
	// Never becomes satisfied, yet Apply always returns nil.
	s := &fakeStep{id: "install", satisfiedAfter: 99}

	err := h.runner.Run(context.Background(), []Phase{clusterPhase("l0", s)})
	if err == nil {
		t.Fatal("expected the run to fail")
	}
	var halted *ErrHalted
	if !errors.As(err, &halted) {
		t.Fatalf("expected ErrHalted, got %T", err)
	}
	if halted.Code != "EX-003" {
		t.Errorf("code = %s, want EX-003", halted.Code)
	}
}

// ---------------------------------------------------------------------------
// B. Resume
// ---------------------------------------------------------------------------

// B1: work already done is not repeated after an interruption.
func TestResumeSkipsCompletedSteps(t *testing.T) {
	h := newHarness(t)
	first := &fakeStep{id: "install", satisfiedAfter: 1}
	second := &fakeStep{id: "configure", satisfiedAfter: 1, applyErr: FailFatal("EX-002", errors.New("boom"))}

	phases := []Phase{clusterPhase("l0", first, second)}
	if err := h.runner.Run(context.Background(), phases); err == nil {
		t.Fatal("expected the first run to fail")
	}

	// Repair the second step and resume.
	second.applyErr = nil
	second.satisfiedAfter = 1

	resumed := h.resume()
	if err := resumed.runner.Run(context.Background(), phases); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if applies, _ := first.counts(); applies != 1 {
		t.Errorf("the completed step was applied again: %d applies", applies)
	}
}

// B4: a sequential traversal stops where it failed and never touches the nodes
// after it.
func TestSequentialTraversalStopsAtFailure(t *testing.T) {
	h := newHarness(t)
	nodes := []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}
	perNode := map[string]*fakeStep{}
	for _, n := range nodes {
		perNode[n] = &fakeStep{id: "join", satisfiedAfter: 1}
	}
	perNode["10.0.0.2"].applyErr = FailFatal("EX-002", errors.New("node refused"))
	perNode["10.0.0.2"].satisfiedAfter = 99

	phase := Phase{
		ID: "l1-join-agent", Grade: GradeMutating, Traversal: TraversalSequential,
		Nodes: nodes,
		Steps: func(node string) []Step { return []Step{perNode[node]} },
	}

	if err := h.runner.Run(context.Background(), []Phase{phase}); err == nil {
		t.Fatal("expected the run to fail")
	}

	if applies, _ := perNode["10.0.0.1"].counts(); applies != 1 {
		t.Errorf("first node applied %d times, want 1", applies)
	}
	if applies, observes := perNode["10.0.0.3"].counts(); applies != 0 || observes != 0 {
		t.Errorf("third node was touched: %d applies, %d observes", applies, observes)
	}
	if _, found := h.runner.State.StepState("l1-join-agent", "join", "10.0.0.3"); found {
		t.Error("the unattempted node has a state record; it must have none")
	}
	if st := h.runner.State.NodeStatus("l1-join-agent"); st["10.0.0.2"] != event.StatusFailed {
		t.Errorf("failed node status = %s, want failed", st["10.0.0.2"])
	}
}

// B5: a one-shot step left failed is not re-run on resume; the runner stops and
// asks.
func TestOneShotNeedsConfirmationOnResume(t *testing.T) {
	h := newHarness(t)
	inner := &fakeStep{id: "restore-etcd", satisfiedAfter: 99,
		applyErr: FailFatal("EX-002", errors.New("restore failed"))}
	s := oneShotStep{inner}

	phases := []Phase{clusterPhase("l2-pki", s)}
	if err := h.runner.Run(context.Background(), phases); err == nil {
		t.Fatal("expected the first run to fail")
	}
	appliesAfterFirst, _ := inner.counts()

	resumed := h.resume()
	err := resumed.runner.Run(context.Background(), phases)

	var confirm *ErrNeedsConfirmation
	if !errors.As(err, &confirm) {
		t.Fatalf("expected ErrNeedsConfirmation, got %T: %v", err, err)
	}
	if applies, _ := inner.counts(); applies != appliesAfterFirst {
		t.Errorf("the one-shot step was re-run: %d -> %d applies", appliesAfterFirst, applies)
	}

	var blocked bool
	for _, e := range resumed.events() {
		if e.Kind == event.KindStep && e.Status == event.StatusBlocked && e.Code == "EX-005" {
			blocked = true
		}
	}
	if !blocked {
		t.Error("no EX-005 blocked event was emitted")
	}
}

// ---------------------------------------------------------------------------
// Retry and halting
// ---------------------------------------------------------------------------

func TestRetriesThenSucceeds(t *testing.T) {
	h := newHarness(t)
	s := &fakeStep{id: "install", satisfiedAfter: 3, failApplies: 2}

	if err := h.runner.Run(context.Background(), []Phase{clusterPhase("l0", s)}); err != nil {
		t.Fatalf("expected success within the retry budget, got %v", err)
	}
	if applies, _ := s.counts(); applies != 3 {
		t.Errorf("applied %d times, want 3", applies)
	}
}

func TestFatalErrorIsNotRetried(t *testing.T) {
	h := newHarness(t)
	s := &fakeStep{id: "install", satisfiedAfter: 99,
		applyErr: FailFatal("EX-002", errors.New("permanently broken"))}

	if err := h.runner.Run(context.Background(), []Phase{clusterPhase("l0", s)}); err == nil {
		t.Fatal("expected failure")
	}
	if applies, _ := s.counts(); applies != 1 {
		t.Errorf("a fatal error was retried: %d applies", applies)
	}
}

func TestRetryBudgetIsExhausted(t *testing.T) {
	h := newHarness(t)
	inner := &fakeStep{id: "install", satisfiedAfter: 99, failApplies: 99}
	s := limitedStep{inner, 2}

	if err := h.runner.Run(context.Background(), []Phase{clusterPhase("l0", s)}); err == nil {
		t.Fatal("expected failure")
	}
	if applies, _ := inner.counts(); applies != 2 {
		t.Errorf("applied %d times, want the 2 allowed by MaxAttempts", applies)
	}
}

// An unobservable step is fatal: without observation nothing about it can be
// decided, including whether resuming is safe (§3.2).
func TestObserveErrorIsFatal(t *testing.T) {
	h := newHarness(t)
	s := &fakeStep{id: "install", observeErr: errors.New("ssh closed")}

	err := h.runner.Run(context.Background(), []Phase{clusterPhase("l0", s)})
	var halted *ErrHalted
	if !errors.As(err, &halted) {
		t.Fatalf("expected ErrHalted, got %T", err)
	}
	if halted.Code != "EX-001" {
		t.Errorf("code = %s, want EX-001", halted.Code)
	}
	if _, observes := s.counts(); observes != 1 {
		t.Errorf("observed %d times; an observation failure must not be retried", observes)
	}
}

// §6: a failed phase stops the run and later phases are not entered.
func TestLaterPhasesAreNotEntered(t *testing.T) {
	h := newHarness(t)
	bad := &fakeStep{id: "boom", satisfiedAfter: 99,
		applyErr: FailFatal("EX-002", errors.New("nope"))}
	later := &fakeStep{id: "later", satisfiedAfter: 1}

	err := h.runner.Run(context.Background(), []Phase{
		clusterPhase("l1-bootstrap", bad),
		clusterPhase("l2-dataplane", later),
	})
	if err == nil {
		t.Fatal("expected failure")
	}
	if applies, observes := later.counts(); applies != 0 || observes != 0 {
		t.Errorf("a later phase ran: %d applies, %d observes", applies, observes)
	}

	// The renderer still learns the phase exists, as pending.
	got := h.statuses(event.KindPhase, "l2-dataplane")
	if len(got) != 1 || got[0] != event.StatusPending {
		t.Errorf("l2-dataplane statuses = %v, want exactly [pending]", got)
	}
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

// Everything the runner emits must satisfy the schema, including the rule that
// a failure carries a registered code (§6).
func TestEveryEmittedEventIsValid(t *testing.T) {
	h := newHarness(t)
	bad := &fakeStep{id: "boom", satisfiedAfter: 99, failApplies: 99}

	_ = h.runner.Run(context.Background(), []Phase{
		clusterPhase("l0", &fakeStep{id: "ok-step", satisfiedAfter: 1}),
		clusterPhase("l1", bad),
	})

	events := h.events()
	if len(events) == 0 {
		t.Fatal("no events were emitted")
	}
	for _, e := range events {
		if errs := e.Validate(); len(errs) > 0 {
			t.Errorf("seq %d is invalid: %v", e.Seq, errors.Join(errs...))
		}
		if (e.Status == event.StatusFailed || e.Status == event.StatusBlocked) && e.Code == "" {
			t.Errorf("seq %d failed without a code", e.Seq)
		}
	}
}

func TestPhaseTreeIsAnnouncedUpFront(t *testing.T) {
	h := newHarness(t)
	phases := []Phase{
		clusterPhase("preflight", &fakeStep{id: "probe", satisfiedAfter: 1}),
		clusterPhase("plan", &fakeStep{id: "resolve", satisfiedAfter: 1}),
		clusterPhase("l0-node-prep", &fakeStep{id: "sysctl", satisfiedAfter: 1}),
	}
	if err := h.runner.Run(context.Background(), phases); err != nil {
		t.Fatal(err)
	}

	// The first four events are the run start plus one pending row per phase,
	// so a renderer can draw the whole tree before anything happens.
	events := h.events()
	if events[0].Kind != event.KindRun {
		t.Fatalf("first event is %s, want run", events[0].Kind)
	}
	for i, want := range []string{"preflight", "plan", "l0-node-prep"} {
		e := events[i+1]
		if e.Kind != event.KindPhase || e.Phase != want || e.Status != event.StatusPending {
			t.Errorf("event %d = %s/%s/%s, want phase/%s/pending", i+1, e.Kind, e.Phase, e.Status, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Traversal and cancellation
// ---------------------------------------------------------------------------

func TestParallelTraversalVisitsEveryNode(t *testing.T) {
	h := newHarness(t)
	nodes := []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}
	steps := map[string]*fakeStep{}
	for _, n := range nodes {
		steps[n] = &fakeStep{id: "prep", satisfiedAfter: 1}
	}

	phase := Phase{
		ID: "l0-node-prep", Grade: GradeMutating, Traversal: TraversalParallel,
		Nodes: nodes,
		Steps: func(node string) []Step { return []Step{steps[node]} },
	}
	if err := h.runner.Run(context.Background(), []Phase{phase}); err != nil {
		t.Fatal(err)
	}
	for n, s := range steps {
		if applies, _ := s.counts(); applies != 1 {
			t.Errorf("node %s applied %d times, want 1", n, applies)
		}
	}
	if got := len(h.runner.State.Nodes("l0-node-prep")); got != 3 {
		t.Errorf("recorded %d nodes, want 3", got)
	}
}

func TestCancellationStopsTheRun(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := h.runner.Run(ctx, []Phase{
		clusterPhase("l0", &fakeStep{id: "install", satisfiedAfter: 1}),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	var cancelled bool
	for _, e := range h.events() {
		if e.Kind == event.KindRun && e.Code == "EX-104" {
			cancelled = true
		}
	}
	if !cancelled {
		t.Error("no EX-104 event was emitted for the cancelled run")
	}
}
