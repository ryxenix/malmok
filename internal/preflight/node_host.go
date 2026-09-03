package preflight

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ryxenix/malmok/api/v1alpha1"
	"github.com/ryxenix/malmok/internal/dataplane"
)

// ---------------------------------------------------------------------------
// PF-3xx: what the host's security posture allows
// ---------------------------------------------------------------------------

// CheckSELinux implements PF-301.
func (n *Node) CheckSELinux(ctx context.Context, f Facts) ProbeResult {
	r := n.run(ctx, "getenforce 2>/dev/null || echo ABSENT")
	if r.ExitCode < 0 {
		return unmeasured("PF-301", "the node could not be asked: "+r.Err())
	}
	mode := r.Out()

	if mode == "ABSENT" || mode == "" {
		if f.Family() == v1alpha1.OSRocky {
			// On a RHEL derivative the tools missing is itself the finding: the
			// policy is expected and something removed it.
			return warnf("PF-301", "SELINUX_TOOLS_MISSING",
				"getenforce is not present on a RHEL-family node, so the SELinux state cannot be read; "+
					"RKE2 expects the policy to be in place there")
		}
		return skipped("PF-301", "the node does not run SELinux")
	}
	switch strings.ToLower(mode) {
	case "enforcing":
		return passf("PF-301", "SELinux is enforcing; PF-302 checks that the RKE2 policy can be installed")
	case "permissive":
		return warnf("PF-301", "SELINUX_PERMISSIVE",
			"SELinux is permissive, so denials are logged and not enforced; "+
				"a cluster built this way fails the moment somebody sets it back to enforcing")
	case "disabled":
		return passf("PF-301", "SELinux is disabled")
	}
	return unmeasured("PF-301", "getenforce printed "+strconv.Quote(mode))
}

// CheckSELinuxPolicy implements PF-302.
func (n *Node) CheckSELinuxPolicy(ctx context.Context, f Facts) ProbeResult {
	if f.Family() != v1alpha1.OSRocky {
		return skipped("PF-302", "rke2-selinux applies to RHEL-family nodes only")
	}
	r := n.run(ctx, "rpm -q rke2-selinux 2>/dev/null || echo ABSENT")
	if r.ExitCode < 0 {
		return unmeasured("PF-302", "the node could not be asked: "+r.Err())
	}
	if r.Out() != "ABSENT" {
		return passf("PF-302", "%s is installed", r.Out())
	}
	// Whether it can be obtained depends on the network mode: online can fetch
	// it, an airgap has to have carried it.
	if n.Cluster.Network.Mode == v1alpha1.NetworkAirgap {
		return warnf("PF-302", "SELINUX_POLICY_ABSENT",
			"rke2-selinux is not installed and this is an airgapped build, so it cannot be fetched; "+
				"omitting it on a RHEL-family node produces cluster-wide pod failures "+
				"whose cause is very hard to trace from the symptom")
	}
	return passf("PF-302", "rke2-selinux is not installed yet; the install fetches it")
}

// CheckAppArmor implements PF-303.
func (n *Node) CheckAppArmor(ctx context.Context) ProbeResult {
	r := n.run(ctx, "aa-enabled 2>/dev/null || echo ABSENT")
	if r.ExitCode < 0 {
		return unmeasured("PF-303", "the node could not be asked: "+r.Err())
	}
	switch {
	case strings.Contains(r.Out(), "ABSENT"):
		return skipped("PF-303", "the node does not run AppArmor")
	case r.OK():
		return passf("PF-303", "AppArmor is enabled; container profiles are honoured")
	}
	return passf("PF-303", "AppArmor is present but not enabled")
}

