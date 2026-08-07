package preflight

import (
	"context"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"platform.ryxen.dev/platformctl/api/v1alpha1"
	"platform.ryxen.dev/platformctl/internal/codes"
)

// The outside world is an interface so the failure paths can be exercised
// rather than reasoned about. Every fake here stands for a real customer-site
// failure somebody has spent an afternoon on.

type fakeResolver struct {
	addrs []netip.Addr
	err   error
}

func (f fakeResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return f.addrs, f.err
}

// fakeDialer answers on a fixed set of addresses and refuses everything else.
type fakeDialer struct {
	open map[string]bool
}

func (f fakeDialer) DialContext(_ context.Context, _, address string) (net.Conn, error) {
	if f.open[address] {
		c, _ := net.Pipe()
		return c, nil
	}
	return nil, errors.New("connection refused")
}

func addrs(t *testing.T, ss ...string) []netip.Addr {
	t.Helper()
	out := make([]netip.Addr, 0, len(ss))
	for _, s := range ss {
		a, err := netip.ParseAddr(s)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func TestCheckRegistrationAddress(t *testing.T) {
	tests := []struct {
		name     string
		address  string
		resolver Resolver
		wantFail bool
		wantCode string
		wantIn   string
	}{
		{
			name:     "resolves",
			address:  "k8s-api.acme.internal",
			resolver: fakeResolver{addrs: addrs(t, "10.10.0.10")},
			wantIn:   "10.10.0.10",
		},
		{
			// A round-robin HA record is normal. It is reported rather than
			// flagged, because an operator who expected one VIP has learned
			// something either way.
			name:     "resolves to several addresses",
			address:  "k8s-api.acme.internal",
			resolver: fakeResolver{addrs: addrs(t, "10.10.0.11", "10.10.0.12")},
			wantIn:   "10.10.0.11, 10.10.0.12",
		},
		{
			name:     "does not resolve",
			address:  "k8s-api.acme.internal",
			resolver: fakeResolver{err: errors.New("no such host")},
			wantFail: true,
			wantCode: "DNS_NXDOMAIN",
			wantIn:   "every node joins through this name",
		},
		{
			name:     "resolves to nothing",
			address:  "k8s-api.acme.internal",
			resolver: fakeResolver{},
			wantFail: true,
			wantCode: "DNS_NXDOMAIN",
		},
		{
			// A literal needs no resolution, and the resolver must not be
			// consulted -- an airgapped site has no DNS to consult.
			name:     "a literal address",
			address:  "10.10.0.10",
			resolver: fakeResolver{err: errors.New("the resolver must not be called")},
			wantIn:   "literal",
		},
		{
			name:     "a name carrying a port",
			address:  "k8s-api.acme.internal:9345",
			resolver: fakeResolver{addrs: addrs(t, "10.10.0.10")},
			wantIn:   "10.10.0.10",
		},
		{
			name:     "empty",
			address:  "",
			resolver: fakeResolver{},
			wantFail: true,
			wantCode: "ADDRESS_MISSING",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := baseSpec()
			s.Topology.RegistrationAddress = tc.address
			p := &Prober{Resolver: tc.resolver, Dialer: fakeDialer{}, Timeout: time.Second}

			got := p.CheckRegistrationAddress(context.Background(), s)
			if got.Failed() != tc.wantFail {
				t.Fatalf("status is %s: %s", got.Status, got.Detail)
			}
			if tc.wantCode != "" && got.Code != tc.wantCode {
				t.Errorf("code is %q, want %q", got.Code, tc.wantCode)
			}
			if tc.wantIn != "" && !strings.Contains(got.Detail, tc.wantIn) {
				t.Errorf("the detail does not mention %q: %s", tc.wantIn, got.Detail)
			}
		})
	}
}

func TestCheckVIPFree(t *testing.T) {
	t.Run("nothing answers", func(t *testing.T) {
		s := baseSpec()
		s.Topology.VIP = &v1alpha1.VIPSpec{Address: "10.10.0.10"}
		p := &Prober{Dialer: fakeDialer{}, Timeout: time.Second}

		got := p.CheckVIPFree(context.Background(), s)
		if got.Failed() {
			t.Fatalf("PF-606 failed on a free address: %s", got.Detail)
		}
		// Silence is not proof, and the message must not claim it is.
		if !strings.Contains(got.Detail, "would look the same") {
			t.Errorf("the pass overstates what was measured: %s", got.Detail)
		}
	})

	t.Run("something already holds it", func(t *testing.T) {
		s := baseSpec()
		s.Topology.VIP = &v1alpha1.VIPSpec{Address: "10.10.0.10"}
		p := &Prober{Dialer: fakeDialer{open: map[string]bool{"10.10.0.10:6443": true}}, Timeout: time.Second}

		got := p.CheckVIPFree(context.Background(), s)
		if !got.Failed() {
			t.Fatalf("PF-606 passed on an address in use: %s", got.Detail)
		}
		if got.Code != "VIP_IN_USE" || !strings.Contains(got.Detail, "6443") {
			t.Errorf("the failure does not say what answered: %s", got.Detail)
		}
	})

	t.Run("no VIP configured", func(t *testing.T) {
		p := &Prober{Dialer: fakeDialer{}, Timeout: time.Second}
		if got := p.CheckVIPFree(context.Background(), baseSpec()); got.Status != StatusSkip {
			t.Errorf("status is %s, want skip", got.Status)
		}
	})
}

// A proxy that forwards plain requests and refuses CONNECT is the shape this
// probe exists for: it passes every curl anyone tries and fails every pull.
func TestCheckProxyConnect(t *testing.T) {
	tests := []struct {
		name     string
		reply    string
		wantFail bool
		wantCode string
	}{
		{name: "tunnels", reply: "HTTP/1.1 200 Connection established\r\n\r\n"},
		{name: "refuses CONNECT", reply: "HTTP/1.1 403 Forbidden\r\n\r\n", wantFail: true, wantCode: "PROXY_CONNECT_REFUSED"},
		{name: "demands authentication", reply: "HTTP/1.1 407 Proxy Authentication Required\r\n\r\n", wantFail: true, wantCode: "PROXY_CONNECT_REFUSED"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()

			go func() {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				buf := make([]byte, 512)
				_, _ = conn.Read(buf)
				_, _ = conn.Write([]byte(tc.reply))
			}()

			s := baseSpec()
			s.Network.Mode = v1alpha1.NetworkProxy
			s.Network.Proxy = &v1alpha1.ProxySpec{HTTPS: "http://" + ln.Addr().String()}
			p := &Prober{Dialer: &net.Dialer{}, Timeout: 2 * time.Second}

			got := p.CheckProxyConnect(context.Background(), s)
			if got.Failed() != tc.wantFail {
				t.Fatalf("status is %s: %s", got.Status, got.Detail)
			}
			if tc.wantCode != "" && got.Code != tc.wantCode {
				t.Errorf("code is %q, want %q", got.Code, tc.wantCode)
			}
		})
	}

	t.Run("skipped when the mode is not proxy", func(t *testing.T) {
		p := &Prober{Dialer: fakeDialer{}, Timeout: time.Second}
		if got := p.CheckProxyConnect(context.Background(), baseSpec()); got.Status != StatusSkip {
			t.Errorf("status is %s, want skip", got.Status)
		}
	})

	t.Run("proxy mode with no proxy configured", func(t *testing.T) {
		s := baseSpec()
		s.Network.Mode = v1alpha1.NetworkProxy
		p := &Prober{Dialer: fakeDialer{}, Timeout: time.Second}
		if got := p.CheckProxyConnect(context.Background(), s); !got.Failed() {
			t.Errorf("status is %s, want fail", got.Status)
		}
	})
}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

