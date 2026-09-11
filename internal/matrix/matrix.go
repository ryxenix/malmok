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

	"github.com/ryxenix/malmok/api/v1alpha1"
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
	DimNetwork   = "network"
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
	// OpUpgrade builds at one version and moves the cluster to a newer one,
	// node by node. It is the operation with the most to lose: every node
	// restarts, and a cluster that was working is the thing at risk.
	OpUpgrade = "upgrade"
	// OpFailover builds three servers and reboots the one answering for the
	// VIP. The address has to move, the cluster has to keep accepting writes
	// on two of its three etcd members, and the lost server has to rejoin by
	// itself. It is what "high availability" means, measured
	// rather than claimed.
	OpFailover = "failover"
)

// DefaultArtifactPath is where the lab stages RKE2's release artifacts. It is
// a path on the nodes, not on the machine running the tool, and the harness
// refuses an air-gapped case whose nodes do not hold the files.
const DefaultArtifactPath = "/home/k8s/rke2-artifacts"

// DefaultChartDir is where the harness puts the chart archives, relative to
// the generated document. A site with no registry to mirror into carries them
// exactly this way.
const DefaultChartDir = "./charts"

// Case is one row of the matrix: a document to build and a thing to do to it.
type Case struct {
	Name string

	Nodes     int    // 1, 2 or 3 -- three are three servers
	Dataplane string // cilium-gw | cilium-traefik | canal-traefik
	PKI       string // none | private-ca | byo-cert
	Exposure  string // node-ips | lb-pool | none (no gateway)
	Registry  string // embedded | upstream
	GitOps    bool
	Op        string

	// Network is online unless this says otherwise. An air-gapped case is not
	// a document with a different word in it: the harness cuts the nodes off
	// from everything but the segment before the run, so a step that reaches
	// for the internet fails here rather than at a customer's site.
	Network string // "" (online) | airgap

	// ArtifactPath is where the release artifacts were staged on the nodes.
	// Only an air-gapped case needs it, and only because the images and the
	// binaries have to already be there.
	ArtifactPath string

	// ChartDir holds the chart archives, beside the document rather than on
	// the nodes: they are read by the tool and embedded in each HelmChart. It
	// is relative on purpose, so the harness can put them next to the
	// cluster.yaml it generates -- which is what a staging machine does.
	ChartDir string

	// VIP asks for kube-vip. Without it the server registers under its own
	// address (acceptNodeRegistration), which is the IDC case: policy forbids
	// a second address on the segment.
	VIP bool

	// Why says what this row is here to catch. A case nobody can justify is a
	// case somebody will delete the next time the suite is slow.
	Why string
}

// Cases is the matrix. Eleven rows, chosen for coverage rather than for
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
		{
			Name: "upgrade-two", Nodes: 2, Dataplane: "cilium-gw", PKI: "none",
			Exposure: "node-ips", Registry: "embedded", VIP: true, Op: OpUpgrade,
			Why: "the operation with the most to lose: a working cluster, every node restarting, " +
				"a VIP that has to keep answering and an agent that must follow its server",
		},
		{
			Name: "airgap-pair", Nodes: 2, Dataplane: "cilium-gw", PKI: "none",
			Exposure: "node-ips", Registry: "embedded", Op: OpBuild,
			Network: "airgap", ArtifactPath: DefaultArtifactPath, ChartDir: DefaultChartDir,
			Why: "the customer case this tool exists for. Verified by hand once, which found " +
				"nine defects in a day -- a private CA that never reached a node's trust store, " +
				"a registry mirror the default named and never enabled, an artifact check that " +
				"passed a set the installer ignores. None of them would be caught again by " +
				"anything that runs on its own",
		},
		{
			Name: "ha-failover", Nodes: 3, Dataplane: "cilium-gw", PKI: "none",
			Exposure: "node-ips", Registry: "embedded", VIP: true, Op: OpFailover,
			Why: "three servers under a VIP, and the one answering for it lost. Every " +
				"customer who asks for production asks for this, and until this row it had " +
				"been built on paper only: the join of a second server had never run on a " +
				"machine, and nothing had ever checked that the address outlives its holder",
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
		// Issued material terminating TLS: the cluster has to sign for the
		// listener, not merely stand up an issuer that signs nothing.
		{DimPKI + "=private-ca", DimExposure + "=node-ips"},
		// The bundled dataplane on more than one node, where nothing this
		// tool writes is involved at all.
		{DimNodes + "=2", DimDataplane + "=canal-traefik"},
		// Resume on a cluster that issues certificates: the phases after the
		// interruption are the ones with state in the cluster.
		{DimOp + "=" + OpResume, DimPKI + "=private-ca"},
		// Growth on the dataplane that has per-node agents to extend.
		{DimOp + "=" + OpGrow, DimDataplane + "=cilium-gw"},
		// An upgrade with an agent: servers and agents move by different
		// paths, and a single-node cluster exercises only one of them.
		{DimOp + "=" + OpUpgrade, DimNodes + "=2"},
		// An upgrade under a VIP: the address every node joins through has to
		// keep answering while the node serving it restarts.
		{DimOp + "=" + OpUpgrade, DimExposure + "=node-ips"},
		// Air-gapped with the Gateway API. That preset is what makes the
		// artifact set four files rather than two -- the combined image
		// archive, Cilium's own, the binaries and the Gateway API bundle --
		// and the reading of them is where three of the nine defects the
		// hand-run found were hiding.
		{DimNetwork + "=airgap", DimDataplane + "=cilium-gw"},
		// Air-gapped on two nodes: the agent is the node that has to take its
		// images from its peer rather than from a registry, which is the
		// whole point of the embedded mirror.
		{DimNetwork + "=airgap", DimNodes + "=2"},
		// A failover needs three servers. With two, losing one loses the
		// quorum, so a two-node failover could pass only by testing nothing.
		{DimOp + "=" + OpFailover, DimNodes + "=3"},
	}
}