// CheckFirewall implements PF-304.
//
// The unit is not the firewall. A node with ufw.service running and its policy
// set to allow answers "active" to `systemctl is-active ufw` while blocking
// nothing, and a node with the service stopped can still have nftables rules
// loaded. Each firewall is asked about itself.
func (n *Node) CheckFirewall(ctx context.Context) ProbeResult {
	var active []string

	if r := n.run(ctx, "ufw status 2>/dev/null | head -1 || true"); r.Out() != "" {
		if strings.Contains(strings.ToLower(r.Out()), "status: active") {
			active = append(active, "ufw")
		}
	}
	if r := n.run(ctx, "firewall-cmd --state 2>/dev/null || true"); strings.TrimSpace(r.Out()) == "running" {
		active = append(active, "firewalld")
	}

	if len(active) == 0 {
		return passf("PF-304", "no host firewall is filtering; the inter-node port matrix is PF-601's question")
	}
	// A firewall is not a fault. What it is, is a thing that has to have the
	// cluster's ports opened in it, and saying which one is running is what
	// lets somebody do that.
	return warnf("PF-304", "FIREWALL_ACTIVE",
		"%s is active on this node; RKE2 needs 6443, 9345, 10250, 2379-2380 and the dataplane's own ports "+
			"open between nodes, and PF-601 measures whether they are",
		strings.Join(active, " and "))
}

// CheckCISPrerequisites implements PF-305.
func (n *Node) CheckCISPrerequisites(ctx context.Context) ProbeResult {
	h := n.Cluster.OS.Hardening
	wanted := (h.CISProfile != nil && *h.CISProfile) ||
		(h.PrepareCISPrerequisites != nil && *h.PrepareCISPrerequisites)
	if !wanted {
		return skipped("PF-305", "neither the CIS profile nor its prerequisites are requested")
	}
	// The etcd user has to exist before the profile is applied, and the kernel
	// parameters RKE2 sets under protect-kernel-defaults have to be settable.
	r := n.run(ctx, "id etcd >/dev/null 2>&1 && echo HAVE || echo MISSING")
	if r.ExitCode < 0 {
		return unmeasured("PF-305", "the node could not be asked: "+r.Err())
	}
	if strings.Contains(r.Out(), "MISSING") {
		return warnf("PF-305", "CIS_ETCD_USER_MISSING",
			"CIS hardening is requested and no etcd user exists; the install creates it, "+
				"but a node where accounts are managed centrally needs it in place first")
	}
	return passf("PF-305", "the etcd user exists, so CIS hardening can be applied without creating accounts")
}

// ---------------------------------------------------------------------------
// PF-4xx: storage
// ---------------------------------------------------------------------------

// DataDir is where RKE2 keeps everything that grows.
const DataDir = "/var/lib/rancher"

// Thresholds for PF-402 and PF-403.
const (
	MinFreeBytes  = 20 << 30
	WantFreeBytes = 50 << 30
	MinFreeInodes = 200000
)

// CheckDataDirMount implements PF-401.
//
// A separate mount is a recommendation, not a requirement, and the reason is
// worth stating: images and etcd both live under the data directory, and on a
// shared root a runaway image pull takes the whole node down rather than one
// directory.
func (n *Node) CheckDataDirMount(ctx context.Context) ProbeResult {
	r := n.run(ctx, "findmnt -no TARGET,FSTYPE "+DataDir+" 2>/dev/null || echo NOTAMOUNT")
	if r.ExitCode < 0 {
		return unmeasured("PF-401", "the node could not be asked: "+r.Err())
	}
	if !strings.Contains(r.Out(), "NOTAMOUNT") && r.Out() != "" {
		return passf("PF-401", "%s is its own mount (%s)", DataDir, strings.Join(strings.Fields(r.Out()), " "))
	}
	// The directory not existing is the normal state before an install and is
	// not an error; what is being reported is that it will share the root.
	root := n.run(ctx, "findmnt -no SOURCE,FSTYPE / 2>/dev/null").Out()
	return warnf("PF-401", "DATADIR_ON_ROOT",
		"%s is not a separate mount, so images and etcd share the root filesystem (%s); "+
			"a runaway image pull then fills the whole node rather than one directory",
		DataDir, strings.Join(strings.Fields(root), " "))
}

