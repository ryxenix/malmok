package exec

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"platform.ryxen.dev/platformctl/api/v1alpha1"
)

// The local runner has to behave exactly like the SSH one, because every probe
// and every step is written against the Runner contract and none of them knows
// which it has.
func TestLocalRunnerFollowsTheRunnerContract(t *testing.T) {
	l := NewLocal("10.0.0.11")

	tests := []struct {
		name     string
		cmd      string
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{name: "stdout and a zero exit", cmd: "echo hello", wantOut: "hello"},
		{
			// A non-zero exit is an answer, not a fault: `test -e` failing is
			// the probe working, and returning an error for it would make every
			// negative measurement look like an unreachable node.
			name: "a non-zero exit is not an error",
			cmd:  "exit 3", wantCode: 3,
		},
		{name: "stderr is kept separate", cmd: "echo oops >&2", wantErr: "oops"},
		{
			// The command is run through a shell, not as argv. Probes pipe,
			// test and redirect, and an argv path would be a second dialect.
			name: "a pipeline runs", cmd: "printf 'a\\nb\\n' | grep -c .", wantOut: "2",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := l.Run(context.Background(), tc.cmd)
			if err != nil {
				t.Fatalf("the command could not be run: %v", err)
			}
			if res.ExitCode != tc.wantCode {
				t.Errorf("exit %d, want %d", res.ExitCode, tc.wantCode)
			}
			if res.Out() != tc.wantOut {
				t.Errorf("stdout %q, want %q", res.Out(), tc.wantOut)
			}
			if res.Err() != tc.wantErr {
				t.Errorf("stderr %q, want %q", res.Err(), tc.wantErr)
			}
		})
	}

	// The node keeps the name the document gave it, so a diagnostic names the
	// node the operator named rather than always saying "local".
	if l.Host() != "10.0.0.11" {
		t.Errorf("Host = %q", l.Host())
	}
}

// A context that is already done must not run anything.
func TestLocalRunnerHonoursTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := NewLocal("").Run(ctx, "echo hello"); err == nil {
		t.Error("a cancelled context still ran a command")
	}
}

// An address either is or is not assigned to an interface here, which is the
// whole question. Nothing has to be told.
func TestIsLocal(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"local", true},
		{"localhost", true},
		{"LOCALHOST", true},
		{"  local  ", true},
		{"127.0.0.1", true},
		{"127.0.0.53", true},
		{"::1", true},
		{"10.255.255.254", false},
		{"192.0.2.10", false},
		{"", false},
		// A name is not resolved. A tool that installed locally because a name
		// happened to point at 127.0.0.1 in one resolver's view would be
		// surprising in a way that is hard to undo.
		{"node1.acme.internal", false},
	}
	for _, tc := range tests {
		t.Run(tc.host, func(t *testing.T) {
			if got := IsLocal(tc.host); got != tc.want {
				t.Errorf("IsLocal(%q) = %v", tc.host, got)
			}
		})
	}

	// And an address this machine actually holds is this machine.
	for _, ip := range localAddresses() {
		if ip.IsLoopback() || ip.To4() == nil {
			continue
		}
		if !IsLocal(ip.String()) {
			t.Errorf("%s is assigned to this machine and was not recognised", ip)
		}
		return
	}
}

// Both overrides are real, and the document has to be able to say either.
func TestNodeIsLocalOverrides(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name string
		node v1alpha1.NodeSpec
		want bool
	}{
		{
			name: "an address that is not this machine's",
			node: v1alpha1.NodeSpec{Host: "192.0.2.10"},
		},
		{
			// A container with host networking sees the host's addresses and is
			// not the host. The address alone cannot tell them apart, so the
			// document has to be able to.
			name: "forced remote despite a local address",
			node: v1alpha1.NodeSpec{Host: "127.0.0.1", Local: &no},
			want: false,
		},
		{
			// A node behind NAT, or one named by the VIP it will carry once the
			// cluster is up, is still this machine.
			name: "forced local despite a remote address",
			node: v1alpha1.NodeSpec{Host: "192.0.2.10", Local: &yes},
			want: true,
		},
		{name: "loopback", node: v1alpha1.NodeSpec{Host: "127.0.0.1"}, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NodeIsLocal(tc.node); got != tc.want {
				t.Errorf("NodeIsLocal = %v, want %v", got, tc.want)
			}
		})
	}
}

