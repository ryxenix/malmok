// Package v1alpha1 — gateway layer (Gateway API).
//
// This file models the Gateway API, NOT the deprecated Kubernetes Ingress
// resource. Ingress is out of scope entirely: rke2-ingress-nginx reached EOL in
// March 2026 and the engine disables it unconditionally (ADR-005).
//
// SCOPE BOUNDARY
//
//	This file describes everything up to and including the Gateway. HTTPRoute is
//	deliberately NOT modelled here: routes belong to application Helm charts.
//	If cluster.yaml had to change every time an application was added, the
//	infrastructure tool would become an application deployment tool — which is
//	exactly the coupling that killed the previous generation of this project.
//
//	The platform instead publishes a CONTRACT that application charts consume:
//	  - Gateway name / namespace  (HTTPRoute parentRef)
//	  - Domain suffix             (HTTPRoute hostnames)
//	  - Cross-namespace permission (allowedRoutes / ReferenceGrant)
//	These are surfaced to charts via a ConfigMap written by the engine.
package v1alpha1

// ---------------------------------------------------------------------------
// Gateway root
// ---------------------------------------------------------------------------

type GatewaySpec struct {
	// GatewayClass is derived from dataplane.preset unless overridden:
	//   cilium-gw       -> "cilium"
	//   *-traefik       -> "traefik"
	GatewayClass string `yaml:"gatewayClass,omitempty" json:"gatewayClass,omitempty"`

	// DomainSuffix is the base domain published to application charts.
	// Example: "acme.internal" => app hostnames become "<app>.acme.internal".
	DomainSuffix string `yaml:"domainSuffix" json:"domainSuffix"`

	Gateways []Gateway `yaml:"gateways" json:"gateways"`

	DNS DNSSpec `yaml:"dns,omitempty" json:"dns,omitempty"`

	// ContractConfigMap: name of the ConfigMap the engine writes so that
	// application charts can discover gateway name/namespace/domain without
	// hardcoding them. Default: "platform-gateway-contract" in every namespace
	// listed in RouteNamespaces.
	ContractConfigMap string `yaml:"contractConfigMap,omitempty" json:"contractConfigMap,omitempty"`
}

// ---------------------------------------------------------------------------
// Gateway
// ---------------------------------------------------------------------------

// Gateway describes one Gateway API Gateway resource. Named without a `Spec`
// suffix because it is a list element of GatewaySpec.Gateways, not the top-level
// specification itself.
type Gateway struct {
	Name      string `yaml:"name"                json:"name"`
	Namespace string `yaml:"namespace,omitempty" json:"namespace,omitempty"` // default gateway-system

	// Address pins the external IP. Leaving this empty lets LB-IPAM allocate
	// from loadBalancerPool, which is fine in a homelab but unacceptable at a
	// customer site: the DNS record must be requested BEFORE install, so the IP
	// has to be decided up front. PF-612 warns when a customer-facing profile
	// omits this.
	Address string `yaml:"address,omitempty" json:"address,omitempty"`

	// Exposure is how the gateway gets an address the outside can reach.
	//
	//   "" / "loadBalancer" -> LB-IPAM hands it one from loadBalancerPool
	//   "node-ips"          -> the nodes' own addresses, bound directly
	//
	// node-ips was implemented through the generated Service's externalIPs
	// first, and that does not hold: Cilium owns the Service completely and
	// strips a patched externalIPs on its next reconcile. What works is a
	// host-networked gateway -- Envoy binds the listener port in the node's
	// own network namespace -- so the observable is not the Programmed
	// condition (Cilium never gives such a gateway a pool address) but
	// whether every named node answers on the port.
	//
	// node-ips exists for the same site acceptNodeRegistration does: a pool
	// address is one more IP answering on the segment, and IDC and air-gapped
	// network policy frequently allows only the addresses the nodes already
	// hold. Traffic to <node>:<port> reaches the gateway on any listed node;
	// the DNS record points at one or several of them. The cost is the same
	// shape as ADR-008's: a node that leaves takes its endpoint with it.
	Exposure GatewayExposure `yaml:"exposure,omitempty" json:"exposure,omitempty"`

	// NodeIPs lists which nodes answer, by the address the document names them
	// by. Empty with exposure node-ips means every node in the topology.
	NodeIPs []string `yaml:"nodeIPs,omitempty" json:"nodeIPs,omitempty"`

	Listeners []ListenerSpec `yaml:"listeners" json:"listeners"`

	// RouteNamespaces controls which namespaces may attach HTTPRoutes.
	//   "same"     -> gateway namespace only
	//   "all"      -> any namespace (convenient, weak isolation)
	//   "selector" -> label selector (recommended for multi-tenant sites)
	RouteNamespaces   string            `yaml:"routeNamespaces,omitempty"  json:"routeNamespaces,omitempty"`
	NamespaceSelector map[string]string `yaml:"namespaceSelector,omitempty" json:"namespaceSelector,omitempty"`

	// Zone tags a gateway for split-horizon deployments, e.g. one gateway on
	// the DMZ segment and one on the internal segment, each with different
	// certificates and different DNS views.
	Zone string `yaml:"zone,omitempty" json:"zone,omitempty"`
}

