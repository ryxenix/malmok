// Package verify performs the wire checks of docs/20-cert.md §6.5.
//
// PF-9xx validates the input files. This validates what a client actually
// receives, and the two are not the same thing. The incident the document
// records -- a customer bundle that browsers accepted and a Spring application
// rejected -- passed every file check and failed on the wire.
//
// The verifier is deliberately the strictest client, not the most forgiving
// one. Go's crypto/x509 never fetches an issuer over AIA, which makes it
// exactly the profile Java, curl and Go clients present: if the chain verifies
// here, it verifies for them. A browser passing proves nothing, and ADR-010
// forbids using it as evidence.
package verify

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"platform.ryxen.dev/malmok/internal/codes"
	"platform.ryxen.dev/malmok/internal/preflight"
)

// Result is one wire check.
//
// preflight.ProbeResult is reused rather than a third result type invented: an
// audit report shows PF and PV findings side by side, and preflight does not
// import this package, so there is no cycle.
type Result = preflight.ProbeResult

// Target is one endpoint to verify.
type Target struct {
	// Address is what to connect to -- the gateway's external address.
	Address string
	Port    int
	// Hostnames are the listener names served here. Each is verified on its own
	// (PV-007), because a gateway with several listeners can serve the right
	// certificate for one and the default for the rest.
	Hostnames []string
}

// Options configure the verifier.
type Options struct {
	// Roots is the trust store the strict profile verifies against. Empty means
	// the host's own store; a private CA has to be supplied here or nothing
	// will verify against it.
	Roots *x509.CertPool

	// ExpiryWarningDays is the window PV-003 warns inside.
	ExpiryWarningDays int

	Timeout time.Duration
	// Now is the clock the validity window is judged against, for tests.
	Now time.Time
}

func (o Options) timeout() time.Duration {
	if o.Timeout <= 0 {
		return 10 * time.Second
	}
	return o.Timeout
}

func (o Options) now() time.Time {
	if o.Now.IsZero() {
		return time.Now()
	}
	return o.Now
}

// DefaultExpiryWarningDays matches the certificate bundle's own window.
const DefaultExpiryWarningDays = 45

// Verify runs every wire check against one endpoint.
//
// The order matters only in that everything depends on the handshake: a target
// that cannot be reached produces one result saying so rather than eight
// results each inventing a verdict from no evidence.
func Verify(ctx context.Context, t Target, o Options) []Result {
	if o.ExpiryWarningDays == 0 {
		o.ExpiryWarningDays = DefaultExpiryWarningDays
	}
	port := t.Port
	if port == 0 {
		port = 443
	}
	addr := net.JoinHostPort(t.Address, fmt.Sprint(port))

	sni := ""
	if len(t.Hostnames) > 0 {
		sni = exampleFor(t.Hostnames[0])
	}

	state, err := handshake(ctx, addr, sni, o)
	if err != nil {
		return []Result{{
			ID: "PV-001", Status: preflight.StatusFail, Severity: codes.SeverityBlock,
			Code: "NO_HANDSHAKE",
			Detail: fmt.Sprintf("no TLS handshake completed with %s (SNI %q): %v; "+
				"nothing else can be verified without one", addr, sni, err),
		}}
	}

	chain := state.PeerCertificates
	out := []Result{
		capturedChain(addr, chain),
		selfSufficient(addr, sni, chain, o),
		notExpired(addr, chain, o),
		revocationReachable(ctx, chain, o),
		withoutSNI(ctx, addr, o),
		byIPAddress(ctx, addr, t, o),
	}
	out = append(out, perHostname(ctx, addr, t, o))
	out = append(out, negotiable(ctx, addr, sni, o))

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// handshake connects and captures whatever the server sends.
//
// Verification is off on purpose: the point is to see the chain as it arrives,
// including the case where it does not verify. Judging it is the next step's
// job, and doing both at once would lose the evidence on failure.
func handshake(ctx context.Context, addr, sni string, o Options) (*tls.ConnectionState, error) {
	return handshakeWith(ctx, addr, &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: true, //nolint:gosec // capturing the chain is the point; §PV-002 judges it
	}, o)
}

