package preflight

import (
	"strings"

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
type Emitter struct {
	Writer *event.Writer
	// Phase is the phase name the results are attributed to. Empty means
	// "preflight", which is what the catalogue calls it.
	Phase string
}

// PhasePreflight is where probe events are filed.
const PhasePreflight = "preflight"

// Emit writes one probe result.
//
// Errors are dropped deliberately. A preflight that stops because its log could
// not be written has traded a report for nothing; the run continues and the
// missing lines show up as a sequence gap, which the reader already reports.
func (e Emitter) Emit(p ProbeResult) {
	if e.Writer == nil {
		return
	}
	phase := e.Phase
	if phase == "" {
		phase = PhasePreflight
	}
	_, _ = e.Writer.Emit(event.Event{
		Kind:     event.KindProbe,
		Phase:    phase,
		Step:     p.ID,
		Node:     p.Node,
		Status:   eventStatus(p),
		Code:     p.ID,
		Detail:   oneLine(p.Detail),
		Evidence: p.Evidence,
	})
}

// ForNode returns an emitter that stamps every result with a node.
//
// The node is on the result rather than passed alongside, because results
// arrive from concurrent probes and a shared field would attribute one node's
// finding to another.
func (e Emitter) ForNode(host string) func(ProbeResult) {
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
