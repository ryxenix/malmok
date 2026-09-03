// Package state records what a run has already done, so that an interrupted
// install resumes instead of starting over. See docs/11-execute.md §4.
//
// WHAT THIS FILE IS NOT
//
//	It is a resume summary, not a log. Log lines and probe evidence live in the
//	event stream; putting them here would mean rewriting a growing file
//	atomically after every step, and `attach` rebuilds the screen by replaying
//	events precisely because this file cannot (§1.2).
//
// The engine writes it from several goroutines — node-scoped phases run in
// parallel — so every exported method is safe for concurrent use.
package state

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ryxenix/malmok/internal/event"
)

// Timestamp is the event package's millisecond RFC3339 form. One timestamp
// format across the run directory keeps state.json and events.jsonl readable
// side by side at a customer site.
type Timestamp = event.Timestamp

// ---------------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------------

// Step is one unit of work's outcome. Identity lives in the map key, not here;
// see stepKey.
type Step struct {
	Status  event.Status `json:"status"`
	Attempt int          `json:"attempt,omitempty"`

	// Code is the diagnostic code of a failure, registered in internal/codes.
	Code string `json:"code,omitempty"`

	// OneShot marks work that cannot honestly claim to be idempotent — an etcd
	// restore, a CA replacement (§3.3). Resume never re-runs one of these on
	// its own.
	OneShot bool `json:"oneShot,omitempty"`

	UpdatedAt Timestamp `json:"updatedAt"`
}

// Phase groups the steps of one phase from the §2 catalogue.
type Phase struct {
	Status event.Status `json:"status"`

	// FailedStep and Code duplicate the failing step's identity at phase level
	// so an operator can read the file top-down without scanning every entry.
	FailedStep string `json:"failedStep,omitempty"`
	Code       string `json:"code,omitempty"`

	// Steps is keyed by step id, or "step@node" for node-scoped work. Node
	// granularity is required: when a three-node traversal fails on the second
	// node, resume has to know the first is done and the third was never
	// attempted (§4.3).
	Steps map[string]*Step `json:"steps,omitempty"`
}

// State is the whole run.
type State struct {
	mu sync.RWMutex

	Run string `json:"run"`

	// SpecDigest pins the cluster.yaml this run started from. Resuming with a
	// different input makes the file lie about what was built, which is the
	// failure mode that kept Pulumi state out of this project (ADR-001).
	SpecDigest string `json:"specDigest"`

	StartedAt Timestamp `json:"startedAt"`
	UpdatedAt Timestamp `json:"updatedAt"`

	Phases map[string]*Phase `json:"phases,omitempty"`
}

// New starts a fresh run.
func New(run, specDigest string, now time.Time) *State {
	ts := event.NewTimestamp(now)
	return &State{
		Run:        run,
		SpecDigest: specDigest,
		StartedAt:  ts,
		UpdatedAt:  ts,
		Phases:     map[string]*Phase{},
	}
}

