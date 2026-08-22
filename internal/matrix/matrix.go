// Package matrix defines what "verified" means for this tool.
//
// The tool has more shapes than anyone can hold in their head: a build starts
// from bare metal or from a cluster that already exists, on one node or
// several, with one of three dataplanes, one of four certificate modes, a
// gateway exposed two ways, and images from four kinds of registry. The cross
// product is in the hundreds. Nobody runs hundreds of builds, so the honest
// question is not "did we run them all" but "which few, run together, touch
// every value and every combination that has ever broken".
//
// This package answers that question as data rather than as a habit. The
// cases below are chosen so that every value of every dimension appears at
// least once and every pair listed in RequiredPairs appears together; a test
// in this package proves it, offline, on every commit. The lab harness under
// test/lab executes them against real nodes.
//
// It exists because six scenarios, written by hand in one afternoon, found
// thirteen defects -- including one that failed every from-scratch build. The
// defects were not exotic; they were the paths nobody had walked. A matrix is
// how a path stops being one nobody walked.
package matrix

import (
	"fmt"
	"sort"
	"strings"

	"platform.ryxen.dev/malmok/api/v1alpha1"
)

// Dimensions of the build. The names are the document's own words, so a case
// reads like the cluster.yaml it produces.
const (
	DimNodes     = "nodes"
	DimDataplane = "dataplane"
	DimPKI       = "pki"
	DimExposure  = "exposure"
	DimRegistry  = "registry"
	DimGitOps    = "gitops"
	DimOp        = "operation"
)

// Operations are what is done to the cluster, which is a dimension of its own:
// most defects this matrix exists for were found not in a first build but in
// the second thing done to it.
const (
	// OpBuild is a build from bare nodes.
	OpBuild = "build"
	// OpGrow adds a node to a cluster that already exists.
	OpGrow = "grow"
	// OpResume kills the run mid-phase and re-runs the same command.
	OpResume = "resume"
	// OpReapply runs the same document twice and expects the second run to
	// change nothing.
	OpReapply = "reapply"
)

// Case is one row of the matrix: a document to build and a thing to do to it.
type Case struct {
	Name string

	Nodes     int    // 1 or 2
	Dataplane string // cilium-gw | cilium-traefik | canal-traefik
	PKI       string // none | private-ca | byo-cert
	Exposure  string // node-ips | lb-pool | none (no gateway)
	Registry  string // embedded | upstream
	GitOps    bool
	Op        string

	// VIP asks for kube-vip. Without it the server registers under its own
	// address (acceptNodeRegistration), which is the IDC case: policy forbids
	// a second address on the segment.
	VIP bool

	// Why says what this row is here to catch. A case nobody can justify is a
	// case somebody will delete the next time the suite is slow.
	Why string
}

// Cases is the matrix. Ten rows, chosen for coverage rather than for
// symmetry: see RequiredPairs for the combinations that are not optional.
func Cases() []Case {
	return []Case{
		{
			Name: "idc-single", Nodes: 1, Dataplane: "cilium-gw", PKI: "none",
			Exposure: "node-ips", Registry: "embedded", Op: OpBuild,
			Why: "the IDC shape: one node, no VIP the network would refuse, no domain decided yet",
		},
		{
			Name: "homelab-full", Nodes: 2, Dataplane: "cilium-gw", PKI: "private-ca",
			Exposure: "node-ips", Registry: "embedded", GitOps: true, VIP: true, Op: OpBuild,
			Why: "the whole stack at once: VIP, issued certificates, gitops",
		},
		{
			Name: "byocert-lb", Nodes: 2, Dataplane: "cilium-gw", PKI: "byo-cert",
			Exposure: "lb-pool", Registry: "upstream", VIP: true, Op: OpBuild,
			Why: "material the customer hands over, served from a pool address over HTTPS",
		},
		{
			Name: "canal-pair", Nodes: 2, Dataplane: "canal-traefik", PKI: "none",
			Exposure: "none", Registry: "embedded", Op: OpBuild,
			Why: "the fallback dataplane, which no Cilium step touches and nothing else exercises",
		},
		{
			Name: "cilium-traefik-single", Nodes: 1, Dataplane: "cilium-traefik", PKI: "none",
			Exposure: "none", Registry: "embedded", Op: OpBuild,
			Why: "Cilium without the Gateway API: kube-proxy replacement on, no GatewayClass to wait for",
		},
		{
			Name: "grow-to-two", Nodes: 1, Dataplane: "cilium-gw", PKI: "none",
			Exposure: "node-ips", Registry: "embedded", Op: OpGrow,
			Why: "every site starts small; the second node arrives later and must not disturb the first",
		},
		{
			Name: "resume-after-kill", Nodes: 2, Dataplane: "cilium-gw", PKI: "private-ca",
			Exposure: "node-ips", Registry: "embedded", VIP: true, Op: OpResume,
			Why: "power cut, dropped SSH, closed laptop: the run dies mid-phase and is re-run",
		},
		{
			Name: "reapply-changes-nothing", Nodes: 2, Dataplane: "cilium-gw", PKI: "none",
			Exposure: "lb-pool", Registry: "upstream", VIP: true, Op: OpReapply,
			Why: "a second run of the same document must observe and skip, never reinstall",
		},
	}
}

