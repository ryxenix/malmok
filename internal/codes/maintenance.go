package codes

// Maintenance check codes (MC-xxx). Collected from docs/30-maintenance.md §3.
//
// Inspection is READ-ONLY. A check that changes the cluster is a defect.
//
// MC codes carry no severity by design. "normal / attention / action required"
// is decided at runtime against the thresholds in maintenance.thresholds, and
// those are negotiated per contract (docs/30-maintenance.md §4.3). Freezing a
// verdict into the definition would hardcode one site's agreement into every
// other site's report — and a verdict the customer never agreed to is exactly
// the thing that gets argued about instead of fixed.
//
// Item IDs survive translation: the maintenance report is written in Korean for
// the customer, but MC-402 stays MC-402 so the raw evidence remains traceable.

const (
	catMCCert     = "Certificates"
	catMCCluster  = "Cluster state"
	catMCEtcd     = "etcd"
	catMCResource = "Resources / storage"
	catMCNetwork  = "Network / access"
	catMCWorkload = "Workload / service"
	catMCSecurity = "Security / configuration"
)

var maintenanceCodes = []Code{
	// --- MC-1xx · Certificates -------------------------------------------
	// Series A service domain, B RKE2 internal leaf, C RKE2 CA, D private
	// intermediate. Four issuers, four renewal procedures, four disruption
	// profiles — see docs/30-maintenance.md §1.
	{
		ID: "MC-101", Family: FamilyMaintenance, Category: catMCCert,
		Summary: "Series A: days remaining, per listener",
		Message: "Service domain certificate is approaching expiry",
	},
	{
		ID: "MC-102", Family: FamilyMaintenance, Category: catMCCert,
		Summary: "Series A: served chain still complete",
		Message: "Served chain no longer verifies without AIA fetching; re-run of PV-002",
	},
	{
		ID: "MC-111", Family: FamilyMaintenance, Category: catMCCert,
		Summary: "Series B: days remaining, per node and service",
		Message: "RKE2 internal certificate is approaching expiry on one or more nodes",
	},
	{
		ID: "MC-112", Family: FamilyMaintenance, Category: catMCCert,
		Summary: "Series B: renewal window entered",
		Message: "RKE2 internal certificates are inside the 120-day renewal window; a restart will now rotate them",
	},
	{
		ID: "MC-113", Family: FamilyMaintenance, Category: catMCCert,
		Summary: "Series B: on-disk certificate matches the rke2-serving Secret",
		Message: "On-disk certificate and the rke2-serving Secret disagree; a node restart can fail in this state",
		// Reported in the wild: the disk copy rotates while the datastore Secret
		// does not, and the failure only shows up at the next restart. Both
		// sides must be read; checking one is checking neither.
	},
	{
		ID: "MC-121", Family: FamilyMaintenance, Category: catMCCert,
		Summary: "Series C: days remaining on the RKE2 CA",
		Message: "RKE2 CA expiry recorded; ten years usually outlives the contract, so it must appear in every report and in the handover",
	},
	{
		ID: "MC-131", Family: FamilyMaintenance, Category: catMCCert,
		Summary: "Series D: days remaining on the private intermediate",
		Message: "Private CA intermediate is approaching expiry",
	},

	// --- MC-2xx · Cluster state -------------------------------------------
	{
		ID: "MC-201", Family: FamilyMaintenance, Category: catMCCluster,
		Summary: "Node readiness and last restart",
		Message: "A node is NotReady, or restarted unexpectedly during the period",
	},
	{
		ID: "MC-202", Family: FamilyMaintenance, Category: catMCCluster,
		Summary: "System pod health",
		Message: "System pods are in CrashLoopBackOff or Pending",
	},
	{
		ID: "MC-203", Family: FamilyMaintenance, Category: catMCCluster,
		Summary: "Top pod restart counts for the period",
		Message: "Pod restart counts increased notably during the period",
	},
	{
		ID: "MC-204", Family: FamilyMaintenance, Category: catMCCluster,
		Summary: "Node conditions",
		Message: "A node reports DiskPressure, MemoryPressure or PIDPressure",
	},
	{
		ID: "MC-205", Family: FamilyMaintenance, Category: catMCCluster,
		Summary: "Workload replicas: desired versus actual",
		Message: "Actual replica count differs from the desired count",
	},
	{
		ID: "MC-206", Family: FamilyMaintenance, Category: catMCCluster,
		Summary: "Kubernetes and RKE2 version, EOL horizon",
		Message: "The running version is approaching or past end of life",
	},

	// --- MC-3xx · etcd -----------------------------------------------------
	{
		ID: "MC-301", Family: FamilyMaintenance, Category: catMCEtcd,
		Summary: "Snapshot freshness",
		Message: "The most recent successful etcd snapshot is older than the agreed maximum age",
	},
	{
		ID: "MC-302", Family: FamilyMaintenance, Category: catMCEtcd,
		Summary: "Snapshot retention and off-cluster copy",
		Message: "Snapshot retention is below target, or no copy exists outside the cluster",
	},
	{
		ID: "MC-303", Family: FamilyMaintenance, Category: catMCEtcd,
		Summary: "Database size against quota",
		Message: "etcd database size is approaching its quota",
	},
	{
		ID: "MC-304", Family: FamilyMaintenance, Category: catMCEtcd,
		Summary: "Defragmentation needed",
		Message: "etcd fragmentation is high enough to warrant a defragmentation",
	},
	{
		ID: "MC-305", Family: FamilyMaintenance, Category: catMCEtcd,
		Summary: "Member health and leader changes",
		Message: "An etcd member is unhealthy, or leader elections are unexpectedly frequent",
	},
	{
		ID: "MC-306", Family: FamilyMaintenance, Category: catMCEtcd,
		Summary: "Date of the last restore rehearsal",
		Message: "No restore rehearsal within the agreed interval; an untested backup is not a backup",
	},

	// --- MC-4xx · Resources / storage --------------------------------------
	{
		ID: "MC-401", Family: FamilyMaintenance, Category: catMCResource,
		Summary: "CPU and memory usage per node, mean and peak",
		Message: "Node CPU or memory usage exceeded the agreed threshold during the period",
	},
	{
		ID: "MC-402", Family: FamilyMaintenance, Category: catMCResource,
		Summary: "/var/lib/rancher usage",
		Message: "/var/lib/rancher usage exceeded its threshold; this is the most frequent on-prem outage cause and carries its own limit",
	},
	{
		ID: "MC-403", Family: FamilyMaintenance, Category: catMCResource,
		Summary: "Free disk and inodes",
		Message: "Free disk space or inode count is below the agreed threshold",
	},
	{
		ID: "MC-404", Family: FamilyMaintenance, Category: catMCResource,
		Summary: "PersistentVolume usage",
		Message: "One or more PersistentVolumes exceed their usage threshold",
	},
	{
		ID: "MC-405", Family: FamilyMaintenance, Category: catMCResource,
		Summary: "Longhorn volume and replica health",
		Message: "A Longhorn volume is degraded or has fewer healthy replicas than configured",
	},
	{
		ID: "MC-406", Family: FamilyMaintenance, Category: catMCResource,
		Summary: "Longhorn backup freshness",
		Message: "The most recent Longhorn backup is older than the agreed maximum age",
	},
	{
		ID: "MC-407", Family: FamilyMaintenance, Category: catMCResource,
		Summary: "Image cache growth and garbage collection",
		Message: "Container image cache keeps growing; garbage collection may not be running",
	},

	// --- MC-5xx · Network / access -----------------------------------------
	{
		ID: "MC-501", Family: FamilyMaintenance, Category: catMCNetwork,
		Summary: "Gateway and HTTPRoute acceptance",
		Message: "A Gateway or HTTPRoute is not in the Accepted state",
	},
	{
		ID: "MC-502", Family: FamilyMaintenance, Category: catMCNetwork,
		Summary: "Load balancer IP reachability",
		Message: "A load balancer address is unreachable",
	},
	{
		ID: "MC-503", Family: FamilyMaintenance, Category: catMCNetwork,
		Summary: "Service domain DNS resolution",
		Message: "A service domain no longer resolves",
	},
	{
		ID: "MC-504", Family: FamilyMaintenance, Category: catMCNetwork,
		Summary: "End-to-end HTTPS response per service",
		Message: "A service did not return a healthy HTTPS response end to end",
	},
	{
		ID: "MC-505", Family: FamilyMaintenance, Category: catMCNetwork,
		Summary: "Inter-node port matrix versus build time",
		Message: "The inter-node port matrix changed since the cluster was built; a firewall policy may have been altered",
	},
	{
		ID: "MC-506", Family: FamilyMaintenance, Category: catMCNetwork,
		Summary: "Registry reachability and authentication",
		Message: "The registry is unreachable, or the stored credentials no longer authenticate",
	},

	// --- MC-6xx · Workload / service ---------------------------------------
	{
		ID: "MC-601", Family: FamilyMaintenance, Category: catMCWorkload,
		Summary: "Per-service health endpoint",
		Message: "A service health endpoint reported unhealthy",
	},
	{
		ID: "MC-602", Family: FamilyMaintenance, Category: catMCWorkload,
		Summary: "Response time P50 and P95 for the period",
		Message: "P95 response time exceeded the agreed threshold",
	},
	{
		ID: "MC-603", Family: FamilyMaintenance, Category: catMCWorkload,
		Summary: "5xx rate for the period",
		Message: "The 5xx rate exceeded the agreed threshold",
	},
	{
		ID: "MC-604", Family: FamilyMaintenance, Category: catMCWorkload,
		Summary: "Alerts raised during the period",
		Message: "Alerts were raised during the period and are summarised in the report",
	},
	{
		ID: "MC-605", Family: FamilyMaintenance, Category: catMCWorkload,
		Summary: "Deployment and change history for the period",
		Message: "Deployments and changes applied during the period, by GitOps revision and Helm release",
	},

	// --- MC-7xx · Security / configuration ---------------------------------
	{
		ID: "MC-701", Family: FamilyMaintenance, Category: catMCSecurity,
		Summary: "CIS scan delta since build",
		Message: "CIS scan results regressed relative to the build-time baseline",
	},
	{
		ID: "MC-702", Family: FamilyMaintenance, Category: catMCSecurity,
		Summary: "Secrets and tokens nearing expiry",
		Message: "A Secret or token is approaching expiry",
	},
	{
		ID: "MC-703", Family: FamilyMaintenance, Category: catMCSecurity,
		Summary: "Image vulnerability scan",
		Message: "Image vulnerability findings; in an airgap the scanner database must be refreshed for this to mean anything",
	},
	{
		ID: "MC-704", Family: FamilyMaintenance, Category: catMCSecurity,
		Summary: "Drift from the cluster.yaml used at build time",
		Message: "The live cluster has drifted from the cluster.yaml it was built from",
	},
	{
		ID: "MC-705", Family: FamilyMaintenance, Category: catMCSecurity,
		Summary: "RBAC change history",
		Message: "RBAC bindings changed during the period",
	},
}
