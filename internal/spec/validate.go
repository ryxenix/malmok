package spec

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"platform.ryxen.dev/platformctl/api/v1alpha1"
	"platform.ryxen.dev/platformctl/internal/exec"
)

// Validate reports every problem with the document at once.
//
// All of them, not the first: an operator at a customer site who has to run the
// tool six times to learn six things about their own file will stop reading the
// messages. Each error names the field path, so it maps onto the document in
// front of them.
func (d *Document) Validate(allowLiteralSecrets bool) error {
	var errs []error
	bad := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}
	s := &d.Spec

	if strings.TrimSpace(s.Metadata.Name) == "" {
		bad("metadata.name is required")
	}

	errs = append(errs, validateNetwork(s)...)
	errs = append(errs, validateTopology(s)...)
	errs = append(errs, validateKubernetes(s)...)
	errs = append(errs, validatePKI(s)...)
	errs = append(errs, validateRegistry(s)...)
	errs = append(errs, validateStorage(s)...)
	errs = append(errs, validateGateway(s)...)

	for field, ref := range SecretRefs(s) {
		if strings.HasPrefix(strings.TrimSpace(string(ref)), "literal://") && !allowLiteralSecrets {
			errs = append(errs, &ErrSecretInPlaintext{Field: field})
		}
	}

	return errors.Join(errs...)
}

func validateNetwork(s *v1alpha1.ClusterSpec) []error {
	var errs []error
	switch s.Network.Mode {
	case v1alpha1.NetworkOnline, v1alpha1.NetworkAirgap:
	case v1alpha1.NetworkProxy:
		if s.Network.Proxy == nil || (s.Network.Proxy.HTTP == "" && s.Network.Proxy.HTTPS == "") {
			errs = append(errs, errors.New("network.proxy is required when network.mode is proxy"))
		}
	case "":
		errs = append(errs, errors.New("network.mode is required: online, proxy or airgap"))
	default:
		errs = append(errs, fmt.Errorf("network.mode %q is not one of online, proxy, airgap", s.Network.Mode))
	}

	for field, cidr := range map[string]string{
		"network.podCIDR": s.Network.PodCIDR, "network.svcCIDR": s.Network.SvcCIDR,
	} {
		if cidr == "" {
			continue
		}
		if _, err := netip.ParsePrefix(cidr); err != nil {
			errs = append(errs, fmt.Errorf("%s %q is not a CIDR", field, cidr))
		}
	}

	// Native routing assumes the operator owns the L3 fabric. Defaulting to it
	// across segments produces a cluster that half works, so it has to be asked
	// for explicitly and is never inferred.
	if s.Network.Routing != "" &&
		s.Network.Routing != v1alpha1.RoutingOverlay && s.Network.Routing != v1alpha1.RoutingNative {
		errs = append(errs, fmt.Errorf("network.routing %q is not overlay or native", s.Network.Routing))
	}
	return errs
}

