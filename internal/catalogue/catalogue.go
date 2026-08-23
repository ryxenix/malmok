// Package catalogue assembles the phases of docs/11-execute.md §2 into
// something the engine can run.
//
// The phase packages each know how to do one thing and nothing about order.
// This is where order lives, and order is most of the correctness: the trust
// store has to exist before L1 pulls an image, the first server has to be Ready
// before anything joins it, and the dataplane has to be reconfigured before a
// Gateway is created against it.
//
// It builds; it does not run. The engine owns execution, retry, resume and the
// event stream, and keeping the two apart is what lets the catalogue be tested
// without a node.
package catalogue

import (
	"context"
	"fmt"
	"sort"

	"platform.ryxen.dev/malmok/api/v1alpha1"
	"platform.ryxen.dev/malmok/internal/cert"
	"platform.ryxen.dev/malmok/internal/dataplane"
	"platform.ryxen.dev/malmok/internal/engine"
	"platform.ryxen.dev/malmok/internal/exec"
	"platform.ryxen.dev/malmok/internal/gateway"
	"platform.ryxen.dev/malmok/internal/nodeprep"
	"platform.ryxen.dev/malmok/internal/pki"
	"platform.ryxen.dev/malmok/internal/platform"
	"platform.ryxen.dev/malmok/internal/rke2"
)

// Runners gives the catalogue a shell on each node.
//
// Connections are opened once by the caller and shared: a build asks each node
// several hundred questions across the phases, and reconnecting per phase would
// spend most of the run in key exchange.
type Runners struct {
	// ByHost is a runner per node, already elevated.
	ByHost map[string]exec.Runner
	// Control is a node already in the cluster, used for cluster-scoped work
	// and for the checks a joining node cannot answer about itself.
	Control exec.Runner
}

// Material is everything the caller resolved from the document's SourceRefs.
type Material struct {
	// Trust is the CA and registry configuration l0-node-prep installs.
	Trust nodeprep.TrustMaterial
	// Bundles are the assembled TLS bundles, keyed by the secretRef listeners
	// reference.
	Bundles map[string]*cert.Bundle
	// Token is the cluster token joining nodes use. Empty means read it from
	// the first server at build time, which is the normal path.
	Token string
	// PKI is what l2-pki installs: the issuing CA and any ACME credential.
	PKI pki.Material
}

// Options carry the timeouts and the airgap artifact locations.
type Options struct {
	RKE2      rke2.Options
	Dataplane dataplane.Options
	Gateway   gateway.Options
	PKI       pki.Options
	Platform  platform.Options
}

// Build returns the phases for a document, in the order they must run.
//
// A phase with nothing to do is left out rather than included empty: a run that
// reports twelve phases and did work in five is harder to read than one that
// reports five.
func Build(spec v1alpha1.ClusterSpec, r Runners, m Material, o Options) ([]engine.Phase, error) {
	servers, agents := roles(spec)
	if len(servers) == 0 {
		return nil, fmt.Errorf("catalogue: the document names no server, so there is no cluster to build")
	}
	first := servers[0]

	control := r.Control
	if control == nil {
		control = r.ByHost[first.Host]
	}
	if control == nil {
		return nil, fmt.Errorf("catalogue: no runner for the first server %s", first.Host)
	}

	var phases []engine.Phase

	// L0 runs on every node at once. The work is independent per node and it is
	// the longest phase that can be parallel, so serialising it would be the
	// single biggest waste in a build.
	phases = append(phases, engine.Phase{
		ID:        nodeprep.Phase,
		Grade:     engine.GradeMutating,
		Traversal: engine.TraversalParallel,
		Nodes:     hosts(append(append([]v1alpha1.NodeSpec{}, servers...), agents...)),
		Steps: func(node string) []engine.Step {
			runner := r.ByHost[node]
			if runner == nil {
				return nil
			}
			return nodeprep.Steps(runner, node, spec, m.Trust)
		},
	})

	// The first server is a phase of its own because everything after it needs
	// a cluster to talk to.
	phases = append(phases, engine.Phase{
		ID:        rke2.PhaseBootstrap,
		Grade:     engine.GradeMutating,
		Traversal: engine.TraversalCluster,
		Steps: func(string) []engine.Step {
			ro := o.RKE2
			// With kube-proxy disabled, Cilium needs the API server's direct
			// address before it first starts, or bootstrap deadlocks on the
			// in-cluster service IP nothing routes yet. The same file the
			// dataplane phase maintains, written one phase earlier.
			if dataplane.WantsCilium(spec) {
				ro.Prestage = append(ro.Prestage, rke2.PrestagedManifest{
					Name: "cilium-values",
					Path: dataplane.CiliumConfigFile,
					Body: dataplane.CiliumHelmConfig(spec),
				})
			}
			steps := rke2.BootstrapSteps(r.ByHost[first.Host], first, spec, ro)
			// kube-vip belongs to bootstrap, not to a phase of its own: the
			// address it serves is what every later join uses, so the phase
			// that brings up the first server has to finish with it answering.
			return append(steps, vipSteps(r.ByHost[first.Host], spec, o)...)
		},
	})

	// Joins are one node at a time. CLAUDE.md forbids restarting every node at
	// once, and a control plane that loses quorum while two members are joining
	// is a restore rather than a retry.
	if rest := servers[1:]; len(rest) > 0 {
		phases = append(phases, joinPhase(rke2.PhaseJoinServer, rest, spec, r, control, m, o))
	}
	if len(agents) > 0 {
		phases = append(phases, joinPhase(rke2.PhaseJoinAgent, agents, spec, r, control, m, o))
	}

	// L2 is cluster state: written once, from a node already in the cluster.
	if steps := dataplane.Steps(control, spec, o.Dataplane); len(steps) > 0 {
		phases = append(phases, engine.Phase{
			ID: dataplane.Phase,
			// ADR-004 binds the CNI, the Gateway controller and the load
			// balancer together, and applying that to a running cluster
			// replaces kube-proxy and rolls every dataplane pod.
			Grade:     engine.GradeDisruptive,
			Traversal: engine.TraversalCluster,
			Steps:     func(string) []engine.Step { return steps },
		})
	}

	// PKI before the gateway: a listener whose certificate is issued in-cluster
	// needs an issuer that already exists, and one that is supplied does not
	// care about the order -- so the order that works for both is this one.
	if steps := pki.Steps(control, spec, m.PKI, o.PKI); len(steps) > 0 {
		phases = append(phases, engine.Phase{
			ID:        pki.Phase,
			Grade:     engine.GradeAdditive,
			Traversal: engine.TraversalCluster,
			Steps:     func(string) []engine.Step { return steps },
		})
	}

	issuer := ""
	if pki.Issues(spec.PKI.Mode) {
		issuer = pki.IssuerName
	}
	if steps := gateway.Steps(control, spec, gateway.Options{
		Bundles: m.Bundles, Issuer: issuer, Timeout: o.Gateway.Timeout,
	}); len(steps) > 0 {
		phases = append(phases, engine.Phase{
			ID:        gateway.Phase,
			Grade:     engine.GradeAdditive,
			Traversal: engine.TraversalCluster,
			Steps:     func(string) []engine.Step { return steps },
		})
	}

	// GitOps is last, and it is last on purpose. Its final step waits for an
	// Application to actually deploy, which needs the dataplane routing, the
	// trust store the registry pull depends on and -- for anything with a
	// certificate -- the issuer. Running it earlier would make every one of
	// those failures arrive wearing ArgoCD's name.
	//
	// It is also the handover point. Everything after this belongs to what the
	// operator's repository says, not to this tool.
	if steps := platform.Steps(control, spec, platform.Material{
		RegistryHost: m.Trust.RegistryHost,
		RegistryUser: m.Trust.RegistryUser,
		RegistryPass: m.Trust.RegistryPass,
	}, o.Platform); len(steps) > 0 {
		phases = append(phases, engine.Phase{
			ID:        platform.Phase,
			Grade:     engine.GradeAdditive,
			Traversal: engine.TraversalCluster,
			Steps:     func(string) []engine.Step { return steps },
		})
	}

	return phases, nil
}

