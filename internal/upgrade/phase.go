package upgrade

import (
	"fmt"
	"strings"
	"time"

	"platform.ryxen.dev/platformctl/api/v1alpha1"
	"platform.ryxen.dev/platformctl/internal/engine"
	"platform.ryxen.dev/platformctl/internal/exec"
	"platform.ryxen.dev/platformctl/internal/rke2"
)

// The phases, in the order they must run.
//
// Servers before agents, and one node at a time within each. The order is not
// a preference: an agent's kubelet must never lead its API server, so bringing
// a worker up first puts the cluster outside the supported skew until the
// servers catch up. And CLAUDE.md's prohibition on restarting every node at
// once is the same rule a control plane needs to keep quorum.
const (
	PhaseServers = "upgrade-server"
	PhaseAgents  = "upgrade-agent"
)

// Options are the timeouts and the airgap artifact location.
type Options struct {
	RKE2 rke2.Options
	// DrainTimeout bounds how long a node is given to hand its workloads over.
	DrainTimeout time.Duration
	// Force makes the drain proceed past a PodDisruptionBudget it cannot
	// satisfy. Off by default: a budget is somebody's statement about how much
	// of their service may be down, and the tool is not the one to overrule it.
	Force bool
	// SingleNode says there is nowhere to drain to, so the drain is skipped.
	SingleNode bool
}

func (o Options) drainTimeout() time.Duration {
	if o.DrainTimeout <= 0 {
		return 10 * time.Minute
	}
	return o.DrainTimeout
}

// Runners is a shell on each node plus one on a node that stays up.
type Runners struct {
	// ByHost is a runner per node, already elevated.
	ByHost map[string]exec.Runner
	// Control answers cluster-scoped questions. An agent has no kubeconfig and
	// cannot say anything about itself, and the node being restarted is the one
	// least able to report on its own progress.
	Control exec.Runner
}

// Phases returns the upgrade in the order it must run.
func Phases(spec v1alpha1.ClusterSpec, target string, r Runners, o Options) ([]engine.Phase, error) {
	want, err := ParseVersion(target)
	if err != nil {
		return nil, err
	}

	servers, agents := roles(spec)
	if len(servers) == 0 {
		return nil, fmt.Errorf("upgrade: the document names no server, so there is no control plane to move")
	}
	control := r.Control
	if control == nil {
		control = r.ByHost[servers[0].Host]
	}
	if control == nil {
		return nil, fmt.Errorf("upgrade: no runner for the first server %s", servers[0].Host)
	}

	var phases []engine.Phase
	for _, group := range []struct {
		id    string
		nodes []v1alpha1.NodeSpec
		kind  string
		unit  string
	}{
		{PhaseServers, servers, "server", "rke2-server"},
		{PhaseAgents, agents, "agent", "rke2-agent"},
	} {
		if len(group.nodes) == 0 {
			continue
		}
		byHost := map[string]v1alpha1.NodeSpec{}
		for _, n := range group.nodes {
			byHost[n.Host] = n
		}

		phases = append(phases, engine.Phase{
			ID: group.id,
			// Every node restarts its kubelet, so workloads move whatever the
			// drain does. Calling this additive would put a maintenance window
			// behind a grade that says there is no need for one.
			Grade:     engine.GradeDisruptive,
			Traversal: engine.TraversalSequential,
			Nodes:     hosts(group.nodes),
			Steps: func(node string) []engine.Step {
				target, ok := byHost[node]
				if !ok {
					return nil
				}
				runner := r.ByHost[node]
				if runner == nil {
					return nil
				}
				return NodeSteps(group.id, runner, control, target, want, group.kind, group.unit, o)
			},
		})
	}
	return phases, nil
}

// NodeSteps is what moving one node to a new version takes.
//
// The order is the whole of it: get the workloads off, put the new binary
// down, restart into it, wait for the control plane to agree that this node is
// running the new version, and only then let work back onto it.
func NodeSteps(phase string, runner, control exec.Runner, node v1alpha1.NodeSpec,
	want Version, kind, unit string, o Options) []engine.Step {

	addr := node.NodeIP
	if addr == "" {
		addr = node.Host
	}

	// The install and the restart happen on the node; everything that asks the
	// cluster a question happens somewhere that still has an API server to ask.
	onNode := func(s *engine.ShellStep) engine.Step {
		s.Phase, s.Runner, s.Host = phase, runner, node.Host
		return s
	}
	onControl := func(s *engine.ShellStep) engine.Step {
		s.Phase, s.Runner, s.Host = phase, control, node.Host
		return s
	}

	var steps []engine.Step
	if !o.SingleNode {
		steps = append(steps, onControl(drainStep(addr, o)))
	}
	steps = append(steps,
		onNode(rke2.InstallStep(want.String(), kind, o.RKE2)),
		onNode(restartStep(unit, o)),
		onControl(upgradedStep(addr, want, o)),
	)
	if !o.SingleNode {
		steps = append(steps, onControl(uncordonStep(addr)))
	}
	return steps
}

