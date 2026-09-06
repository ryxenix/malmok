package preflight

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ryxenix/malmok/api/v1alpha1"
	"github.com/ryxenix/malmok/internal/codes"
)

// pkiBase is the reference clock, so "expiring" means the same thing whenever
// the test runs.
var pkiBase = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

type testCA struct {
	cert *x509.Certificate
	der  []byte
	key  crypto.Signer
}

var pkiSerial int64

func mkCert(t *testing.T, parent *testCA, cn string, isCA bool, notBefore, notAfter time.Time) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pkiSerial++
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(pkiSerial),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		BasicConstraintsValid: true,
		IsCA:                  isCA,
	}
	if isCA {
		tmpl.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
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
	return testCA{cert: c, der: der, key: key}
}

func pemCert(c testCA) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.der})
}

func pemKey(t *testing.T, c testCA) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(c.key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// privateCASpec is a document that uses a private CA.
func privateCASpec() v1alpha1.ClusterSpec {
	s := baseSpec()
	s.PKI = v1alpha1.PKISpec{Mode: v1alpha1.PKIPrivateCA, Domain: "acme.internal"}
	return s
}

func TestCheckPKIMaterial(t *testing.T) {
	root := mkCert(t, nil, "Acme Root CA", true, pkiBase.AddDate(-1, 0, 0), pkiBase.AddDate(9, 0, 0))
	inter := mkCert(t, &root, "Acme Issuing CA", true, pkiBase.AddDate(-1, 0, 0), pkiBase.AddDate(4, 0, 0))

	tests := []struct {
		name     string
		material PKIMaterial
		spec     func() v1alpha1.ClusterSpec
		id       string
		wantCode string
		// wantStatus defaults to fail.
		wantStatus Status
		wantIn     string
	}{
		{
			name: "a healthy private CA",
			material: PKIMaterial{
				RootCert: pemCert(root), IntermediateCert: pemCert(inter), IntermediateKey: pemKey(t, inter),
			},
			id: "PF-704", wantStatus: StatusPass,
		},
		{
			name:     "a root with no intermediate cannot issue anything",
			material: PKIMaterial{RootCert: pemCert(root)},
			id:       "PF-704", wantCode: "INTERMEDIATE_MISSING", wantIn: "must stay offline",
		},
		{
			name: "the intermediate belongs to a different root",
			material: PKIMaterial{
				RootCert:         pemCert(mkCert(t, nil, "Other Root CA", true, pkiBase.AddDate(-1, 0, 0), pkiBase.AddDate(9, 0, 0))),
				IntermediateCert: pemCert(inter),
			},
			id: "PF-704", wantCode: "INTERMEDIATE_UNCHAINED", wantIn: "Acme Root CA",
		},
		{
			name: "the intermediate has expired",
			material: func() PKIMaterial {
				old := mkCert(t, &root, "Acme Issuing CA", true, pkiBase.AddDate(-2, 0, 0), pkiBase.AddDate(0, 0, -1))
				return PKIMaterial{RootCert: pemCert(root), IntermediateCert: pemCert(old)}
			}(),
			id: "PF-704", wantCode: "INTERMEDIATE_EXPIRED",
		},
		{
			// Every leaf it issues is capped at the issuer's date, so this is a
			// failure that arrives months later during a routine renewal.
			name: "the intermediate expires soon",
			material: func() PKIMaterial {
				soon := mkCert(t, &root, "Acme Issuing CA", true, pkiBase.AddDate(-1, 0, 0), pkiBase.AddDate(0, 1, 0))
				return PKIMaterial{RootCert: pemCert(root), IntermediateCert: pemCert(soon)}
			}(),
			id: "PF-704", wantCode: "INTERMEDIATE_EXPIRING", wantIn: "outlive their issuer",
		},
		{
			name: "the intermediate key belongs to something else",
			material: func() PKIMaterial {
				other := mkCert(t, &root, "Other Issuing CA", true, pkiBase.AddDate(-1, 0, 0), pkiBase.AddDate(4, 0, 0))
				return PKIMaterial{
					RootCert: pemCert(root), IntermediateCert: pemCert(inter), IntermediateKey: pemKey(t, other),
				}
			}(),
			id: "PF-704", wantCode: "INTERMEDIATE_KEY_MISMATCH",
		},
		{
			name: "a certificate is not valid yet",
			material: func() PKIMaterial {
				future := mkCert(t, &root, "Acme Issuing CA", true, pkiBase.AddDate(0, 1, 0), pkiBase.AddDate(4, 0, 0))
				return PKIMaterial{RootCert: pemCert(root), IntermediateCert: pemCert(future)}
			}(),
			id: "PF-705", wantCode: "NOT_YET_VALID", wantIn: "PF-501",
		},
		{
			// The one that matters most: an offline root key that has reached a
			// build directory is not offline any more.
			name: "the root private key is in the material",
			material: PKIMaterial{
				RootCert: pemCert(root), IntermediateCert: pemCert(inter), IntermediateKey: pemKey(t, root),
			},
			id: "PF-706", wantCode: "ROOT_KEY_PRESENT", wantIn: "no longer in it",
		},
		{
			name:     "no material supplied",
			material: PKIMaterial{},
			id:       "PF-706", wantStatus: StatusSkip,
		},
		{
			name: "pki.mode none skips the lot",
			spec: func() v1alpha1.ClusterSpec {
				s := baseSpec()
				s.PKI = v1alpha1.PKISpec{Mode: v1alpha1.PKINone}
				return s
			},
			material: PKIMaterial{RootCert: pemCert(root)},
			id:       "PF-704", wantStatus: StatusSkip, wantIn: "issues no certificates",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := privateCASpec()
			if tc.spec != nil {
				spec = tc.spec()
			}
			got := byID(t, CheckPKIMaterial(spec, tc.material, pkiBase), tc.id)

			want := tc.wantStatus
			if want == "" {
				want = StatusFail
			}
			if got.Status != want {
				t.Fatalf("%s is %s, want %s: %s", tc.id, got.Status, want, got.Detail)
			}
			if tc.wantCode != "" && got.Code != tc.wantCode {
				t.Errorf("%s code is %q, want %q: %s", tc.id, got.Code, tc.wantCode, got.Detail)
			}
			if tc.wantIn != "" && !strings.Contains(got.Detail, tc.wantIn) {
				t.Errorf("%s detail does not mention %q: %s", tc.id, tc.wantIn, got.Detail)
			}
		})
	}
}

