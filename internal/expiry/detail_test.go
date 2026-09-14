package expiry

import (
	"strings"
	"testing"
	"time"

	"github.com/ryxenix/malmok/internal/cert"
	"github.com/ryxenix/malmok/internal/event"
)

// Every line this package produces has to satisfy the event schema, or the
// finding never reaches the audit report. The schema is the authority, so the
// test asks it rather than reimplementing its rules.
func TestEveryLineSatisfiesTheEventSchema(t *testing.T) {
	inv := Inventory{
		Node: "10.10.0.11",
		Rows: []Row{
			row(cert.KindLeaf, "kube-apiserver", 20),
			row(cert.KindLeaf, "kube-etcd", 300),
			row(cert.KindRoot, "server-ca", 100),
			// A subject a customer CA can really have.
			{Node: "10.10.0.11", Path: Dir + "/x.crt", Kind: cert.KindRoot,
				Subject:  "CN=사설 CA,O=한국회사",
				NotAfter: base.AddDate(0, 0, 30)},
		},
		Problems: []Problem{
			{Node: "10.10.0.11", Reason: ReasonUnreachable, Detail: "dial tcp: i/o timeout"},
			{Node: "10.10.0.12", Reason: ReasonDirMissing},
			{Node: "10.10.0.11", Path: Dir + "/locked.crt", Reason: ReasonUnreadable},
			{Node: "10.10.0.11", Path: Dir + "/notes.crt", Reason: ReasonNotPEM},
			{Node: "10.10.0.11", Reason: ReasonNoPrivilege,
				Detail: Dir + " is not visible to this account, which runs unprivileged"},
			{Node: "10.10.0.11", Reason: ReasonEmpty,
				Detail: Dir + " is readable and holds no certificate"},
		},
	}

	var lines []string
	for _, f := range Assess(inv, base) {
		lines = append(lines, f.Line())
	}
	for _, p := range inv.Problems {
		lines = append(lines, p.Line())
	}

	for _, line := range lines {
		e := event.Event{
			TS: event.NewTimestamp(time.Now()), Run: "01JBQ8F2K3M5N7P9R1S3T5V7W9", Seq: 1,
			Kind: event.KindProbe, Code: "MC-111", Detail: line,
		}
		if errs := e.Validate(); errs != nil {
			t.Errorf("the schema rejects %q: %v", line, errs)
		}
	}
}

// A DN is written by whoever issued the certificate. Losing the finding
// because the issuer used their own alphabet is the wrong trade.
func TestNonASCIISubjectStillProducesAUsableLine(t *testing.T) {
	f := Finding{
		Code: "MC-121", Subject: "CN=사설 CA", NotAfter: base, Days: 10,
	}

	got := f.Line()
	for _, r := range got {
		if r > 0x7e {
			t.Fatalf("a non-ASCII rune survived into %q", got)
		}
	}
	if !strings.Contains(got, "expires on") {
		t.Errorf("the line lost its meaning: %q", got)
	}
}

func TestLineWording(t *testing.T) {
	tests := []struct {
		name  string
		f     Finding
		want  []string
		avoid []string
	}{
		{
			name: "an internal leaf with time left",
			f:    Finding{Code: "MC-111", Subject: "CN=kube-etcd", NotAfter: base, Days: 97},
			want: []string{"CN=kube-etcd", "in 97 days", "1970-01-01"[:0] + base.Format("2006-01-02")},
		},
		{
			// Past expiry the sentence changes tense rather than printing a
			// negative number, which reads as a typo in a customer report.
			name:  "an expired leaf",
			f:     Finding{Code: "MC-111", Subject: "CN=kube-etcd", NotAfter: base, Days: -11},
			want:  []string{"expired on", "11 days ago"},
			avoid: []string{"-11"},
		},
		{
			// The renewal-window item exists to say what can be done, so the
			// action has to be in the sentence.
			name: "the renewal window",
			f:    Finding{Code: "MC-112", Subject: "CN=kube-etcd", NotAfter: base, Days: 97},
			want: []string{"renewal window", "restarting the service rotates it", "120-day"},
		},
		{
			// The CA's cost is the whole cluster, and a line that does not say
			// so invites somebody to treat it like the others.
			name: "the certificate authority",
			f:    Finding{Code: "MC-121", Subject: "CN=rke2-server-ca", NotAfter: base, Days: 170},
			want: []string{"the RKE2 CA", "stops the whole cluster"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.f.Line()
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("%q does not contain %q", got, w)
				}
			}
			for _, a := range tt.avoid {
				if strings.Contains(got, a) {
					t.Errorf("%q contains %q", got, a)
				}
			}
		})
	}
}

// A problem has to name what it could not measure and why. "unknown" with no
// reason is the same as silence to the person holding the report.
func TestProblemLinesNameTheReason(t *testing.T) {
	tests := []struct {
		name           string
		p              Problem
		wantApplicable bool
		want           []string
	}{
		{
			name:           "unreachable",
			p:              Problem{Reason: ReasonUnreachable, Detail: "dial tcp: i/o timeout"},
			wantApplicable: true,
			want:           []string{"could not be reached", "i/o timeout"},
		},
		{
			// An agent is not a failure. Reporting it as one puts a permanent
			// red line in every report for a node behaving as designed.
			name:           "no server on this node",
			p:              Problem{Reason: ReasonDirMissing},
			wantApplicable: false,
			want:           []string{"runs no RKE2 server"},
		},
		{
			name:           "unreadable file",
			p:              Problem{Path: Dir + "/locked.crt", Reason: ReasonUnreadable, Detail: "Permission denied"},
			wantApplicable: true,
			want:           []string{"locked.crt", "could not be read", "Permission denied"},
		},
		{
			name:           "not a certificate",
			p:              Problem{Path: Dir + "/notes.crt", Reason: ReasonNotPEM},
			wantApplicable: true,
			want:           []string{"notes.crt", "no certificate"},
		},
		{
			// Not an agent. The line has to say that nothing was established,
			// or a reader takes it for a node that legitimately holds none.
			name:           "not allowed to look",
			p:              Problem{Reason: ReasonNoPrivilege, Detail: Dir + " is not visible to this account"},
			wantApplicable: true,
			want:           []string{"nothing was measured", "has not established that it is empty"},
		},
		{
			// Measured, and still not a pass. The line has to point at the
			// node rather than at the scan.
			name:           "readable and empty",
			p:              Problem{Reason: ReasonEmpty, Detail: Dir + " is readable and holds no certificate"},
			wantApplicable: true,
			want:           []string{"nothing was measured", "needs looking at"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.Applicable(); got != tt.wantApplicable {
				t.Errorf("applicable = %v, want %v", got, tt.wantApplicable)
			}
			if got := tt.p.Item(); got != "MC-111" {
				t.Errorf("item = %q, want MC-111", got)
			}
			got := tt.p.Line()
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("%q does not contain %q", got, w)
				}
			}
			// A problem that counts against the scan has to say out loud that
			// the answer is not known. Silence on that point is what lets a
			// failed measurement be read as a healthy one.
			if tt.wantApplicable {
				said := strings.Contains(got, "unknown") ||
					strings.Contains(got, "no certificate on it was measured") ||
					strings.Contains(got, "nothing was measured")
				if !said {
					t.Errorf("%q does not say the measurement did not happen", got)
				}
			}
		})
	}
}

func TestAlarming(t *testing.T) {
	if (Finding{Step: 0}).Alarming() {
		t.Error("a finding with no step crossed reads as alarming")
	}
	if !(Finding{Step: 30}).Alarming() {
		t.Error("a finding past an alert step does not read as alarming")
	}
}