func validateTopology(s *v1alpha1.ClusterSpec) []error {
	var errs []error
	t := &s.Topology

	if strings.TrimSpace(t.RegistrationAddress) == "" {
		errs = append(errs, errors.New(
			"topology.registrationAddress is required, even for a single node: "+
				"it must be a VIP or DNS name so that HA promotion later does not "+
				"require re-joining every node (ADR-008)"))
	}
	if len(t.Servers) == 0 {
		errs = append(errs, errors.New("topology.servers needs at least one node"))
	}

	all := append(append([]v1alpha1.NodeSpec{}, t.Servers...), t.Agents...)
	seenHost, seenName := map[string]bool{}, map[string]bool{}
	for i, n := range all {
		if strings.TrimSpace(n.Host) == "" {
			errs = append(errs, fmt.Errorf("topology node %d has no host", i))
			continue
		}
		if seenHost[n.Host] {
			errs = append(errs, fmt.Errorf("topology: host %s appears twice", n.Host))
		}
		seenHost[n.Host] = true

		// A loopback address or the literal `local` says how to reach the node,
		// not what the cluster calls it. Every node needs an address other
		// nodes can use: the registration address, the certificate SANs and the
		// address advertised at join all come from here, and 127.0.0.1 is a
		// different machine from every one of them.
		if isLoopbackHost(n.Host) && strings.TrimSpace(n.NodeIP) == "" {
			errs = append(errs, fmt.Errorf(
				"topology: node %s is named by an address that only means this machine, so it needs "+
					"nodeIP as well -- that is what it registers with and what other nodes reach it on",
				n.Host))
		}

		if n.Hostname != "" {
			if seenName[n.Hostname] {
				errs = append(errs, fmt.Errorf(
					"topology: hostname %s appears twice; duplicates are a hard failure at join (PF-604)",
					n.Hostname))
			}
			seenName[n.Hostname] = true
		}
		if n.NodeIP != "" && net.ParseIP(n.NodeIP) == nil {
			errs = append(errs, fmt.Errorf("topology: node %s has nodeIP %q, which is not an IP", n.Host, n.NodeIP))
		}
		if n.GPU != nil && n.GPU.Vendor != "amd" && n.GPU.Vendor != "nvidia" {
			errs = append(errs, fmt.Errorf("topology: node %s gpu.vendor %q is not amd or nvidia", n.Host, n.GPU.Vendor))
		}
	}

	// A registrationAddress equal to a node's own address is the trap ADR-008
	// exists to close: it works until the day a second server is added, and
	// then every node has to re-join.
	for _, n := range all {
		if t.RegistrationAddress == n.Host || (n.NodeIP != "" && t.RegistrationAddress == n.NodeIP) {
			errs = append(errs, fmt.Errorf(
				"topology.registrationAddress is node %s's own address; use a VIP or DNS name, "+
					"otherwise adding a second server later means re-joining every node (ADR-008)",
				n.Host))
		}
	}

	if t.VIP != nil {
		switch t.VIP.Provider {
		case v1alpha1.VIPKubeVIP:
			if t.VIP.Address == "" {
				errs = append(errs, errors.New("topology.vip.address is required with provider kube-vip"))
			} else if net.ParseIP(t.VIP.Address) == nil {
				errs = append(errs, fmt.Errorf("topology.vip.address %q is not an IP", t.VIP.Address))
			}
		case v1alpha1.VIPNone, v1alpha1.VIPBYO:
		default:
			errs = append(errs, fmt.Errorf("topology.vip.provider %q is not kube-vip, none or byo", t.VIP.Provider))
		}
	}
	return errs
}

func validateKubernetes(s *v1alpha1.ClusterSpec) []error {
	var errs []error
	k := &s.Kubernetes

	if strings.TrimSpace(k.Version) == "" {
		errs = append(errs, errors.New("kubernetes.version is required, e.g. v1.34.5+rke2r1"))
	}

	switch k.Dataplane.Preset {
	case v1alpha1.DataplaneCiliumGW, v1alpha1.DataplaneCiliumTraefik:
		// Cilium supplies the load balancer addresses; without a pool the
		// Gateway comes up with no external IP and looks healthy while being
		// unreachable.
		if len(k.Dataplane.LoadBalancerPool) == 0 {
			errs = append(errs, fmt.Errorf(
				"kubernetes.dataplane.loadBalancerPool is required with preset %s: "+
					"Cilium LB-IPAM has nothing to hand the Gateway otherwise (DG-010)",
				k.Dataplane.Preset))
		}
	case v1alpha1.DataplaneCanalTraefik:
	case "":
		errs = append(errs, errors.New("kubernetes.dataplane.preset is required"))
	default:
		errs = append(errs, fmt.Errorf("kubernetes.dataplane.preset %q is unknown", k.Dataplane.Preset))
	}

	for _, cidr := range k.Dataplane.LoadBalancerPool {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			errs = append(errs, fmt.Errorf("kubernetes.dataplane.loadBalancerPool %q is not a CIDR", cidr))
		}
	}

	switch k.Dataplane.DowngradePolicy {
	case "", v1alpha1.DowngradeAuto, v1alpha1.DowngradeConfirm, v1alpha1.DowngradeForbid:
	default:
		errs = append(errs, fmt.Errorf(
			"kubernetes.dataplane.downgradePolicy %q is not auto, confirm or forbid",
			k.Dataplane.DowngradePolicy))
	}
	if k.Dataplane.Fallback != "" && k.Dataplane.Fallback == k.Dataplane.Preset {
		errs = append(errs, errors.New(
			"kubernetes.dataplane.fallback is the same as preset, so a downgrade has nowhere to go"))
	}
	return errs
}

