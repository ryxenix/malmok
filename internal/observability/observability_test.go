package observability

import (
	"strings"
	"testing"

	"github.com/ryxen/malmok/api/v1alpha1"
	"github.com/ryxen/malmok/internal/engine"
	"github.com/ryxen/malmok/internal/exec"
)

func on() *bool  { v := true; return &v }
func off() *bool { v := false; return &v }

func spec() v1alpha1.ClusterSpec {
	s := v1alpha1.ClusterSpec{}
	s.Platform.Observability.Enabled = on()
	return s
}

// Nothing is installed unless the document asks. A metrics stack takes a
// volume on whatever the default StorageClass provides, which on a single node
// is the filesystem holding etcd and the images (PF-401) -- so it is a phase
// that has to be asked for, and a document that says no gets no phase at all
// rather than a phase that skips.
func TestTheStackIsOnlyBuiltWhenAskedFor(t *testing.T) {
	if steps := Steps(&exec.Fake{}, v1alpha1.ClusterSpec{}, Options{}); len(steps) != 0 {
		t.Errorf("a document that says nothing builds %d steps", len(steps))
	}
	s := v1alpha1.ClusterSpec{}
	s.Platform.Observability.Enabled = off()
	if steps := Steps(&exec.Fake{}, s, Options{}); len(steps) != 0 {
		t.Errorf("a document that says no builds %d steps", len(steps))
	}
	if steps := Steps(&exec.Fake{}, spec(), Options{}); len(steps) == 0 {
		t.Error("a document that asks for metrics builds nothing")
	}
}

// The defaults are short and small deliberately, and a document that says
// otherwise is obeyed.
func TestRetentionAndSizeComeFromTheDocument(t *testing.T) {
	chart := StackChart(spec(), Options{})
	for _, want := range []string{`retentionPeriod: "7d"`, `storage: "10Gi"`} {
		if !strings.Contains(chart, want) {
			t.Errorf("the default chart does not carry %q:\n%s", want, chart)
		}
	}

	s := spec()
	s.Platform.Observability.Retention = "90d"
	s.Platform.Observability.StorageSize = "200Gi"
	chart = StackChart(s, Options{})
	for _, want := range []string{`retentionPeriod: "90d"`, `storage: "200Gi"`} {
		if !strings.Contains(chart, want) {
			t.Errorf("the chart ignores what the document asked for: %q\n%s", want, chart)
		}
	}
}

// Grafana is AGPL-3.0. The chart ships it on, so this states the answer in
// both directions rather than leaving it to a default that moves with the
// chart -- a licence arriving in a customer's cluster because an upstream
// value flipped is not a decision anybody made.
func TestGrafanaIsOffUnlessAskedFor(t *testing.T) {
	if !strings.Contains(StackChart(spec(), Options{}), "grafana:\n      enabled: false") {
		t.Errorf("grafana is not disabled by default:\n%s", StackChart(spec(), Options{}))
	}
	s := spec()
	s.Platform.Observability.Grafana = on()
	if !strings.Contains(StackChart(s, Options{}), "grafana:\n      enabled: true") {
		t.Error("a document that asks for grafana does not get it")
	}
}

// A stack this build cannot install is a failure with a reason, not a silent
// substitution: installing VictoriaMetrics for a document that asked for
// something else answers a question nobody put.
func TestAnUnknownStackFailsRatherThanSubstitutes(t *testing.T) {
	s := spec()
	s.Platform.Observability.Stack = "prometheus"
	steps := Steps(&exec.Fake{}, s, Options{})
	if len(steps) != 1 {
		t.Fatalf("an unknown stack produced %d steps", len(steps))
	}
	if _, ok := steps[0].(engine.FailedStep); !ok {
		t.Errorf("an unknown stack produced %T rather than a failure", steps[0])
	}
}

// The observable is the database serving, not the release existing. A
// HelmChart that reports installed while vmsingle crash-loops on a volume it
// cannot bind is the state an operator finds weeks later, the first time they
// go looking for a graph.
func TestReadinessMeasuresTheDatabaseNotTheRelease(t *testing.T) {
	step := databaseReadyStep(Options{})
	if !strings.Contains(step.Check, "app.kubernetes.io/name=vmsingle") {
		t.Errorf("the check does not look at vmsingle:\n%s", step.Check)
	}
	if !strings.Contains(step.Do, "waiting $") {
		t.Errorf("the wait says nothing while it waits:\n%s", step.Do)
	}
	if !strings.Contains(step.Do, "get pvc") {
		t.Errorf("a timeout does not report the volume, the usual cause:\n%s", step.Do)
	}
}
