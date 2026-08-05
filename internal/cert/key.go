package cert

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/pbkdf2"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"hash"
)

// Key is one private key that was read successfully.
type Key struct {
	Signer crypto.Signer
	// From is the file it came from, for diagnostics only.
	From string
	// WasEncrypted records that a passphrase was needed. The audit report says
	// so, and an operator who did not expect it has a question to ask.
	WasEncrypted bool
}

// LockedKey is an encrypted key the supplied passphrase did not open.
type LockedKey struct {
	From string
	Err  error
}

// errLocked marks a decryption failure, as opposed to a malformed key. The two
// need different messages: one asks for a passphrase, the other for a new file.
var errLocked = errors.New("private key is encrypted and could not be decrypted")

func isLocked(err error) bool { return errors.Is(err, errLocked) }

// PublicKeyBytes renders a public key as its SubjectPublicKeyInfo DER.
//
// docs/20-cert.md §2.4 requires the key-to-leaf match to be a byte comparison
// of the SubjectPublicKeyInfo rather than anything derived from file names.
// Attaching another domain's key surfaces only as a handshake failure, which
// takes a long time to trace back.
func PublicKeyBytes(pub crypto.PublicKey) ([]byte, error) {
	return x509.MarshalPKIXPublicKey(pub)
}

// Matches reports whether this key belongs to that certificate.
func (k Key) Matches(c *x509.Certificate) bool {
	a, err := PublicKeyBytes(k.Signer.Public())
	if err != nil {
		return false
	}
	b, err := PublicKeyBytes(c.PublicKey)
	if err != nil {
		return false
	}
	return bytes.Equal(a, b)
}

// parseKey reads one PEM key block, decrypting it when necessary.
//
// The four shapes of docs/20-cert.md §2.3 are all handled, plus both ways a key
// can be encrypted: the legacy Proc-Type header OpenSSL wrote for years, and
// PKCS#8 EncryptedPrivateKeyInfo, which is what current OpenSSL produces.
func parseKey(b block, passphrase []byte) (Key, error) {
	der := b.der
	encrypted := false

	switch {
	case b.pemType == "ENCRYPTED PRIVATE KEY":
		if len(passphrase) == 0 {
			return Key{}, fmt.Errorf("%w: no passphrase was supplied", errLocked)
		}
		plain, err := decryptPKCS8(der, passphrase)
		if err != nil {
			return Key{}, fmt.Errorf("%w: %v", errLocked, err)
		}
		der, encrypted = plain, true

	//nolint:staticcheck // x509.IsEncryptedPEMBlock is deprecated because the
	// format is weak, not because it stopped existing. Customer key files
	// still arrive with Proc-Type headers, and refusing to read one means
	// refusing the install.
	case b.pem != nil && x509.IsEncryptedPEMBlock(b.pem):
		if len(passphrase) == 0 {
			return Key{}, fmt.Errorf("%w: no passphrase was supplied", errLocked)
		}
		plain, err := x509.DecryptPEMBlock(b.pem, passphrase) //nolint:staticcheck
		if err != nil {
			return Key{}, fmt.Errorf("%w: %v", errLocked, err)
		}
		der, encrypted = plain, true
	}

	signer, err := parseKeyDER(der)
	if err != nil {
		return Key{}, err
	}
	return Key{Signer: signer, WasEncrypted: encrypted}, nil
}

// parseKeyDER tries every private key encoding the standard library knows.
//
// The PEM header is not trusted to say which one it is: files relabelled by
// hand or by a Windows tool routinely carry the wrong header over PKCS#8 bytes.
func parseKeyDER(der []byte) (crypto.Signer, error) {
	if k, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		s, ok := k.(crypto.Signer)
		if !ok {
			return nil, fmt.Errorf("cert: unsupported private key type %T", k)
		}
		return s, nil
	}
	if k, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return k, nil
	}
	if k, err := x509.ParseECPrivateKey(der); err == nil {
		return k, nil
	}
	return nil, errors.New("cert: not a recognised private key")
}

