package codes

// Post-apply verification codes (PV-xxx). Collected from docs/20-cert.md §6.5.
//
// PF-9xx gates the INPUT FILES. PV gates the WIRE. The two are not
// interchangeable and passing the first says nothing about the second: a
// perfectly valid bundle on disk still fails here if the gateway serves only
// part of it.
//
// The reference incident behind this whole block: a customer certificate was
// concatenated into a fullchain and applied, the browser was happy, and a
// Spring-based internal system then failed every API call. Browsers fetch
// missing intermediates over AIA; JSSE does not, by default. So the acceptance
// bar is the strictest client, never the browser — see ADR-010.

const catWire = "TLS wire verification"

var verificationCodes = []Code{
	{
		ID: "PV-001", Family: FamilyVerification, Category: catWire,
		Summary:  "Served chain is captured",
		Message:  "Could not capture the certificate chain the server actually sends",
		Severity: SeverityBlock,
	},
	{
		ID: "PV-002", Family: FamilyVerification, Category: catWire,
		Summary: "Chain verifies without AIA fetching",
		Message: "Served chain does not reach a trust anchor on its own; browsers pass by fetching the missing issuer over AIA, JSSE, Go and curl fail",
		// The decisive check of the block. Pass it and Java, Go, curl and
		// Python all pass. Fail it and only the browser passes, which is how
		// the defect reaches production unnoticed.
		Severity: SeverityBlock,
		// Two failures wear the same x509 error and have opposite fixes. A
		// chain missing an intermediate is fixed by adding the intermediate; a
		// chain complete in itself that ends at an untrusted root is fixed by
		// distributing the root. Sending an operator to look for something they
		// already have is worse than saying nothing.
		Reasons: []string{"CHAIN_NOT_SELF_SUFFICIENT", "ROOT_NOT_TRUSTED"},
	},
	{
		ID: "PV-003", Family: FamilyVerification, Category: catWire,
		Summary:  "No expired certificate in the served chain",
		Message:  "Served chain contains an expired certificate, possibly a leftover cross-signed intermediate",
		Severity: SeverityBlock,
	},
	{
		ID: "PV-004", Family: FamilyVerification, Category: catWire,
		Summary: "Revocation distribution points are reachable",
		Message: "OCSP or CRL distribution points are unreachable; expected in an airgap, but it must be stated in the audit report and the handover",
		// Not a defect in an airgap. It is a defect to hand over without
		// saying so, because the customer's Java clients may have revocation
		// checking on and will hard-fail.
		Severity: SeverityWarn,
	},
	{
		ID: "PV-005", Family: FamilyVerification, Category: catWire,
		Summary:  "Certificate returned when SNI is absent",
		Message:  "The default certificate returned for connections without SNI is unexpected",
		Severity: SeverityWarn,
	},
	{
		ID: "PV-006", Family: FamilyVerification, Category: catWire,
		Summary:  "Behaviour on direct IP access",
		Message:  "Response to direct IP access recorded for the audit report",
		Severity: SeverityInfo,
	},
	{
		ID: "PV-007", Family: FamilyVerification, Category: catWire,
		Summary:  "Each listener hostname returns its own certificate",
		Message:  "A listener hostname returns a certificate that does not match it",
		Severity: SeverityBlock,
	},
	{
		ID: "PV-008", Family: FamilyVerification, Category: catWire,
		Summary:  "Negotiable TLS versions and cipher suites",
		Message:  "Supported TLS versions or cipher suites may exclude older JDK clients still in use at the site",
		Severity: SeverityWarn,
	},
}