// CheckDiskSpace implements PF-402.
func (n *Node) CheckDiskSpace(ctx context.Context) ProbeResult {
	// The parent is measured because the data directory itself does not exist
	// before an install, and df on a missing path answers nothing.
	r := n.run(ctx, "df -PB1 /var/lib | tail -1")
	if !r.OK() {
		return unmeasured("PF-402", "df could not be run: "+r.Err())
	}
	fields := strings.Fields(r.Out())
	if len(fields) < 4 {
		return unmeasured("PF-402", "df printed "+strconv.Quote(r.Out()))
	}
	avail, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return unmeasured("PF-402", "the available column was "+strconv.Quote(fields[3]))
	}

	switch {
	case avail < MinFreeBytes:
		return failf("PF-402", "DISK_INSUFFICIENT",
			"%s free under /var/lib, below the %s minimum; the airgap image set alone is larger than that",
			humanBytes(avail), humanBytes(MinFreeBytes))
	case avail < WantFreeBytes:
		return warnf("PF-402", "DISK_TIGHT",
			"%s free under /var/lib; %s is the comfortable figure once images accumulate across upgrades",
			humanBytes(avail), humanBytes(WantFreeBytes))
	}
	return passf("PF-402", "%s free under /var/lib", humanBytes(avail))
}

// CheckInodes implements PF-403.
//
// Container image layers are many small files. A filesystem formatted with a
// small inode count runs out of inodes with most of its capacity unused, and
// the error -- no space left on device -- sends people to look at df, which
// shows plenty of room.
func (n *Node) CheckInodes(ctx context.Context) ProbeResult {
	r := n.run(ctx, "df -Pi /var/lib | tail -1")
	if !r.OK() {
		return unmeasured("PF-403", "df -i could not be run: "+r.Err())
	}
	fields := strings.Fields(r.Out())
	if len(fields) < 4 {
		return unmeasured("PF-403", "df -i printed "+strconv.Quote(r.Out()))
	}
	free, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		// Some filesystems report "-" for inodes because they allocate
		// dynamically. That is an answer, not a failure.
		return skipped("PF-403", "the filesystem does not report an inode count, which btrfs and xfs do dynamically")
	}
	if free < MinFreeInodes {
		return failf("PF-403", "INODES_LOW",
			"%d free inodes under /var/lib, below %d; image layers are many small files, "+
				"and running out reports 'no space left on device' while df still shows free capacity",
			free, MinFreeInodes)
	}
	return passf("PF-403", "%d free inodes under /var/lib", free)
}

// CheckFilesystem implements PF-407.
//
// The type matters for one specific reason: overlayfs refuses to mount on an
// XFS filesystem formatted without d_type support, and containerd then falls
// back to a snapshotter that is dramatically slower, if it starts at all.
func (n *Node) CheckFilesystem(ctx context.Context) ProbeResult {
	r := n.run(ctx, "findmnt -no FSTYPE,SOURCE --target /var/lib 2>/dev/null")
	if !r.OK() || r.Out() == "" {
		return unmeasured("PF-407", "the filesystem under /var/lib could not be identified: "+r.Err())
	}
	fields := strings.Fields(r.Out())
	fstype := fields[0]
	source := ""
	if len(fields) > 1 {
		source = fields[1]
	}

	switch fstype {
	case "ext4", "ext3":
		return passf("PF-407", "%s on %s", fstype, source)
	case "xfs":
		// ftype=0 is the one that breaks overlayfs, and it is only visible from
		// xfs_info, which needs the mount point.
		info := n.run(ctx, "xfs_info /var/lib 2>/dev/null | grep -o 'ftype=[01]' | head -1")
		if info.Out() == "ftype=0" {
			return failf("PF-407", "XFS_FTYPE_ZERO",
				"/var/lib is XFS formatted with ftype=0; overlayfs will not mount on it and the filesystem "+
					"has to be recreated with -n ftype=1, which is not something the install can do")
		}
		if info.Out() == "" {
			return warnf("PF-407", "XFS_FTYPE_UNKNOWN",
				"/var/lib is XFS and xfs_info is not available to confirm ftype=1; "+
					"overlayfs will not mount on an ftype=0 filesystem")
		}
		return passf("PF-407", "xfs with %s on %s", info.Out(), source)
	case "btrfs", "zfs":
		return warnf("PF-407", "FS_UNCOMMON",
			"/var/lib is %s; containerd uses a different snapshotter there and the combination "+
				"is far less exercised than ext4 or xfs", fstype)
	case "overlay", "tmpfs":
		return failf("PF-407", "FS_EPHEMERAL",
			"/var/lib is %s, which does not survive a reboot", fstype)
	}
	return warnf("PF-407", "FS_UNKNOWN", "/var/lib is %s, which has not been validated", fstype)
}

