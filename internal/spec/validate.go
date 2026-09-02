package spec

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/ryxen/malmok/api/v1alpha1"
	"github.com/ryxen/malmok/internal/exec"
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
	// then every node has to re-join. Sites whose network policy forbids the
	// alternatives -- an ARP VIP is a second IP on the segment, a DNS name
	// needs a writable zone -- can state the trade with acceptNodeRegistration,
	// which is what makes it a decision rather than an accident.
	for _, n := range all {
		if t.RegistrationAddress != n.Host && (n.NodeIP == "" || t.RegistrationAddress != n.NodeIP) {
			continue
		}
		if !t.AcceptNodeRegistration {
			errs = append(errs, fmt.Errorf(
				"topology.registrationAddress is node %s's own address; use a VIP or DNS name, "+
					"otherwise adding a second server later means re-joining every node (ADR-008). "+
					"If the network policy allows neither, set topology.acceptNodeRegistration: true "+
					"to state that trade",
				n.Host))
		} else if t.VIP != nil && t.VIP.Address != "" {
			// Both at once is a contradiction: a VIP exists exactly so the
			// registration address is not a node's.
			errs = append(errs, fmt.Errorf(
				"topology.acceptNodeRegistration is set and a VIP is configured; "+
					"register through the VIP %s instead", t.VIP.Address))
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
		// unreachable. Except when every gateway answers on the nodes' own
		// addresses -- then there is nothing for LB-IPAM to hand out, and
		// demanding a pool would demand exactly the segment IPs the exposure
		// mode exists to avoid.
		// The rule fires only when a gateway will actually ask: none at all
		// means nothing requests an address, and every gateway on the nodes'
		// own addresses means there is nothing for LB-IPAM to hand out --
		// demanding a pool in either case would demand exactly the segment
		// IPs a first build may not have.
		if len(k.Dataplane.LoadBalancerPool) == 0 &&
			len(s.Gateway.Gateways) > 0 && !allGatewaysOnNodeIPs(s) {
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
		// Nothing to check. Gateways come up on HTTP; certificates are added
		// later by setting pki.mode in the document and applying it again.
	case v1alpha1.PKIACMEDNS01, v1alpha1.PKIACMEHTTP01:
		if p.ACME == nil || p.ACME.Email == "" {
			errs = append(errs, fmt.Errorf("pki.acme.email is required with mode %s", p.Mode))
		}
		if p.Mode == v1alpha1.PKIACMEDNS01 && p.ACME != nil && p.ACME.DNSProvider == "" {
			errs = append(errs, errors.New("pki.acme.dnsProvider is required with mode acme-dns01"))
		}
		// An AWS key ID without its secret, or the reverse, produces a solver
		// that cert-manager accepts and cannot use: the ClusterIssuer reports
		// Ready and the failure surfaces much later, as an authentication
		// error inside a Challenge. Both or neither -- neither meaning the
		// ambient credential of an EC2 instance profile or an IRSA role.
		if p.Mode == v1alpha1.PKIACMEDNS01 && p.ACME != nil && p.ACME.DNSProvider == "route53" {
			hasID := strings.TrimSpace(p.ACME.AccessKeyID) != ""
			hasSecret := strings.TrimSpace(string(p.ACME.APIToken)) != ""
			switch {
			case hasID && !hasSecret:
				errs = append(errs, errors.New(
					"pki.acme.accessKeyID is set but pki.acme.apiToken is not; route53 needs the secret access key too"))
			case !hasID && hasSecret:
				errs = append(errs, errors.New(
					"pki.acme.apiToken is set but pki.acme.accessKeyID is not; route53 needs both, or neither to use an instance profile"))
			}
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
	//
	// There are three somewheres, not two. RKE2's own airgap artifacts include
	// rke2-images-*.tar.zst, which the installer loads into the node's image
	// store before anything starts -- that is the path RKE2 documents for a
	// cluster with no registry at all, and naming a registry to satisfy this
	// rule breaks it: system-default-registry rewrites every system image
	// reference, and the preloaded images carry their original names.
	//
	// Whether the archive is really in that directory is not a question a
	// document can answer. PF-709 reads it on the node and says so.
	if s.Network.Mode == v1alpha1.NetworkAirgap &&
		r.Bundle == "" && r.SystemDefaultRegistry == "" &&
		strings.TrimSpace(s.Kubernetes.ArtifactPath) == "" {
		errs = append(errs, errors.New(
			"network.mode is airgap but none of registry.bundle, registry.systemDefaultRegistry "+
				"or kubernetes.artifactPath is set; there is nowhere to pull images from"))
	}

	// The binaries are the third thing to carry. An air-gapped node cannot
	// reach the release page either, and the installer answers a missing
	// artifact path with a failed download -- on a machine that was never
	// going to download anything.
	if s.Network.Mode == v1alpha1.NetworkAirgap && strings.TrimSpace(s.Kubernetes.ArtifactPath) == "" {
		errs = append(errs, errors.New(
			"network.mode is airgap and kubernetes.artifactPath is not set; "+
				"RKE2's own tarball, checksum and images archive are carried to each node "+
				"and the document says where they landed"))
	}

	// Images and charts are two mirrors, and a site that moved one without the
	// other has an install that pulls its containers locally and its chart
	// definitions from the internet. cert-manager, the metrics stack and
	// ArgoCD each fetch a chart before they fetch an image, so in an air gap
	// the run reaches l2-pki and stops there -- twenty minutes after the point
	// where this could have been said.
	if s.Network.Mode == v1alpha1.NetworkAirgap && r.ChartRepo == "" && wantsCharts(s) {
		errs = append(errs, errors.New(
			"network.mode is airgap and registry.chartRepo is not set; "+
				"cert-manager, observability and ArgoCD each fetch a Helm chart, "+
				"so mirror them and name the mirror (https:// or oci://)"))
	}
	return errs
}

// wantsCharts reports whether the document asks for anything installed by a
// Helm chart this tool fetches.
//
// The dataplane is not among them: RKE2 carries Cilium's chart and images in
// its own airgap artifacts, which is why an air-gapped cluster comes up with a
// network before any of this matters.
func wantsCharts(s *v1alpha1.ClusterSpec) bool {
	if s.PKI.Mode != "" && s.PKI.Mode != v1alpha1.PKINone && s.PKI.Mode != v1alpha1.PKIBYOCert {
		return true
	}
	if o := s.Platform.Observability; o.Enabled != nil && *o.Enabled {
		return true
	}
	if g := s.Platform.GitOps; g.Enabled != nil && *g.Enabled {
		return true
	}
	return false
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
		errs = append(errs, validateGatewayExposure(s, i, gw)...)
		if i == 0 && mixedExposure(s) {
			errs = append(errs, errors.New(
				"gateway: node-ips and load-balanced gateways cannot coexist; Cilium's host "+
					"networking is cluster-wide, so one node-ips gateway moves every gateway's "+
					"envoy into the host namespace"))
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

// mixedExposure reports whether the gateways disagree about how they are
// reached. Cilium's host networking is cluster-wide configuration: one node-ips
// gateway puts every gateway's envoy in the host namespace, so a load-balanced
// gateway beside it would silently stop being what the document says it is.
func mixedExposure(s *v1alpha1.ClusterSpec) bool {
	var lb, node bool
	for _, gw := range s.Gateway.Gateways {
		if gw.Exposure == v1alpha1.ExposureNodeIPs {
			node = true
		} else {
			lb = true
		}
	}
	return lb && node
}

// allGatewaysOnNodeIPs reports whether every gateway answers on node addresses,
// which is the one case a Cilium preset needs no LB pool.
func allGatewaysOnNodeIPs(s *v1alpha1.ClusterSpec) bool {
	if len(s.Gateway.Gateways) == 0 {
		return false
	}
	for _, gw := range s.Gateway.Gateways {
		if gw.Exposure != v1alpha1.ExposureNodeIPs {
			return false
		}
	}
	return true
}

// validateGatewayExposure checks one gateway's exposure statement.
//
// The rules mirror acceptNodeRegistration's: the node addresses are a real
// answer for a site whose policy allows no others, and a half-statement -- a
// pinned pool address on a gateway that answers from the nodes, or a node the
// topology does not contain -- is caught here rather than as an unreachable
// endpoint later.
func validateGatewayExposure(s *v1alpha1.ClusterSpec, i int, gw v1alpha1.Gateway) []error {
	var errs []error
	where := fmt.Sprintf("gateway.gateways[%d]", i)

	switch gw.Exposure {
	case "", v1alpha1.ExposureLoadBalancer:
		if len(gw.NodeIPs) > 0 {
			errs = append(errs, fmt.Errorf(
				"%s.nodeIPs is set without exposure: node-ips; a load-balanced gateway takes "+
					"its address from the pool, so the list would be silently ignored", where))
		}
	case v1alpha1.ExposureNodeIPs:
		if gw.Address != "" {
			errs = append(errs, fmt.Errorf(
				"%s pins address %s and answers on node IPs; the two contradict -- the address "+
					"of a node-ips gateway is the nodes' own", where, gw.Address))
		}
		nodes := map[string]bool{}
		for _, n := range append(append([]v1alpha1.NodeSpec{}, s.Topology.Servers...), s.Topology.Agents...) {
			nodes[n.Host] = true
			if n.NodeIP != "" {
				nodes[n.NodeIP] = true
			}
		}
		for _, ip := range gw.NodeIPs {
			if !nodes[ip] {
				errs = append(errs, fmt.Errorf(
					"%s.nodeIPs lists %s, which is no node in this topology; an endpoint on an "+
						"address nothing holds is a DNS record pointing at silence", where, ip))
			}
		}
	default:
		errs = append(errs, fmt.Errorf(
			"%s.exposure %q is not loadBalancer or node-ips", where, gw.Exposure))
	}
	return errs
}
