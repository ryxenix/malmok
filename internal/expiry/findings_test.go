package expiry

import (
	"testing"
	"time"

	"github.com/ryxenix/malmok/internal/cert"
	"github.com/ryxenix/malmok/internal/codes"
)

// row builds one measured certificate at a given distance from now.
func row(kind cert.Kind, subject string, days float64) Row {
	return Row{
		Node:     "10.10.0.11",
		Path:     Dir + "/" + subject + ".crt",
		Subject:  "CN=" + subject,
		NotAfter: base.Add(time.Duration(days * float64(24*time.Hour))),
		Kind:     kind,
	}
}

// find returns the finding with the given code, or fails.
func find(t *testing.T, got []Finding, code string) Finding {
	t.Helper()
	for _, f := range got {
		if f.Code == code {
			return f
		}
	}
	t.Fatalf("no %s in %+v", code, got)
	return Finding{}
}

// A code that is not in the registry cannot reach an audit report: the event
// schema rejects it, and the run is over before anybody notices the gap. This
// is the same guard the preflight package keeps over its own probes.
func TestEveryEmittedCodeIsRegistered(t *testing.T) {
	inv := Inventory{Rows: []Row{
		row(cert.KindLeaf, "kube-apiserver", 10),
		row(cert.KindRoot, "server-ca", 100),
		row(cert.KindIntermediate, "etcd-ca", 100),
	}}

	for _, f := range Assess(inv, base) {
		if _, ok := codes.Lookup(f.Code); !ok {
			t.Errorf("%s is not registered in internal/codes", f.Code)
		}
	}
}

// The series decide the thresholds. A leaf and a CA at the same distance from
// expiry are different items with different answers, and collapsing them is
// the mistake the four-series model exists to prevent.
func TestSeriesSelectsCodeAndThreshold(t *testing.T) {
	tests := []struct {
		name       string
		kind       cert.Kind
		days       float64
		wantCode   string
		wantSeries Series
		wantStep   int
	}{
		// Series B, 365 days, alerting at 150 / 100 / 30.
		{"leaf well clear", cert.KindLeaf, 200, "MC-111", SeriesInternal, 0},
		{"leaf at the first step", cert.KindLeaf, 150, "MC-111", SeriesInternal, 150},
		{"leaf past the first step", cert.KindLeaf, 130, "MC-111", SeriesInternal, 150},
		{"leaf past the second", cert.KindLeaf, 95, "MC-111", SeriesInternal, 100},
		{"leaf past the last", cert.KindLeaf, 20, "MC-111", SeriesInternal, 30},

		// Series C, ten years, one step at 180. A self-signed root and an
		// intermediate both need rotate-ca, so both are series C.
		{"root well clear", cert.KindRoot, 3650, "MC-121", SeriesCA, 0},
		{"root inside six months", cert.KindRoot, 179, "MC-121", SeriesCA, 180},
		{"intermediate is series C too", cert.KindIntermediate, 179, "MC-121", SeriesCA, 180},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Assess(Inventory{Rows: []Row{row(tt.kind, "x", tt.days)}}, base)
			f := find(t, got, tt.wantCode)
			if f.Series != tt.wantSeries {
				t.Errorf("series %q, want %q", f.Series, tt.wantSeries)
			}
			if f.Step != tt.wantStep {
				t.Errorf("step %d, want %d", f.Step, tt.wantStep)
			}
		})
	}
}

// The renewal window is a separate item because it reports something that can
// be done -- restart and the certificate rotates -- which "97 days remaining"
// does not say. The first alert step sits outside the window on purpose, so
// there is a period where MC-111 has fired and MC-112 has not.
func TestRenewalWindowIsItsOwnItem(t *testing.T) {
	tests := []struct {
		name       string
		days       float64
		wantWindow bool
	}{
		{"outside the window, first step already crossed", 130, false},
		{"exactly at the window edge", 120, true},
		{"inside the window", 119, true},
		{"expired", -3, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Assess(Inventory{Rows: []Row{row(cert.KindLeaf, "kube-etcd", tt.days)}}, base)

			var window bool
			for _, f := range got {
				if f.Code == "MC-112" {
					window = true
				}
			}
			if window != tt.wantWindow {
				t.Errorf("MC-112 present = %v, want %v (%+v)", window, tt.wantWindow, got)
			}
			// MC-111 is reported either way. A cluster's remaining days are
			// stated in every report, not only once they are alarming.
			find(t, got, "MC-111")
		})
	}
}

// A CA never enters a renewal window: there is no restart that rotates it.
func TestCertificateAuthorityHasNoRenewalWindowItem(t *testing.T) {
	got := Assess(Inventory{Rows: []Row{row(cert.KindRoot, "server-ca", 30)}}, base)

	for _, f := range got {
		if f.Code == "MC-112" {
			t.Errorf("a CA was reported as restart-renewable: %+v", f)
		}
	}
}

// Expiry is reported as a negative number of days, not clamped to zero. "it
// expired eleven days ago" and "it expires today" lead to different actions
// and must not share a value.
func TestExpiredReadsAsNegativeDays(t *testing.T) {
	tests := []struct {
		name string
		days float64
		want int
	}{
		{"expires in a year", 365, 365},
		{"expires today", 0, 0},
		// Four hours past expiry floors to -1 rather than truncating to 0,
		// which would place it in the same column as a healthy certificate.
		{"four hours past expiry", -4.0 / 24.0, -1},
		{"eleven days past expiry", -11, -11},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Assess(Inventory{Rows: []Row{row(cert.KindLeaf, "kube-etcd", tt.days)}}, base)
			if f := find(t, got, "MC-111"); f.Days != tt.want {
				t.Errorf("days %d, want %d", f.Days, tt.want)
			}
		})
	}
}

// Every row is reported. A report listing only what is near expiry cannot be
// told apart from one where the measurement never ran, and the registry's own
// wording for MC-121 requires the CA date in every handover.
func TestEveryRowProducesAFinding(t *testing.T) {
	inv := Inventory{Rows: []Row{
		row(cert.KindLeaf, "kube-apiserver", 300),
		row(cert.KindLeaf, "kube-etcd", 300),
		row(cert.KindRoot, "server-ca", 3600),
	}}

	got := Assess(inv, base)
	if len(got) != 3 {
		t.Fatalf("got %d findings for 3 certificates: %+v", len(got), got)
	}
	for _, f := range got {
		if f.Step != 0 {
			t.Errorf("%s reported step %d, but nothing is near expiry", f.Subject, f.Step)
		}
		if f.Node == "" || f.Path == "" || f.Subject == "" {
			t.Errorf("a finding lost its identity: %+v", f)
		}
		if f.NotAfter.IsZero() {
			t.Errorf("a finding lost the measurement: %+v", f)
		}
	}
}

// What could not be measured must not arrive as a finding. A problem turned
// into an item with no expiry date reads, in a table of dates, as a
// certificate that is fine.
func TestProblemsDoNotBecomeFindings(t *testing.T) {
	inv := Inventory{
		Node: "10.10.0.12",
		Problems: []Problem{
			{Node: "10.10.0.12", Reason: ReasonUnreachable, Detail: "timed out"},
			{Node: "10.10.0.12", Path: Dir + "/locked.crt", Reason: ReasonUnreadable},
		},
	}

	if got := Assess(inv, base); len(got) != 0 {
		t.Errorf("problems produced findings: %+v", got)
	}
}
