// Package v1alpha1 defines the declarative specification consumed by malmok.
//
// ARCHITECTURAL CONTRACT
//
//	The engine MUST be able to execute using this document alone.
//
//	The TUI does drive installs: it collects input, generates this document,
//	calls the engine, and renders the engine's event stream — progress, logs,
//	resume after a failure. What it must never do is OWN installation logic.
//	The distinction is ownership, not who presses the button; an OS installer
//	draws the screen while a lower layer does the partitioning.
//
//	So "requires a terminal" is the defect, not "runs from a terminal": every
//	install must be reachable headless, through the same code path, from this
//	document alone. That is what the CI matrix exercises.
//
//	internal/engine    -> MUST NOT import internal/tui  (enforced in CI)
//	internal/tui       -> imports engine (one-way)
//
// Field philosophy:
//   - `Profile` supplies a validated baseline (Tier-1 combination).
//   - Every other field is an *override*. Zero value == "inherit from profile".
//     This keeps the on-site editable surface to ~5 fields.
//   - Pointer types are used where "unset" must be distinguishable from "false".
package v1alpha1

// ---------------------------------------------------------------------------
// Root
// ---------------------------------------------------------------------------

const (
	// Group is the schema's namespace. It is a domain this project controls:
	// a reverse-DNS group borrowed from a domain nobody has registered is a
	// namespace somebody else can take.
	Group = "malmok.dev"

	APIVersion = Group + "/v1alpha1"
	KindSpec   = "ClusterSpec"
)

type ClusterSpec struct {
	APIVersion string   `yaml:"apiVersion" json:"apiVersion"`
	Kind       string   `yaml:"kind"       json:"kind"`
	Metadata   Metadata `yaml:"metadata"   json:"metadata"`

	Network    NetworkSpec    `yaml:"network"    json:"network"`
	Topology   TopologySpec   `yaml:"topology"   json:"topology"`
	OS         OSSpec         `yaml:"os"         json:"os"`
	Kubernetes KubernetesSpec `yaml:"kubernetes" json:"kubernetes"`
	PKI        PKISpec        `yaml:"pki"        json:"pki"`
	Registry   RegistrySpec   `yaml:"registry"   json:"registry"`
	Storage    StorageSpec    `yaml:"storage"    json:"storage"`
	// Gateway is the Gateway API layer (GatewayClass / Gateway / listener / TLS
	// / DNS), defined in gateway.go. HTTPRoute is deliberately absent — routes
	// are the application chart's responsibility (ADR-006).
	Gateway  GatewaySpec  `yaml:"gateway"  json:"gateway"`
	Platform PlatformSpec `yaml:"platform" json:"platform"`
	Output   OutputSpec   `yaml:"output"   json:"output"`
}

type Metadata struct {
	Name string `yaml:"name" json:"name"`
	// Profile selects a Tier-1 validated baseline. Empty => "custom" (Tier-3),
	// which forces an explicit acknowledgement in plan approval.
	Profile ProfileName `yaml:"profile,omitempty" json:"profile,omitempty"`
	// Annotations are opaque; used to carry customer/site identifiers into the
	// audit report without polluting the schema.
	Annotations map[string]string `yaml:"annotations,omitempty" json:"annotations,omitempty"`
}

type ProfileName string

// Tier-1 profiles. Each is exercised on every release by the CI matrix.
// Changing this list without adding a CI lane is a release blocker.
const (
	ProfileHomelab            ProfileName = "homelab"
	ProfileCompanyProd        ProfileName = "company-prod"
	ProfileOnpremDMZ          ProfileName = "onprem-dmz"
	ProfileAirgapUbuntu       ProfileName = "airgap-ubuntu"
	ProfileAirgapRocky        ProfileName = "airgap-rocky"
	ProfileAirgapConservative ProfileName = "airgap-conservative"
)

// ProfileCustom is Tier-3: unvalidated, never exercised by CI, and accepted
// only after an explicit acknowledgement during plan approval. It is kept out
// of the block above so that "every constant here has a CI lane" stays true —
// the release gate reads that list.
const ProfileCustom ProfileName = "custom"

// ---------------------------------------------------------------------------
// Network
// ---------------------------------------------------------------------------

type NetworkMode string

const (
	NetworkOnline NetworkMode = "online" // direct egress
	NetworkProxy  NetworkMode = "proxy"  // HTTP(S) proxy / allowlist (DMZ)
	NetworkAirgap NetworkMode = "airgap" // physically disconnected
)

