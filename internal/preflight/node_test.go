package preflight

import (
	"context"
	"strings"
	"testing"
	"time"

	"platform.ryxen.dev/malmok/api/v1alpha1"
	"platform.ryxen.dev/malmok/internal/codes"
	"platform.ryxen.dev/malmok/internal/exec"
)

// Every fixture below is output copied from a real machine, not output somebody
// imagined. That distinction has already paid for itself: `hostname -f` returns
// a short name on a normal node, `df -i --output` is rejected outright, and
// `systemctl is-active ufw` answers "active" on a node whose firewall is
// filtering nothing.

// ubuntuNode is the recorded state of an Ubuntu 24.04 node with the Kubernetes
// prerequisites already in place.
func ubuntuNode() map[string]exec.Result {
	return map[string]exec.Result{
		"cat /etc/os-release": {Stdout: `PRETTY_NAME="Ubuntu 24.04.3 LTS"
NAME="Ubuntu"
VERSION_ID="24.04"
VERSION="24.04.3 LTS (Noble Numbat)"
ID=ubuntu
ID_LIKE=debian
`},
		"uname -r":                              {Stdout: "6.8.0-107-generic\n"},
		"product_uuid":                          {Stdout: "9E107D9D-372B-4A6E-B2F8-4C6E1B2F8C6E\n"},
		"uname -m":                              {Stdout: "x86_64\n"},
		"hostname":                              {Stdout: "rke2-server-03\n"},
		"nproc":                                 {Stdout: "16\n"},
		"MemTotal":                              {Stdout: "65840068\n"},
		"systemctl --version":                   {Stdout: "systemd 255 (255.4-1ubuntu8.14)\n"},
		"stat -fc %T /sys/fs/cgroup":            {Stdout: "cgroup2fs\n"},
		"cat /sys/fs/cgroup/cgroup.controllers": {Stdout: "cpuset cpu io memory hugetlb pids rdma misc\n"},
		"swapon":                                {Stdout: "\n"},
		"CONFIG_BPF_SYSCALL":                    {Stdout: "CONFIG_BPF_SYSCALL=y\n"},
		"/sys/kernel/btf/vmlinux":               {Stdout: "6092870\n"},
		"stat -fc %T /sys/fs/bpf":               {Stdout: "bpf_fs\n"},
		"bpftool feature probe":                 {Stdout: "LOADED bpftool\n"},
		"lockdown":                              {Stdout: "[none] integrity confidentiality\n"},
		"/sys/kernel/security/lsm":              {Stdout: "lockdown,capability,landlock,yama,apparmor\n"},
		"br_netfilter ip_tables":                {Stdout: "\n"},
		"nf_conntrack_max":                      {Stdout: "262144\n"},
		"modprobe -n br_netfilter":              {Stdout: "rc=0\n"},
		"getenforce":                            {Stdout: "ABSENT\n"},
		"aa-enabled":                            {Stdout: "Yes\n"},
		"ufw status":                            {Stdout: "Status: inactive\n"},
		"firewall-cmd --state":                  {Stdout: "\n"},
		"findmnt -no TARGET,FSTYPE /var/lib/rancher":  {Stdout: "NOTAMOUNT\n"},
		"findmnt -no SOURCE,FSTYPE /":                 {Stdout: "/dev/sda2 ext4\n"},
		"df -PB1 /var/lib":                            {Stdout: "/dev/sda2      133569777664 4900819968 122668929024   4% /\n"},
		"df -Pi /var/lib":                             {Stdout: "/dev/sda2       8323072 148129 8174943    2% /\n"},
		"findmnt -no FSTYPE,SOURCE --target /var/lib": {Stdout: "ext4 /dev/sda2\n"},
		"timedatectl show -p NTPSynchronized":         {Stdout: "yes\nEtc/UTC\nyes\n"},
		"timedatectl show-timesync":                   {Stdout: "ntp.ubuntu.com\n185.125.190.57\n"},
		"chronyc":                                     {ExitCode: 127},
		"sysctl -n net.ipv4.ip_forward":               {Stdout: "0\n"},
		"ip -o -4 addr show scope global":             {Stdout: "enp6s18 192.168.88.241/24\n"},
		"nameserver":                                  {Stdout: "nameserver 127.0.0.53\n"},
		"resolvectl status":                           {Stdout: "DNS Servers: 192.168.88.1\n"},
		"for b in docker containerd":                  {Stdout: "\n"},
		"/etc/rancher/rke2":                           {Stdout: "\n"},
		"ss -lntpH":                                   {Stdout: "\n"},
		"ip -br link show":                            {Stdout: "lo\nenp6s18\n"},
		"iptables-save":                               {Stdout: "0\n"},
	}
}