// ---------------------------------------------------------------------------
// Steps
// ---------------------------------------------------------------------------

// nodeName resolves the address the document names a node by into the name the
// cluster knows it as.
//
// Matched by address rather than by name for the same reason the join phase
// does it: a hostname depends on what the node happens to call itself and on
// whether node-name was set, while the address is what the document says.
func nodeName(addr string) string {
	return fmt.Sprintf(`node=$(kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name}{" "}{range .status.addresses[?(@.type=="InternalIP")]}{.address}{end}{"\n"}{end}' 2>/dev/null | awk -v a=%s '$2==a {print $1}' | head -1)
[ -n "$node" ] || { echo "no node in this cluster has the address %s"; exit 1; }`,
		rke2.ShellQuote(addr), addr)
}

// drainStep moves the workloads off before the kubelet restarts.
//
// The observable is not "drain was run" -- that is an action, and an action is
// not a state anything can be resumed from. It is that the node is
// unschedulable and no pod is left on it that would have to be evicted.
func drainStep(addr string, o Options) *engine.ShellStep {
	// Counted by what owns a pod, not by what it is called.
	//
	// A drain does not evict two kinds of pod, and both stay behind on a node
	// that drained perfectly: DaemonSet pods, which come back immediately by
	// design, and mirror pods -- owner kind Node -- which are the static
	// manifests that make up the control plane itself. An earlier version of
	// this excluded `kube-system/etcd-` by name and left the API server, the
	// scheduler, the controller manager and the cloud controller manager
	// counted, so a successful drain reported five pods still to go and the
	// step failed on a node it had just emptied.
	//
	// A pod with no owner at all is counted. Nothing manages it, a drain
	// refuses to evict it without --force, and it is exactly the pod an
	// operator needs to be told about.
	remaining := `kubectl get pods --all-namespaces --field-selector spec.nodeName=$node ` +
		`-o jsonpath='{range .items[*]}{.metadata.ownerReferences[*].kind}{"\n"}{end}' 2>/dev/null | ` +
		`grep -cvE '^(DaemonSet|Node)$'`

	force := ""
	if o.Force {
		// Named in the command so the audit report shows that somebody's
		// disruption budget was overruled and by whose instruction.
		force = " --disable-eviction"
	}

	return &engine.ShellStep{
		Name: "drain",
		Check: rke2.Kubectl + nodeName(addr) + fmt.Sprintf(`
sched=$(kubectl get node "$node" -o jsonpath='{.spec.unschedulable}' 2>/dev/null)
[ "$sched" = "true" ] || { echo "$node still accepts work"; exit 1; }
left=$(%s)
[ "$left" = "0" ] || { echo "$node still runs $left pod(s) that would be evicted"; exit 1; }
echo "$node is cordoned and its workloads have moved"`, remaining),

		Do: rke2.Kubectl + nodeName(addr) + fmt.Sprintf(`
kubectl drain "$node" --ignore-daemonsets --delete-emptydir-data --timeout=%ds%s`,
			int(o.drainTimeout().Seconds()), force),

		Satisfied: "%s",
		Missing:   "%s",
		DoTimeout: o.drainTimeout() + time.Minute,
		// The drain has its own deadline, and a second attempt at a
		// PodDisruptionBudget that will not be satisfied waits again for the
		// same reason.
		Attempts: 1,
	}
}

