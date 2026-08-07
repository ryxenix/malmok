package preflight

import (
	"context"
	"strconv"
	"time"

	"platform.ryxen.dev/platformctl/api/v1alpha1"
)

// Probe runs every node-local check and returns what the node turned out to be.
//
// Probes are run in a fixed order so two runs against the same node produce the
// same report, and each is independent: one that cannot be measured does not
// stop the ones after it. A preflight that gives up on the first unreadable
// file tells an operator about one problem per round trip.
func (n *Node) Probe(ctx context.Context) NodeCapability {
	f := n.Collect(ctx)

	cap := NodeCapability{
		Host:        n.Runner.Host(),
		Hostname:    f.Hostname,
		Role:        n.Spec.Role,
		Arch:        normaliseArch(f.Arch),
		OS:          OSInfo{Family: f.Family(), Version: f.OSRelease["VERSION_ID"], Kernel: f.Kernel},
		Probes:      map[string]ProbeResult{},
		CollectedAt: time.Now().UTC(),
	}

	results := []ProbeResult{
		// The machine.
		n.CheckOS(f),
		n.CheckArch(f),
		n.CheckKernel(f),
		n.CheckCgroup(ctx),
		n.CheckSwap(ctx),
		n.CheckSystemd(ctx),
		n.CheckResources(f),

		// eBPF, which decides the dataplane.
		n.CheckBPFSyscall(ctx),
		n.CheckBTF(ctx),
		n.CheckBPFFS(ctx),
		n.CheckBPFLoad(ctx),
		n.CheckLockdown(ctx),
		n.CheckLSM(ctx),
		n.CheckNetfilter(ctx),
		n.CheckConntrack(ctx),
		n.CheckModuleLoading(ctx),

		// Security posture.
		n.CheckSELinux(ctx, f),
		n.CheckSELinuxPolicy(ctx, f),
		n.CheckAppArmor(ctx),
		n.CheckFirewall(ctx),
		n.CheckCISPrerequisites(ctx),

		// Storage.
		n.CheckDataDirMount(ctx),
		n.CheckDiskSpace(ctx),
		n.CheckInodes(ctx),
		n.CheckISCSI(ctx),
		n.CheckNFSClient(ctx),
		n.CheckMultipath(ctx),
		n.CheckFilesystem(ctx),

		// The clock.
		n.CheckTimeSync(ctx),
		n.CheckNTPServers(ctx),

		// The network as this node sees it.
		n.CheckSysctls(ctx),
		n.CheckVIPInterface(ctx),
		n.CheckNodeIP(ctx),
		n.CheckStubResolver(ctx),

		// What is already here.
		n.CheckExistingRuntime(ctx),
		n.CheckExistingKubernetes(ctx),
		n.CheckPortsFree(ctx),
		n.CheckCNILeftovers(ctx),
		n.CheckPacketFilterLeftovers(ctx),
	}

	for _, r := range results {
		cap.Probes[r.ID] = r
	}
	return cap
}

// ProbePeers runs the checks that need another node to aim at.
//
// Separate from Probe because the peer list is only known once every node has
// been reached, and running them earlier would measure against nodes that had
// not been contacted yet.
func (n *Node) ProbePeers(ctx context.Context, peers []string, cap *NodeCapability) {
	for _, r := range []ProbeResult{
		n.CheckPortMatrix(ctx, peers),
		n.CheckMTU(ctx, peers),
	} {
		cap.Probes[r.ID] = r
	}
}

// Clock reads the node's clock for the skew comparison.
func (n *Node) Clock(ctx context.Context) (int64, bool) {
	r := n.run(ctx, "date -u +%s")
	if !r.OK() {
		return 0, false
	}
	v, err := strconv.ParseInt(r.Out(), 10, 64)
	return v, err == nil
}

// Timezone reads the node's timezone for the consistency comparison.
func (n *Node) Timezone(ctx context.Context) string {
	return n.run(ctx, "timedatectl show -p Timezone --value 2>/dev/null").Out()
}

// normaliseArch collapses the names the kernel and Go use onto one spelling, so
// PF-108 does not report x86_64 and amd64 as two architectures.
func normaliseArch(a string) string {
	switch a {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	}
	return a
}

// DefaultClockTolerance is how far apart node clocks may be (PF-502).
//
// etcd tolerates far less than this in practice; a second is the point at which
// something is clearly wrong rather than merely imprecise.
const DefaultClockTolerance = 1

// Cluster runs preflight across every node the document names.
//
// Cross-node checks come last because they need every node's answer: skew,
// timezone consistency, hostname uniqueness and architecture homogeneity are
// all statements about the set rather than about a machine.
type ClusterRun struct {
	Caps []NodeCapability
	// Shared holds the probes that are about the set rather than one node.
	Shared []ProbeResult
}

// Finish computes the cross-node probes from what was collected.
func Finish(caps []NodeCapability, clocks map[string]int64, zones map[string]string, spec v1alpha1.ClusterSpec) ClusterRun {
	hostnames := map[string]string{}
	for _, c := range caps {
		hostnames[c.Host] = c.Hostname
	}
	return ClusterRun{
		Caps: caps,
		Shared: []ProbeResult{
			CheckHomogeneous(caps),
			CheckHostnames(hostnames),
			CheckClockSkew(clocks, DefaultClockTolerance),
			CheckTimezones(zones),
			CheckCIDRs(spec),
		},
	}
}