// joinPhase builds one of the two join phases.
//
// The token is read when the steps are built rather than when the catalogue is:
// the first server writes it during bootstrap, which has not happened yet at
// the time Build is called.
func joinPhase(id string, nodes []v1alpha1.NodeSpec, spec v1alpha1.ClusterSpec,
	r Runners, control exec.Runner, m Material, o Options) engine.Phase {

	byHost := map[string]v1alpha1.NodeSpec{}
	for _, n := range nodes {
		byHost[n.Host] = n
	}

	return engine.Phase{
		ID:        id,
		Grade:     engine.GradeMutating,
		Traversal: engine.TraversalSequential,
		Nodes:     hosts(nodes),
		Steps: func(node string) []engine.Step {
			target, ok := byHost[node]
			if !ok {
				return nil
			}
			runner := r.ByHost[node]
			if runner == nil {
				return nil
			}

			token := m.Token
			if token == "" {
				// Read from the server that generated it. A failure here is
				// reported by the first step rather than swallowed: a phase
				// that silently produced no steps would report success.
				t, err := rke2.ReadToken(context.Background(), control)
				if err != nil {
					return []engine.Step{tokenFailure(id, node, err)}
				}
				token = t
			}
			return rke2.JoinSteps(runner, control, spec, target, token, o.RKE2)
		},
	}
}

// vipSteps returns kube-vip's steps, resolving the interface from the node.
//
// The interface is read rather than assumed: a node with two NICs has exactly
// one that can answer for the address, and picking the other produces a VIP
// reachable from nowhere anybody cares about.
func vipSteps(runner exec.Runner, spec v1alpha1.ClusterSpec, o Options) []engine.Step {
	v := spec.Topology.VIP
	if v == nil || v.Address == "" || runner == nil {
		return nil
	}

	iface := v.Interface
	if iface == "" || iface == "auto" {
		resolved, err := rke2.ResolveVIPInterface(context.Background(), runner, v.Address)
		if err != nil {
			return []engine.Step{vipFailure(runner.Host(), err)}
		}
		iface = resolved
	}
	return rke2.VIPSteps(runner, spec, iface, o.RKE2)
}

// ---------------------------------------------------------------------------
// Failure steps
// ---------------------------------------------------------------------------

// tokenFailure and vipFailure turn a decision that could not be made into a
// step that fails, so the phase fails rather than quietly having no work.
func tokenFailure(phase, node string, err error) engine.Step {
	return engine.FailedStep{StepID: phase + "/token@" + node, Why: err.Error()}
}

func vipFailure(node string, err error) engine.Step {
	return engine.FailedStep{StepID: rke2.PhaseBootstrap + "/vip-interface@" + node, Why: err.Error()}
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

// SecretRefs lists the TLS bundles the caller has to assemble before Build.
func SecretRefs(spec v1alpha1.ClusterSpec) []string {
	refs := gateway.SecretRefs(spec)
	sort.Strings(refs)
	return refs
}
