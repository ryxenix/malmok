package main

import (
	"context"
	"crypto/x509"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/ryxen/malmok/api/v1alpha1"
	"github.com/ryxen/malmok/internal/catalogue"
	"github.com/ryxen/malmok/internal/cert"
	"github.com/ryxen/malmok/internal/engine"
	"github.com/ryxen/malmok/internal/exec"
	"github.com/ryxen/malmok/internal/gateway"
	"github.com/ryxen/malmok/internal/preflight"
	"github.com/ryxen/malmok/internal/spec"
	"github.com/ryxen/malmok/internal/verify"
)

// nodeSession is everything a real run needs that the engine does not own: the
// connections, the material read out of the document, and the phase list.
//
// It is separate from the engine because the engine must stay runnable from a
// cluster.yaml alone. Resolving a SourceRef needs the document's directory and
// its secret policy, and opening an SSH connection needs credentials -- neither
// is the engine's business.
type nodeSession struct {
	doc      *spec.Document
	runners  catalogue.Runners
	material catalogue.Material
	closers  []func() error
}

// close releases every connection opened for the run.
func (b *nodeSession) close() {
	for _, c := range b.closers {
		_ = c()
	}
}

// connect opens a shell on every node the document names.
//
// All of them up front, and it fails if any is unreachable: a build that
// discovers on its third phase that it cannot reach a node has already changed
// the first two, and unwinding that is worse than not starting.
func (o *preflightOptions) connect(ctx context.Context, doc *spec.Document) (*nodeSession, error) {
	b := &nodeSession{doc: doc}

	s := doc.Spec
	nodes := append(append([]v1alpha1.NodeSpec{}, s.Topology.Servers...), s.Topology.Agents...)
	if len(nodes) == 0 {
		return nil, fmt.Errorf("the document names no node")
	}

	b.runners.ByHost = map[string]exec.Runner{}
	for _, n := range nodes {
		runner, err := o.dial(ctx, doc, n)
		if err != nil {
			b.close()
			return nil, fmt.Errorf("%s: %w", n.Host, err)
		}
		b.runners.ByHost[n.Host] = runner
		b.closers = append(b.closers, runner.Close)
	}
	b.runners.Control = b.runners.ByHost[s.Topology.Servers[0].Host]

	if err := o.resolveMaterial(doc, &b.material); err != nil {
		b.close()
		return nil, err
	}
	return b, nil
}

// dialer hands the preflight session the connections the build already holds.
//
// Preflight would otherwise open a second connection to every node, which costs
// a handshake each and, more to the point, means the checks could succeed on a
// connection the build then fails to make.
func (b *nodeSession) dialer() func(context.Context, exec.SSHConfig) (exec.Runner, error) {
	return func(_ context.Context, cfg exec.SSHConfig) (exec.Runner, error) {
		if r, ok := b.runners.ByHost[cfg.Host]; ok {
			// Not closed by the caller: the build owns these for the whole run,
			// and a preflight that closed them would leave the phases with
			// nothing to talk to.
			return noCloseRunner{r}, nil
		}
		return nil, fmt.Errorf("no connection was opened to %s", cfg.Host)
	}
}

// noCloseRunner lends a connection without giving up ownership of it.
type noCloseRunner struct{ exec.Runner }

func (noCloseRunner) Close() error { return nil }

// dial opens one connection with the document's credentials.
func (o *preflightOptions) dial(ctx context.Context, doc *spec.Document, n v1alpha1.NodeSpec) (exec.Runner, error) {
	cfg := exec.FromSpec(n)
	cfg.InsecureSkipHostKeyCheck = o.insecureHost

	field := "topology." + n.Host + ".ssh"
	key, err := doc.Resolve(field+".privateKey", n.SSH.PrivateKey, true, o.allowLiteral)
	if err != nil {
		return nil, err
	}
	cfg.PrivateKey = key

	pw, err := doc.Resolve(field+".password", n.SSH.Password, true, o.allowLiteral)
	if err != nil {
		return nil, err
	}
	cfg.Password = string(pw)

	runner, err := exec.Connect(ctx, cfg)
	if err != nil {
		return nil, err
	}

	// Every phase reads files an unprivileged account cannot and writes files
	// only root may write. Without elevation the build fails on its first step
	// rather than on its last.
	if n.SSH.Become == nil || *n.SSH.Become {
		become, err := doc.Resolve(field+".becomePassword", n.SSH.BecomePassword, true, o.allowLiteral)
		if err != nil {
			runner.Close()
			return nil, err
		}
		if len(become) == 0 {
			become = pw
		}
		// Proved rather than assumed: an account that cannot elevate produces a
		// node that reports "no" to every privileged question, which reads as a
		// machine that cannot run the dataplane.
		elevated, err := exec.Elevate(ctx, runner, string(become))
		if err != nil {
			runner.Close()
			return nil, err
		}
		return elevated, nil
	}
	return runner, nil
}

