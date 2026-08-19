package rke2

import (
	"context"
	"strings"
	"testing"

	"platform.ryxen.dev/malmok/api/v1alpha1"
	"platform.ryxen.dev/malmok/internal/engine"
	"platform.ryxen.dev/malmok/internal/exec"
)

func agentNode() v1alpha1.NodeSpec {
	return v1alpha1.NodeSpec{Host: "192.168.88.244", Role: v1alpha1.RoleAgent}
}

func TestReadToken(t *testing.T) {
	t.Run("reads it", func(t *testing.T) {
		f := &exec.Fake{Responses: map[string]exec.Result{
			TokenFile: {Stdout: "K10abc::server:def\n"},
		}}
		got, err := ReadToken(context.Background(), f)
		if err != nil {
			t.Fatal(err)
		}
		if got != "K10abc::server:def" {
			t.Errorf("token is %q", got)
		}
	})

	t.Run("the server has not written one yet", func(t *testing.T) {
		f := &exec.Fake{Default: exec.Result{ExitCode: 1, Stderr: "No such file or directory\n"}}
		_, err := ReadToken(context.Background(), f)
		if err == nil {
			t.Fatal("a missing token file reported success")
		}
		// The reason is what tells an operator to wait rather than to go
		// looking for a configuration mistake.
		if !strings.Contains(err.Error(), "once it has started") {
			t.Errorf("the failure does not say why the file might be absent: %v", err)
		}
	})

	t.Run("an empty file is not a token", func(t *testing.T) {
		f := &exec.Fake{Responses: map[string]exec.Result{TokenFile: {Stdout: "\n"}}}
		if _, err := ReadToken(context.Background(), f); err == nil {
			t.Fatal("an empty token file reported success")
		}
	})
}

func TestAgentConfig(t *testing.T) {
	spec := clusterSpec()
	spec.Topology.Agents = []v1alpha1.NodeSpec{agentNode()}

	got := parse(t, AgentConfig(agentNode(), spec, "K10abc::server:def"))

	// ADR-008: a joining node points at the registration address, never at a
	// peer's own. A node that joined through a peer has that peer baked in, and
	// removing it later means re-joining every node that used it.
	if got["server"] != "https://k8s-api.acme.internal:9345" {
		t.Errorf("server is %v", got["server"])
	}
	if got["token"] != "K10abc::server:def" {
		t.Errorf("token is %v", got["token"])
	}
	// An agent runs no API server, so these belong to a server and not here.
	for _, key := range []string{"tls-san", "disable", "cni"} {
		if _, ok := got[key]; ok {
			t.Errorf("an agent config carries %s", key)
		}
	}
}

// Labels and taints are what make a node worth adding rather than just another
// machine, and they have to be one sequence per key.
func TestAgentConfigCarriesLabelsAndTaints(t *testing.T) {
	node := agentNode()
	node.Labels = map[string]string{"node-role": "worker", "zone": "dmz"}
	node.Taints = []string{"dedicated=gpu:NoSchedule"}

	got := parse(t, AgentConfig(node, clusterSpec(), "t"))

	labels, _ := got["node-label"].([]any)
	if len(labels) != 2 {
		t.Fatalf("node-label holds %v", labels)
	}
	// Sorted, so the rendered file does not change when a map iterates
	// differently and the config check does not report a spurious difference.
	if labels[0] != "node-role=worker" || labels[1] != "zone=dmz" {
		t.Errorf("node-label is %v, want it sorted", labels)
	}
	if taints, _ := got["node-taint"].([]any); len(taints) != 1 {
		t.Errorf("node-taint is %v", taints)
	}
}

// A second server joins the first through the registration address too, and
// keeps everything a server needs.
func TestJoiningServerConfigKeepsServerSettings(t *testing.T) {
	spec := clusterSpec()
	second := v1alpha1.NodeSpec{Host: "192.168.88.242", Role: v1alpha1.RoleServer}
	spec.Topology.Servers = append(spec.Topology.Servers, second)

	got := parse(t, withServerURL(ServerConfig(second, spec, "K10abc::server:def"), spec))

	if got["server"] != "https://k8s-api.acme.internal:9345" {
		t.Errorf("server is %v", got["server"])
	}
	if got["token"] != "K10abc::server:def" {
		t.Errorf("token is %v", got["token"])
	}
	if _, ok := got["tls-san"]; !ok {
		t.Error("a joining server has no tls-san")
	}
	if got["cni"] != "cilium" {
		t.Errorf("cni is %v", got["cni"])
	}
	if !strings.HasPrefix(withServerURL(ServerConfig(second, spec, "t"), spec), managedFileHeader) {
		t.Error("the marker is no longer the first line, so PF-802 would not find it")
	}
}

// The first server has no server: line -- it is the one being joined.
func TestFirstServerHasNoJoinTarget(t *testing.T) {
	if _, ok := parse(t, ServerConfig(serverNode(), clusterSpec(), ""))["server"]; ok {
		t.Error("the first server was told to join something")
	}
}

// ---------------------------------------------------------------------------
// Steps
// ---------------------------------------------------------------------------

