package preflight

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"platform.ryxen.dev/malmok/api/v1alpha1"
	"platform.ryxen.dev/malmok/internal/codes"
	"platform.ryxen.dev/malmok/internal/exec"
)

// Node runs the probes that need a shell on the machine.
//
// Nothing here changes the node. Every command reads, tests or dry-runs, and
// where a value can only be observed after loading a kernel module the probe
// reports that it could not be measured rather than loading it -- preflight is
// read-only (CLAUDE.md), and a preflight that modifies the node cannot be run
// twice with the same meaning.
//
// Commands are measured, not inferred. `systemctl is-active ufw` reports the
// unit, not the firewall: a node with the service running and every policy set
// to allow answers "active" while the firewall blocks nothing. Probes ask the
// thing itself.
type Node struct {
	Runner  exec.Runner
	Spec    v1alpha1.NodeSpec
	Cluster v1alpha1.ClusterSpec

	// Timeout bounds one command. Zero means twenty seconds, which is long
	// enough for a slow `bpftool feature probe` on a loaded machine.
	Timeout time.Duration

	// cache holds output already collected, so probes sharing a command do not
	// each pay for it.
	cache map[string]exec.Result
}

// Minimums for PF-107. A server carries etcd and the API server; an agent
// carries workloads, and what it needs is the customer's business.
const (
	MinServerCPU   = 2
	MinServerBytes = 4 << 30
	MinAgentCPU    = 1
	MinAgentBytes  = 2 << 30
)

// MinSystemdVersion is what RKE2's units and cgroup handling assume (PF-106).
const MinSystemdVersion = 245

func (n *Node) timeout() time.Duration {
	if n.Timeout <= 0 {
		return 20 * time.Second
	}
	return n.Timeout
}

// run executes a command once and remembers the answer.
func (n *Node) run(ctx context.Context, cmd string) exec.Result {
	if n.cache == nil {
		n.cache = map[string]exec.Result{}
	}
	if r, ok := n.cache[cmd]; ok {
		return r
	}
	c, cancel := context.WithTimeout(ctx, n.timeout())
	defer cancel()

	res, err := n.Runner.Run(c, cmd)
	if err != nil {
		// A transport failure is recorded as a command that could not run, so
		// every probe downstream reports "could not be measured" rather than
		// inventing a verdict from empty output.
		res.ExitCode = -1
		res.Stderr = strings.TrimSpace(res.Stderr + "\n" + err.Error())
	}
	n.cache[cmd] = res
	return res
}

// unmeasured is the result for a probe that could not run.
//
// It is a skip rather than a failure. A node that did not answer has not been
// shown to be broken, and reporting it as broken sends people to fix the wrong
// thing; what it has shown is that the evidence is missing.
func unmeasured(id, why string) ProbeResult {
	return ProbeResult{
		ID: id, Status: StatusSkip, Severity: codes.SeverityInfo,
		Code: "NOT_MEASURED", Detail: why,
	}
}

func passf(id, format string, args ...any) ProbeResult {
	return ProbeResult{
		ID: id, Status: StatusPass, Severity: codes.SeverityInfo,
		Detail: fmt.Sprintf(format, args...),
	}
}

func failf(id, reason, format string, args ...any) ProbeResult {
	return fail(id, reason, fmt.Sprintf(format, args...))
}

// warnResult is a deliberate softening: the same code can have sub-cases of
// different weight (an unreadable NTP daemon is not an unreachable NTP
// server), and this is how a probe says "this one is only worth a warning"
// against its code's registered severity.
func warnResult(id, reason, detail string) ProbeResult {
	return ProbeResult{
		ID: id, Status: StatusFail, Severity: codes.SeverityWarn,
		Code: reason, Detail: detail,
	}
}

func warnf(id, reason, format string, args ...any) ProbeResult {
	return warnResult(id, reason, fmt.Sprintf(format, args...))
}

// ---------------------------------------------------------------------------
// Facts
// ---------------------------------------------------------------------------

// Facts are what the node says about itself, collected once.
type Facts struct {
	OSRelease map[string]string
	Kernel    string
	Arch      string
	Hostname  string
	FQDN      string
	CPUs      int
	MemBytes  int64
	// Addresses are the node's global IPv4 addresses, without the netmask.
	// PF-606 needs them: an address a node is already carrying is this
	// cluster's own rather than somebody else's.
	Addresses []string
	// MachineUUID is the DMI product UUID -- on a Proxmox VM it is the
	// smbios1.uuid the hypervisor stamped in, which makes it the one
	// identifier that survives reinstalls and renames. The handoff carries
	// it so an inventory tool can bind node to VM without guessing.
	MachineUUID string
}

