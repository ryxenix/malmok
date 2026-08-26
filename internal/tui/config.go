package tui

import (
	"strconv"
	"strings"

	"github.com/ryxen/malmok/api/v1alpha1"
	"github.com/ryxen/malmok/internal/exec"
	"github.com/ryxen/malmok/internal/spec"
)

// The wizard collects values; internal/spec decides what they mean. Building
// the document here rather than in the command keeps the screens and the
// validation on the same side of the boundary, so a field the operator can type
// is always a field the validator has an opinion about.

// ToSpec assembles what the screens collected into a ClusterSpec.
//
// It is deliberately partial. The wizard does not ask about PKI material,
// registry credentials or gateway listeners, so those are left empty and the
// validator says so — which is more useful than the wizard inventing values
// nobody chose.
func (c Config) ToSpec() v1alpha1.ClusterSpec {
	var s v1alpha1.ClusterSpec
	c.ApplyTo(&s)
	return s
}

// ApplyTo writes the fields the wizard owns onto a document and leaves the rest
// of it alone.
//
// This is the whole difference between building and editing. A build starts
// from nothing, so replacing the document wholesale and writing it are the same
// act. Editing is not: the wizard asks about a subset of the schema — there is
// no screen for gateway listeners, for etcd snapshots to S3, or for the GitOps
// repository — and a document rebuilt from the answers would silently drop
// every one of them. What the operator was never shown, they did not agree to
// delete.
func (c Config) ApplyTo(s *v1alpha1.ClusterSpec) {
	s.APIVersion, s.Kind = v1alpha1.APIVersion, v1alpha1.KindSpec
	if s.Metadata.Name == "" {
		s.Metadata.Name = "cluster"
	}
	// Derived, not chosen. Six canned combinations cannot cover the
	// combinations people actually have, and asking for one first made every
	// screen after it an override of a decision nobody wanted to make.
	s.Metadata.Profile = c.MatchedProfile()

	// A VIP or DNS name, never a node's own address (ADR-008). Left empty, it
	// falls to the first server's own address with the trade stated: IDC and
	// air-gapped network policy frequently forbids a second IP on the segment,
	// and a DNS name needs a zone somebody may write to. A site with neither
	// still deserves a cluster; promoting it to HA later means re-joining.
	s.Topology.RegistrationAddress = strings.TrimSpace(c.Registration)
	s.Topology.AcceptNodeRegistration = false
	if s.Topology.RegistrationAddress == "" && c.Server != "" {
		addr := c.Server
		// The document records the routable address, not the literal `local`.
		if len(s.Topology.Servers) > 0 && s.Topology.Servers[0].NodeIP != "" {
			addr = s.Topology.Servers[0].NodeIP
		}
		s.Topology.RegistrationAddress = addr
		s.Topology.AcceptNodeRegistration = true
	}

	ssh := v1alpha1.SSHSpec{User: c.SSHUser, Port: atoiOr(c.SSHPort, 22)}
	// A node this machine is, is not a node this machine dials, so it carries
	// no login. The screens already hide the SSH fields there; writing them
	// anyway did more than clutter the document -- the kubeconfig step reads
	// that account to decide whose copy to make, saw the seeded "root", and
	// concluded the operator already had it. On a build run under sudo the
	// person at the keyboard is $SUDO_USER, which is exactly what the step
	// falls back to when the document names nobody. An IDC build finished
	// with no kubeconfig for the account that ran it because of this line.
	local := ssh
	if c.Local {
		local = v1alpha1.SSHSpec{}
	}
	s.Topology.Servers = mergeNodes(s.Topology.Servers, []string{c.Server}, v1alpha1.RoleServer, local)
	s.Topology.Agents = mergeNodes(s.Topology.Agents, c.Agents, v1alpha1.RoleAgent, ssh)

	s.Kubernetes.Version = c.Version
	s.Kubernetes.Dataplane.Preset = v1alpha1.DataplanePreset(c.Dataplane)
	s.Kubernetes.Dataplane.LoadBalancerPool = c.LBPool
	s.Kubernetes.Dataplane.Fallback = v1alpha1.DataplanePreset(c.Fallback)
	s.OS.Family = v1alpha1.OSFamily(c.OSFamily)
	s.Network.Routing = v1alpha1.RoutingMode(c.Routing)

	s.Storage.Driver = v1alpha1.StorageDriver(c.Storage)
	if c.Storage == string(v1alpha1.StorageNFS) {
		s.Storage.NFS = &v1alpha1.NFSSpec{Server: c.NFSServer, Path: c.NFSPath}
	} else {
		// An export left behind by a driver nobody uses reads as configuration
		// somebody meant.
		s.Storage.NFS = nil
	}

	s.Network.Mode = v1alpha1.NetworkMode(c.NetworkMode)
	// A pointer, so "not stated" and "off" stay different: the profile decides
	// when the document is silent, and writing false would freeze an answer the
	// profile is meant to give.
	if c.Encrypt {
		yes := true
		s.Network.EncryptNodeTraffic = &yes
	} else {
		s.Network.EncryptNodeTraffic = nil
	}
	s.Kubernetes.Dataplane.DowngradePolicy = v1alpha1.DowngradePolicy(c.DowngradePolicy)

	if c.ProxyHTTP != "" || c.ProxyHTTPS != "" {
		s.Network.Proxy = &v1alpha1.ProxySpec{
			HTTP: c.ProxyHTTP, HTTPS: c.ProxyHTTPS, NoProxy: c.NoProxy,
		}
	} else {
		s.Network.Proxy = nil
	}

	s.Registry.Mode = v1alpha1.RegistryMode(c.RegistryMode)
	s.Registry.SystemDefaultRegistry = c.RegistryHost
	if c.RegistryHost == "" {
		// No registry, so no credentials for one. A reference left pointing at
		// a host that is not in the document resolves to a file or a variable
		// nobody set, and the run fails before it reaches a node.
		s.Registry.Username, s.Registry.Password, s.Registry.CACert = "", "", ""
	} else {
		s.Registry.Username = v1alpha1.SourceRef(c.RegistryUser)
		s.Registry.Password = v1alpha1.SourceRef(c.RegistryPass)
		s.Registry.CACert = v1alpha1.SourceRef(c.RegistryCA)
	}
	s.Registry.Bundle = c.RegistryBundle
	// The same pointer discipline as node encryption: stated only when it says
	// something. `insecure: false` written into every document would make the
	// one that means it indistinguishable from the ones that never thought
	// about it.
	if c.RegistryInsecure {
		yes := true
		s.Registry.Insecure = &yes
	} else {
		s.Registry.Insecure = nil
	}

	s.PKI.Mode = v1alpha1.PKIMode(c.PKIMode)
	s.PKI.Domain = c.Domain
	s.Gateway.DomainSuffix = c.Domain

	// The gateway the screen asked for, written onto whatever the document
	// already had.
	//
	// Edited rather than rebuilt: a document may name listeners, TLS
	// references, zones and namespaces this wizard has no screen for, and
	// replacing it with three fields would delete them silently -- the one
	// thing ApplyTo exists to prevent. What the screen owns is the exposure,
	// the name and the pinned address; everything else stays as written.
	switch {
	case c.Exposure == "":
		// The screen was never visited (a document opened straight into
		// another flow). Nothing to say, nothing to change.

	case c.Exposure == "none":
		// An explicit answer, so an explicit removal: the operator said this
		// cluster answers on nothing.
		s.Gateway.Gateways = nil

	default:
		name := c.GatewayName
		if name == "" {
			name = "public"
		}
		existing := len(s.Gateway.Gateways) > 0
		gw := v1alpha1.Gateway{Name: name, RouteNamespaces: "all"}
		if existing {
			gw = s.Gateway.Gateways[0]
			gw.Name = name
		}
		// Cluster-wide rather than per gateway: Cilium's ALPN setting is a
		// chart value, so it is written where it takes effect. Only when true
		// -- an explicit false in every document that never thought about it
		// would be noise.
		if c.HTTP2 {
			on := true
			s.Gateway.HTTP2 = &on
		} else {
			s.Gateway.HTTP2 = nil
		}
		if c.Exposure == "node-ips" {
			gw.Exposure, gw.Address = v1alpha1.ExposureNodeIPs, ""
		} else {
			// Pinned rather than allocated: the DNS record is requested
			// before the install, so the address has to be decided (PF-612).
			gw.Exposure, gw.Address = "", c.GatewayAddress
		}
		// Listeners are written only for a gateway this screen is creating. A
		// document that names a gateway with none has a problem, and filling
		// it in here would hide it from the validation that exists to report
		// it -- the wizard repairing what it cannot see is worse than the
		// wizard leaving it alone.
		if !existing {
			gw.Listeners = []v1alpha1.ListenerSpec{
				{Name: "http", Protocol: v1alpha1.ListenerHTTP, Port: 80},
			}
			// HTTPS only where a certificate can exist. A listener that
			// terminates TLS with nothing to terminate it with resets every
			// handshake, and the operator hears about it from a browser.
			if c.PKIMode != "" && c.PKIMode != string(v1alpha1.PKINone) && c.Domain != "" {
				gw.Listeners = append(gw.Listeners, v1alpha1.ListenerSpec{
					Name: "https", Protocol: v1alpha1.ListenerHTTPS, Port: 443,
					Hostname: "*." + c.Domain,
				})
			}
		}
		if len(s.Gateway.Gateways) > 0 {
			s.Gateway.Gateways[0] = gw
		} else {
			s.Gateway.Gateways = []v1alpha1.Gateway{gw}
		}
	}

	// The mode decides which material block is legal, and the illegal one is
	// cleared rather than left inert. A private-ca document that still carries
	// an acme block is not harmless: the loader resolves every SourceRef it
	// finds, so a leftover token reference fails the run before it starts.
	//
	// The offline root's private key has no field here and never will: it must
	// not leave its custody (PF-706). Only the public root is referenced.
	s.PKI.PrivateCA, s.PKI.ACME, s.PKI.BYOCert = nil, nil, nil
	switch v1alpha1.PKIMode(c.PKIMode) {
	case v1alpha1.PKINone:
		// Nothing is issued, so nothing is carried. A domain left in the
		// document here would be a name the cluster claims and never serves.
		s.PKI.Domain = ""

	case v1alpha1.PKIPrivateCA:
		if c.CARoot != "" || c.CAIntermediate != "" || c.CAKey != "" {
			s.PKI.PrivateCA = &v1alpha1.PrivateCASpec{
				RootCert:         v1alpha1.SourceRef(c.CARoot),
				IntermediateCert: v1alpha1.SourceRef(c.CAIntermediate),
				IntermediateKey:  v1alpha1.SourceRef(c.CAKey),
			}
		}
		// Stated only when on: the document's zero value already means "no
		// bundle", and l2-pki reads the pointer.
		if c.TrustBundle {
			yes := true
			s.PKI.Trust.ClusterBundle = &yes
		} else {
			s.PKI.Trust.ClusterBundle = nil
		}

	case v1alpha1.PKIBYOCert:
		if c.BYOCert != "" || c.BYOKey != "" {
			s.PKI.BYOCert = &v1alpha1.BYOCertSpec{
				Cert:   v1alpha1.SourceRef(c.BYOCert),
				Key:    v1alpha1.SourceRef(c.BYOKey),
				CACert: v1alpha1.SourceRef(c.BYOCA),
			}
		}

	case v1alpha1.PKIACMEDNS01, v1alpha1.PKIACMEHTTP01:
		if c.ACMEEmail != "" {
			s.PKI.ACME = &v1alpha1.ACMESpec{
				Email: c.ACMEEmail, Server: c.ACMEServer, DNSProvider: c.ACMEProvider,
				APIToken: v1alpha1.SourceRef(c.ACMEToken),
			}
		}
	}
}

