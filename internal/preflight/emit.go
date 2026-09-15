package preflight

import (
	"strings"
	"sync"

	"github.com/ryxenix/malmok/internal/codes"
	"github.com/ryxenix/malmok/internal/event"
)

// Emitting probe results as events is what lets a screen draw preflight without
// calling into this package.
//
// ADR-002: the engine emits, the renderer consumes. A TUI that read a Report
// directly would be reading engine state, and the same run would then look
// different depending on whether somebody was watching -- which is exactly what
// the event file exists to prevent.

// Emitter turns probe results into events on a writer.
//
// Methods take a pointer because the emitter counts what it could not write.
// Every call site holds it in a local variable, so `emitter.Emit` and
// `emitter.ForNode(host)` bind the address without any of them changing.
type Emitter struct {
	Writer *event.Writer
	// Phase is the phase name the results are attributed to. Empty means
	// "preflight", which is what the catalogue calls it.
	Phase string

	// Probes run concurrently and ForNode hands the same emitter to each
	// node's goroutine, so the counters below are shared and need the lock.
	// The writer has its own; this one is for the record of what it refused.
	mu      sync.Mutex
	lost    int
	lostErr error
}

// PhasePreflight is where probe events are filed.
const PhasePreflight = "preflight"

// Emit writes one probe result, and counts it when the write fails.
//
// A failed write does not stop the run. A preflight that gave up because its
// log could not be written would have traded a report for nothing, and that
// part of the original reasoning holds.
//
// What did not hold was the sentence after it: that a lost line shows up as a
// gap in the sequence, which the reader reports. The reader does check
// continuity and does raise ErrGap -- but the writer consumes the sequence
// number only after the write succeeds, deliberately, so that a failed Emit
// leaves the numbering intact. Both halves are correct on their own and the
// conclusion drawn from them was not: a dropped event leaves no gap, so
// nothing downstream can tell it ever existed.
//
// So the loss is counted here, because here is the only place that knows. Ask
// with Lost, and say so: an audit report assembled from a file that is missing
// findings is not an audit report with fewer findings in it.
func (e *Emitter) Emit(p ProbeResult) {
	if e.Writer == nil {
		return
	}
	phase := e.Phase
	if phase == "" {
		phase = PhasePreflight
	}
	_, err := e.Writer.Emit(event.Event{
		Kind:     event.KindProbe,
		Phase:    phase,
		Step:     p.ID,
		Node:     p.Node,
		Status:   eventStatus(p),
		Code:     p.ID,
		Detail:   oneLine(p.Detail),
		Evidence: p.Evidence,
	})
	if err == nil {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.lost++
	if e.lostErr == nil {
		// The first one, because it is the one that says why. The rest are
		// usually the same disk saying the same thing.
		e.lostErr = err
	}
}

// Lost reports how many results never reached the event file, and why the
// first of them did not.
//
// Zero and nil is the only answer that means the evidence is complete.
func (e *Emitter) Lost() (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lost, e.lostErr
}

// ForNode returns an emitter that stamps every result with a node.
//
// The node is on the result rather than passed alongside, because results
// arrive from concurrent probes and a shared field would attribute one node's
// finding to another.
func (e *Emitter) ForNode(host string) func(ProbeResult) {
	return func(p ProbeResult) {
		p.Node = host
		e.Emit(p)
	}
}

// eventStatus maps a probe outcome onto the event vocabulary.
//
// The distinction the schema already draws is exactly the one preflight needs:
// "blocked" is what cannot proceed and no retry fixes, "failed" is a check that
// did not pass but leaves a way forward. So a blocking probe is blocked and a
// warning is failed -- rather than collapsing warnings into "ok", which would
// leave a renderer unable to tell a warning from a clean pass.
//
// No Level is set. The schema reserves it for kind=log, and a probe carries its
// weight in its status and in the severity its code is registered with.
func eventStatus(p ProbeResult) event.Status {
	switch {
	case p.Status == StatusSkip:
		return event.StatusSkipped
	case p.Status == StatusFail && p.Severity == codes.SeverityBlock:
		return event.StatusBlocked
	case p.Status == StatusFail:
		return event.StatusFailed
	}
	return event.StatusOK
}

// oneLine collapses a detail into the single line the schema requires.
//
// The messages are written long on purpose -- a failure has to say what it
// costs -- and a newline slipping into one would break every reader that treats
// the file as JSONL.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.Join(strings.Fields(s), " ")
}
