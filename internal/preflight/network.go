package preflight

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/ryxenix/malmok/api/v1alpha1"
	"github.com/ryxenix/malmok/internal/codes"
)

// These probes run from the machine executing the tool rather than from a node.
//
// That is a real limitation and the messages say so where it matters: a
// registry the bastion can reach is not proof the nodes can. What this buys is
// finding the common failures -- a name that does not resolve, a registry whose
// CA was never supplied, credentials that were rotated -- before anybody waits
// on an install window.
//
// Every dependency on the outside world is an interface, so the failure paths
// are unit tested rather than reasoned about.

// Resolver looks names up.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Dialer opens TCP connections.
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// Prober holds what the local network probes need.
type Prober struct {
	Resolver Resolver
	Dialer   Dialer
	// Timeout bounds each individual attempt. Zero means five seconds, which is
	// long enough for a slow customer DNS and short enough that a whole preflight
	// against an unreachable site still finishes.
	Timeout time.Duration
}

// NewProber builds a prober against the real network.
func NewProber() *Prober {
	return &Prober{
		Resolver: net.DefaultResolver,
		Dialer:   &net.Dialer{},
		Timeout:  5 * time.Second,
	}
}

func (p *Prober) timeout() time.Duration {
	if p.Timeout <= 0 {
		return 5 * time.Second
	}
	return p.Timeout
}

// CheckRegistrationAddress implements PF-603.
//
// Every node joins through this name. When it does not resolve the join fails
// on every node at once, and the error RKE2 prints at that point names a
// connection rather than a DNS record, which sends people to the firewall.
func (p *Prober) CheckRegistrationAddress(ctx context.Context, spec v1alpha1.ClusterSpec) ProbeResult {
	addr := strings.TrimSpace(spec.Topology.RegistrationAddress)
	if addr == "" {
		return fail("PF-603", "ADDRESS_MISSING", "topology.registrationAddress is empty")
	}

	// Strip a port if the document carried one; the name is what resolves.
	if host, _, err := net.SplitHostPort(addr); err == nil {
		addr = host
	}

	if ip, err := netip.ParseAddr(addr); err == nil {
		// A literal address resolves by definition. Whether it should be a
		// literal at all is ADR-008's question and the loader already asks it.
		return ProbeResult{
			ID: "PF-603", Status: StatusPass, Severity: codes.SeverityInfo,
			Detail: fmt.Sprintf("the registration address is the literal %s and needs no resolution", ip),
		}
	}

	c, cancel := context.WithTimeout(ctx, p.timeout())
	defer cancel()

	ips, err := p.Resolver.LookupNetIP(c, "ip", addr)
	if err != nil {
		return fail("PF-603", "DNS_NXDOMAIN", fmt.Sprintf(
			"the registration address %s does not resolve from this machine: %v; "+
				"every node joins through this name, so the join fails on all of them at once", addr, err))
	}
	if len(ips) == 0 {
		return fail("PF-603", "DNS_NXDOMAIN", fmt.Sprintf(
			"the registration address %s resolved to nothing", addr))
	}

	// More than one address is normal for a round-robin HA record and is not a
	// problem, but it is worth stating: an operator who expected a single VIP
	// has learned something.
	strs := make([]string, 0, len(ips))
	for _, ip := range ips {
		strs = append(strs, ip.String())
	}
	sort.Strings(strs)

	// Resolving is not the same as resolving to this cluster. A name that
	// answers with somebody else's address passes every DNS check and then
	// fails every join, because the joining node dials the supervisor at
	// whatever came back. It is the same class of mistake as a stale record
	// left from a previous build.
	if !pointsAtCluster(ips, spec) {
		return ProbeResult{
			ID: "PF-603", Status: StatusFail, Severity: codes.SeverityBlock,
			Code: "DNS_ELSEWHERE",
			Detail: fmt.Sprintf(
				"the registration address %s resolves to %s, which is none of this cluster's nodes, "+
					"its VIP or its load balancer pool; every node joins through this name, so the join "+
					"would be attempted against something else entirely",
				addr, strings.Join(strs, ", ")),
			Evidence: strings.Join(strs, ","),
		}
	}

	return ProbeResult{
		ID: "PF-603", Status: StatusPass, Severity: codes.SeverityInfo,
		Detail:   fmt.Sprintf("the registration address %s resolves to %s", addr, strings.Join(strs, ", ")),
		Evidence: strings.Join(strs, ","),
	}
}

