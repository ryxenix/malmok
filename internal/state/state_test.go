package state

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryxen/malmok/internal/event"
)

const testRun = "01JBQ8F2K3M5N7P9R1S3T5V7W9"

func at(sec int) time.Time {
	return time.Date(2026, 8, 3, 9, 0, sec, 0, time.UTC)
}

func newState() *State {
	return New(testRun, Digest([]byte("cluster: a")), at(0))
}

func ok(s *State, phase, step, node string, sec int) {
	s.SetStep(phase, step, node, Step{Status: event.StatusOK}, at(sec))
}

// ---------------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------------

func TestDigestIsStableAndDistinguishing(t *testing.T) {
	a := Digest([]byte("cluster: a"))
	if a != Digest([]byte("cluster: a")) {
		t.Error("digest is not stable for identical input")
	}
	if a == Digest([]byte("cluster: b")) {
		t.Error("different specs produced the same digest")
	}
	if !strings.HasPrefix(a, "sha256:") {
		t.Errorf("digest %q lacks the algorithm prefix", a)
	}
}

func TestStepKeyRoundTrip(t *testing.T) {
	tests := []struct{ step, node string }{
		{"rke2-agent-ready", "10.10.0.22"},
		{"install-cilium", ""},
		{"weird-step", "node@with@ats"},
	}
	for _, tc := range tests {
		gotStep, gotNode := SplitStepKey(stepKey(tc.step, tc.node))
		if gotStep != tc.step || gotNode != tc.node {
			t.Errorf("round trip of (%q,%q) gave (%q,%q)", tc.step, tc.node, gotStep, gotNode)
		}
	}
}

func TestSetStepRecordsFailureAtPhaseLevel(t *testing.T) {
	s := newState()
	s.SetStep("l1-join-agent", "rke2-agent-ready", "10.10.0.22",
		Step{Status: event.StatusFailed, Attempt: 3, Code: "PF-601"}, at(5))

	p, found := s.PhaseState("l1-join-agent")
	if !found {
		t.Fatal("phase was not recorded")
	}
	if p.Status != event.StatusFailed {
		t.Errorf("phase status = %s, want failed", p.Status)
	}
	if p.FailedStep != "rke2-agent-ready@10.10.0.22" {
		t.Errorf("failedStep = %q", p.FailedStep)
	}
	if p.Code != "PF-601" {
		t.Errorf("phase code = %q, want PF-601", p.Code)
	}
}

func TestSetPhaseClearsStaleFailure(t *testing.T) {
	s := newState()
	s.SetStep("l1-join-agent", "ready", "n1", Step{Status: event.StatusFailed, Code: "PF-601"}, at(5))
	s.SetPhase("l1-join-agent", event.StatusOK, at(9))

	p, _ := s.PhaseState("l1-join-agent")
	if p.FailedStep != "" || p.Code != "" {
		t.Errorf("a retry that succeeded left the old failure behind: step=%q code=%q",
			p.FailedStep, p.Code)
	}
}

func TestNodeStatusRollup(t *testing.T) {
	s := newState()
	ok(s, "l0-node-prep", "sysctl", "10.10.0.11", 1)
	ok(s, "l0-node-prep", "trust-store", "10.10.0.11", 2)
	ok(s, "l0-node-prep", "sysctl", "10.10.0.12", 3)
	s.SetStep("l0-node-prep", "trust-store", "10.10.0.12",
		Step{Status: event.StatusFailed, Code: "PF-702"}, at(4))

	got := s.NodeStatus("l0-node-prep")
	if got["10.10.0.11"] != event.StatusOK {
		t.Errorf("node .11 = %s, want ok", got["10.10.0.11"])
	}
	if got["10.10.0.12"] != event.StatusFailed {
		t.Errorf("node .12 = %s, want failed (one failed step decides the node)", got["10.10.0.12"])
	}
	if nodes := s.Nodes("l0-node-prep"); len(nodes) != 2 {
		t.Errorf("Nodes() = %v, want two entries", nodes)
	}
}

// ---------------------------------------------------------------------------
// Persistence
// ---------------------------------------------------------------------------

func TestSaveLoadRoundTrip(t *testing.T) {
	path := Path(t.TempDir())

	s := newState()
	ok(s, "preflight", "probe-sweep", "", 1)
	s.SetStep("l1-join-agent", "rke2-agent-ready", "10.10.0.22",
		Step{Status: event.StatusFailed, Attempt: 3, Code: "PF-601"}, at(5))
	s.SetStep("l2-pki", "restore-ca", "", Step{Status: event.StatusOK, OneShot: true}, at(7))

	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if back.Run != s.Run || back.SpecDigest != s.SpecDigest {
		t.Errorf("identity changed: run=%q digest=%q", back.Run, back.SpecDigest)
	}
	if st, found := back.StepState("l1-join-agent", "rke2-agent-ready", "10.10.0.22"); !found {
		t.Error("node-scoped step did not survive the round trip")
	} else if st.Attempt != 3 || st.Code != "PF-601" {
		t.Errorf("step reloaded as %+v", st)
	}
	if st, _ := back.StepState("l2-pki", "restore-ca", ""); !st.OneShot {
		t.Error("oneShot flag did not survive the round trip")
	}
}

func TestSaveLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := Path(dir)

	s := newState()
	for i := 0; i < 5; i++ {
		ok(s, "preflight", "probe", "", i)
		if err := s.Save(path); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != FileName {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// Save must replace the file atomically: a reader never sees a truncated or
// half-written state, because it is rewritten after every step of a run that
// may take hours.
func TestSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := Path(dir)

	s := newState()
	ok(s, "preflight", "probe", "", 1)
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	errs := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				errs <- nil
				return
			default:
				if _, err := Load(path); err != nil {
					errs <- err
					return
				}
			}
		}
	}()

	for i := 0; i < 200; i++ {
		ok(s, "l0-node-prep", "sysctl", "10.10.0.11", i)
		if err := s.Save(path); err != nil {
			close(stop)
			t.Fatal(err)
		}
	}
	close(stop)
	if err := <-errs; err != nil {
		t.Fatalf("a concurrent reader saw a partial file: %v", err)
	}
}

func TestLoadIfExistsOnFirstRun(t *testing.T) {
	s, err := LoadIfExists(Path(t.TempDir()))
	if err != nil {
		t.Fatalf("a missing state file is the normal first run, not an error: %v", err)
	}
	if s != nil {
		t.Error("expected nil state for a missing file")
	}
}

func TestLoadRejectsFileWithoutRunID(t *testing.T) {
	path := Path(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"specDigest":"sha256:abc"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("expected a state file without a run id to be rejected")
	}
}

// The engine records node-scoped phases from several goroutines at once.
func TestConcurrentAccess(t *testing.T) {
	s := newState()
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			node := string(rune('a' + i%5))
			ok(s, "l0-node-prep", "sysctl", node, i)
			s.StepState("l0-node-prep", "sysctl", node)
			s.NodeStatus("l0-node-prep")
			s.Snapshot()
		}(i)
	}
	wg.Wait()

	if got := len(s.Nodes("l0-node-prep")); got != 5 {
		t.Errorf("recorded %d nodes, want 5", got)
	}
}
