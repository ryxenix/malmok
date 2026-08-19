package upgrade

import (
	"context"
	"strings"
	"testing"
	"time"

	"platform.ryxen.dev/malmok/api/v1alpha1"
	"platform.ryxen.dev/malmok/internal/codes"
	"platform.ryxen.dev/malmok/internal/engine"
	"platform.ryxen.dev/malmok/internal/exec"
	"platform.ryxen.dev/malmok/internal/preflight"
	"platform.ryxen.dev/malmok/internal/rke2"
)

func mustParse(t *testing.T, s string) Version {
	t.Helper()
	v, err := ParseVersion(s)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return v
}

// A near-miss is what an operator types under time pressure, and its failure
// mode is an installer that downloads nothing and a node that stays where it
// was.
func TestParseVersion(t *testing.T) {
	tests := []struct {
		in   string
		ok   bool
		want Version
	}{
		{in: "v1.34.10+rke2r1", ok: true, want: Version{1, 34, 10, 1, "v1.34.10+rke2r1"}},
		{in: "  v1.35.7+rke2r2  ", ok: true, want: Version{1, 35, 7, 2, "v1.35.7+rke2r2"}},
		{in: "1.34.10+rke2r1"},        // no v
		{in: "v1.34.10"},              // no build
		{in: "v1.34+rke2r1"},          // no patch
		{in: "v1.34.10-rc6+rke2r1"},   // a prerelease is not a release
		{in: "v1.34.10+rke2r1-dirty"}, // trailing anything
		{in: "latest"},
		{in: ""},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseVersion(tc.in)
			if tc.ok != (err == nil) {
				t.Fatalf("err = %v", err)
			}
			if err != nil {
				if !strings.Contains(err.Error(), "v1.34.10+rke2r1") {
					t.Errorf("the message does not show the shape: %v", err)
				}
				return
			}
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// The build number is part of the version. +rke2r2 is a real release with real
// fixes over +rke2r1, and treating the suffix as decoration would make the tool
// call an upgrade satisfied when it is not.
func TestCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"v1.34.10+rke2r1", "v1.34.10+rke2r1", 0},
		{"v1.34.10+rke2r1", "v1.34.10+rke2r2", -1},
		{"v1.34.10+rke2r1", "v1.34.11+rke2r1", -1},
		{"v1.34.10+rke2r1", "v1.35.0+rke2r1", -1},
		{"v2.0.0+rke2r1", "v1.99.99+rke2r9", 1},
		// Not string order: 9 comes before 10.
		{"v1.9.0+rke2r1", "v1.10.0+rke2r1", -1},
	}
	for _, tc := range tests {
		t.Run(tc.a+" vs "+tc.b, func(t *testing.T) {
			a, b := mustParse(t, tc.a), mustParse(t, tc.b)
			if got := a.Compare(b); got != tc.want {
				t.Errorf("Compare = %d, want %d", got, tc.want)
			}
			if got := b.Compare(a); got != -tc.want {
				t.Errorf("the reverse comparison is %d", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Preconditions
// ---------------------------------------------------------------------------

func state(t *testing.T, nodes ...NodeState) State {
	t.Helper()
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	return State{Nodes: nodes, Now: now, LastSnapshot: now.Add(-time.Hour)}
}

func server(t *testing.T, host, version string) NodeState {
	t.Helper()
	return NodeState{Host: host, Version: mustParse(t, version), Ready: true}
}

func agent(t *testing.T, host, version string) NodeState {
	t.Helper()
	n := server(t, host, version)
	n.Agent = true
	return n
}

// failed reports which codes stopped the upgrade.
func failed(results []preflight.ProbeResult) []string {
	var out []string
	for _, r := range results {
		if r.Failed() {
			out = append(out, r.ID)
		}
	}
	return out
}

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// Every rule here is one somebody paid for.
func TestCheck(t *testing.T) {
	tests := []struct {
		name     string
		st       State
		target   string
		wantFail []string
		wantOK   bool
	}{
		{
			name:   "one minor forward with everything Ready",
			st:     state(t, server(t, "10.0.0.11", "v1.34.10+rke2r1"), agent(t, "10.0.0.21", "v1.34.10+rke2r1")),
			target: "v1.35.7+rke2r1",
			wantOK: true,
		},
		{
			name:   "a patch within the same minor",
			st:     state(t, server(t, "10.0.0.11", "v1.34.10+rke2r1")),
			target: "v1.34.11+rke2r1",
			wantOK: true,
		},
		{
			name:     "a version that is not one",
			st:       state(t, server(t, "10.0.0.11", "v1.34.10+rke2r1")),
			target:   "1.35",
			wantFail: []string{"UP-001"},
		},
		{
			// etcd has no downgrade: the API server writes storage the older one
			// cannot read, and a snapshot restore is the only way back.
			name:     "backwards",
			st:       state(t, server(t, "10.0.0.11", "v1.35.7+rke2r1")),
			target:   "v1.34.10+rke2r1",
			wantFail: []string{"UP-002", "UP-004"},
		},
		{
			name:     "the same version again",
			st:       state(t, server(t, "10.0.0.11", "v1.34.10+rke2r1")),
			target:   "v1.34.10+rke2r1",
			wantFail: []string{"UP-002"},
		},
		{
			name:     "skipping a minor",
			st:       state(t, server(t, "10.0.0.11", "v1.34.10+rke2r1")),
			target:   "v1.36.3+rke2r1",
			wantFail: []string{"UP-003"},
		},
		{
			name:     "a major boundary is not one step",
			st:       state(t, server(t, "10.0.0.11", "v1.99.0+rke2r1")),
			target:   "v2.0.0+rke2r1",
			wantFail: []string{"UP-003"},
		},
		{
			// The bound is the node with furthest to come, not the one that is
			// furthest ahead.
			name: "the oldest server sets the step",
			st: state(t, server(t, "10.0.0.11", "v1.34.10+rke2r1"),
				server(t, "10.0.0.12", "v1.35.7+rke2r1")),
			target:   "v1.36.3+rke2r1",
			wantFail: []string{"UP-003"},
		},
		{
			name: "an agent that leads its servers",
			st: state(t, server(t, "10.0.0.11", "v1.34.10+rke2r1"),
				agent(t, "10.0.0.21", "v1.35.7+rke2r1")),
			target:   "v1.35.7+rke2r1",
			wantFail: []string{"UP-005"},
		},
		{
			name: "a node that is not Ready",
			st: state(t, server(t, "10.0.0.11", "v1.34.10+rke2r1"),
				NodeState{Host: "10.0.0.21", Agent: true,
					Version: mustParse(t, "v1.34.10+rke2r1"), Ready: false}),
			target:   "v1.35.7+rke2r1",
			wantFail: []string{"UP-101"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			results := Check(tc.st, tc.target)
			got := failed(results)

			for _, want := range tc.wantFail {
				if !has(got, want) {
					t.Errorf("%s did not fail; failures were %v", want, got)
				}
			}
			if tc.wantOK && len(Blocking(results)) > 0 {
				t.Errorf("the upgrade was blocked by %v", failed(results))
			}
			if !tc.wantOK && len(Blocking(results)) == 0 {
				t.Errorf("nothing blocked the upgrade; failures were %v", got)
			}
		})
	}
}

// A malformed target stops everything. Every rule after it is a comparison
// against a version that was never understood, and reporting eight failures
// about it would bury the one that matters.
func TestAMalformedTargetIsTheOnlyFinding(t *testing.T) {
	results := Check(state(t, server(t, "10.0.0.11", "v1.34.10+rke2r1")), "nonsense")
	if len(results) != 1 || results[0].ID != "UP-001" {
		t.Errorf("got %d findings: %v", len(results), failed(results))
	}
}

// A single-node cluster is a supported shape, and this is what upgrading one
// means. It is recorded so the restart is not a surprise afterwards.
func TestASingleNodeIsToldItsWorkloadsRestart(t *testing.T) {
	results := Check(state(t, server(t, "10.0.0.11", "v1.34.10+rke2r1")), "v1.35.7+rke2r1")

	var found *preflight.ProbeResult
	for i := range results {
		if results[i].ID == "UP-102" {
			found = &results[i]
		}
	}
	if found == nil {
		t.Fatalf("nothing said what happens to the workloads: %v", results)
	}
	if found.Failed() {
		t.Error("having one node was reported as a fault")
	}
	if len(Blocking(results)) > 0 {
		t.Errorf("a single-node upgrade was blocked: %v", failed(results))
	}
}

// A failed control plane upgrade is recovered by restoring a snapshot, and the
// time to find out there is none is not afterwards.
func TestSnapshotFreshness(t *testing.T) {
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		snapshot time.Time
		wantFail bool
	}{
		{name: "an hour old", snapshot: now.Add(-time.Hour)},
		{name: "none at all", wantFail: true},
		{name: "a week old", snapshot: now.Add(-7 * 24 * time.Hour), wantFail: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := State{
				Nodes:        []NodeState{server(t, "10.0.0.11", "v1.34.10+rke2r1")},
				Now:          now,
				LastSnapshot: tc.snapshot,
			}
			results := Check(st, "v1.35.7+rke2r1")
			if got := has(failed(results), "UP-103"); got != tc.wantFail {
				t.Errorf("UP-103 failed = %v", got)
			}
			// A stale snapshot is a warning, not a refusal. Somebody upgrading a
			// lab cluster does not need this tool to stop them.
			if len(Blocking(results)) > 0 {
				t.Errorf("a missing snapshot blocked the upgrade: %v", failed(results))
			}
		})
	}
}

// Every code this package emits has to exist in the registry, or the renderer
// draws a code nobody can look up and the audit report cites nothing.
func TestEveryFindingIsARegisteredCode(t *testing.T) {
	sts := []State{
		state(t, server(t, "10.0.0.11", "v1.34.10+rke2r1")),
		state(t, server(t, "10.0.0.11", "v1.35.7+rke2r1"), agent(t, "10.0.0.21", "v1.36.3+rke2r1")),
		{Nodes: []NodeState{{Host: "10.0.0.11"}}},
	}
	for _, st := range sts {
		for _, r := range Check(st, "v1.35.7+rke2r1") {
			c, ok := codes.Lookup(r.ID)
			if !ok {
				t.Errorf("%s is not in the registry", r.ID)
				continue
			}
			if c.Family != codes.FamilyUpgrade {
				t.Errorf("%s is in family %s", r.ID, c.Family)
			}
			if r.Severity != c.Severity {
				t.Errorf("%s reports severity %q, the registry says %q", r.ID, r.Severity, c.Severity)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Phases
// ---------------------------------------------------------------------------

func twoNodeSpec() v1alpha1.ClusterSpec {
	return v1alpha1.ClusterSpec{
		Topology: v1alpha1.TopologySpec{
			Servers: []v1alpha1.NodeSpec{{Host: "10.0.0.11"}},
			Agents:  []v1alpha1.NodeSpec{{Host: "10.0.0.21"}},
		},
	}
}

func runners() Runners {
	return Runners{
		ByHost:  map[string]exec.Runner{"10.0.0.11": &exec.Fake{}, "10.0.0.21": &exec.Fake{}},
		Control: &exec.Fake{},
	}
}

// Servers before agents, and one node at a time. A worker brought up first puts
// the cluster outside the supported skew until the servers catch up, and two
// control plane members restarting together is a restore rather than a retry.
func TestPhaseOrderAndTraversal(t *testing.T) {
	phases, err := Phases(twoNodeSpec(), "v1.35.7+rke2r1", runners(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(phases) != 2 {
		t.Fatalf("%d phases", len(phases))
	}
	if phases[0].ID != PhaseServers || phases[1].ID != PhaseAgents {
		t.Errorf("order is %s then %s", phases[0].ID, phases[1].ID)
	}
	for _, p := range phases {
		if p.Traversal != engine.TraversalSequential {
			t.Errorf("%s walks its nodes %v", p.ID, p.Traversal)
		}
		// Every node restarts its kubelet, so workloads move whatever the drain
		// does. Anything softer would put a maintenance window behind a grade
		// that says there is no need for one.
		if p.Grade != engine.GradeDisruptive {
			t.Errorf("%s is graded %v", p.ID, p.Grade)
		}
	}
}

func stepNames(steps []engine.Step) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		if st, ok := s.(*engine.ShellStep); ok {
			out = append(out, st.Name)
			continue
		}
		out = append(out, s.ID())
	}
	return out
}

// Get the workloads off, put the new binary down, restart into it, wait for the
// cluster to agree, and only then let work back on.
func TestNodeStepOrder(t *testing.T) {
	phases, err := Phases(twoNodeSpec(), "v1.35.7+rke2r1", runners(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := stepNames(phases[0].Steps("10.0.0.11"))
	want := []string{"drain", "install", "restart", "upgraded", "uncordon"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("steps are %v, want %v", got, want)
	}
}

// There is nowhere to drain to, so draining would evict workloads that have no
// other home and then wait for them.
func TestASingleNodeIsNotDrained(t *testing.T) {
	spec := v1alpha1.ClusterSpec{
		Topology: v1alpha1.TopologySpec{Servers: []v1alpha1.NodeSpec{{Host: "10.0.0.11"}}},
	}
	phases, err := Phases(spec, "v1.35.7+rke2r1", Runners{
		ByHost: map[string]exec.Runner{"10.0.0.11": &exec.Fake{}},
	}, Options{SingleNode: true})
	if err != nil {
		t.Fatal(err)
	}
	got := stepNames(phases[0].Steps("10.0.0.11"))
	for _, unwanted := range []string{"drain", "uncordon"} {
		if has(got, unwanted) {
			t.Errorf("a single node is %sed: %v", unwanted, got)
		}
	}
	if !has(got, "upgraded") {
		t.Errorf("a single node is never checked: %v", got)
	}
}

// The step that makes the phase honest.
//
// A binary on disk at the new version and a unit that restarted are both true
// of a node whose kubelet is crash-looping. What has to be true is that the
// cluster reports this node Ready and running the version that was asked for.
func TestTheLastWordIsWhatTheClusterReports(t *testing.T) {
	phases, err := Phases(twoNodeSpec(), "v1.35.7+rke2r1", runners(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	steps := phases[0].Steps("10.0.0.11")

	var upgraded *engine.ShellStep
	for _, s := range steps {
		if st, ok := s.(*engine.ShellStep); ok && st.Name == "upgraded" {
			upgraded = st
		}
	}
	if upgraded == nil {
		t.Fatal("there is no step that asks the cluster")
	}
	for _, want := range []string{"kubeletVersion", "v1.35.7+rke2r1", "True"} {
		if !strings.Contains(upgraded.Check, want) {
			t.Errorf("the check does not look for %q:\n%s", want, upgraded.Check)
		}
	}
	// Asked from a node that stayed up. The one that just restarted is the
	// least able to report on its own progress.
	if upgraded.Runner == nil {
		t.Fatal("the step has no runner")
	}
}

// The restart is a separate fact from the install: the installer replaces the
// files and the process keeps running the old ones until something restarts it.
func TestRestartIsMeasuredAgainstTheInstalledBinary(t *testing.T) {
	phases, err := Phases(twoNodeSpec(), "v1.35.7+rke2r1", runners(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range phases[0].Steps("10.0.0.11") {
		st, ok := s.(*engine.ShellStep)
		if !ok || st.Name != "restart" {
			continue
		}
		for _, want := range []string{"ExecMainStartTimestamp", "command -v rke2"} {
			if !strings.Contains(st.Check, want) {
				t.Errorf("the check does not compare the unit with the binary (%q missing):\n%s",
					want, st.Check)
			}
		}
		// Change time, not modification time. The installer unpacks a tarball
		// and tar restores the archive's timestamps, so a freshly installed
		// binary carries an mtime from the day upstream built it. A live
		// upgrade put v1.35.7 on disk with an mtime eight days older than the
		// running unit, the check called it current, and the node kept serving
		// v1.34.10 with the new binary sitting beside it.
		if !strings.Contains(st.Check, "stat -c %Z") {
			t.Errorf("the check reads a timestamp an archive can carry:\n%s", st.Check)
		}
		if strings.Contains(st.Check, "stat -c %Y") {
			t.Errorf("the check compares modification time:\n%s", st.Check)
		}
		return
	}
	t.Fatal("there is no restart step")
}

// A disruption budget is somebody's statement about how much of their service
// may be down. The tool is not the one to overrule it unasked.
func TestTheDrainRespectsDisruptionBudgetsUnlessTold(t *testing.T) {
	find := func(o Options) *engine.ShellStep {
		phases, err := Phases(twoNodeSpec(), "v1.35.7+rke2r1", runners(), o)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range phases[0].Steps("10.0.0.11") {
			if st, ok := s.(*engine.ShellStep); ok && st.Name == "drain" {
				return st
			}
		}
		t.Fatal("there is no drain step")
		return nil
	}

	if got := find(Options{}); strings.Contains(got.Do, "disable-eviction") {
		t.Errorf("the default drain overrules a disruption budget:\n%s", got.Do)
	}
	if got := find(Options{Force: true}); !strings.Contains(got.Do, "disable-eviction") {
		t.Errorf("--force did not reach the drain:\n%s", got.Do)
	}
}

// The upgrade must not install a version it was never given.
func TestTheInstalledVersionIsTheTarget(t *testing.T) {
	phases, err := Phases(twoNodeSpec(), "v1.35.7+rke2r1", runners(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range phases[0].Steps("10.0.0.11") {
		st, ok := s.(*engine.ShellStep)
		if !ok || st.Name != "install" {
			continue
		}
		if !strings.Contains(st.Do, "INSTALL_RKE2_VERSION='v1.35.7+rke2r1'") {
			t.Errorf("the install does not pin the target:\n%s", st.Do)
		}
		if !strings.Contains(st.Do, "INSTALL_RKE2_TYPE=server") {
			t.Errorf("a server was not installed as one:\n%s", st.Do)
		}
		return
	}
	t.Fatal("there is no install step")
}

// A target that is not a version is refused before any phase is built, rather
// than by the first node to try to install it.
func TestPhasesRefuseAMalformedTarget(t *testing.T) {
	if _, err := Phases(twoNodeSpec(), "latest", runners(), Options{}); err == nil {
		t.Error("`latest` was accepted as an upgrade target")
	}
}

func TestObserveChangesNothing(t *testing.T) {
	f := &exec.Fake{Default: exec.Result{ExitCode: 1}}
	r := Runners{ByHost: map[string]exec.Runner{"10.0.0.11": f, "10.0.0.21": f}, Control: f}

	phases, err := Phases(twoNodeSpec(), "v1.35.7+rke2r1", r, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range phases {
		for _, node := range p.Nodes {
			for _, s := range p.Steps(node) {
				if _, err := s.Observe(context.Background()); err != nil {
					t.Fatalf("%s: %v", s.ID(), err)
				}
			}
		}
	}
	for _, cmd := range f.Log {
		for _, bad := range []string{
			"kubectl drain", "kubectl cordon", "kubectl uncordon",
			"systemctl restart", "systemctl daemon-reload", "install.sh",
		} {
			if strings.Contains(cmd, bad) {
				t.Errorf("an Observe would have changed the cluster: it contains %q\n%s", bad, cmd)
			}
		}
	}
}

func TestStepIDs(t *testing.T) {
	phases, err := Phases(twoNodeSpec(), "v1.35.7+rke2r1", runners(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range phases {
		for _, node := range p.Nodes {
			for _, s := range p.Steps(node) {
				name, host, ok := strings.Cut(s.ID(), "@")
				if !ok {
					t.Errorf("%q does not name a node", s.ID())
				}
				// The id has to name the node the step is about, not the node
				// the command happens to run on: state.json keys on it, and a
				// resume that could not tell two nodes' drains apart would skip
				// the second one.
				if host != node {
					t.Errorf("%q is filed under %s", s.ID(), node)
				}
				if !strings.HasPrefix(name, p.ID+"/") {
					t.Errorf("%q is not filed under %s", s.ID(), p.ID)
				}
			}
		}
	}
}

// The kubectl prelude has to be there: RKE2 keeps its binaries outside PATH and
// its kubeconfig outside the default location, so a cluster-scoped step without
// it fails with "command not found" and reads like the cluster is gone.
func TestClusterStepsCarryTheKubectlPrelude(t *testing.T) {
	phases, err := Phases(twoNodeSpec(), "v1.35.7+rke2r1", runners(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range phases[0].Steps("10.0.0.11") {
		st, ok := s.(*engine.ShellStep)
		if !ok || !strings.Contains(st.Check, "kubectl") {
			continue
		}
		if !strings.Contains(st.Check, rke2.BinDir) {
			t.Errorf("%s uses kubectl without the prelude:\n%s", st.Name, st.Check)
		}
	}
}

// A drain leaves two kinds of pod behind on a node it emptied perfectly, and
// counting them as work still to do fails the step on a node that is done.
//
// DaemonSet pods come back immediately by design; mirror pods -- owner kind
// Node -- are the static manifests that make up the control plane. The first
// version of this excluded `kube-system/etcd-` by name and left the API server,
// the scheduler, the controller manager and the cloud controller manager
// counted. A live server drained cleanly and the step reported five pods
// remaining.
func TestTheDrainCountsByOwnerNotByName(t *testing.T) {
	phases, err := Phases(twoNodeSpec(), "v1.35.7+rke2r1", runners(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	var drain *engine.ShellStep
	for _, s := range phases[0].Steps("10.0.0.11") {
		if st, ok := s.(*engine.ShellStep); ok && st.Name == "drain" {
			drain = st
		}
	}
	if drain == nil {
		t.Fatal("there is no drain step")
	}

	if !strings.Contains(drain.Check, "ownerReferences") {
		t.Errorf("the check does not look at what owns a pod:\n%s", drain.Check)
	}
	for _, kind := range []string{"DaemonSet", "Node"} {
		if !strings.Contains(drain.Check, kind) {
			t.Errorf("the check counts %s-owned pods as work still to do:\n%s", kind, drain.Check)
		}
	}
	// Naming one control plane component means the others were forgotten.
	if strings.Contains(drain.Check, "kube-system/etcd-") {
		t.Errorf("the check excludes a control plane pod by name:\n%s", drain.Check)
	}
}