// GatewayExposure names how a gateway is reached from outside the cluster.
type GatewayExposure string

const (
	ExposureLoadBalancer GatewayExposure = "loadBalancer"
	ExposureNodeIPs      GatewayExposure = "node-ips"
)

// ---------------------------------------------------------------------------
// Listener
// ---------------------------------------------------------------------------

type ListenerProtocol string

const (
	ListenerHTTP  ListenerProtocol = "HTTP"
	ListenerHTTPS ListenerProtocol = "HTTPS"
	// ListenerTLSPassthrough is spelled out rather than "ListenerTLS" because
	// that identifier is taken by the ListenerTLS config struct below. The wire
	// value stays "TLS", so cluster.yaml is unaffected.
	ListenerTLSPassthrough ListenerProtocol = "TLS" // passthrough
)

type ListenerSpec struct {
	Name     string           `yaml:"name"     json:"name"`
	Protocol ListenerProtocol `yaml:"protocol" json:"protocol"`
	Port     int              `yaml:"port"     json:"port"`

	// Hostname may be exact ("api.acme.internal") or wildcard
	// ("*.acme.internal"). This value is cross-checked against the certificate
	// SAN list by PF-903 — the single most common BYO-certificate failure is a
	// hostname that the customer's certificate does not actually cover.
	Hostname string `yaml:"hostname,omitempty" json:"hostname,omitempty"`

	TLS *ListenerTLS `yaml:"tls,omitempty" json:"tls,omitempty"`

	// RedirectToHTTPS on an HTTP listener generates the redirect route.
	RedirectToHTTPS *bool `yaml:"redirectToHTTPS,omitempty" json:"redirectToHTTPS,omitempty"`
}

// TLSSource is per-listener, NOT per-cluster. A DMZ deployment routinely needs
// a public ACME certificate on the external listener and a private-CA
// certificate on the internal one; a single cluster-wide pki.mode cannot
// express that.
//
// When omitted, the listener inherits pki.mode from the cluster-level PKISpec.
type TLSSource string

const (
	TLSFromPKI       TLSSource = ""            // inherit cluster pki.mode
	TLSFromACME      TLSSource = "acme"        // cert-manager issues
	TLSFromPrivateCA TLSSource = "private-ca"  // cert-manager, internal issuer
	TLSFromBYO       TLSSource = "byo"         // customer supplied material
	TLSFromSecret    TLSSource = "secret"      // pre-existing k8s Secret
	TLSPassthrough   TLSSource = "passthrough" // terminate at backend
)

type ListenerTLS struct {
	Source TLSSource `yaml:"source,omitempty" json:"source,omitempty"`

	// SecretRef is used when Source == secret, or as the output secret name for
	// other sources (so application charts can reference it deterministically).
	SecretRef string `yaml:"secretRef,omitempty" json:"secretRef,omitempty"`

	BYO *BYOMaterial `yaml:"byo,omitempty" json:"byo,omitempty"`

	// MinVersion / CipherSuites: public institutions occasionally mandate these.
	MinVersion   string   `yaml:"minVersion,omitempty"   json:"minVersion,omitempty"` // TLS1.2 | TLS1.3
	CipherSuites []string `yaml:"cipherSuites,omitempty" json:"cipherSuites,omitempty"`

	ClientAuth *ClientAuthSpec `yaml:"clientAuth,omitempty" json:"clientAuth,omitempty"`
}