func validatePKI(s *v1alpha1.ClusterSpec) []error {
	var errs []error
	p := &s.PKI

	// Required only where something is being issued. At a first build the
	// service domain is frequently not decided yet, and a placeholder here
	// becomes a certificate for a name nobody uses.
	if p.Mode != "" && p.Mode != v1alpha1.PKINone && strings.TrimSpace(p.Domain) == "" {
		errs = append(errs, fmt.Errorf("pki.domain is required with mode %s", p.Mode))
	}

	switch p.Mode {
	case v1alpha1.PKINone:
		// Nothing to check. Gateways come up on HTTP and certificates are added
		// later with `platformctl cert apply`.
	case v1alpha1.PKIACMEDNS01, v1alpha1.PKIACMEHTTP01:
		if p.ACME == nil || p.ACME.Email == "" {
			errs = append(errs, fmt.Errorf("pki.acme.email is required with mode %s", p.Mode))
		}
		if p.Mode == v1alpha1.PKIACMEDNS01 && p.ACME != nil && p.ACME.DNSProvider == "" {
			errs = append(errs, errors.New("pki.acme.dnsProvider is required with mode acme-dns01"))
		}
	case v1alpha1.PKIPrivateCA:
		if p.PrivateCA == nil {
			errs = append(errs, errors.New("pki.privateCA is required with mode private-ca"))
			break
		}
		selfSigned := p.PrivateCA.SelfSign != nil && *p.PrivateCA.SelfSign
		if !selfSigned {
			for field, ref := range map[string]v1alpha1.SourceRef{
				"pki.privateCA.rootCert":         p.PrivateCA.RootCert,
				"pki.privateCA.intermediateCert": p.PrivateCA.IntermediateCert,
				"pki.privateCA.intermediateKey":  p.PrivateCA.IntermediateKey,
			} {
				if strings.TrimSpace(string(ref)) == "" {
					errs = append(errs, fmt.Errorf("%s is required with mode private-ca", field))
				}
			}
		}
	case v1alpha1.PKIBYOCert:
		if p.BYOCert == nil || p.BYOCert.Cert == "" || p.BYOCert.Key == "" {
			errs = append(errs, errors.New("pki.byoCert.cert and .key are required with mode byo-cert"))
		}
	case "":
		errs = append(errs, errors.New("pki.mode is required"))
	default:
		errs = append(errs, fmt.Errorf("pki.mode %q is unknown", p.Mode))
	}

	// The private key of an offline root has no business in a cluster.yaml, and
	// no schema field for it exists. Catching the attempt here means the
	// message says why rather than the file simply not working.
	if p.PrivateCA != nil && strings.Contains(strings.ToLower(string(p.PrivateCA.RootCert)), "key") {
		errs = append(errs, errors.New(
			"pki.privateCA.rootCert appears to point at a key; the offline root key must never "+
				"leave its custody (PF-706)"))
	}
	return errs
}