func ubuntuProber(t *testing.T, overrides map[string]exec.Result) (*Node, *exec.Fake) {
	t.Helper()
	responses := ubuntuNode()
	for k, v := range overrides {
		responses[k] = v
	}
	f := &exec.Fake{Responses: responses}
	return &Node{
		Runner:  f,
		Spec:    v1alpha1.NodeSpec{Host: "192.168.88.241", Role: v1alpha1.RoleServer},
		Cluster: baseSpec(),
	}, f
}

// The whole set has to run against a healthy node without a single blocking
// finding, or the probes are rejecting a machine that works.
func TestProbeHealthyNode(t *testing.T) {
	n, _ := ubuntuProber(t, nil)
	cap := n.Probe(context.Background())

	if cap.Arch != "amd64" {
		t.Errorf("arch is %q, want amd64 (x86_64 has to be normalised or PF-108 sees two architectures)", cap.Arch)
	}
	if cap.OS.Family != v1alpha1.OSUbuntu || cap.OS.Version != "24.04" {
		t.Errorf("os is %+v", cap.OS)
	}
	if !cap.EBPFUsable() {
		t.Error("a node that loads an eBPF program was reported as unable to run one")
	}
	if b := cap.Blocking(); len(b) > 0 {
		for _, r := range b {
			t.Errorf("blocked by %s: %s", r.ID, r.Detail)
		}
	}

	// Every probe must produce a result. A probe that silently returns nothing
	// is indistinguishable from one that passed.
	for _, id := range []string{
		"PF-101", "PF-102", "PF-103", "PF-104", "PF-105", "PF-106", "PF-107",
		"PF-201", "PF-202", "PF-203", "PF-204", "PF-205", "PF-206", "PF-207", "PF-208", "PF-209",
		"PF-301", "PF-302", "PF-303", "PF-304", "PF-305",
		"PF-401", "PF-402", "PF-403", "PF-404", "PF-405", "PF-406", "PF-407",
		"PF-501", "PF-503",
		"PF-605", "PF-607", "PF-609", "PF-610",
		"PF-801", "PF-802", "PF-803", "PF-804", "PF-805",
	} {
		if _, ok := cap.Probes[id]; !ok {
			t.Errorf("%s produced no result", id)
		}
	}
}

// Preflight is read-only. A probe that loads a module or writes a sysctl cannot
// be run twice with the same meaning, and on a customer's node it is a change
// nobody authorised.
func TestProbesNeverModifyTheNode(t *testing.T) {
	n, f := ubuntuProber(t, nil)
	n.Probe(context.Background())

	forbidden := []string{
		"modprobe ", // loading a module, as opposed to `modprobe -n`
		"sysctl -w", // writing a kernel parameter
		"systemctl start", "systemctl enable", "systemctl restart",
		"apt-get", "apt install", "yum ", "dnf ",
		"mount ", "swapoff", "mkdir ", "rm ", "> /", "tee ",
	}
	for _, cmd := range f.Log {
		for _, bad := range forbidden {
			if !strings.Contains(cmd, bad) {
				continue
			}
			// `modprobe -n` is a dry run and is the point of PF-209.
			if bad == "modprobe " && strings.Contains(cmd, "modprobe -n") {
				continue
			}
			t.Errorf("a probe would have changed the node: %q contains %q", firstLine(cmd), bad)
		}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " ..."
	}
	return s
}

