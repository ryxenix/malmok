package codes

// Preflight codes (PF-xxx). Collected from docs/10-preflight-plan.md and
// docs/20-cert.md §4.
//
// Preflight is READ-ONLY. No code in this file may describe a check that
// mutates a node; mutation belongs to apply. PF-203 is the closest call — it
// mounts bpffs to find out whether mounting works — and it must restore the
// prior state before returning.
//
// Number blocks:
//
//	PF-1xx  base            PF-6xx  network
//	PF-2xx  eBPF/dataplane  PF-7xx  registry / PKI reachability
//	PF-3xx  security        PF-8xx  residue / conflict
//	PF-4xx  storage         PF-9xx  certificate material (docs/20-cert.md)
//	PF-5xx  time
//
// PF-9xx belongs to docs/20-cert.md and is reserved for validation of supplied
// certificate MATERIAL. Gateway and DNS prerequisites do not go there — that is
// why the gateway address check is PF-612 and not PF-908, which the schema
// referenced for a while without any definition existing.

const (
	catBase        = "Base"
	catDataplane   = "eBPF / dataplane capability"
	catSecurity    = "Security posture"
	catStorage     = "Storage"
	catTime        = "Time"
	catNetwork     = "Network"
	catRegistryPKI = "Registry / PKI"
	catResidue     = "Residue / conflict"
	catCertificate = "Certificate material"
)