// ---------------------------------------------------------------------------
// PF-404, PF-405, PF-406: Longhorn prerequisites
// ---------------------------------------------------------------------------

// External block storage is out of scope for now by explicit decision: probing
// it properly means validating iscsid, the NFS client, multipath blacklists and
// the interaction between them, and none of that is worth carrying until the
// storage layer itself is being built.
//
// They are skips rather than passes, deliberately. NodeCapability.Passed treats
// a probe that never ran as not passed, and plan.decideStorage downgrades on
// FailedAt, which a skip does not satisfy -- so an unimplemented probe neither
// claims Longhorn works nor silently downgrades the storage the document asked
// for. Whichever way that is resolved has to be a decision somebody makes.

const longhornStub = "external block storage is not probed yet; " +
	"this reports neither a pass nor a failure so nothing is downgraded on evidence that was never collected"

// CheckISCSI implements PF-404.
func (n *Node) CheckISCSI(context.Context) ProbeResult { return unmeasured("PF-404", longhornStub) }

// CheckNFSClient implements PF-405.
func (n *Node) CheckNFSClient(context.Context) ProbeResult { return unmeasured("PF-405", longhornStub) }

// CheckMultipath implements PF-406.
func (n *Node) CheckMultipath(context.Context) ProbeResult { return unmeasured("PF-406", longhornStub) }

// ---------------------------------------------------------------------------
// PF-5xx: the clock
// ---------------------------------------------------------------------------

// CheckTimeSync implements PF-501.
//
// etcd is the reason this is a blocking check rather than a tidiness one: its
// leases and its leader election both depend on the members agreeing about
// time, and a cluster whose clocks drift apart loses quorum for reasons that
// look like a network fault.
func (n *Node) CheckTimeSync(ctx context.Context) ProbeResult {
	r := n.run(ctx, "timedatectl show -p NTPSynchronized -p Timezone -p NTP --value 2>/dev/null")
	if !r.OK() {
		return unmeasured("PF-501", "timedatectl could not be run: "+r.Err())
	}
	lines := strings.Fields(r.Out())
	if len(lines) < 2 {
		return unmeasured("PF-501", "timedatectl printed "+strconv.Quote(r.Out()))
	}
	// --value prints in the order asked: NTP, NTPSynchronized, Timezone is not
	// guaranteed, so the booleans are found rather than positioned.
	synced := false
	for _, l := range lines {
		if l == "yes" {
			synced = true
		}
	}
	if !synced {
		return failf("PF-501", "CLOCK_UNSYNCED",
			"the clock is not synchronised (%s); etcd leases and leader election both assume the members "+
				"agree about time, and the symptom of drift is a quorum loss that reads as a network fault",
			strings.Join(lines, " "))
	}
	return passf("PF-501", "the clock is synchronised (%s)", strings.Join(lines, " "))
}

// Offset is how far one node's clock is from the machine running the tool, and
// how precisely that could be measured.
type Offset struct {
	Delta time.Duration
	// Uncertainty is half the round trip: the node's clock was read somewhere
	// inside it, so the reading is good to within this much either way.
	Uncertainty time.Duration
}

