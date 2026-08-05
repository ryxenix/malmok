package cert

import (
	"bytes"
	"crypto/x509"
	"strings"
	"time"
)

// Chain assembly follows docs/20-cert.md §3: start at the leaf and walk issuer
// links until a self-signed certificate or a dead end.
//
// File order, name order and directory order are all ignored. Customers send
// fullchain files in the wrong order often enough that trusting the order means
// shipping a chain some clients reject and others accept, which is the slowest
// possible way to find out.

// Chain is the result of the walk.
type Chain struct {
	// Certs is leaf first, then each issuer in order. The root is not included
	// even when it was found; §3.1 keeps it out of the serving chain.
	Certs []Cert
	// Root is the self-signed certificate the walk ended at, when there was one.
	Root *Cert
	// MissingIssuer describes the certificate that could not be found, when the
	// walk ran out of candidates before reaching a root.
	MissingIssuer *MissingIssuer
}

// MissingIssuer is everything needed to ask for the certificate that is absent.
//
// In an airgap the AIA URL cannot be fetched, so the failure has to carry the
// information a human needs to obtain the file themselves (§3.2).
type MissingIssuer struct {
	// Subject is the certificate whose issuer is missing.
	Subject string
	// Issuer is the DN to ask for.
	Issuer string
	// AIA are the URLs the issuer published, if any.
	AIA []string
}

// Complete reports whether the chain reached a self-signed root.
func (c Chain) Complete() bool { return c.MissingIssuer == nil }

// Depth is how many certificates the serving chain holds.
func (c Chain) Depth() int { return len(c.Certs) }

// Build walks from leaf through the supplied candidates.
func Build(leaf Cert, candidates []Cert) Chain {
	chain := Chain{Certs: []Cert{leaf}}

	// Certificates that are their own issuer would loop forever, and so would a
	// cross-signed pair. Track what has been used.
	used := map[string]bool{leaf.Fingerprint(): true}

	current := leaf
	for {
		if isSelfSigned(current.Certificate) {
			// The leaf itself is self-signed: a self-signed serving certificate
			// is a complete chain of one.
			return chain
		}

		issuer, ok := findIssuer(current, candidates, used)
		if !ok {
			chain.MissingIssuer = &MissingIssuer{
				Subject: current.SubjectLine(),
				Issuer:  current.Issuer.String(),
				AIA:     current.IssuingCertificateURL,
			}
			return chain
		}

		used[issuer.Fingerprint()] = true
		if issuer.Kind == KindRoot {
			// The root is where the walk ends and where the serving chain
			// stops. It goes to the trust store instead (§3.1).
			r := issuer
			chain.Root = &r
			return chain
		}
		chain.Certs = append(chain.Certs, issuer)
		current = issuer
	}
}

// findIssuer looks for the certificate that signed c.
//
// Matching is on the DER of the names plus, when both sides carry them, the key
// identifiers. Signature verification is the final word: two CAs can share a DN
// after a rekey, and the identifiers are optional, so the only thing that
// settles it is whether the signature checks out.
func findIssuer(c Cert, candidates []Cert, used map[string]bool) (Cert, bool) {
	var fallback *Cert

	for i := range candidates {
		cand := candidates[i]
		if used[cand.Fingerprint()] {
			continue
		}
		if cand.Kind == KindLeaf {
			continue // an end-entity certificate cannot have signed anything
		}
		if !bytes.Equal(c.RawIssuer, cand.RawSubject) {
			continue
		}
		if len(c.AuthorityKeyId) > 0 && len(cand.SubjectKeyId) > 0 &&
			!bytes.Equal(c.AuthorityKeyId, cand.SubjectKeyId) {
			continue
		}
		if err := c.CheckSignatureFrom(cand.Certificate); err == nil {
			return cand, true
		}
		// The names line up but the signature does not. Keep it as a last
		// resort so the diagnostic can name something concrete rather than
		// claiming nothing was found at all.
		if fallback == nil {
			f := cand
			fallback = &f
		}
	}

	if fallback != nil {
		return *fallback, false
	}
	return Cert{}, false
}

func isSelfSigned(c *x509.Certificate) bool {
	if !bytes.Equal(c.RawSubject, c.RawIssuer) {
		return false
	}
	return c.CheckSignatureFrom(c) == nil
}

// chainsToPublicRoot reports whether the assembled chain leads to a root the
// host already trusts, and names it.
//
// The current time is not used: whether a certificate has expired is PF-904's
// question, and letting it decide this one would report a renewal problem as a
// chain problem. The leaf's own notBefore is used instead, which asks only
// whether the links hold.
func chainsToPublicRoot(c Chain) (bool, string) {
	if len(c.Certs) == 0 {
		return false, ""
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		// No store to check against -- an airgapped host, a scratch container.
		// Claiming the chain is fine would be a guess, so it is not made.
		return false, ""
	}
	inter := x509.NewCertPool()
	for _, cert := range c.Certs[1:] {
		inter.AddCert(cert.Certificate)
	}
	chains, err := c.Certs[0].Verify(x509.VerifyOptions{
		Roots:         pool,
		Intermediates: inter,
		CurrentTime:   c.Certs[0].NotBefore.Add(time.Second),
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	if err != nil || len(chains) == 0 {
		return false, ""
	}
	found := chains[0][len(chains[0])-1]
	return true, found.Subject.String()
}

// PEM renders the serving chain, leaf first.
func (c Chain) PEM() []byte {
	var b bytes.Buffer
	for _, cert := range c.Certs {
		b.Write(encodeCert(cert.Raw))
	}
	return b.Bytes()
}

// Describe renders the chain for a summary line.
func (c Chain) Describe() string {
	var parts []string
	for _, cert := range c.Certs {
		parts = append(parts, cert.Subject.CommonName)
	}
	if c.Root != nil {
		parts = append(parts, "["+c.Root.Subject.CommonName+"]")
	}
	return strings.Join(parts, " -> ")
}
