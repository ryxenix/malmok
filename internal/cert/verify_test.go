package cert

import (
	"crypto/elliptic"
	"strings"
	"testing"
	"time"
)

// find returns the finding for a code, so a case can assert on the one gate it
// is about without depending on the order of the rest.
func find(t *testing.T, fs []Finding, id string) Finding {
	t.Helper()
	for _, f := range fs {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("no finding for %s; got %v", id, ids(fs))
	return Finding{}
}

func ids(fs []Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.ID+"="+string(f.Status))
	}
	return out
}

// The eight fixture shapes CLAUDE.md names, each asserted against the gate that
// is supposed to catch it.
func TestGates(t *testing.T) {
	tests := []struct {
		name string
		// build returns the input files and the hostnames to serve.
		build      func(t *testing.T) ([]File, []string)
		passphrase string
		// want is the gate that must fail, empty when the input is healthy.
		wantFail   string
		wantReason string
		// wantWarn is a gate that must warn rather than block.
		wantWarn string
	}{
		{
			name: "healthy fullchain",
			build: func(t *testing.T) ([]File, []string) {
				s := newSite(t)
				return s.files(t), []string{"api.acme.co.kr"}
			},
		},
		{
			name: "chain in the wrong order is still assembled",
			build: func(t *testing.T) ([]File, []string) {
				s := newSite(t)
				// Root, intermediate and leaf in one file, reversed. The order
				// customers send is not the order the walk uses.
				var b []byte
				b = append(b, certPEM(s.root)...)
				b = append(b, certPEM(s.leaf)...)
				b = append(b, certPEM(s.inter)...)
				return []File{
					{Name: "fullchain.pem", Data: b},
					{Name: "privkey.pem", Data: keyPKCS8(t, s.leaf.key)},
				}, []string{"api.acme.co.kr"}
			},
		},
		{
			name: "intermediate missing",
			build: func(t *testing.T) ([]File, []string) {
				s := newSite(t)
				return []File{
					{Name: "leaf.crt", Data: certPEM(s.leaf)},
					{Name: "root.crt", Data: certPEM(s.root)},
					{Name: "leaf.key", Data: keyPKCS8(t, s.leaf.key)},
				}, []string{"api.acme.co.kr"}
			},
			wantFail:   "PF-902",
			wantReason: "CHAIN_INCOMPLETE",
		},
		{
			name: "key belongs to another certificate",
			build: func(t *testing.T) ([]File, []string) {
				s := newSite(t)
				other := newSite(t, "*.example.com")
				return []File{
					{Name: "leaf.crt", Data: certPEM(s.leaf)},
					{Name: "inter.crt", Data: certPEM(s.inter)},
					{Name: "wrong.key", Data: keyPKCS8(t, other.leaf.key)},
				}, []string{"api.acme.co.kr"}
			},
			wantFail:   "PF-901",
			wantReason: "KEY_MISMATCH",
		},
		{
			name: "no key at all",
			build: func(t *testing.T) ([]File, []string) {
				s := newSite(t)
				return []File{
					{Name: "leaf.crt", Data: certPEM(s.leaf)},
					{Name: "inter.crt", Data: certPEM(s.inter)},
				}, []string{"api.acme.co.kr"}
			},
			wantFail:   "PF-901",
			wantReason: "KEY_MISSING",
		},
		{
			name: "DER certificate mixed with PEM",
			build: func(t *testing.T) ([]File, []string) {
				s := newSite(t)
				return []File{
					{Name: "leaf.crt", Data: certPEM(s.leaf)},
					// A Windows export: raw DER behind a .cer suffix.
					{Name: "AcmeTLSRSACAG2.cer", Data: s.inter.der},
					{Name: "AcmeRootCA.crt", Data: certPEM(s.root)},
					{Name: "leaf.key", Data: keyPKCS8(t, s.leaf.key)},
				}, []string{"api.acme.co.kr"}
			},
		},
		{
			name: "encrypted key with the right passphrase",
			build: func(t *testing.T) ([]File, []string) {
				s := newSite(t)
				return []File{
					{Name: "leaf.crt", Data: certPEM(s.leaf)},
					{Name: "inter.crt", Data: certPEM(s.inter)},
					{Name: "root.crt", Data: certPEM(s.root)},
					{Name: "leaf.key", Data: keyEncryptedPKCS8(t, s.leaf.key, "correct horse")},
				}, []string{"api.acme.co.kr"}
			},
			passphrase: "correct horse",
		},
		{
			name: "encrypted key with the wrong passphrase",
			build: func(t *testing.T) ([]File, []string) {
				s := newSite(t)
				return []File{
					{Name: "leaf.crt", Data: certPEM(s.leaf)},
					{Name: "inter.crt", Data: certPEM(s.inter)},
					{Name: "root.crt", Data: certPEM(s.root)},
					{Name: "leaf.key", Data: keyEncryptedPKCS8(t, s.leaf.key, "correct horse")},
				}, []string{"api.acme.co.kr"}
			},
			passphrase: "wrong",
			wantFail:   "PF-906",
			wantReason: "KEY_LOCKED",
		},
		{
			name: "encrypted key with no passphrase supplied",
			build: func(t *testing.T) ([]File, []string) {
				s := newSite(t)
				return []File{
					{Name: "leaf.crt", Data: certPEM(s.leaf)},
					{Name: "inter.crt", Data: certPEM(s.inter)},
					{Name: "root.crt", Data: certPEM(s.root)},
					{Name: "leaf.key", Data: keyEncryptedPKCS8(t, s.leaf.key, "correct horse")},
				}, []string{"api.acme.co.kr"}
			},
			wantFail:   "PF-906",
			wantReason: "KEY_LOCKED",
		},
		{
			name: "expired certificate",
			build: func(t *testing.T) ([]File, []string) {
				root := newRoot(t, "Acme Root CA")
				inter := issue(t, &root, issueOpts{cn: "Acme CA G2", isCA: true, notAfter: base.AddDate(5, 0, 0)})
				leaf := issue(t, &inter, issueOpts{
					cn: "*.acme.co.kr", dns: []string{"*.acme.co.kr"},
					notBefore: base.AddDate(-2, 0, 0), notAfter: base.AddDate(0, 0, -10),
				})
				return []File{
					{Name: "leaf.crt", Data: certPEM(leaf)},
					{Name: "inter.crt", Data: certPEM(inter)},
					{Name: "leaf.key", Data: keyPKCS8(t, leaf.key)},
				}, []string{"api.acme.co.kr"}
			},
			wantFail:   "PF-904",
			wantReason: "CERT_EXPIRED",
		},
		{
			name: "certificate inside the warning window",
			build: func(t *testing.T) ([]File, []string) {
				root := newRoot(t, "Acme Root CA")
				inter := issue(t, &root, issueOpts{cn: "Acme CA G2", isCA: true, notAfter: base.AddDate(5, 0, 0)})
				leaf := issue(t, &inter, issueOpts{
					cn: "*.acme.co.kr", dns: []string{"*.acme.co.kr"},
					notAfter: base.AddDate(0, 0, 20),
				})
				return []File{
					{Name: "leaf.crt", Data: certPEM(leaf)},
					{Name: "inter.crt", Data: certPEM(inter)},
					{Name: "root.crt", Data: certPEM(root)},
					{Name: "leaf.key", Data: keyPKCS8(t, leaf.key)},
				}, []string{"api.acme.co.kr"}
			},
			wantWarn: "PF-904",
		},
		{
			name: "notBefore in the node's future",
			build: func(t *testing.T) ([]File, []string) {
				root := newRoot(t, "Acme Root CA")
				inter := issue(t, &root, issueOpts{cn: "Acme CA G2", isCA: true, notAfter: base.AddDate(5, 0, 0)})
				leaf := issue(t, &inter, issueOpts{
					cn: "*.acme.co.kr", dns: []string{"*.acme.co.kr"},
					notBefore: base.AddDate(0, 0, 30), notAfter: base.AddDate(1, 0, 0),
				})
				return []File{
					{Name: "leaf.crt", Data: certPEM(leaf)},
					{Name: "inter.crt", Data: certPEM(inter)},
					{Name: "leaf.key", Data: keyPKCS8(t, leaf.key)},
				}, []string{"api.acme.co.kr"}
			},
			wantFail:   "PF-911",
			wantReason: "NOT_YET_VALID",
		},
		{
			name: "SAN does not cover the listener",
			build: func(t *testing.T) ([]File, []string) {
				s := newSite(t, "*.acme.co.kr")
				return s.files(t), []string{"acme.co.kr"}
			},
			wantFail:   "PF-903",
			wantReason: "SAN_MISMATCH",
		},
		{
			name: "root private key in the input",
			build: func(t *testing.T) ([]File, []string) {
				s := newSite(t)
				files := s.files(t)
				files = append(files, File{Name: "root.key", Data: keyPKCS8(t, s.root.key)})
				return files, []string{"api.acme.co.kr"}
			},
			wantFail:   "PF-910",
			wantReason: "ROOT_KEY_PRESENT",
		},
		{
			name: "two leaves claiming the same name",
			build: func(t *testing.T) ([]File, []string) {
				s := newSite(t)
				// The renewal dropped next to the old file, which is how this
				// arrives in practice.
				old := issue(t, &s.inter, issueOpts{
					cn: "*.acme.co.kr", dns: []string{"*.acme.co.kr"},
					notAfter: base.AddDate(0, 0, 30),
				})
				files := s.files(t)
				files = append(files, File{Name: "old.crt", Data: certPEM(old)})
				return files, []string{"api.acme.co.kr"}
			},
			wantFail:   "PF-912",
			wantReason: "DUPLICATE_LEAF",
		},
		{
			name: "RSA key below the minimum",
			build: func(t *testing.T) ([]File, []string) {
				root := newRoot(t, "Acme Root CA")
				inter := issue(t, &root, issueOpts{cn: "Acme CA G2", isCA: true, notAfter: base.AddDate(5, 0, 0)})
				leaf := issue(t, &inter, issueOpts{
					cn: "*.acme.co.kr", dns: []string{"*.acme.co.kr"}, key: rsaKey(t, 1024),
				})
				return []File{
					{Name: "leaf.crt", Data: certPEM(leaf)},
					{Name: "inter.crt", Data: certPEM(inter)},
					{Name: "leaf.key", Data: keyPKCS1(t, leaf.key)},
				}, []string{"api.acme.co.kr"}
			},
			wantFail:   "PF-905",
			wantReason: "KEY_UNSUPPORTED",
		},
		{
			name: "Ed25519 is refused by default",
			build: func(t *testing.T) ([]File, []string) {
				root := newRoot(t, "Acme Root CA")
				inter := issue(t, &root, issueOpts{cn: "Acme CA G2", isCA: true, notAfter: base.AddDate(5, 0, 0)})
				leaf := issue(t, &inter, issueOpts{
					cn: "*.acme.co.kr", dns: []string{"*.acme.co.kr"}, key: ed25519Key(t),
				})
				return []File{
					{Name: "leaf.crt", Data: certPEM(leaf)},
					{Name: "inter.crt", Data: certPEM(inter)},
					{Name: "leaf.key", Data: keyPKCS8(t, leaf.key)},
				}, []string{"api.acme.co.kr"}
			},
			wantFail:   "PF-905",
			wantReason: "KEY_UNSUPPORTED",
		},
		{
			name: "no leaf in the input at all",
			build: func(t *testing.T) ([]File, []string) {
				s := newSite(t)
				return []File{
					{Name: "inter.crt", Data: certPEM(s.inter)},
					{Name: "root.crt", Data: certPEM(s.root)},
				}, []string{"api.acme.co.kr"}
			},
			wantFail:   "PF-903",
			wantReason: "NO_LEAF",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			files, hosts := tc.build(t)
			res := Assemble(Input{
				Files:      files,
				Passphrase: []byte(tc.passphrase),
				Hostnames:  hosts,
				Now:        base,
			})

			if tc.wantFail != "" {
				f := find(t, res.Findings, tc.wantFail)
				if f.Status != StatusFail {
					t.Fatalf("%s is %s, want fail: %s", tc.wantFail, f.Status, f.Detail)
				}
				if tc.wantReason != "" && f.Reason != tc.wantReason {
					t.Errorf("%s reason is %q, want %q", tc.wantFail, f.Reason, tc.wantReason)
				}
				if !res.Blocked() {
					t.Error("a blocking finding did not block the result")
				}
				if res.Bundle != nil {
					t.Error("a bundle was produced from input that should have been refused")
				}
				// Every failure has to say something an operator can act on.
				if len(f.Detail) < 20 {
					t.Errorf("the detail is too short to act on: %q", f.Detail)
				}
				return
			}

			if tc.wantWarn != "" {
				f := find(t, res.Findings, tc.wantWarn)
				if f.Status != StatusWarn {
					t.Fatalf("%s is %s, want warn: %s", tc.wantWarn, f.Status, f.Detail)
				}
			}

			if res.Blocked() {
				for _, f := range res.Findings {
					if f.Blocking() {
						t.Errorf("blocked by %s: %s", f.ID, f.Detail)
					}
				}
				t.FailNow()
			}
			if res.Bundle == nil {
				t.Fatal("no bundle was produced from usable input")
			}
		})
	}
}

