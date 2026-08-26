package upgrade

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ryxen/malmok/api/v1alpha1"
	"github.com/ryxen/malmok/internal/exec"
	"github.com/ryxen/malmok/internal/rke2"
)

// ---------------------------------------------------------------------------
// Measurement
// ---------------------------------------------------------------------------

// Read asks the cluster what each node is running and each node what it has.
//
// Two sources, because they answer two questions, and getting them the wrong
// way round is a mistake this made once. `rke2 --version` is the binary on
// disk: what the node will run after a restart. The kubelet version the API
// server reports is what the node is running now. A half-finished upgrade --
// new binary installed, service not yet restarted -- is the case where they
// disagree, and reading the disk as though it were the running version made the
// tool refuse to finish the very upgrade it had started.
//
// So the skew rules are about the running version, and the installed version is
// carried alongside to explain a node that is between the two.
func Read(ctx context.Context, s v1alpha1.ClusterSpec,
	byHost map[string]exec.Runner, control exec.Runner) State {

	st := State{Now: time.Now()}
	cluster := clusterNodes(ctx, control)

	for _, group := range []struct {
		nodes []v1alpha1.NodeSpec
		agent bool
	}{
		{s.Topology.Servers, false},
		{s.Topology.Agents, true},
	} {
		for _, n := range group.nodes {
			node := NodeState{Host: n.Host, Agent: group.agent}

			addr := n.NodeIP
			if addr == "" {
				addr = n.Host
			}
			if seen, ok := cluster[addr]; ok {
				node.Ready = seen.ready
				node.Evidence = seen.kubelet
				if v, err := ParseVersion(seen.kubelet); err == nil {
					node.Version = v
				}
			}

			if r := byHost[n.Host]; r != nil {
				if res, err := r.Run(ctx, "rke2 --version 2>/dev/null | head -1"); err == nil && res.OK() {
					if fields := strings.Fields(strings.TrimSpace(res.Out())); len(fields) >= 3 {
						if v, err := ParseVersion(fields[2]); err == nil {
							node.Installed = v
						}
					}
				}
			}
			st.Nodes = append(st.Nodes, node)
		}
	}

	st.LastSnapshot = newestSnapshot(ctx, control)
	return st
}

// clusterView is what the control plane says about one node.
type clusterView struct {
	ready   bool
	kubelet string
}

// clusterNodes maps a node's address to what the cluster reports about it.
func clusterNodes(ctx context.Context, control exec.Runner) map[string]clusterView {
	out := map[string]clusterView{}
	if control == nil {
		return out
	}
	res, err := control.Run(ctx, rke2.Kubectl+
		`kubectl get nodes -o jsonpath='{range .items[*]}`+
		`{range .status.addresses[?(@.type=="InternalIP")]}{.address}{end}{" "}`+
		`{range .status.conditions[?(@.type=="Ready")]}{.status}{end}{" "}`+
		`{.status.nodeInfo.kubeletVersion}{"\n"}{end}' 2>/dev/null`)
	if err != nil || !res.OK() {
		return out
	}
	for _, line := range strings.Split(res.Out(), "\n") {
		if f := strings.Fields(line); len(f) == 3 {
			out[f[0]] = clusterView{ready: f[1] == "True", kubelet: f[2]}
		}
	}
	return out
}

// newestSnapshot is the time of the most recent etcd snapshot on a server.
//
// Read from disk rather than from the API, because a snapshot that exists only
// as a CR is not one anybody can restore from.
func newestSnapshot(ctx context.Context, control exec.Runner) time.Time {
	if control == nil {
		return time.Time{}
	}
	res, err := control.Run(ctx,
		`ls -t /var/lib/rancher/rke2/server/db/snapshots 2>/dev/null | head -1 | `+
			`xargs -I{} stat -c %Y /var/lib/rancher/rke2/server/db/snapshots/{} 2>/dev/null`)
	if err != nil || !res.OK() {
		return time.Time{}
	}
	var unix int64
	if _, err := fmt.Sscanf(strings.TrimSpace(res.Out()), "%d", &unix); err != nil || unix == 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0)
}
