package preflight

import (
	"context"
	"strconv"
	"strings"
)

// The eBPF probes decide whether a Cilium dataplane is possible on this node.
//
// PF-201 through PF-203 read what the kernel advertises. PF-204 is the one that
// settles it, because everything the others read can be true on a node that
// still refuses the load: kernel lockdown, an LSM policy, a vendor security
// agent hooking the bpf syscall. The static indicators all look healthy and the
// dataplane does not come up.

// CheckBPFSyscall implements PF-201.
func (n *Node) CheckBPFSyscall(ctx context.Context) ProbeResult {
	r := n.run(ctx, kernelConfig+" | grep -E '^CONFIG_BPF_SYSCALL=' || true")
	if r.ExitCode < 0 {
		return unmeasured("PF-201", "the kernel configuration could not be read: "+r.Err())
	}
	if strings.Contains(r.Out(), "=y") {
		return passf("PF-201", "CONFIG_BPF_SYSCALL=y")
	}
	if r.Out() == "" {
		// Neither /proc/config.gz nor /boot/config-* exists. Some vendor
		// kernels ship neither, and PF-204 answers the question anyway.
		return unmeasured("PF-201", "the kernel does not publish its configuration; PF-204 measures the same thing directly")
	}
	return failf("PF-201", "BPF_SYSCALL_MISSING",
		"the kernel was built without CONFIG_BPF_SYSCALL (%s); no eBPF dataplane can run on it", r.Out())
}

// CheckBTF implements PF-202.
//
// Without BTF, Cilium falls back to compiling against kernel headers, which are
// frequently not installed on a minimal server image. The failure appears as an
// agent that crash-loops with a compiler error.
func (n *Node) CheckBTF(ctx context.Context) ProbeResult {
	r := n.run(ctx, "test -r /sys/kernel/btf/vmlinux && stat -c %s /sys/kernel/btf/vmlinux")
	if r.OK() && r.Out() != "" {
		size, _ := strconv.ParseInt(r.Out(), 10, 64)
		return passf("PF-202", "/sys/kernel/btf/vmlinux is present (%s)", humanBytes(size))
	}
	if r.ExitCode < 0 {
		return unmeasured("PF-202", "the node could not be asked: "+r.Err())
	}
	return failf("PF-202", "BTF_MISSING",
		"/sys/kernel/btf/vmlinux is absent, so the kernel was built without CONFIG_DEBUG_INFO_BTF; "+
			"Cilium then needs kernel headers at runtime, which a minimal server image does not carry")
}

// CheckBPFFS implements PF-203.
//
// Whether bpffs is mounted now matters less than whether it can be: the
// installer mounts it. What cannot be fixed at install time is a kernel with no
// bpf filesystem at all.
func (n *Node) CheckBPFFS(ctx context.Context) ProbeResult {
	if r := n.run(ctx, "stat -fc %T /sys/fs/bpf"); r.OK() && r.Out() == "bpf_fs" {
		return passf("PF-203", "bpffs is mounted at /sys/fs/bpf")
	}
	// Not mounted is the normal state before an install. What is being asked is
	// whether the kernel knows the filesystem type at all, which is readable
	// without mounting anything.
	r := n.run(ctx, "grep -qw bpf /proc/filesystems")
	switch {
	case r.ExitCode < 0:
		return unmeasured("PF-203", "the node could not be asked: "+r.Err())
	case r.OK():
		return passf("PF-203", "bpffs is not mounted yet; the kernel supports it and the install mounts it")
	}
	return failf("PF-203", "BPFFS_UNSUPPORTED",
		"the kernel does not list bpf in /proc/filesystems, so bpffs cannot be mounted")
}

