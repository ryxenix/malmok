// Package observability builds the l2-observability phase: the metrics stack
// the platform runs on behalf of everything in the cluster.
//
// Platform, not application -- the same boundary ADR-006 draws for routes,
// read from the other side. Every workload needs the same answer to "is this
// node out of memory" and "did the apiserver get slower", and a cluster where
// each chart brings its own collector has telemetry rather than
// observability. What an application owns is which of its own series to
// expose; what the platform owns is that something is scraping them.
//
// VictoriaMetrics rather than Prometheus: PromQL and the same scrape
// configuration, an order of magnitude less memory for the same series count,
// and Apache-2.0 for everything installed here. Grafana is the one piece that
// is not -- it is AGPL-3.0 -- so it stays off unless the document asks.
//
// Nothing is applied with kubectl. Everything is written into RKE2's
// auto-deploying manifest directory, so the cluster reconciles it without this
// tool present.
package observability

import (
	"fmt"
	"strings"
	"time"

	"github.com/ryxen/malmok/api/v1alpha1"
	"github.com/ryxen/malmok/internal/engine"
	"github.com/ryxen/malmok/internal/exec"
	"github.com/ryxen/malmok/internal/rke2"
)

// Phase is where these steps are filed.
const Phase = "l2-observability"

// Versions are pinned rather than tracked, for the reason the PKI phase pins
// its own: an airgap bundle carries these exact images, and a floating version
// means the bundle and the manifest disagree about what is installed.
const (
	// StackChartVersion is victoria-metrics-k8s-stack.
	StackChartVersion = "0.91.2"
	// ChartRepo is where the chart comes from.
	ChartRepo = "https://victoriametrics.github.io/helm-charts/"
)

// Namespace is where the stack lives.
const Namespace = "observability"

// Defaults the document inherits when it says nothing.
//
// Short and small on purpose. Metrics land on whatever the default
// StorageClass provides, which on a single node is the filesystem that also
// holds etcd and the image store (PF-401). A full disk there is not a lost
// dashboard, it is a stopped cluster -- so the default is a week of data in
// ten gigabytes, and a site that wants a year says so.
const (
	DefaultRetention   = "7d"
	DefaultStorageSize = "10Gi"
)

const stackFile = rke2.ManifestDir + "/malmok-observability.yaml"

const managedFileHeader = "# Managed by malmok. Changes here are overwritten on the next apply."

// Options carry what the phase needs from the caller.
type Options struct {
	Timeout time.Duration
	// Repo overrides the chart repository, for a site that mirrors it.
	Repo string
}

func (o Options) timeout() time.Duration {
	if o.Timeout <= 0 {
		return 10 * time.Minute
	}
	return o.Timeout
}

func (o Options) repo() string {
	if strings.TrimSpace(o.Repo) != "" {
		return strings.TrimSpace(o.Repo)
	}
	return ChartRepo
}

// Enabled reports whether the document asks for a metrics stack.
func Enabled(spec v1alpha1.ClusterSpec) bool {
	o := spec.Platform.Observability
	return o.Enabled != nil && *o.Enabled
}

// WantsGrafana reports whether the dashboards are asked for.
//
// Off unless said: Grafana OSS is AGPL-3.0. Installing it by default puts a
// licence into a customer's cluster that their legal review may refuse, to
// provide something VictoriaMetrics' own UI already answers.
func WantsGrafana(spec v1alpha1.ClusterSpec) bool {
	g := spec.Platform.Observability.Grafana
	return g != nil && *g
}

// Retention is how long samples are kept.
func Retention(spec v1alpha1.ClusterSpec) string {
	if v := strings.TrimSpace(spec.Platform.Observability.Retention); v != "" {
		return v
	}
	return DefaultRetention
}

// StorageSize is the volume the database asks for.
func StorageSize(spec v1alpha1.ClusterSpec) string {
	if v := strings.TrimSpace(spec.Platform.Observability.StorageSize); v != "" {
		return v
	}
	return DefaultStorageSize
}

