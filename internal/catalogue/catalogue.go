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
	"strings"

	"github.com/ryxenix/malmok/api/v1alpha1"
	"github.com/ryxenix/malmok/internal/cert"
	"github.com/ryxenix/malmok/internal/dataplane"
	"github.com/ryxenix/malmok/internal/engine"
	"github.com/ryxenix/malmok/internal/exec"
	"github.com/ryxenix/malmok/internal/gateway"
	"github.com/ryxenix/malmok/internal/nodeprep"
	"github.com/ryxenix/malmok/internal/observability"
	"github.com/ryxenix/malmok/internal/pki"
	"github.com/ryxenix/malmok/internal/platform"
	"github.com/ryxenix/malmok/internal/rke2"
	"github.com/ryxenix/malmok/internal/storage"
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
	// Charts are the chart archives registry.chartDir held, keyed by chart
	// name. A chart absent from here comes from a repository instead.
	Charts map[string][]byte
}

// Options carry the timeouts and the airgap artifact locations.
type Options struct {
	RKE2      rke2.Options
	Dataplane dataplane.Options
	Gateway   gateway.Options
	PKI       pki.Options
	Platform  platform.Options
	Storage   storage.Options

	Observability observability.Options
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

	// The document's artifact path reaches every phase that installs from it,
	// filled here rather than at each caller: apply, upgrade and the headless
	// builder each construct their own options, and a value threaded through
	// three of them is a value missing from the fourth the day somebody adds
	// one.
	//
	// A caller that set one explicitly keeps it -- that is the test harness's
	// way of pointing a run at a staged directory.
	if p := strings.TrimSpace(spec.Kubernetes.ArtifactPath); p != "" {
		if o.RKE2.ArtifactPath == "" {
			o.RKE2.ArtifactPath = p
		}
		if o.Dataplane.ArtifactPath == "" {
			o.Dataplane.ArtifactPath = p
		}
	}

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

	// Joins are one node at a time. This tool never restarts every node at
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

	// Storage before anything that asks for a volume, which in practice means
	// before observability. It went missing entirely until 0.93.0: the
	// document named a driver, the report printed it, and no phase installed
	// anything -- so the metrics database sat Pending on a claim that could
	// never bind, two layers away from the field that caused it.
	//
	// After the dataplane, because the provisioner is an ordinary pod and
	// needs a working CNI to run at all.
	if steps := storage.Steps(control, spec, o.Storage); len(steps) > 0 {
		phases = append(phases, engine.Phase{
			ID:        storage.Phase,
			Grade:     engine.GradeAdditive,
			Traversal: engine.TraversalCluster,
			Steps:     func(string) []engine.Step { return steps },
		})
	}

	// PKI before the gateway: a listener whose certificate is issued in-cluster
	// needs an issuer that already exists, and one that is supplied does not
	// care about the order -- so the order that works for both is this one.
	// The archives reach the packages that render HelmCharts. Filled once, so
	// a phase added later cannot quietly go back to fetching.
	o.PKI.Charts, o.Observability.Charts, o.Platform.Charts = m.Charts, m.Charts, m.Charts

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

	// Observability before GitOps and after the gateway: the stack is
	// ordinary workload, and putting it ahead of the handover means the first
	// application ArgoCD deploys is already being scraped rather than
	// invisible until somebody notices.
	if steps := observability.Steps(control, spec, o.Observability); len(steps) > 0 {
		phases = append(phases, engine.Phase{
			ID:        observability.Phase,
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

// ChartRef is one Helm chart this document installs, at the version pinned in
// code rather than one resolved at install time.
type ChartRef struct {
	Name    string
	Version string
}

// File is the archive's name as `helm pull` writes it, which is the name an
// operator staging a chartDir will already have.
func (c ChartRef) File() string { return c.Name + "-" + c.Version + ".tgz" }

// Charts lists what this document installs, so that the two questions an
// air-gapped site asks -- which archives do I carry, and are they present --
// are answered from one place rather than from three packages that can drift.
//
// The dataplane is absent on purpose: RKE2 carries Cilium's chart in its own
// artifacts, which is why an air-gapped cluster has a network before any of
// this matters.
func Charts(spec v1alpha1.ClusterSpec) []ChartRef {
	var out []ChartRef
	if pki.WantsCertManager(spec) {
		out = append(out, ChartRef{"cert-manager", pki.CertManagerVersion})
	}
	if pki.WantsTrustBundle(spec) {
		out = append(out, ChartRef{"trust-manager", pki.TrustManagerVersion})
	}
	if observability.Enabled(spec) {
		out = append(out, ChartRef{"victoria-metrics-k8s-stack", observability.StackChartVersion})
	}
	if g := spec.Platform.GitOps; g.Enabled != nil && *g.Enabled {
		out = append(out, ChartRef{"argo-cd", platform.ChartVersion})
	}
	return out
}

// AllCharts is every chart this release can install, with the repository it
// comes from.
//
// Charts() answers "what does this document install"; this answers "what could
// any document install", which is the question the image list is generated
// from. They read the same constants, so a version bumped in one place cannot
// be missed by the other.
func AllCharts() []ChartSource {
	return []ChartSource{
		{ChartRef{"cert-manager", pki.CertManagerVersion}, pki.UpstreamRepo},
		{ChartRef{"trust-manager", pki.TrustManagerVersion}, pki.UpstreamRepo},
		{ChartRef{"victoria-metrics-k8s-stack", observability.StackChartVersion}, observability.ChartRepo},
		{ChartRef{"argo-cd", platform.ChartVersion}, platform.UpstreamRepo},
	}
}

// ChartSource is a chart and where it is published.
type ChartSource struct {
	ChartRef
	Repo string
}
