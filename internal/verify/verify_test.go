package verify

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ryxenix/malmok/internal/codes"
	"github.com/ryxenix/malmok/internal/preflight"
)

// The fixtures are generated and the servers are real: these checks are about
// what happens on the wire, so a test that stubbed the handshake would be
// testing the stub.

var base = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

type ca struct {
	cert *x509.Certificate
	der  []byte
	key  crypto.Signer
}

var serial int64

func issue(t *testing.T, parent *ca, cn string, isCA bool, dns []string, notAfter time.Time) ca {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial++
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             base.AddDate(0, 0, -1),
		NotAfter:              notAfter,
		DNSNames:              dns,
		BasicConstraintsValid: true,
		IsCA:                  isCA,
	}
	if isCA {
		tmpl.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	} else {
		tmpl.KeyUsage = x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	signer, signerKey := tmpl, crypto.Signer(key)
	if parent != nil {
		signer, signerKey = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, key.Public(), signerKey)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return ca{cert: c, der: der, key: key}
}

// serveTLS starts a listener presenting exactly the chain it is given.
//
// "Exactly" is the point: the whole of PV-002 is what the server chooses to
// send, so the test has to be able to send a deliberately short chain.
func serveTLS(t *testing.T, chain [][]byte, key crypto.Signer, minVersion uint16) string {
	t.Helper()
	cfg := &tls.Config{
		Certificates: []tls.Certificate{{Certificate: chain, PrivateKey: key}},
		MinVersion:   minVersion,
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_ = c.(*tls.Conn).Handshake()
				c.Close()
			}()
		}
	}()
	return ln.Addr().String()
}

func target(t *testing.T, addr string, hostnames ...string) Target {
	t.Helper()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	p := 0
	if _, err := fmtSscan(port, &p); err != nil {
		t.Fatal(err)
	}
	return Target{Address: host, Port: p, Hostnames: hostnames}
}

func fmtSscan(s string, p *int) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errBadPort
		}
		n = n*10 + int(r-'0')
	}
	*p = n
	return 1, nil
}

var errBadPort = &net.AddrError{Err: "not a port"}

func find(t *testing.T, rs []Result, id string) Result {
	t.Helper()
	for _, r := range rs {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no result for %s", id)
	return Result{}
}

// pki builds root -> intermediate -> leaf for one name.
func pki(t *testing.T, name string) (root, inter, leaf ca) {
	t.Helper()
	root = issue(t, nil, "Test Root CA", true, nil, base.AddDate(10, 0, 0))
	inter = issue(t, &root, "Test Issuing CA", true, nil, base.AddDate(5, 0, 0))
	leaf = issue(t, &inter, name, false, []string{name}, base.AddDate(1, 0, 0))
	return
}

func poolOf(c ca) *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(c.cert)
	return p
}

// The whole point of the phase: a server sending leaf only passes in a browser
// and fails everywhere else. Go never fetches an AIA issuer, so the verifier is
// the strict client by construction.
func TestChainMissingAnIntermediateIsCaught(t *testing.T) {
	root, inter, leaf := pki(t, "api.acme.internal")

	t.Run("leaf only fails", func(t *testing.T) {
		addr := serveTLS(t, [][]byte{leaf.der}, leaf.key, tls.VersionTLS12)
		got := find(t, Verify(context.Background(), target(t, addr, "api.acme.internal"),
			Options{Roots: poolOf(root), Now: base}), "PV-002")

		if !got.Failed() || got.Severity != codes.SeverityBlock {
			t.Fatalf("PV-002 is %s/%s: %s", got.Status, got.Severity, got.Detail)
		}
		if got.Code != "CHAIN_NOT_SELF_SUFFICIENT" {
			t.Errorf("code is %q", got.Code)
		}
		// The missing certificate has to be named, or the operator has to go
		// and work out which one it is.
		if !strings.Contains(got.Detail, "Test Issuing CA") {
			t.Errorf("the failure does not name the missing intermediate: %s", got.Detail)
		}
	})

	t.Run("leaf plus intermediate passes", func(t *testing.T) {
		addr := serveTLS(t, [][]byte{leaf.der, inter.der}, leaf.key, tls.VersionTLS12)
		got := find(t, Verify(context.Background(), target(t, addr, "api.acme.internal"),
			Options{Roots: poolOf(root), Now: base}), "PV-002")

		if got.Failed() {
			t.Fatalf("PV-002 failed on a complete chain: %s", got.Detail)
		}
		if !strings.Contains(got.Detail, "AIA") {
			t.Errorf("the pass does not say what it proves: %s", got.Detail)
		}
	})
}