// The root key finding has to block. A warning on this is the same as no check:
// the key is already where it should not be, and the run must not continue as
// though nothing happened.
func TestRootKeyInPKIMaterialBlocks(t *testing.T) {
	root := mkCert(t, nil, "Acme Root CA", true, pkiBase.AddDate(-1, 0, 0), pkiBase.AddDate(9, 0, 0))
	got := byID(t, CheckPKIMaterial(privateCASpec(),
		PKIMaterial{RootCert: pemCert(root), IntermediateKey: pemKey(t, root)}, pkiBase), "PF-706")

	if got.Severity != codes.SeverityBlock {
		t.Errorf("PF-706 severity is %s, want block", got.Severity)
	}
}

// ---------------------------------------------------------------------------
// PF-708
// ---------------------------------------------------------------------------

func acmeSpec(mode v1alpha1.PKIMode) v1alpha1.ClusterSpec {
	s := baseSpec()
	s.PKI = v1alpha1.PKISpec{
		Mode:   mode,
		Domain: "acme.co.kr",
		ACME:   &v1alpha1.ACMESpec{Email: "ops@acme.co.kr", DNSProvider: "cloudflare"},
	}
	s.Gateway.Gateways = []v1alpha1.Gateway{{Name: "public", Address: "203.0.113.10"}}
	return s
}