type NetworkSpec struct {
	Mode  NetworkMode `yaml:"mode" json:"mode"`
	Proxy *ProxySpec  `yaml:"proxy,omitempty" json:"proxy,omitempty"`

	PodCIDR string `yaml:"podCIDR,omitempty" json:"podCIDR,omitempty"` // default 10.42.0.0/16
	SvcCIDR string `yaml:"svcCIDR,omitempty" json:"svcCIDR,omitempty"` // default 10.43.0.0/16

	// EncryptNodeTraffic enables Cilium WireGuard. Recommended (and defaulted to
	// true) whenever nodes straddle a network boundary, because customer security
	// teams routinely object to cleartext inter-node traffic crossing a DMZ.
	// Ignored when dataplane preset does not include Cilium.
	EncryptNodeTraffic *bool `yaml:"encryptNodeTraffic,omitempty" json:"encryptNodeTraffic,omitempty"`

	// Routing: "overlay" (VXLAN/geneve) or "native".
	// Native routing requires the operator to own the L3 fabric; it is NOT safe
	// to default to native when nodes span segments. Default: overlay.
	Routing RoutingMode `yaml:"routing,omitempty" json:"routing,omitempty"`
}

type RoutingMode string

const (
	RoutingOverlay RoutingMode = "overlay"
	RoutingNative  RoutingMode = "native"
)

// ProxySpec values are injected into FOUR distinct consumers. Missing any one of
// them produces a failure that is extremely hard to attribute in the field:
//
//  1. rke2-server / rke2-agent systemd unit environment
//  2. containerd (image pull)
//  3. helm / malmok's own egress
//  4. in-cluster workloads that egress (ArgoCD, cert-manager ACME)
type ProxySpec struct {
	HTTP    string   `yaml:"http,omitempty"    json:"http,omitempty"`
	HTTPS   string   `yaml:"https,omitempty"   json:"https,omitempty"`
	NoProxy []string `yaml:"noProxy,omitempty" json:"noProxy,omitempty"`
	// CACert: proxies performing TLS interception need their CA trusted.
	CACert SourceRef `yaml:"caCert,omitempty" json:"caCert,omitempty"`
}

// ---------------------------------------------------------------------------
// Topology
// ---------------------------------------------------------------------------

type TopologySpec struct {
	// RegistrationAddress is the address every node uses to join
	// (https://<addr>:9345). A VIP or DNS name, never a node IP — otherwise HA
	// promotion later requires re-joining every node (ADR-008).
	// Required even for single-node deployments.
	RegistrationAddress string `yaml:"registrationAddress" json:"registrationAddress"`

	// AcceptNodeRegistration accepts a registrationAddress that is a node's own
	// IP, taking the cost ADR-008 exists to prevent: promoting to HA later
	// means re-joining every node.
	//
	// It exists because the alternatives are not always available. An ARP VIP
	// is a second IP answering on a segment, which IDC and air-gapped network
	// policy frequently forbids, and a DNS name needs a zone somebody is
	// allowed to write to. A site that has neither still deserves a cluster —
	// but the trade is theirs to state, not this tool's to assume.
	AcceptNodeRegistration bool `yaml:"acceptNodeRegistration,omitempty" json:"acceptNodeRegistration,omitempty"`

	VIP *VIPSpec `yaml:"vip,omitempty" json:"vip,omitempty"`

	Servers []NodeSpec `yaml:"servers" json:"servers"`
	Agents  []NodeSpec `yaml:"agents,omitempty" json:"agents,omitempty"`

	// TLSSAN must pre-register the addresses of *future* servers so that HA
	// promotion does not require certificate regeneration.
	TLSSAN []string `yaml:"tlsSAN,omitempty" json:"tlsSAN,omitempty"`
}

type VIPProvider string

const (
	// VIPKubeVIP: static pod, works before CNI is up. Correct choice for the
	// control-plane VIP. Cilium LB-IPAM cannot serve this role (chicken/egg).
	VIPKubeVIP VIPProvider = "kube-vip"
	// VIPNone: customer refuses to allocate a spare IP. Falls back to a DNS
	// record that the operator must maintain. Tier-2.
	VIPNone VIPProvider = "none"
	VIPBYO  VIPProvider = "byo" // external hardware/virtual LB
)

