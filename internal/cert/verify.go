package cert

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"platform.ryxen.dev/platformctl/internal/codes"
)

// The verification gates of docs/20-cert.md §4. Every one of them must pass
// before anything is applied.
//
// A gate reports the reason as well as the verdict. In an airgap nobody can
// look the answer up, so the finding has to carry what the operator needs to
// act: which file, which name, which issuer to go and ask for.

// Status is a gate outcome.
type Status string

const (
	StatusPass Status = "pass"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
	StatusSkip Status = "skip"
)

// Finding is one gate outcome.
//
// Deliberately not preflight.ProbeResult: the preflight layer consumes this
// package, and importing it back would be a cycle. The shapes are close enough
// that the adapter is a few lines.
type Finding struct {
	ID       string // PF-901
	Status   Status
	Severity codes.Severity
	Reason   string // CHAIN_INCOMPLETE
	Detail   string // English, fixed -- docs/11-execute.md language policy
	Evidence string
}

// Blocking reports whether this finding stops the run.
func (f Finding) Blocking() bool {
	return f.Status == StatusFail && f.Severity == codes.SeverityBlock
}

func pass(id, detail string) Finding {
	return Finding{ID: id, Status: StatusPass, Severity: codes.SeverityInfo, Detail: detail}
}

func skip(id, detail string) Finding {
	return Finding{ID: id, Status: StatusSkip, Severity: codes.SeverityInfo, Detail: detail}
}

func blockf(id, reason, format string, args ...any) Finding {
	return Finding{
		ID:       id,
		Status:   StatusFail,
		Severity: codes.SeverityBlock,
		Reason:   reason,
		Detail:   fmt.Sprintf(format, args...),
	}
}

func warnf(id, reason, format string, args ...any) Finding {
	return Finding{
		ID:       id,
		Status:   StatusWarn,
		Severity: codes.SeverityWarn,
		Reason:   reason,
		Detail:   fmt.Sprintf(format, args...),
	}
}

// gate runs every check and, when the input survives, returns what to build
// the bundle from.
//
// Selection and validation are one pass because they answer the same question:
// PF-903 fails precisely when no leaf can be selected, and PF-901 fails when
// the selected leaf has no key. Splitting them would mean deciding twice.
func gate(in Input, m Material) (*Cert, *Key, *Chain, []Finding) {
	var out []Finding

	// PF-910 first. A root private key in the input is a custody incident, and
	// nothing else about the material matters until it is dealt with.
	out = append(out, checkRootKey(m))

	out = append(out, checkLocked(m))
	out = append(out, checkDuplicateLeaves(m))

	leaf, sel := selectLeaf(in, m)
	out = append(out, sel)
	if leaf == nil {
		out = append(out, skip("PF-901", "no leaf was selected"),
			skip("PF-902", "no leaf was selected"),
			skip("PF-904", "no leaf was selected"),
			skip("PF-905", "no leaf was selected"),
			skip("PF-911", "no leaf was selected"))
		sortFindings(out)
		return nil, nil, nil, out
	}

	key, keyFinding := matchKey(*leaf, m)
	out = append(out, keyFinding)

	chain := Build(*leaf, m.Certs())
	out = append(out, checkChain(chain))
	out = append(out, checkValidity(in, *leaf)...)
	out = append(out, checkKeyPolicy(in, *leaf))

	sortFindings(out)
	if key == nil {
		return leaf, nil, &chain, out
	}
	return leaf, key, &chain, out
}

// PF-910: no root private key in the bundle.
//
// The offline root key must never leave its custody. Finding one in a customer
// drop is not a configuration problem to work around; the input is refused and
// somebody is told.
func checkRootKey(m Material) Finding {
	for _, k := range m.Keys {
		for _, r := range m.Roots {
			if k.Matches(r.Certificate) {
				return Finding{
					ID: "PF-910", Status: StatusFail, Severity: codes.SeverityBlock,
					Reason: "ROOT_KEY_PRESENT",
					Detail: fmt.Sprintf("the private key in %s belongs to the root certificate %q; "+
						"a root private key must never leave its custody and this input is refused",
						k.From, r.SubjectLine()),
					Evidence: r.Fingerprint(),
				}
			}
		}
	}
	return pass("PF-910", "no root private key is present in the input")
}

