// Package exec runs commands on nodes.
//
// It is an interface first and an SSH implementation second, because CLAUDE.md
// requires the probes to be unit testable without a node: every probe takes a
// Runner, and the tests hand it recorded output from a real machine rather than
// output somebody imagined.
//
// The package deliberately knows nothing about what it runs. Deciding what to
// execute, and what the output means, belongs to the probe.
package exec

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Result is one command's outcome.
//
// Stderr and the exit status are kept separately rather than folded into an
// error, because a probe frequently cares about all three: `test -e` failing is
// an answer, not a fault.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// OK reports whether the command exited zero.
func (r Result) OK() bool { return r.ExitCode == 0 }

// Out is stdout with surrounding whitespace removed, which is what almost every
// probe actually wants.
func (r Result) Out() string { return strings.TrimSpace(r.Stdout) }

// Err is stderr trimmed, for the evidence field of a probe result.
func (r Result) Err() string { return strings.TrimSpace(r.Stderr) }

// Runner executes a command on one node.
//
// The command is a string rather than an argv because it is run through the
// node's shell: preflight reads files, follows pipes and tests conditions, and
// expressing that as argv would mean reimplementing a shell.
//
// A Runner returns an error only when the command could not be run at all --
// the connection dropped, the context expired. A command that ran and failed is
// a Result with a non-zero ExitCode, because that is an answer.
type Runner interface {
	Run(ctx context.Context, cmd string) (Result, error)
	// Host is the address this runner talks to, for diagnostics.
	Host() string
	Close() error
}

// ErrNotConnected is returned when a runner is used after it was closed.
var ErrNotConnected = errors.New("exec: not connected")

// Sudo wraps a Runner so that every command runs with elevated privileges.
//
// Preflight reads things an unprivileged account cannot -- /proc/config.gz,
// the kernel lockdown state, the SELinux mode -- so the alternative to sudo is
// a probe that reports "unknown" and is ignored.
type Sudo struct {
	Runner
	// Password is supplied to sudo on stdin when the account needs one. Empty
	// means passwordless sudo, which is what a purpose-built install account
	// normally has.
	Password string
}

// Run prefixes the command with sudo.
func (s Sudo) Run(ctx context.Context, cmd string) (Result, error) {
	if s.Password == "" {
		return s.Runner.Run(ctx, "sudo -n -- sh -c "+quote(cmd))
	}
	// -S reads the password from stdin and -p '' keeps the prompt out of
	// stderr, so a probe reading stderr does not find a prompt in it.
	full := fmt.Sprintf("printf '%%s\\n' %s | sudo -S -p '' -- sh -c %s",
		quote(s.Password), quote(cmd))
	return s.Runner.Run(ctx, full)
}

// quote wraps a string for a POSIX shell.
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Fake is a Runner backed by recorded output, for tests.
//
// Commands are matched by substring rather than equality so a test does not
// have to reproduce the exact shell line a probe happens to build; what a probe
// asks for is allowed to change without rewriting every fixture.
type Fake struct {
	// Responses maps a substring of the command to what the node answered.
	Responses map[string]Result
	// Default is returned when nothing matched. The zero value is a successful
	// command with no output, which is rarely what a test means, so an
	// unmatched command is recorded in Unmatched.
	Default Result
	// Unmatched collects commands no response was found for, so a test can
	// assert it exercised what it thought it did.
	Unmatched []string
	// Err, when set, is returned for every command: the node is unreachable.
	Err error

	// Log records every command in order.
	Log []string
}

// Run answers from the recorded responses.
//
// The longest matching pattern wins. Map iteration order is random, and a
// command frequently contains more than one pattern -- the shell that reads the
// kernel configuration also contains `uname -r` -- so first-match would make a
// test's result depend on the order Go happened to walk the map that run.
func (f *Fake) Run(_ context.Context, cmd string) (Result, error) {
	f.Log = append(f.Log, cmd)
	if f.Err != nil {
		return Result{}, f.Err
	}

	best := ""
	var found Result
	for pattern, res := range f.Responses {
		if strings.Contains(cmd, pattern) && len(pattern) > len(best) {
			best, found = pattern, res
		}
	}
	if best != "" {
		return found, nil
	}

	f.Unmatched = append(f.Unmatched, cmd)
	return f.Default, nil
}

// Host names the fake.
func (f *Fake) Host() string { return "fake" }

// Close does nothing.
func (f *Fake) Close() error { return nil }