// pointsAtCluster reports whether any resolved address belongs to this cluster.
//
// The VIP and the load balancer pool count because neither exists yet at
// preflight time: a record pointing at an address the document has reserved is
// correct and simply early, which is the whole reason PF-612 asks for the
// gateway address to be pinned before the install.
func pointsAtCluster(ips []netip.Addr, spec v1alpha1.ClusterSpec) bool {
	claimed := map[netip.Addr]bool{}
	add := func(s string) {
		if a, err := netip.ParseAddr(strings.TrimSpace(s)); err == nil {
			claimed[a] = true
		}
	}
	for _, n := range append(append([]v1alpha1.NodeSpec{}, spec.Topology.Servers...), spec.Topology.Agents...) {
		add(n.Host)
		add(n.NodeIP)
	}
	if v := spec.Topology.VIP; v != nil {
		add(v.Address)
	}
	for _, gw := range spec.Gateway.Gateways {
		add(gw.Address)
	}

	var pools []netip.Prefix
	for _, raw := range spec.Kubernetes.Dataplane.LoadBalancerPool {
		if p, err := netip.ParsePrefix(strings.TrimSpace(raw)); err == nil {
			pools = append(pools, p)
		}
	}

	// Nothing to compare against means nothing can be said. A document with no
	// addressed node is not a document this check can judge.
	if len(claimed) == 0 && len(pools) == 0 {
		return true
	}

	for _, ip := range ips {
		if claimed[ip] {
			return true
		}
		for _, p := range pools {
			if p.Contains(ip) {
				return true
			}
		}
	}
	return false
}

// CheckVIPFree implements PF-606.
//
// A VIP that already answers belongs to something else. Assigning it anyway
// produces two hosts claiming one address, and what breaks is whatever was
// there first -- usually while the installer reports success.
// heldBy are the addresses the nodes reported carrying, keyed by host. A VIP
// among them is held by this cluster rather than by somebody else.
func (p *Prober) CheckVIPFree(ctx context.Context, spec v1alpha1.ClusterSpec, heldBy map[string][]string) ProbeResult {
	v := spec.Topology.VIP
	if v == nil || strings.TrimSpace(v.Address) == "" {
		return skipped("PF-606", "no VIP is configured")
	}
	addr := strings.TrimSpace(v.Address)

	// Once this tool has built the cluster, the VIP answers because kube-vip is
	// serving it. Reporting that as "somebody else has your address" would
	// block every run after the first, the same way PF-802 did.
	for host, addrs := range heldBy {
		for _, a := range addrs {
			if a == addr {
				return ProbeResult{
					ID: "PF-606", Status: StatusPass, Severity: codes.SeverityInfo,
					Detail: fmt.Sprintf("%s is carried by %s, which is a node in this document; "+
						"the address is this cluster's own", addr, host),
				}
			}
		}
	}

	// The ports something would answer on if the address were already a
	// Kubernetes control plane, plus the two that say a host is simply there.
	ports := []int{6443, 9345, 443, 22}
	var answered []int
	for _, port := range ports {
		c, cancel := context.WithTimeout(ctx, p.timeout())
		conn, err := p.Dialer.DialContext(c, "tcp", net.JoinHostPort(addr, fmt.Sprint(port)))
		cancel()
		if err == nil {
			conn.Close()
			answered = append(answered, port)
		}
	}

	if len(answered) == 0 {
		return ProbeResult{
			ID: "PF-606", Status: StatusPass, Severity: codes.SeverityInfo,
			// Silence is not proof the address is free -- a host that drops
			// packets is silent too -- and the message does not claim it is.
			Detail: fmt.Sprintf("nothing answered on %s; the address appears unused, "+
				"though a host that drops unsolicited packets would look the same", addr),
		}
	}

	var ps []string
	for _, port := range answered {
		ps = append(ps, fmt.Sprint(port))
	}
	return fail("PF-606", "VIP_IN_USE", fmt.Sprintf(
		"something already answers on the VIP %s (port %s); assigning it would leave two hosts claiming one address",
		addr, strings.Join(ps, ", ")))
}