// An agent has no kubeconfig, so whether it joined can only be answered by the
// control plane. Asking the agent about its own kubelet answers a different and
// much weaker question.
func TestRegisteredStepRunsOnTheControlPlane(t *testing.T) {
	node := &exec.Fake{}
	control := &exec.Fake{}
	steps := JoinSteps(node, control, clusterSpec(), agentNode(), "t", Options{})

	last := steps[len(steps)-1].(*engine.ShellStep)
	if last.Name != "registered" {
		t.Fatalf("the last step is %q", last.Name)
	}
	if last.Runner != control {
		t.Error("the readiness step runs on the joining node, which cannot answer it")
	}
	// The id still names the node being joined: that is what the state file
	// records and what an operator is watching.
	if !strings.HasSuffix(last.ID(), "@192.168.88.244") {
		t.Errorf("the step id is %q", last.ID())
	}

	// Everything else runs on the node itself.
	for _, s := range steps[:len(steps)-1] {
		if s.(*engine.ShellStep).Runner != node {
			t.Errorf("%s does not run on the joining node", s.ID())
		}
	}
}

// A hostname depends on what a node calls itself and on whether node-name was
// set; the address is what the document says and what the operator typed.
func TestRegisteredStepMatchesByAddress(t *testing.T) {
	s := registeredStep(agentNode(), Options{})
	if !strings.Contains(s.Check, "192.168.88.244") {
		t.Error("the readiness check does not look for the node's address")
	}

	node := agentNode()
	node.NodeIP = "10.0.0.44"
	if !strings.Contains(registeredStep(node, Options{}).Check, "10.0.0.44") {
		t.Error("nodeIP is ignored when it is set, so a multi-homed node would not be found")
	}
}

// A unit that will not start is a different problem from a node the control
// plane has not accepted; reporting them as one sends people to the wrong logs.
func TestJoinSplitsStartingFromRegistering(t *testing.T) {
	steps := JoinSteps(&exec.Fake{}, &exec.Fake{}, clusterSpec(), agentNode(), "t", Options{})
	names := make([]string, 0, len(steps))
	for _, s := range steps {
		names = append(names, s.(*engine.ShellStep).Name)
	}
	want := []string{"install", "config", "service", "registered"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("the join steps are %v, want %v", names, want)
	}
}

// A server joins with the server unit and an agent with the agent unit, and
// each is filed under its own phase.
func TestJoinPhaseFollowsTheRole(t *testing.T) {
	agent := JoinSteps(&exec.Fake{}, &exec.Fake{}, clusterSpec(), agentNode(), "t", Options{})
	if !strings.HasPrefix(agent[0].ID(), PhaseJoinAgent+"/") {
		t.Errorf("an agent is filed under %q", agent[0].ID())
	}
	if !strings.Contains(agent[2].(*engine.ShellStep).Check, "rke2-agent") {
		t.Error("an agent does not start the agent unit")
	}

	server := v1alpha1.NodeSpec{Host: "192.168.88.242", Role: v1alpha1.RoleServer}
	joined := JoinSteps(&exec.Fake{}, &exec.Fake{}, clusterSpec(), server, "t", Options{})
	if !strings.HasPrefix(joined[0].ID(), PhaseJoinServer+"/") {
		t.Errorf("a server is filed under %q", joined[0].ID())
	}
	if !strings.Contains(joined[2].(*engine.ShellStep).Check, "rke2-server") {
		t.Error("a joining server does not start the server unit")
	}
	if !strings.Contains(joined[0].(*engine.ShellStep).Do, "INSTALL_RKE2_TYPE=server") {
		t.Error("a joining server installs the agent build")
	}
}

// Observe must not change anything, on either runner.
func TestJoinObserveChangesNothing(t *testing.T) {
	node, control := &exec.Fake{Default: exec.Result{ExitCode: 1}}, &exec.Fake{Default: exec.Result{ExitCode: 1}}
	for _, s := range JoinSteps(node, control, clusterSpec(), agentNode(), "t", Options{}) {
		if _, err := s.Observe(context.Background()); err != nil {
			t.Fatalf("%s: %v", s.ID(), err)
		}
	}
	for _, f := range []*exec.Fake{node, control} {
		for _, cmd := range f.Log {
			for _, bad := range []string{
				"install.sh", "get.rke2.io", "systemctl enable", "systemctl start",
				"install -d", "chmod ", "> /etc/", "kubectl apply", "kubectl delete",
			} {
				if strings.Contains(cmd, bad) {
					t.Errorf("an Observe would have changed something: it contains %q\n%s", bad, cmd)
				}
			}
		}
	}
}

// The config file carries the cluster token on every joining node.
func TestJoinConfigNeverPrintsTheToken(t *testing.T) {
	steps := JoinSteps(&exec.Fake{Default: exec.Result{
		ExitCode: 1, Stdout: "/etc/rancher/rke2/config.yaml differs from the document\n",
	}}, &exec.Fake{}, clusterSpec(), agentNode(), "K10secret::server:token", Options{})

	obs, err := steps[1].Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(obs.Detail+obs.Evidence, "K10secret") {
		t.Errorf("the token reached the event stream: %q %q", obs.Detail, obs.Evidence)
	}
}