type VIPSpec struct {
	Provider  VIPProvider `yaml:"provider" json:"provider"`
	Address   string      `yaml:"address,omitempty" json:"address,omitempty"`
	Interface string      `yaml:"interface,omitempty" json:"interface,omitempty"` // "auto" => resolved by PF-607
	// ARPMode: "arp" (L2) or "bgp". BGP requires customer network cooperation.
	Mode string `yaml:"mode,omitempty" json:"mode,omitempty"`
}

type NodeRole string

const (
	RoleServer NodeRole = "server"
	RoleAgent  NodeRole = "agent"
)

type NodeSpec struct {
	Host string   `yaml:"host" json:"host"`
	Role NodeRole `yaml:"role,omitempty" json:"role,omitempty"`

	// Hostname override. Empty => use the node's own hostname.
	// Duplicate hostnames across nodes are a hard failure (PF-604).
	Hostname string `yaml:"hostname,omitempty" json:"hostname,omitempty"`

	SSH SSHSpec `yaml:"ssh,omitempty" json:"ssh,omitempty"`

	// Local runs this node's work on the machine malmok is invoked on
	// instead of opening an SSH connection to it.
	//
	// Unset means decide from the address, which is right almost always: an
	// address either is or is not assigned to an interface here, and installing
	// onto the machine you are sitting at should not require an sshd and a
	// credential for your own box. It is a pointer because both overrides are
	// real. Set it true where the address is not one this machine holds -- a
	// node behind NAT, or one named by the VIP it will carry once the cluster
	// is up. Set it false where it is: a tool running in a container with host
	// networking sees the host's addresses and is not the host, and the address
	// alone cannot tell the two apart.
	Local *bool `yaml:"local,omitempty" json:"local,omitempty"`

	Labels map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
	Taints []string          `yaml:"taints,omitempty" json:"taints,omitempty"`

	// NodeIP pins the address advertised to the cluster on multi-homed hosts.
	// Leaving this empty on a DMZ node with two NICs is a classic silent failure.
	NodeIP string `yaml:"nodeIP,omitempty" json:"nodeIP,omitempty"`

	// GPU marks the node for device-plugin / RuntimeClass installation.
	GPU *GPUSpec `yaml:"gpu,omitempty" json:"gpu,omitempty"`

	// Annotations are opaque, like Metadata.Annotations: a caller that knows
	// this node by another name (a Proxmox VMID, an asset tag) writes it here
	// and reads it back out of the handoff. Nothing in the engine interprets
	// them -- an interpreted field would be schema, and external identifiers
	// do not deserve schema.
	Annotations map[string]string `yaml:"annotations,omitempty" json:"annotations,omitempty"`
}

type GPUSpec struct {
	Vendor string `yaml:"vendor" json:"vendor"` // "amd" | "nvidia"
}

type SSHSpec struct {
	User           string    `yaml:"user,omitempty"           json:"user,omitempty"`
	Port           int       `yaml:"port,omitempty"           json:"port,omitempty"`
	PrivateKey     SourceRef `yaml:"privateKey,omitempty"     json:"privateKey,omitempty"`
	Password       SourceRef `yaml:"password,omitempty"       json:"password,omitempty"`
	Become         *bool     `yaml:"become,omitempty"         json:"become,omitempty"`
	BecomePassword SourceRef `yaml:"becomePassword,omitempty" json:"becomePassword,omitempty"`
	// Bastion: customer sites frequently only expose a jump host.
	Bastion string `yaml:"bastion,omitempty" json:"bastion,omitempty"`
}

// ---------------------------------------------------------------------------
// OS
// ---------------------------------------------------------------------------

type OSFamily string

const (
	OSAuto   OSFamily = "auto"
	OSUbuntu OSFamily = "ubuntu"
	OSRocky  OSFamily = "rocky"
)

type OSSpec struct {
	Family OSFamily `yaml:"family,omitempty" json:"family,omitempty"` // default: auto

	// SELinux: "auto" installs rke2-selinux on RHEL-family. Omitting this on
	// Rocky produces cluster-wide pod failures whose root cause is very hard to
	// trace from the symptom.
	SELinux string `yaml:"selinux,omitempty" json:"selinux,omitempty"` // auto | enforcing | permissive | disabled

	Hardening HardeningSpec `yaml:"hardening,omitempty" json:"hardening,omitempty"`

	// NTPServers: required in airgap (no pool.ntp.org). Clock skew breaks TLS
	// immediately, so PF-501/502 are hard blockers.
	NTPServers []string `yaml:"ntpServers,omitempty" json:"ntpServers,omitempty"`

	// DisableSwap: unset means yes. The kubelet refuses to start with swap on
	// unless it is told to tolerate it, so turning swap off is the only default
	// that produces a cluster. Setting it false is a statement that this site
	// keeps its swap, and it is honoured -- along with the kubelet argument
	// that makes it survivable, which spec validation requires.
	DisableSwap *bool `yaml:"disableSwap,omitempty" json:"disableSwap,omitempty"`
}

