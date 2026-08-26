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
	if !strings.Contains(step.Check, "get deploy vmsingle-"+NamePrefix) {
		t.Errorf("the check does not read the vmsingle deployment:\n%s", step.Check)
	}
	// The install job is where a chart that failed to render says why, and it
	// is in kube-system rather than beside the pods that never appeared.
	if !strings.Contains(step.Do, "helm-install-victoria-metrics") {
		t.Errorf("a timeout does not report the install job:\n%s", step.Do)
	}
	if !strings.Contains(step.Do, "waiting $") {
		t.Errorf("the wait says nothing while it waits:\n%s", step.Do)
	}
	if !strings.Contains(step.Do, "get pvc") {
		t.Errorf("a timeout does not report the volume, the usual cause:\n%s", step.Do)
	}
}

// Kubernetes allows 63 characters for a name and 63 bytes for a label value,
// and this chart appends its own suffixes to whatever it is called: a Service
// named "-kube-controller-manager", a StatefulSet label carrying a revision
// hash. Left to the chart's default the prefix is the release name and the
// chart name concatenated, which overruns both -- the install fails, RKE2
// reinstalls on failure, and pods appear and disappear for as long as anybody
// watches. That happened on a live cluster.
//
// The arithmetic rather than the symptom: a name that fits today and not after
// the chart adds one more suffix is the same defect deferred.
func TestGeneratedNamesFitKubernetesLimits(t *testing.T) {
	if !strings.Contains(StackChart(spec(), Options{}), `fullnameOverride: "`+NamePrefix+`"`) {
		t.Fatalf("the chart does not pin a name prefix:\n%s", StackChart(spec(), Options{}))
	}

	// The longest names this chart is known to build from the prefix. The
	// StatefulSet one is the worst: a pod label is the set name plus a ten
	// character revision hash.
	suffixes := []string{
		"-kube-controller-manager",
		"-kube-proxy",
		"-victoria-metrics-operator",
		"-prometheus-node-exporter",
	}
	for _, suffix := range suffixes {
		if n := len(NamePrefix + suffix); n > 63 {
			t.Errorf("%q is %d characters, over the 63 a name may have", NamePrefix+suffix, n)
		}
	}
	// vmalertmanager-<prefix>-<revision hash>, as a pod label value.
	label := "vmalertmanager-" + NamePrefix + "-" + strings.Repeat("d", 10)
	if len(label) > 63 {
		t.Errorf("the alertmanager pod label %q is %d bytes, over 63", label, len(label))
	}

	// Room left for a suffix nobody has added yet. A prefix that exactly fits
	// today is one chart release away from this bug returning.
	if margin := 63 - len(NamePrefix) - len("-victoria-metrics-operator"); margin < 20 {
		t.Errorf("only %d characters are left for a new suffix; the prefix %q is too long",
			margin, NamePrefix)
	}
}
