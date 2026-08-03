package tui

import (
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
	s := v1alpha1.ClusterSpec{
		APIVersion: v1alpha1.APIVersion,
		Kind:       v1alpha1.KindSpec,
		Metadata: v1alpha1.Metadata{
			Name:    "cluster",
			Profile: v1alpha1.ProfileName(c.Profile),
		},
		Topology: v1alpha1.TopologySpec{
			// A VIP or DNS name, never a node's own address: otherwise adding a
			// second server later means re-joining every node (ADR-008).
			RegistrationAddress: c.Registration,
			Servers: []v1alpha1.NodeSpec{{
				Host: c.Server, Role: v1alpha1.RoleServer,
				SSH: v1alpha1.SSHSpec{User: c.SSHUser, Port: atoiOr(c.SSHPort, 22)},
			}},
		},
		Kubernetes: v1alpha1.KubernetesSpec{
			Version: c.Version,
			Dataplane: v1alpha1.DataplaneSpec{
				Preset: v1alpha1.DataplanePreset(c.Dataplane),
			},
		},
		PKI:     v1alpha1.PKISpec{Domain: c.Domain},
		Storage: v1alpha1.StorageSpec{Driver: v1alpha1.StorageDriver(c.Storage)},
		Gateway: v1alpha1.GatewaySpec{DomainSuffix: c.Domain},
	}

	s.Kubernetes.Dataplane.LoadBalancerPool = c.LBPool

	if c.Storage == string(v1alpha1.StorageNFS) {
		s.Storage.NFS = &v1alpha1.NFSSpec{Server: c.NFSServer, Path: c.NFSPath}
	}

	if c.ProxyHTTP != "" || c.ProxyHTTPS != "" {
		s.Network.Proxy = &v1alpha1.ProxySpec{
			HTTP: c.ProxyHTTP, HTTPS: c.ProxyHTTPS, NoProxy: c.NoProxy,
		}
	}

	if c.RegistryHost != "" {
		s.Registry.SystemDefaultRegistry = c.RegistryHost
		s.Registry.Username = v1alpha1.SourceRef(c.RegistryUser)
		s.Registry.Password = v1alpha1.SourceRef(c.RegistryPass)
		s.Registry.CACert = v1alpha1.SourceRef(c.RegistryCA)
	}

	// The offline root's private key has no field here and never will: it must
	// not leave its custody (PF-706). Only the public root is referenced.
	if c.CARoot != "" || c.CAIntermediate != "" || c.CAKey != "" {
		s.PKI.PrivateCA = &v1alpha1.PrivateCASpec{
			RootCert:         v1alpha1.SourceRef(c.CARoot),
			IntermediateCert: v1alpha1.SourceRef(c.CAIntermediate),
			IntermediateKey:  v1alpha1.SourceRef(c.CAKey),
		}
	}
	if c.ACMEEmail != "" {
		s.PKI.ACME = &v1alpha1.ACMESpec{
			Email: c.ACMEEmail, DNSProvider: c.ACMEProvider,
			APIToken: v1alpha1.SourceRef(c.ACMEToken),
		}
	}

	for _, a := range c.Agents {
		s.Topology.Agents = append(s.Topology.Agents, v1alpha1.NodeSpec{
			Host: a, Role: v1alpha1.RoleAgent,
			SSH: v1alpha1.SSHSpec{User: c.SSHUser, Port: atoiOr(c.SSHPort, 22)},
		})
	}
	return s
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

// validateConfig returns the document's problems as display lines.
//
// Reported on the summary screen rather than at install time: a malformed
// field is worth saying while the operator is still looking at the screen that
// produced it.
func (w *Wizard) validateConfig() []string {
	doc, _, err := w.cfg.Document()
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
