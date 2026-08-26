package spec

import (
	"fmt"
	"sort"

	"github.com/ryxen/malmok/api/v1alpha1"
)

// Profile baselines: the Tier-1 combinations of docs/00-architecture.md ADR-003.
//
// A profile is a validated starting point, not a preset that overrides the
// operator. Every field is applied only where the document left a zero value,
// which is what the schema means by "zero value == inherit from profile" — so
// the on-site editable surface stays small without any setting becoming
// unreachable.
//
// Changing this list without adding a CI lane is a release blocker (ADR-003).

// Baseline is the subset of a ClusterSpec that a profile fixes.
type Baseline struct {
	OSFamily        v1alpha1.OSFamily
	NetworkMode     v1alpha1.NetworkMode
	Routing         v1alpha1.RoutingMode
	Dataplane       v1alpha1.DataplanePreset
	Fallback        v1alpha1.DataplanePreset
	DowngradePolicy v1alpha1.DowngradePolicy
	PKIMode         v1alpha1.PKIMode
	// Observability is the metrics stack. On where the cluster has a network:
	// a platform nobody can see the load of is one whose first capacity
	// problem is discovered by a user. Off in an airgap, where every image has
	// to be seeded into the registry before a chart can pull it.
	Observability bool
	Storage       v1alpha1.StorageDriver
	RegistryMode  v1alpha1.RegistryMode
	GitOpsSource  v1alpha1.GitOpsSourceType

	// EncryptNodeTraffic defaults to on wherever nodes can straddle a network
	// boundary: customer security teams object to cleartext inter-node traffic
	// crossing a DMZ, and finding that out after the fact is expensive.
	EncryptNodeTraffic bool

	// RequirePinnedGatewayAddress marks the profiles where a DNS record has to
	// be requested before install starts, so an unset gateway address is worth
	// a warning rather than silent LB-IPAM allocation (PF-612).
	RequirePinnedGatewayAddress bool
}

