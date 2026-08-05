// Package cert assembles and validates the certificate material an operator
// supplies, using only crypto/x509 and encoding/pem (plus crypto/pbkdf2 for
// encrypted PKCS#8 keys, which is standard library as of Go 1.24).
//
// The governing rule from docs/20-cert.md §2: file names are not trusted. Every
// CA names its files differently and customers rename them in transit, so the
// only inputs are the bytes. Classification, key matching and chain assembly
// all work from certificate contents.
//
// This package deliberately does not import internal/preflight. The preflight
// layer consumes these findings to build probe results; importing it back would
// be a cycle. Findings are shaped closely enough that the adapter is trivial.
package cert

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// File is one input file, kept with its name only so diagnostics can point the
// operator at something they can see on disk. Nothing is decided from the name.
type File struct {
	Name string
	Data []byte
}

// LoadDir reads every regular file in a directory, non-recursively.
//
// Everything is read, including files that turn out not to be certificate
// material: what a README or a .zip is gets decided by its bytes in Scan, not
// by its extension here.
func LoadDir(dir string) ([]File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("cert: read %s: %w", dir, err)
	}
	var out []File
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("cert: read %s: %w", p, err)
		}
		out = append(out, File{Name: e.Name(), Data: b})
	}
	// Directory order is filesystem order, which differs between machines. The
	// assembler must not depend on it, and a stable order makes that testable.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Kind classifies one certificate by what it says about itself.
type Kind string

const (
	// KindLeaf is an end-entity certificate: BasicConstraints CA=false, or the
	// extension is absent altogether, which older certificates do.
	KindLeaf Kind = "leaf"
	// KindIntermediate is a CA whose issuer is somebody else.
	KindIntermediate Kind = "intermediate"
	// KindRoot is a self-signed CA: subject == issuer.
	KindRoot Kind = "root"
)

// Cert is one parsed certificate with where it came from.
type Cert struct {
	*x509.Certificate
	Kind Kind
	// From is the file it was found in. One file can hold many certificates.
	From string
}

// Fingerprint is the SHA-256 of the DER, which is what the audit annotations
// and the duplicate check both compare on.
func (c Cert) Fingerprint() string {
	sum := sha256.Sum256(c.Raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Subject renders the subject for a diagnostic line.
func (c Cert) SubjectLine() string { return c.Certificate.Subject.String() }

// Material is everything found across the input files, already classified.
type Material struct {
	Leaves        []Cert
	Intermediates []Cert
	Roots         []Cert

	// Keys are every private key found, decrypted where a passphrase was
	// supplied and it worked. Ones that stayed locked are in Locked.
	Keys []Key
	// Locked are encrypted keys the passphrase did not open. They are kept
	// rather than dropped so PF-906 can say which file failed.
	Locked []LockedKey

	// Skipped are files whose leading bytes were neither PEM nor DER. Recorded
	// rather than silently ignored: an operator who dropped the wrong export
	// needs to be told the file was not read.
	Skipped []string
}

// Certs returns every certificate, in leaf/intermediate/root order.
func (m Material) Certs() []Cert {
	out := make([]Cert, 0, len(m.Leaves)+len(m.Intermediates)+len(m.Roots))
	out = append(out, m.Leaves...)
	out = append(out, m.Intermediates...)
	out = append(out, m.Roots...)
	return out
}

// Scan reads the supplied files into classified material.
//
// passphrase may be nil; encrypted keys then land in Locked and PF-906 reports
// them. Scanning never fails on one bad file: a directory holding a stray text
// file still has to yield the certificates beside it.
func Scan(files []File, passphrase []byte) Material {
	var m Material
	seen := map[string]bool{} // by fingerprint; the same cert in two files is common

	for _, f := range files {
		blocks, ok := split(f.Data)
		if !ok {
			m.Skipped = append(m.Skipped, f.Name)
			continue
		}
		for _, b := range blocks {
			switch {
			case b.isCert():
				c, err := x509.ParseCertificate(b.der)
				if err != nil {
					continue
				}
				cc := Cert{Certificate: c, Kind: classify(c), From: f.Name}
				// Deduplicate here rather than at assembly time: customers
				// routinely ship the same intermediate in three files, and a
				// chain walker that sees it three times reports nonsense depth.
				fp := cc.Fingerprint()
				if seen[fp] {
					continue
				}
				seen[fp] = true
				switch cc.Kind {
				case KindLeaf:
					m.Leaves = append(m.Leaves, cc)
				case KindIntermediate:
					m.Intermediates = append(m.Intermediates, cc)
				case KindRoot:
					m.Roots = append(m.Roots, cc)
				}

			case b.isKey():
				k, err := parseKey(b, passphrase)
				if err != nil {
					if isLocked(err) {
						m.Locked = append(m.Locked, LockedKey{From: f.Name, Err: err})
					}
					continue
				}
				k.From = f.Name
				m.Keys = append(m.Keys, k)
			}
		}
	}
	return m
}

// classify implements docs/20-cert.md §2.2.
//
// A certificate with no BasicConstraints extension is a leaf: an absent
// extension means "not a CA", and treating it as one would let an end-entity
// certificate be used to build a chain.
func classify(c *x509.Certificate) Kind {
	if !c.BasicConstraintsValid || !c.IsCA {
		return KindLeaf
	}
	// Self-signed is decided on the DER of the names, not their string forms:
	// two different DNs can render to the same string.
	if string(c.RawSubject) == string(c.RawIssuer) {
		return KindRoot
	}
	return KindIntermediate
}

// block is one decoded input block, either DER bytes of a certificate or the
// PEM block of a key.
type block struct {
	pemType string // empty when the input was raw DER
	der     []byte
	pem     *pem.Block
}

func (b block) isCert() bool {
	if b.pem == nil {
		return true // raw DER input is only accepted as a certificate
	}
	return b.pemType == "CERTIFICATE"
}

func (b block) isKey() bool {
	return b.pem != nil && strings.Contains(b.pemType, "PRIVATE KEY")
}

// split implements docs/20-cert.md §2.1: decide the encoding from the leading
// bytes, then take every block a PEM file holds.
//
// Reports false when the input is neither, so the caller can record the file as
// skipped instead of failing the whole run.
func split(data []byte) ([]block, bool) {
	trimmed := trimLeadingSpace(data)
	switch {
	case len(trimmed) == 0:
		return nil, false

	case strings.HasPrefix(string(trimmed), "-----BEGIN"):
		var out []block
		rest := trimmed
		for {
			var p *pem.Block
			p, rest = pem.Decode(rest)
			if p == nil {
				break
			}
			out = append(out, block{pemType: p.Type, der: p.Bytes, pem: p})
		}
		if len(out) == 0 {
			return nil, false
		}
		return out, true

	case trimmed[0] == 0x30:
		// DER SEQUENCE. Windows exports arrive this way with a .crt suffix.
		return []block{{der: data}}, true
	}
	return nil, false
}

func trimLeadingSpace(b []byte) []byte {
	i := 0
	for i < len(b) && (b[i] == ' ' || b[i] == '\n' || b[i] == '\r' || b[i] == '\t') {
		i++
	}
	return b[i:]
}
