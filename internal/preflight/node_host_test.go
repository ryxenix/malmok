package preflight

import (
	"strings"
	"testing"
	"time"
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
