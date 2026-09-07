package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ryxenix/malmok/internal/exec"
)

// ShellStep implements Step as a pair of shell programs run on a node.
//
// ADR-012 settled that there is no configuration management tool underneath
// the L0/L1 phases, and this is the one shape those phases take. It is not a
// module layer: there is no package manager abstraction, no template engine and
// no file-editing primitive here, because the boundary is the concrete steps
// the catalogue asks for.
//
// Check must not change anything. Exiting zero means the target state already
// holds, which is what makes the step safe to re-run and what resume depends
// on: a process killed mid-step leaves a record saying "running", and the only
// way back is to look at the world again (docs/11-execute.md §4.2 rule 4).
type ShellStep struct {
	// Phase and Name form the step id; Host is the node it runs on.
	//
	// The runner splits a step key on the first '@', so neither Phase nor Name
	// may contain one -- the constraint is on the ids we author (§4.1).
	Phase string
	Name  string
	Host  string

	Runner exec.Runner

	// Check exits zero when the target state holds.
	Check string
	// Do moves the node to the target state.
	Do string

	// Satisfied and Missing are the sentences for the event stream. A single
	// %s in either is filled with what the command printed, so the node's own
	// words reach the operator rather than a restatement of the step name.
	Satisfied string
	Missing   string

	// CheckTimeout and DoTimeout bound each half. Zero means the caller's
	// context decides, which is right for a quick file test and wrong for an
	// install that pulls images.
	CheckTimeout time.Duration
	DoTimeout    time.Duration

	// Attempts overrides the runner's retry budget. An install that fetches
	// from the internet deserves more than a file test does.
	Attempts int

	// Once marks work that cannot honestly claim idempotency (§3.3).
	Once bool

	// Input is fed to Check and Do on standard input, for a step whose
	// subject is too large to put in a command.
	//
	// A command is not a place to put a file. Measured against a node, a
	// command carrying 128KB reaches the far side and dies on the
	// argument-length limit, and at 256KB the connection is dropped before
	// anything runs -- which is what a chart archive embedded in a HelmChart
	// does. Bytes on stdin have no such ceiling.
	Input []byte
}

// ID is the step identity the state file records.
func (s *ShellStep) ID() string { return s.Phase + "/" + s.Name + "@" + s.Host }

// MaxAttempts implements RetryableStep.
func (s *ShellStep) MaxAttempts() int { return s.Attempts }

// OneShot implements OneShotStep.
func (s *ShellStep) OneShot() bool { return s.Once }

// Observe runs Check.
//
// A non-zero exit is an answer, not a fault: it means the target state does not
// hold yet. Only a transport failure is an error, because that is the case
// where nothing was learned.
func (s *ShellStep) Observe(ctx context.Context) (Observation, error) {
	res, err := s.run(ctx, s.Check, s.CheckTimeout)
	if err != nil {
		return Observation{}, Fail("EX-001",
			fmt.Errorf("%s: could not be observed on %s: %w", s.Name, s.Host, err))
	}
	if res.OK() {
		return Observation{
			Satisfied: true,
			Detail:    fill(s.Satisfied, res),
			Evidence:  CleanForEvent(res.Out()),
		}, nil
	}
	return Observation{
		Detail:   fill(s.Missing, res),
		Evidence: CleanForEvent(res.Out() + " " + res.Err()),
	}, nil
}

// Apply runs Do.
//
// Do is traced. A step body is a script, and a script under `set -e` can die
// on a line that prints nothing -- which is how a real failure reached an
// operator as "gateway-api-crds failed (exit 1):" with nothing after the
// colon, twice, on two different steps. Tracing costs nothing on the happy
// path (a successful Apply's output is never reported; the step's sentence
// comes from re-observing) and on the unhappy one it names the command that
// failed.
func (s *ShellStep) Apply(ctx context.Context) error {
	res, err := s.stream(ctx, "set -x\n"+s.Do, s.DoTimeout)
	if err != nil {
		return Fail("EX-002", fmt.Errorf("%s: could not be applied on %s: %w", s.Name, s.Host, err))
	}
	if !res.OK() {
		// The node's own words are more use than a restatement of the step
		// name, so they are carried into the failure rather than summarised.
		said := CleanForEvent(res.Out() + " " + res.Err())
		if strings.TrimSpace(said) == "" {
			said = "the command printed nothing"
		}
		return FailWith("EX-002", said, fmt.Errorf("%s failed on %s (exit %d): %s",
			s.Name, s.Host, res.ExitCode, clip(said)))
	}
	return nil
}

