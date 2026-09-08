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

	"github.com/ryxenix/malmok/api/v1alpha1"
	"github.com/ryxenix/malmok/internal/engine"
	"github.com/ryxenix/malmok/internal/exec"
	"github.com/ryxenix/malmok/internal/rke2"
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

// NamePrefix is what every generated object is named after.
//
// Short on purpose. Kubernetes allows 63 characters for a name and 63 bytes
// for a label value, and this chart appends things like
// "-kube-controller-manager" and a StatefulSet's revision hash to it. The
// budget is what is left after the longest of those, and the chart's own
// default spends it before it starts.
const NamePrefix = "vm"

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

	// Charts are chart archives the loader read from registry.chartDir, keyed
	// by chart name. Present means the document carried the bytes across and
	// nothing is fetched for that chart.
	Charts map[string][]byte
}

func (o Options) timeout() time.Duration {
	if o.Timeout <= 0 {
		return 10 * time.Minute
	}
	return o.Timeout
}

// chartSource is where the chart comes from, in the shape a HelmChart wants.
// The document's mirror first, then the caller's override, then upstream.
func (o Options) chartSource(spec v1alpha1.ClusterSpec, chart, version string) string {
	// Bytes win over an address. A document that carried the archive across
	// the gap has answered the question more specifically than one that names
	// somewhere to fetch from, and on a closed site the address may be
	// aspirational.
	if archive := o.Charts[chart]; len(archive) > 0 {
		return rke2.ChartContent(archive)
	}
	repo := strings.TrimSpace(spec.Registry.ChartRepo)
	if repo == "" {
		repo = strings.TrimSpace(o.Repo)
	}
	if repo == "" {
		repo = ChartRepo
	}
	return rke2.ChartSource(repo, chart) + "  version: " + rke2.ShellQuoteYAML(version) + "\n"
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
` + o.chartSource(spec, "victoria-metrics-k8s-stack", StackChartVersion) + `
  targetNamespace: ` + yamlString(Namespace) + `
  createNamespace: true
  valuesContent: |-
    # Every name the chart generates starts with this.
    #
    # Without it the chart concatenates the release name and its own name --
    # "victoria-metrics-victoria-metrics-k8s-stack" -- and the Service it
    # creates for the controller-manager scrape target lands at 66 characters
    # against Kubernetes' limit of 63. The install fails, RKE2's helm
    # controller reinstalls on failure, and the result is pods appearing and
    # disappearing for as long as anybody watches. Found on a live cluster.
    fullnameOverride: ` + yamlString(NamePrefix) + `
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
// HelmChart that reports installed while vmsingle cannot bind its volume is
// exactly the state an operator finds weeks later, the first time they go
// looking for a graph.
//
// It reads the Deployment the operator creates rather than a label selector.
// The name follows from the prefix this phase pins -- vmsingle-<prefix> -- so
// it is a fact about the document, while a label is a guess about what the
// operator writes.
func databaseReadyStep(o Options) *engine.ShellStep {
	const kubectl = `export KUBECONFIG=/etc/rancher/rke2/rke2.yaml
export PATH=$PATH:/var/lib/rancher/rke2/bin
`
	deploy := "vmsingle-" + NamePrefix
	ready := fmt.Sprintf(`kubectl -n %s get deploy %s `+
		`-o jsonpath='{.status.readyReplicas}' 2>/dev/null || true`, Namespace, deploy)

	return &engine.ShellStep{
		Name: "metrics-ready",
		Check: kubectl + fmt.Sprintf(`v=$(%s)
case "${v:-0}" in
  0) echo "%s has no ready replica"; exit 1 ;;
  *) echo "the metrics database is serving (%s: $v ready)" ;;
esac`, ready, deploy, deploy),
		Do: kubectl + fmt.Sprintf(`set -e
# A claim with no StorageClass anywhere in the cluster is not slow, it is
# impossible, and it is decidable in one second. Waiting the full timeout for
# it -- twice, because the step retries -- is how a missing storage phase
# arrived as a deadline on the metrics database in five verification cases,
# naming the wrong component in the process.
if [ "$(kubectl get storageclass --no-headers 2>/dev/null | wc -l)" -eq 0 ]; then
  echo "the cluster has no StorageClass, so the metrics database's volume claim can never bind."
  echo "storage.driver in the document decides what provides one; l2-storage installs it."
  kubectl -n %s get pvc 2>&1 | tail -5
  exit 1
fi
started=$(date +%%s)
deadline=$(( started + %d ))
while [ "$(date +%%s)" -lt "$deadline" ]; do
  v=$(%s)
  case "${v:-0}" in 0) ;; *) echo "the metrics database is serving"; exit 0 ;; esac
  # Named so a wait can be told from a hang, which is the difference between
  # an operator leaving it alone and an operator killing it.
  pods=$(kubectl -n %s get pods --no-headers 2>/dev/null | awk '{print $1"="$3}' | tr '\n' ' ')
  echo "waiting $(( $(date +%%s) - started ))s: ${pods:-no pods yet}"
  sleep 10
done
echo "the metrics database did not become Ready within %ds. The namespace holds:"
kubectl -n %s get pods 2>&1 | tail -20
kubectl -n %s get pvc 2>&1 | tail -10
# The install job is where a chart that never rendered says why.
kubectl -n kube-system logs -l job-name=helm-install-victoria-metrics --tail=20 2>&1 | tail -20
exit 1`, Namespace, int(o.timeout().Seconds()), ready, Namespace,
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