// resolveMaterial reads what the phases need out of the document.
func (o *preflightOptions) resolveMaterial(doc *spec.Document, m *catalogue.Material) error {
	s := doc.Spec
	var err error

	if ca := s.PKI.PrivateCA; ca != nil {
		root, err := doc.Resolve("pki.privateCA.rootCert", ca.RootCert, false, o.allowLiteral)
		if err != nil {
			return err
		}
		m.Trust.CABundle = root
	}
	if len(m.Trust.CABundle) == 0 {
		// The registry's CA also has to reach the node trust store, or every
		// image pull fails with an opaque x509 error.
		ca, err := doc.Resolve("registry.caCert", s.Registry.CACert, false, o.allowLiteral)
		if err != nil {
			return err
		}
		m.Trust.CABundle = ca
	}

	// l2-pki signs with the intermediate. The root private key is never read:
	// PF-706 refuses material that contains one, and the offline root must stay
	// in its custody.
	if ca := s.PKI.PrivateCA; ca != nil {
		if m.PKI.RootCert, err = doc.Resolve("pki.privateCA.rootCert", ca.RootCert, false, o.allowLiteral); err != nil {
			return err
		}
		if m.PKI.IntermediateCert, err = doc.Resolve(
			"pki.privateCA.intermediateCert", ca.IntermediateCert, false, o.allowLiteral); err != nil {
			return err
		}
		if m.PKI.IntermediateKey, err = doc.Resolve(
			"pki.privateCA.intermediateKey", ca.IntermediateKey, true, o.allowLiteral); err != nil {
			return err
		}
	}
	if s.PKI.ACME != nil {
		if m.PKI.ACMEToken, err = doc.Resolve(
			"pki.acme.apiToken", s.PKI.ACME.APIToken, true, o.allowLiteral); err != nil {
			return err
		}
	}

	user, err := doc.Resolve("registry.username", s.Registry.Username, true, o.allowLiteral)
	if err != nil {
		return err
	}
	pass, err := doc.Resolve("registry.password", s.Registry.Password, true, o.allowLiteral)
	if err != nil {
		return err
	}
	m.Trust.RegistryUser, m.Trust.RegistryPass = string(user), string(pass)
	m.Trust.RegistryHost = s.Registry.SystemDefaultRegistry

	bundles, err := o.assembleBundles(doc)
	if err != nil {
		return err
	}
	m.Bundles = bundles
	return nil
}

// assembleBundles builds a certificate bundle for every listener that supplies
// its own material.
//
// Assembled here rather than in the gateway phase because reading the files
// needs the document's directory, and because a bundle that will not assemble
// should stop the run before anything is installed.
func (o *preflightOptions) assembleBundles(doc *spec.Document) (map[string]*cert.Bundle, error) {
	refs := gateway.SecretRefs(doc.Spec)
	if len(refs) == 0 {
		return nil, nil
	}

	byRef := map[string]*v1alpha1.ListenerTLS{}
	for _, gw := range doc.Spec.Gateway.Gateways {
		for i := range gw.Listeners {
			l := gw.Listeners[i]
			if l.TLS != nil && l.TLS.SecretRef != "" {
				byRef[l.TLS.SecretRef] = l.TLS
			}
		}
	}

	out := map[string]*cert.Bundle{}
	for _, ref := range refs {
		tls := byRef[ref]
		if tls == nil && doc.Spec.PKI.Mode == v1alpha1.PKIBYOCert && doc.Spec.PKI.BYOCert != nil {
			// The listener named no material and the cluster did: byo-cert
			// means the operator handed over one certificate for the service
			// domain, and a listener that omits the tls block inherits it.
			// Without this the inheritance the schema documents produced
			// nothing at all, and the listener served no certificate.
			byo := doc.Spec.PKI.BYOCert
			tls = &v1alpha1.ListenerTLS{
				Source:    v1alpha1.TLSFromBYO,
				SecretRef: ref,
				BYO: &v1alpha1.BYOMaterial{
					// The CA travels as the chain: a private CA's own root is
					// what in-cluster clients verify against, and the assembler
					// treats chain input as additive.
					Cert: byo.Cert, Key: byo.Key, Chain: byo.CACert,
				},
			}
		}
		if tls == nil || tls.Source != v1alpha1.TLSFromBYO {
			// A listener whose certificate is issued in-cluster has nothing to
			// assemble here; l2-pki will own it.
			continue
		}
		files, err := o.certFiles(doc, ref, tls)
		if err != nil {
			return nil, fmt.Errorf("gateway %s: %w", ref, err)
		}
		pass, err := doc.Resolve("gateway."+ref+".tls.byo.passphrase", tls.BYO.Passphrase, true, o.allowLiteral)
		if err != nil {
			return nil, err
		}

		res := cert.Assemble(cert.Input{
			Files:             files,
			Passphrase:        pass,
			Hostnames:         preflight.ListenerHostnames(doc.Spec, ref),
			ExpiryWarningDays: tls.BYO.ExpiryWarningDays,
			IncludeRoot:       tls.BYO.IncludeRoot != nil && *tls.BYO.IncludeRoot,
		})
		if res.Bundle == nil {
			var why []string
			for _, f := range res.Findings {
				if f.Blocking() {
					why = append(why, f.ID+": "+f.Detail)
				}
			}
			return nil, fmt.Errorf("the certificate for %s cannot be assembled:\n  %s",
				ref, strings.Join(why, "\n  "))
		}
		out[ref] = res.Bundle
	}
	return out, nil
}