func TestNodeProbes(t *testing.T) {
	tests := []struct {
		name      string
		overrides map[string]exec.Result
		id        string
		wantCode  string
		// wantStatus defaults to fail.
		wantStatus Status
		wantIn     string
	}{
		{
			name:      "cgroup v1",
			overrides: map[string]exec.Result{"stat -fc %T /sys/fs/cgroup": {Stdout: "tmpfs\n"}},
			id:        "PF-104", wantCode: "CGROUP_V1",
		},
		{
			name:      "cgroup v2 without the memory controller delegated",
			overrides: map[string]exec.Result{"cat /sys/fs/cgroup/cgroup.controllers": {Stdout: "cpuset cpu io pids\n"}},
			id:        "PF-104", wantCode: "CGROUP_CONTROLLER_MISSING", wantIn: "memory",
		},
		{
			name:      "swap is on",
			overrides: map[string]exec.Result{"swapon": {Stdout: "/swap.img 4G\n"}},
			id:        "PF-105", wantCode: "SWAP_ACTIVE", wantIn: "/etc/fstab",
		},
		{
			name:      "systemd too old",
			overrides: map[string]exec.Result{"systemctl --version": {Stdout: "systemd 219 (219-78.el7)\n"}},
			id:        "PF-106", wantCode: "SYSTEMD_TOO_OLD",
		},
		{
			name: "below the server minimum",
			overrides: map[string]exec.Result{
				"nproc": {Stdout: "1\n"}, "MemTotal": {Stdout: "2000000\n"},
			},
			id: "PF-107", wantCode: "RESOURCES_BELOW_MINIMUM",
		},
		{
			// The case the whole eBPF group exists for: every static indicator
			// healthy, and the kernel still refuses the load.
			name:      "eBPF load denied while every static indicator passes",
			overrides: map[string]exec.Result{"bpftool feature probe": {Stdout: "DENIED bpftool restricted\n"}},
			id:        "PF-204", wantCode: "EBPF_LOAD_DENIED", wantIn: "measured rather than inferred",
		},
		{
			name:      "no BTF",
			overrides: map[string]exec.Result{"/sys/kernel/btf/vmlinux": {ExitCode: 1}},
			id:        "PF-202", wantCode: "BTF_MISSING", wantIn: "kernel headers",
		},
		{
			name:      "neither bpftool nor python3",
			overrides: map[string]exec.Result{"bpftool feature probe": {Stdout: "NOTOOL\n"}},
			id:        "PF-204", wantStatus: StatusSkip, wantIn: "could not be attempted",
		},
		{
			name:      "kernel lockdown restricts bpf",
			overrides: map[string]exec.Result{"lockdown": {Stdout: "none [integrity] confidentiality\n"}},
			id:        "PF-205", wantCode: "LOCKDOWN_ACTIVE", wantIn: "PF-204",
		},
		{
			name:      "the Canal fallback has nothing to fall back to",
			overrides: map[string]exec.Result{"br_netfilter ip_tables": {Stdout: "br_netfilter\niptable_nat\n"}},
			id:        "PF-207", wantCode: "NETFILTER_MODULES_MISSING",
		},
		{
			// Measured on the real node before anything had loaded the module.
			// The value cannot be read and preflight must not load it.
			name:      "conntrack is not loaded",
			overrides: map[string]exec.Result{"nf_conntrack_max": {Stdout: "ABSENT\n"}},
			id:        "PF-208", wantStatus: StatusSkip, wantIn: "change preflight must not make",
		},
		{
			name:      "conntrack table is small",
			overrides: map[string]exec.Result{"nf_conntrack_max": {Stdout: "65536\n"}},
			id:        "PF-208", wantCode: "CONNTRACK_LOW", wantStatus: StatusFail,
		},
		{
			name:      "module loading is denied",
			overrides: map[string]exec.Result{"modprobe -n br_netfilter": {Stdout: "modprobe: ERROR: could not insert\nrc=1\n"}},
			id:        "PF-209", wantCode: "MODULE_LOAD_DENIED",
		},
		{
			// A node running the service with every policy set to allow. The
			// unit says active; the firewall filters nothing.
			name:      "ufw service running with the firewall inactive",
			overrides: map[string]exec.Result{"ufw status": {Stdout: "Status: inactive\n"}},
			id:        "PF-304", wantStatus: StatusPass,
		},
		{
			name:      "ufw actually filtering",
			overrides: map[string]exec.Result{"ufw status": {Stdout: "Status: active\n"}},
			id:        "PF-304", wantCode: "FIREWALL_ACTIVE", wantIn: "9345",
		},
		{
			name:      "not enough disk",
			overrides: map[string]exec.Result{"df -PB1 /var/lib": {Stdout: "/dev/sda2 40000000000 30000000000 10000000000 75% /\n"}},
			id:        "PF-402", wantCode: "DISK_INSUFFICIENT",
		},
		{
			name:      "not enough inodes",
			overrides: map[string]exec.Result{"df -Pi /var/lib": {Stdout: "/dev/sda2 200000 190000 10000 95% /\n"}},
			id:        "PF-403", wantCode: "INODES_LOW", wantIn: "no space left on device",
		},
		{
			name: "XFS formatted without d_type",
			overrides: map[string]exec.Result{
				"findmnt -no FSTYPE,SOURCE --target /var/lib": {Stdout: "xfs /dev/sda2\n"},
				"xfs_info": {Stdout: "ftype=0\n"},
			},
			id: "PF-407", wantCode: "XFS_FTYPE_ZERO", wantIn: "overlayfs",
		},
		{
			name:      "the clock is not synchronised",
			overrides: map[string]exec.Result{"timedatectl show -p NTPSynchronized": {Stdout: "no\nEtc/UTC\nno\n"}},
			id:        "PF-501", wantCode: "CLOCK_UNSYNCED", wantIn: "etcd",
		},
		{
			name:      "a previous RKE2 installation is still present",
			overrides: map[string]exec.Result{"/etc/rancher/rke2": {Stdout: "/etc/rancher/rke2\n/var/lib/rancher/rke2\n"}},
			id:        "PF-802", wantCode: "KUBERNETES_PRESENT",
		},
		{
			name:      "a control plane port is taken",
			overrides: map[string]exec.Result{"ss -lntpH": {Stdout: "0.0.0.0:6443 users:((\"haproxy\",pid=900,fd=7))\n"}},
			id:        "PF-803", wantCode: "PORT_IN_USE", wantIn: "haproxy",
		},
		{
			name:      "dataplane interfaces left over",
			overrides: map[string]exec.Result{"ip -br link show": {Stdout: "lo\nenp6s18\ncilium_host@cilium_net\nlxc1234\n"}},
			id:        "PF-804", wantCode: "CNI_LEFTOVER", wantIn: "cilium_host",
		},
		{
			name:      "packet filter rules left over",
			overrides: map[string]exec.Result{"iptables-save": {Stdout: "42\n"}},
			id:        "PF-805", wantCode: "PACKET_FILTER_LEFTOVER",
		},
		{
			// Reading the ruleset without root reports an empty ruleset that is
			// not empty. Claiming a pass from that would be a lie.
			name:      "packet filter needs root",
			overrides: map[string]exec.Result{"iptables-save": {Stdout: "Permission denied (you must be root)\n"}},
			id:        "PF-805", wantStatus: StatusSkip, wantIn: "not empty",
		},
		{
			name:      "multi-homed with no nodeIP pinned",
			overrides: map[string]exec.Result{"ip -o -4 addr show scope global": {Stdout: "eth0 10.10.0.11/24\neth1 203.0.113.5/24\n"}},
			id:        "PF-609", wantCode: "NODEIP_AMBIGUOUS", wantIn: "DMZ",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n, _ := ubuntuProber(t, tc.overrides)
			cap := n.Probe(context.Background())

			got, ok := cap.Probes[tc.id]
			if !ok {
				t.Fatalf("%s produced no result", tc.id)
			}

			want := tc.wantStatus
			if want == "" {
				want = StatusFail
			}
			if got.Status != want {
				t.Fatalf("%s is %s, want %s: %s", tc.id, got.Status, want, got.Detail)
			}
			if tc.wantCode != "" && got.Code != tc.wantCode {
				t.Errorf("%s code is %q, want %q: %s", tc.id, got.Code, tc.wantCode, got.Detail)
			}
			if tc.wantIn != "" && !strings.Contains(got.Detail, tc.wantIn) {
				t.Errorf("%s detail does not mention %q: %s", tc.id, tc.wantIn, got.Detail)
			}
		})
	}
}