// A healthy bundle has to produce exactly what docs/20-cert.md §5 describes.
func TestBundleOutput(t *testing.T) {
	s := newSite(t)
	res := Assemble(Input{Files: s.files(t), Hostnames: []string{"api.acme.co.kr"}, Now: base})
	if res.Bundle == nil {
		t.Fatalf("no bundle: %v", ids(res.Findings))
	}
	b := res.Bundle

	// tls.crt is leaf then intermediate, and the root is not in it.
	if n := strings.Count(string(b.ChainPEM), "BEGIN CERTIFICATE"); n != 2 {
		t.Errorf("the serving chain holds %d certificates, want leaf + intermediate", n)
	}
	if strings.Contains(string(b.ChainPEM), string(certPEM(s.root))) {
		t.Error("the root is in the serving chain; §3.1 keeps it out")
	}
	if b.ChainDepth != 2 {
		t.Errorf("chain depth is %d, want 2", b.ChainDepth)
	}

	// The private root goes to the trust store instead.
	if len(b.CAPEM) == 0 {
		t.Error("the private root was not emitted for the trust bundle")
	}

	// tls.key is PKCS#8 regardless of what came in.
	if !strings.Contains(string(b.KeyPEM), "BEGIN PRIVATE KEY") {
		t.Errorf("the key is not PKCS#8: %.40q", b.KeyPEM)
	}

	ann := b.Annotations()
	if !strings.HasPrefix(ann["platform.ryxen.dev/fingerprint"], "sha256:") {
		t.Errorf("fingerprint annotation is %q", ann["platform.ryxen.dev/fingerprint"])
	}
	if ann["platform.ryxen.dev/san"] != "*.acme.co.kr" {
		t.Errorf("san annotation is %q", ann["platform.ryxen.dev/san"])
	}
	if ann["platform.ryxen.dev/chain-depth"] != "2" {
		t.Errorf("chain-depth annotation is %q", ann["platform.ryxen.dev/chain-depth"])
	}
	if got := ann["platform.ryxen.dev/not-after"]; got != s.leaf.cert.NotAfter.UTC().Format(time.RFC3339) {
		t.Errorf("not-after annotation is %q", got)
	}
}