// CheckClockSkew implements PF-502 across nodes.
//
// Offsets are compared rather than raw times. The nodes are read one after
// another over SSH, and the gap between two reads is not skew -- subtracting
// raw times reports the tool's own round trip as drift, which is how a pair of
// NTP-synchronised nodes came to look two seconds apart.
//
// A difference smaller than what the measurement could resolve is not evidence
// of anything. Reporting it as skew would be the tool blaming the cluster for
// its own latency.
// CheckClockSkewTrend is CheckClockSkew given a second set of readings taken
// a little later.
//
// One sample cannot tell a clock that is converging from one that is wrong.
// Nodes come up from a reboot seconds apart, each reports itself synchronised
// while its time daemon is still stepping, and the gap closes on its own --
// which a single measurement calls drift and blocks the build for. Two
// samples separated by a pause answer the question the operator actually
// has: is this settling, or is it staying wrong.
func CheckClockSkewTrend(first, second map[string]Offset, tolerance time.Duration) ProbeResult {
	latest := CheckClockSkew(second, tolerance)
	if !latest.Failed() || latest.Code != "CLOCK_SKEW" {
		return latest
	}
	if was, is := skewOf(first), skewOf(second); was > is && was-is > tolerance/10 {
		// Still closing. Say so rather than blocking: the operator is looking
		// at a cluster whose clocks are converging, and the honest verdict is
		// "measure again", not "these nodes are wrong".
		return warnResult("PF-502", "CLOCK_CONVERGING", fmt.Sprintf(
			"the node clocks are %s apart and closing (%s a moment ago), so their time daemons are "+
				"still stepping after a boot rather than drifting; re-run the checks in a minute, "+
				"and if the gap stops closing above %s, treat it as %s",
			is.Round(time.Millisecond), was.Round(time.Millisecond), tolerance, "CLOCK_SKEW"))
	}
	return latest
}

// skewOf is the spread between the furthest-apart readings.
func skewOf(readings map[string]Offset) time.Duration {
	var low, high time.Duration
	first := true
	for _, o := range readings {
		if first || o.Delta < low {
			low = o.Delta
		}
		if first || o.Delta > high {
			high = o.Delta
		}
		first = false
	}
	return high - low
}

func CheckClockSkew(readings map[string]Offset, tolerance time.Duration) ProbeResult {
	if len(readings) < 2 {
		return skipped("PF-502", "skew needs at least two nodes to compare")
	}

	var lowHost, highHost string
	var low, high Offset
	first := true
	for host, o := range readings {
		if first || o.Delta < low.Delta {
			low, lowHost = o, host
		}
		if first || o.Delta > high.Delta {
			high, highHost = o, host
		}
		first = false
	}

	skew := high.Delta - low.Delta
	margin := low.Uncertainty + high.Uncertainty

	switch {
	case skew <= tolerance:
		return passf("PF-502", "the node clocks agree to within %s (measured to +/-%s)",
			skew.Round(time.Millisecond), margin.Round(time.Millisecond))

	case skew <= margin:
		// Over the tolerance but inside what the measurement can resolve. The
		// honest answer is that this could not be measured well enough, not
		// that the clocks are wrong.
		return unmeasured("PF-502", fmt.Sprintf(
			"the readings differ by %s and could only be measured to +/-%s, which is not "+
				"enough to tell drift from the round trip. Both nodes report their clock "+
				"synchronised (PF-501); measure again on a quieter link if this matters",
			skew.Round(time.Millisecond), margin.Round(time.Millisecond)))
	}

	// With the remedy, because the finding on its own sends an operator to
	// read about etcd rather than to the one thing that fixes this: two nodes
	// synchronising against different sources drift apart while each reports
	// itself synchronised, which is exactly what a lab pair did at 1.1s.
	return failf("PF-502", "CLOCK_SKEW",
		"the node clocks differ by %s, more than the %s etcd tolerates (%s is behind %s); "+
			"the reading is good to +/-%s, so this is drift rather than measurement noise. "+
			"Point every node at the same time source and resynchronise -- with systemd-timesyncd, "+
			"put NTP=<server> in /etc/systemd/timesyncd.conf.d/, then "+
			"`timedatectl set-ntp true && systemctl restart systemd-timesyncd`; with chrony, set the "+
			"server in /etc/chrony/chrony.conf and `chronyc makestep`. Nodes can each report "+
			"themselves synchronised (PF-501) and still disagree when they follow different servers",
		skew.Round(time.Millisecond), tolerance, lowHost, highHost, margin.Round(time.Millisecond))
}