func validateRegistry(s *v1alpha1.ClusterSpec) []error {
	var errs []error
	r := &s.Registry

	switch r.Mode {
	case v1alpha1.RegistryExternal, v1alpha1.RegistryBYO:
		if r.SystemDefaultRegistry == "" {
			errs = append(errs, fmt.Errorf("registry.systemDefaultRegistry is required with mode %s", r.Mode))
		}
	case v1alpha1.RegistryEmbedded, v1alpha1.RegistryInternal:
	case v1alpha1.RegistryUpstream:
		// Pulling from the internet needs the internet.
		if s.Network.Mode == v1alpha1.NetworkAirgap {
			errs = append(errs, errors.New(
				"registry.mode upstream cannot work in an air-gapped network; "+
					"use embedded with a seeded bundle, or an internal registry"))
		}
	case "":
		errs = append(errs, errors.New("registry.mode is required"))
	default:
		errs = append(errs, fmt.Errorf("registry.mode %q is unknown", r.Mode))
	}

	// Air-gapped means nothing can be fetched, so the images have to already be
	// somewhere. A file that does not say where is one that fails on the first
	// pull, hours in.
	if s.Network.Mode == v1alpha1.NetworkAirgap &&
		r.Bundle == "" && r.SystemDefaultRegistry == "" {
		errs = append(errs, errors.New(
			"network.mode is airgap but neither registry.bundle nor registry.systemDefaultRegistry is set; "+
				"there is nowhere to pull images from"))
	}
	return errs
}

func validateStorage(s *v1alpha1.ClusterSpec) []error {
	var errs []error
	st := &s.Storage

	switch st.Driver {
	case v1alpha1.StorageLocalPath, v1alpha1.StorageBYOCSI:
	case v1alpha1.StorageLonghorn:
		if st.Longhorn != nil && st.Longhorn.ReplicaCount < 0 {
			errs = append(errs, errors.New("storage.longhorn.replicaCount must not be negative"))
		}
		if n := len(s.Topology.Servers) + len(s.Topology.Agents); n < 2 && st.Longhorn != nil &&
			st.Longhorn.ReplicaCount > n {
			errs = append(errs, fmt.Errorf(
				"storage.longhorn.replicaCount is %d but the cluster has %d node(s)",
				st.Longhorn.ReplicaCount, n))
		}
	case v1alpha1.StorageNFS:
		if st.NFS == nil || st.NFS.Server == "" || st.NFS.Path == "" {
			errs = append(errs, errors.New("storage.nfs.server and .path are required with driver nfs"))
		}
	case "":
		errs = append(errs, errors.New("storage.driver is required"))
	default:
		errs = append(errs, fmt.Errorf("storage.driver %q is unknown", st.Driver))
	}
	return errs
}

func validateGateway(s *v1alpha1.ClusterSpec) []error {
	var errs []error
	g := &s.Gateway

	// Required once anything is served under a name. Deferring certificates
	// defers the naming question with them: at a first build there is often no
	// domain yet, and applications are reached by address until DNS exists.
	if strings.TrimSpace(g.DomainSuffix) == "" && s.PKI.Mode != v1alpha1.PKINone {
		errs = append(errs, errors.New(
			"gateway.domainSuffix is required; application charts read it from the contract ConfigMap"))
	}

	seen := map[string]bool{}
	for i, gw := range g.Gateways {
		key := gw.Namespace + "/" + gw.Name
		if gw.Name == "" {
			errs = append(errs, fmt.Errorf("gateway.gateways[%d].name is required", i))
		}
		if seen[key] {
			errs = append(errs, fmt.Errorf("gateway.gateways[%d]: %s is declared twice", i, key))
		}
		seen[key] = true

		if gw.Address != "" && net.ParseIP(gw.Address) == nil {
			errs = append(errs, fmt.Errorf("gateway.gateways[%d].address %q is not an IP", i, gw.Address))
		}
		if gw.RouteNamespaces == "selector" && len(gw.NamespaceSelector) == 0 {
			errs = append(errs, fmt.Errorf(
				"gateway.gateways[%d].namespaceSelector is required with routeNamespaces=selector", i))
		}
		if len(gw.Listeners) == 0 {
			errs = append(errs, fmt.Errorf("gateway.gateways[%d] has no listeners", i))
		}

		ports := map[int]bool{}
		for j, l := range gw.Listeners {
			where := fmt.Sprintf("gateway.gateways[%d].listeners[%d]", i, j)
			if l.Name == "" {
				errs = append(errs, fmt.Errorf("%s.name is required", where))
			}
			if l.Port <= 0 || l.Port > 65535 {
				errs = append(errs, fmt.Errorf("%s.port %d is out of range", where, l.Port))
			}
			if ports[l.Port] {
				errs = append(errs, fmt.Errorf("%s: port %d is used twice on this gateway", where, l.Port))
			}
			ports[l.Port] = true

			switch l.Protocol {
			case v1alpha1.ListenerHTTP:
			case v1alpha1.ListenerHTTPS, v1alpha1.ListenerTLSPassthrough:
				// A listener without a tls block inherits the cluster's
				// pki.mode, which is the documented meaning of an empty
				// TLSSource. Requiring the block would force every listener to
				// restate what the cluster already says.
				errs = append(errs, validateListenerTLS(where, l)...)
			case "":
				errs = append(errs, fmt.Errorf("%s.protocol is required", where))
			default:
				errs = append(errs, fmt.Errorf("%s.protocol %q is unknown", where, l.Protocol))
			}
		}
	}
	return errs
}

