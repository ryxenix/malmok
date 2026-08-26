// Package spec reads and validates cluster.yaml.
//
// It is the only door into the engine. Everything downstream — the plan
// generator, the runner, the audit report — works from a ClusterSpec that has
// already been through here, so a malformed document fails before any node is
// contacted rather than three phases into an install.
package spec

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ryxen/malmok/api/v1alpha1"
)

// Document is a parsed cluster.yaml together with what it took to read it.
type Document struct {
	Spec v1alpha1.ClusterSpec

	// Path is where the document came from. Relative SourceRefs resolve against
	// its directory, so a bundle can be carried to a customer site as one
	// directory and still work from any working directory.
	Path string

	// Digest identifies this exact input. state.Digest of the raw bytes; the
	// resume check compares it, so it must be of the bytes on disk rather than
	// of anything re-serialised.
	Digest string

	raw []byte
}

// Load reads, parses, applies profile defaults and validates.
func Load(path string) (*Document, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("spec: read %s: %w", path, err)
	}
	doc, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("spec: %s: %w", path, err)
	}
	doc.Path = path
	return doc, nil
}

// Parse handles bytes that are already in hand.
func Parse(raw []byte) (*Document, error) {
	var spec v1alpha1.ClusterSpec

	dec := yaml.NewDecoder(bytes.NewReader(raw))
	// An unknown key is an error, not something to ignore. A typo in
	// `registrationAddress` that silently becomes nothing is exactly the class
	// of failure that surfaces as an unexplained rejoin months later.
	dec.KnownFields(true)
	if err := dec.Decode(&spec); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	if spec.APIVersion != v1alpha1.APIVersion {
		return nil, fmt.Errorf("apiVersion is %q, want %q", spec.APIVersion, v1alpha1.APIVersion)
	}
	if spec.Kind != v1alpha1.KindSpec {
		return nil, fmt.Errorf("kind is %q, want %q", spec.Kind, v1alpha1.KindSpec)
	}

	doc := &Document{Spec: spec, raw: raw}
	return doc, nil
}

// Raw returns the bytes the document was parsed from.
func (d *Document) Raw() []byte { return d.raw }

// Dir is the directory relative SourceRefs resolve against.
func (d *Document) Dir() string {
	if d.Path == "" {
		return "."
	}
	return filepath.Dir(d.Path)
}

// ---------------------------------------------------------------------------
// SourceRef
// ---------------------------------------------------------------------------

// ErrSecretInPlaintext reports a literal:// value on a field that holds a
// secret.
//
// cluster.yaml is an audit artifact that gets handed to a customer. A password
// in it is a liability that outlives the engagement, which is why the schema
// makes indirection the only path and this makes it enforced rather than
// advisory.
type ErrSecretInPlaintext struct{ Field string }

func (e *ErrSecretInPlaintext) Error() string {
	return fmt.Sprintf("spec: %s uses literal://; cluster.yaml is handed to customers, "+
		"use env:// or file:// (or pass --allow-literal-secrets to override)", e.Field)
}

// Resolve reads the value a SourceRef points at.
//
// Supported: file://, env://, literal://. sops:// is recognised and rejected
// with a message that says so, rather than being read as a filename.
func (d *Document) Resolve(field string, ref v1alpha1.SourceRef, secret, allowLiteral bool) ([]byte, error) {
	s := strings.TrimSpace(string(ref))
	if s == "" {
		return nil, nil
	}

	scheme, rest, found := strings.Cut(s, "://")
	if !found {
		return nil, fmt.Errorf("spec: %s: %q has no scheme; want file://, env:// or literal://", field, s)
	}

	switch scheme {
	case "file":
		path := rest
		if !filepath.IsAbs(path) {
			path = filepath.Join(d.Dir(), path)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("spec: %s: %w", field, err)
		}
		return body, nil

	case "env":
		v, ok := os.LookupEnv(rest)
		if !ok {
			return nil, fmt.Errorf("spec: %s: environment variable %s is not set", field, rest)
		}
		return []byte(v), nil

	case "literal":
		if secret && !allowLiteral {
			return nil, &ErrSecretInPlaintext{Field: field}
		}
		return []byte(rest), nil

	case "sops":
		return nil, fmt.Errorf("spec: %s: sops:// is not implemented yet; "+
			"decrypt to a file and use file://", field)

	default:
		return nil, fmt.Errorf("spec: %s: unknown scheme %q", field, scheme)
	}
}

// SecretRefs lists every SourceRef in the document that holds a secret, with
// the field path each came from.
//
// Kept as one list so the plaintext check cannot drift from the schema: a new
// secret field that is not added here is not checked, and that is visible in
// one place rather than spread across a validator.
func SecretRefs(s *v1alpha1.ClusterSpec) map[string]v1alpha1.SourceRef {
	out := map[string]v1alpha1.SourceRef{}
	add := func(field string, ref v1alpha1.SourceRef) {
		if strings.TrimSpace(string(ref)) != "" {
			out[field] = ref
		}
	}

	for i, n := range append(append([]v1alpha1.NodeSpec{}, s.Topology.Servers...), s.Topology.Agents...) {
		add(fmt.Sprintf("topology.nodes[%d].ssh.privateKey", i), n.SSH.PrivateKey)
		add(fmt.Sprintf("topology.nodes[%d].ssh.password", i), n.SSH.Password)
		add(fmt.Sprintf("topology.nodes[%d].ssh.becomePassword", i), n.SSH.BecomePassword)
	}
	add("registry.username", s.Registry.Username)
	add("registry.password", s.Registry.Password)
	if s.PKI.ACME != nil {
		add("pki.acme.apiToken", s.PKI.ACME.APIToken)
	}
	if s.PKI.PrivateCA != nil {
		add("pki.privateCA.intermediateKey", s.PKI.PrivateCA.IntermediateKey)
	}
	if s.PKI.BYOCert != nil {
		add("pki.byoCert.key", s.PKI.BYOCert.Key)
	}
	if s.Platform.Secrets.AgeKey != "" {
		add("platform.secrets.ageKey", s.Platform.Secrets.AgeKey)
	}
	if s.Kubernetes.Etcd.S3 != nil {
		add("kubernetes.etcd.s3.accessKey", s.Kubernetes.Etcd.S3.AccessKey)
		add("kubernetes.etcd.s3.secretKey", s.Kubernetes.Etcd.S3.SecretKey)
	}
	for gi, g := range s.Gateway.Gateways {
		for li, l := range g.Listeners {
			if l.TLS != nil && l.TLS.BYO != nil {
				add(fmt.Sprintf("gateway.gateways[%d].listeners[%d].tls.byo.passphrase", gi, li),
					l.TLS.BYO.Passphrase)
			}
		}
	}
	return out
}