// CheckProxyConnect implements PF-608.
//
// An HTTP proxy that forwards plain requests but refuses CONNECT looks healthy
// to every curl anyone tries, and then fails every image pull. The method is
// the thing to test, not reachability.
func (p *Prober) CheckProxyConnect(ctx context.Context, spec v1alpha1.ClusterSpec) ProbeResult {
	if spec.Network.Mode != v1alpha1.NetworkProxy {
		return skipped("PF-608", "the network mode is not proxy")
	}
	px := spec.Network.Proxy
	if px == nil || strings.TrimSpace(px.HTTPS) == "" {
		return fail("PF-608", "PROXY_MISSING", "network.mode is proxy but network.proxy.https is empty")
	}

	u, err := url.Parse(px.HTTPS)
	if err != nil || u.Host == "" {
		return fail("PF-608", "PROXY_MISSING", fmt.Sprintf("network.proxy.https %q is not a URL", px.HTTPS))
	}

	// The target is the registry when there is one, because that is what the
	// proxy will actually be asked to tunnel.
	target := "registry-1.docker.io:443"
	if h := registryHost(spec); h != "" {
		target = net.JoinHostPort(h, "443")
	}

	c, cancel := context.WithTimeout(ctx, p.timeout())
	defer cancel()

	conn, err := p.Dialer.DialContext(c, "tcp", u.Host)
	if err != nil {
		return fail("PF-608", "PROXY_UNREACHABLE", fmt.Sprintf(
			"the proxy %s is not reachable from this machine: %v", u.Host, err))
	}
	defer conn.Close()

	if dl, ok := c.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
	if u.User != nil {
		pw, _ := u.User.Password()
		cred := base64.StdEncoding.EncodeToString([]byte(u.User.Username() + ":" + pw))
		req += "Proxy-Authorization: Basic " + cred + "\r\n"
	}
	req += "\r\n"

	if _, err := io.WriteString(conn, req); err != nil {
		return fail("PF-608", "PROXY_CONNECT_REFUSED", fmt.Sprintf(
			"the proxy %s accepted the connection and then dropped it: %v", u.Host, err))
	}

	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil && n == 0 {
		return fail("PF-608", "PROXY_CONNECT_REFUSED", fmt.Sprintf(
			"the proxy %s did not answer a CONNECT for %s: %v", u.Host, target, err))
	}
	line := strings.SplitN(string(buf[:n]), "\r\n", 2)[0]

	if !strings.Contains(line, " 200") {
		return fail("PF-608", "PROXY_CONNECT_REFUSED", fmt.Sprintf(
			"the proxy %s refused CONNECT for %s: %q; plain requests through this proxy will still work, "+
				"which is why this is usually found during the first image pull", u.Host, target, line))
	}
	return ProbeResult{
		ID: "PF-608", Status: StatusPass, Severity: codes.SeverityInfo,
		Detail: fmt.Sprintf("the proxy %s tunnels CONNECT to %s", u.Host, target),
	}
}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

// RegistryCredentials are resolved by the caller, because reading a SourceRef
// needs the document's directory and its secret policy, which is the loader's
// business rather than this package's.
type RegistryCredentials struct {
	Username string
	Password string
	// CACert is the PEM the registry's certificate should verify against.
	CACert []byte
}