// stream runs a script and reports the lines it prints while it runs.
//
// A step that waits -- for a node to be Ready, for a certificate to be
// signed, for the dataplane to take a configuration -- says nothing for
// minutes, and a screen with a spinner and no words cannot be told apart from
// a hung one. The waits print where they have got to; this carries those
// lines out as they appear rather than when the step ends.
//
// Trace lines are dropped: `set -x` echoes every command to stderr, which is
// exactly what a failure needs and exactly what nobody wants scrolling past
// while things are working. They are still collected for the failure.
func (s *ShellStep) stream(ctx context.Context, cmd string, timeout time.Duration) (exec.Result, error) {
	// A step with input cannot stream: the bytes and the trace share one
	// session, and what matters here is that the bytes arrive at all.
	if len(s.Input) > 0 {
		return s.run(ctx, cmd, timeout)
	}
	st, ok := s.Runner.(exec.Streamer)
	if !ok {
		return s.run(ctx, cmd, timeout)
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return st.RunStream(ctx, cmd, func(line string) {
		if strings.HasPrefix(line, "+") {
			return
		}
		Logf(ctx, "%s", CleanForEvent(line))
	})
}

func (s *ShellStep) run(ctx context.Context, cmd string, timeout time.Duration) (exec.Result, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if len(s.Input) > 0 {
		f, ok := s.Runner.(exec.Feeder)
		if !ok {
			// Rather than send it as a command and have the connection die
			// with nothing said: a runner that cannot carry the bytes should
			// say so, not fail somewhere further down.
			return exec.Result{}, fmt.Errorf(
				"%s needs to send %d bytes on standard input and %s cannot",
				s.Name, len(s.Input), s.Runner.Host())
		}
		return f.RunInput(ctx, cmd, s.Input)
	}
	return s.Runner.Run(ctx, cmd)
}

// fill puts what the command printed into the step's sentence.
func fill(tmpl string, res exec.Result) string {
	if !strings.Contains(tmpl, "%s") {
		return tmpl
	}
	out := CleanForEvent(res.Out())
	if out == "" {
		out = CleanForEvent(res.Err())
	}
	return fmt.Sprintf(tmpl, out)
}

// CleanForEvent strips what the event schema refuses: control sequences,
// newlines and tabs (§5.3). Exported because every step that captures remote
// output has to do it at capture time.
func CleanForEvent(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == 0x1b:
			continue
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(' ')
		case r < 0x20:
			continue
		default:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// clip shortens a long message from the end.
//
// The head is kept, not the tail: every step here prints what went wrong first
// and dumps diagnostics after it, so truncating from the front removes the one
// sentence that says what happened. A failure that opens mid-way through a pod
// listing tells the reader nothing they can start from.
func clip(s string) string {
	const max = 400
	if len(s) <= max {
		return s
	}
	return s[:max] + " ... (truncated; the full output is in the run's event file)"
}

// FailedStep reports something that went wrong while deciding what work to do.
//
// A phase that returned no steps because it could not decide would be reported
// as a phase that succeeded, which is the worst available outcome: a run that
// says it installed something and did not. This is a step that cannot be
// satisfied, so the failure is the phase's failure.
//
// Implemented in Go rather than as a shell command that exits non-zero: a step
// whose contract is "never satisfied" must not depend on a shell, a fake, or
// anything else that could answer differently.
type FailedStep struct {
	// StepID is the identity the state file records.
	StepID string
	// Why is one English line saying what is missing.
	Why string
}

func (f FailedStep) ID() string { return f.StepID }

func (f FailedStep) Observe(context.Context) (Observation, error) {
	return Observation{Detail: f.Why}, nil
}

func (f FailedStep) Apply(context.Context) error {
	return FailFatal("EX-002", errors.New(f.Why))
}
