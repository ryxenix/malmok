package codes

// UP · Upgrade preconditions.
//
// These are measured before a node is touched, which makes them preflight in
// character — but they are not about whether a cluster can be built, they are
// about whether the cluster that exists can move to a particular version. A
// preflight code answers "will this install work here"; an upgrade code answers
// "is this step legal from where we are". Filing them together would make every
// PF inventory a mix of the two, and an operator reading an audit report could
// not tell which question a finding answered.
//
// The rules they encode come from Kubernetes' own skew policy and from etcd:
// the control plane moves one minor at a time, a kubelet may lag its API server
// but must never lead it, and etcd has no downgrade.

const (
	catUPVersion  = "Version skew"
	catUPReadines = "Cluster readiness"
)

var upgradeCodes = []Code{
	// --- UP-0xx · Version skew ---------------------------------------------
	{
		ID: "UP-001", Family: FamilyUpgrade, Category: catUPVersion,
		Summary: "Target version is well formed",
		Message: "The target version is not an RKE2 version. It has the form v<major>.<minor>.<patch>+rke2r<n>",
		// Nothing about the cluster makes this better or worse; it is wrong
		// before anything is measured.
		Severity: SeverityBlock,
	},
	{
		ID: "UP-002", Family: FamilyUpgrade, Category: catUPVersion,
		Summary: "Target is newer than what runs",
		Message: "The target is not newer than the version already running. Kubernetes and etcd have no supported downgrade: " +
			"the API server writes storage the older one cannot read, and restoring a snapshot is the only way back",
		Severity: SeverityBlock,
	},
	{
		ID: "UP-003", Family: FamilyUpgrade, Category: catUPVersion,
		Summary: "One minor version at a time",
		Message: "The target skips a minor version. The control plane supports one minor step, and skipping one leaves " +
			"API objects stored in a version the new server never learned to convert",
		Severity: SeverityBlock,
	},
	{
		ID: "UP-004", Family: FamilyUpgrade, Category: catUPVersion,
		Summary:  "No node is ahead of the target",
		Message:  "A node already runs a version newer than the target, so the upgrade would move it backwards",
		Severity: SeverityBlock,
	},
	{
		ID: "UP-005", Family: FamilyUpgrade, Category: catUPVersion,
		Summary: "No agent leads its servers",
		Message: "An agent runs a newer version than the servers. A kubelet may lag its API server and must never lead it; " +
			"this cluster is already outside the supported skew and the servers have to be brought up first",
		Severity: SeverityBlock,
	},

	// --- UP-1xx · Cluster readiness ----------------------------------------
	{
		ID: "UP-101", Family: FamilyUpgrade, Category: catUPReadines,
		Summary: "Every node is Ready before starting",
		Message: "A node is not Ready. An upgrade restarts each node in turn, and starting one while another is already " +
			"down is how a cluster loses quorum during a maintenance window",
		Severity: SeverityBlock,
	},
	{
		ID: "UP-102", Family: FamilyUpgrade, Category: catUPReadines,
		Summary: "Workloads have somewhere to go",
		Message: "There is one node, so there is nowhere to drain to. Its workloads restart in place while it is upgraded",
		// Not a fault. A single-node cluster is a supported shape and this is
		// what upgrading one means; recording it keeps the restart from being
		// a surprise afterwards.
		Severity: SeverityInfo,
	},
	{
		ID: "UP-103", Family: FamilyUpgrade, Category: catUPReadines,
		Summary: "etcd has a recent snapshot",
		Message: "No etcd snapshot was taken recently. A failed control plane upgrade is recovered by restoring one, " +
			"and the time to find out there is none is not afterwards",
		Severity: SeverityWarn,
	},
}
