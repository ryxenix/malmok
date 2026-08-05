package cert

import (
	"crypto/x509"
	"net"
	"testing"
)

// The wildcard rule is where "we gave you a wildcard" and what the SAN actually
// covers come apart. Every row here is a shape that has shown up in a real
// customer drop.
func TestMatchName(t *testing.T) {
	tests := []struct {
		san  string
		host string
		want bool
	}{
		// Exact.
		{"api.acme.co.kr", "api.acme.co.kr", true},
		{"api.acme.co.kr", "www.acme.co.kr", false},

		// The rule the customer means.
		{"*.acme.co.kr", "api.acme.co.kr", true},
		{"*.acme.co.kr", "grafana.acme.co.kr", true},

		// The two the customer does not mean, and finds out about later.
		{"*.acme.co.kr", "acme.co.kr", false},
		{"*.acme.co.kr", "a.b.acme.co.kr", false},

		// Partial wildcards are refused outright; no mainstream client honours
		// them, so accepting one here would pass a check the browser fails.
		{"f*.acme.co.kr", "foo.acme.co.kr", false},
		{"*oo.acme.co.kr", "foo.acme.co.kr", false},

		// A wildcard on a single-label private TLD, which is what a homelab
		// private CA issues and what a public-suffix guard would wrongly refuse.
		{"*.internal", "k8s.internal", true},
		{"*.internal", "a.k8s.internal", false},

		// A wildcard directly under a public suffix is matched rather than
		// refused: separating co.kr from acme.co.kr needs the Public Suffix
		// List, and no public CA can issue such a certificate anyway. See the
		// comment on matchName.
		{"*.co.kr", "acme.co.kr", true},

		// Case and the trailing root dot are not differences.
		{"*.ACME.co.kr", "API.acme.co.kr", true},
		{"api.acme.co.kr.", "api.acme.co.kr", true},
		{"api.acme.co.kr", "api.acme.co.kr.", true},

		// A wildcard cannot be the whole name.
		{"*", "acme.co.kr", false},
		{"*.", "acme.co.kr", false},

		// Empty input matches nothing rather than everything.
		{"", "acme.co.kr", false},
	}

	for _, tc := range tests {
		host := normaliseForTest(tc.host)
		if got := matchName(tc.san, host); got != tc.want {
			t.Errorf("matchName(%q, %q) = %v, want %v", tc.san, tc.host, got, tc.want)
		}
	}
}

// normaliseForTest applies what Covers does to the host before matching, so the
// table can be written the way an operator would type the name.
func normaliseForTest(h string) string {
	for len(h) > 0 && h[len(h)-1] == '.' {
		h = h[:len(h)-1]
	}
	out := []byte(h)
	for i := range out {
		if out[i] >= 'A' && out[i] <= 'Z' {
			out[i] += 'a' - 'A'
		}
	}
	return string(out)
}

// An IP listener is matched against the IP SANs, never against the DNS ones.
func TestCoversIP(t *testing.T) {
	c := &x509.Certificate{
		DNSNames:    []string{"10.10.0.11", "*.acme.co.kr"},
		IPAddresses: []net.IP{net.ParseIP("10.10.0.12")},
	}
	if Covers(c, "10.10.0.12") != true {
		t.Error("the IP SAN did not match")
	}
	// The same digits sitting in a DNS SAN do not make an IP certificate: a
	// client connecting to 10.10.0.11 checks IP SANs and would reject it.
	if Covers(c, "10.10.0.11") != false {
		t.Error("a DNS SAN was accepted for an IP connection")
	}
}

// The CommonName has been ignored by mainstream clients since 2017. Honouring
// it here would pass an install that browsers then refuse.
func TestCommonNameIsNotConsulted(t *testing.T) {
	s := newSite(t, "other.acme.co.kr")
	if Covers(s.leaf.cert, "other.acme.co.kr") != true {
		t.Fatal("the SAN did not match")
	}

	cnOnly := issue(t, &s.inter, issueOpts{cn: "legacy.acme.co.kr"}) // no DNS SANs
	if Covers(cnOnly.cert, "legacy.acme.co.kr") {
		t.Error("a certificate with only a CommonName was accepted")
	}
}

// SANsOf feeds the diagnostic line, so a certificate with no SAN still has to
// say something the operator recognises.
func TestSANsOfFallsBackToCommonName(t *testing.T) {
	s := newSite(t)
	cnOnly := issue(t, &s.inter, issueOpts{cn: "legacy.acme.co.kr"})
	got := SANsOf(cnOnly.cert)
	if len(got) != 1 || got[0] != "CN=legacy.acme.co.kr" {
		t.Errorf("SANsOf = %v, want the CommonName as a fallback", got)
	}
}

// A certificate with no BasicConstraints extension is an end-entity
// certificate. Treating it as a CA would let it be used to build a chain.
func TestNoBasicConstraintsIsALeaf(t *testing.T) {
	s := newSite(t)
	old := issue(t, &s.inter, issueOpts{
		cn: "old.acme.co.kr", dns: []string{"old.acme.co.kr"}, noBasicConstraints: true,
	})
	m := Scan([]File{{Name: "old.crt", Data: certPEM(old)}}, nil)
	if len(m.Leaves) != 1 {
		t.Errorf("classified as %d leaves, %d intermediates, %d roots",
			len(m.Leaves), len(m.Intermediates), len(m.Roots))
	}
}
