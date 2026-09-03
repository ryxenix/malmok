package cert

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"time"
)

// Input is what an operator supplied plus the context the gates need to judge
// it. Nothing here is discovered; it all comes from cluster.yaml or the files.
type Input struct {
	Files      []File
	Passphrase []byte

	// Hostnames are the listener hostnames the bundle has to cover. Empty means
	// there is nothing to select a leaf against, and PF-903 is skipped rather
	// than guessed at.
	Hostnames []string

	// ExpiryWarningDays is the bundle's own warning window (PF-904). It is not
	// the report grading threshold, which is contract-level and wider --
	// docs/30-maintenance.md §2.5.
	ExpiryWarningDays int

	// IncludeRoot puts a self-signed root into the serving chain. Off by
	// default: it wastes bandwidth on every handshake and some verifiers warn.
	IncludeRoot bool

	// Now is the clock the validity window is judged against. Supplied rather
	// than read so PF-911 can be checked against a node's clock and so tests
	// are not time-dependent.
	Now time.Time

	// KeyPolicy is what the selected GatewayClass accepts (PF-905).
	KeyPolicy KeyPolicy
}

// DefaultExpiryWarningDays is the window docs/20-cert.md §4 sets for PF-904.
const DefaultExpiryWarningDays = 45

// Bundle is the assembled output of docs/20-cert.md §5.
type Bundle struct {
	Leaf  Cert
	Chain Chain
	Key   Key

	// ChainPEM is tls.crt: leaf followed by intermediates, root excluded unless
	// IncludeRoot was set.
	ChainPEM []byte
	// KeyPEM is tls.key, decrypted. It is PKCS#8 regardless of what came in, so
	// that consumers have one shape to handle.
	KeyPEM []byte
	// CAPEM is ca.crt: the private root, when the input carried one. A public
	// CA root is not emitted; clients already trust it.
	CAPEM []byte

	// Annotations are the audit and renewal values of §5.
	Fingerprint string
	NotAfter    time.Time
	SANs        []string
	ChainDepth  int
}

// Annotations renders the values §5 attaches to the generated Secret.
func (b Bundle) Annotations() map[string]string {
	san := ""
	if len(b.SANs) > 0 {
		san = b.SANs[0]
	}
	return map[string]string{
		"malmok.dev/fingerprint": b.Fingerprint,
		"malmok.dev/not-after":   b.NotAfter.UTC().Format(time.RFC3339),
		"malmok.dev/san":         san,
		"malmok.dev/chain-depth": fmt.Sprintf("%d", b.ChainDepth),
		"malmok.dev/source":      "byo-dir",
	}
}

// Result is an assembly attempt: the bundle when every blocking gate passed,
// and the findings either way.
//
// Findings are returned even on success, because a warning -- a certificate
// inside its expiry window, say -- has to reach the audit report.
type Result struct {
	Bundle   *Bundle
	Material Material
	Findings []Finding
}

// Blocked reports whether anything refuses the input.
func (r Result) Blocked() bool {
	for _, f := range r.Findings {
		if f.Blocking() {
			return true
		}
	}
	return false
}

// Assemble classifies the input, runs every gate, and produces the bundle when
// the input is usable.
//
// The gates run before assembly rather than after, because several of them --
// a root private key in the input, two leaves claiming one name -- are reasons
// not to produce anything at all.
func Assemble(in Input) Result {
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	if in.ExpiryWarningDays == 0 {
		in.ExpiryWarningDays = DefaultExpiryWarningDays
	}
	if in.KeyPolicy.MinRSABits == 0 && in.KeyPolicy.AllowedCurves == nil {
		in.KeyPolicy = DefaultKeyPolicy()
	}

	m := Scan(in.Files, in.Passphrase)
	res := Result{Material: m}

	leaf, key, chain, findings := gate(in, m)
	res.Findings = findings
	if res.Blocked() || leaf == nil || key == nil {
		return res
	}

	b := &Bundle{Leaf: *leaf, Chain: *chain, Key: *key}

	var buf bytes.Buffer
	buf.Write(chain.PEM())
	if in.IncludeRoot && chain.Root != nil {
		buf.Write(encodeCert(chain.Root.Raw))
	}
	b.ChainPEM = buf.Bytes()

	keyDER, err := x509.MarshalPKCS8PrivateKey(key.Signer)
	if err != nil {
		res.Findings = append(res.Findings, blockf("PF-901", "KEY_UNENCODABLE",
			"the private key could not be re-encoded as PKCS#8: %v", err))
		return res
	}
	b.KeyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	// A private root goes to the trust store, never into the serving chain
	// (§3.1). A public one is left out of both.
	if chain.Root != nil && isPrivateRoot(*chain.Root) {
		b.CAPEM = encodeCert(chain.Root.Raw)
	}

	sum := sha256.Sum256(leaf.Raw)
	b.Fingerprint = "sha256:" + hex.EncodeToString(sum[:])
	b.NotAfter = leaf.NotAfter
	b.SANs = SANsOf(leaf.Certificate)
	b.ChainDepth = chain.Depth()

	res.Bundle = b
	return res
}

// isPrivateRoot distinguishes a customer's own CA from a public one.
//
// The system trust store is the only honest test available offline: if the root
// verifies against it, clients already trust it and distributing it again would
// be noise. When the store cannot be read -- a scratch container, an airgapped
// host -- the root is treated as private, because distributing a certificate
// that was not needed is recoverable and omitting one that was is not.
func isPrivateRoot(root Cert) bool {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		return true
	}
	_, err = root.Verify(x509.VerifyOptions{Roots: pool, CurrentTime: root.NotBefore})
	return err != nil
}

func encodeCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// keyDescription renders an algorithm and size for a diagnostic line.
func keyDescription(pub any) string {
	switch k := pub.(type) {
	case *rsa.PublicKey:
		return fmt.Sprintf("RSA %d-bit", k.N.BitLen())
	case *ecdsa.PublicKey:
		return "ECDSA " + k.Curve.Params().Name
	case ed25519.PublicKey:
		return "Ed25519"
	}
	return fmt.Sprintf("%T", pub)
}