// ---------------------------------------------------------------------------
// Encrypted PKCS#8
// ---------------------------------------------------------------------------

// The standard library parses PKCS#8 but not the encrypted wrapper, and
// CLAUDE.md fixes the dependency budget for certificate handling at zero. The
// wrapper is small enough to read directly, and crypto/pbkdf2 has been standard
// library since Go 1.24, so this stays inside the budget.
//
// Only PBES2 is implemented. PBES1 and the PKCS#12 derivations appear on files
// old enough that a customer sending one has a bigger problem than this tool.

type encryptedPrivateKeyInfo struct {
	Algo          pkix2AlgorithmIdentifier
	EncryptedData []byte
}

type pkix2AlgorithmIdentifier struct {
	Algorithm  asn1.ObjectIdentifier
	Parameters asn1.RawValue `asn1:"optional"`
}

type pbes2Params struct {
	KDF    pkix2AlgorithmIdentifier
	Cipher pkix2AlgorithmIdentifier
}

type pbkdf2Params struct {
	Salt      []byte
	Iter      int
	KeyLength int                      `asn1:"optional"`
	PRF       pkix2AlgorithmIdentifier `asn1:"optional"`
}

var (
	oidPBES2  = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 13}
	oidPBKDF2 = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 12}

	oidAES128CBC = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 2}
	oidAES192CBC = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 22}
	oidAES256CBC = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 42}

	oidHMACSHA1   = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 7}
	oidHMACSHA256 = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 9}
	oidHMACSHA384 = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 10}
	oidHMACSHA512 = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 11}
)

func decryptPKCS8(der, passphrase []byte) ([]byte, error) {
	var info encryptedPrivateKeyInfo
	if _, err := asn1.Unmarshal(der, &info); err != nil {
		return nil, fmt.Errorf("malformed EncryptedPrivateKeyInfo: %w", err)
	}
	if !info.Algo.Algorithm.Equal(oidPBES2) {
		return nil, fmt.Errorf("unsupported key encryption %v; only PBES2 is implemented", info.Algo.Algorithm)
	}

	var params pbes2Params
	if _, err := asn1.Unmarshal(info.Algo.Parameters.FullBytes, &params); err != nil {
		return nil, fmt.Errorf("malformed PBES2 parameters: %w", err)
	}
	if !params.KDF.Algorithm.Equal(oidPBKDF2) {
		return nil, fmt.Errorf("unsupported key derivation %v; only PBKDF2 is implemented", params.KDF.Algorithm)
	}

	var kdf pbkdf2Params
	if _, err := asn1.Unmarshal(params.KDF.Parameters.FullBytes, &kdf); err != nil {
		return nil, fmt.Errorf("malformed PBKDF2 parameters: %w", err)
	}

	keyLen, err := aesKeyLength(params.Cipher.Algorithm)
	if err != nil {
		return nil, err
	}
	if kdf.KeyLength > 0 {
		keyLen = kdf.KeyLength
	}

	prf, err := prfHash(kdf.PRF.Algorithm)
	if err != nil {
		return nil, err
	}

	key, err := pbkdf2.Key(prf, string(passphrase), kdf.Salt, kdf.Iter, keyLen)
	if err != nil {
		return nil, fmt.Errorf("key derivation failed: %w", err)
	}

	var iv []byte
	if _, err := asn1.Unmarshal(params.Cipher.Parameters.FullBytes, &iv); err != nil {
		return nil, fmt.Errorf("malformed cipher IV: %w", err)
	}

	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(iv) != blk.BlockSize() {
		return nil, fmt.Errorf("cipher IV is %d bytes, want %d", len(iv), blk.BlockSize())
	}
	if len(info.EncryptedData) == 0 || len(info.EncryptedData)%blk.BlockSize() != 0 {
		return nil, errors.New("ciphertext length is not a multiple of the block size")
	}

	out := make([]byte, len(info.EncryptedData))
	cipher.NewCBCDecrypter(blk, iv).CryptBlocks(out, info.EncryptedData)

	// A wrong passphrase almost always shows up as invalid padding, which is
	// the signal that the passphrase is wrong rather than the file broken.
	return stripPKCS7(out, blk.BlockSize())
}