// Digest is the canonical spec digest: sha256 of the raw cluster.yaml bytes,
// hex, prefixed with the algorithm so a future change stays distinguishable in
// files already written.
func Digest(spec []byte) string {
	sum := sha256.Sum256(spec)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// stepKey addresses a step within a phase. Node-scoped steps are suffixed so
// that the same step id on different nodes cannot collide.
//
// The split is on the FIRST separator, which puts the "must not contain @"
// constraint on the step id rather than on the node. Step ids are constants we
// author; node ids come from the customer's cluster.yaml and are whatever they
// are. Constraining the side we control is the only version of this that cannot
// silently lose data.
func stepKey(step, node string) string {
	if node == "" {
		return step
	}
	return step + "@" + node
}

// SplitStepKey reverses stepKey. An empty node means the step is cluster-scoped.
func SplitStepKey(key string) (step, node string) {
	if i := strings.Index(key, "@"); i >= 0 {
		return key[:i], key[i+1:]
	}
	return key, ""
}

// ---------------------------------------------------------------------------
// Mutation
// ---------------------------------------------------------------------------

// SetStep records a step's outcome, creating the phase entry if needed.
func (s *State) SetStep(phase, step, node string, st Step, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st.UpdatedAt = event.NewTimestamp(now)
	p := s.phaseLocked(phase)
	p.Steps[stepKey(step, node)] = &st

	if st.Status == event.StatusFailed || st.Status == event.StatusBlocked {
		p.Status = st.Status
		p.FailedStep = stepKey(step, node)
		p.Code = st.Code
	}
	s.UpdatedAt = event.NewTimestamp(now)
}

// SetPhase records a phase-level transition. Clearing a failure is explicit:
// moving a phase back to a non-terminal status drops the recorded failure so a
// stale FailedStep cannot outlive the retry that fixed it.
func (s *State) SetPhase(phase string, status event.Status, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	p := s.phaseLocked(phase)
	p.Status = status
	if status != event.StatusFailed && status != event.StatusBlocked {
		p.FailedStep, p.Code = "", ""
	}
	s.UpdatedAt = event.NewTimestamp(now)
}

func (s *State) phaseLocked(name string) *Phase {
	if s.Phases == nil {
		s.Phases = map[string]*Phase{}
	}
	p, ok := s.Phases[name]
	if !ok {
		p = &Phase{Status: event.StatusPending, Steps: map[string]*Step{}}
		s.Phases[name] = p
	}
	if p.Steps == nil {
		p.Steps = map[string]*Step{}
	}
	return p
}

// ---------------------------------------------------------------------------
// Inspection
// ---------------------------------------------------------------------------

// StepState returns the recorded outcome of a step.
func (s *State) StepState(phase, step, node string) (Step, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	p, ok := s.Phases[phase]
	if !ok {
		return Step{}, false
	}
	st, ok := p.Steps[stepKey(step, node)]
	if !ok {
		return Step{}, false
	}
	return *st, true
}

// PhaseState returns a phase's status. A phase with no record is pending —
// which is how "never attempted" is represented, and what §4.3 requires for the
// nodes after a traversal stops.
func (s *State) PhaseState(phase string) (Phase, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	p, ok := s.Phases[phase]
	if !ok {
		return Phase{Status: event.StatusPending}, false
	}
	cp := Phase{Status: p.Status, FailedStep: p.FailedStep, Code: p.Code,
		Steps: make(map[string]*Step, len(p.Steps))}
	for k, v := range p.Steps {
		c := *v
		cp.Steps[k] = &c
	}
	return cp, true
}

// NodeStatus rolls a phase's node-scoped steps up per node: failed if any step
// failed, ok only when every recorded step is terminal and none failed.
//
// Derived rather than stored, so it cannot drift from the steps it summarises.
func (s *State) NodeStatus(phase string) map[string]event.Status {
	s.mu.RLock()
	defer s.mu.RUnlock()

	p, ok := s.Phases[phase]
	if !ok {
		return nil
	}
	out := map[string]event.Status{}
	for key, st := range p.Steps {
		_, node := SplitStepKey(key)
		if node == "" {
			continue
		}
		switch cur := out[node]; {
		case cur == event.StatusFailed || cur == event.StatusBlocked:
			// A failure already decided this node.
		case st.Status == event.StatusFailed || st.Status == event.StatusBlocked,
			st.Status == event.StatusRunning,
			cur == "" || cur == event.StatusOK || cur == event.StatusSkipped:
			out[node] = st.Status
		}
	}
	return out
}

// Nodes lists the nodes a phase has records for, sorted. Nodes absent from this
// list were never attempted.
func (s *State) Nodes(phase string) []string {
	out := make([]string, 0)
	for node := range s.NodeStatus(phase) {
		out = append(out, node)
	}
	sort.Strings(out)
	return out
}

// Snapshot returns a deep copy safe to marshal or hand to a renderer.
//
// It returns a pointer and builds a fresh value rather than copying the
// receiver: State carries a mutex, and copying one by value is the bug `go vet`
// exists to catch.
func (s *State) Snapshot() *State {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cp := &State{
		Run: s.Run, SpecDigest: s.SpecDigest,
		StartedAt: s.StartedAt, UpdatedAt: s.UpdatedAt,
		Phases: make(map[string]*Phase, len(s.Phases)),
	}
	for name, p := range s.Phases {
		np := &Phase{Status: p.Status, FailedStep: p.FailedStep, Code: p.Code,
			Steps: make(map[string]*Step, len(p.Steps))}
		for k, v := range p.Steps {
			c := *v
			np.Steps[k] = &c
		}
		cp.Phases[name] = np
	}
	return cp
}
