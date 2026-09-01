package preflight

import (
	"strings"
	"testing"
	"time"

	"github.com/ryxen/malmok/internal/codes"
	"github.com/ryxen/malmok/internal/exec"
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

// An air-gapped install reads RKE2's artifacts from a directory on the node.
// The installer's answer to a directory that is absent or half-staged is a
// failed download, which on a machine with no route is the least useful thing
// it could say -- so the files are measured here, before anything is
// installed.
func TestCheckArtifactPath(t *testing.T) {
	const dir = "/srv/rke2"

	// Byte for byte what the probe runs. A fake keyed on anything else
	// answers every case with silence, which reads as an empty directory.
	cmd := `p='` + dir + `'
[ -d "$p" ] || { echo "MISSING"; exit 0; }
ls -1 "$p" 2>/dev/null | tr '\n' ' '`

	const full = "install.sh rke2-images.linux-amd64.tar.zst rke2.linux-amd64.tar.gz sha256sum-amd64.txt"

	tests := []struct {
		name    string
		path    string
		out     string
		failing bool
		reason  string
	}{
		{name: "not asked for", path: ""},
		{name: "complete", path: dir, out: full},
		{name: "absent", path: dir, out: "MISSING", failing: true, reason: "ARTIFACT_PATH_MISSING"},
		{name: "empty", path: dir, out: "", failing: true, reason: "ARTIFACT_PATH_EMPTY"},
		// Binaries without the images archive still install; the node then
		// pulls, which is the thing an air gap cannot do. Said, not blocked:
		// the same directory is legitimate where there is a route.
		{name: "binaries only", path: dir,
			out: "install.sh rke2.linux-amd64.tar.gz sha256sum-amd64.txt"},
		{name: "no installer", path: dir,
			out:     "rke2.linux-amd64.tar.gz sha256sum-amd64.txt",
			failing: true, reason: "ARTIFACT_PATH_INCOMPLETE"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n, _ := ubuntuProber(t, map[string]exec.Result{cmd: {Stdout: tc.out + "\n"}})
			got := n.CheckArtifactPath(t.Context(), tc.path)
			if got.ID != "PF-709" {
				t.Fatalf("code is %s, want PF-709", got.ID)
			}
			if got.Failed() != tc.failing {
				t.Errorf("failed=%v, want %v: %s %s", got.Failed(), tc.failing, got.Code, got.Detail)
			}
			if tc.reason != "" && got.Code != tc.reason {
				t.Errorf("reason is %q, want %q: %s", got.Code, tc.reason, got.Detail)
			}
		})
	}
}