// Steps builds the phase.
func Steps(runner exec.Runner, spec v1alpha1.ClusterSpec, o Options) []engine.Step {
	if runner == nil || !Enabled(spec) {
		return nil
	}
	// Only one stack is implemented. A document naming another one describes a
	// cluster this build cannot produce, and installing VictoriaMetrics anyway
	// would answer a different question than the one asked.
	if s := spec.Platform.Observability.Stack; s != "" && s != v1alpha1.ObservabilityVictoriaMetrics {
		return []engine.Step{engine.FailedStep{
			StepID: Phase + "/stack@" + runner.Host(),
			Why: fmt.Sprintf("platform.observability.stack is %q; this build installs %q",
				s, v1alpha1.ObservabilityVictoriaMetrics),
		}}
	}

	host := runner.Host()
	add := func(s *engine.ShellStep) engine.Step {
		s.Phase, s.Runner, s.Host = Phase, runner, host
		return s
	}

	return []engine.Step{
		add(rke2.ManifestStep(Phase, "metrics-stack", stackFile,
			StackChart(spec, o), "helmchart -n kube-system victoria-metrics", o.timeout())),
		add(databaseReadyStep(o)),
	}
}

// StackChart renders the HelmChart that installs the stack.
func StackChart(spec v1alpha1.ClusterSpec, o Options) string {
	var b strings.Builder
	b.WriteString(managedFileHeader + "\n")
	b.WriteString(`apiVersion: helm.cattle.io/v1
kind: HelmChart
metadata:
  name: victoria-metrics
  namespace: kube-system
spec:
  repo: ` + yamlString(o.repo()) + `
  chart: victoria-metrics-k8s-stack
  version: ` + yamlString(StackChartVersion) + `
  targetNamespace: ` + yamlString(Namespace) + `
  createNamespace: true
  valuesContent: |-
    vmsingle:
      spec:
        retentionPeriod: ` + yamlString(Retention(spec)) + `
        storage:
          resources:
            requests:
              storage: ` + yamlString(StorageSize(spec)) + `
`)

	// The chart ships Grafana on. Whether it is installed is a licence
	// decision, so it is stated in both directions rather than left to a
	// default that changes with the chart.
	b.WriteString("    grafana:\n      enabled: " + boolString(WantsGrafana(spec)) + "\n")

	// A private registry has to be told to every chart, or the pull fails with
	// an opaque error naming an upstream host nobody configured.
	if r := strings.TrimSpace(spec.Registry.SystemDefaultRegistry); r != "" &&
		spec.Registry.Mode != v1alpha1.RegistryEmbedded {
		b.WriteString("    global:\n      image:\n        registry: " + yamlString(r) + "\n")
	}
	return b.String()
}

// databaseReadyStep waits for the metrics database to answer.
//
// The observable is the database serving, not the Helm release existing: a
// HelmChart that reports installed while vmsingle crash-loops on a volume it
// cannot bind is exactly the state an operator would otherwise find weeks
// later, the first time they went looking for a graph.
func databaseReadyStep(o Options) *engine.ShellStep {
	const kubectl = `export KUBECONFIG=/etc/rancher/rke2/rke2.yaml
export PATH=$PATH:/var/lib/rancher/rke2/bin
`
	ready := fmt.Sprintf(`kubectl -n %s get pods -l app.kubernetes.io/name=vmsingle `+
		`-o jsonpath='{.items[*].status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true`, Namespace)

	return &engine.ShellStep{
		Name: "metrics-ready",
		Check: kubectl + fmt.Sprintf(`v=$(%s)
case "$v" in
  *True*) echo "the metrics database is serving" ;;
  "") echo "no vmsingle pod exists yet"; exit 1 ;;
  *) echo "vmsingle is present and not Ready: $v"; exit 1 ;;
esac`, ready),
		Do: kubectl + fmt.Sprintf(`set -e
started=$(date +%%s)
deadline=$(( started + %d ))
while [ "$(date +%%s)" -lt "$deadline" ]; do
  v=$(%s)
  case "$v" in *True*) echo "the metrics database is serving"; exit 0 ;; esac
  # Named so a wait can be told from a hang, which is the difference between
  # an operator leaving it alone and an operator killing it.
  pods=$(kubectl -n %s get pods --no-headers 2>/dev/null | awk '{print $1"="$3}' | tr '\n' ' ')
  echo "waiting $(( $(date +%%s) - started ))s: ${pods:-no pods yet}"
  sleep 10
done
echo "the metrics database did not become Ready within %ds. The namespace holds:"
kubectl -n %s get pods 2>&1 | tail -20
kubectl -n %s get pvc 2>&1 | tail -10
exit 1`, int(o.timeout().Seconds()), ready, Namespace,
			int(o.timeout().Seconds()), Namespace, Namespace),
		Satisfied: "%s",
		Missing:   "%s",
	}
}

func yamlString(s string) string { return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"` }

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
