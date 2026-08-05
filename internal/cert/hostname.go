package cert

import (
	"crypto/x509"
	"net"
	"strings"
)

// Hostname matching is implemented here rather than delegated to
// x509.Certificate.VerifyHostname because the answer alone is not useful. When
// a customer says "we gave you a wildcard" and the listener still fails, the
// operator needs to be told which SANs were actually present and why none of
// them covered the name -- docs/20-cert.md §4, PF-903.
//
// The rule is RFC 6125 §6.4.3 as narrowed by current practice:
//
//	*.acme.co.kr   covers  api.acme.co.kr
//	*.acme.co.kr   does not cover  acme.co.kr        (no label to match)
//	*.acme.co.kr   does not cover  a.b.acme.co.kr    (wildcard spans one label)
//	f*.acme.co.kr  covers nothing                    (partial wildcards refused)

// SANsOf lists every name a certificate presents, in a form fit for a
// diagnostic line.
func SANsOf(c *x509.Certificate) []string {
	out := make([]string, 0, len(c.DNSNames)+len(c.IPAddresses))
	out = append(out, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		out = append(out, ip.String())
	}
	// A certificate with no SAN at all is not usable, but the CN is worth
	// showing: it is almost always what the sender thought they were sending.
	if len(out) == 0 && c.Subject.CommonName != "" {
		out = append(out, "CN="+c.Subject.CommonName)
	}
	return out
}

// Covers reports whether a certificate is valid for host.
//
// The CommonName is not consulted. It has been ignored by every mainstream
// client since 2017, so honouring it here would let an install pass a check
// that browsers and Go clients then fail.
func Covers(c *x509.Certificate, host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		for _, sanIP := range c.IPAddresses {
			if sanIP.Equal(ip) {
				return true
			}
		}
		return false
	}
	for _, san := range c.DNSNames {
		if matchName(san, host) {
			return true
		}
	}
	return false
}

// matchName applies the wildcard rule to one SAN entry.
func matchName(san, host string) bool {
	san = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(san)), ".")
	if san == "" {
		return false
	}
	if san == host {
		return true
	}
	if !strings.HasPrefix(san, "*.") {
		return false
	}

	// A partial wildcard such as f*.acme.co.kr is refused outright above by the
	// prefix test; what remains is a whole-label wildcard.
	suffix := san[1:] // ".acme.co.kr"

	// The wildcard label must not be empty and must not span a dot, which is
	// what keeps *.acme.co.kr away from a.b.acme.co.kr.
	if !strings.HasSuffix(host, suffix) {
		return false
	}
	label := host[:len(host)-len(suffix)]
	if label == "" || strings.Contains(label, ".") {
		return false
	}

	// A wildcard directly under a public suffix -- *.co.kr -- is not rejected
	// here, and deliberately so. Telling co.kr from acme.co.kr requires the
	// Public Suffix List, which is an external dependency the certificate path
	// does not take (CLAUDE.md), and every rule-of-thumb substitute breaks a
	// real case: counting labels refuses *.internal, which is exactly what a
	// homelab private CA issues. No public CA can issue such a certificate
	// under the CA/Browser Forum baseline requirements, so what would be caught
	// here is a certificate that cannot be obtained.
	return true
}

// CoveringLeaves returns every leaf that covers host.
//
// More than one is a failure, not a choice: docs/20-cert.md §2.5 forbids
// picking arbitrarily, because the wrong pick surfaces months later as an
// expired certificate on a listener nobody was watching.
func CoveringLeaves(leaves []Cert, host string) []Cert {
	var out []Cert
	for _, l := range leaves {
		if Covers(l.Certificate, host) {
			out = append(out, l)
		}
	}
	return out
}