// CheckRegistry implements PF-701, PF-702 and PF-703 in one pass.
//
// They are one pass because they are one connection: reachability, whether the
// certificate verifies against the supplied CA, and whether the credentials are
// accepted are three answers to the same request, and asking three times would
// report a rotated password as a reachability problem.
func (p *Prober) CheckRegistry(ctx context.Context, spec v1alpha1.ClusterSpec, creds RegistryCredentials) []ProbeResult {
	host := registryHost(spec)
	if host == "" {
		return []ProbeResult{
			skipped("PF-701", "no private registry is configured"),
			skipped("PF-702", "no private registry is configured"),
			skipped("PF-703", "no private registry is configured"),
		}
	}
	if spec.Registry.Mode == v1alpha1.RegistryEmbedded {
		return []ProbeResult{
			skipped("PF-701", "the embedded registry is created during the install and cannot be probed before it"),
			skipped("PF-702", "the embedded registry is created during the install"),
			skipped("PF-703", "the embedded registry is created during the install"),
		}
	}

	insecure := spec.Registry.Insecure != nil && *spec.Registry.Insecure

	tlsCfg := &tls.Config{InsecureSkipVerify: insecure} //nolint:gosec // the field is the operator's explicit choice
	var caSupplied bool
	if len(creds.CACert) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(creds.CACert) {
			return []ProbeResult{
				skipped("PF-701", "the registry CA could not be parsed"),
				fail("PF-702", "CA_UNREADABLE", "registry.caCert holds no PEM certificate"),
				skipped("PF-703", "the registry CA could not be parsed"),
			}
		}
		tlsCfg.RootCAs = pool
		caSupplied = true
	}

	client := &http.Client{
		Timeout: p.timeout(),
		Transport: &http.Transport{
			DialContext:     p.Dialer.DialContext,
			TLSClientConfig: tlsCfg,
			Proxy:           http.ProxyFromEnvironment,
		},
	}

	// /v2/ is the registry API's own liveness endpoint: it answers 200 when
	// anonymous reads are allowed and 401 with a challenge when they are not.
	endpoint := "https://" + host + "/v2/"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return []ProbeResult{fail("PF-701", "REGISTRY_UNREACHABLE", err.Error())}
	}

	resp, err := client.Do(req)
	if err != nil {
		return p.explainRegistryError(host, endpoint, err, insecure, caSupplied)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	reach := ProbeResult{
		ID: "PF-701", Status: StatusPass, Severity: codes.SeverityInfo,
		Detail: fmt.Sprintf("the registry %s answered on /v2/ from this machine, "+
			"which is not proof the nodes can reach it", host),
		Evidence: resp.Status,
	}

	verify := ProbeResult{
		ID: "PF-702", Status: StatusPass, Severity: codes.SeverityInfo,
		Detail: "the registry certificate verified against the supplied CA",
	}
	switch {
	case insecure:
		verify = ProbeResult{
			ID: "PF-702", Status: StatusPass, Severity: codes.SeverityWarn,
			Code: "TLS_UNVERIFIED",
			Detail: fmt.Sprintf("registry.insecure is set, so the certificate of %s was not verified; "+
				"every node will pull images over a connection nothing authenticates", host),
		}
	case !caSupplied:
		verify = ProbeResult{
			ID: "PF-702", Status: StatusPass, Severity: codes.SeverityInfo,
			Detail: fmt.Sprintf("the registry certificate verified against the host trust store; " +
				"no registry.caCert was supplied, so the nodes must trust it the same way"),
		}
	}

	auth := p.checkRegistryAuth(ctx, client, endpoint, host, resp.StatusCode, creds)
	return []ProbeResult{reach, verify, auth}
}

