package state

import (
	"errors"
	"strings"
	"testing"

	"platform.ryxen.dev/platformctl/internal/event"
)

// phaseOrder is the §2 catalogue, abbreviated to the phases these tests touch.
var phaseOrder = []string{
	"preflight", "plan", "l0-node-prep", "l1-bootstrap",
	"l1-join-agent", "l2-dataplane", "verify", "report",
}

// TestDecide covers docs/11-execute.md §4.2 rule by rule.
func TestDecide(t *testing.T) {
	tests := []struct {
		name       string
		record     *Step // nil means no record at all
		opts       Options
		want       Action
		wantReason string
	}{
		{
			name: "no record runs",
			want: ActionRun, wantReason: "no record",
		},
		{
			name:   "completed step is skipped",
			record: &Step{Status: event.StatusOK},
			want:   ActionSkip, wantReason: "already satisfied",
		},
		{
			name:   "skipped step stays skipped",
			record: &Step{Status: event.StatusSkipped},
			want:   ActionSkip,
		},
		{
			// A2: recheck re-observes, and the idempotency contract says the
			// outcome must be identical.
			name:   "recheck re-observes a completed step",
			record: &Step{Status: event.StatusOK},
			opts:   Options{Recheck: true},
			want:   ActionRun, wantReason: "recheck requested",
		},
		{
			// B2: the process died at an unknown point, so the step's own
			// claim about itself is worthless.
			name:   "interrupted step is observed again",
			record: &Step{Status: event.StatusRunning},
			want:   ActionRun, wantReason: "interrupted while running",
		},
		{
			name:   "failed step is retried",
			record: &Step{Status: event.StatusFailed, Code: "PF-601"},
			want:   ActionRun, wantReason: "previous attempt failed",
		},
		{
			name:   "blocked step is retried by the engine, not skipped",
			record: &Step{Status: event.StatusBlocked, Code: "PF-802"},
			want:   ActionRun,
		},
		{
			name:   "pending step runs",
			record: &Step{Status: event.StatusPending},
			want:   ActionRun,
		},

		// B5: one-shot work is never re-run on its own.
		{
			name:   "completed one-shot is skipped",
			record: &Step{Status: event.StatusOK, OneShot: true},
			want:   ActionSkip, wantReason: "one-shot step already completed",
		},
		{
			name:   "completed one-shot ignores recheck",
			record: &Step{Status: event.StatusOK, OneShot: true},
			opts:   Options{Recheck: true},
			want:   ActionSkip,
		},
		{
			name:   "failed one-shot needs confirmation",
			record: &Step{Status: event.StatusFailed, OneShot: true, Code: "PF-601"},
			want:   ActionConfirm, wantReason: "not automatically safe",
		},
		{
			name:   "interrupted one-shot needs confirmation",
			record: &Step{Status: event.StatusRunning, OneShot: true},
			want:   ActionConfirm, wantReason: "not automatically safe",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newState()
			if tc.record != nil {
				s.SetStep("l2-pki", "restore-ca", "", *tc.record, at(1))
			}
			got, reason := s.Decide("l2-pki", "restore-ca", "", tc.opts)
			if got != tc.want {
				t.Errorf("Decide = %s (%s), want %s", got, reason, tc.want)
			}
			if tc.wantReason != "" && !strings.Contains(reason, tc.wantReason) {
				t.Errorf("reason %q does not mention %q", reason, tc.wantReason)
			}
		})
	}
}

// B1: killing the engine mid-step must not cost the work already done.
func TestResumeDoesNotRerunCompletedSteps(t *testing.T) {
	path := Path(t.TempDir())

	live := newState()
	ok(live, "l0-node-prep", "sysctl", "10.10.0.11", 1)
	ok(live, "l0-node-prep", "trust-store", "10.10.0.11", 2)
	live.SetStep("l0-node-prep", "containerd-registries", "10.10.0.11",
		Step{Status: event.StatusRunning}, at(3))
	if err := live.Save(path); err != nil {
		t.Fatal(err)
	}

	// ... the process is killed here; nothing more is written.

	resumed, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	for _, step := range []string{"sysctl", "trust-store"} {
		if act, reason := resumed.Decide("l0-node-prep", step, "10.10.0.11", Options{}); act != ActionSkip {
			t.Errorf("%s would be re-run after resume: %s (%s)", step, act, reason)
		}
	}
	if act, _ := resumed.Decide("l0-node-prep", "containerd-registries", "10.10.0.11", Options{}); act != ActionRun {
		t.Errorf("the interrupted step must be observed again, got %s", act)
	}
}