// FromSpec fills the wizard's values from a document.
//
// The inverse of ApplyTo for the fields the wizard owns, and only those. What
// it does not read stays in the document and comes back out untouched when the
// document is written, which is what makes editing safe.
//
// Lang and SSHPassword are not read: neither is in a document. The language is
// a property of this session and the password is deliberately never serialised.
func FromSpec(s v1alpha1.ClusterSpec) Config {
	c := Config{
		Registration: s.Topology.RegistrationAddress,
		Version:      s.Kubernetes.Version,
		Domain:       s.Gateway.DomainSuffix,
		Profile:      string(s.Metadata.Profile),
		OSFamily:     string(s.OS.Family),
		Routing:      string(s.Network.Routing),
		Fallback:     string(s.Kubernetes.Dataplane.Fallback),
		GitOpsSource: string(s.Platform.GitOps.Source),
		Dataplane:    string(s.Kubernetes.Dataplane.Preset),
		Storage:      string(s.Storage.Driver),
		PKIMode:      string(s.PKI.Mode),
		RegistryMode: string(s.Registry.Mode),
		NetworkMode:  string(s.Network.Mode),
		Encrypt:      s.Network.EncryptNodeTraffic != nil && *s.Network.EncryptNodeTraffic,

		DowngradePolicy: string(s.Kubernetes.Dataplane.DowngradePolicy),
		LBPool:          s.Kubernetes.Dataplane.LoadBalancerPool,
		Exposure:        exposureOf(s),
		HTTP2:           s.Gateway.HTTP2 != nil && *s.Gateway.HTTP2,
		GatewayName:     gatewayNameOf(s),
		GatewayAddress:  gatewayAddressOf(s),

		RegistryHost:     s.Registry.SystemDefaultRegistry,
		RegistryUser:     string(s.Registry.Username),
		RegistryPass:     string(s.Registry.Password),
		RegistryCA:       string(s.Registry.CACert),
		RegistryBundle:   s.Registry.Bundle,
		RegistryInsecure: s.Registry.Insecure != nil && *s.Registry.Insecure,
		TrustBundle:      s.PKI.Trust.ClusterBundle != nil && *s.PKI.Trust.ClusterBundle,

		SSHUser: "root",
		SSHPort: "22",
	}

	// `pki.mode: none` clears the domain on the way out, so the suffix is where
	// it survives; a document that has neither leaves the field empty rather
	// than showing a name it does not have.
	if c.Domain == "" {
		c.Domain = s.PKI.Domain
	}

	if s.Topology.AcceptNodeRegistration {
		c.Registration = ""
	}
	if len(s.Topology.Servers) > 0 {
		first := s.Topology.Servers[0]
		c.Server = first.Host
		// Which branch the screens are in, read back the same way the runner
		// decides it. A document that names this machine opens on the address
		// chooser rather than on a field asking for an address it already has.
		c.Local = exec.NodeIsLocal(first)
		if first.SSH.User != "" {
			c.SSHUser = first.SSH.User
		}
		if first.SSH.Port > 0 {
			c.SSHPort = strconv.Itoa(first.SSH.Port)
		}
	}
	// Only the first server is shown, because the wizard has one field for it.
	// The rest are still in the document and ApplyTo leaves them there.
	for _, a := range s.Topology.Agents {
		c.Agents = append(c.Agents, a.Host)
	}

	if p := s.Network.Proxy; p != nil {
		c.ProxyHTTP, c.ProxyHTTPS, c.NoProxy = p.HTTP, p.HTTPS, p.NoProxy
	}
	if n := s.Storage.NFS; n != nil {
		c.NFSServer, c.NFSPath = n.Server, n.Path
	}
	if ca := s.PKI.PrivateCA; ca != nil {
		c.CARoot = string(ca.RootCert)
		c.CAIntermediate = string(ca.IntermediateCert)
		c.CAKey = string(ca.IntermediateKey)
	}
	if a := s.PKI.ACME; a != nil {
		c.ACMEEmail, c.ACMEServer, c.ACMEProvider = a.Email, a.Server, a.DNSProvider
		c.ACMEToken = string(a.APIToken)
	}
	if b := s.PKI.BYOCert; b != nil {
		c.BYOCert, c.BYOKey, c.BYOCA = string(b.Cert), string(b.Key), string(b.CACert)
	}
	return c
}