// bpfLoadProbe is the shell that attempts a real program load.
//
// Two routes, because neither tool is universally present. bpftool is the
// normal one and is packaged with the kernel tools on both families. Where it
// is missing, python3 calls the bpf syscall directly through ctypes; the
// program is the smallest valid one -- set r0 to 0 and exit -- so what is being
// measured is the load path and nothing else.
//
// Both routes only read. A loaded program with no attachment point is freed
// when the process exits and the file descriptor closes.
const bpfLoadProbe = `
if command -v bpftool >/dev/null 2>&1; then
  out=$(bpftool feature probe kernel 2>&1)
  echo "$out" | grep -q 'program_type socket_filter is available' && { echo LOADED bpftool; exit 0; }
  echo "$out" | grep -q 'bpf() syscall.*restricted' && { echo DENIED bpftool restricted; exit 0; }
  echo "$out" | grep -qi 'program_type socket_filter is NOT available' && { echo DENIED bpftool socket_filter; exit 0; }
fi
if command -v python3 >/dev/null 2>&1; then
  python3 - <<'PYEOF'
import ctypes, struct, os
libc = ctypes.CDLL("libc.so.6", use_errno=True)
# BPF_MOV64_IMM(r0, 0) ; BPF_EXIT_INSN()
insns = struct.pack("<QQ", 0xb7, 0x95)
buf = ctypes.create_string_buffer(insns)
lic = ctypes.create_string_buffer(b"GPL")
attr = bytearray(120)
struct.pack_into("<I", attr, 0, 1)   # BPF_PROG_TYPE_SOCKET_FILTER
struct.pack_into("<I", attr, 4, 2)   # insn_cnt
struct.pack_into("<Q", attr, 8, ctypes.addressof(buf))
struct.pack_into("<Q", attr, 16, ctypes.addressof(lic))
ab = (ctypes.c_char * len(attr)).from_buffer(attr)
rc = libc.syscall(321, 5, ctypes.byref(ab), len(attr))
if rc >= 0:
    os.close(rc)
    print("LOADED python3")
else:
    print("DENIED python3 errno=%d" % ctypes.get_errno())
PYEOF
  exit 0
fi
echo NOTOOL
`

// CheckBPFLoad implements PF-204, the decisive eBPF probe.
func (n *Node) CheckBPFLoad(ctx context.Context) ProbeResult {
	r := n.run(ctx, bpfLoadProbe)
	out := r.Out()

	switch {
	case r.ExitCode < 0:
		return unmeasured("PF-204", "the node could not be asked: "+r.Err())

	case strings.HasPrefix(out, "LOADED"):
		return passf("PF-204", "a minimal eBPF program loaded on this node (via %s)",
			strings.TrimSpace(strings.TrimPrefix(out, "LOADED")))

	case strings.HasPrefix(out, "DENIED"):
		return failf("PF-204", "EBPF_LOAD_DENIED",
			"the kernel accepted no eBPF program (%s); every static indicator can look healthy "+
				"while lockdown, an LSM policy or a security agent rejects the load, "+
				"which is why this is measured rather than inferred",
			strings.TrimSpace(strings.TrimPrefix(out, "DENIED")))

	case strings.Contains(out, "NOTOOL"):
		return unmeasured("PF-204",
			"neither bpftool nor python3 is present, so the load could not be attempted; "+
				"install either, or accept that the dataplane choice rests on the static probes alone")
	}
	return unmeasured("PF-204", "the probe printed "+strconv.Quote(out)+" "+r.Err())
}

// CheckLockdown implements PF-205.
func (n *Node) CheckLockdown(ctx context.Context) ProbeResult {
	r := n.run(ctx, "cat /sys/kernel/security/lockdown 2>/dev/null || echo ABSENT")
	if r.ExitCode < 0 {
		return unmeasured("PF-205", "the node could not be asked: "+r.Err())
	}
	out := r.Out()

	// The active mode is the one in brackets.
	switch {
	case out == "ABSENT", out == "":
		return passf("PF-205", "the kernel has no lockdown interface, so nothing is restricted by it")
	case strings.Contains(out, "[none]"):
		return passf("PF-205", "kernel lockdown is off")
	case strings.Contains(out, "[integrity]"), strings.Contains(out, "[confidentiality]"):
		mode := "integrity"
		if strings.Contains(out, "[confidentiality]") {
			mode = "confidentiality"
		}
		// Lockdown is enabled by Secure Boot on both families. Saying so is the
		// difference between a fixable finding and a mysterious one.
		sb := n.run(ctx, "mokutil --sb-state 2>/dev/null | head -1 || true").Out()
		detail := "kernel lockdown is in " + mode + " mode, which restricts the bpf syscall"
		if sb != "" {
			detail += "; " + sb
		}
		detail += ". PF-204 is what says whether the load actually fails"
		return warnResult("PF-205", "LOCKDOWN_ACTIVE", detail)
	}
	return unmeasured("PF-205", "/sys/kernel/security/lockdown printed "+strconv.Quote(out))
}

// CheckLSM implements PF-206.
//
// The stack is recorded rather than judged: which modules are loaded decides
// what has to be configured later, and a node with BPF LSM active is a node
// where PF-204 is doing real work.
func (n *Node) CheckLSM(ctx context.Context) ProbeResult {
	r := n.run(ctx, "cat /sys/kernel/security/lsm 2>/dev/null || echo ABSENT")
	if r.ExitCode < 0 {
		return unmeasured("PF-206", "the node could not be asked: "+r.Err())
	}
	if r.Out() == "ABSENT" || r.Out() == "" {
		return unmeasured("PF-206", "the kernel does not publish its LSM stack")
	}
	return passf("PF-206", "the active LSM stack is %s", r.Out())
}