// Collect gathers the identity every other probe and the plan generator need.
func (n *Node) Collect(ctx context.Context) Facts {
	f := Facts{OSRelease: map[string]string{}}

	if r := n.run(ctx, "cat /etc/os-release"); r.OK() {
		for _, line := range strings.Split(r.Stdout, "\n") {
			k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok {
				continue
			}
			f.OSRelease[k] = strings.Trim(v, `"`)
		}
	}
	f.Kernel = n.run(ctx, "uname -r").Out()
	f.Arch = n.run(ctx, "uname -m").Out()
	f.Hostname = n.run(ctx, "hostname").Out()
	f.FQDN = n.run(ctx, "hostname -f 2>/dev/null || hostname").Out()
	// Root-only file, which the elevated runner can read. Lower-cased because
	// DMI reports it upper-case and Proxmox configures it lower-case, and the
	// consumer matches them byte for byte.
	f.MachineUUID = strings.ToLower(n.run(ctx, "cat /sys/class/dmi/id/product_uuid 2>/dev/null").Out())

	if r := n.run(ctx, "nproc"); r.OK() {
		f.CPUs, _ = strconv.Atoi(r.Out())
	}
	if r := n.run(ctx, "awk '/^MemTotal:/{print $2}' /proc/meminfo"); r.OK() {
		kb, _ := strconv.ParseInt(r.Out(), 10, 64)
		f.MemBytes = kb * 1024
	}
	if r := n.run(ctx, "ip -o -4 addr show scope global | awk '{print $4}'"); r.OK() {
		for _, cidr := range strings.Fields(r.Out()) {
			f.Addresses = append(f.Addresses, strings.SplitN(cidr, "/", 2)[0])
		}
	}
	return f
}

// Family maps the os-release ID onto the families the tool supports.
func (f Facts) Family() v1alpha1.OSFamily {
	id := strings.ToLower(f.OSRelease["ID"])
	like := strings.ToLower(f.OSRelease["ID_LIKE"])
	switch {
	case id == "ubuntu":
		return v1alpha1.OSUbuntu
	case id == "rocky", id == "rhel", id == "almalinux", id == "centos":
		return v1alpha1.OSRocky
	// A RHEL derivative that is not one of the named ones still has the SELinux
	// policy, the package names and the module layout the Rocky path assumes.
	// Debian derivatives are not mapped onto Ubuntu the same way: only the two
	// families in the schema are supported, and quietly treating Debian as
	// Ubuntu would install packages that are not there.
	case strings.Contains(like, "rhel"), strings.Contains(like, "fedora"):
		return v1alpha1.OSRocky
	}
	return ""
}

// ---------------------------------------------------------------------------
// PF-1xx: the machine itself
// ---------------------------------------------------------------------------

// CheckOS implements PF-101.
func (n *Node) CheckOS(f Facts) ProbeResult {
	if len(f.OSRelease) == 0 {
		return unmeasured("PF-101", "/etc/os-release could not be read")
	}
	fam := f.Family()
	pretty := f.OSRelease["PRETTY_NAME"]
	if fam == "" {
		return failf("PF-101", "OS_UNSUPPORTED",
			"%s is not one of the supported families; the RKE2 packaging, the SELinux policy "+
				"and the module names all differ by family", pretty)
	}
	return passf("PF-101", "%s (%s)", pretty, fam)
}

// CheckArch implements PF-102.
func (n *Node) CheckArch(f Facts) ProbeResult {
	switch f.Arch {
	case "x86_64", "amd64":
		return passf("PF-102", "amd64")
	case "aarch64", "arm64":
		return passf("PF-102", "arm64")
	case "":
		return unmeasured("PF-102", "uname -m returned nothing")
	}
	return failf("PF-102", "ARCH_UNSUPPORTED",
		"%s is not an architecture RKE2 publishes images for", f.Arch)
}

// CheckKernel implements PF-103.
//
// The version is recorded rather than judged. What matters for the dataplane is
// whether eBPF actually loads, which PF-204 measures; refusing a kernel by
// number would reject vendor kernels that carry the features backported.
func (n *Node) CheckKernel(f Facts) ProbeResult {
	if f.Kernel == "" {
		return unmeasured("PF-103", "uname -r returned nothing")
	}
	return passf("PF-103", "kernel %s; what it can actually do is measured by PF-201 through PF-204", f.Kernel)
}

// CheckMachineUUID implements PF-109.
//
// Recorded rather than judged, like PF-103: there is no wrong UUID, only a
// missing one -- a container or an exotic board without DMI. Missing is a
// skip, because a node without the file has not been shown to be broken.
func (n *Node) CheckMachineUUID(f Facts) ProbeResult {
	if f.MachineUUID == "" {
		return unmeasured("PF-109", "/sys/class/dmi/id/product_uuid could not be read")
	}
	return passf("PF-109", "%s", f.MachineUUID)
}