var baselines = map[v1alpha1.ProfileName]Baseline{
	v1alpha1.ProfileHomelab: {
		OSFamily: v1alpha1.OSUbuntu, NetworkMode: v1alpha1.NetworkOnline,
		Routing:   v1alpha1.RoutingOverlay,
		Dataplane: v1alpha1.DataplaneCiliumGW, Fallback: v1alpha1.DataplaneCanalTraefik,
		DowngradePolicy: v1alpha1.DowngradeAuto,
		// Nothing is issued at build time. The service domain is usually not
		// decided yet, and `malmok cert apply` adds certificates later
		// with the same command used for renewal.
		PKIMode: v1alpha1.PKINone, Storage: v1alpha1.StorageLocalPath,
		// No registry to stand up, no credentials to keep alive for the life of
		// the cluster: RKE2 shares images between nodes that already hold them.
		RegistryMode: v1alpha1.RegistryEmbedded, GitOpsSource: v1alpha1.GitOpsGit,
		Observability: true,
	},
	v1alpha1.ProfileCompanyProd: {
		OSFamily: v1alpha1.OSUbuntu, NetworkMode: v1alpha1.NetworkOnline,
		Routing:   v1alpha1.RoutingOverlay,
		Dataplane: v1alpha1.DataplaneCiliumGW, Fallback: v1alpha1.DataplaneCanalTraefik,
		DowngradePolicy: v1alpha1.DowngradeAuto,
		PKIMode:         v1alpha1.PKIACMEHTTP01, Storage: v1alpha1.StorageLonghorn,
		RegistryMode: v1alpha1.RegistryEmbedded, GitOpsSource: v1alpha1.GitOpsGit,
		Observability: true,
	},
	v1alpha1.ProfileOnpremDMZ: {
		OSFamily: v1alpha1.OSUbuntu, NetworkMode: v1alpha1.NetworkProxy,
		Routing:   v1alpha1.RoutingOverlay,
		Dataplane: v1alpha1.DataplaneCiliumGW, Fallback: v1alpha1.DataplaneCanalTraefik,
		// A downgrade nobody noticed, discovered at a customer site months
		// later, is expensive to explain. Customer-facing profiles ask.
		DowngradePolicy: v1alpha1.DowngradeConfirm,
		PKIMode:         v1alpha1.PKIPrivateCA, Storage: v1alpha1.StorageLonghorn,
		RegistryMode: v1alpha1.RegistryExternal, GitOpsSource: v1alpha1.GitOpsOCI,
		EncryptNodeTraffic: true, RequirePinnedGatewayAddress: true,
		Observability: true,
	},
	v1alpha1.ProfileAirgapUbuntu: {
		OSFamily: v1alpha1.OSUbuntu, NetworkMode: v1alpha1.NetworkAirgap,
		Routing:   v1alpha1.RoutingOverlay,
		Dataplane: v1alpha1.DataplaneCiliumGW, Fallback: v1alpha1.DataplaneCanalTraefik,
		DowngradePolicy: v1alpha1.DowngradeConfirm,
		PKIMode:         v1alpha1.PKIPrivateCA, Storage: v1alpha1.StorageLonghorn,
		RegistryMode: v1alpha1.RegistryInternal, GitOpsSource: v1alpha1.GitOpsOCI,
		EncryptNodeTraffic: true, RequirePinnedGatewayAddress: true,
	},
	v1alpha1.ProfileAirgapRocky: {
		OSFamily: v1alpha1.OSRocky, NetworkMode: v1alpha1.NetworkAirgap,
		Routing:   v1alpha1.RoutingOverlay,
		Dataplane: v1alpha1.DataplaneCiliumGW, Fallback: v1alpha1.DataplaneCanalTraefik,
		DowngradePolicy: v1alpha1.DowngradeConfirm,
		PKIMode:         v1alpha1.PKIPrivateCA, Storage: v1alpha1.StorageLonghorn,
		RegistryMode: v1alpha1.RegistryInternal, GitOpsSource: v1alpha1.GitOpsOCI,
		EncryptNodeTraffic: true, RequirePinnedGatewayAddress: true,
	},
	// The safety net of ADR-003. It exists so that one kernel or security-agent
	// problem cannot stop a delivery: nothing here needs eBPF.
	v1alpha1.ProfileAirgapConservative: {
		OSFamily: v1alpha1.OSRocky, NetworkMode: v1alpha1.NetworkAirgap,
		Routing:   v1alpha1.RoutingOverlay,
		Dataplane: v1alpha1.DataplaneCanalTraefik,
		// No fallback: this already is the fallback.
		DowngradePolicy: v1alpha1.DowngradeConfirm,
		PKIMode:         v1alpha1.PKIPrivateCA, Storage: v1alpha1.StorageNFS,
		RegistryMode: v1alpha1.RegistryInternal, GitOpsSource: v1alpha1.GitOpsOCI,
		EncryptNodeTraffic: true, RequirePinnedGatewayAddress: true,
	},
}