// SwapDisabled reports whether the tool turns swap off on the nodes.
//
// This is the one piece of behaviour a schema package decides, because three
// packages read the field -- node preparation, validation and preflight -- and
// an unset pointer that means different things in each of them is how a
// document ends up doing something nobody asked for.
func (o OSSpec) SwapDisabled() bool {
	return o.DisableSwap == nil || *o.DisableSwap
}

type HardeningSpec struct {
	// CISProfile toggles RKE2's `profile: cis`. This is a DISRUPTIVE day-2
	// operation (kernel params, etcd ownership, admission config, restart).
	// Recommendation: leave false at v1; the L0 role still installs the
	// prerequisites so that enabling later avoids a reboot.
	CISProfile *bool `yaml:"cisProfile,omitempty" json:"cisProfile,omitempty"`
	// PrepareCISPrerequisites installs etcd user, sysctl, kernel params without
	// activating the profile. Cheap, reversible, saves a reboot later.
	PrepareCISPrerequisites *bool `yaml:"prepareCISPrerequisites,omitempty" json:"prepareCISPrerequisites,omitempty"`
}

// ---------------------------------------------------------------------------
// Kubernetes / dataplane
// ---------------------------------------------------------------------------

type KubernetesSpec struct {
	Version string `yaml:"version" json:"version"` // e.g. v1.34.5+rke2r1

	// ArtifactPath is a directory on each node holding RKE2's release
	// artifacts for exactly this version: the tarball, its checksum file, the
	// images archive and install.sh.
	//
	// Empty means the installer fetches them, which needs a route to GitHub.
	// An air-gapped node has none, so the artifacts are carried there first
	// and this says where they landed -- and the installer verifies them
	// against the checksum file beside them rather than trusting the copy.
	//
	// Per node, not on the operator's machine: the install runs on the node.
	ArtifactPath string `yaml:"artifactPath,omitempty" json:"artifactPath,omitempty"`

	Dataplane DataplaneSpec `yaml:"dataplane" json:"dataplane"`

	// DisableBundled: rke2-ingress-nginx is ALWAYS disabled by the engine
	// regardless of this list — the upstream project reached EOL in March 2026
	// and receives no security patches. Listing it here is documentation only.
	DisableBundled []string `yaml:"disableBundled,omitempty" json:"disableBundled,omitempty"`

	KubeletArgs   []string `yaml:"kubeletArgs,omitempty"   json:"kubeletArgs,omitempty"`
	APIServerArgs []string `yaml:"apiServerArgs,omitempty" json:"apiServerArgs,omitempty"`

	Etcd EtcdSpec `yaml:"etcd,omitempty" json:"etcd,omitempty"`
}

// DataplanePreset binds CNI + Gateway + LoadBalancer-IP source as ONE atomic
// choice. These three are not independent: dropping Cilium also removes
// LB-IPAM, which the Gateway depends on for an external address.
type DataplanePreset string

const (
	// CiliumGW: Cilium CNI (kube-proxy replacement) + Cilium Gateway API +
	// Cilium LB-IPAM/L2 announcement. No MetalLB required.
	DataplaneCiliumGW DataplanePreset = "cilium-gw"
	// CiliumTraefik: Cilium CNI + Traefik gateway. For sites that need Traefik's
	// middleware ecosystem but can run eBPF.
	DataplaneCiliumTraefik DataplanePreset = "cilium-traefik"
	// CanalTraefik: RKE2 bundled Canal + Traefik + ServiceLB. The conservative
	// fallback for kernels or security agents that reject eBPF.
	DataplaneCanalTraefik DataplanePreset = "canal-traefik"
)