// BYOMaterial models what customers actually hand over, not what would be
// convenient. Every field here exists because of a real failure mode.
//
// v1 SCOPE: PEM only. Commercial certificates arrive as PEM in practice, so
// PKCS#12 ingest is deferred. The Format/PFX fields are reserved so that adding
// it later is not a schema break.
type BYOMaterial struct {
	// Dir is the preferred input. Point it at a directory and the engine
	// classifies every file it finds, matches the key to the leaf by public key,
	// assembles the chain by issuer/subject graph walk, and builds the Secret.
	//
	// File NAMES are never trusted. Naming differs per CA (STAR_acme_co_kr.crt,
	// DigiCertCA.crt, ca-bundle.crt) and customers rename files in transit.
	// A single file may also contain multiple PEM blocks, and Windows-origin
	// .cer/.crt files are sometimes DER rather than PEM.
	//
	// Explicit Cert/Key/Chain below override auto-detection when set.
	Dir SourceRef `yaml:"dir,omitempty" json:"dir,omitempty"`

	// Format: "pem" (default). "pkcs12" is RESERVED — rejected by the engine in
	// v1 with a clear message rather than silently ignored.
	Format string `yaml:"format,omitempty" json:"format,omitempty"`

	// Explicit PEM inputs. Optional when Dir is set.
	//
	// What arrives as "the cert file" varies and cannot be assumed:
	//   (a) leaf only, chain in a separate file
	//   (b) fullchain (leaf + intermediates concatenated) in one file
	//   (c) leaf only, chain never delivered at all
	// The engine accepts Cert as either (a) or (b) and treats Chain as additive.
	// Case (c) is caught by PF-902, not at runtime by a confused Java client.
	Cert  SourceRef `yaml:"cert,omitempty"  json:"cert,omitempty"` // leaf or fullchain
	Key   SourceRef `yaml:"key,omitempty"   json:"key,omitempty"`
	Chain SourceRef `yaml:"chain,omitempty" json:"chain,omitempty"` // intermediates, optional

	// PFX is RESERVED for a future release. See Format.
	PFX SourceRef `yaml:"pfx,omitempty" json:"pfx,omitempty"`

	// IncludeRoot controls whether a self-signed root found in the input is
	// served in the TLS chain. Default FALSE, which is correct for both cases:
	//   public CA   -> clients already trust the root; sending it wastes bytes
	//                  and some validators complain
	//   private CA  -> the root belongs in the trust bundle
	//                  (pki.trustDistribution), not in the served chain
	IncludeRoot *bool `yaml:"includeRoot,omitempty" json:"includeRoot,omitempty"`

	// Passphrase for an encrypted private key. Common when the customer's CSR
	// was generated by their own security team.
	Passphrase SourceRef `yaml:"passphrase,omitempty" json:"passphrase,omitempty"`

	// AutoOrderChain reorders a mis-ordered PEM bundle instead of failing.
	// Customers frequently send leaf/intermediate in the wrong order; some
	// clients tolerate it and some do not, which produces a maddening
	// "works in the browser, fails in Java" report. Default: true.
	AutoOrderChain *bool `yaml:"autoOrderChain,omitempty" json:"autoOrderChain,omitempty"`

	// ExpiryWarningDays is roles (1) and (2) of docs/30-maintenance.md §2.5: the
	// PF-904 warn window when the bundle is applied, and the first alert step
	// for this bundle once it is running. One bundle needs one "start telling me
	// this many days out" value; there is no reason for those two to fire on
	// different dates.
	//
	// It is NOT the report grading threshold. That one is contract-level
	// (maintenance.thresholds, §4.3) and deliberately wider, because a quarterly
	// report has to flag an expiry that falls before the next issue.
	//
	// Per bundle because reissue lead time is a property of the customer's
	// purchasing process rather than of the cluster: two weeks at one site, two
	// months at another, and 45 days is already too late for the latter.
	//
	// BYO certificates cannot be auto-renewed, so an unmonitored one WILL take
	// the service down at expiry. Default: 45, matching the series A first alert
	// step in §2.4.
	ExpiryWarningDays int `yaml:"expiryWarningDays,omitempty" json:"expiryWarningDays,omitempty"`
}

// ClientAuthSpec enables mTLS. Occasionally mandated for public-sector
// deployments; modelled here so it does not require a schema break later.
type ClientAuthSpec struct {
	Mode   string    `yaml:"mode,omitempty"   json:"mode,omitempty"` // required | optional
	CACert SourceRef `yaml:"caCert,omitempty" json:"caCert,omitempty"`
}

// ---------------------------------------------------------------------------
// DNS
// ---------------------------------------------------------------------------

// DNSSpec is deliberately minimal. Record registration is the customer's
// operation, requested out of band — the engine does not manage zones, does not
// write /etc/hosts, and has no DNS provider integrations.
//
// Two things are kept because they cost almost nothing and both are byproducts
// of data the spec already contains:
//
//	EmitRecordSheet     the FQDN -> IP list that has to be sent to the customer
//	                    anyway. Generated at `plan` time so the request can go
//	                    out before install starts, running the customer's lead
//	                    time in parallel with the work.
//	VerifyResolution    resolves each FQDN at the end of install. Not to fix
//	                    anything — to establish whether an unreachable service is
//	                    a cluster problem or an unregistered record. Without it,
//	                    that question costs hours of finger-pointing later.
//	                    Default severity is WARN, not block: an install is
//	                    legitimately complete before the customer registers.
type DNSSpec struct {
	EmitRecordSheet  *bool `yaml:"emitRecordSheet,omitempty"  json:"emitRecordSheet,omitempty"`
	VerifyResolution *bool `yaml:"verifyResolution,omitempty" json:"verifyResolution,omitempty"`

	// CorefileRewrite is NOT DNS management. It solves an in-cluster problem:
	// when a pod calls a service by its public FQDN, resolution returns the
	// gateway's external IP and the traffic has to hairpin back in, which fails
	// or degrades on many L2/LB setups. The rewrite points in-cluster lookups at
	// the gateway Service instead. Independent of whatever the customer's DNS
	// does for external clients.
	CorefileRewrite *bool `yaml:"corefileRewrite,omitempty" json:"corefileRewrite,omitempty"`

	// ExtraRecords are appended to the record sheet only (registry, api, git).
	ExtraRecords []DNSRecord `yaml:"extraRecords,omitempty" json:"extraRecords,omitempty"`
}

type DNSRecord struct {
	FQDN  string `yaml:"fqdn"           json:"fqdn"`
	Type  string `yaml:"type,omitempty" json:"type,omitempty"` // A | CNAME
	Value string `yaml:"value"          json:"value"`
	Zone  string `yaml:"zone,omitempty" json:"zone,omitempty"` // internal | dmz | public
}