// explainRegistryError turns a transport error into the probe that is actually
// at fault.
//
// A certificate error reported as unreachable is the single most expensive
// misdiagnosis here: it sends an operator to the firewall for a problem that is
// one missing CA file.
func (p *Prober) explainRegistryError(host, endpoint string, err error, insecure, caSupplied bool) []ProbeResult {
	var unknownAuthority x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var certInvalid x509.CertificateInvalidError

	switch {
	case errors.As(err, &unknownAuthority):
		detail := fmt.Sprintf("the certificate of %s is signed by an authority this machine does not trust", host)
		if !caSupplied {
			detail += "; supply the registry's CA in registry.caCert -- this is the most common private registry misinstall, " +
				"and on the nodes it surfaces as an opaque x509 error on every image pull"
		}
		return []ProbeResult{
			{ID: "PF-701", Status: StatusPass, Severity: codes.SeverityInfo,
				Detail: fmt.Sprintf("%s accepted a connection; the failure was in verification, not reachability", host)},
			fail("PF-702", "CA_UNKNOWN", detail),
			skipped("PF-703", "the connection was not established"),
		}

	case errors.As(err, &hostErr):
		return []ProbeResult{
			{ID: "PF-701", Status: StatusPass, Severity: codes.SeverityInfo,
				Detail: fmt.Sprintf("%s accepted a connection", host)},
			fail("PF-702", "CERT_NAME_MISMATCH", fmt.Sprintf(
				"the certificate of %s does not cover that name: %v", host, hostErr)),
			skipped("PF-703", "the connection was not established"),
		}

	case errors.As(err, &certInvalid):
		return []ProbeResult{
			{ID: "PF-701", Status: StatusPass, Severity: codes.SeverityInfo,
				Detail: fmt.Sprintf("%s accepted a connection", host)},
			fail("PF-702", "CERT_INVALID", fmt.Sprintf("the certificate of %s is not usable: %v", host, certInvalid)),
			skipped("PF-703", "the connection was not established"),
		}
	}

	return []ProbeResult{
		fail("PF-701", "REGISTRY_UNREACHABLE", fmt.Sprintf(
			"%s is not reachable from this machine: %v", endpoint, err)),
		skipped("PF-702", "the registry was not reachable"),
		skipped("PF-703", "the registry was not reachable"),
	}
}

// checkRegistryAuth implements PF-703 against an already-established client.
func (p *Prober) checkRegistryAuth(ctx context.Context, client *http.Client, endpoint, host string, anonStatus int, creds RegistryCredentials) ProbeResult {
	if creds.Username == "" && creds.Password == "" {
		if anonStatus == http.StatusUnauthorized {
			return fail("PF-703", "CREDENTIALS_MISSING", fmt.Sprintf(
				"%s requires authentication and no registry.username or registry.password was supplied", host))
		}
		return skipped("PF-703", "no registry credentials were supplied and the registry allowed an anonymous read")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fail("PF-703", "CREDENTIALS_REJECTED", err.Error())
	}
	req.SetBasicAuth(creds.Username, creds.Password)

	resp, err := client.Do(req)
	if err != nil {
		return fail("PF-703", "CREDENTIALS_REJECTED", fmt.Sprintf(
			"the authenticated request to %s failed: %v", host, err))
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return fail("PF-703", "CREDENTIALS_REJECTED", fmt.Sprintf(
			"%s rejected the supplied credentials for %q (%s); "+
				"a rotated registry password stops every image pull on every node",
			host, creds.Username, resp.Status))
	case resp.StatusCode >= 400:
		return fail("PF-703", "CREDENTIALS_REJECTED", fmt.Sprintf(
			"%s answered %s to an authenticated request", host, resp.Status))
	}
	return ProbeResult{
		ID: "PF-703", Status: StatusPass, Severity: codes.SeverityInfo,
		Detail:   fmt.Sprintf("%s accepted the credentials for %q", host, creds.Username),
		Evidence: resp.Status,
	}
}

// registryHost is the host:port the document points image pulls at.
func registryHost(spec v1alpha1.ClusterSpec) string {
	if h := strings.TrimSpace(spec.Registry.SystemDefaultRegistry); h != "" {
		return h
	}
	// Fall back to the first mirror endpoint, which is where a document that
	// mirrors specific upstreams without setting a system default points.
	keys := make([]string, 0, len(spec.Registry.Mirrors))
	for k := range spec.Registry.Mirrors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, ep := range spec.Registry.Mirrors[k] {
			u, err := url.Parse(ep)
			if err == nil && u.Host != "" {
				return u.Host
			}
			if !strings.Contains(ep, "://") && strings.TrimSpace(ep) != "" {
				return strings.TrimSpace(ep)
			}
		}
	}
	return ""
}

func skipped(id, detail string) ProbeResult {
	return ProbeResult{ID: id, Status: StatusSkip, Severity: codes.SeverityInfo, Detail: detail}
}