var preflightCodes = []Code{
	// --- PF-1xx · Base ---------------------------------------------------
	{
		ID: "PF-101", Family: FamilyPreflight, Category: catBase,
		Summary:  "OS family and version are supported",
		Message:  "Unsupported OS family or version; no validated profile covers this distribution",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-102", Family: FamilyPreflight, Category: catBase,
		Summary:  "CPU architecture is amd64 or arm64",
		Message:  "Unsupported CPU architecture; only amd64 and arm64 are supported",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-103", Family: FamilyPreflight, Category: catBase,
		Summary:  "Kernel version",
		Message:  "Kernel version recorded for the audit report",
		Severity: SeverityInfo,
	},
	{
		ID: "PF-104", Family: FamilyPreflight, Category: catBase,
		Summary:  "cgroup v2 unified hierarchy is active",
		Message:  "cgroup v2 unified hierarchy is not active",
		Severity: SeverityDegrade,
	},
	{
		ID: "PF-105", Family: FamilyPreflight, Category: catBase,
		Summary:  "Swap is disabled",
		Message:  "Swap is active; apply will disable it",
		Severity: SeverityWarn,
	},
	{
		ID: "PF-106", Family: FamilyPreflight, Category: catBase,
		Summary:  "systemd version is 245 or newer",
		Message:  "systemd is older than the required version 245",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-107", Family: FamilyPreflight, Category: catBase,
		Summary:  "CPU and memory meet the minimum",
		Message:  "Node is below the recommended minimum of 2 cores and 4 GB of memory",
		Severity: SeverityWarn,
	},
	{
		ID: "PF-108", Family: FamilyPreflight, Category: catBase,
		Summary:  "Node architectures are homogeneous",
		Message:  "Nodes report differing CPU architectures; images must exist for every one of them",
		Severity: SeverityWarn,
	},
	{
		ID: "PF-109", Family: FamilyPreflight, Category: catBase,
		Summary:  "Machine UUID",
		Message:  "Machine UUID recorded for the handoff; it is the node's durable identity across reinstalls",
		Severity: SeverityInfo,
	},

	// --- PF-2xx · eBPF / dataplane capability ----------------------------
	{
		ID: "PF-201", Family: FamilyPreflight, Category: catDataplane,
		Summary:  "Kernel exposes CONFIG_BPF_SYSCALL",
		Message:  "Kernel was built without CONFIG_BPF_SYSCALL",
		Severity: SeverityDegrade,
	},
	{
		ID: "PF-202", Family: FamilyPreflight, Category: catDataplane,
		Summary:  "Kernel BTF is present",
		Message:  "Kernel BTF is absent at /sys/kernel/btf/vmlinux",
		Severity: SeverityDegrade,
	},
	{
		ID: "PF-203", Family: FamilyPreflight, Category: catDataplane,
		Summary:  "bpffs can be mounted",
		Message:  "bpffs is neither mounted nor mountable at /sys/fs/bpf",
		Severity: SeverityDegrade,
	},
	{
		ID: "PF-204", Family: FamilyPreflight, Category: catDataplane,
		Summary: "A minimal eBPF program actually loads",
		Message: "Loading a minimal eBPF program failed; this node cannot run an eBPF dataplane",
		// The decisive probe for dataplane selection. Reading config symbols
		// alone misses LSM policy, kernel lockdown and third-party security
		// agents, all of which reject the load at runtime while every static
		// indicator still looks healthy.
		Severity: SeverityDegrade,
		Reasons: []string{
			"EBPF_LOAD_DENIED",
			"EBPF_NO_BTF",
			"EBPF_VERIFIER_REJECT",
			"EBPF_PERM",
		},
	},
	{
		ID: "PF-205", Family: FamilyPreflight, Category: catDataplane,
		Summary:  "Kernel lockdown and Secure Boot permit eBPF",
		Message:  "Kernel lockdown or Secure Boot restricts eBPF program loading",
		Severity: SeverityDegrade,
	},
	{
		ID: "PF-206", Family: FamilyPreflight, Category: catDataplane,
		Summary:  "Active LSM stack",
		Message:  "A third-party LSM is active and may interfere with the eBPF dataplane",
		Severity: SeverityWarn,
	},
	{
		ID: "PF-207", Family: FamilyPreflight, Category: catDataplane,
		Summary:  "netfilter modules required by the Canal fallback are available",
		Message:  "Required netfilter modules are unavailable; the canal-traefik fallback cannot run",
		Severity: SeverityDegrade,
	},
	{
		ID: "PF-208", Family: FamilyPreflight, Category: catDataplane,
		Summary:  "conntrack table size",
		Message:  "nf_conntrack_max is low for the expected connection volume",
		Severity: SeverityWarn,
	},
	{
		ID: "PF-209", Family: FamilyPreflight, Category: catDataplane,
		Summary:  "Kernel modules can be loaded",
		Message:  "Loading kernel modules is not permitted on this node",
		Severity: SeverityDegrade,
	},

	// --- PF-3xx · Security posture ---------------------------------------
	{
		ID: "PF-301", Family: FamilyPreflight, Category: catSecurity,
		Summary:  "SELinux mode",
		Message:  "SELinux mode recorded for the audit report",
		Severity: SeverityInfo,
	},
	{
		ID: "PF-302", Family: FamilyPreflight, Category: catSecurity,
		Summary:  "rke2-selinux package is obtainable",
		Message:  "SELinux is enforcing on an RHEL-family node but the rke2-selinux package is not available",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-303", Family: FamilyPreflight, Category: catSecurity,
		Summary:  "AppArmor profiles",
		Message:  "AppArmor profiles are active and may constrain container runtimes",
		Severity: SeverityWarn,
	},
	{
		ID: "PF-304", Family: FamilyPreflight, Category: catSecurity,
		Summary:  "Host firewall state",
		Message:  "firewalld or ufw is active; the cluster ports have to be opened in it (PF-601 measures whether they are)",
		Severity: SeverityWarn,
	},
	{
		ID: "PF-305", Family: FamilyPreflight, Category: catSecurity,
		Summary:  "CIS profile prerequisites",
		Message:  "CIS prerequisites recorded; missing items become degrade only when the CIS profile is requested",
		Severity: SeverityInfo,
	},

	// --- PF-4xx · Storage -------------------------------------------------
	{
		ID: "PF-401", Family: FamilyPreflight, Category: catStorage,
		Summary: "/var/lib/rancher is a separate mount",
		Message: "/var/lib/rancher sits on the root filesystem; image accumulation will eventually fill /",
		// The most frequent on-prem outage. Warn, but loudly.
		Severity: SeverityWarn,
	},
	{
		ID: "PF-402", Family: FamilyPreflight, Category: catStorage,
		Summary:  "Free disk capacity",
		Message:  "Free capacity is below the 20 GB hard minimum (50 GB recommended)",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-403", Family: FamilyPreflight, Category: catStorage,
		Summary:  "Free inodes",
		Message:  "Free inode count is low for a container image store",
		Severity: SeverityWarn,
	},
	{
		ID: "PF-404", Family: FamilyPreflight, Category: catStorage,
		Summary:  "Longhorn prerequisite: iscsid",
		Message:  "Storage driver longhorn requires iscsid, which is absent or inactive",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-405", Family: FamilyPreflight, Category: catStorage,
		Summary:  "Longhorn prerequisite: NFS client",
		Message:  "nfs-common/nfs-utils is absent; Longhorn RWX volumes will be unavailable",
		Severity: SeverityDegrade,
	},
	{
		ID: "PF-406", Family: FamilyPreflight, Category: catStorage,
		Summary: "multipathd does not claim Longhorn devices",
		Message: "multipathd is active and will claim Longhorn block devices, causing volume attach to fail; blacklist them first",
		// Well known and very confusing from the symptom alone.
		Severity: SeverityBlock,
	},
	{
		ID: "PF-407", Family: FamilyPreflight, Category: catStorage,
		Summary:  "Filesystem type",
		Message:  "Filesystem is neither ext4 nor xfs",
		Severity: SeverityWarn,
	},

	// --- PF-5xx · Time ----------------------------------------------------
	// Certificates break the instant clocks drift. Nothing in this block may be
	// relaxed to warn.
	{
		ID: "PF-501", Family: FamilyPreflight, Category: catTime,
		Summary:  "Clock is NTP-synchronised",
		Message:  "System clock is not synchronised to any time source",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-502", Family: FamilyPreflight, Category: catTime,
		Summary:  "Clock skew between nodes",
		Message:  "Clock skew between nodes exceeds 5 seconds",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-503", Family: FamilyPreflight, Category: catTime,
		Summary:  "Configured NTP servers are reachable",
		Message:  "None of the configured NTP servers is reachable; required in airgap where no public pool exists",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-504", Family: FamilyPreflight, Category: catTime,
		Summary:  "Timezones are consistent across nodes",
		Message:  "Nodes report differing timezones; correlating logs across them will be error-prone",
		Severity: SeverityWarn,
	},

	// --- PF-6xx · Network -------------------------------------------------
	{
		ID: "PF-601", Family: FamilyPreflight, Category: catNetwork,
		Summary:  "Inter-node port matrix",
		Message:  "A required inter-node port is unreachable (6443, 9345, 2379-2380, 10250, 4240, 8472/udp, 51871/udp)",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-602", Family: FamilyPreflight, Category: catNetwork,
		Summary:  "Path MTU between nodes",
		Message:  "Path MTU is smaller than expected; overlay MTU must be adjusted",
		Severity: SeverityDegrade,
	},
	{
		ID: "PF-603", Family: FamilyPreflight, Category: catNetwork,
		Summary:  "registrationAddress resolves",
		Message:  "registrationAddress does not resolve from this node",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-604", Family: FamilyPreflight, Category: catNetwork,
		Summary:  "Hostnames are unique and fully qualified",
		Message:  "Duplicate or unqualified hostname; nodes would collide on join",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-605", Family: FamilyPreflight, Category: catNetwork,
		Summary:  "Required sysctl values",
		Message:  "Required sysctl values are unset; apply will configure them",
		Severity: SeverityWarn,
	},
	{
		ID: "PF-606", Family: FamilyPreflight, Category: catNetwork,
		Summary:  "VIP address is unused",
		Message:  "The configured VIP already answers ARP; another host holds this address",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-607", Family: FamilyPreflight, Category: catNetwork,
		Summary:  "VIP interface can be determined automatically",
		Message:  "No interface matches the VIP subnet; specify topology.vip.interface explicitly",
		Severity: SeverityDegrade,
	},
	{
		ID: "PF-608", Family: FamilyPreflight, Category: catNetwork,
		Summary:  "Proxy passes CONNECT",
		Message:  "The configured proxy did not complete a CONNECT to the required endpoints",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-609", Family: FamilyPreflight, Category: catNetwork,
		Summary: "nodeIP is unambiguous on multi-homed nodes",
		Message: "Node has two or more NICs and no nodeIP is pinned; the advertised address may be the wrong one",
		// Silent failure: the cluster comes up and misroutes later.
		Severity: SeverityWarn,
	},
	{
		ID: "PF-610", Family: FamilyPreflight, Category: catNetwork,
		Summary:  "systemd-resolved stub resolver",
		Message:  "/etc/resolv.conf points at the 127.0.0.53 stub resolver",
		Severity: SeverityWarn,
	},
	{
		ID: "PF-611", Family: FamilyPreflight, Category: catNetwork,
		Summary:  "Pod and service CIDRs do not collide",
		Message:  "Pod or service CIDR overlaps a node subnet or an existing route",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-612", Family: FamilyPreflight, Category: catNetwork,
		Summary: "Gateway external address is pinned",
		Message: "A customer-facing profile leaves gateways[].address unset; the DNS record cannot be requested before install",
		// Formerly referenced as PF-908 from the schema with no definition
		// anywhere. Renumbered into the network block: PF-9xx is reserved for
		// certificate material, and this check is about addressing and DNS
		// lead time, not about a certificate.
		Severity: SeverityWarn,
	},

	// --- PF-7xx · Registry / PKI -----------------------------------------
	{
		ID: "PF-701", Family: FamilyPreflight, Category: catRegistryPKI,
		Summary:  "Registry is reachable",
		Message:  "systemDefaultRegistry did not answer on TCP or did not serve /v2/",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-702", Family: FamilyPreflight, Category: catRegistryPKI,
		Summary:  "Registry TLS verifies against the supplied CA",
		Message:  "Registry TLS certificate does not verify against the supplied CA",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-703", Family: FamilyPreflight, Category: catRegistryPKI,
		Summary:  "Registry credentials authenticate",
		Message:  "Registry login failed with the supplied credentials",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-704", Family: FamilyPreflight, Category: catRegistryPKI,
		Summary:  "Intermediate certificate is valid",
		Message:  "Intermediate certificate does not chain to the supplied root, or expires too soon",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-705", Family: FamilyPreflight, Category: catRegistryPKI,
		Summary:  "Certificate notBefore is not in the node's future",
		Message:  "Certificate notBefore is later than the node clock; cross-check PF-501",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-706", Family: FamilyPreflight, Category: catRegistryPKI,
		Summary:  "No root private key in the supplied PKI material",
		Message:  "A root private key was found in the supplied material; the offline root key must never leave its custody",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-707", Family: FamilyPreflight, Category: catRegistryPKI,
		Summary:  "Airgap bundle integrity",
		Message:  "Hauler bundle checksum or signature verification failed",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-709", Family: FamilyPreflight, Category: catRegistryPKI,
		Summary:  "RKE2 release artifacts on the node",
		Message:  "kubernetes.artifactPath does not hold the release artifacts for the version this document asks for",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-708", Family: FamilyPreflight, Category: catRegistryPKI,
		Summary:  "ACME prerequisites",
		Message:  "ACME DNS-01 token is invalid or the zone is not writable",
		Severity: SeverityBlock,
	},

	// --- PF-8xx · Residue / conflict --------------------------------------
	{
		ID: "PF-801", Family: FamilyPreflight, Category: catResidue,
		Summary:  "Pre-existing docker or containerd",
		Message:  "An existing docker or containerd installation was found",
		Severity: SeverityWarn,
	},
	{
		ID: "PF-802", Family: FamilyPreflight, Category: catResidue,
		Summary:  "Pre-existing k3s or RKE2 installation",
		Message:  "An existing k3s or RKE2 installation was found; clean it up explicitly before proceeding",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-803", Family: FamilyPreflight, Category: catResidue,
		Summary:  "Control-plane ports are free",
		Message:  "Port 6443 or 9345 is already bound by another process",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-804", Family: FamilyPreflight, Category: catResidue,
		Summary:  "No leftover CNI interfaces",
		Message:  "Leftover CNI interfaces are present (cni0, flannel.1, cilium_host)",
		Severity: SeverityWarn,
	},
	{
		ID: "PF-805", Family: FamilyPreflight, Category: catResidue,
		Summary:  "No leftover packet filter rules",
		Message:  "Leftover iptables or nftables rules from a previous installation are present",
		Severity: SeverityWarn,
	},

	{
		ID: "PF-806", Family: FamilyPreflight, Category: catResidue,
		Summary:  "An existing etcd member matches the address this document advertises",
		Message:  "A previous installation registered this node in etcd under a different address; rke2 will not start until the datastore is reset",
		Severity: SeverityBlock,
	},

	// --- PF-9xx · Certificate material (docs/20-cert.md §4) ---------------
	// Gates on the INPUT FILES. Wire behaviour is PV-xxx; passing this block
	// says nothing about what the server actually sends.
	{
		ID: "PF-901", Family: FamilyPreflight, Category: catCertificate,
		Summary:  "Private key matches the leaf certificate",
		Message:  "Private key does not match the leaf certificate's public key",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-902", Family: FamilyPreflight, Category: catCertificate,
		Summary:  "Certificate chain is complete",
		Message:  "Chain is incomplete; an issuer certificate is missing and cannot be fetched over AIA in an airgap",
		Severity: SeverityBlock,
		Reasons:  []string{"CHAIN_INCOMPLETE"},
	},
	{
		ID: "PF-903", Family: FamilyPreflight, Category: catCertificate,
		Summary: "Listener hostname is covered by the leaf SAN",
		Message: "No leaf certificate covers the listener hostname under RFC 6125 wildcard rules",
		// "We gave you a wildcard" and what the SAN actually covers routinely
		// differ: *.acme.co.kr covers api.acme.co.kr but neither acme.co.kr
		// nor a.b.acme.co.kr.
		Severity: SeverityBlock,
	},
	{
		ID: "PF-904", Family: FamilyPreflight, Category: catCertificate,
		Summary: "Certificate is not expired or expiring",
		Message: "Certificate has expired, or expires within expiryWarningDays",
		// Severity is the worst case. CERT_EXPIRED blocks; CERT_EXPIRING is a
		// warn, and its window is the bundle's own expiryWarningDays (45 by
		// default) -- not the report grading threshold, which is contract-level
		// and wider. See docs/30-maintenance.md §2.5.
		Severity: SeverityBlock,
		Reasons:  []string{"CERT_EXPIRED", "CERT_EXPIRING"},
	},
	{
		ID: "PF-905", Family: FamilyPreflight, Category: catCertificate,
		Summary:  "Key algorithm and size are supported by the GatewayClass",
		Message:  "Key algorithm or size falls outside what the selected GatewayClass supports",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-906", Family: FamilyPreflight, Category: catCertificate,
		Summary:  "Encrypted private key can be decrypted",
		Message:  "Private key is encrypted and the supplied passphrase did not decrypt it",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-910", Family: FamilyPreflight, Category: catCertificate,
		Summary:  "No root private key in the certificate bundle",
		Message:  "A root private key was found in the certificate bundle and the input is rejected",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-911", Family: FamilyPreflight, Category: catCertificate,
		Summary:  "Certificate notBefore is not in the node's future",
		Message:  "Certificate notBefore is later than the node clock; cross-check PF-501",
		Severity: SeverityBlock,
	},
	{
		ID: "PF-912", Family: FamilyPreflight, Category: catCertificate,
		Summary:  "No duplicate leaf claiming the same SAN",
		Message:  "Two or more leaf certificates claim the same SAN; the correct one cannot be chosen automatically",
		Severity: SeverityBlock,
	},
}
