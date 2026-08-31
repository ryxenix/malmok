package rke2

import (
	"fmt"
	"strings"
	"time"

	"github.com/ryxen/malmok/internal/engine"
)

// Writing into RKE2's manifest directory is how every L2 phase installs
// something, and there is one subtlety worth owning in one place.
//
// RKE2's deploy controller is content-hash driven: it records what it applied
// and skips a file whose contents it has seen. So rewriting an identical
// manifest does not restore an object somebody deleted by hand -- the record
// still says "applied" and nothing happens. A step that wrote the file, saw the
// file match, and then waited for the object would wait forever, which is
// exactly what happened to cert-manager on a live cluster.
//
// So the file is written for durability, and applied once directly so the
// object exists now. The file is what the cluster reconciles from on every
// restart; the apply is what makes this run true.

// ManifestStep writes one manifest into the directory RKE2 watches.
//
// resource is what to look for once it has been applied, in kubectl's own
// vocabulary. Empty means the manifest's effect is checked by a later step --
// but prefer naming it: a file on disk the cluster never accepted is the
// failure this exists to catch.
func ManifestStep(phase, name, path, body, resource string, timeout time.Duration) *engine.ShellStep {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}

	present, wait := "", ""
	if resource != "" {
		present = fmt.Sprintf(`
kubectl get %s >/dev/null 2>&1 || { echo "%s is written and the cluster does not have %s"; exit 1; }`,
			resource, path, resource)
		wait = fmt.Sprintf(`
deadline=$(( $(date +%%s) + %d ))
while [ "$(date +%%s)" -lt "$deadline" ]; do
  kubectl get %s >/dev/null 2>&1 && exit 0
  sleep 5
done
echo "the cluster never created %s from %s"
kubectl get %s 2>&1 | tail -5
exit 1`, int(timeout.Seconds()), resource, resource, path, resource)
	}

	return &engine.ShellStep{
		Phase: phase,
		Name:  name,
		Check: Kubectl + fmt.Sprintf(`[ -f %s ] || { echo "%s does not exist"; exit 1; }
printf '%%s' %s | cmp -s - %s || { echo "%s differs from the document"; exit 1; }%s
echo "%s matches the document"`, path, path, ShellQuote(body), path, path, present, path),

		Do: Kubectl + fmt.Sprintf(`set -e
install -d -m 0755 %s
printf '%%s' %s > %s
# Applied as well as written. The directory is what the cluster reconciles from
# on every restart, and this is what makes the object exist now: RKE2 skips a
# file whose contents it has already recorded, so rewriting an identical
# manifest would not restore something deleted by hand.
kubectl apply -f %s >/dev/null%s`,
			ManifestDir, ShellQuote(body), path, path, wait),

		Satisfied: "%s",
		Missing:   "%s",
		DoTimeout: timeout + time.Minute,
		// Apply already waits on its own deadline; retrying it just waits again.
		Attempts: 1,
	}
}

// Kubectl is the prelude every cluster-scoped step needs: RKE2 keeps its
// binaries outside PATH and its kubeconfig outside the default location.
var Kubectl = fmt.Sprintf("export PATH=$PATH:%s\nexport KUBECONFIG=%s\n", BinDir, Kubeconfig)

// ShellQuote wraps a value for a POSIX shell.
func ShellQuote(s string) string { return shellQuote(s) }

// AcceptsStep waits until the API server accepts an object.
//
// The pattern it replaces appeared three times and was got wrong twice. A
// controller installed by a chart is not usable when its pods report ready: its
// CRDs may not be registered, and a validating webhook's CA bundle is injected
// after the pods start. Between those moments everything looks healthy and the
// first object anybody creates fails with "no matches for kind" or "x509:
// certificate signed by unknown authority".
//
// The observable is a server-side dry run of the very object that is about to
// be created. It goes through the API server, the CRD registration and any
// admission webhook -- the whole path that has to work -- and changes nothing,
// because the API server discards the result. cert-manager's own `cmctl check
// api` does the same thing.
func AcceptsStep(phase, name, manifest, what string, timeout time.Duration) *engine.ShellStep {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	probe := fmt.Sprintf(`printf '%%s' %s | kubectl apply --dry-run=server -f - 2>&1`, ShellQuote(manifest))

	// The transient failures, which are worth waiting through, against a
	// refusal, which is not going to improve.
	transient := `*"no matches for kind"*|*"could not find the requested resource"*|` +
		`*"failed calling webhook"*|*"unknown authority"*|*"connection refused"*|` +
		`*"no endpoints available"*`

	return &engine.ShellStep{
		Phase: phase,
		Name:  name,
		Check: Kubectl + fmt.Sprintf(`out=$(%s)
case "$out" in
  %s) echo "%s is installed and not serving yet"; exit 1 ;;
  *error*|*Error*) echo "%s refused it: $out"; exit 1 ;;
esac
echo "%s accepts what the document asks for"`, probe, transient, what, what, what),

		Do: Kubectl + fmt.Sprintf(`deadline=$(( $(date +%%s) + %d ))
last=
while [ "$(date +%%s)" -lt "$deadline" ]; do
  last=$(%s)
  case "$last" in
    %s|*error*|*Error*) ;;
    *) exit 0 ;;
  esac
  sleep 5
done
echo "%s never accepted an object within %ds. The last answer was: $last"
exit 1`, int(timeout.Seconds()), probe, transient, what, int(timeout.Seconds())),

		Satisfied: "%s",
		Missing:   "%s",
		DoTimeout: timeout + time.Minute,
		// Apply is already a bounded wait; retrying it just waits again.
		Attempts: 1,
	}
}

// ChartSource renders the two fields of a HelmChart that say where a chart
// comes from, for whichever kind of mirror the document names.
//
// A Helm repository and an OCI registry are not the same shape to the helm
// controller: one is `repo:` plus a bare chart name, the other is a `chart:`
// that carries the whole reference and no repo at all. An air-gapped site has
// whichever its registry provides -- ADR-007 puts charts in the OCI registry
// beside the images precisely so no second server is needed -- so both are
// understood rather than one being the supported way.
//
// repo empty means the chart's own upstream, which the caller passes as the
// default.
func ChartSource(repo, chart string) string {
	repo = strings.TrimSpace(repo)
	if strings.HasPrefix(repo, "oci://") {
		// One field, and the chart name appended to the reference. A repo
		// field beside an OCI chart is rejected by the controller.
		return "  chart: " + ShellQuoteYAML(strings.TrimSuffix(repo, "/")+"/"+chart) + "\n"
	}
	return "  repo: " + ShellQuoteYAML(repo) + "\n  chart: " + chart + "\n"
}

// ShellQuoteYAML wraps a value so YAML reads it as a string.
func ShellQuoteYAML(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}