// A local node opens no connection, so its credential fields mean nothing.
// Filling them with defaults would put "root" in a diagnostic about a machine
// nobody logged into.
func TestFromSpecRoutesLocally(t *testing.T) {
	local := FromSpec(v1alpha1.NodeSpec{Host: "localhost"})
	if !local.Local {
		t.Fatal("a loopback node was not routed locally")
	}
	if local.User != "" || local.Port != 0 {
		t.Errorf("a local node was given credentials: user=%q port=%d", local.User, local.Port)
	}

	remote := FromSpec(v1alpha1.NodeSpec{Host: "192.0.2.10"})
	if remote.Local {
		t.Error("a remote node was routed locally")
	}
	if remote.User != "root" || remote.Port != 22 {
		t.Errorf("the SSH defaults were not applied: user=%q port=%d", remote.User, remote.Port)
	}
}

// Connect is one function so preflight, the build and the upgrade cannot
// disagree about which machine a node is.
func TestConnectRoutesByConfig(t *testing.T) {
	r, err := Connect(context.Background(), SSHConfig{Host: "localhost", Local: true})
	if err != nil {
		t.Fatalf("%v", err)
	}
	defer r.Close()
	if _, ok := r.(*LocalRunner); !ok {
		t.Errorf("a local node got a %T", r)
	}
}

// A tool invoked with `sudo platformctl` is already root, and wrapping every
// command in another sudo needs the binary installed and the account in the
// sudoers file for no gain. A minimal image frequently has neither.
func TestSudoLeavesAnAlreadyRootRunnerAlone(t *testing.T) {
	root := &fakeRooted{root: true}
	if _, err := (Sudo{Runner: root, Password: "pw"}).Run(context.Background(), "id -u"); err != nil {
		t.Fatal(err)
	}
	if got := root.last; got != "id -u" {
		t.Errorf("a root runner was wrapped: %q", got)
	}

	notRoot := &fakeRooted{}
	if _, err := (Sudo{Runner: notRoot, Password: "pw"}).Run(context.Background(), "id -u"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notRoot.last, "sudo -S") {
		t.Errorf("an unprivileged runner was not elevated: %q", notRoot.last)
	}

	// A runner that says nothing about itself is elevated, which is the safe
	// default: the SSH path cannot answer this cheaply and has always sudo'd.
	plain := &Fake{}
	if _, err := (Sudo{Runner: plain}).Run(context.Background(), "id -u"); err != nil {
		t.Fatal(err)
	}
	if len(plain.Log) == 0 || !strings.Contains(plain.Log[0], "sudo -n") {
		t.Errorf("a runner with no opinion was not elevated: %v", plain.Log)
	}
}

// The real thing, on the machine running the tests: a local runner reports the
// same user id the process has, which is what makes the elevation decision
// meaningful rather than a guess.
func TestLocalRunnerRunsAsThisProcess(t *testing.T) {
	res, err := NewLocal("").Run(context.Background(), "id -u")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := res.Out(), strconv.Itoa(os.Geteuid()); got != want {
		t.Errorf("the local runner is uid %s, this process is %s", got, want)
	}
	if NewLocal("").IsRoot() != (os.Geteuid() == 0) {
		t.Error("IsRoot disagrees with the process")
	}
}

type fakeRooted struct {
	root bool
	last string
}

func (f *fakeRooted) Run(_ context.Context, cmd string) (Result, error) {
	f.last = cmd
	return Result{}, nil
}
func (f *fakeRooted) Host() string { return "fake" }
func (f *fakeRooted) Close() error { return nil }
func (f *fakeRooted) IsRoot() bool { return f.root }

// A development box carries a bridge per docker network, and a chooser that
// lists thirty 172.x gateways around the one address the LAN reaches is a
// chooser nobody can use. The filter is about what to offer -- IsLocal still
// accepts a bridge address, because a document that names one still means this
// machine.
func TestLocalIPv4sSkipsVirtualInterfaces(t *testing.T) {
	for name, virtual := range map[string]bool{
		"docker0": true, "br-1a2b3c": true, "veth12ab": true, "cilium_host": true,
		"virbr0": true, "cni0": true, "wg0": true, "tailscale0": true,
		"eth0": false, "enp6s18": false, "eno1": false, "wlan0": false, "bond0": false,
	} {
		if got := virtualInterface(name); got != virtual {
			t.Errorf("virtualInterface(%q) = %v", name, got)
		}
	}
}