// A chain complete in itself that ends at an untrusted root is a different
// failure with the opposite fix, and it wears the same x509 error. Sending an
// operator to look for an intermediate they already have is worse than saying
// nothing.
func TestUntrustedRootIsNotReportedAsAMissingIntermediate(t *testing.T) {
	_, inter, leaf := pki(t, "api.acme.internal")
	addr := serveTLS(t, [][]byte{leaf.der, inter.der}, leaf.key, tls.VersionTLS12)

	// No roots supplied: the chain is whole and nothing trusts its anchor.
	got := find(t, Verify(context.Background(), target(t, addr, "api.acme.internal"),
		Options{Roots: x509.NewCertPool(), Now: base}), "PV-002")

	if got.Code != "ROOT_NOT_TRUSTED" {
		t.Fatalf("code is %q: %s", got.Code, got.Detail)
	}
	if strings.Contains(got.Detail, "Place that intermediate") {
		t.Error("the failure sends the operator after an intermediate that is already there")
	}
	if !strings.Contains(got.Detail, "trustDistribution") {
		t.Errorf("the failure does not say how to fix it: %s", got.Detail)
	}
}

// PV-001 records what was sent, whether or not it verifies -- the evidence has
// to survive the failure it explains.
func TestCapturedChainIsRecordedEvenWhenVerificationFails(t *testing.T) {
	_, _, leaf := pki(t, "api.acme.internal")
	addr := serveTLS(t, [][]byte{leaf.der}, leaf.key, tls.VersionTLS12)

	rs := Verify(context.Background(), target(t, addr, "api.acme.internal"),
		Options{Roots: x509.NewCertPool(), Now: base})

	got := find(t, rs, "PV-001")
	if got.Failed() {
		t.Fatalf("PV-001 failed: %s", got.Detail)
	}
	if !strings.Contains(got.Detail, "1 certificate") || !strings.Contains(got.Detail, "api.acme.internal") {
		t.Errorf("PV-001 did not record the chain: %s", got.Detail)
	}
	if got.Evidence == "" {
		t.Error("PV-001 kept no evidence for the audit report")
	}
}

// The whole chain, not just the leaf: a cross-signed intermediate left behind
// after its own expiry has a current leaf and only strict clients notice.
func TestExpiredIntermediateIsCaught(t *testing.T) {
	root := issue(t, nil, "Test Root CA", true, nil, base.AddDate(10, 0, 0))
	old := issue(t, &root, "Old Cross-Signed CA", true, nil, base.AddDate(0, 0, -1))
	leaf := issue(t, &old, "api.acme.internal", false, []string{"api.acme.internal"}, base.AddDate(1, 0, 0))

	addr := serveTLS(t, [][]byte{leaf.der, old.der}, leaf.key, tls.VersionTLS12)
	got := find(t, Verify(context.Background(), target(t, addr, "api.acme.internal"),
		Options{Roots: poolOf(root), Now: base}), "PV-003")

	if !got.Failed() || got.Code != "CHAIN_EXPIRED" {
		t.Fatalf("PV-003 is %s/%s: %s", got.Status, got.Code, got.Detail)
	}
	if !strings.Contains(got.Detail, "Old Cross-Signed CA") {
		t.Errorf("the failure does not name the expired certificate: %s", got.Detail)
	}
}

func TestExpiringChainWarns(t *testing.T) {
	root := issue(t, nil, "Test Root CA", true, nil, base.AddDate(10, 0, 0))
	inter := issue(t, &root, "Test Issuing CA", true, nil, base.AddDate(5, 0, 0))
	leaf := issue(t, &inter, "api.acme.internal", false, []string{"api.acme.internal"}, base.AddDate(0, 0, 20))

	addr := serveTLS(t, [][]byte{leaf.der, inter.der}, leaf.key, tls.VersionTLS12)
	got := find(t, Verify(context.Background(), target(t, addr, "api.acme.internal"),
		Options{Roots: poolOf(root), Now: base}), "PV-003")

	if got.Severity != codes.SeverityWarn || got.Code != "CHAIN_EXPIRING" {
		t.Fatalf("PV-003 is %s/%s: %s", got.Severity, got.Code, got.Detail)
	}
}

// A gateway can serve the right certificate for one listener and the default
// for the rest, and nothing in its status says so.
func TestPerHostnameCatchesTheWrongCertificate(t *testing.T) {
	root, inter, leaf := pki(t, "api.acme.internal")
	addr := serveTLS(t, [][]byte{leaf.der, inter.der}, leaf.key, tls.VersionTLS12)

	got := find(t, Verify(context.Background(),
		target(t, addr, "api.acme.internal", "other.acme.internal"),
		Options{Roots: poolOf(root), Now: base}), "PV-007")

	if !got.Failed() || got.Code != "LISTENER_CERT_MISMATCH" {
		t.Fatalf("PV-007 is %s/%s: %s", got.Status, got.Code, got.Detail)
	}
	if !strings.Contains(got.Detail, "other.acme.internal") {
		t.Errorf("the failure does not name the listener that is wrong: %s", got.Detail)
	}
}

// A wildcard is not a name anybody connects to: sending "*.acme.internal" as
// SNI matches nothing, so a concrete label is substituted.
func TestWildcardListenerIsProbedWithAConcreteName(t *testing.T) {
	if got := exampleFor("*.acme.internal"); got == "*.acme.internal" || !strings.HasSuffix(got, ".acme.internal") {
		t.Errorf("exampleFor(*.acme.internal) = %q", got)
	}
	if got := exampleFor("api.acme.internal"); got != "api.acme.internal" {
		t.Errorf("a concrete hostname was rewritten to %q", got)
	}

	root, inter, leaf := pki(t, "*.acme.internal")
	addr := serveTLS(t, [][]byte{leaf.der, inter.der}, leaf.key, tls.VersionTLS12)

	got := find(t, Verify(context.Background(), target(t, addr, "*.acme.internal"),
		Options{Roots: poolOf(root), Now: base}), "PV-007")
	if got.Failed() {
		t.Fatalf("PV-007 failed against a wildcard listener: %s", got.Detail)
	}
}

