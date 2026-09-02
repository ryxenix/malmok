package exec

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/ryxen/malmok/api/v1alpha1"
)

// SSH is the real Runner.
//
// One connection is held open for the life of the preflight rather than dialled
// per command: a preflight run asks a node several dozen questions, and a fresh
// handshake for each turns a two-second probe into a minute of key exchange.

// SSHConfig is what is needed to reach one node.
type SSHConfig struct {
	Host string
	Port int
	User string

	// Password or PrivateKey. A key is preferred everywhere it exists; a
	// password is accepted because customer sites hand one over and refusing it
	// means the tool cannot be used on the day it is needed.
	Password   string
	PrivateKey []byte
	// Passphrase decrypts PrivateKey when it is encrypted.
	Passphrase string

	// KnownHosts is the file host keys are verified against. Empty means
	// ~/.ssh/known_hosts.
	KnownHosts string
	// InsecureSkipHostKeyCheck accepts any host key. This is a real choice with
	// a real cost and it is never the default: on a first build the nodes have
	// no entry yet, and an operator who has just installed the machines
	// themselves is in a position to say so.
	InsecureSkipHostKeyCheck bool

	Timeout time.Duration

	// Local routes to this machine instead of opening a connection. Decided by
	// FromSpec from the document; see NodeIsLocal for what decides it.
	Local bool
}

// FromSpec builds a config from a node's document entry.
func FromSpec(n v1alpha1.NodeSpec) SSHConfig {
	c := SSHConfig{
		Host:  n.Host,
		Port:  n.SSH.Port,
		User:  n.SSH.User,
		Local: NodeIsLocal(n),
	}
	if c.Local {
		// Nothing is dialled, so the credential fields have no meaning. Filling
		// them in with defaults would put "root" in a diagnostic about a machine
		// nobody logged into.
		return c
	}
	if c.Port == 0 {
		c.Port = 22
	}
	if c.User == "" {
		c.User = "root"
	}
	return c
}

// SSHRunner is a live connection to one node.
type SSHRunner struct {
	host   string
	client *ssh.Client

	mu     sync.Mutex
	closed bool
}

// Dial opens a connection.
func Dial(ctx context.Context, cfg SSHConfig) (*SSHRunner, error) {
	if cfg.Port == 0 {
		cfg.Port = 22
	}
	if cfg.User == "" {
		cfg.User = "root"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}

	auth, err := authMethods(cfg)
	if err != nil {
		return nil, err
	}
	hostKey, err := hostKeyCallback(cfg)
	if err != nil {
		return nil, err
	}

	addr := net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port))
	clientCfg := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            auth,
		HostKeyCallback: hostKey,
		Timeout:         cfg.Timeout,
		// Offer the key types this host is already known by, the way OpenSSH
		// does. Left unset, the server picks its own favourite -- ecdsa on a
		// stock Ubuntu -- and a known_hosts holding only that host's ed25519
		// key answers "key mismatch", which reads as "the machine changed"
		// rather than "you have it under a different key type".
		HostKeyAlgorithms: knownAlgorithms(hostKey, addr),
	}

	// Dial through a context-aware dialer so a cancelled preflight does not sit
	// on an unreachable node until the TCP timeout.
	d := net.Dialer{Timeout: cfg.Timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("exec: dial %s: %w", addr, err)
	}

	c, chans, reqs, err := ssh.NewClientConn(conn, addr, clientCfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("exec: ssh handshake with %s: %w", addr, explainHostKey(err))
	}
	return &SSHRunner{host: cfg.Host, client: ssh.NewClient(c, chans, reqs)}, nil
}

// Run executes one command in its own session.
//
// A session per command is the SSH protocol's own model; the expensive part is
// the connection, which is shared.
// Run executes a command and returns when it is done.
func (r *SSHRunner) Run(ctx context.Context, cmd string) (Result, error) {
	return r.RunStream(ctx, cmd, nil)
}

// RunStream executes a command and reports each line of output as it arrives.
func (r *SSHRunner) RunStream(ctx context.Context, cmd string, onLine func(string)) (Result, error) {
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return Result{}, ErrNotConnected
	}

	sess, err := r.client.NewSession()
	if err != nil {
		return Result{}, fmt.Errorf("exec: %s: new session: %w", r.host, err)
	}
	defer sess.Close()

	stdout := &lineWriter{onLine: onLine}
	stderr := &lineWriter{onLine: onLine}
	sess.Stdout = stdout
	sess.Stderr = stderr

	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()

	select {
	case <-ctx.Done():
		// Closing the session is what actually interrupts the remote command;
		// abandoning the goroutine would leave it running on the node.
		_ = sess.Signal(ssh.SIGKILL)
		_ = sess.Close()
		return Result{Stdout: stdout.String(), Stderr: stderr.String()}, ctx.Err()

	case err := <-done:
		res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
		if err == nil {
			return res, nil
		}
		// A non-zero exit is an answer, not a failure to run.
		var exitErr *ssh.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitStatus()
			return res, nil
		}
		// A command killed by a signal has no exit status. 128+n is what a
		// shell reports, and preflight only needs "it did not succeed".
		var missing *ssh.ExitMissingError
		if errors.As(err, &missing) {
			res.ExitCode = 255
			return res, nil
		}
		return res, fmt.Errorf("exec: %s: %w", r.host, err)
	}
}