// IncludeRoot is what a customer asks for when a middlebox refuses a chain that
// stops at the intermediate. It has to actually change the output.
func TestIncludeRoot(t *testing.T) {
	s := newSite(t)
	res := Assemble(Input{
		Files: s.files(t), Hostnames: []string{"api.acme.co.kr"}, Now: base, IncludeRoot: true,
	})
	if res.Bundle == nil {
		t.Fatalf("no bundle: %v", ids(res.Findings))
	}
	if n := strings.Count(string(res.Bundle.ChainPEM), "BEGIN CERTIFICATE"); n != 3 {
		t.Errorf("the serving chain holds %d certificates, want leaf + intermediate + root", n)
	}
}

// Every key encoding docs/20-cert.md §2.3 lists has to be readable, and the PEM
// header has to not be what decides it.
func TestKeyEncodings(t *testing.T) {
	rk := rsaKey(t, 2048)
	ek := ecKey(t, elliptic.P256())

	tests := []struct {
		name string
		data []byte
		pass string
	}{
		{"PKCS#8 RSA", keyPKCS8(t, rk), ""},
		{"PKCS#1 RSA", keyPKCS1(t, rk), ""},
		{"PKCS#8 EC", keyPKCS8(t, ek), ""},
		{"SEC1 EC", keyEC(t, ek), ""},
		{"encrypted PKCS#8", keyEncryptedPKCS8(t, rk, "pw"), "pw"},
		{
			// A PKCS#8 body under an RSA PRIVATE KEY header, which is what a
			// hand-edited file looks like. The bytes decide, not the label.
			name: "mislabelled header",
			data: []byte(strings.ReplaceAll(string(keyPKCS8(t, rk)), "PRIVATE KEY", "RSA PRIVATE KEY")),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := Scan([]File{{Name: "k.pem", Data: tc.data}}, []byte(tc.pass))
			if len(m.Keys) != 1 {
				t.Fatalf("read %d keys and %d locked, want 1 key", len(m.Keys), len(m.Locked))
			}
		})
	}
}