// certFiles gathers one bundle's material.
func (o *preflightOptions) certFiles(doc *spec.Document, ref string, tls *v1alpha1.ListenerTLS) ([]cert.File, error) {
	if tls.BYO == nil {
		return nil, fmt.Errorf("tls.source is byo and tls.byo is absent")
	}
	field := "gateway." + ref + ".tls.byo"

	if tls.BYO.Dir != "" {
		return cert.LoadDir(localPath(string(tls.BYO.Dir), doc.Dir()))
	}

	var files []cert.File
	for _, f := range []struct {
		name   string
		ref    v1alpha1.SourceRef
		secret bool
	}{
		{"cert", tls.BYO.Cert, false},
		{"key", tls.BYO.Key, true},
		{"chain", tls.BYO.Chain, false},
	} {
		if f.ref == "" {
			continue
		}
		body, err := doc.Resolve(field+"."+f.name, f.ref, f.secret, o.allowLiteral)
		if err != nil {
			return nil, err
		}
		files = append(files, cert.File{Name: field + "." + f.name, Data: body})
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("neither dir nor cert/key is set")
	}
	return files, nil
}

// localPath turns a file:// reference into a path relative to the document.
func localPath(raw, dir string) string {
	raw = strings.TrimPrefix(raw, "file://")
	if strings.HasPrefix(raw, "/") || dir == "" {
		return raw
	}
	return dir + "/" + raw
}

// ---------------------------------------------------------------------------
// verify
// ---------------------------------------------------------------------------

// verifyGateways runs the wire checks against every gateway the document pins.
//
// It is the last phase for a reason: PF-9xx validates the files and this
// validates what a client receives, and only one of those can be done before
// the cluster exists.
func verifyGateways(ctx context.Context, doc *spec.Document, m catalogue.Material) []preflight.ProbeResult {
	var out []preflight.ProbeResult

	roots := x509.NewCertPool()
	for _, b := range m.Bundles {
		if len(b.CAPEM) > 0 {
			roots.AppendCertsFromPEM(b.CAPEM)
		}
	}
	if len(m.Trust.CABundle) > 0 {
		roots.AppendCertsFromPEM(m.Trust.CABundle)
	}

	opts := verify.Options{Timeout: 10 * time.Second}
	if len(m.Bundles) > 0 || len(m.Trust.CABundle) > 0 {
		// A private CA is not in any public store, so verifying against the
		// host's alone would report every private-CA cluster as untrusted.
		opts.Roots = roots
	}

	for _, gw := range doc.Spec.Gateway.Gateways {
		addr := strings.TrimSpace(gw.Address)
		if addr == "" {
			// Nothing pinned means nothing to connect to from here: PF-612
			// already warned, and guessing an address would verify somebody
			// else's endpoint.
			continue
		}
		for _, l := range gw.Listeners {
			if l.Protocol != v1alpha1.ListenerHTTPS && l.Protocol != v1alpha1.ListenerTLSPassthrough {
				continue
			}
			port := l.Port
			if port == 0 {
				port = 443
			}
			var names []string
			if l.Hostname != "" {
				names = append(names, l.Hostname)
			}
			for _, r := range verify.Verify(ctx,
				verify.Target{Address: addr, Port: port, Hostnames: names}, opts) {
				r.Node = net.JoinHostPort(addr, fmt.Sprint(port))
				out = append(out, r)
			}
		}
	}
	return out
}

// phaseGrades summarises what a build will do, for the confirmation an operator
// gives before it starts.
func phaseGrades(phases []engine.Phase) string {
	var parts []string
	for _, p := range phases {
		parts = append(parts, fmt.Sprintf("  %-18s %s", p.ID, p.Grade))
	}
	return strings.Join(parts, "\n")
}