func validateListenerTLS(where string, l v1alpha1.ListenerSpec) []error {
	var errs []error

	if l.Protocol == v1alpha1.ListenerHTTPS && l.Hostname == "" {
		errs = append(errs, fmt.Errorf(
			"%s.hostname is required on an HTTPS listener; it is what PF-903 checks the certificate SAN against",
			where))
	}
	if l.TLS == nil {
		return errs
	}

	switch l.TLS.Source {
	case v1alpha1.TLSFromPKI, v1alpha1.TLSFromACME, v1alpha1.TLSFromPrivateCA, v1alpha1.TLSPassthrough:
	case v1alpha1.TLSFromSecret:
		if l.TLS.SecretRef == "" {
			errs = append(errs, fmt.Errorf("%s.tls.secretRef is required with source secret", where))
		}
	case v1alpha1.TLSFromBYO:
		if l.TLS.BYO == nil {
			errs = append(errs, fmt.Errorf("%s.tls.byo is required with source byo", where))
			break
		}
		byo := l.TLS.BYO
		if byo.Dir == "" && (byo.Cert == "" || byo.Key == "") {
			errs = append(errs, fmt.Errorf(
				"%s.tls.byo needs either dir, or both cert and key", where))
		}
		// PKCS#12 is reserved in the schema so that adding it later is not a
		// break. Accepting it silently now would produce a Secret nobody can
		// explain.
		if byo.Format != "" && byo.Format != "pem" {
			errs = append(errs, fmt.Errorf(
				"%s.tls.byo.format %q is not supported in v1; only pem", where, byo.Format))
		}
		if byo.PFX != "" {
			errs = append(errs, fmt.Errorf("%s.tls.byo.pfx is reserved and not implemented in v1", where))
		}
		if byo.ExpiryWarningDays < 0 {
			errs = append(errs, fmt.Errorf("%s.tls.byo.expiryWarningDays must not be negative", where))
		}
	default:
		errs = append(errs, fmt.Errorf("%s.tls.source %q is unknown", where, l.TLS.Source))
	}

	return errs
}

// isLoopbackHost reports whether a host value can only ever mean "here".
//
// Kept beside the rule rather than imported from the runner, because the two
// answer different questions: exec asks which machine to run a command on, and
// this asks whether an address is one another node could use. A node's own
// routable address is local to the tool and perfectly usable by the cluster;
// 127.0.0.1 is local to everybody and usable by nobody.
func isLoopbackHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	for _, name := range exec.LocalHostNames {
		if h == name {
			return true
		}
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