// CheckCgroup implements PF-104.
func (n *Node) CheckCgroup(ctx context.Context) ProbeResult {
	r := n.run(ctx, "stat -fc %T /sys/fs/cgroup")
	if !r.OK() {
		return unmeasured("PF-104", "/sys/fs/cgroup could not be inspected: "+r.Err())
	}
	if r.Out() != "cgroup2fs" {
		return failf("PF-104", "CGROUP_V1",
			"/sys/fs/cgroup is %s, not the cgroup v2 unified hierarchy; "+
				"RKE2 and the kubelet expect v2 and memory accounting silently differs on v1", r.Out())
	}
	// Having the hierarchy is not the same as having the controllers on it.
	c := n.run(ctx, "cat /sys/fs/cgroup/cgroup.controllers")
	if c.OK() {
		for _, want := range []string{"cpu", "memory", "pids"} {
			if !strings.Contains(" "+c.Out()+" ", " "+want+" ") {
				return failf("PF-104", "CGROUP_CONTROLLER_MISSING",
					"the cgroup v2 hierarchy is active but the %s controller is not delegated (available: %s)",
					want, c.Out())
			}
		}
	}
	return passf("PF-104", "cgroup v2 with %s", c.Out())
}

// CheckSwap implements PF-105.
//
// The kubelet refuses to start with swap on unless it is told to tolerate it,
// and the failure names a flag rather than the swap device.
func (n *Node) CheckSwap(ctx context.Context) ProbeResult {
	r := n.run(ctx, "swapon --show=NAME,SIZE --noheadings 2>/dev/null")
	if r.ExitCode < 0 {
		return unmeasured("PF-105", "swapon could not be run: "+r.Err())
	}
	if r.Out() == "" {
		return passf("PF-105", "no swap is active")
	}
	return failf("PF-105", "SWAP_ACTIVE",
		"swap is active (%s); disable it and remove the entry from /etc/fstab, "+
			"or the kubelet refuses to start after the next reboot even if it starts now",
		strings.Join(strings.Fields(r.Out()), " "))
}

// CheckSystemd implements PF-106.
func (n *Node) CheckSystemd(ctx context.Context) ProbeResult {
	r := n.run(ctx, "systemctl --version | head -1")
	if !r.OK() {
		return unmeasured("PF-106", "systemctl --version could not be run: "+r.Err())
	}
	fields := strings.Fields(r.Out())
	if len(fields) < 2 {
		return unmeasured("PF-106", "systemctl --version printed "+strconv.Quote(r.Out()))
	}
	// "systemd 255 (255.4-1ubuntu8.14)"
	v, err := strconv.Atoi(strings.TrimFunc(fields[1], func(c rune) bool { return c < '0' || c > '9' }))
	if err != nil {
		return unmeasured("PF-106", "the systemd version could not be read from "+strconv.Quote(r.Out()))
	}
	if v < MinSystemdVersion {
		return failf("PF-106", "SYSTEMD_TOO_OLD",
			"systemd %d is older than %d, which is where the cgroup v2 delegation RKE2 relies on landed",
			v, MinSystemdVersion)
	}
	return passf("PF-106", "systemd %d", v)
}

// CheckResources implements PF-107.
func (n *Node) CheckResources(f Facts) ProbeResult {
	minCPU, minMem := MinAgentCPU, int64(MinAgentBytes)
	role := "agent"
	if n.Spec.Role == v1alpha1.RoleServer {
		minCPU, minMem, role = MinServerCPU, int64(MinServerBytes), "server"
	}

	if f.CPUs == 0 || f.MemBytes == 0 {
		return unmeasured("PF-107", "the CPU count or memory size could not be read")
	}
	if f.CPUs < minCPU || f.MemBytes < minMem {
		return failf("PF-107", "RESOURCES_BELOW_MINIMUM",
			"%d CPU and %s are below the %s minimum of %d CPU and %s",
			f.CPUs, humanBytes(f.MemBytes), role, minCPU, humanBytes(minMem))
	}
	return passf("PF-107", "%d CPU, %s", f.CPUs, humanBytes(f.MemBytes))
}

// CheckHomogeneous implements PF-108 across the nodes already collected.
//
// A mixed-architecture cluster is a supported thing to want and an unsupported
// thing to arrive at by accident: every chart then needs multi-arch images, and
// the failure is a pod that schedules onto the odd node and crash-loops.
func CheckHomogeneous(caps []NodeCapability) ProbeResult {
	byArch := map[string][]string{}
	for _, c := range caps {
		if c.Arch == "" {
			continue
		}
		byArch[c.Arch] = append(byArch[c.Arch], c.Host)
	}
	if len(byArch) <= 1 {
		for arch := range byArch {
			return passf("PF-108", "every node is %s", arch)
		}
		return unmeasured("PF-108", "no node reported its architecture")
	}
	var parts []string
	for arch, hosts := range byArch {
		parts = append(parts, fmt.Sprintf("%s: %s", arch, strings.Join(hosts, ", ")))
	}
	sortStrings(parts)
	return warnf("PF-108", "ARCH_MIXED",
		"the nodes are not all the same architecture (%s); every chart then needs multi-arch images, "+
			"and what fails is a pod that lands on the odd node",
		strings.Join(parts, " | "))
}

func humanBytes(b int64) string {
	const unit = 1 << 10
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for m := b / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