// A node that cannot be reached must produce skips, never failures. Reporting
// an unreachable node as broken sends people to fix the wrong thing.
func TestUnreachableNodeProducesSkipsNotFailures(t *testing.T) {
	f := &exec.Fake{Err: exec.ErrNotConnected}
	n := &Node{Runner: f, Spec: v1alpha1.NodeSpec{Host: "10.0.0.9"}, Cluster: baseSpec()}

	cap := n.Probe(context.Background())
	for id, r := range cap.Probes {
		if r.Status == StatusFail && r.Severity == codes.SeverityBlock {
			t.Errorf("%s blocked on a node that was never reached: %s", id, r.Detail)
		}
	}
}

// The Longhorn probes are deliberately unimplemented. They must not claim a
// pass, and they must not trigger the storage downgrade either -- a downgrade
// on evidence that was never collected is worse than no probe.
func TestLonghornStubsNeitherPassNorFail(t *testing.T) {
	n, _ := ubuntuProber(t, nil)
	cap := n.Probe(context.Background())

	for _, id := range LonghornProbes {
		r := cap.Probes[id]
		if r.Status != StatusSkip {
			t.Errorf("%s is %s, want skip while it is unimplemented", id, r.Status)
		}
		if cap.Passed(id) {
			t.Errorf("%s claims a pass it did not measure", id)
		}
	}
	if ids := cap.FailedAt(LonghornProbes...); len(ids) > 0 {
		t.Errorf("the stubs would downgrade storage: %v", ids)
	}
}