// Profiles lists the known profile names, sorted.
func Profiles() []v1alpha1.ProfileName {
	out := make([]v1alpha1.ProfileName, 0, len(baselines))
	for name := range baselines {
		out = append(out, name)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// BaselineFor returns a profile's fixed values.
func BaselineFor(name v1alpha1.ProfileName) (Baseline, bool) {
	b, ok := baselines[name]
	return b, ok
}

// ApplyProfile fills every field the document left at its zero value from the
// profile's baseline, and reports which fields it supplied.
//
// The list is not bookkeeping: the audit report has to be able to say which
// settings the operator chose and which the tool did, six months after nobody
// remembers.
func (d *Document) ApplyProfile() ([]string, error) {
	name := d.Spec.Metadata.Profile
	if name == "" || name == v1alpha1.ProfileCustom {
		// Tier-3. Nothing is supplied, and plan approval has to acknowledge it.
		return nil, nil
	}
	b, ok := baselines[name]
	if !ok {
		return nil, fmt.Errorf("spec: unknown profile %q; known: %v", name, Profiles())
	}

	var applied []string
	set := func(field string, cond bool, assign func()) {
		if cond {
			assign()
			applied = append(applied, field)
		}
	}
	s := &d.Spec

	set("os.family", s.OS.Family == "" || s.OS.Family == v1alpha1.OSAuto,
		func() { s.OS.Family = b.OSFamily })
	set("network.mode", s.Network.Mode == "",
		func() { s.Network.Mode = b.NetworkMode })
	set("network.routing", s.Network.Routing == "",
		func() { s.Network.Routing = b.Routing })
	set("network.encryptNodeTraffic", s.Network.EncryptNodeTraffic == nil && b.EncryptNodeTraffic,
		func() { v := true; s.Network.EncryptNodeTraffic = &v })

	set("kubernetes.dataplane.preset", s.Kubernetes.Dataplane.Preset == "",
		func() { s.Kubernetes.Dataplane.Preset = b.Dataplane })
	// A baseline fallback is inherited only where it is a fallback at all.
	// Choosing canal-traefik on a profile whose baseline falls back to
	// canal-traefik used to produce "fallback is the same as preset, so a
	// downgrade has nowhere to go" -- the document refused for a value the
	// operator never wrote. An explicit fallback that collides is still an
	// error: that one is the operator's own contradiction.
	set("kubernetes.dataplane.fallback",
		s.Kubernetes.Dataplane.Fallback == "" && b.Fallback != "" &&
			b.Fallback != s.Kubernetes.Dataplane.Preset,
		func() { s.Kubernetes.Dataplane.Fallback = b.Fallback })
	set("kubernetes.dataplane.downgradePolicy", s.Kubernetes.Dataplane.DowngradePolicy == "",
		func() { s.Kubernetes.Dataplane.DowngradePolicy = b.DowngradePolicy })

	set("pki.mode", s.PKI.Mode == "", func() { s.PKI.Mode = b.PKIMode })
	set("storage.driver", s.Storage.Driver == "", func() { s.Storage.Driver = b.Storage })
	set("registry.mode", s.Registry.Mode == "", func() { s.Registry.Mode = b.RegistryMode })
	set("platform.gitops.source", s.Platform.GitOps.Source == "",
		func() { s.Platform.GitOps.Source = b.GitOpsSource })

	// A document that says nothing about observability gets the profile's
	// answer, and a document that says false keeps it: the pointer is what
	// distinguishes "not mentioned" from "not wanted", which is the whole
	// reason it is a pointer.
	set("platform.observability.enabled", s.Platform.Observability.Enabled == nil, func() {
		on := b.Observability
		s.Platform.Observability.Enabled = &on
	})
	set("platform.observability.stack",
		s.Platform.Observability.Stack == "" && b.Observability,
		func() { s.Platform.Observability.Stack = v1alpha1.ObservabilityVictoriaMetrics })

	// rke2-ingress-nginx is disabled whatever the document says: it reached EOL
	// in March 2026 and receives no security patches (ADR-005). Recording it in
	// the applied list keeps that visible in the audit report rather than being
	// a surprise in the manifest.
	if !contains(s.Kubernetes.DisableBundled, "rke2-ingress-nginx") {
		s.Kubernetes.DisableBundled = append(s.Kubernetes.DisableBundled, "rke2-ingress-nginx")
		applied = append(applied, "kubernetes.disableBundled+=rke2-ingress-nginx")
	}

	sort.Strings(applied)
	return applied, nil
}

// PinnedGatewayAddressRequired reports whether this document's profile expects
// every gateway to have its address decided before install (PF-612).
func (d *Document) PinnedGatewayAddressRequired() bool {
	b, ok := baselines[d.Spec.Metadata.Profile]
	return ok && b.RequirePinnedGatewayAddress
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// Match names the profile whose baseline a composition equals, or custom.
//
// The wizard composes a configuration axis by axis rather than offering six
// canned combinations, because a combination nobody anticipated -- a homelab
// behind a proxy, an air-gapped site on the full dataplane -- was otherwise
// unreachable. The profile is the answer to "which validated baseline is this",
// which is a fact about the composition and not a question to ask before it.
//
// It stays in the document because the audit report is built on it: `custom` is
// Tier-3 and says, correctly, that this combination is not one CI exercises.
func Match(b Baseline) v1alpha1.ProfileName {
	for _, name := range Profiles() {
		known, ok := baselines[name]
		if !ok {
			continue
		}
		// A profile's strictness about pinned gateway addresses is a property
		// of the profile, not of the document: nothing in a ClusterSpec records
		// it, so comparing it would relabel every read-back document custom.
		b.RequirePinnedGatewayAddress = known.RequirePinnedGatewayAddress
		if known == b {
			return name
		}
	}
	return v1alpha1.ProfileCustom
}
