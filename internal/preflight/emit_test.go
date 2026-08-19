package preflight

import (
	"bytes"
	"strings"
	"testing"

	"platform.ryxen.dev/malmok/internal/codes"
	"platform.ryxen.dev/malmok/internal/event"
)

// Probe events have to satisfy the same schema as everything else in the file.
// A line the reader rejects is a line an audit report loses, and the run is
// over by the time anybody notices.
func TestEmittedProbesSatisfyTheEventSchema(t *testing.T) {
	var buf bytes.Buffer
	w := event.NewWriter(&buf, "01JBQ8F2K3M5N7P9R1S3T5V7W9")

	e := Emitter{Writer: w}
	e.ForNode("10.10.0.11")(ProbeResult{
		ID: "PF-204", Status: StatusFail, Severity: codes.SeverityBlock,
		Code: "EBPF_LOAD_DENIED", Detail: "the kernel accepted no eBPF program",
		Evidence: "bpftool: permission denied",
	})
	e.Emit(ProbeResult{ID: "PF-611", Status: StatusPass, Severity: codes.SeverityInfo,
		Detail: "the pod network collides with nothing"})

	sc := event.NewScanner(bytes.NewReader(buf.Bytes()))
	var seen int
	for sc.Scan() {
		got := sc.Event()
		if err := got.Validate(); err != nil {
			t.Errorf("event %d is invalid: %v", got.Seq, err)
		}
		if got.Kind != event.KindProbe {
			t.Errorf("event %d is kind %q, want probe", got.Seq, got.Kind)
		}
		seen++
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("the file could not be read back: %v", err)
	}
	if seen != 2 {
		t.Fatalf("read %d events, wrote 2", seen)
	}
}

// A blocking failure is "blocked", not "failed". docs/11-execute.md separates
// the two by whether a retry could help, and no retry fixes a kernel that
// refuses to load an eBPF program.
func TestBlockingProbeIsBlockedNotFailed(t *testing.T) {
	tests := []struct {
		name string
		in   ProbeResult
		want event.Status
	}{
		{
			name: "blocking failure",
			in:   ProbeResult{ID: "PF-204", Status: StatusFail, Severity: codes.SeverityBlock},
			want: event.StatusBlocked,
		},
		{
			// A warning did not pass, and collapsing it into "ok" would leave a
			// renderer unable to tell it from a clean check.
			name: "warning",
			in:   ProbeResult{ID: "PF-401", Status: StatusFail, Severity: codes.SeverityWarn},
			want: event.StatusFailed,
		},
		{
			name: "skip",
			in:   ProbeResult{ID: "PF-404", Status: StatusSkip, Severity: codes.SeverityInfo},
			want: event.StatusSkipped,
		},
		{
			name: "pass",
			in:   ProbeResult{ID: "PF-101", Status: StatusPass, Severity: codes.SeverityInfo},
			want: event.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := eventStatus(tc.in); got != tc.want {
				t.Errorf("status is %q, want %q", got, tc.want)
			}
		})
	}
}

// The probe messages are written long on purpose -- a failure has to say what
// it costs -- and a newline slipping into one would break every reader that
// treats the file as JSONL.
func TestDetailIsCollapsedToOneLine(t *testing.T) {
	n, _ := ubuntuProber(t, nil)
	cap := n.Probe(t.Context())

	for id, p := range cap.Probes {
		if strings.ContainsAny(oneLine(p.Detail), "\r\n") {
			t.Errorf("%s still holds a newline after collapsing", id)
		}
	}
	if got := oneLine("a\nb\r\nc   d"); got != "a b c d" {
		t.Errorf("oneLine = %q", got)
	}
}

// Concurrent nodes must not have their findings attributed to each other.
func TestForNodeStampsTheHost(t *testing.T) {
	var buf bytes.Buffer
	e := Emitter{Writer: event.NewWriter(&buf, "01JBQ8F2K3M5N7P9R1S3T5V7W9")}

	e.ForNode("10.10.0.11")(ProbeResult{ID: "PF-101", Status: StatusPass, Detail: "ubuntu"})
	e.ForNode("10.10.0.12")(ProbeResult{ID: "PF-101", Status: StatusPass, Detail: "ubuntu"})

	sc := event.NewScanner(bytes.NewReader(buf.Bytes()))
	var hosts []string
	for sc.Scan() {
		hosts = append(hosts, sc.Event().Node)
	}
	if len(hosts) != 2 || hosts[0] != "10.10.0.11" || hosts[1] != "10.10.0.12" {
		t.Errorf("nodes are %v", hosts)
	}
}