type DataplaneSpec struct {
	Preset   DataplanePreset `yaml:"preset"             json:"preset"`
	Fallback DataplanePreset `yaml:"fallback,omitempty" json:"fallback,omitempty"`

	// DowngradePolicy governs what happens when preflight determines the
	// requested preset is not supportable on one or more nodes.
	//
	//   auto    -> silently downgrade, record reason in audit report
	//   confirm -> halt and require explicit operator approval  (DEFAULT for
	//              airgap-* profiles: an unnoticed downgrade discovered months
	//              later at a customer site is expensive to explain)
	//   forbid  -> hard failure
	DowngradePolicy DowngradePolicy `yaml:"downgradePolicy,omitempty" json:"downgradePolicy,omitempty"`

	// GatewayAPICRDs: Cilium does not install Gateway API CRDs itself.
	// Default true.
	InstallGatewayAPICRDs *bool `yaml:"installGatewayAPICRDs,omitempty" json:"installGatewayAPICRDs,omitempty"`

	// LoadBalancerPool is required for cilium-* presets (LB-IPAM range) and for
	// canal-traefik when ServiceLB is insufficient.
	LoadBalancerPool []string `yaml:"loadBalancerPool,omitempty" json:"loadBalancerPool,omitempty"`
}

type DowngradePolicy string

const (
	DowngradeAuto    DowngradePolicy = "auto"
	DowngradeConfirm DowngradePolicy = "confirm"
	DowngradeForbid  DowngradePolicy = "forbid"
)

type EtcdSpec struct {
	SnapshotSchedule  string `yaml:"snapshotSchedule,omitempty"  json:"snapshotSchedule,omitempty"`
	SnapshotRetention int    `yaml:"snapshotRetention,omitempty" json:"snapshotRetention,omitempty"`
	// SnapshotTarget: local path, NFS mount, or S3-compatible endpoint.
	// A backup that has never been restore-tested is not a backup; the engine
	// exposes `malmok restore --dry-run` to force the rehearsal.
	SnapshotTarget string  `yaml:"snapshotTarget,omitempty" json:"snapshotTarget,omitempty"`
	S3             *S3Spec `yaml:"s3,omitempty" json:"s3,omitempty"`
}

type S3Spec struct {
	Endpoint  string    `yaml:"endpoint"            json:"endpoint"`
	Bucket    string    `yaml:"bucket"              json:"bucket"`
	Region    string    `yaml:"region,omitempty"    json:"region,omitempty"`
	AccessKey SourceRef `yaml:"accessKey,omitempty" json:"accessKey,omitempty"`
	SecretKey SourceRef `yaml:"secretKey,omitempty" json:"secretKey,omitempty"`
	CACert    SourceRef `yaml:"caCert,omitempty"    json:"caCert,omitempty"`
}

// ---------------------------------------------------------------------------
// PKI
// ---------------------------------------------------------------------------

type PKIMode string

const (
	// PKINone issues nothing at build time.
	//
	// This is the common case for a first build: the service domain has not
	// been decided, DNS has not been delegated, and nobody has yet said whether
	// the customer will supply certificates or wants them issued. Forcing a
	// mode and a domain at that point configures the cluster around guesses,
	// and a certificate for a name nobody uses still has to be renewed.
	//
	// Gateways come up on HTTP; certificates are added later with
	// `malmok cert apply`, the same command that renews them.
	PKINone       PKIMode = "none"
	PKIACMEDNS01  PKIMode = "acme-dns01" // real LE wildcard (homelab / company)
	PKIACMEHTTP01 PKIMode = "acme-http01"
	PKIPrivateCA  PKIMode = "private-ca" // airgap: offline root, intermediate imported
	PKIBYOCert    PKIMode = "byo-cert"   // customer supplies a cert bundle
)

type PKISpec struct {
	Mode PKIMode `yaml:"mode"   json:"mode"`
	// Domain is the service domain certificates are issued for. Not required
	// with mode none: at a first build it is frequently not yet decided, and a
	// placeholder here becomes a certificate for a name nobody uses.
	Domain string `yaml:"domain,omitempty" json:"domain,omitempty"`

	ACME      *ACMESpec      `yaml:"acme,omitempty"      json:"acme,omitempty"`
	PrivateCA *PrivateCASpec `yaml:"privateCA,omitempty" json:"privateCA,omitempty"`
	BYOCert   *BYOCertSpec   `yaml:"byoCert,omitempty"   json:"byoCert,omitempty"`

	Trust TrustSpec `yaml:"trustDistribution,omitempty" json:"trustDistribution,omitempty"`
}