func aesKeyLength(oid asn1.ObjectIdentifier) (int, error) {
	switch {
	case oid.Equal(oidAES128CBC):
		return 16, nil
	case oid.Equal(oidAES192CBC):
		return 24, nil
	case oid.Equal(oidAES256CBC):
		return 32, nil
	}
	return 0, fmt.Errorf("unsupported cipher %v; only AES-CBC is implemented", oid)
}

func prfHash(oid asn1.ObjectIdentifier) (func() hash.Hash, error) {
	switch {
	// An absent PRF means HMAC-SHA1 by RFC 8018.
	case len(oid) == 0, oid.Equal(oidHMACSHA1):
		return sha1.New, nil
	case oid.Equal(oidHMACSHA256):
		return sha256.New, nil
	case oid.Equal(oidHMACSHA384):
		return sha512.New384, nil
	case oid.Equal(oidHMACSHA512):
		return sha512.New, nil
	}
	return nil, fmt.Errorf("unsupported PBKDF2 PRF %v", oid)
}

func stripPKCS7(b []byte, blockSize int) ([]byte, error) {
	if len(b) == 0 {
		return nil, errors.New("empty plaintext")
	}
	n := int(b[len(b)-1])
	if n == 0 || n > blockSize || n > len(b) {
		return nil, errors.New("invalid padding, which usually means the passphrase is wrong")
	}
	for _, c := range b[len(b)-n:] {
		if int(c) != n {
			return nil, errors.New("invalid padding, which usually means the passphrase is wrong")
		}
	}
	return b[:len(b)-n], nil
}

// ---------------------------------------------------------------------------
// Key policy (PF-905)
// ---------------------------------------------------------------------------

// KeyPolicy is what the selected GatewayClass will accept.
//
// The values are a policy rather than a constant because the answer differs by
// implementation: an eBPF dataplane terminating in Envoy accepts different
// algorithms than an appliance in front of it.
type KeyPolicy struct {
	MinRSABits int
	// AllowedCurves are ECDSA curve names as crypto/elliptic reports them.
	AllowedCurves []string
	AllowEd25519  bool
}

// DefaultKeyPolicy is what current Gateway implementations accept in practice.
func DefaultKeyPolicy() KeyPolicy {
	return KeyPolicy{
		MinRSABits:    2048,
		AllowedCurves: []string{"P-256", "P-384", "P-521"},
		// Ed25519 is still refused by enough terminating proxies that allowing
		// it by default would produce a cluster that installs and then cannot
		// serve. It is opt-in.
		AllowEd25519: false,
	}
}

// Check reports why a key is unacceptable, or an empty string when it is fine.
func (p KeyPolicy) Check(pub crypto.PublicKey) string {
	switch k := pub.(type) {
	case *rsa.PublicKey:
		if bits := k.N.BitLen(); bits < p.MinRSABits {
			return fmt.Sprintf("RSA %d-bit key is below the %d-bit minimum", bits, p.MinRSABits)
		}
		return ""
	case *ecdsa.PublicKey:
		name := k.Curve.Params().Name
		for _, c := range p.AllowedCurves {
			if c == name {
				return ""
			}
		}
		return fmt.Sprintf("ECDSA curve %s is not among the supported curves %v", name, p.AllowedCurves)
	case ed25519.PublicKey:
		if p.AllowEd25519 {
			return ""
		}
		return "Ed25519 is not accepted by the selected GatewayClass"
	}
	return fmt.Sprintf("unsupported key type %T", pub)
}
