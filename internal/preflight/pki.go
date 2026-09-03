package preflight

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ryxenix/malmok/api/v1alpha1"
	"github.com/ryxenix/malmok/internal/cert"
	"github.com/ryxenix/malmok/internal/codes"
)

// PF-704 through PF-708 are about the material the operator supplies for the
// cluster's own PKI and for an airgapped install, as distinct from the gateway
// certificates PF-9xx covers.
//
// They live here rather than in the cert package because they are about the
// document's PKI block and about files on the machine running the tool, not
// about assembling a serving bundle.

// PKIMaterial is what the caller resolved from the document's SourceRefs.
//
// Resolution is the loader's business: it knows the document's directory and
// its secret policy. This package is handed bytes.
type PKIMaterial struct {
	// RootCert and Intermediate are pki.privateCA.
	RootCert         []byte
	IntermediateCert []byte
	IntermediateKey  []byte
	// RegistryCA is registry.caCert, checked alongside because it is the same
	// class of mistake and the same class of consequence.
	RegistryCA []byte
}

// Empty reports whether there is nothing to check.
func (m PKIMaterial) Empty() bool {
	return len(m.RootCert) == 0 && len(m.IntermediateCert) == 0 &&
		len(m.IntermediateKey) == 0 && len(m.RegistryCA) == 0
}

// CheckPKIMaterial implements PF-704, PF-705 and PF-706.
//
// One pass over the same material, because they are three questions about one
// set of files and reading it three times would let them disagree.
func CheckPKIMaterial(spec v1alpha1.ClusterSpec, m PKIMaterial, now time.Time) []ProbeResult {
	if spec.PKI.Mode == v1alpha1.PKINone || m.Empty() {
		why := "no private CA material is supplied"
		if spec.PKI.Mode == v1alpha1.PKINone {
			why = "the document issues no certificates (pki.mode: none)"
		}
		return []ProbeResult{
			skipped("PF-704", why), skipped("PF-705", why), skipped("PF-706", why),
		}
	}
	if now.IsZero() {
		now = time.Now()
	}

	files := []cert.File{
		{Name: "pki.privateCA.rootCert", Data: m.RootCert},
		{Name: "pki.privateCA.intermediateCert", Data: m.IntermediateCert},
		{Name: "pki.privateCA.intermediateKey", Data: m.IntermediateKey},
		{Name: "registry.caCert", Data: m.RegistryCA},
	}
	mat := cert.Scan(files, nil)

	return []ProbeResult{
		checkIntermediate(mat, now),
		checkPKINotBefore(mat, now),
		checkPKIRootKey(mat),
	}
}

// checkIntermediate implements PF-704.
func checkIntermediate(m cert.Material, now time.Time) ProbeResult {
	if len(m.Intermediates) == 0 {
		if len(m.Roots) > 0 {
			return failf("PF-704", "INTERMEDIATE_MISSING",
				"the material holds a root but no intermediate; cert-manager signs with the intermediate "+
					"and the root private key must stay offline, so there is nothing here that can issue")
		}
		return skipped("PF-704", "no intermediate certificate is present in the material")
	}

	for _, inter := range m.Intermediates {
		chain := cert.Build(inter, m.Certs())
		if !chain.Complete() {
			mi := chain.MissingIssuer
			return failf("PF-704", "INTERMEDIATE_UNCHAINED",
				"the intermediate %q does not chain to the supplied root; its issuer is %s and no such "+
					"certificate is in the material", inter.SubjectLine(), mi.Issuer)
		}

		// An intermediate that expires before the leaves it will issue is a
		// failure that arrives months later, when renewals start producing
		// certificates nothing accepts.
		left := inter.NotAfter.Sub(now)
		switch {
		case left <= 0:
			return failf("PF-704", "INTERMEDIATE_EXPIRED",
				"the intermediate %q expired on %s", inter.SubjectLine(),
				inter.NotAfter.UTC().Format("2006-01-02"))
		case left < 90*24*time.Hour:
			return warnf("PF-704", "INTERMEDIATE_EXPIRING",
				"the intermediate %q expires in %d days, on %s; every certificate it issues is capped "+
					"at that date, so renewals will start producing certificates that outlive their issuer",
				inter.SubjectLine(), int(left.Hours()/24), inter.NotAfter.UTC().Format("2006-01-02"))
		}
	}

	// The key has to belong to the intermediate, or nothing can sign.
	if len(m.Keys) > 0 {
		matched := false
		for _, k := range m.Keys {
			for _, inter := range m.Intermediates {
				if k.Matches(inter.Certificate) {
					matched = true
				}
			}
		}
		if !matched {
			return failf("PF-704", "INTERMEDIATE_KEY_MISMATCH",
				"the supplied intermediate key does not match any intermediate certificate in the material")
		}
	}

	return passf("PF-704", "the intermediate chains to the supplied root and is valid until %s",
		m.Intermediates[0].NotAfter.UTC().Format("2006-01-02"))
}