// ---------------------------------------------------------------------------
// Cross-node probes
// ---------------------------------------------------------------------------

func TestCrossNodeProbes(t *testing.T) {
	t.Run("duplicate hostnames", func(t *testing.T) {
		got := CheckHostnames(map[string]string{
			"10.0.0.11": "node1", "10.0.0.12": "node1", "10.0.0.13": "node3",
		})
		if got.Code != "HOSTNAME_DUPLICATE" || !got.Failed() {
			t.Fatalf("PF-604 is %s/%s: %s", got.Status, got.Code, got.Detail)
		}
		if !strings.Contains(got.Detail, "removing a node") {
			t.Errorf("the failure does not say what it costs: %s", got.Detail)
		}
	})

	t.Run("a short hostname is not a failure", func(t *testing.T) {
		// Real nodes have short hostnames and RKE2 does not care.
		got := CheckHostnames(map[string]string{"10.0.0.11": "rke2-server-03"})
		if got.Failed() {
			t.Errorf("PF-604 failed on a normal short hostname: %s", got.Detail)
		}
	})

	t.Run("mixed architectures", func(t *testing.T) {
		got := CheckHomogeneous([]NodeCapability{
			{Host: "10.0.0.11", Arch: "amd64"}, {Host: "10.0.0.12", Arch: "arm64"},
		})
		if got.Code != "ARCH_MIXED" {
			t.Fatalf("PF-108 is %s/%s: %s", got.Status, got.Code, got.Detail)
		}
		if got.Severity == codes.SeverityBlock {
			t.Error("PF-108 blocks; a mixed cluster is a supported thing to want")
		}
	})

	t.Run("x86_64 and amd64 are one architecture", func(t *testing.T) {
		got := CheckHomogeneous([]NodeCapability{
			{Host: "10.0.0.11", Arch: normaliseArch("x86_64")},
			{Host: "10.0.0.12", Arch: normaliseArch("amd64")},
		})
		if got.Failed() {
			t.Errorf("PF-108 saw two architectures where there is one: %s", got.Detail)
		}
	})

	t.Run("clock skew", func(t *testing.T) {
		// Well-measured readings five seconds apart: real drift.
		got := CheckClockSkew(map[string]Offset{
			"a": {Delta: 0, Uncertainty: 5 * time.Millisecond},
			"b": {Delta: 5 * time.Second, Uncertainty: 5 * time.Millisecond},
		}, DefaultClockTolerance)
		if got.Code != "CLOCK_SKEW" {
			t.Fatalf("PF-502 is %s/%s: %s", got.Status, got.Code, got.Detail)
		}

		agree := CheckClockSkew(map[string]Offset{
			"a": {Delta: 0, Uncertainty: 5 * time.Millisecond},
			"b": {Delta: 10 * time.Millisecond, Uncertainty: 5 * time.Millisecond},
		}, DefaultClockTolerance)
		if agree.Failed() {
			t.Errorf("PF-502 failed on agreeing clocks: %s", agree.Detail)
		}
	})

	// The nodes are read one after another over SSH, and the gap between two
	// reads is not skew. A pair of NTP-synchronised nodes looked two seconds
	// apart because the tool was measuring its own round trip.
	t.Run("a difference inside the measurement error is not drift", func(t *testing.T) {
		got := CheckClockSkew(map[string]Offset{
			"a": {Delta: 0, Uncertainty: 2 * time.Second},
			"b": {Delta: 2 * time.Second, Uncertainty: 2 * time.Second},
		}, DefaultClockTolerance)

		if got.Failed() {
			t.Fatalf("PF-502 blamed the cluster for the tool's latency: %s", got.Detail)
		}
		if got.Status != StatusSkip {
			t.Errorf("PF-502 is %s, want a skip that says it could not be measured", got.Status)
		}
		if !strings.Contains(got.Detail, "round trip") {
			t.Errorf("the result does not say why it could not tell: %s", got.Detail)
		}
	})

	t.Run("one node cannot be skewed against itself", func(t *testing.T) {
		if got := CheckClockSkew(map[string]Offset{"a": {}}, time.Second); got.Status != StatusSkip {
			t.Errorf("PF-502 is %s, want skip", got.Status)
		}
	})

	t.Run("mixed timezones", func(t *testing.T) {
		got := CheckTimezones(map[string]string{"a": "Etc/UTC", "b": "Asia/Seoul"})
		if got.Code != "TIMEZONE_MIXED" || got.Severity == codes.SeverityBlock {
			t.Fatalf("PF-504 is %s/%s/%s", got.Status, got.Severity, got.Code)
		}
	})
}

