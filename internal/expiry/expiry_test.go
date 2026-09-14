package expiry

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ryxenix/malmok/internal/exec"
)

// base is a fixed clock. Nothing here may depend on when the test is run: a
// certificate that expires "in 30 days" would silently become a different test
// tomorrow.
var base = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

var serial int64

// testCert builds one self-signed certificate and returns it as PEM.
func testCert(t *testing.T, cn string, notAfter time.Time) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	serial++
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             base.AddDate(0, 0, -1),
		NotAfter:              notAfter,
		BasicConstraintsValid: true,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatalf("create certificate %s: %v", cn, err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// output assembles what the script would have printed for the given files.
func output(files ...[2]string) string {
	var b strings.Builder
	for _, f := range files {
		b.WriteString(markerFile + f[0] + "\n")
		b.WriteString(f[1])
		if !strings.HasSuffix(f[1], "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// fakeNode answers the scan command with the given stdout.
func fakeNode(stdout string) *exec.Fake {
	return &exec.Fake{Responses: map[string]exec.Result{
		"server/tls": {Stdout: stdout},
	}}
}

// The command must never be able to read a private key. The TLS directory
// holds the cluster CA keys beside the certificates, and this is the assertion
// that keeps a later convenience -- "read everything, filter afterwards" --
// from quietly removing the only thing preventing it.
func TestScriptReadsCertificatesOnly(t *testing.T) {
	if !strings.Contains(script, `"$d"/*.crt`) {
		t.Errorf("the script no longer globs *.crt:\n%s", script)
	}
	if strings.Contains(script, ".key") {
		t.Errorf("the script names a key file:\n%s", script)
	}
	if strings.Contains(script, `"$d"/*"`) || strings.Contains(script, `"$d"/* `) {
		t.Errorf("the script globs the whole directory:\n%s", script)
	}
}

func TestScanSortsBySoonestExpiry(t *testing.T) {
	late := testCert(t, "serving-kube-apiserver", base.AddDate(1, 0, 0))
	soon := testCert(t, "kube-etcd", base.AddDate(0, 0, 20))
	middle := testCert(t, "client-admin", base.AddDate(0, 6, 0))

	inv := Scan(context.Background(), fakeNode(output(
		[2]string{Dir + "/serving-kube-apiserver.crt", string(late)},
		[2]string{Dir + "/kube-etcd.crt", string(soon)},
		[2]string{Dir + "/client-admin.crt", string(middle)},
	)))

	if len(inv.Problems) != 0 {
		t.Fatalf("unexpected problems: %+v", inv.Problems)
	}
	want := []string{"kube-etcd", "client-admin", "serving-kube-apiserver"}
	if len(inv.Rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(inv.Rows), len(want), inv.Rows)
	}
	for i, cn := range want {
		if !strings.Contains(inv.Rows[i].Subject, cn) {
			t.Errorf("row %d is %q, want the one for %q", i, inv.Rows[i].Subject, cn)
		}
	}

	// The absolute time is the measurement. A row must carry the moment, not a
	// duration computed while scanning.
	if !inv.Rows[0].NotAfter.Equal(base.AddDate(0, 0, 20)) {
		t.Errorf("notAfter is %s, want %s", inv.Rows[0].NotAfter, base.AddDate(0, 0, 20))
	}
}

// One file can hold a chain. Rows are per certificate, so a bundle must not
// collapse into a single row reporting only the first one's date.
func TestScanReadsEveryCertificateInOneFile(t *testing.T) {
	first := testCert(t, "issuing-ca", base.AddDate(5, 0, 0))
	second := testCert(t, "root-ca", base.AddDate(10, 0, 0))

	inv := Scan(context.Background(), fakeNode(output(
		[2]string{Dir + "/server-ca.crt", string(first) + string(second)},
	)))

	if len(inv.Rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(inv.Rows), inv.Rows)
	}
	for _, r := range inv.Rows {
		if r.Path != Dir+"/server-ca.crt" {
			t.Errorf("row came from %q, want the bundle file", r.Path)
		}
	}
}

// An agent has no server TLS directory. That is the correct answer for that
// machine and must not read as a failure to measure a server.
func TestScanReportsMissingDirectoryAsItsOwnReason(t *testing.T) {
	inv := Scan(context.Background(), fakeNode(markerNoDir+"\n"))

	if len(inv.Rows) != 0 {
		t.Fatalf("got rows from a node with no TLS directory: %+v", inv.Rows)
	}
	if len(inv.Problems) != 1 || inv.Problems[0].Reason != ReasonDirMissing {
		t.Fatalf("got %+v, want one %s problem", inv.Problems, ReasonDirMissing)
	}
	if inv.Problems[0].Path != "" {
		t.Errorf("a missing directory was blamed on file %q", inv.Problems[0].Path)
	}
}

// One unreadable file must cost that file and nothing else. A directory where
// the first read fails is exactly where an inventory silently turns short.
func TestScanKeepsGoingAfterAnUnreadableFile(t *testing.T) {
	good := testCert(t, "kube-etcd", base.AddDate(0, 0, 40))

	inv := Scan(context.Background(), fakeNode(output(
		[2]string{Dir + "/locked.crt", markerUnreadable},
		[2]string{Dir + "/kube-etcd.crt", string(good)},
	)))

	if len(inv.Rows) != 1 || !strings.Contains(inv.Rows[0].Subject, "kube-etcd") {
		t.Fatalf("the readable certificate was lost: %+v", inv.Rows)
	}
	if len(inv.Problems) != 1 || inv.Problems[0].Reason != ReasonUnreadable {
		t.Fatalf("got %+v, want one %s problem", inv.Problems, ReasonUnreadable)
	}
	if inv.Problems[0].Path != Dir+"/locked.crt" {
		t.Errorf("the problem names %q, want the locked file", inv.Problems[0].Path)
	}
}

// A .crt that is not a certificate is reported rather than skipped. An
// operator who dropped the wrong export needs to be told the file was not read.
func TestScanReportsAFileThatHoldsNoCertificate(t *testing.T) {
	inv := Scan(context.Background(), fakeNode(output(
		[2]string{Dir + "/notes.crt", "this is not a certificate\n"},
	)))

	if len(inv.Rows) != 0 {
		t.Fatalf("got rows from a file with no certificate: %+v", inv.Rows)
	}
	if len(inv.Problems) != 1 || inv.Problems[0].Reason != ReasonNotPEM {
		t.Fatalf("got %+v, want one %s problem", inv.Problems, ReasonNotPEM)
	}
}

// A node that never answered is not a node with no certificates.
func TestScanSeparatesUnreachableFromEmpty(t *testing.T) {
	f := &exec.Fake{Err: context.DeadlineExceeded}

	inv := Scan(context.Background(), f)

	if len(inv.Rows) != 0 {
		t.Fatalf("got rows from an unreachable node: %+v", inv.Rows)
	}
	if len(inv.Problems) != 1 || inv.Problems[0].Reason != ReasonUnreachable {
		t.Fatalf("got %+v, want one %s problem", inv.Problems, ReasonUnreachable)
	}
}

// A command that ran and failed -- no privilege to list the directory -- is
// unreadable, not an absent directory. The two lead to different actions.
func TestScanReportsAFailedCommandAsUnreadable(t *testing.T) {
	f := &exec.Fake{Responses: map[string]exec.Result{
		"server/tls": {Stderr: "Permission denied", ExitCode: 1},
	}}

	inv := Scan(context.Background(), f)

	if len(inv.Problems) != 1 || inv.Problems[0].Reason != ReasonUnreadable {
		t.Fatalf("got %+v, want one %s problem", inv.Problems, ReasonUnreadable)
	}
	if !strings.Contains(inv.Problems[0].Detail, "Permission denied") {
		t.Errorf("the detail dropped what the node said: %q", inv.Problems[0].Detail)
	}
}

func TestRemaining(t *testing.T) {
	tests := []struct {
		name     string
		notAfter time.Time
		now      time.Time
		want     time.Duration
	}{
		{"a year out", base.AddDate(1, 0, 0), base, 365 * 24 * time.Hour},
		{"expiring today", base, base, 0},
		// Past expiry the answer is negative rather than zero or an error:
		// "expired eleven days ago" is what the operator needs to read.
		{"already expired", base.AddDate(0, 0, -11), base, -11 * 24 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Row{NotAfter: tt.notAfter}.Remaining(tt.now)
			if got != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}