// checkPKINotBefore implements PF-705.
func checkPKINotBefore(m cert.Material, now time.Time) ProbeResult {
	for _, c := range m.Certs() {
		if c.NotBefore.After(now) {
			return failf("PF-705", "NOT_YET_VALID",
				"%s in %s is not valid until %s, which is later than the clock (%s); "+
					"cross-check PF-501 before assuming the certificate is at fault",
				c.SubjectLine(), c.From,
				c.NotBefore.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
		}
	}
	return passf("PF-705", "every supplied certificate is already valid at the current clock")
}

// checkPKIRootKey implements PF-706.
//
// The same rule as PF-910 and for the same reason, applied to the cluster's own
// PKI rather than to a gateway bundle. An offline root key that has been copied
// into a build directory is no longer offline, and no amount of care afterwards
// undoes that.
func checkPKIRootKey(m cert.Material) ProbeResult {
	for _, k := range m.Keys {
		for _, r := range m.Roots {
			if k.Matches(r.Certificate) {
				return ProbeResult{
					ID: "PF-706", Status: StatusFail, Severity: codes.SeverityBlock,
					Code: "ROOT_KEY_PRESENT",
					Detail: fmt.Sprintf(
						"%s holds the private key of the root %q; the root key must stay in its custody, "+
							"and a copy that has reached a build directory is no longer in it",
						k.From, r.SubjectLine()),
					Evidence: r.Fingerprint(),
				}
			}
		}
	}
	return passf("PF-706", "no root private key is present in the supplied material")
}

// ---------------------------------------------------------------------------
// PF-707: the airgap bundle
// ---------------------------------------------------------------------------

// CheckAirgapBundle implements PF-707.
//
// A bundle is one source of images an airgapped install can have, and this
// check is about that one: a truncated download is the common failure and it
// does not announce itself, so the install proceeds until the first missing
// layer, by which point half the cluster is up.
//
// It is not the only source, and for a long time this check said it was --
// failing every air-gapped document that carried RKE2's own image archives or
// named a mirrored registry, which is to say every air-gapped document that
// would actually have worked. The bundle is the one thing here nothing loads
// yet. So an empty bundle is a question about the others, and only a document
// that names none of the three has nowhere to pull from.
func CheckAirgapBundle(spec v1alpha1.ClusterSpec, docDir string) ProbeResult {
	if spec.Network.Mode != v1alpha1.NetworkAirgap {
		return skipped("PF-707", "the network mode is not airgap")
	}
	raw := strings.TrimSpace(spec.Registry.Bundle)
	if raw == "" {
		// PF-709 reads the artifact path on the node and reports whether the
		// image archive is really in it. Here the document is all there is.
		if p := strings.TrimSpace(spec.Kubernetes.ArtifactPath); p != "" {
			return skipped("PF-707",
				"no registry.bundle; the images come from RKE2's own archives in "+p+" (PF-709 reads them)")
		}
		if r := strings.TrimSpace(spec.Registry.SystemDefaultRegistry); r != "" {
			return skipped("PF-707",
				"no registry.bundle; the images come from "+r)
		}
		return failf("PF-707", "BUNDLE_MISSING",
			"network.mode is airgap and none of registry.bundle, kubernetes.artifactPath "+
				"or registry.systemDefaultRegistry is set; there is no source of images")
	}

	path := resolveLocalPath(raw, docDir)
	info, err := os.Stat(path)
	if err != nil {
		return failf("PF-707", "BUNDLE_MISSING",
			"the airgap bundle %s cannot be read: %v", path, err)
	}
	if info.IsDir() {
		return failf("PF-707", "BUNDLE_MISSING",
			"registry.bundle points at a directory (%s); it has to be the Hauler artifact itself", path)
	}
	if info.Size() == 0 {
		return failf("PF-707", "BUNDLE_TRUNCATED", "the airgap bundle %s is empty", path)
	}

	// The checksum sidecar is the convention Hauler and every release process
	// around it produce. Without one there is nothing to verify against, and
	// saying so is more useful than computing a digest nobody can compare.
	sidecar := path + ".sha256"
	want, err := os.ReadFile(sidecar)
	if err != nil {
		return warnf("PF-707", "BUNDLE_UNVERIFIED",
			"the airgap bundle %s is present (%s) and there is no %s beside it to verify it against; "+
				"a truncated bundle is not detected until the first missing layer, "+
				"by which point half the cluster is up",
			path, humanBytes(info.Size()), filepath.Base(sidecar))
	}

	sum, err := fileSHA256(path)
	if err != nil {
		return unmeasured("PF-707", "the bundle could not be read to the end: "+err.Error())
	}
	expected := strings.ToLower(strings.Fields(string(want) + " ")[0])
	if expected != sum {
		return failf("PF-707", "BUNDLE_CHECKSUM_MISMATCH",
			"the airgap bundle %s does not match %s: the file is %s and the checksum says %s; "+
				"the usual cause is a transfer that was interrupted and resumed",
			filepath.Base(path), filepath.Base(sidecar), sum, expected)
	}
	return passf("PF-707", "the airgap bundle %s (%s) matches its checksum",
		filepath.Base(path), humanBytes(info.Size()))
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// resolveLocalPath turns a file:// SourceRef or a bare path into a path on this
// machine, relative to the document.
func resolveLocalPath(raw, docDir string) string {
	if u, err := url.Parse(raw); err == nil && u.Scheme == "file" {
		raw = u.Opaque
		if raw == "" {
			raw = u.Host + u.Path
		}
	}
	if filepath.IsAbs(raw) || docDir == "" {
		return raw
	}
	return filepath.Join(docDir, raw)
}

// ---------------------------------------------------------------------------
// PF-708: ACME prerequisites
// ---------------------------------------------------------------------------

// CheckACME implements PF-708.
//
// ACME fails in ways that are cheap to predict and expensive to discover during
// an install window: an airgapped site cannot reach a CA at all, HTTP-01 needs
// the world to reach port 80 on an address the document may not even pin, and
// DNS-01 needs a provider credential nobody remembered to supply.
func (p *Prober) CheckACME(ctx context.Context, spec v1alpha1.ClusterSpec, tokenSupplied bool) ProbeResult {
	mode := spec.PKI.Mode
	if mode != v1alpha1.PKIACMEDNS01 && mode != v1alpha1.PKIACMEHTTP01 {
		return skipped("PF-708", "the document does not use ACME")
	}

	if spec.Network.Mode == v1alpha1.NetworkAirgap {
		return failf("PF-708", "ACME_IN_AIRGAP",
			"pki.mode is %s and the network mode is airgap; an ACME certificate authority "+
				"cannot be reached from a disconnected site", mode)
	}

	acme := spec.PKI.ACME
	if acme == nil {
		return failf("PF-708", "ACME_UNCONFIGURED", "pki.mode is %s and pki.acme is absent", mode)
	}
	if strings.TrimSpace(acme.Email) == "" {
		// Let's Encrypt sends expiry warnings there, and it is the only notice
		// anybody gets when renewal has been silently failing.
		return failf("PF-708", "ACME_EMAIL_MISSING",
			"pki.acme.email is empty; it is where the certificate authority sends expiry warnings, "+
				"and those are the only notice anybody gets that renewal has stopped working")
	}

	if mode == v1alpha1.PKIACMEDNS01 {
		if strings.TrimSpace(acme.DNSProvider) == "" {
			return failf("PF-708", "ACME_DNS_PROVIDER_MISSING",
				"pki.mode is acme-dns01 and pki.acme.dnsProvider is empty")
		}
		if !tokenSupplied {
			return failf("PF-708", "ACME_TOKEN_MISSING",
				"pki.mode is acme-dns01 with provider %q and no API token resolved from pki.acme.apiToken",
				acme.DNSProvider)
		}
	}

	// HTTP-01 needs the challenge to arrive from the internet, which means a
	// gateway whose address is known before the install rather than allocated
	// during it -- the DNS record has to exist for the challenge to be
	// delivered at all.
	//
	// A node-ips gateway already satisfies that. It is not given an address:
	// Envoy binds the port in the node's own network namespace, so the
	// addresses are the ones the nodes already hold, and they were decided
	// long before this document was written. Demanding a pin there asked the
	// operator to write down an address the gateway does not use, and the one
	// value that would look right -- the node's public address -- lands in the
	// Gateway's spec.addresses, which is not how a host-networked gateway
	// works. Found on a live single-node install where the check blocked an
	// issue that would otherwise have succeeded.
	if mode == v1alpha1.PKIACMEHTTP01 {
		var unpinned []string
		for _, gw := range spec.Gateway.Gateways {
			if gw.Exposure == v1alpha1.ExposureNodeIPs {
				continue
			}
			if gw.Address == "" {
				unpinned = append(unpinned, gw.Name)
			}
		}
		if len(unpinned) > 0 {
			return failf("PF-708", "ACME_HTTP01_UNPINNED",
				"pki.mode is acme-http01 and %s have no pinned address; the challenge is delivered to "+
					"whatever the DNS record points at, so the address has to be decided before install "+
					"rather than allocated during it", strings.Join(unpinned, ", "))
		}
	}

	// Reachability of the directory endpoint is the last cheap thing to check.
	server := acme.Server
	if server == "" {
		server = "https://acme-v02.api.letsencrypt.org/directory"
	}
	if p == nil || p.Dialer == nil {
		return passf("PF-708", "the ACME configuration is complete for %s", mode)
	}

	u, err := url.Parse(server)
	if err != nil || u.Host == "" {
		return failf("PF-708", "ACME_SERVER_INVALID", "pki.acme.server %q is not a URL", server)
	}
	host := u.Host
	if u.Port() == "" {
		host += ":443"
	}

	c, cancel := context.WithTimeout(ctx, p.timeout())
	defer cancel()
	conn, err := p.Dialer.DialContext(c, "tcp", host)
	if err != nil {
		return failf("PF-708", "ACME_UNREACHABLE",
			"the ACME directory %s is not reachable from this machine: %v; "+
				"the cluster reaches it from wherever cert-manager runs, which may differ, "+
				"but a site that cannot reach it at all will not issue",
			server, err)
	}
	conn.Close()
	return passf("PF-708", "the ACME configuration is complete and %s is reachable", u.Host)
}