// PF-906: an encrypted key the passphrase did not open.
func checkLocked(m Material) Finding {
	if len(m.Locked) == 0 {
		if len(m.Keys) == 0 {
			return skip("PF-906", "no private key was found in the input")
		}
		for _, k := range m.Keys {
			if k.WasEncrypted {
				return pass("PF-906", fmt.Sprintf("the encrypted key in %s was decrypted", k.From))
			}
		}
		return skip("PF-906", "no private key in the input was encrypted")
	}
	l := m.Locked[0]
	return blockf("PF-906", "KEY_LOCKED",
		"the private key in %s is encrypted and was not decrypted: %v", l.From, l.Err)
}

// PF-912: two leaves claiming the same name.
//
// The correct one cannot be chosen automatically, and choosing wrongly puts an
// expired certificate on a listener nobody is watching. The message names both
// files and both expiry dates, because the fix is almost always to delete the
// older file.
func checkDuplicateLeaves(m Material) Finding {
	byName := map[string][]Cert{}
	for _, l := range m.Leaves {
		for _, n := range l.DNSNames {
			n = strings.ToLower(n)
			byName[n] = append(byName[n], l)
		}
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, n := range names {
		group := byName[n]
		if len(group) < 2 {
			continue
		}
		var parts []string
		for _, c := range group {
			parts = append(parts, fmt.Sprintf("%s (expires %s)",
				c.From, c.NotAfter.UTC().Format("2006-01-02")))
		}
		return blockf("PF-912", "DUPLICATE_LEAF",
			"%d leaf certificates claim %s: %s; remove the ones that do not belong",
			len(group), n, strings.Join(parts, ", "))
	}
	return pass("PF-912", "no two leaf certificates claim the same name")
}

// PF-903: the listener hostname is covered.
func selectLeaf(in Input, m Material) (*Cert, Finding) {
	if len(m.Leaves) == 0 {
		return nil, blockf("PF-903", "NO_LEAF",
			"no leaf certificate was found in the input; %d certificates were read (%d intermediate, %d root)",
			len(m.Certs()), len(m.Intermediates), len(m.Roots))
	}

	if len(in.Hostnames) == 0 {
		// Nothing to select against. One leaf is unambiguous; more than one is
		// not, and guessing is what §2.5 forbids.
		if len(m.Leaves) == 1 {
			l := m.Leaves[0]
			return &l, skip("PF-903", "no listener hostname was supplied; the single leaf in the input was used")
		}
		return nil, blockf("PF-903", "AMBIGUOUS_LEAF",
			"%d leaf certificates are present and no listener hostname was supplied to choose between them",
			len(m.Leaves))
	}

	// Every hostname must be covered by the same leaf: a listener presents one
	// certificate, so a leaf covering half the names is not a usable answer.
	var chosen *Cert
	for i := range m.Leaves {
		l := m.Leaves[i]
		all := true
		for _, h := range in.Hostnames {
			if !Covers(l.Certificate, h) {
				all = false
				break
			}
		}
		if !all {
			continue
		}
		if chosen != nil {
			return nil, blockf("PF-903", "AMBIGUOUS_LEAF",
				"both %s and %s cover %s; the correct one cannot be chosen automatically",
				chosen.From, l.From, strings.Join(in.Hostnames, ", "))
		}
		c := l
		chosen = &c
	}

	if chosen == nil {
		var have []string
		for _, l := range m.Leaves {
			have = append(have, fmt.Sprintf("%s: %s", l.From, strings.Join(SANsOf(l.Certificate), ", ")))
		}
		return nil, blockf("PF-903", "SAN_MISMATCH",
			"no leaf covers %s under RFC 6125 wildcard rules; the input presents %s",
			strings.Join(in.Hostnames, ", "), strings.Join(have, " | "))
	}
	return chosen, pass("PF-903", fmt.Sprintf("%s covers %s",
		chosen.From, strings.Join(in.Hostnames, ", ")))
}

// PF-901: the key belongs to the leaf.
func matchKey(leaf Cert, m Material) (*Key, Finding) {
	for i := range m.Keys {
		if m.Keys[i].Matches(leaf.Certificate) {
			k := m.Keys[i]
			return &k, pass("PF-901", fmt.Sprintf("the key in %s matches %s", k.From, leaf.From))
		}
	}
	if len(m.Keys) == 0 {
		return nil, blockf("PF-901", "KEY_MISSING",
			"no private key was found for the leaf certificate in %s", leaf.From)
	}
	var from []string
	for _, k := range m.Keys {
		from = append(from, k.From)
	}
	return nil, blockf("PF-901", "KEY_MISMATCH",
		"none of the private keys (%s) matches the public key in %s; "+
			"a key from another domain produces a handshake failure with no other symptom",
		strings.Join(from, ", "), leaf.From)
}

// PF-902: the chain is complete.
//
// "Complete" does not mean the root is present. A public CA does not ship its
// root and does not need to: the client already has it, and requiring one here
// would refuse every commercially issued certificate. So a walk that ends
// without a root is complete when the assembled chain verifies against the host
// trust store, and incomplete only when it does not -- which is the case
// docs/20-cert.md §3.2 is about, a missing intermediate that cannot be fetched
// over AIA in an airgap.
func checkChain(c Chain) Finding {
	if c.Complete() {
		return pass("PF-902", fmt.Sprintf("the chain is complete: %s", c.Describe()))
	}
	if ok, root := chainsToPublicRoot(c); ok {
		return pass("PF-902", fmt.Sprintf(
			"the chain is complete: %s; its root (%s) is publicly trusted and is not served", c.Describe(), root))
	}
	mi := c.MissingIssuer
	detail := fmt.Sprintf("the issuer of %s is missing from the input: %s", mi.Subject, mi.Issuer)
	if len(mi.AIA) > 0 {
		detail += fmt.Sprintf("; the issuer publishes it at %s", strings.Join(mi.AIA, ", "))
	}
	// The action matters more than the diagnosis here: in an airgap nothing can
	// be fetched, so the operator has to be told what file to obtain and where
	// to put it (§3.2).
	detail += "; obtain that certificate and place it in the same directory, then re-run"
	return Finding{
		ID: "PF-902", Status: StatusFail, Severity: codes.SeverityBlock,
		Reason: "CHAIN_INCOMPLETE", Detail: detail, Evidence: mi.Issuer,
	}
}

// PF-904 and PF-911: the validity window against the clock.
func checkValidity(in Input, leaf Cert) []Finding {
	var out []Finding

	if leaf.NotBefore.After(in.Now) {
		out = append(out, blockf("PF-911", "NOT_YET_VALID",
			"the certificate in %s is not valid until %s, which is later than the clock (%s); "+
				"cross-check PF-501 before assuming the certificate is wrong",
			leaf.From,
			leaf.NotBefore.UTC().Format(time.RFC3339),
			in.Now.UTC().Format(time.RFC3339)))
	} else {
		out = append(out, pass("PF-911", "the certificate is already valid at the current clock"))
	}

	switch {
	case leaf.NotAfter.Before(in.Now):
		out = append(out, blockf("PF-904", "CERT_EXPIRED",
			"the certificate in %s expired on %s", leaf.From,
			leaf.NotAfter.UTC().Format(time.RFC3339)))
	default:
		left := leaf.NotAfter.Sub(in.Now)
		days := int(left.Hours() / 24)
		if days <= in.ExpiryWarningDays {
			out = append(out, warnf("PF-904", "CERT_EXPIRING",
				"the certificate in %s expires in %d days, on %s, which is inside the %d-day warning window; "+
					"a BYO certificate does not renew itself",
				leaf.From, days, leaf.NotAfter.UTC().Format("2006-01-02"), in.ExpiryWarningDays))
		} else {
			out = append(out, pass("PF-904", fmt.Sprintf("the certificate expires in %d days, on %s",
				days, leaf.NotAfter.UTC().Format("2006-01-02"))))
		}
	}
	return out
}

// PF-905: the key is one the GatewayClass will serve.
func checkKeyPolicy(in Input, leaf Cert) Finding {
	if why := in.KeyPolicy.Check(leaf.PublicKey); why != "" {
		return blockf("PF-905", "KEY_UNSUPPORTED",
			"the certificate in %s uses a key the selected GatewayClass will not serve: %s", leaf.From, why)
	}
	return pass("PF-905", fmt.Sprintf("the %s key is within what the GatewayClass supports",
		keyDescription(leaf.PublicKey)))
}

// sortFindings puts the findings in code order, so a report of the same input
// reads the same way every time.
func sortFindings(f []Finding) {
	sort.SliceStable(f, func(i, j int) bool { return f[i].ID < f[j].ID })
}