type ACMESpec struct {
	Email       string `yaml:"email"                json:"email"`
	Server      string `yaml:"server,omitempty"     json:"server,omitempty"`
	DNSProvider string `yaml:"dnsProvider,omitempty" json:"dnsProvider,omitempty"` // e.g. cloudflare, route53
	// APIToken is the provider's secret: a Cloudflare API token, or an AWS
	// secret access key for route53.
	APIToken SourceRef `yaml:"apiToken,omitempty"   json:"apiToken,omitempty"`

	// AccessKeyID identifies the AWS credential whose secret is APIToken.
	//
	// route53 only, and only off AWS. Inside AWS cert-manager picks up an
	// instance profile or an IRSA role and needs neither field; on the bare
	// metal this tool usually installs there is no ambient credential, and a
	// route53 solver without one is Ready and cannot solve -- the failure
	// arrives as an AWS authentication error inside a Challenge nobody is
	// looking at.
	//
	// The key ID is not a secret, which is why it is a plain string while the
	// secret access key goes through SourceRef like every other credential.
	AccessKeyID string `yaml:"accessKeyID,omitempty" json:"accessKeyID,omitempty"`
	// Region is the AWS region for the route53 API. Defaults to us-east-1,
	// which is where the global Route53 endpoint lives.
	Region string `yaml:"region,omitempty" json:"region,omitempty"`
	// HostedZoneID narrows the solver to one zone. Optional: without it
	// cert-manager searches the account's zones for the name being issued.
	HostedZoneID string `yaml:"hostedZoneID,omitempty" json:"hostedZoneID,omitempty"`
}

type PrivateCASpec struct {
	// RootCert is the public root. The root PRIVATE KEY must stay offline —
	// the engine never accepts it and will reject a bundle containing one.
	RootCert SourceRef `yaml:"rootCert" json:"rootCert"`
	// Intermediate signs leaf certs inside the cluster via cert-manager.
	IntermediateCert SourceRef `yaml:"intermediateCert" json:"intermediateCert"`
	IntermediateKey  SourceRef `yaml:"intermediateKey"  json:"intermediateKey"`
	// SelfSign generates a throwaway root+intermediate when no PKI exists.
	// Tier-2: acceptable for PoC, must be flagged in the audit report.
	SelfSign *bool `yaml:"selfSign,omitempty" json:"selfSign,omitempty"`
}

type BYOCertSpec struct {
	Cert   SourceRef `yaml:"cert"             json:"cert"`
	Key    SourceRef `yaml:"key"              json:"key"`
	CACert SourceRef `yaml:"caCert,omitempty" json:"caCert,omitempty"`
}

// TrustSpec controls the THREE independent trust stores. They are separate
// systems and each has its own failure signature:
//
//	ClusterBundle       missing -> in-cluster clients reject the CA
//	NodeTrustStore      missing -> host-level tooling (curl, helm on node) fails
//	ContainerdRegistries missing -> ALL image pulls from the private registry
//	                                fail with an opaque x509 error. This is the
//	                                single most common private-CA misinstall.
type TrustSpec struct {
	ClusterBundle        *bool `yaml:"clusterBundle,omitempty"        json:"clusterBundle,omitempty"`        // trust-manager
	NodeTrustStore       *bool `yaml:"nodeTrustStore,omitempty"       json:"nodeTrustStore,omitempty"`       // OS ca-certificates
	ContainerdRegistries *bool `yaml:"containerdRegistries,omitempty" json:"containerdRegistries,omitempty"` // registries.yaml
}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

type RegistryMode string

const (
	// RegistryEmbedded uses RKE2's own embedded registry mirror, which shares
	// images peer-to-peer between nodes that already hold them.
	//
	// The right default for most builds: it needs no registry to be stood up,
	// no credentials to be managed and no second thing to keep alive for the
	// life of the cluster. What it is not is a registry you push to -- it
	// mirrors what the nodes already have, so an air-gapped cluster still needs
	// images seeded from a bundle first.
	RegistryEmbedded RegistryMode = "embedded"
	// RegistryUpstream pulls straight from the internet. Online builds with no
	// registry of their own.
	RegistryUpstream RegistryMode = "upstream"
	RegistryExternal RegistryMode = "external" // existing Harbor
	RegistryInternal RegistryMode = "internal" // Hauler-served, seeded from bundle
	RegistryBYO      RegistryMode = "byo"
)