// Host is the address this runner talks to.
func (r *SSHRunner) Host() string { return r.host }

// Close ends the connection.
func (r *SSHRunner) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return r.client.Close()
}

// authMethods builds the authentication list, key first.
func authMethods(cfg SSHConfig) ([]ssh.AuthMethod, error) {
	var out []ssh.AuthMethod

	if len(cfg.PrivateKey) > 0 {
		var signer ssh.Signer
		var err error
		if cfg.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(cfg.PrivateKey, []byte(cfg.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(cfg.PrivateKey)
		}
		if err != nil {
			return nil, fmt.Errorf("exec: read private key: %w", err)
		}
		out = append(out, ssh.PublicKeys(signer))
	}

	if cfg.Password != "" {
		out = append(out, ssh.Password(cfg.Password))
		// Some sshd configurations answer password authentication only through
		// keyboard-interactive. Offering the same secret both ways costs
		// nothing and avoids a failure that reads as a wrong password.
		out = append(out, ssh.KeyboardInteractive(
			func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = cfg.Password
				}
				return answers, nil
			}))
	}

	if len(out) == 0 {
		return nil, errors.New("exec: no ssh credentials were supplied")
	}
	return out, nil
}

// hostKeyCallback decides how the node's identity is checked.
func hostKeyCallback(cfg SSHConfig) (ssh.HostKeyCallback, error) {
	if cfg.InsecureSkipHostKeyCheck {
		//nolint:gosec // the operator asked for this explicitly; see the field comment
		return ssh.InsecureIgnoreHostKey(), nil
	}

	path := cfg.KnownHosts
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("exec: locate known_hosts: %w", err)
		}
		path = home + "/.ssh/known_hosts"
	}
	cb, err := knownhosts.New(path)
	if err != nil {
		return nil, fmt.Errorf("exec: read %s: %w; "+
			"freshly installed nodes have no entry yet -- add them, or accept the risk explicitly", path, err)
	}
	return cb, nil
}

// knownAlgorithms reports the host key types known_hosts already holds for a
// host, in the form the handshake names them.
//
// There is no exported way to ask the database directly, so it is asked the way
// it answers: check a key that cannot match, and read the types it says it
// wanted. An unknown host yields nothing, and nothing means "no preference" --
// which is the right answer for a host whose key is about to be rejected
// anyway, on grounds that will name the host rather than an algorithm.
func knownAlgorithms(cb ssh.HostKeyCallback, addr string) []string {
	if cb == nil {
		return nil
	}
	probe, err := unmatchableKey()
	if err != nil {
		return nil
	}
	var keyErr *knownhosts.KeyError
	if !errors.As(cb(addr, &net.TCPAddr{IP: net.IPv4zero}, probe), &keyErr) || len(keyErr.Want) == 0 {
		return nil
	}

	seen := map[string]bool{}
	var out []string
	add := func(a string) {
		if a != "" && !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	for _, w := range keyErr.Want {
		t := w.Key.Type()
		// known_hosts records an RSA key as ssh-rsa whatever signature
		// algorithm the connection will use, and a server that has retired
		// SHA-1 refuses the bare name. The key is the same one.
		if t == ssh.KeyAlgoRSA {
			add(ssh.KeyAlgoRSASHA512)
			add(ssh.KeyAlgoRSASHA256)
		}
		add(t)
	}
	return out
}

// unmatchableKey is a freshly generated key, used only to make the known_hosts
// database report what it holds. It is never sent anywhere.
func unmatchableKey() (ssh.PublicKey, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	return signer.PublicKey(), nil
}

// explainHostKey rewrites the one handshake failure whose wording sends people
// the wrong way.
//
// x/crypto says "key mismatch" both when a host's key has genuinely changed and
// when known_hosts simply holds it under another type. The first is a reason to
// stop; the second is a reason to connect once with ssh(1). They deserve
// different sentences.
func explainHostKey(err error) error {
	var keyErr *knownhosts.KeyError
	if !errors.As(err, &keyErr) || len(keyErr.Want) == 0 {
		return err
	}
	types := map[string]bool{}
	var names []string
	for _, w := range keyErr.Want {
		if t := w.Key.Type(); !types[t] {
			types[t] = true
			names = append(names, t)
		}
	}
	return fmt.Errorf("%w; known_hosts holds this host as %s -- if the machine "+
		"was rebuilt the entry is stale, and if it was not, this is the warning it looks like",
		err, strings.Join(names, ", "))
}