// CheckNTPServers implements PF-503.
func (n *Node) CheckNTPServers(ctx context.Context) ProbeResult {
	// Whichever daemon is in use, what is wanted is the same: is it talking to
	// anything.
	if r := n.run(ctx, "chronyc -n sources 2>/dev/null | tail -n +3"); r.OK() && r.Out() != "" {
		return passf("PF-503", "chrony has %d sources", len(strings.Split(r.Out(), "\n")))
	}
	r := n.run(ctx, "timedatectl show-timesync -p ServerName -p ServerAddress --value 2>/dev/null")
	if r.OK() && strings.TrimSpace(r.Out()) != "" {
		return passf("PF-503", "systemd-timesyncd is using %s", strings.Join(strings.Fields(r.Out()), " "))
	}
	return warnf("PF-503", "NTP_SOURCE_UNKNOWN",
		"no time daemon reported a server it is talking to; the clock may be synchronised now "+
			"and have nothing keeping it that way")
}

// CheckTimezones implements PF-504 across nodes.
//
// Different zones are not a fault -- UTC everywhere is a convention, not a
// requirement -- but logs correlated across nodes become guesswork, and an
// incident is a bad time to discover it.
func CheckTimezones(byHost map[string]string) ProbeResult {
	zones := map[string][]string{}
	for host, z := range byHost {
		if z == "" {
			continue
		}
		zones[z] = append(zones[z], host)
	}
	if len(zones) <= 1 {
		for z := range zones {
			return passf("PF-504", "every node is in %s", z)
		}
		return unmeasured("PF-504", "no node reported a timezone")
	}
	var parts []string
	for z, hosts := range zones {
		parts = append(parts, fmt.Sprintf("%s: %s", z, strings.Join(hosts, ", ")))
	}
	sortStrings(parts)
	return warnf("PF-504", "TIMEZONE_MIXED",
		"the nodes are in different timezones (%s); correlating logs across them becomes guesswork",
		strings.Join(parts, " | "))
}