type RegistrySpec struct {
	Mode RegistryMode `yaml:"mode" json:"mode"`

	// SystemDefaultRegistry is passed to RKE2 so that every system image is
	// pulled from the private registry.
	SystemDefaultRegistry string `yaml:"systemDefaultRegistry,omitempty" json:"systemDefaultRegistry,omitempty"`

	Username SourceRef `yaml:"username,omitempty" json:"username,omitempty"`
	Password SourceRef `yaml:"password,omitempty" json:"password,omitempty"`
	CACert   SourceRef `yaml:"caCert,omitempty"   json:"caCert,omitempty"`
	Insecure *bool     `yaml:"insecure,omitempty" json:"insecure,omitempty"`

	// Mirrors maps upstream host -> private endpoint (containerd registries.yaml).
	Mirrors map[string][]string `yaml:"mirrors,omitempty" json:"mirrors,omitempty"`

	// Bundle is the Hauler artifact (.tar.zst) carried across the air gap.
	Bundle string `yaml:"bundle,omitempty" json:"bundle,omitempty"`

	// ChartRepo is where the platform's Helm charts come from.
	//
	// SystemDefaultRegistry moves the images; this moves the charts, and a
	// site that mirrors one without the other gets an install that pulls its
	// containers locally and its chart definitions from the internet. In an
	// air gap that is not a slow install, it is a failed one: cert-manager,
	// the metrics stack and ArgoCD each fetch a chart before they fetch an
	// image.
	//
	// Two forms are understood, because the two kinds of mirror are:
	//
	//	https://charts.acme.internal        a Helm repository
	//	oci://harbor.acme.internal/charts   an OCI registry (ADR-007)
	//
	// Empty means the upstream each chart came from, which is right wherever
	// there is a route to it.
	ChartRepo string `yaml:"chartRepo,omitempty" json:"chartRepo,omitempty"`
}

// ---------------------------------------------------------------------------
// Storage
// ---------------------------------------------------------------------------

type StorageDriver string

const (
	StorageLocalPath StorageDriver = "local-path"
	StorageLonghorn  StorageDriver = "longhorn"
	StorageNFS       StorageDriver = "nfs"
	StorageBYOCSI    StorageDriver = "byo-csi"
)

type StorageSpec struct {
	Driver       StorageDriver `yaml:"driver"                 json:"driver"`
	DefaultClass *bool         `yaml:"defaultClass,omitempty" json:"defaultClass,omitempty"`

	// Longhorn prerequisites are checked by PF-404 (iscsid) and PF-405 (NFS
	// client). PF-406 covers multipathd claiming Longhorn devices, a well-known
	// and confusing failure mode.
	Longhorn *LonghornSpec `yaml:"longhorn,omitempty" json:"longhorn,omitempty"`
	NFS      *NFSSpec      `yaml:"nfs,omitempty"      json:"nfs,omitempty"`
}

type LonghornSpec struct {
	DataPath     string `yaml:"dataPath,omitempty"      json:"dataPath,omitempty"`
	ReplicaCount int    `yaml:"replicaCount,omitempty"  json:"replicaCount,omitempty"`
	BackupTarget string `yaml:"backupTarget,omitempty"  json:"backupTarget,omitempty"`
}

type NFSSpec struct {
	Server string `yaml:"server" json:"server"`
	Path   string `yaml:"path"   json:"path"`
}

// ---------------------------------------------------------------------------
// Platform add-ons (L2)
// ---------------------------------------------------------------------------

type PlatformSpec struct {
	GitOps        GitOpsSpec        `yaml:"gitops,omitempty"        json:"gitops,omitempty"`
	Observability ObservabilitySpec `yaml:"observability,omitempty" json:"observability,omitempty"`
	Secrets       SecretsSpec       `yaml:"secrets,omitempty"       json:"secrets,omitempty"`
	Upgrade       UpgradeSpec       `yaml:"upgradeController,omitempty" json:"upgradeController,omitempty"`
}

type GitOpsSourceType string

const (
	// GitOpsOCI points ArgoCD Applications at Helm charts stored in the private
	// OCI registry. This removes the need for a Git server inside the air gap —
	// images and charts both live in Harbor.
	GitOpsOCI GitOpsSourceType = "oci"
	GitOpsGit GitOpsSourceType = "git"
)