func byID(t *testing.T, rs []ProbeResult, id string) ProbeResult {
	t.Helper()
	for _, r := range rs {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no result for %s", id)
	return ProbeResult{}
}

// registrySpec points a document at a test server.
func registrySpec(host string) v1alpha1.ClusterSpec {
	s := baseSpec()
	s.Registry = v1alpha1.RegistrySpec{
		Mode:                  v1alpha1.RegistryExternal,
		SystemDefaultRegistry: host,
	}
	return s
}

func TestCheckRegistry(t *testing.T) {
	t.Run("reachable and anonymous", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		host, ca := hostAndCA(t, srv)
		p := &Prober{Dialer: &net.Dialer{}, Timeout: 3 * time.Second}
		got := p.CheckRegistry(context.Background(), registrySpec(host), RegistryCredentials{CACert: ca})

		if r := byID(t, got, "PF-701"); r.Failed() {
			t.Errorf("PF-701 failed: %s", r.Detail)
		}
		if r := byID(t, got, "PF-702"); r.Failed() {
			t.Errorf("PF-702 failed: %s", r.Detail)
		}
		// Reachability from this machine is not reachability from the nodes,
		// and the message has to say so rather than imply otherwise.
		if r := byID(t, got, "PF-701"); !strings.Contains(r.Detail, "not proof the nodes") {
			t.Errorf("PF-701 overstates what was measured: %s", r.Detail)
		}
	})

	t.Run("credentials accepted", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, pw, ok := r.BasicAuth()
			if !ok {
				w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if u == "robot" && pw == "s3cret" {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()

		host, ca := hostAndCA(t, srv)
		p := &Prober{Dialer: &net.Dialer{}, Timeout: 3 * time.Second}
		got := p.CheckRegistry(context.Background(), registrySpec(host),
			RegistryCredentials{Username: "robot", Password: "s3cret", CACert: ca})

		if r := byID(t, got, "PF-703"); r.Failed() {
			t.Errorf("PF-703 failed: %s", r.Detail)
		}
	})

	t.Run("credentials rejected", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()

		host, ca := hostAndCA(t, srv)
		p := &Prober{Dialer: &net.Dialer{}, Timeout: 3 * time.Second}
		got := p.CheckRegistry(context.Background(), registrySpec(host),
			RegistryCredentials{Username: "robot", Password: "stale", CACert: ca})

		r := byID(t, got, "PF-703")
		if !r.Failed() || r.Code != "CREDENTIALS_REJECTED" {
			t.Fatalf("PF-703 is %s/%s: %s", r.Status, r.Code, r.Detail)
		}
		// The consequence is what makes an operator act on it.
		if !strings.Contains(r.Detail, "every image pull on every node") {
			t.Errorf("the failure does not say what it costs: %s", r.Detail)
		}
	})

	t.Run("authentication required and none supplied", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()

		host, ca := hostAndCA(t, srv)
		p := &Prober{Dialer: &net.Dialer{}, Timeout: 3 * time.Second}
		got := p.CheckRegistry(context.Background(), registrySpec(host), RegistryCredentials{CACert: ca})

		r := byID(t, got, "PF-703")
		if !r.Failed() || r.Code != "CREDENTIALS_MISSING" {
			t.Fatalf("PF-703 is %s/%s: %s", r.Status, r.Code, r.Detail)
		}
	})

	// The misdiagnosis this probe exists to prevent: an untrusted CA reported
	// as unreachable sends the operator to the firewall.
	t.Run("untrusted CA is not reported as unreachable", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		host, _ := hostAndCA(t, srv)
		p := &Prober{Dialer: &net.Dialer{}, Timeout: 3 * time.Second}
		got := p.CheckRegistry(context.Background(), registrySpec(host), RegistryCredentials{})

		if r := byID(t, got, "PF-701"); r.Failed() {
			t.Errorf("PF-701 blamed reachability for a verification failure: %s", r.Detail)
		}
		r := byID(t, got, "PF-702")
		if !r.Failed() || r.Code != "CA_UNKNOWN" {
			t.Fatalf("PF-702 is %s/%s: %s", r.Status, r.Code, r.Detail)
		}
		if !strings.Contains(r.Detail, "registry.caCert") {
			t.Errorf("the failure does not name the field to set: %s", r.Detail)
		}
	})

	t.Run("unreachable", func(t *testing.T) {
		p := &Prober{Dialer: &net.Dialer{}, Timeout: time.Second}
		// Port 1 on the loopback: nothing listens there and nothing will.
		got := p.CheckRegistry(context.Background(), registrySpec("127.0.0.1:1"), RegistryCredentials{})

		r := byID(t, got, "PF-701")
		if !r.Failed() || r.Code != "REGISTRY_UNREACHABLE" {
			t.Fatalf("PF-701 is %s/%s: %s", r.Status, r.Code, r.Detail)
		}
		if byID(t, got, "PF-702").Status != StatusSkip {
			t.Error("PF-702 claimed a verdict without a connection")
		}
	})

	t.Run("insecure is a warning, not a pass in silence", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		host, _ := hostAndCA(t, srv)
		s := registrySpec(host)
		yes := true
		s.Registry.Insecure = &yes

		p := &Prober{Dialer: &net.Dialer{}, Timeout: 3 * time.Second}
		got := p.CheckRegistry(context.Background(), s, RegistryCredentials{})

		r := byID(t, got, "PF-702")
		if r.Severity != codes.SeverityWarn {
			t.Errorf("PF-702 severity is %s, want warn: %s", r.Severity, r.Detail)
		}
	})

	t.Run("the embedded registry cannot be probed before it exists", func(t *testing.T) {
		s := baseSpec()
		s.Registry = v1alpha1.RegistrySpec{Mode: v1alpha1.RegistryEmbedded, SystemDefaultRegistry: "127.0.0.1:1"}
		p := &Prober{Dialer: &net.Dialer{}, Timeout: time.Second}

		for _, r := range p.CheckRegistry(context.Background(), s, RegistryCredentials{}) {
			if r.Status != StatusSkip {
				t.Errorf("%s is %s, want skip: %s", r.ID, r.Status, r.Detail)
			}
		}
	})

	t.Run("no registry configured", func(t *testing.T) {
		p := &Prober{Dialer: &net.Dialer{}, Timeout: time.Second}
		for _, r := range p.CheckRegistry(context.Background(), baseSpec(), RegistryCredentials{}) {
			if r.Status != StatusSkip {
				t.Errorf("%s is %s, want skip", r.ID, r.Status)
			}
		}
	})
}

// A document that mirrors specific upstreams without a system default still
// points image pulls somewhere, and the probe has to find it.
func TestRegistryHostFallsBackToMirrors(t *testing.T) {
	s := baseSpec()
	s.Registry.Mirrors = map[string][]string{
		"docker.io": {"https://harbor.acme.internal"},
	}
	if got := registryHost(s); got != "harbor.acme.internal" {
		t.Errorf("registryHost = %q", got)
	}

	s.Registry.Mirrors = map[string][]string{"docker.io": {"harbor.acme.internal:5000"}}
	if got := registryHost(s); got != "harbor.acme.internal:5000" {
		t.Errorf("registryHost = %q", got)
	}
}

// hostAndCA returns the test server's host:port and its CA in PEM.
func hostAndCA(t *testing.T, srv *httptest.Server) (string, []byte) {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host, pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: srv.Certificate().Raw,
	})
}
