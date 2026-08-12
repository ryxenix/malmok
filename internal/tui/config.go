package tui

import (
	"strconv"
	"strings"

	"platform.ryxen.dev/platformctl/api/v1alpha1"
	"platform.ryxen.dev/platformctl/internal/spec"
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
	s.Metadata.Profile = v1alpha1.ProfileName(c.Profile)

	// A VIP or DNS name, never a node's own address: otherwise adding a second
	// server later means re-joining every node (ADR-008).
	s.Topology.RegistrationAddress = c.Registration

	ssh := v1alpha1.SSHSpec{User: c.SSHUser, Port: atoiOr(c.SSHPort, 22)}
	s.Topology.Servers = mergeNodes(s.Topology.Servers, []string{c.Server}, v1alpha1.RoleServer, ssh)
	s.Topology.Agents = mergeNodes(s.Topology.Agents, c.Agents, v1alpha1.RoleAgent, ssh)

	s.Kubernetes.Version = c.Version
	s.Kubernetes.Dataplane.Preset = v1alpha1.DataplanePreset(c.Dataplane)
	s.Kubernetes.Dataplane.LoadBalancerPool = c.LBPool

	s.Storage.Driver = v1alpha1.StorageDriver(c.Storage)
	if c.Storage == string(v1alpha1.StorageNFS) {
		s.Storage.NFS = &v1alpha1.NFSSpec{Server: c.NFSServer, Path: c.NFSPath}
	} else {
		// An export left behind by a driver nobody uses reads as configuration
		// somebody meant.
		s.Storage.NFS = nil
	}

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

	s.PKI.Mode = v1alpha1.PKIMode(c.PKIMode)
	s.PKI.Domain = c.Domain
	s.Gateway.DomainSuffix = c.Domain

	// The mode decides which material block is legal, and the illegal one is
	// cleared rather than left inert. A private-ca document that still carries
	// an acme block is not harmless: the loader resolves every SourceRef it
	// finds, so a leftover token reference fails the run before it starts.
	//
	// The offline root's private key has no field here and never will: it must
	// not leave its custody (PF-706). Only the public root is referenced.
	s.PKI.PrivateCA, s.PKI.ACME = nil, nil
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

	case v1alpha1.PKIACMEDNS01, v1alpha1.PKIACMEHTTP01:
		if c.ACMEEmail != "" {
			s.PKI.ACME = &v1alpha1.ACMESpec{
				Email: c.ACMEEmail, DNSProvider: c.ACMEProvider,
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
		Dataplane:    string(s.Kubernetes.Dataplane.Preset),
		Storage:      string(s.Storage.Driver),
		PKIMode:      string(s.PKI.Mode),
		RegistryMode: string(s.Registry.Mode),
		LBPool:       s.Kubernetes.Dataplane.LoadBalancerPool,

		RegistryHost: s.Registry.SystemDefaultRegistry,
		RegistryUser: string(s.Registry.Username),
		RegistryPass: string(s.Registry.Password),
		RegistryCA:   string(s.Registry.CACert),

		SSHUser: "root",
		SSHPort: "22",
	}

	// `pki.mode: none` clears the domain on the way out, so the suffix is where
	// it survives; a document that has neither leaves the field empty rather
	// than showing a name it does not have.
	if c.Domain == "" {
		c.Domain = s.PKI.Domain
	}

	if len(s.Topology.Servers) > 0 {
		first := s.Topology.Servers[0]
		c.Server = first.Host
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
		c.ACMEEmail, c.ACMEProvider = a.Email, a.DNSProvider
		c.ACMEToken = string(a.APIToken)
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

// applyProfileDefaults moves a profile's baseline into the collected values, so
// the options screen shows what the profile chose instead of whatever was there
// before.
//
// Only the fields the wizard actually offers. Everything else the baseline
// fixes is applied by spec.ApplyProfile when the document is built, and showing
// a value the operator cannot change on a screen that looks editable would be
// worse than not showing it.
func (w *Wizard) applyProfileDefaults() {
	b, ok := spec.BaselineFor(v1alpha1.ProfileName(w.cfg.Profile))
	if !ok {
		return
	}
	w.cfg.Dataplane = string(b.Dataplane)
	w.cfg.Storage = string(b.Storage)
	w.cfg.PKIMode = string(b.PKIMode)
	w.cfg.RegistryMode = string(b.RegistryMode)
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