// Values lists every value the matrix must cover, by dimension.
func Values() map[string][]string {
	return map[string][]string{
		DimNodes:     {"1", "2", "3"},
		DimDataplane: {"cilium-gw", "cilium-traefik", "canal-traefik"},
		DimPKI:       {"none", "private-ca", "byo-cert"},
		DimExposure:  {"node-ips", "lb-pool", "none"},
		DimRegistry:  {"embedded", "upstream"},
		DimGitOps:    {"true", "false"},
		DimNetwork:   {"online", "airgap"},
		DimOp:        {OpBuild, OpGrow, OpResume, OpReapply, OpUpgrade, OpFailover},
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
		DimNetwork + "=" + c.network(),
		DimOp + "=" + c.Op,
	}
}

// network is the case's mode, defaulting to online so that a row which says
// nothing about the network reads as the ordinary one.
func (c Case) network() string {
	if c.Network == "" {
		return "online"
	}
	return c.Network
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
	// Third is the machine only a three-server case uses. In that case the
	// machine that is an agent everywhere else is a server too.
	Third string
	User  string
	// PasswordRef is a SourceRef, never a password: cluster.yaml carries no
	// plaintext secret, and the matrix produces real documents.
	PasswordRef v1alpha1.SourceRef

	// VIP, LBPool and LBAddress are addresses on the segment the nodes are on.
	// Empty falls back to the documentation ranges below, which is right for a
	// default in a public repository and wrong for a run: kube-vip claims the
	// VIP on an interface, and no interface is on 192.0.2.0/24, so every case
	// with a VIP failed at vip-interface saying exactly that. The harness knows
	// which segment it was pointed at; this package must not guess.
	VIP       string
	LBPool    string
	LBAddress string
}

// vip is the address the cluster registers under.
func (h Hosts) vip() string { return orDefault(h.VIP, vipAddress) }

// pool is the range the load balancer allocates from.
func (h Hosts) pool() string { return orDefault(h.LBPool, lbPool) }

// lb is the address a gateway is pinned to.
func (h Hosts) lb() string { return orDefault(h.LBAddress, lbAddress) }

func orDefault(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
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
				"malmok.dev/matrix-case": c.Name,
			},
		},
		Network: v1alpha1.NetworkSpec{Mode: v1alpha1.NetworkMode(c.network())},
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

	// An air-gapped document has to say where the artifacts landed: the
	// installer reads its tarball and image archives from there rather than
	// fetching them, and validation refuses a document that names no image
	// source at all.
	if c.ArtifactPath != "" {
		spec.Kubernetes.ArtifactPath = c.ArtifactPath
	}
	// The charts cross the gap as files rather than through a mirror, which
	// is the path a site with no registry of its own has to take.
	if c.ChartDir != "" {
		spec.Registry.ChartDir = c.ChartDir
	}

	// Two nodes are a server and an agent. Three are three servers: high
	// availability is three etcd members and the lab has three machines, so
	// the one that is an agent everywhere else is a server here. A grown case
	// starts with one node and gains the agent on the second apply.
	switch {
	case c.Nodes == 3:
		spec.Topology.Servers = append(spec.Topology.Servers, node(h.Agent), node(h.Third))
	case c.Nodes == 2 || (c.Op == OpGrow && grown):
		spec.Topology.Agents = []v1alpha1.NodeSpec{node(h.Agent)}
	}

	if c.VIP {
		spec.Topology.RegistrationAddress = h.vip()
		spec.Topology.VIP = &v1alpha1.VIPSpec{
			Provider: "kube-vip", Address: h.vip(), Mode: "arp",
		}
	} else {
		// No VIP: the server registers under its own address, and the
		// document has to say it accepts that trade (ADR-008).
		spec.Topology.RegistrationAddress = h.Server
		spec.Topology.AcceptNodeRegistration = true
	}

	if c.Exposure == "lb-pool" {
		spec.Kubernetes.Dataplane.LoadBalancerPool = []string{h.pool()}
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
			gw.Address = h.lb()
		}
		// A certificate mode gets a listener to serve on: supplied material
		// nobody terminates with is material nobody can tell is broken, and
		// an issuer that signs nothing proves only that it exists.
		if c.PKI == "byo-cert" || c.PKI == "private-ca" {
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
//
// The documentation ranges (RFC 5737, RFC 2606) rather than somebody's real
// network: these are defaults in a public repository, and a default that names
// a real address is a default aimed at whatever answers there.
//
// They are defaults and not the values a run uses. A VIP has to be on the
// nodes' own segment -- kube-vip claims it on an interface -- so a run that
// takes these verbatim fails at vip-interface, which is what every VIP case
// did until Hosts carried the real ones.
const (
	vipAddress = "192.0.2.10"
	lbPool     = "192.0.2.16/29"
	lbAddress  = "192.0.2.16"
	domain     = "lab.example.com"
)

// String renders a case the way a report should print it.
func (c Case) String() string {
	return fmt.Sprintf("%s (%s)", c.Name, strings.Join(c.values(), " "))
}