// quotePath wraps a path for the shell. Single quotes, with the one escape a
// single-quoted string allows, because a path is operator input and a space in
// it should be a path with a space rather than two arguments.
func quotePath(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// CheckArtifactPath implements PF-709.
//
// An air-gapped install reads RKE2's release artifacts from a directory on the
// node rather than fetching them, and the installer verifies them against the
// checksum file beside them. What it does not do is explain a directory that
// is absent, empty, or holds a different version than the document asks for:
// it reports a failed download on a machine that was never going to download
// anything.
//
// The observable is the files themselves. The version is read out of the
// tarball's own name, so a directory staged for the previous release is caught
// here rather than after the install has replaced the binary.
func (n *Node) CheckArtifactPath(ctx context.Context, spec v1alpha1.ClusterSpec) ProbeResult {
	path := strings.TrimSpace(spec.Kubernetes.ArtifactPath)
	preset := spec.Kubernetes.Dataplane.Preset
	if path == "" {
		return skipped("PF-709", "the document names no artifact path; the installer fetches its own")
	}

	r := n.run(ctx, `p=`+quotePath(path)+`
[ -d "$p" ] || { echo "MISSING"; exit 0; }
ls -1 "$p" 2>/dev/null | tr '\n' ' '`)
	if r.ExitCode < 0 {
		return unmeasured("PF-709", "the node could not be asked: "+r.Err())
	}

	out := strings.TrimSpace(r.Out())
	if out == "MISSING" {
		return failf("PF-709", "ARTIFACT_PATH_MISSING",
			"kubernetes.artifactPath is %s and no such directory exists on this node; "+
				"the release artifacts are carried to each node before the install, not fetched by it", path)
	}
	if out == "" {
		return failf("PF-709", "ARTIFACT_PATH_EMPTY",
			"kubernetes.artifactPath %s is empty; it holds rke2.linux-<arch>.tar.gz, "+
				"its sha256sum file, the images archive and install.sh", path)
	}

	files := strings.Fields(out)
	has := func(match func(string) bool) bool {
		for _, f := range files {
			if match(f) {
				return true
			}
		}
		return false
	}

	var missing []string
	if !has(func(f string) bool { return strings.HasPrefix(f, "rke2.linux-") && strings.HasSuffix(f, ".tar.gz") }) {
		missing = append(missing, "rke2.linux-<arch>.tar.gz")
	}
	if !has(func(f string) bool { return strings.HasPrefix(f, "sha256sum-") }) {
		missing = append(missing, "sha256sum-<arch>.txt")
	}
	// install.sh is what the step runs. Without it the artifact path is a
	// directory of tarballs nothing knows how to apply.
	if !has(func(f string) bool { return f == "install.sh" }) {
		missing = append(missing, "install.sh")
	}
	if len(missing) > 0 {
		return failf("PF-709", "ARTIFACT_PATH_INCOMPLETE",
			"kubernetes.artifactPath %s is missing %s; it holds %s",
			path, strings.Join(missing, ", "), out)
	}

	// The images archive is what makes the cluster come up without a registry,
	// and which archives are needed depends on the dataplane the document
	// asked for. Two names, and each is silent in its own way when missing.
	//
	// rke2-images.<os>-<arch>.tar is the one the installer stages, and it is
	// the gate: the loop that copies the per-CNI archives runs only after it
	// has been staged. Carrying only the per-CNI archives loads nothing at
	// all, and rke2-server dies two minutes later looking for its runtime
	// image.
	//
	// The combined archive carries Calico and Flannel and not Cilium. So a
	// cilium-* preset needs rke2-images-cilium as well, and without it the
	// install succeeds, the node registers, and every Cilium pod sits in
	// ImagePullBackOff against a registry the node cannot reach.
	isArchive := func(f string) bool {
		return strings.HasSuffix(f, ".tar.zst") || strings.HasSuffix(f, ".tar.gz")
	}
	combined := has(func(f string) bool { return strings.HasPrefix(f, "rke2-images.") && isArchive(f) })
	perCNI := has(func(f string) bool { return strings.HasPrefix(f, "rke2-images-") && isArchive(f) })

	if !combined {
		if perCNI {
			return failf("PF-709", "ARTIFACT_IMAGES_SPLIT",
				"%s holds the per-CNI image archives and not rke2-images.linux-<arch>.tar.zst; "+
					"the installer stages that one first and copies the others only afterwards, "+
					"so as it stands nothing is loaded at all", path)
		}
		return passf("PF-709",
			"%s holds the binaries and no rke2-images archive, so the node will pull its "+
				"system images -- which works where there is a route and hangs where there is not", path)
	}

	if cni := cniArchive(preset); cni != "" {
		if !has(func(f string) bool { return strings.HasPrefix(f, "rke2-images-"+cni) && isArchive(f) }) {
			return failf("PF-709", "ARTIFACT_IMAGES_NO_CNI",
				"%s holds rke2-images.linux-<arch>.tar.zst, which carries Calico and Flannel; "+
					"the document asks for %s, so rke2-images-%s.linux-<arch>.tar.zst has to be "+
					"carried beside it or every %s pod waits on a pull that cannot happen",
				path, preset, cni, cni)
		}
	}

	// The Gateway API CRDs are the fourth thing to carry. The step that
	// applies them reads exactly this file, so it is asked for by name.
	if bundle := dataplane.GatewayAPIBundle(spec); bundle != "" {
		if !has(func(f string) bool { return f == bundle }) {
			return failf("PF-709", "ARTIFACT_NO_GATEWAY_API",
				"%s does not hold %s; the document asks for %s, which installs the Gateway API "+
					"types from that bundle and cannot fetch it here",
				path, bundle, preset)
		}
	}

	return passf("PF-709", "%s holds the release artifacts (%s)", path, out)
}

// cniArchive names the per-CNI image archive a preset needs beside the combined
// one, or "" when the combined archive already carries its CNI.
//
// Calico and Flannel are in rke2-images.<os>-<arch>.tar; Cilium is published
// separately and is not.
func cniArchive(preset v1alpha1.DataplanePreset) string {
	if strings.HasPrefix(string(preset), "cilium-") {
		return "cilium"
	}
	return ""
}