// A file that is not certificate material must be recorded, not silently
// dropped: an operator who exported the wrong thing needs to be told.
func TestUnreadableFilesAreReported(t *testing.T) {
	s := newSite(t)
	files := append(s.files(t),
		File{Name: "README.txt", Data: []byte("please find the certificate attached")},
		File{Name: "bundle.zip", Data: []byte{0x50, 0x4b, 0x03, 0x04, 0x00}},
	)
	m := Scan(files, nil)
	if len(m.Skipped) != 2 {
		t.Errorf("skipped %v, want both non-certificate files", m.Skipped)
	}
	if len(m.Leaves) != 1 {
		t.Errorf("the certificates beside them were not read: %d leaves", len(m.Leaves))
	}
}

// The same intermediate arriving in three files is normal. A chain walker that
// counts it three times reports a depth nobody can make sense of.
func TestDuplicateCertificatesAreCollapsed(t *testing.T) {
	s := newSite(t)
	var fullchain []byte
	fullchain = append(fullchain, certPEM(s.leaf)...)
	fullchain = append(fullchain, certPEM(s.inter)...)

	files := []File{
		{Name: "fullchain.pem", Data: fullchain},
		{Name: "cert.crt", Data: certPEM(s.leaf)},
		{Name: "chain.crt", Data: certPEM(s.inter)},
		{Name: "AcmeCA.cer", Data: s.inter.der},
		{Name: "privkey.pem", Data: keyPKCS8(t, s.leaf.key)},
	}
	m := Scan(files, nil)
	if len(m.Leaves) != 1 {
		t.Errorf("read %d leaves, want 1", len(m.Leaves))
	}
	if len(m.Intermediates) != 1 {
		t.Errorf("read %d intermediates, want 1", len(m.Intermediates))
	}

	// And PF-912 must not fire on what is one certificate seen four times.
	res := Assemble(Input{Files: files, Hostnames: []string{"api.acme.co.kr"}, Now: base})
	if f := find(t, res.Findings, "PF-912"); f.Status == StatusFail {
		t.Errorf("PF-912 fired on duplicates of one certificate: %s", f.Detail)
	}
}
