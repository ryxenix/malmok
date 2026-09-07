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

// Streamer is a Runner that can report output while the command is still
// running.
//
// It exists for the waits. A step that waits ten minutes for a node to become
// Ready produces nothing until it is done, so the screen shows a spinner and
// an operator cannot tell waiting from hung -- which is the same confusion
// this tool spent a week removing from its own findings. A step that prints
// where it has got to needs somebody to carry those lines out while it runs.
type Streamer interface {
	RunStream(ctx context.Context, cmd string, onLine func(string)) (Result, error)
}

// Feeder is a Runner that can send a command its standard input.
//
// It exists because a command is not a place to put a file. The manifest steps
// embedded the YAML they write in the command string, which is fine for the
// few kilobytes a HelmChart usually is and is not fine for one carrying a
// chart archive: measured against a node, a 128KB command reaches the far side
// and dies on the argument-length limit, and at 256KB the connection itself is
// dropped before anything runs. A file arrives as bytes on stdin, where there
// is no such ceiling.
type Feeder interface {
	RunInput(ctx context.Context, cmd string, stdin []byte) (Result, error)
}

// lineWriter collects everything and reports whole lines as they arrive.
type lineWriter struct {
	buf    strings.Builder
	pend   strings.Builder
	onLine func(string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	if w.onLine == nil {
		return len(p), nil
	}
	for _, b := range p {
		if b != '\n' {
			w.pend.WriteByte(b)
			continue
		}
		if line := strings.TrimSpace(w.pend.String()); line != "" {
			w.onLine(line)
		}
		w.pend.Reset()
	}
	return len(p), nil
}

func (w *lineWriter) String() string { return w.buf.String() }

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
	// A runner that is already root needs no sudo, and asking for it would
	// require the binary to be installed and the account to be in the sudoers
	// file for no gain. `sudo malmok` on the machine being installed is
	// the ordinary local invocation and a minimal image frequently has neither.
	if r, ok := s.Runner.(Rooted); ok && r.IsRoot() {
		return s.Runner.Run(ctx, cmd)
	}
	if s.Password == "" {
		return s.Runner.Run(ctx, "sudo -n -- sh -c "+quote(cmd))
	}
	// -S reads the password from stdin and -p '' keeps the prompt out of
	// stderr, so a probe reading stderr does not find a prompt in it.
	full := fmt.Sprintf("printf '%%s\\n' %s | sudo -S -p '' -- sh -c %s",
		quote(s.Password), quote(cmd))
	return s.Runner.Run(ctx, full)
}

// RunInput prefixes the command with sudo and feeds it the bytes.
//
// With a password this reads more than it looks like. `sudo -S` takes the
// password from its own standard input as the first line and then leaves the
// rest of it to the command, so the two travel together: the password line,
// then the file. Sending the password through a pipe as Run does would consume
// the whole of stdin before the command ever saw it.
func (s Sudo) RunInput(ctx context.Context, cmd string, stdin []byte) (Result, error) {
	inner, ok := s.Runner.(Feeder)
	if !ok {
		return Result{}, fmt.Errorf("exec: %s cannot send standard input", s.Runner.Host())
	}
	if r, ok := s.Runner.(Rooted); ok && r.IsRoot() {
		return inner.RunInput(ctx, cmd, stdin)
	}
	if s.Password == "" {
		return inner.RunInput(ctx, "sudo -n -- sh -c "+quote(cmd), stdin)
	}
	withPassword := append([]byte(s.Password+"\n"), stdin...)
	return inner.RunInput(ctx, "sudo -S -p '' -- sh -c "+quote(cmd), withPassword)
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
	// Input records what each RunInput was fed, in the same order.
	Input []string
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

// RunInput answers like Run and records what was fed in, so a test can assert
// the bytes a step meant to write are the bytes it sent.
func (f *Fake) RunInput(ctx context.Context, cmd string, stdin []byte) (Result, error) {
	f.Input = append(f.Input, string(stdin))
	return f.Run(ctx, cmd)
}

// Host names the fake.
func (f *Fake) Host() string { return "fake" }

// Close does nothing.
func (f *Fake) Close() error { return nil }

// Elevate wraps a runner so every command runs with the privileges the probes
// need, and proves it works before anything depends on it.
//
// The proof is the point. `sudo -n` on an account that needs a password exits
// non-zero with the command never having run -- and a non-zero exit is how
// every probe spells "no". Without this, a machine whose sudo wants a password
// reports that its kernel has no BTF and no bpf filesystem, because
// `test -e /sys/kernel/btf/vmlinux` and `grep bpf /proc/filesystems` both came
// back non-zero. That is a measurement of the account presented as a
// measurement of the kernel, and a downgrade decision would be made on it.
//
// One check at the start, so the failure names the real cause once instead of
// arriving as several dozen wrong answers.
func Elevate(ctx context.Context, r Runner, password string) (Runner, error) {
	// Already root: nothing to wrap. `sudo malmok` on the machine being
	// installed is the ordinary local invocation, and requiring sudo inside it
	// would need the binary present and the account in the sudoers file for no
	// gain.
	if rooted, ok := r.(Rooted); ok && rooted.IsRoot() {
		return r, nil
	}

	s := Sudo{Runner: r, Password: password}
	res, err := s.Run(ctx, "id -u")
	if err != nil {
		return nil, fmt.Errorf("%s: could not be asked whether it can elevate: %w", r.Host(), err)
	}
	if res.Out() != "0" {
		detail := res.Err()
		if detail == "" {
			detail = res.Out()
		}
		if password == "" {
			return nil, fmt.Errorf(
				"%s: this account cannot elevate without a password and none was given. "+
					"Set ssh.becomePassword in the document, or run malmok as root on a local node: %s",
				r.Host(), CleanLine(detail))
		}
		return nil, fmt.Errorf("%s: this account cannot elevate: %s", r.Host(), CleanLine(detail))
	}
	return s, nil
}

// CleanLine folds a command's output into one line for an error message.
func CleanLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