// RequiredPairs are the combinations that have broken before or that no other
// row would reach. A pair here is a promise the matrix keeps.
func RequiredPairs() [][2]string {
	return [][2]string{
		// One node with the Gateway API: the cilium-operator asks for two
		// replicas that will not share a node, which deadlocked a restart.
		{DimNodes + "=1", DimDataplane + "=cilium-gw"},
		// A gateway on node addresses, with no pool to take one from.
		{DimNodes + "=1", DimExposure + "=node-ips"},
		// Supplied material terminating TLS: the listener has to name a
		// Secret, and something has to put the certificate in it.
		{DimPKI + "=byo-cert", DimExposure + "=lb-pool"},
		// The bundled dataplane on more than one node, where nothing this
		// tool writes is involved at all.
		{DimNodes + "=2", DimDataplane + "=canal-traefik"},
		// Resume on a cluster that issues certificates: the phases after the
		// interruption are the ones with state in the cluster.
		{DimOp + "=" + OpResume, DimPKI + "=private-ca"},
		// Growth on the dataplane that has per-node agents to extend.
		{DimOp + "=" + OpGrow, DimDataplane + "=cilium-gw"},
	}
}

// Values lists every value the matrix must cover, by dimension.
func Values() map[string][]string {
	return map[string][]string{
		DimNodes:     {"1", "2"},
		DimDataplane: {"cilium-gw", "cilium-traefik", "canal-traefik"},
		DimPKI:       {"none", "private-ca", "byo-cert"},
		DimExposure:  {"node-ips", "lb-pool", "none"},
		DimRegistry:  {"embedded", "upstream"},
		DimGitOps:    {"true", "false"},
		DimOp:        {OpBuild, OpGrow, OpResume, OpReapply},
	}
}

// values renders one case as dimension=value strings.
func (c Case) values() []string {
	return []string{
		fmt.Sprintf("%s=%d", DimNodes, c.Nodes),
		DimDataplane + "=" + c.Dataplane,
		DimPKI + "=" + c.PKI,
		DimExposure + "=" + c.Exposure,
		DimRegistry + "=" + c.Registry,
		fmt.Sprintf("%s=%t", DimGitOps, c.GitOps),
		DimOp + "=" + c.Op,
	}
}

// Uncovered reports the dimension values no case exercises. Empty is the only
// acceptable answer, and a test says so.
func Uncovered(cases []Case) []string {
	seen := map[string]bool{}
	for _, c := range cases {
		for _, v := range c.values() {
			seen[v] = true
		}
	}
	var out []string
	for dim, values := range Values() {
		for _, v := range values {
			if !seen[dim+"="+v] {
				out = append(out, dim+"="+v)
			}
		}
	}
	sort.Strings(out)
	return out
}

// UncoveredPairs reports the required combinations no single case contains.
func UncoveredPairs(cases []Case) []string {
	var out []string
	for _, want := range RequiredPairs() {
		found := false
		for _, c := range cases {
			has := map[string]bool{}
			for _, v := range c.values() {
				has[v] = true
			}
			if has[want[0]] && has[want[1]] {
				found = true
				break
			}
		}
		if !found {
			out = append(out, want[0]+" with "+want[1])
		}
	}
	sort.Strings(out)
	return out
}

// Hosts are the machines a case is built on.
type Hosts struct {
	Server string
	Agent  string
	User   string
	// PasswordRef is a SourceRef, never a password: cluster.yaml carries no
	// plaintext secret, and the matrix produces real documents.
	PasswordRef v1alpha1.SourceRef
}

// Material names the files a case's certificate mode reads.
type Material struct {
	RootCert         string
	IntermediateCert string
	IntermediateKey  string
	LeafCert         string
	LeafKey          string
}