func TestCheckACME(t *testing.T) {
	// A listener the dialer can actually reach, standing in for the CA.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	reachable := "https://" + ln.Addr().String() + "/directory"

	tests := []struct {
		name     string
		spec     func() v1alpha1.ClusterSpec
		token    bool
		wantCode string
		// wantStatus defaults to fail.
		wantStatus Status
		wantIn     string
	}{
		{
			name: "dns-01 fully configured",
			spec: func() v1alpha1.ClusterSpec {
				s := acmeSpec(v1alpha1.PKIACMEDNS01)
				s.PKI.ACME.Server = reachable
				return s
			},
			token: true, wantStatus: StatusPass,
		},
		{
			// The cheapest possible finding and the most expensive to discover
			// during an install window.
			name: "acme in an airgap",
			spec: func() v1alpha1.ClusterSpec {
				s := acmeSpec(v1alpha1.PKIACMEDNS01)
				s.Network.Mode = v1alpha1.NetworkAirgap
				return s
			},
			token: true, wantCode: "ACME_IN_AIRGAP",
		},
		{
			name: "no email",
			spec: func() v1alpha1.ClusterSpec {
				s := acmeSpec(v1alpha1.PKIACMEDNS01)
				s.PKI.ACME.Email = ""
				return s
			},
			token: true, wantCode: "ACME_EMAIL_MISSING", wantIn: "renewal has stopped working",
		},
		{
			name: "dns-01 with no provider",
			spec: func() v1alpha1.ClusterSpec {
				s := acmeSpec(v1alpha1.PKIACMEDNS01)
				s.PKI.ACME.DNSProvider = ""
				return s
			},
			token: true, wantCode: "ACME_DNS_PROVIDER_MISSING",
		},
		{
			name:     "dns-01 with no token",
			spec:     func() v1alpha1.ClusterSpec { return acmeSpec(v1alpha1.PKIACMEDNS01) },
			token:    false,
			wantCode: "ACME_TOKEN_MISSING",
		},
		{
			// The challenge is delivered to whatever the DNS record points at,
			// so the address has to exist before the install rather than be
			// allocated during it.
			name: "http-01 with an unpinned gateway",
			spec: func() v1alpha1.ClusterSpec {
				s := acmeSpec(v1alpha1.PKIACMEHTTP01)
				s.Gateway.Gateways = []v1alpha1.Gateway{{Name: "public"}}
				return s
			},
			wantCode: "ACME_HTTP01_UNPINNED", wantIn: "before install",
		},
		{
			name: "the CA cannot be reached",
			spec: func() v1alpha1.ClusterSpec {
				s := acmeSpec(v1alpha1.PKIACMEDNS01)
				s.PKI.ACME.Server = "https://127.0.0.1:1/directory"
				return s
			},
			token: true, wantCode: "ACME_UNREACHABLE",
		},
		{
			name:       "skipped when the document does not use ACME",
			spec:       privateCASpec,
			wantStatus: StatusSkip,
		},
	}

	p := &Prober{Dialer: &net.Dialer{}, Timeout: 2 * time.Second}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := p.CheckACME(context.Background(), tc.spec(), tc.token)

			want := tc.wantStatus
			if want == "" {
				want = StatusFail
			}
			if got.Status != want {
				t.Fatalf("PF-708 is %s, want %s: %s", got.Status, want, got.Detail)
			}
			if tc.wantCode != "" && got.Code != tc.wantCode {
				t.Errorf("PF-708 code is %q, want %q: %s", got.Code, tc.wantCode, got.Detail)
			}
			if tc.wantIn != "" && !strings.Contains(got.Detail, tc.wantIn) {
				t.Errorf("PF-708 detail does not mention %q: %s", tc.wantIn, got.Detail)
			}
		})
	}
}

// A node-ips gateway has no address to pin. Envoy binds the port in the
// node's own network namespace, so the addresses are the nodes' own and were
// settled before anyone wrote the document -- which is exactly what the check
// wants to be true. It blocked a live install anyway, on a cluster whose
// HTTP-01 challenge would have been delivered correctly.
func TestHTTP01AcceptsANodeIPsGateway(t *testing.T) {
	spec := acmeSpec(v1alpha1.PKIACMEHTTP01)
	spec.Gateway.Gateways = []v1alpha1.Gateway{
		{Name: "public", Exposure: v1alpha1.ExposureNodeIPs},
	}
	var p *Prober
	if got := p.CheckACME(t.Context(), spec, true); got.Failed() {
		t.Errorf("a node-ips gateway is blocked: %s %s", got.Code, got.Detail)
	}

	// A gateway that does take an allocated address still has to name it: a
	// DNS record cannot be created for an address LB-IPAM has not handed out
	// yet.
	spec.Gateway.Gateways = []v1alpha1.Gateway{
		{Name: "public", Exposure: v1alpha1.ExposureLoadBalancer},
	}
	got := p.CheckACME(t.Context(), spec, true)
	if !got.Failed() || got.Code != "ACME_HTTP01_UNPINNED" {
		t.Errorf("an unpinned loadBalancer gateway is allowed through: %s %s", got.Code, got.Detail)
	}
}