func handshakeWith(ctx context.Context, addr string, cfg *tls.Config, o Options) (*tls.ConnectionState, error) {
	c, cancel := context.WithTimeout(ctx, o.timeout())
	defer cancel()

	d := &tls.Dialer{NetDialer: &net.Dialer{}, Config: cfg}
	conn, err := d.DialContext(c, "tcp", addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	state := conn.(*tls.Conn).ConnectionState()
	return &state, nil
}

// PV-001: what the server actually sends.
func capturedChain(addr string, chain []*x509.Certificate) Result {
	if len(chain) == 0 {
		return fail("PV-001", "NO_CERTIFICATE",
			"%s completed a handshake and sent no certificate", addr)
	}

	var parts []string
	for _, c := range chain {
		parts = append(parts, fmt.Sprintf("%s (issuer %s, expires %s)",
			c.Subject.CommonName, c.Issuer.CommonName, c.NotAfter.UTC().Format("2006-01-02")))
	}
	return Result{
		ID: "PV-001", Status: preflight.StatusPass, Severity: codes.SeverityInfo,
		Detail:   fmt.Sprintf("%s sends %d certificate(s): %s", addr, len(chain), strings.Join(parts, " -> ")),
		Evidence: strings.Join(parts, " | "),
	}
}

// PV-002: the chain verifies on its own, with no issuer fetched over AIA.
//
// This is the decisive check. Go never fetches an AIA issuer, so passing here
// means Java, Go, curl and Python all pass. Failing here while a browser
// succeeds is the exact shape of the incident docs/20-cert.md records: the
// browser supplements the chain and the application does not.
func selfSufficient(addr, sni string, chain []*x509.Certificate, o Options) Result {
	if len(chain) == 0 {
		return skip("PV-002", "no certificate was sent")
	}

	inter := x509.NewCertPool()
	for _, c := range chain[1:] {
		inter.AddCert(c)
	}
	opts := x509.VerifyOptions{
		Roots:         o.Roots,
		Intermediates: inter,
		CurrentTime:   o.now(),
		DNSName:       sni,
	}

	if _, err := chain[0].Verify(opts); err == nil {
		return pass("PV-002",
			"the chain %s sends verifies on its own, with no issuer fetched over AIA; "+
				"Java, Go, curl and Python all accept it", addr)
	} else {
		// Retrying with AIA supplementation separates "the chain is incomplete"
		// from "the certificate is wrong". Only the first has the signature the
		// document warns about, and only it has this fix.
		lenient, fetched := verifyWithAIA(chain, opts)
		broken, gap := brokenLink(chain)

		// Two different failures wear the same error, and they have opposite
		// fixes. A chain missing an intermediate is fixed by adding the
		// intermediate; a chain that is internally complete and ends at a root
		// nobody trusts is fixed by distributing that root. Telling an operator
		// to go find an intermediate they already have is worse than saying
		// nothing.
		reason, detail := "CHAIN_NOT_SELF_SUFFICIENT", ""
		switch {
		case broken:
			detail = fmt.Sprintf(
				"%s sends %d certificate(s) and there is a gap in them: nothing it sends was issued by %s, "+
					"which %s needs. Place that intermediate in the bundle directory and re-apply",
				addr, len(chain), gap.issuer, gap.subject)

		case len(chain) == 1:
			// A single certificate that does not verify is the documented
			// incident: the server sends the leaf and expects the client to
			// find the rest. Browsers do; nothing else does.
			detail = fmt.Sprintf(
				"%s sends only its own certificate, and %s is not among what it sends. "+
					"Place that intermediate in the bundle directory and re-apply",
				addr, chain[0].Issuer.CommonName)

		default:
			// Every certificate the server sends links to the next, so nothing
			// is missing from the chain itself: what is missing is trust in
			// what sits above it. The root is deliberately not served
			// (docs/20-cert.md §3.1) -- it belongs in a trust store.
			reason = "ROOT_NOT_TRUSTED"
			detail = fmt.Sprintf(
				"the chain %s sends links all the way through and ends at a certificate issued by %s, "+
					"which this verifier does not trust. The chain is not short -- the root is not "+
					"served on purpose -- so the fix is to distribute that root through "+
					"pki.trustDistribution, or to verify against it explicitly when it is a private CA",
				addr, chain[len(chain)-1].Issuer.CommonName)
		}

		if lenient {
			detail += fmt.Sprintf(". Fetching the issuer over AIA (%s) does make it verify, "+
				"which means a browser passes and Java, Go and curl do not -- the failure this "+
				"check exists to catch", strings.Join(fetched, ", "))
		}

		return Result{
			ID: "PV-002", Status: preflight.StatusFail, Severity: codes.SeverityBlock,
			Code: reason, Detail: detail,
			Evidence: err.Error(),
		}
	}
}

// verifyWithAIA repeats the verification while fetching issuers, which is what
// a browser does. Reported as context, never as a pass: ADR-010 forbids using
// browser behaviour as evidence.
func verifyWithAIA(chain []*x509.Certificate, opts x509.VerifyOptions) (bool, []string) {
	inter := x509.NewCertPool()
	for _, c := range chain[1:] {
		inter.AddCert(c)
	}

	var fetched []string
	current := chain[len(chain)-1]
	for range 4 {
		if len(current.IssuingCertificateURL) == 0 {
			break
		}
		u := current.IssuingCertificateURL[0]
		issuer, err := fetchIssuer(u)
		if err != nil {
			break
		}
		inter.AddCert(issuer)
		fetched = append(fetched, u)
		if issuer.Subject.String() == issuer.Issuer.String() {
			break
		}
		current = issuer
	}
	if len(fetched) == 0 {
		return false, nil
	}

	opts.Intermediates = inter
	_, err := chain[0].Verify(opts)
	return err == nil, fetched
}

// fetchIssuer retrieves one certificate from an AIA URL.
func fetchIssuer(raw string) (*x509.Certificate, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("unsupported AIA URL")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(raw) //nolint:gosec,noctx // the URL comes from the certificate under test
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(body)
}

// link names one certificate and the issuer it needs.
type link struct{ subject, issuer string }

// brokenLink finds a gap inside the served chain.
//
// Only gaps *within* what was sent count. The certificate above the topmost one
// is always absent, because the root is deliberately not served (§3.1), and
// counting that as a gap would report every correctly assembled chain as
// broken -- which is exactly what it did before this was written.
func brokenLink(chain []*x509.Certificate) (bool, link) {
	for i := 0; i+1 < len(chain); i++ {
		if chain[i].Issuer.String() != chain[i+1].Subject.String() {
			return true, link{
				subject: chain[i].Subject.CommonName,
				issuer:  chain[i].Issuer.CommonName,
			}
		}
	}
	return false, link{}
}

// PV-003: nothing in the served chain has expired.
//
// The whole chain, not just the leaf. A cross-signed intermediate left in the
// bundle after its own expiry is the classic version of this: the leaf is
// current, the chain is not, and only strict clients notice.
func notExpired(addr string, chain []*x509.Certificate, o Options) Result {
	if len(chain) == 0 {
		return skip("PV-003", "no certificate was sent")
	}
	now := o.now()

	var expired, expiring []string
	for _, c := range chain {
		switch {
		case c.NotAfter.Before(now):
			expired = append(expired, fmt.Sprintf("%s expired %s",
				c.Subject.CommonName, c.NotAfter.UTC().Format("2006-01-02")))
		case c.NotAfter.Sub(now) < time.Duration(o.ExpiryWarningDays)*24*time.Hour:
			expiring = append(expiring, fmt.Sprintf("%s expires %s",
				c.Subject.CommonName, c.NotAfter.UTC().Format("2006-01-02")))
		}
	}

	switch {
	case len(expired) > 0:
		return fail("PV-003", "CHAIN_EXPIRED",
			"%s serves an expired certificate in its chain: %s; a cross-signed intermediate left "+
				"behind after its own expiry looks exactly like this, with a current leaf",
			addr, strings.Join(expired, ", "))
	case len(expiring) > 0:
		return warn("PV-003", "CHAIN_EXPIRING",
			"%s serves a chain with something expiring inside %d days: %s",
			addr, o.ExpiryWarningDays, strings.Join(expiring, ", "))
	}
	return pass("PV-003", "nothing in the chain %s serves has expired or is close to it", addr)
}

// PV-004: revocation distribution points are reachable.
//
// A warning rather than a failure, and the reason is airgap: a site with no
// route to the internet cannot reach an OCSP responder, which is expected and
// has to be recorded rather than treated as a fault. What matters is that the
// handover document says so, because a client configured to hard-fail on
// revocation will refuse the connection.
func revocationReachable(ctx context.Context, chain []*x509.Certificate, o Options) Result {
	if len(chain) == 0 {
		return skip("PV-004", "no certificate was sent")
	}
	leaf := chain[0]

	points := append([]string{}, leaf.OCSPServer...)
	points = append(points, leaf.CRLDistributionPoints...)
	if len(points) == 0 {
		return Result{
			ID: "PV-004", Status: preflight.StatusPass, Severity: codes.SeverityInfo,
			Detail: "the certificate publishes no OCSP or CRL distribution point, so nothing " +
				"can check its revocation status and no client can hard-fail on it",
		}
	}

	var unreachable []string
	for _, p := range points {
		if !reachable(ctx, p, o.timeout()) {
			unreachable = append(unreachable, p)
		}
	}
	if len(unreachable) == 0 {
		return pass("PV-004", "every revocation distribution point is reachable: %s",
			strings.Join(points, ", "))
	}
	return warn("PV-004", "REVOCATION_UNREACHABLE",
		"these revocation distribution points cannot be reached from this machine: %s. "+
			"On a disconnected site that is expected and has to be recorded: a client configured "+
			"to hard-fail on revocation will refuse the connection, and the handover document "+
			"has to say the check must be disabled",
		strings.Join(unreachable, ", "))
}

func reachable(ctx context.Context, raw string, timeout time.Duration) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	host := u.Host
	if u.Port() == "" {
		if u.Scheme == "https" {
			host += ":443"
		} else {
			host += ":80"
		}
	}
	c, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(c, "tcp", host)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// PV-005: what comes back when the client sends no SNI.
//
// Old clients and some health checkers send none. What they get is whatever the
// gateway serves by default, which may be a certificate for another listener
// entirely -- and the failure looks like a name mismatch nobody can reproduce.
func withoutSNI(ctx context.Context, addr string, o Options) Result {
	state, err := handshake(ctx, addr, "", o)
	if err != nil {
		return warn("PV-005", "NO_SNI_REFUSED",
			"%s refuses a handshake with no SNI: %v; a client that sends none -- an old library, "+
				"a health checker -- cannot connect at all", addr, err)
	}
	if len(state.PeerCertificates) == 0 {
		return warn("PV-005", "NO_SNI_NO_CERT", "%s sends no certificate when SNI is absent", addr)
	}
	leaf := state.PeerCertificates[0]
	return Result{
		ID: "PV-005", Status: preflight.StatusPass, Severity: codes.SeverityInfo,
		Detail: fmt.Sprintf("with no SNI, %s serves %s (%s)", addr,
			leaf.Subject.CommonName, strings.Join(sansOf(leaf), ", ")),
	}
}

// PV-006: what happens on a direct connection to the address.
//
// Recorded rather than judged. A certificate that does not cover the address is
// normal -- names are what certificates are for -- but somebody debugging a
// monitoring check that connects by IP needs to know it before they start.
func byIPAddress(ctx context.Context, addr string, t Target, o Options) Result {
	state, err := handshake(ctx, addr, "", o)
	if err != nil || len(state.PeerCertificates) == 0 {
		return skip("PV-006", "the endpoint did not complete a handshake without SNI")
	}
	leaf := state.PeerCertificates[0]

	covered := false
	if ip := net.ParseIP(t.Address); ip != nil {
		for _, sanIP := range leaf.IPAddresses {
			if sanIP.Equal(ip) {
				covered = true
			}
		}
	}
	if covered {
		return Result{ID: "PV-006", Status: preflight.StatusPass, Severity: codes.SeverityInfo,
			Detail: fmt.Sprintf("the certificate served at %s covers the address itself", addr)}
	}
	return Result{
		ID: "PV-006", Status: preflight.StatusPass, Severity: codes.SeverityInfo,
		Detail: fmt.Sprintf("connecting to %s by address returns a certificate for %s, which does not "+
			"cover it; that is normal, and worth knowing before somebody debugs a monitoring check "+
			"that connects by address", addr, strings.Join(sansOf(leaf), ", ")),
	}
}

// PV-007: every listener hostname gets its own certificate.
//
// A gateway with several listeners can serve the right certificate for one and
// the default for the rest, and nothing in its status says so.
func perHostname(ctx context.Context, addr string, t Target, o Options) Result {
	if len(t.Hostnames) == 0 {
		return skip("PV-007", "no listener hostname was supplied to check")
	}

	var wrong []string
	var served []string
	for _, h := range t.Hostnames {
		name := exampleFor(h)
		state, err := handshake(ctx, addr, name, o)
		if err != nil || len(state.PeerCertificates) == 0 {
			wrong = append(wrong, fmt.Sprintf("%s: no certificate (%v)", name, err))
			continue
		}
		leaf := state.PeerCertificates[0]
		if err := leaf.VerifyHostname(name); err != nil {
			wrong = append(wrong, fmt.Sprintf("%s: served a certificate for %s",
				name, strings.Join(sansOf(leaf), ", ")))
			continue
		}
		served = append(served, name)
	}

	if len(wrong) > 0 {
		return fail("PV-007", "LISTENER_CERT_MISMATCH",
			"%s does not serve the right certificate for every listener: %s",
			addr, strings.Join(wrong, "; "))
	}
	return pass("PV-007", "every listener hostname gets a certificate that covers it: %s",
		strings.Join(served, ", "))
}

// PV-008: which TLS versions the endpoint will negotiate.
//
// Recorded because compatibility is a real constraint at a customer site: an
// endpoint that refuses everything below TLS 1.3 is correct and will not talk
// to the JDK 8 application somebody has not told you about.
func negotiable(ctx context.Context, addr, sni string, o Options) Result {
	versions := []struct {
		name string
		id   uint16
	}{
		{"TLS 1.0", tls.VersionTLS10},
		{"TLS 1.1", tls.VersionTLS11},
		{"TLS 1.2", tls.VersionTLS12},
		{"TLS 1.3", tls.VersionTLS13},
	}

	var ok, refused []string
	for _, v := range versions {
		_, err := handshakeWith(ctx, addr, &tls.Config{
			ServerName:         sni,
			InsecureSkipVerify: true, //nolint:gosec // measuring what negotiates, not trusting it
			MinVersion:         v.id,
			MaxVersion:         v.id,
		}, o)
		if err == nil {
			ok = append(ok, v.name)
		} else {
			refused = append(refused, v.name)
		}
	}

	if len(ok) == 0 {
		return warn("PV-008", "NO_VERSION_NEGOTIATED",
			"%s negotiated none of TLS 1.0 through 1.3 individually, which usually means "+
				"the handshake fails for a reason other than the version", addr)
	}
	// Anything below 1.2 being available is worth saying out loud: it is
	// deprecated everywhere and a scanner will report it.
	if contains(ok, "TLS 1.0") || contains(ok, "TLS 1.1") {
		return warn("PV-008", "WEAK_TLS_VERSION",
			"%s negotiates %s; TLS 1.0 and 1.1 are deprecated and any scanner will report them",
			addr, strings.Join(ok, ", "))
	}
	return Result{
		ID: "PV-008", Status: preflight.StatusPass, Severity: codes.SeverityInfo,
		Detail: fmt.Sprintf("%s negotiates %s and refuses %s", addr,
			strings.Join(ok, ", "), strings.Join(refused, ", ")),
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// exampleFor turns a listener hostname into a name a client would send.
//
// A wildcard is not a name anybody connects to, so a concrete label is
// substituted -- otherwise SNI carries "*.acme.internal" and no server matches
// it.
func exampleFor(hostname string) string {
	h := strings.TrimSpace(hostname)
	if strings.HasPrefix(h, "*.") {
		return "malmok-verify" + h[1:]
	}
	return h
}

func sansOf(c *x509.Certificate) []string {
	out := append([]string{}, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		out = append(out, ip.String())
	}
	if len(out) == 0 && c.Subject.CommonName != "" {
		out = append(out, "CN="+c.Subject.CommonName)
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func pass(id, format string, args ...any) Result {
	return Result{ID: id, Status: preflight.StatusPass, Severity: codes.SeverityInfo,
		Detail: fmt.Sprintf(format, args...)}
}

func skip(id, detail string) Result {
	return Result{ID: id, Status: preflight.StatusSkip, Severity: codes.SeverityInfo, Detail: detail}
}

func warn(id, code, format string, args ...any) Result {
	return Result{ID: id, Status: preflight.StatusFail, Severity: codes.SeverityWarn,
		Code: code, Detail: fmt.Sprintf(format, args...)}
}

func fail(id, code, format string, args ...any) Result {
	return Result{ID: id, Status: preflight.StatusFail, Severity: codes.SeverityBlock,
		Code: code, Detail: fmt.Sprintf(format, args...)}
}
