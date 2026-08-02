package codes

// Downgrade reason codes (DG-xxx). Collected from docs/10-preflight-plan.md.
//
// A downgrade means the cluster is not what the customer asked for. Whether
// that is acceptable is DowngradePolicy's decision, not this registry's, so DG
// codes carry no severity.
//
// Every downgrade, including an automatic one, must reach the audit report with
// four facts: requested configuration, actual configuration, the probe that
// triggered it, and the nodes affected. Six months later somebody asks why the
// cluster runs Traefik when the contract said Cilium, and that question has to
// be answerable from the report alone.

const catDowngrade = "Downgrade reasons"

var downgradeCodes = []Code{
	{
		ID: "DG-001", Family: FamilyDowngrade, Category: catDowngrade,
		Summary: "CNI downgraded because eBPF is unusable",
		Message: "Requested Cilium dataplane replaced by the fallback preset; record the triggering probe IDs and the affected nodes",
	},
	{
		ID: "DG-002", Family: FamilyDowngrade, Category: catDowngrade,
		Summary: "Gateway implementation downgraded",
		Message: "Cilium Gateway replaced by Traefik; record the triggering probe IDs and the affected nodes",
	},
	{
		ID: "DG-003", Family: FamilyDowngrade, Category: catDowngrade,
		Summary: "Requested configuration kept by excluding nodes",
		Message: "The requested configuration is retained by excluding the nodes that cannot support it; record which nodes and why",
	},
	{
		ID: "DG-010", Family: FamilyDowngrade, Category: catDowngrade,
		Summary: "No load balancer IP source available",
		Message: "No LB-IPAM pool and no external load balancer; the Gateway will not obtain an external address without manual action",
	},
	{
		ID: "DG-020", Family: FamilyDowngrade, Category: catDowngrade,
		Summary: "Storage driver downgraded",
		Message: "Requested storage driver replaced by local-path; replicated volumes are lost and the customer must be told",
	},
}