// TLS 1.0 and 1.1 are deprecated everywhere and any scanner reports them.
func TestWeakTLSVersionsWarn(t *testing.T) {
	root, inter, leaf := pki(t, "api.acme.internal")

	t.Run("modern", func(t *testing.T) {
		addr := serveTLS(t, [][]byte{leaf.der, inter.der}, leaf.key, tls.VersionTLS12)
		got := find(t, Verify(context.Background(), target(t, addr, "api.acme.internal"),
			Options{Roots: poolOf(root), Now: base}), "PV-008")
		if got.Failed() {
			t.Fatalf("PV-008 is %s: %s", got.Status, got.Detail)
		}
		if !strings.Contains(got.Detail, "TLS 1.2") || !strings.Contains(got.Detail, "TLS 1.3") {
			t.Errorf("PV-008 did not record what negotiates: %s", got.Detail)
		}
	})

	t.Run("legacy", func(t *testing.T) {
		addr := serveTLS(t, [][]byte{leaf.der, inter.der}, leaf.key, tls.VersionTLS10)
		got := find(t, Verify(context.Background(), target(t, addr, "api.acme.internal"),
			Options{Roots: poolOf(root), Now: base}), "PV-008")
		if got.Code != "WEAK_TLS_VERSION" {
			t.Fatalf("PV-008 is %s/%s: %s", got.Status, got.Code, got.Detail)
		}
	})
}

// A target that never completes a handshake produces one result saying so,
// not eight results each inventing a verdict from no evidence.
func TestUnreachableTargetProducesOneResult(t *testing.T) {
	// Port 1 on the loopback: nothing listens there and nothing will.
	rs := Verify(context.Background(), Target{Address: "127.0.0.1", Port: 1},
		Options{Timeout: time.Second, Now: base})

	if len(rs) != 1 {
		t.Fatalf("an unreachable target produced %d results", len(rs))
	}
	if rs[0].Code != "NO_HANDSHAKE" || !rs[0].Failed() {
		t.Errorf("the result is %s/%s: %s", rs[0].Status, rs[0].Code, rs[0].Detail)
	}
}

// A certificate publishing no distribution point cannot be revocation-checked,
// which means no client can hard-fail on it -- that is an answer, not a gap.
func TestNoRevocationPointsIsAnAnswer(t *testing.T) {
	root, inter, leaf := pki(t, "api.acme.internal")
	addr := serveTLS(t, [][]byte{leaf.der, inter.der}, leaf.key, tls.VersionTLS12)

	got := find(t, Verify(context.Background(), target(t, addr, "api.acme.internal"),
		Options{Roots: poolOf(root), Now: base}), "PV-004")
	if got.Failed() {
		t.Fatalf("PV-004 is %s: %s", got.Status, got.Detail)
	}
	if !strings.Contains(got.Detail, "hard-fail") {
		t.Errorf("PV-004 does not say what it means for a client: %s", got.Detail)
	}
}

// Every PV code in the registry has to be produced by something, and nothing
// may emit a code that is not registered. The same drift the preflight codes
// already had a test for.
func TestEveryVerifyCodeIsImplemented(t *testing.T) {
	root, inter, leaf := pki(t, "api.acme.internal")
	addr := serveTLS(t, [][]byte{leaf.der, inter.der}, leaf.key, tls.VersionTLS12)

	produced := map[string]bool{}
	for _, r := range Verify(context.Background(), target(t, addr, "api.acme.internal"),
		Options{Roots: poolOf(root), Now: base}) {
		produced[r.ID] = true
		if _, ok := codes.Lookup(r.ID); !ok {
			t.Errorf("%s is emitted and not defined in internal/codes", r.ID)
		}
	}

	for _, c := range codes.All() {
		if c.Family != codes.FamilyVerification {
			continue
		}
		if !produced[c.ID] {
			t.Errorf("%s is defined and never produced (%s)", c.ID, c.Summary)
		}
	}
}

// A failure has to say what to do about it.
func TestFailuresExplainThemselves(t *testing.T) {
	_, _, leaf := pki(t, "api.acme.internal")
	addr := serveTLS(t, [][]byte{leaf.der}, leaf.key, tls.VersionTLS12)

	for _, r := range Verify(context.Background(), target(t, addr, "api.acme.internal"),
		Options{Roots: x509.NewCertPool(), Now: base}) {

		if r.Status != preflight.StatusFail {
			continue
		}
		if r.Code == "" {
			t.Errorf("%s fails with no reason code", r.ID)
		}
		if len(r.Detail) < 40 {
			t.Errorf("%s fails with nothing to act on: %q", r.ID, r.Detail)
		}
	}
}