// restartStep brings the node up on the binary that was just installed.
//
// Separate from the install because they are separate facts: the installer
// replaces the files on disk and the process keeps running the old ones until
// something restarts it. A step that installed and called the node upgraded
// would report a version the node is not running.
func restartStep(unit string, o Options) *engine.ShellStep {
	return &engine.ShellStep{
		Name: "restart",
		// The unit is compared against the binary on disk. A running service
		// started before the install is a service running the old version, and
		// `is-active` cannot tell the two apart.
		Check: fmt.Sprintf(`systemctl is-active --quiet %s || { echo "%s is not running"; exit 1; }
started=$(systemctl show -p ExecMainStartTimestamp --value %s 2>/dev/null)
[ -n "$started" ] || { echo "%s reports no start time"; exit 1; }
started=$(date -d "$started" +%%s 2>/dev/null || echo 0)
# Change time, not modification time. The installer unpacks a tarball and tar
# restores the archive's timestamps, so the new binary's mtime is the day
# upstream built it -- older than a unit that started last week. A live upgrade
# put v1.35.7 on disk with an mtime from eight days earlier, the check called
# the unit current, nothing restarted, and the node kept serving the old
# version with the new binary sitting beside it. Change time is set by the
# filesystem when the inode is written and no archive can carry it.
binary=$(stat -c %%Z "$(command -v rke2)" 2>/dev/null || echo 0)
[ "$binary" -le "$started" ] || {
  echo "%s has been running since before the current binary was installed"; exit 1; }
echo "%s is running the installed binary"`, unit, unit, unit, unit, unit, unit),

		Do: fmt.Sprintf(`set -e
systemctl daemon-reload
systemctl restart %s
for i in $(seq 1 60); do
  systemctl is-active --quiet %s && exit 0
  sleep 2
done
echo "%s did not come back:"; journalctl -u %s -n 30 --no-pager 2>&1 | tail -30
exit 1`, unit, unit, unit, unit),

		Satisfied: "%s",
		Missing:   "%s",
		DoTimeout: o.RKE2.ReadyTimeout + 5*time.Minute,
		Attempts:  1,
	}
}

// upgradedStep waits for the control plane to agree.
//
// This is the step that makes the phase honest. A binary on disk at the new
// version and a unit that restarted are both true of a node whose kubelet is
// crash-looping; what has to be true is that the cluster reports this node as
// Ready and running the version that was asked for. Asked from a node that
// stayed up, because the one that just restarted is the least able to answer.
func upgradedStep(addr string, want Version, o Options) *engine.ShellStep {
	read := nodeName(addr) + `
line=$(kubectl get node "$node" -o jsonpath='{.status.nodeInfo.kubeletVersion}{"|"}{range .status.conditions[?(@.type=="Ready")]}{.status}{end}' 2>/dev/null)`

	timeout := o.RKE2.ReadyTimeout
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}

	return &engine.ShellStep{
		Name: "upgraded",
		Check: rke2.Kubectl + read + fmt.Sprintf(`
[ "$line" = "%s|True" ] || { echo "$node reports ${line:-nothing} and the target is %s"; exit 1; }
echo "$node runs %s and is Ready"`, want.String(), want.String(), want.String()),

		Do: rke2.Kubectl + fmt.Sprintf(`deadline=$(( $(date +%%s) + %d ))
while [ "$(date +%%s)" -lt "$deadline" ]; do
  %s
  [ "$line" = "%s|True" ] && exit 0
  sleep 5
done
echo "the node at %s did not come back on %s within %ds. The cluster reports:"
kubectl get nodes -o wide 2>&1 | tail -10
exit 1`, int(timeout.Seconds()),
			// The name is resolved inside the loop: a node that has not
			// re-registered yet has no entry to read, and giving up on the
			// first pass would fail before the restart had finished.
			strings.ReplaceAll(read, "exit 1", "sleep 5; continue"),
			want.String(), addr, want.String(), int(timeout.Seconds())),

		Satisfied: "%s",
		Missing:   "%s",
		DoTimeout: timeout + time.Minute,
		Attempts:  1,
	}
}

// uncordonStep lets work back onto the node.
func uncordonStep(addr string) *engine.ShellStep {
	return &engine.ShellStep{
		Name: "uncordon",
		Check: rke2.Kubectl + nodeName(addr) + `
sched=$(kubectl get node "$node" -o jsonpath='{.spec.unschedulable}' 2>/dev/null)
[ "$sched" = "true" ] && { echo "$node still refuses work"; exit 1; }
echo "$node accepts work again"`,
		Do:        rke2.Kubectl + nodeName(addr) + "\nkubectl uncordon \"$node\"",
		Satisfied: "%s",
		Missing:   "%s",
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// roles splits the document's nodes, filling in the role each list implies.
func roles(spec v1alpha1.ClusterSpec) (servers, agents []v1alpha1.NodeSpec) {
	for _, n := range spec.Topology.Servers {
		if n.Role == "" {
			n.Role = v1alpha1.RoleServer
		}
		servers = append(servers, n)
	}
	for _, n := range spec.Topology.Agents {
		if n.Role == "" {
			n.Role = v1alpha1.RoleAgent
		}
		agents = append(agents, n)
	}
	return
}

func hosts(nodes []v1alpha1.NodeSpec) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Host)
	}
	return out
}