// netfilterModules are what the Canal fallback needs. Without them there is
// nothing left to fall back to when the eBPF probes fail.
var netfilterModules = []string{"br_netfilter", "ip_tables", "iptable_nat", "xt_conntrack", "overlay"}

// CheckNetfilter implements PF-207.
//
// Availability is what is asked, not whether the module is loaded: a module the
// installer can load is not a problem, and loading one here would be a change
// preflight is not allowed to make. `modinfo -n` resolves the file without
// loading it, and a builtin module answers through /sys/module.
func (n *Node) CheckNetfilter(ctx context.Context) ProbeResult {
	cmd := "for m in " + strings.Join(netfilterModules, " ") + "; do " +
		`if modinfo -n "$m" >/dev/null 2>&1 || [ -d "/sys/module/$m" ]; then :; else echo "$m"; fi; done`
	r := n.run(ctx, cmd)
	if r.ExitCode < 0 {
		return unmeasured("PF-207", "the node could not be asked: "+r.Err())
	}
	missing := strings.Fields(r.Out())
	if len(missing) == 0 {
		return passf("PF-207", "every module the Canal fallback needs is available: %s",
			strings.Join(netfilterModules, ", "))
	}
	return failf("PF-207", "NETFILTER_MODULES_MISSING",
		"the node has no %s; if the eBPF probes also fail there is nothing left to fall back to",
		strings.Join(missing, ", "))
}

// CheckConntrack implements PF-208.
//
// The table size lives under /proc/sys/net/netfilter, which does not exist
// until nf_conntrack is loaded, and on a node that has never run a container it
// usually is not. Loading it would be a change preflight is not allowed to
// make, so the honest answer when it is absent is that the value could not be
// measured -- not a number invented from the kernel default.
func (n *Node) CheckConntrack(ctx context.Context) ProbeResult {
	const wanted = 131072

	r := n.run(ctx, "cat /proc/sys/net/netfilter/nf_conntrack_max 2>/dev/null || echo ABSENT")
	if r.ExitCode < 0 {
		return unmeasured("PF-208", "the node could not be asked: "+r.Err())
	}
	if r.Out() == "ABSENT" {
		avail := n.run(ctx, "modinfo -n nf_conntrack >/dev/null 2>&1 || [ -d /sys/module/nf_conntrack ]")
		if avail.OK() {
			return unmeasured("PF-208",
				"nf_conntrack is not loaded, so the table size cannot be read; the module is available "+
					"and the install sets the value. Loading it here would be a change preflight must not make")
		}
		return failf("PF-208", "CONNTRACK_UNAVAILABLE",
			"nf_conntrack is neither loaded nor available as a module; kube-proxy and the Canal fallback both need it")
	}

	v, err := strconv.Atoi(r.Out())
	if err != nil {
		return unmeasured("PF-208", "nf_conntrack_max printed "+strconv.Quote(r.Out()))
	}
	if v < wanted {
		return warnf("PF-208", "CONNTRACK_LOW",
			"nf_conntrack_max is %d, below the %d a busy node needs; the symptom is dropped connections "+
				"under load with nothing in the application logs", v, wanted)
	}
	return passf("PF-208", "nf_conntrack_max is %d", v)
}

// CheckModuleLoading implements PF-209.
//
// A dry run asks the question without answering it destructively: `modprobe -n`
// resolves and checks the module without inserting it, so a locked-down node
// that refuses insertion is found without inserting anything.
func (n *Node) CheckModuleLoading(ctx context.Context) ProbeResult {
	r := n.run(ctx, "modprobe -n br_netfilter 2>&1; echo rc=$?")
	if r.ExitCode < 0 {
		return unmeasured("PF-209", "the node could not be asked: "+r.Err())
	}
	out := r.Out()
	if strings.Contains(out, "rc=0") {
		return passf("PF-209", "kernel modules can be resolved and loaded")
	}
	// A node where module loading is disabled entirely is a deliberate hardening
	// choice, and it decides the dataplane rather than merely inconveniencing it.
	return failf("PF-209", "MODULE_LOAD_DENIED",
		"a module could not be resolved for loading: %s", strings.TrimSpace(strings.TrimSuffix(out, "rc=1")))
}

// kernelConfig reads the kernel build configuration from wherever it lives.
const kernelConfig = `(zcat /proc/config.gz 2>/dev/null || cat /boot/config-$(uname -r) 2>/dev/null)`