// B3: resuming against a changed cluster.yaml is refused by default.
func TestCheckResumableRejectsChangedSpec(t *testing.T) {
	s := New(testRun, Digest([]byte("cluster: original")), at(0))
	changed := Digest([]byte("cluster: edited"))

	err := s.CheckResumable(changed, false)
	if err == nil {
		t.Fatal("expected a changed spec to be refused")
	}
	var changedErr *ErrSpecChanged
	if !errors.As(err, &changedErr) {
		t.Fatalf("expected ErrSpecChanged, got %T", err)
	}
	if !strings.Contains(err.Error(), "--force-resume") {
		t.Errorf("the error must name the way forward, got: %v", err)
	}

	if err := s.CheckResumable(changed, true); err != nil {
		t.Errorf("--force-resume should proceed, got: %v", err)
	}
	if err := s.CheckResumable(s.SpecDigest, false); err != nil {
		t.Errorf("an unchanged spec must resume cleanly, got: %v", err)
	}
}

// B4: a node traversal stops where it failed. The nodes after it must be
// indistinguishable from never having been attempted -- half-configured
// clusters are what §4.3 exists to prevent.
func TestNodeTraversalStopsAtFailure(t *testing.T) {
	s := newState()
	nodes := []string{"10.10.0.21", "10.10.0.22", "10.10.0.23"}

	ok(s, "l1-join-agent", "rke2-agent-install", nodes[0], 1)
	ok(s, "l1-join-agent", "rke2-agent-ready", nodes[0], 2)
	ok(s, "l1-join-agent", "rke2-agent-install", nodes[1], 3)
	s.SetStep("l1-join-agent", "rke2-agent-ready", nodes[1],
		Step{Status: event.StatusFailed, Attempt: 3, Code: "PF-601"}, at(4))
	// nodes[2] is deliberately never touched.

	rollup := s.NodeStatus("l1-join-agent")
	if rollup[nodes[0]] != event.StatusOK {
		t.Errorf("first node = %s, want ok", rollup[nodes[0]])
	}
	if rollup[nodes[1]] != event.StatusFailed {
		t.Errorf("second node = %s, want failed", rollup[nodes[1]])
	}
	if st, present := rollup[nodes[2]]; present {
		t.Errorf("third node has status %s; it must have no record at all", st)
	}

	for _, step := range []string{"rke2-agent-install", "rke2-agent-ready"} {
		if act, _ := s.Decide("l1-join-agent", step, nodes[2], Options{}); act != ActionRun {
			t.Errorf("unattempted node's %s = %s, want run", step, act)
		}
	}
	if act, _ := s.Decide("l1-join-agent", "rke2-agent-install", nodes[0], Options{}); act != ActionSkip {
		t.Error("the completed node would be redone")
	}
}

func TestPendingPhasesAndResumePoint(t *testing.T) {
	s := newState()
	s.SetPhase("preflight", event.StatusOK, at(1))
	s.SetPhase("plan", event.StatusOK, at(2))
	s.SetPhase("l0-node-prep", event.StatusOK, at(3))
	s.SetStep("l1-bootstrap", "rke2-server-ready", "10.10.0.11",
		Step{Status: event.StatusFailed, Code: "PF-601"}, at(4))

	pending := s.PendingPhases(phaseOrder)
	want := []string{"l1-bootstrap", "l1-join-agent", "l2-dataplane", "verify", "report"}
	if len(pending) != len(want) {
		t.Fatalf("pending = %v, want %v", pending, want)
	}
	for i := range want {
		if pending[i] != want[i] {
			t.Fatalf("pending = %v, want %v", pending, want)
		}
	}

	phase, step, ok := s.ResumePoint(phaseOrder)
	if !ok {
		t.Fatal("expected a resume point")
	}
	if phase != "l1-bootstrap" {
		t.Errorf("resume phase = %q, want l1-bootstrap", phase)
	}
	if step != "rke2-server-ready@10.10.0.11" {
		t.Errorf("resume step = %q", step)
	}
}

// A phase left running was interrupted; it is work, not history.
func TestRunningPhaseIsStillPending(t *testing.T) {
	s := newState()
	s.SetPhase("preflight", event.StatusOK, at(1))
	s.SetPhase("plan", event.StatusRunning, at(2))

	pending := s.PendingPhases(phaseOrder)
	if len(pending) == 0 || pending[0] != "plan" {
		t.Errorf("pending = %v, want plan first", pending)
	}
}

func TestResumePointWhenEverythingIsDone(t *testing.T) {
	s := newState()
	for _, p := range phaseOrder {
		s.SetPhase(p, event.StatusOK, at(1))
	}
	if _, _, ok := s.ResumePoint(phaseOrder); ok {
		t.Error("a completed run should have no resume point")
	}
}