// PF-601 distinguishes refused from dropped. Before an install nothing is
// listening, so refused is the expected answer and a timeout is the finding.
func TestPortMatrixDistinguishesRefusedFromDropped(t *testing.T) {
	t.Run("refused is expected before an install", func(t *testing.T) {
		n, _ := ubuntuProber(t, map[string]exec.Result{"/dev/tcp/": {Stdout: "rc=1\n"}})
		got := n.CheckPortMatrix(context.Background(), []string{"10.0.0.12"})
		if got.Failed() {
			t.Errorf("PF-601 failed on refused connections: %s", got.Detail)
		}
	})

	t.Run("a timeout means something is dropping it", func(t *testing.T) {
		n, _ := ubuntuProber(t, map[string]exec.Result{"/dev/tcp/": {Stdout: "rc=124\n"}})
		got := n.CheckPortMatrix(context.Background(), []string{"10.0.0.12"})
		if got.Code != "PORTS_FILTERED" {
			t.Fatalf("PF-601 is %s/%s: %s", got.Status, got.Code, got.Detail)
		}
		if !strings.Contains(got.Detail, "9345") {
			t.Errorf("the failure does not name the port people miss: %s", got.Detail)
		}
	})

	t.Run("one node has no peers", func(t *testing.T) {
		n, _ := ubuntuProber(t, nil)
		if got := n.CheckPortMatrix(context.Background(), nil); got.Status != StatusSkip {
			t.Errorf("PF-601 is %s, want skip", got.Status)
		}
	})
}

