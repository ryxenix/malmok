//go:build lab

package lab

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeMaterial generates the certificate chain a case's PKI mode reads.
//
// Generated here rather than kept as files, for the reason every certificate
// fixture in this repository is: material on disk expires, and a suite that
// starts failing in six months for a reason nobody changed is a suite people
// stop believing. Root, intermediate and a wildcard leaf, all fresh.
func writeMaterial(t *testing.T, dir string) matrixMaterial {
	t.Helper()

	pkiDir := filepath.Join(dir, "pki")
	if err := os.MkdirAll(pkiDir, 0o755); err != nil {
		t.Fatal(err)
	}

	rootKey, rootCert := issue(t, nil, nil, pkix.Name{CommonName: "Malmok Lab Root CA"}, true, nil)
	interKey, interCert := issue(t, rootCert, rootKey, pkix.Name{CommonName: "Malmok Lab Issuing CA"}, true, nil)
	leafKey, leafCert := issue(t, interCert, interKey,
		pkix.Name{CommonName: "*.lab.example.com"}, false,
		[]string{"*.lab.example.com", "lab.example.com"})

	write := func(name string, block *pem.Block) string {
		path := filepath.Join(pkiDir, name)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := pem.Encode(f, block); err != nil {
			t.Fatal(err)
		}
		return path
	}
	certBlock := func(der []byte) *pem.Block { return &pem.Block{Type: "CERTIFICATE", Bytes: der} }
	keyBlock := func(k *rsa.PrivateKey) *pem.Block {
		return &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}
	}

	// The leaf travels with its issuer: a chain missing its intermediate is
	// the single most common way customer-supplied material fails, and this
	// suite is not the place to reproduce it.
	fullchain := filepath.Join(pkiDir, "leaf-fullchain.crt")
	f, err := os.Create(fullchain)
	if err != nil {
		t.Fatal(err)
	}
	for _, der := range [][]byte{leafCert.Raw, interCert.Raw} {
		if err := pem.Encode(f, certBlock(der)); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()

	return matrixMaterial{
		RootCert:         write("root.crt", certBlock(rootCert.Raw)),
		IntermediateCert: write("inter.crt", certBlock(interCert.Raw)),
		IntermediateKey:  write("inter.key", keyBlock(interKey)),
		LeafCert:         fullchain,
		LeafKey:          write("leaf.key", keyBlock(leafKey)),
	}
}

// issue signs one certificate, self-signed when parent is nil.
func issue(t *testing.T, parent *x509.Certificate, parentKey *rsa.PrivateKey,
	subject pkix.Name, ca bool, dns []string) (*rsa.PrivateKey, *x509.Certificate) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               subject,
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(90 * 24 * time.Hour),
		BasicConstraintsValid: true,
		IsCA:                  ca,
		DNSNames:              dns,
	}
	if ca {
		tmpl.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature
	} else {
		tmpl.KeyUsage = x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}

	signer, signerKey := tmpl, key
	if parent != nil {
		signer, signerKey = parent, parentKey
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return key, cert
}

// matrixMaterial mirrors matrix.Material; the conversion at the call site
// keeps the generator free of the package it feeds.
type matrixMaterial struct {
	RootCert         string
	IntermediateCert string
	IntermediateKey  string
	LeafCert         string
	LeafKey          string
}