// mergeNodes updates the hosts of a node list without discarding what the
// wizard has no screen for.
//
// A node carries more than its address: an SSH private key reference, a become
// password, labels and taints. Rebuilding the list from the hosts alone would
// drop all of it. An entry is matched to its old self by host, and failing that
// by position — so renaming a node in place keeps its credentials, which is
// what renaming a node in place means.
func mergeNodes(existing []v1alpha1.NodeSpec, hosts []string,
	role v1alpha1.NodeRole, ssh v1alpha1.SSHSpec) []v1alpha1.NodeSpec {

	byHost := make(map[string]v1alpha1.NodeSpec, len(existing))
	for _, n := range existing {
		byHost[n.Host] = n
	}

	out := make([]v1alpha1.NodeSpec, 0, len(hosts))
	for i, host := range hosts {
		if host == "" {
			continue
		}
		node, ok := byHost[host]
		if !ok && i < len(existing) {
			node = existing[i]
		}
		node.Host, node.Role = host, role
		node.SSH.User, node.SSH.Port = ssh.User, ssh.Port
		out = append(out, node)
	}
	return out
}

// Document builds and resolves the specification, returning it together with
// the fields the profile supplied.
func (c Config) Document() (*spec.Document, []string, error) {
	doc, err := spec.Parse(nil)
	if err != nil {
		// Parse(nil) cannot succeed; build the document directly instead.
		doc = &spec.Document{}
	}
	doc.Spec = c.ToSpec()
	applied, err := doc.ApplyProfile()
	return doc, applied, err
}