// Document renders the cluster.yaml this case builds from.
//
// Generated rather than kept as fixture files: a dimension gains a value by
// editing one list, not by writing another YAML nobody will keep in step.
func (c Case) Document(version string, h Hosts, m Material, grown bool) v1alpha1.ClusterSpec {
	node := func(host string) v1alpha1.NodeSpec {
		return v1alpha1.NodeSpec{
			Host: host,
			SSH:  v1alpha1.SSHSpec{User: h.User, Password: h.PasswordRef},
		}
	}

	spec := v1alpha1.ClusterSpec{
		APIVersion: v1alpha1.APIVersion,
		Kind:       v1alpha1.KindSpec,
		Metadata: v1alpha1.Metadata{
			Name:    c.Name,
			Profile: v1alpha1.ProfileHomelab,
			Annotations: map[string]string{
				"platform.ryxen.dev/matrix-case": c.Name,
			},
		},
		Network: v1alpha1.NetworkSpec{Mode: v1alpha1.NetworkOnline},
		Topology: v1alpha1.TopologySpec{
			Servers: []v1alpha1.NodeSpec{node(h.Server)},
		},
		Kubernetes: v1alpha1.KubernetesSpec{
			Version: version,
			Dataplane: v1alpha1.DataplaneSpec{
				Preset: v1alpha1.DataplanePreset(c.Dataplane),
			},
		},
		Registry: v1alpha1.RegistrySpec{Mode: v1alpha1.RegistryMode(c.Registry)},
		PKI:      v1alpha1.PKISpec{Mode: v1alpha1.PKIMode(c.PKI)},
	}

	// A grown case starts with one node and gains the agent on the second
	// apply; every other two-node case has both from the start.
	if c.Nodes > 1 || (c.Op == OpGrow && grown) {
		spec.Topology.Agents = []v1alpha1.NodeSpec{node(h.Agent)}
	}

	if c.VIP {
		spec.Topology.RegistrationAddress = vipAddress
		spec.Topology.VIP = &v1alpha1.VIPSpec{
			Provider: "kube-vip", Address: vipAddress, Mode: "arp",
		}
	} else {
		// No VIP: the server registers under its own address, and the
		// document has to say it accepts that trade (ADR-008).
		spec.Topology.RegistrationAddress = h.Server
		spec.Topology.AcceptNodeRegistration = true
	}

	if c.Exposure == "lb-pool" {
		spec.Kubernetes.Dataplane.LoadBalancerPool = []string{lbPool}
	}

	switch c.PKI {
	case "private-ca":
		spec.PKI.Domain = domain
		spec.PKI.PrivateCA = &v1alpha1.PrivateCASpec{
			RootCert:         v1alpha1.SourceRef("file://" + m.RootCert),
			IntermediateCert: v1alpha1.SourceRef("file://" + m.IntermediateCert),
			IntermediateKey:  v1alpha1.SourceRef("file://" + m.IntermediateKey),
		}
		yes := true
		spec.PKI.Trust.ClusterBundle = &yes
	case "byo-cert":
		spec.PKI.Domain = domain
		spec.PKI.BYOCert = &v1alpha1.BYOCertSpec{
			Cert:   v1alpha1.SourceRef("file://" + m.LeafCert),
			Key:    v1alpha1.SourceRef("file://" + m.LeafKey),
			CACert: v1alpha1.SourceRef("file://" + m.RootCert),
		}
	}

	if c.Exposure != "none" {
		gw := v1alpha1.Gateway{
			Name:            "edge",
			RouteNamespaces: "all",
			Listeners: []v1alpha1.ListenerSpec{
				{Name: "http", Protocol: v1alpha1.ListenerHTTP, Port: 80},
			},
		}
		if c.Exposure == "node-ips" {
			gw.Exposure = v1alpha1.ExposureNodeIPs
		} else {
			gw.Address = lbAddress
		}
		// A certificate mode that supplies material gets a listener to serve
		// it on; without one the material is installed and never used.
		if c.PKI == "byo-cert" {
			gw.Listeners = append(gw.Listeners, v1alpha1.ListenerSpec{
				Name: "https", Protocol: v1alpha1.ListenerHTTPS, Port: 443,
				Hostname: "*." + domain,
			})
		}
		spec.Gateway = v1alpha1.GatewaySpec{DomainSuffix: domain, Gateways: []v1alpha1.Gateway{gw}}
	}

	if c.GitOps {
		on := true
		spec.Platform.GitOps = v1alpha1.GitOpsSpec{
			Enabled:       &on,
			Source:        "git",
			GitRepo:       "https://github.com/argoproj/argocd-example-apps.git",
			Branch:        "master",
			BootstrapApps: []string{"guestbook"},
		}
	}
	return spec
}

// Lab addresses. They belong to the throwaway segment the harness is pointed
// at, and they are constants here so a case reads without a lookup.
const (
	vipAddress = "192.168.88.210"
	lbPool     = "192.168.88.216/29"
	lbAddress  = "192.168.88.216"
	domain     = "lab.example.com"
)

// String renders a case the way a report should print it.
func (c Case) String() string {
	return fmt.Sprintf("%s (%s)", c.Name, strings.Join(c.values(), " "))
}
