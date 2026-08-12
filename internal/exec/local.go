package exec

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	osexec "os/exec"
	"strings"

	"platform.ryxen.dev/platformctl/api/v1alpha1"
)

// Running on the machine the tool is invoked on.
//
// This is the ordinary case, not the special one. An installer is normally run
// on the machine being installed; reaching another machine over SSH is the
// addition. The tool was SSH-only for a while, which meant installing onto the
// host you were sitting at required an sshd, an account and a credential for
// your own box -- a round trip through the network stack to reach a filesystem
// that was already open.
//
// The routing is by address rather than by a flag, for the same reason PF-802
// reads a marker off the node rather than trusting a mode: an address either is
// or is not assigned to an interface on this machine, and that is a fact
// nothing has to be told.

// LocalHostNames are the literal values that mean this machine whatever its
// addresses are.
//
// Useful when a document is written before anybody knows what address the node
// will have, and for a single-node build where the address is a detail.
var LocalHostNames = []string{"local", "localhost"}

// LocalRunner runs commands on the machine the tool is running on.
type LocalRunner struct {
	// host is what the document called this node, kept so diagnostics name the
	// node the operator named rather than always saying "local".
	host string
}

// NewLocal returns a runner for this machine.
func NewLocal(host string) *LocalRunner {
	if host == "" {
		host = "local"
	}
	return &LocalRunner{host: host}
}

// Run executes the command through a shell, exactly as the SSH runner does.
//
// The same shell, because every probe and every step is written as a shell
// program: a local path that ran argv instead would be a second dialect, and
// the two would drift on the first pipe somebody wrote.
func (l *LocalRunner) Run(ctx context.Context, cmd string) (Result, error) {
	c := osexec.CommandContext(ctx, "sh", "-c", cmd)

	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr

	err := c.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}

	var exit *osexec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		// A non-zero exit is an answer, not a fault. `test -e` failing is the
		// probe working.
		res.ExitCode = exit.ExitCode()
	default:
		// The command could not be run at all -- no shell, context expired.
		return Result{}, err
	}
	return res, nil
}

// Host names the node this runner is for.
func (l *LocalRunner) Host() string { return l.host }

// Close does nothing. There is no connection to give back.
func (l *LocalRunner) Close() error { return nil }

// IsRoot reports that no elevation is needed.
//
// Read at call time rather than at construction, because it is a property of
// the process and a stale answer would produce a step that runs `sudo` on a
// machine that has none.
func (l *LocalRunner) IsRoot() bool { return os.Geteuid() == 0 }

// Rooted is implemented by a runner that already runs with the privileges the
// probes need, so Sudo can leave it alone.
//
// It exists for the local path: a tool invoked with `sudo platformctl` is
// already root, and wrapping every command in another `sudo` requires the
// binary to be installed and the account to be in the sudoers file for no gain
// -- a minimal image frequently has neither.
type Rooted interface{ IsRoot() bool }

// IsLocal reports whether an address belongs to the machine this is running on.
//
// Loopback and the literal names always count. Everything else is compared
// against the addresses actually assigned to this machine's interfaces, which
// is the question being asked: an address that resolves here is here.
func IsLocal(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return false
	}
	for _, name := range LocalHostNames {
		if h == name {
			return true
		}
	}

	ip := net.ParseIP(h)
	if ip == nil {
		// A name rather than an address. Resolving it would make the answer
		// depend on DNS, and a tool that installed locally because a name
		// happened to resolve to 127.0.0.1 in one resolver's view would be
		// surprising in a way that is hard to undo.
		return false
	}
	if ip.IsLoopback() {
		return true
	}

	for _, own := range localAddresses() {
		if own.Equal(ip) {
			return true
		}
	}
	return false
}

// localAddresses lists the addresses assigned to this machine.
//
// A failure to enumerate is treated as "nothing is local": the cost of being
// wrong that way is an SSH connection to a node that could have been reached
// directly, and the cost of being wrong the other way is configuring the wrong
// machine.
func localAddresses() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		switch v := a.(type) {
		case *net.IPNet:
			out = append(out, v.IP)
		case *net.IPAddr:
			out = append(out, v.IP)
		}
	}
	return out
}

// NodeIsLocal decides how one node in the document is reached.
//
// `local` on the node overrides the address, in both directions, because both
// are real:
//
//   - true where the address is not one this machine holds. A node behind NAT,
//     or named by the VIP it will carry once the cluster is up, is still this
//     machine.
//   - false where it is. A tool running in a container with host networking
//     sees the host's addresses and is not the host; forcing SSH is the way out
//     of that, and it has to exist because the address alone cannot tell.
func NodeIsLocal(n v1alpha1.NodeSpec) bool {
	if n.Local != nil {
		return *n.Local
	}
	return IsLocal(n.Host)
}

// Connect opens a runner for one node: this machine when the address is this
// machine's, SSH otherwise.
//
// One function rather than a decision at each call site, so preflight, the
// build and the upgrade cannot disagree about which machine a node is.
func Connect(ctx context.Context, cfg SSHConfig) (Runner, error) {
	if cfg.Local {
		return NewLocal(cfg.Host), nil
	}
	return Dial(ctx, cfg)
}