// documentUnderEdit is the document the current flow would produce.
//
// In the settings flow that is the loaded document with the wizard's fields
// applied over it -- not a document rebuilt from the fields. Validation has to
// be about the file that is going to be written: validating a reconstruction
// would report problems the real document does not have, and, worse, miss the
// ones it does, because the parts no screen shows are exactly the parts the
// reconstruction is missing.
func (w *Wizard) documentUnderEdit() (*spec.Document, []string, error) {
	if w.mode != modeSettings {
		return w.cfg.Document()
	}
	// The profile is applied for the validation and not for the write: a
	// baseline belongs to the profile, and baking it into the file would turn a
	// default that follows the profile into a value frozen at edit time.
	edited := w.doc
	w.cfg.ApplyTo(&edited)
	doc := &spec.Document{Spec: edited}
	applied, err := doc.ApplyProfile()
	return doc, applied, err
}

// validateConfig returns the document's problems as display lines.
//
// Reported on the summary screen rather than at install time: a malformed
// field is worth saying while the operator is still looking at the screen that
// produced it.
func (w *Wizard) validateConfig() []string {
	doc, _, err := w.documentUnderEdit()
	if err != nil {
		return []string{err.Error()}
	}
	if err := doc.Validate(false); err != nil {
		var out []string
		for _, line := range strings.Split(err.Error(), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				out = append(out, line)
			}
		}
		return out
	}
	return nil
}

