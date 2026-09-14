package expiry

import (
	"math"
	"time"

	"github.com/ryxenix/malmok/internal/cert"
)

// Turning measurements into the maintenance items the registry already
// defines.
//
// No new diagnostic codes are minted here. MC-111, MC-112 and MC-121 have been
// in internal/codes since the maintenance family was written, have been
// published in docs/99-codes.md, and nothing has ever emitted one -- the whole
// MC family had no producer. What was missing was never the numbers.
//
// PF-9xx would have been the wrong place even though it is the certificate
// block: its own header reserves it for validation of certificate material an
// operator SUPPLIES, and these certificates are ones RKE2 issued to itself.
// MC is the family for periodic inspection of a cluster that already runs,
// which is exactly what this is.

// Series names which certificate family a row belongs to. The four differ in
// issuer, lifetime, renewal procedure and what breaks during renewal, so they
// do not share a threshold and must not share a pipeline.
type Series string

const (
	// SeriesInternal is series B: the leaf certificates RKE2 issues to
	// apiserver, etcd, kubelet, controller-manager, scheduler and the auth
	// proxy. 365 days, renewed by restarting inside the renewal window, one
	// node interrupted at a time.
	SeriesInternal Series = "B"
	// SeriesCA is series C: RKE2's own self-signed CAs. Ten years, renewed
	// only by rotate-ca, and rotating one stops the whole cluster.
	SeriesCA Series = "C"
)

// RenewalWindowDays is how close to expiry a series B certificate has to be
// before restarting the service rotates it.
//
// 120 days on RKE2 releases from May 2025 onward; it was 90 before that. A
// restart outside the window renews nothing, which is why "we restarted it"
// and "it was renewed" are different statements.
const RenewalWindowDays = 120

// Alert steps, in days remaining. Series B and C have no way to override
// these: only a series A bundle carries expiryWarningDays, and borrowing that
// field for certificates RKE2 issued to itself would apply a number chosen for
// one customer's certificate purchasing lead time to a cluster's own PKI.
var (
	// internalSteps is series B. The first step is deliberately outside the
	// 120-day renewal window: scheduling a maintenance slot with a customer
	// takes longer than the window leaves, so the first notice has to arrive
	// before a restart would begin rotating anything.
	internalSteps = []int{150, 100, 30}
	// caSteps is series C. One step, six months out, because the answer is
	// always a planned disruptive procedure and never an urgent one.
	caSteps = []int{180}
)

// Finding is one maintenance item about one certificate.
//
// It carries facts and the alert step that was crossed, and no verdict. MC
// codes are registered without a severity on purpose: "normal / attention /
// action required" is graded at runtime against thresholds negotiated per
// contract, and freezing one site's agreement into the definition is how a
// report ends up asserting something the customer never agreed to.
type Finding struct {
	// Code is the registry identifier: MC-111, MC-112 or MC-121.
	Code string `json:"code"`
	// Node, Path and Subject identify the certificate this is about.
	Node    string `json:"node"`
	Path    string `json:"path"`
	Subject string `json:"subject"`
	// NotAfter is the measurement. Days is derived from it.
	NotAfter time.Time `json:"notAfter"`
	// Series is which family the certificate belongs to.
	Series Series `json:"series"`
	// Days is whole days remaining, floored, and negative once expired:
	// -11 reads as "expired eleven days ago", which is what the operator has
	// to act on.
	Days int `json:"days"`
	// Step is the alert threshold crossed, in days, or zero when none has
	// been. Zero is not "fine" on its own -- an item is still reported, because
	// a handover has to state when the cluster's own CA expires whether or not
	// that date is near.
	Step int `json:"step"`
}

// Assess turns one node's inventory into maintenance findings.
//
// Every row produces a finding. The registry's own wording for MC-121 requires
// it -- the CA's expiry "must appear in every report and in the handover" --
// and the same holds for series B: a report that lists only what is near
// expiry cannot be told apart from a report where the measurement failed.
//
// Problems are not turned into findings. What could not be measured stays in
// Inventory.Problems, where it cannot be mistaken for a certificate that was
// measured and found healthy.
func Assess(inv Inventory, now time.Time) []Finding {
	var out []Finding

	// A CA is carried inside every bundle it signed, so one directory holds
	// the same four CA certificates a dozen times over. Measured on a live
	// server: thirteen files, twenty-one certificates, four distinct CAs
	// reported thirteen times. Each copy is the same fact, and printing it
	// once per file invites an operator to believe there are thirteen
	// authorities to rotate.
	seenCA := map[string]bool{}

	for _, r := range inv.Rows {
		days := wholeDays(r.Remaining(now))

		series, code, steps := SeriesInternal, "MC-111", internalSteps
		// Anything that can sign is a CA of RKE2's own making: server-ca,
		// client-ca, etcd's peer and server CAs, the request header CA. They
		// are self-signed roots, and intermediates are classified alongside
		// them rather than as leaves, because what matters here is the renewal
		// procedure and both need rotate-ca.
		if r.Kind != cert.KindLeaf {
			series, code, steps = SeriesCA, "MC-121", caSteps

			// Identity is the certificate, not the file it was found in.
			key := r.Node + "\x00" + r.Subject + "\x00" + r.NotAfter.UTC().String()
			if seenCA[key] {
				continue
			}
			seenCA[key] = true
		}

		out = append(out, Finding{
			Code:     code,
			Node:     r.Node,
			Path:     r.Path,
			Subject:  r.Subject,
			NotAfter: r.NotAfter,
			Series:   series,
			Days:     days,
			Step:     crossed(days, steps),
		})

		// A series B certificate inside the renewal window is a separate item
		// and not a louder version of the first one: it says a restart now
		// rotates the certificate, which is an action that can be taken, where
		// MC-111 only says how long is left.
		if series == SeriesInternal && days <= RenewalWindowDays {
			out = append(out, Finding{
				Code:     "MC-112",
				Node:     r.Node,
				Path:     r.Path,
				Subject:  r.Subject,
				NotAfter: r.NotAfter,
				Series:   series,
				Days:     days,
				Step:     RenewalWindowDays,
			})
		}
	}

	return out
}

// wholeDays floors a duration to days.
//
// Floored rather than truncated so that expiry stays negative: truncation
// turns "expired four hours ago" into 0, and a zero that means "expired" sits
// in the same column as a zero that means "expires today".
func wholeDays(d time.Duration) int {
	return int(math.Floor(d.Hours() / 24))
}

// crossed returns the most urgent alert step the remaining days have passed,
// or zero when none has been.
func crossed(days int, steps []int) int {
	best := 0
	for _, s := range steps {
		if days <= s && (best == 0 || s < best) {
			best = s
		}
	}
	return best
}
