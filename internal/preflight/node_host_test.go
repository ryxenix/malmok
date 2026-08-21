package preflight

import (
	"strings"
	"testing"
	"time"

	"platform.ryxen.dev/malmok/internal/codes"
)

// A finding an operator cannot act on sends them to read about etcd instead
// of to the one thing that fixes this. Two nodes following different time
// servers drift apart while each reports itself synchronised -- which is what
// a lab pair did at 1.1s -- so the remedy names the source, not the symptom.
func TestClockSkewSaysHowToFixIt(t *testing.T) {
	got := CheckClockSkew(map[string]Offset{
		"10.0.0.11": {Delta: 0, Uncertainty: 30 * time.Millisecond},
		"10.0.0.12": {Delta: 1100 * time.Millisecond, Uncertainty: 30 * time.Millisecond},
	}, time.Second)

	if got.Code != "CLOCK_SKEW" {
		t.Fatalf("PF-502 is %s/%s: %s", got.Status, got.Code, got.Detail)
	}
	for _, want := range []string{"same time source", "timesyncd", "chrony"} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("the finding does not mention %q:\n%s", want, got.Detail)
		}
	}
}

// One sample cannot tell a clock that is converging from one that is wrong.
// Nodes come up from a reboot seconds apart, each reporting itself
// synchronised while its daemon steps, and the gap closes on its own -- which
// a single measurement calls drift and blocks the build for.
func TestClockSkewDistinguishesConvergingFromDrifting(t *testing.T) {
	pair := func(a, b time.Duration) map[string]Offset {
		return map[string]Offset{
			"10.0.0.11": {Delta: a, Uncertainty: 30 * time.Millisecond},
			"10.0.0.12": {Delta: b, Uncertainty: 30 * time.Millisecond},
		}
	}

	closing := CheckClockSkewTrend(pair(0, 1800*time.Millisecond), pair(0, 1200*time.Millisecond), time.Second)
	if closing.Code != "CLOCK_CONVERGING" {
		t.Errorf("a closing gap is %s/%s: %s", closing.Status, closing.Code, closing.Detail)
	}
	if closing.Severity == codes.SeverityBlock {
		t.Error("a closing gap blocks the build")
	}

	stuck := CheckClockSkewTrend(pair(0, 1800*time.Millisecond), pair(0, 1790*time.Millisecond), time.Second)
	if stuck.Code != "CLOCK_SKEW" {
		t.Errorf("a gap that is not closing is %s/%s", stuck.Status, stuck.Code)
	}

	// And a gap that closed all the way is simply a pass.
	fixed := CheckClockSkewTrend(pair(0, 1800*time.Millisecond), pair(0, 40*time.Millisecond), time.Second)
	if fixed.Failed() {
		t.Errorf("a settled clock still fails: %s", fixed.Detail)
	}
}
