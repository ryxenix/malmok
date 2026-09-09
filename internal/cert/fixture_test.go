package cert

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"
)

// Fixtures are generated in the test rather than checked in. A checked-in
// certificate expires, and a test suite that starts
// failing on a date nobody chose teaches people to ignore it.

// base is the reference time every fixture is built around, so that "expired"
// and "expiring" mean the same thing in every test regardless of when it runs.
var base = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

type ca struct {
	cert *x509.Certificate
	der  []byte
	key  crypto.Signer
}

type issueOpts struct {
	cn       string
	dns      []string
	ips      []net.IP
	isCA     bool
	notAfter time.Time
	// notBefore defaults to a day before base.
	notBefore time.Time
	key       crypto.Signer
	// noBasicConstraints emits a certificate without the extension at all,
	// which is how older end-entity certificates look.
	noBasicConstraints bool
}

func rsaKey(t *testing.T, bits int) crypto.Signer {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	return k
}

func ecKey(t *testing.T, c elliptic.Curve) crypto.Signer {
	t.Helper()
	k, err := ecdsa.GenerateKey(c, rand.Reader)
	if err != nil {
		t.Fatalf("generate ec key: %v", err)
	}
	return k
}

var serial int64

// newRoot builds a self-signed CA.
func newRoot(t *testing.T, cn string) ca {
	t.Helper()
	return issue(t, nil, issueOpts{cn: cn, isCA: true, notAfter: base.AddDate(10, 0, 0)})
}

// issue signs a certificate, self-signing it when parent is nil.
func issue(t *testing.T, parent *ca, o issueOpts) ca {
	t.Helper()

	if o.key == nil {
		o.key = ecKey(t, elliptic.P256())
	}
	if o.notAfter.IsZero() {
		o.notAfter = base.AddDate(1, 0, 0)
	}
	if o.notBefore.IsZero() {
		o.notBefore = base.AddDate(0, 0, -1)
	}

	serial++
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: o.cn},
		NotBefore:             o.notBefore,
		NotAfter:              o.notAfter,
		DNSNames:              o.dns,
		IPAddresses:           o.ips,
		BasicConstraintsValid: !o.noBasicConstraints,
		IsCA:                  o.isCA,
		// A subject key id makes the issuer link testable the way real chains
		// are linked, rather than by DN alone.
		SubjectKeyId: skid(t, o.key.Public()),
	}
	if o.isCA {
		tmpl.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	} else {
		tmpl.KeyUsage = x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}

	signer := tmpl
	signerKey := o.key
	if parent != nil {
		signer = parent.cert
		signerKey = parent.key
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, o.key.Public(), signerKey)
	if err != nil {
		t.Fatalf("create certificate %s: %v", o.cn, err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse generated certificate %s: %v", o.cn, err)
	}
	return ca{cert: c, der: der, key: o.key}
}

func skid(t *testing.T, pub crypto.PublicKey) []byte {
	t.Helper()
	b, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	sum := sha256.Sum256(b)
	return sum[:20]
}

// ---------------------------------------------------------------------------
// Encoding helpers
// ---------------------------------------------------------------------------

func certPEM(c ca) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.der})
}

func keyPKCS8(t *testing.T, k crypto.Signer) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func keyPKCS1(t *testing.T, k crypto.Signer) []byte {
	t.Helper()
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		t.Fatalf("pkcs1 needs an rsa key, got %T", k)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rk),
	})
}

func keyEC(t *testing.T, k crypto.Signer) []byte {
	t.Helper()
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatalf("ec needs an ecdsa key, got %T", k)
	}
	der, err := x509.MarshalECPrivateKey(ek)
	if err != nil {
		t.Fatalf("marshal ec key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}

// keyEncryptedPKCS8 produces what current OpenSSL writes: PBES2 with
// PBKDF2-HMAC-SHA256 and AES-256-CBC.
//
// Written out by hand because the standard library encrypts nothing, and the
// decryptor under test has to be exercised against bytes it did not produce
// itself. The OIDs below are the same ones openssl pkcs8 -topk8 emits.
func keyEncryptedPKCS8(t *testing.T, k crypto.Signer, passphrase string) []byte {
	t.Helper()

	plain, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}

	salt := make([]byte, 16)
	iv := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(iv); err != nil {
		t.Fatal(err)
	}

	const iter = 2048
	dk, err := pbkdf2.Key(sha256.New, passphrase, salt, iter, 32)
	if err != nil {
		t.Fatal(err)
	}

	blk, err := aes.NewCipher(dk)
	if err != nil {
		t.Fatal(err)
	}
	padded := padPKCS7(plain, blk.BlockSize())
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(blk, iv).CryptBlocks(ct, padded)

	kdfParams, err := asn1.Marshal(struct {
		Salt []byte
		Iter int
		PRF  pkix.AlgorithmIdentifier
	}{salt, iter, pkix.AlgorithmIdentifier{
		Algorithm:  oidHMACSHA256,
		Parameters: asn1.RawValue{Tag: asn1.TagNull},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ivParams, err := asn1.Marshal(iv)
	if err != nil {
		t.Fatal(err)
	}
	pbes2, err := asn1.Marshal(struct {
		KDF    pkix.AlgorithmIdentifier
		Cipher pkix.AlgorithmIdentifier
	}{
		KDF:    pkix.AlgorithmIdentifier{Algorithm: oidPBKDF2, Parameters: asn1.RawValue{FullBytes: kdfParams}},
		Cipher: pkix.AlgorithmIdentifier{Algorithm: oidAES256CBC, Parameters: asn1.RawValue{FullBytes: ivParams}},
	})
	if err != nil {
		t.Fatal(err)
	}
	outer, err := asn1.Marshal(struct {
		Algo pkix.AlgorithmIdentifier
		Data []byte
	}{
		Algo: pkix.AlgorithmIdentifier{Algorithm: oidPBES2, Parameters: asn1.RawValue{FullBytes: pbes2}},
		Data: ct,
	})
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: outer})
}

func padPKCS7(b []byte, size int) []byte {
	n := size - len(b)%size
	out := make([]byte, len(b)+n)
	copy(out, b)
	for i := len(b); i < len(out); i++ {
		out[i] = byte(n)
	}
	return out
}

// site is a complete, healthy three-tier fixture: root -> intermediate -> leaf.
type site struct {
	root  ca
	inter ca
	leaf  ca
}

func newSite(t *testing.T, dns ...string) site {
	t.Helper()
	if len(dns) == 0 {
		dns = []string{"*.acme.co.kr"}
	}
	root := newRoot(t, "Acme Root CA")
	inter := issue(t, &root, issueOpts{
		cn: "Acme TLS RSA CA G2", isCA: true, notAfter: base.AddDate(5, 0, 0),
	})
	leaf := issue(t, &inter, issueOpts{cn: dns[0], dns: dns})
	return site{root: root, inter: inter, leaf: leaf}
}

// files renders a site the way a customer usually sends one.
func (s site) files(t *testing.T) []File {
	t.Helper()
	return []File{
		{Name: "STAR_acme_co_kr.crt", Data: certPEM(s.leaf)},
		{Name: "AcmeTLSRSACAG2.crt", Data: certPEM(s.inter)},
		{Name: "AcmeRootCA.crt", Data: certPEM(s.root)},
		{Name: "private.key", Data: keyPKCS8(t, s.leaf.key)},
	}
}

func ed25519Key(t *testing.T) crypto.Signer {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}
