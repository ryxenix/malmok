package engine

import (
	"context"
	"fmt"
	"strings"

	"platform.ryxen.dev/malmok/internal/event"
)

// A step needs to stream output while it works: the ADR-002 mockup shows log
// lines arriving under a running step, and a three-hour Ansible role that says
// nothing until it finishes is indistinguishable from one that has hung.
//
// The sink travels in the context rather than in the Step interface, so a step
// that has nothing to say implements nothing, and so the runner can bind the
// phase, step and node to every line without the step repeating them.

type logKey struct{}

// LogFunc receives one line of step output.
type LogFunc func(level event.Level, detail string)

// WithLogger attaches a log sink to ctx. The runner does this per step; callers
// outside the runner rarely need it.
func WithLogger(ctx context.Context, fn LogFunc) context.Context {
	return context.WithValue(ctx, logKey{}, fn)
}

// Log emits one line of step output.
//
// Detail must be English and single-line — event.Validate enforces both. A step
// that has captured multi-line command output should send it line by line, or
// attach it as Evidence on the Observation instead.
func Log(ctx context.Context, level event.Level, format string, args ...any) {
	fn, ok := ctx.Value(logKey{}).(LogFunc)
	if !ok || fn == nil {
		return // running outside a runner, e.g. a unit test on the step alone
	}
	fn(level, fmt.Sprintf(format, args...))
}

// Logf emits at info level.
func Logf(ctx context.Context, format string, args ...any) {
	Log(ctx, event.LevelInfo, format, args...)
}

// stepLogger builds the sink the runner installs for one step.
func (r *Runner) stepLogger(phase, step, node string) LogFunc {
	return func(level event.Level, detail string) {
		// A log line that cannot be emitted must not take the step down with
		// it: losing a line of output is bad, losing the install because a
		// line of output was malformed is worse. The failure still reaches the
		// operator, through the step's own outcome.
		_, _ = r.emit(event.Event{
			Kind: event.KindLog, Phase: phase, Step: step, Node: node,
			Level: level, Detail: detail,
		})
	}
}

// eventStep reduces a step id to the name the event schema asks for.
//
// docs/11-execute.md §5.1 gives `phase`, `step` and `node` fields of their own,
// and §5.5 writes the step as `rke2-server-ready` -- not as
// `l1-bootstrap/rke2-server-ready@10.10.0.11`. The id has to carry all three
// because state.json keys on it and a resume must find the same step again on
// the same host; the event does not, and a renderer that joins the fields it
// was given printed the phase twice and the node twice.
//
// The host is dropped rather than moved into `node`, because a cluster-scoped
// phase deliberately names no node (§5.1) -- the node it happened to talk to is
// not what the step is about. Where the host does matter, in a failure, the
// step's own message already says which one it was.
func eventStep(id, phase, node string) string {
	name := strings.TrimPrefix(id, phase+"/")
	if node != "" {
		return strings.TrimSuffix(name, "@"+node)
	}
	if at := strings.LastIndex(name, "@"); at > 0 {
		return name[:at]
	}
	return name
}