type GitOpsSpec struct {
	Enabled *bool            `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Source  GitOpsSourceType `yaml:"source,omitempty"  json:"source,omitempty"`
	OCIRepo string           `yaml:"ociRepo,omitempty" json:"ociRepo,omitempty"`
	GitRepo string           `yaml:"gitRepo,omitempty" json:"gitRepo,omitempty"`
	Branch  string           `yaml:"branch,omitempty"  json:"branch,omitempty"`
	// BootstrapApps are applied once by the engine; everything afterwards is
	// reconciled by ArgoCD.
	BootstrapApps []string `yaml:"bootstrapApps,omitempty" json:"bootstrapApps,omitempty"`
}

// ObservabilitySpec is the metrics stack the platform runs.
//
// Platform, not application: every workload needs the same answer to "is the
// node out of memory", and a cluster where each chart brings its own collector
// has telemetry rather than observability.
type ObservabilitySpec struct {
	// Enabled defaults per profile: on where the cluster has a network, off in
	// an airgap, where every image has to be seeded into the registry first
	// and a chart that pulls from the internet fails opaquely.
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`

	// Stack names what is installed. Only victoria-metrics is implemented.
	//
	// VictoriaMetrics rather than Prometheus: same query language and the same
	// scrape configs, an order of magnitude less memory for the same series
	// count, and Apache-2.0 throughout.
	Stack ObservabilityStack `yaml:"stack,omitempty" json:"stack,omitempty"`

	// Retention is how long samples are kept, in VictoriaMetrics' notation:
	// 7d, 4w, 1y. Short by default -- metrics land on whatever the default
	// StorageClass gives them, which on a single node is the same filesystem
	// as etcd and the image store (PF-401). A full disk there is not a lost
	// dashboard, it is a stopped cluster.
	Retention string `yaml:"retention,omitempty" json:"retention,omitempty"`

	// StorageSize is the volume the metrics database asks for.
	StorageSize string `yaml:"storageSize,omitempty" json:"storageSize,omitempty"`

	// Grafana adds the dashboard suite. Off unless asked: Grafana OSS is
	// AGPL-3.0, which is a licence a customer's legal review may refuse, and
	// VictoriaMetrics answers ad-hoc queries through its own UI without it.
	Grafana *bool `yaml:"grafana,omitempty" json:"grafana,omitempty"`
}

// ObservabilityStack names a metrics stack.
type ObservabilityStack string

const (
	// ObservabilityVictoriaMetrics is victoria-metrics-k8s-stack: the
	// operator, a single-node database, the scraper, rule evaluation and
	// alerting, with kube-state-metrics and node-exporter feeding them.
	ObservabilityVictoriaMetrics ObservabilityStack = "victoria-metrics"
)

type SecretsSpec struct {
	Provider string    `yaml:"provider,omitempty" json:"provider,omitempty"` // sops-age
	AgeKey   SourceRef `yaml:"ageKey,omitempty"   json:"ageKey,omitempty"`
}

type UpgradeSpec struct {
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// Airgap: upgrade images must be pre-seeded into the registry before the
	// controller is allowed to act, otherwise it will drain a node and stall.
	RequirePreSeededImages *bool `yaml:"requirePreSeededImages,omitempty" json:"requirePreSeededImages,omitempty"`
}

// ---------------------------------------------------------------------------
// Output / audit
// ---------------------------------------------------------------------------

type OutputSpec struct {
	// AuditReport emits component version manifest, SBOM, CIS scan result and —
	// critically — every downgrade decision with its triggering probe ID.
	AuditReport *bool  `yaml:"auditReport,omitempty" json:"auditReport,omitempty"`
	BundlePath  string `yaml:"bundlePath,omitempty"  json:"bundlePath,omitempty"`
	// EventLog is the JSONL stream the TUI subscribes to. Always written, even
	// in headless mode; it is the only post-mortem artifact after a TUI session
	// scrolls away.
	EventLog string `yaml:"eventLog,omitempty" json:"eventLog,omitempty"`
}

// ---------------------------------------------------------------------------
// SourceRef
// ---------------------------------------------------------------------------

// SourceRef is an indirection for any value that may be secret or bulky.
// Supported schemes:
//
//	file://./pki/root.crt
//	env://REGISTRY_PASSWORD
//	sops://secrets.enc.yaml#registry.password
//	literal://plaintext        (rejected for secret-typed fields unless
//	                            --allow-literal-secrets is passed)
//
// Rationale: cluster.yaml is an audit artifact and gets handed to customers.
// Plaintext credentials inside it are a liability, so the schema makes the
// safe path the default one.
type SourceRef string