// PF-109 records the DMI product UUID -- the identity a hypervisor stamps
// into a VM, and the one field the handoff has for binding a node to the
// machine an inventory tool knows. Lower-cased on collection, because DMI
// reports upper-case, Proxmox configures lower-case, and the consumer
// compares byte for byte.
func TestMachineUUIDIsRecordedLowerCase(t *testing.T) {
	n, _ := ubuntuProber(t, nil)
	f := n.Collect(t.Context())
	r := n.CheckMachineUUID(f)
	if r.Status != StatusPass {
		t.Fatalf("PF-109 on a machine with DMI: %+v", r)
	}
	if want := "9e107d9d-372b-4a6e-b2f8-4c6e1b2f8c6e"; r.Detail != want {
		t.Errorf("detail = %q, want the lower-cased UUID %q", r.Detail, want)
	}
}

// A machine without the file -- a container, an exotic board -- has not been
// shown to be broken; PF-109 skips rather than fails, and the handoff simply
// omits the field.
func TestMachineUUIDMissingIsASkipNotAFailure(t *testing.T) {
	n, _ := ubuntuProber(t, map[string]exec.Result{"product_uuid": {ExitCode: 1}})
	f := n.Collect(t.Context())
	r := n.CheckMachineUUID(f)
	if r.Status != StatusSkip {
		t.Fatalf("PF-109 without DMI should skip, got %+v", r)
	}
}

// A probe's severity is its code's registered severity. Swap is the case that
// found this: PF-105 is registered warn because l0-node-prep turns swap off,
// yet the probe hardcoded block -- so preflight refused the install and told
// the operator to do by hand what the next phase automates. The same
// hardcoding kept degrade-registered eBPF findings from ever reaching the
// plan's fallback path.
func TestFailedProbesCarryTheirRegisteredSeverity(t *testing.T) {
	n, _ := ubuntuProber(t, map[string]exec.Result{
		"swapon": {Stdout: "/dev/sda3 32G\n"},
	})
	r := n.CheckSwap(t.Context())
	if r.Status != StatusFail {
		t.Fatalf("swap on did not fail the check: %+v", r)
	}
	if r.Severity != codes.SeverityWarn {
		t.Errorf("PF-105 severity = %s; registered warn, because apply disables swap", r.Severity)
	}

	// And a degrade-registered finding degrades rather than blocks.
	den := map[string]exec.Result{"bpftool feature probe": {Stdout: "DENIED permission denied\n"}}
	n2, _ := ubuntuProber(t, den)
	r2 := n2.CheckBPFLoad(t.Context())
	if r2.Status != StatusFail || r2.Severity != codes.SeverityDegrade {
		t.Errorf("PF-204 = %s/%s; registered degrade, the plan's cue to fall back", r2.Status, r2.Severity)
	}
}