func atoiOr(s string, def int) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return def
		}
		n = n*10 + int(r-'0')
	}
	if n == 0 {
		return def
	}
	return n
}

// MatchedProfile names the validated baseline this configuration equals.
//
// `custom` when it equals none of them, which is a true and useful statement:
// Tier-3 means the combination is not one CI exercises, and the audit report
// says so rather than claiming a profile the document does not actually match.
func (c Config) MatchedProfile() v1alpha1.ProfileName {
	return spec.Match(spec.Baseline{
		OSFamily:           v1alpha1.OSFamily(c.OSFamily),
		NetworkMode:        v1alpha1.NetworkMode(c.NetworkMode),
		Routing:            v1alpha1.RoutingMode(c.Routing),
		Dataplane:          v1alpha1.DataplanePreset(c.Dataplane),
		Fallback:           v1alpha1.DataplanePreset(c.Fallback),
		DowngradePolicy:    v1alpha1.DowngradePolicy(c.DowngradePolicy),
		PKIMode:            v1alpha1.PKIMode(c.PKIMode),
		Storage:            v1alpha1.StorageDriver(c.Storage),
		RegistryMode:       v1alpha1.RegistryMode(c.RegistryMode),
		GitOpsSource:       v1alpha1.GitOpsSourceType(c.GitOpsSource),
		EncryptNodeTraffic: c.Encrypt,

		// Not composed anywhere: it is a profile's own strictness about
		// requiring a DNS record before install (PF-612), not a setting. A
		// composition that matches a profile in every other respect inherits
		// it, which is what makes the match meaningful.
		RequirePinnedGatewayAddress: c.PinnedGateway,
	})
}

// exposureOf reads back what a document says about being reached, so the
// screen opens on the answer already written rather than on the default.
func exposureOf(s v1alpha1.ClusterSpec) string {
	if len(s.Gateway.Gateways) == 0 {
		return "none"
	}
	if s.Gateway.Gateways[0].Exposure == v1alpha1.ExposureNodeIPs {
		return "node-ips"
	}
	return "lb-pool"
}

func gatewayNameOf(s v1alpha1.ClusterSpec) string {
	if len(s.Gateway.Gateways) == 0 {
		return "public"
	}
	return s.Gateway.Gateways[0].Name
}

func gatewayAddressOf(s v1alpha1.ClusterSpec) string {
	if len(s.Gateway.Gateways) == 0 {
		return ""
	}
	return s.Gateway.Gateways[0].Address
}
