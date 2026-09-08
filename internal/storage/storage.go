// Package storage builds the l2-storage phase.
//
// It exists because `storage.driver` was a field nothing read. The document
// named a driver, `malmok plan` printed it, the audit report told the customer
// "Storage | local-path", and no phase ever installed anything -- so the
// cluster came up with no StorageClass at all. Nothing said so. It surfaced
// two layers away, as the metrics database sitting Pending on a
// PersistentVolumeClaim that could never bind, which the observability phase
// then waited fifteen minutes for, twice, before failing with a deadline. Five
// of the ten verification cases died there.
//
// What this installs is Rancher's local-path-provisioner, rendered here rather
// than fetched. A chart would be one more thing to reach for at install time,
// and the sites this tool is for cannot reach for anything: the manifest is
// pinned to a release, carried in the binary, and the only thing that has to
// arrive separately is the two images.
//
// The other drivers are not implemented. They say so, out loud, in the shape
// of a step that fails and names what to do about it -- because the thing this
// package is fixing is a field that quietly did nothing.
package storage

import (
	"fmt"
	"time"

	"github.com/ryxenix/malmok/api/v1alpha1"
	"github.com/ryxenix/malmok/internal/engine"
	"github.com/ryxenix/malmok/internal/exec"
	"github.com/ryxenix/malmok/internal/rke2"
)

// Phase is where these steps are filed.
//
// l2 and not l1: it is cluster state, written once from a node that is already
// in the cluster, like the dataplane and the gateway.
const Phase = "l2-storage"

// Namespace is where the provisioner runs. Upstream's, unchanged: a site that
// has seen local-path-provisioner before knows where to look.
const Namespace = "local-path-storage"

// ClassName is the StorageClass this phase creates.
const ClassName = "local-path"

// ProvisionerVersion is the release rendered below.
//
// Pinned rather than latest, for the same reason the payload in a release is:
// the manifest and the image have to be the same pair every time, and an
// air-gapped site carries the image by name before the cluster exists.
const ProvisionerVersion = "v0.0.37"

// Images are what an air-gapped site has to carry for this phase.
//
// HelperImage is pinned where upstream leaves it floating. Upstream's default
// is `docker.io/library/busybox` with no tag, which resolves to :latest -- a
// tag no bundle can carry, because what it means changes. The helper pod runs
// on every volume create and delete, so an image that cannot be pulled is a
// volume that is never made and never cleaned up.
const (
	ProvisionerImage = "docker.io/rancher/local-path-provisioner:" + ProvisionerVersion
	HelperImage      = "docker.io/library/busybox:1.37.0"
)

// DataPath is where volumes are carved out on each node.
//
// Upstream's default. Worth knowing that it is on the root filesystem unless
// the site mounted something there: PF-401 already says what a full root
// filesystem does to a node, and a 10Gi metrics database is the first thing
// this phase gives somewhere to grow.
const DataPath = "/opt/local-path-provisioner"

const manifestFile = rke2.ManifestDir + "/malmok-storage.yaml"

const managedFileHeader = "# Managed by malmok. Changes here are overwritten on the next apply."

var kubectl = fmt.Sprintf("export PATH=$PATH:%s\nexport KUBECONFIG=%s\n", rke2.BinDir, rke2.Kubeconfig)

// Options are what the steps need beyond the document.
type Options struct {
	Timeout time.Duration
}

func (o Options) timeout() time.Duration {
	if o.Timeout <= 0 {
		return 10 * time.Minute
	}
	return o.Timeout
}

// IsDefaultClass reports whether the class this phase creates is the one a
// claim gets when it names none.
//
// True unless the document says otherwise. A claim that names no class and
// finds no default stays Pending forever, and the components this tool
// installs -- the metrics database among them -- name no class.
func IsDefaultClass(spec v1alpha1.ClusterSpec) bool {
	return spec.Storage.DefaultClass == nil || *spec.Storage.DefaultClass
}

// Images are what this phase pulls, for the carry list.
//
// They do not come from a chart, so nothing that reads charts can find them:
// the manifest is rendered here and the images are named here. A carry list
// that misses them is discovered on a closed site as a provisioner that never
// starts and volumes that are never made.
func Images(spec v1alpha1.ClusterSpec) []string {
	if spec.Storage.Driver != v1alpha1.StorageLocalPath {
		return nil
	}
	return AllImages()
}

// AllImages is every image this package can pull, whatever the document says.
func AllImages() []string { return []string{ProvisionerImage, HelperImage} }

// Steps build the phase.
//
// A document that names no driver gets no steps, which is what happened before
// this package existed. That is deliberate for the empty case and only for the
// empty case: every profile fills the field in, so a document arriving here
// with nothing set said nothing on purpose.
func Steps(runner exec.Runner, spec v1alpha1.ClusterSpec, o Options) []engine.Step {
	host := runner.Host()
	add := func(s *engine.ShellStep) engine.Step {
		s.Phase, s.Runner, s.Host = Phase, runner, host
		return s
	}

	switch spec.Storage.Driver {
	case "":
		return nil

	case v1alpha1.StorageLocalPath:
		return []engine.Step{
			add(rke2.ManifestStep(Phase, "provisioner", manifestFile, Manifest(spec),
				"deployment -n "+Namespace+" local-path-provisioner", o.timeout())),
			add(readyStep(spec, o)),
		}

	case v1alpha1.StorageBYOCSI:
		// Nothing to install: the site brought its own. What is worth doing is
		// saying whether it actually arrived, because the failure otherwise
		// lands on whichever workload asks for a volume first and reads as
		// that workload being broken.
		return []engine.Step{add(byoStep())}

	default:
		// Not implemented, and it says so here rather than by leaving the
		// cluster without a StorageClass and letting a claim wait forever.
		// That is the exact failure this package was written to remove.
		return []engine.Step{add(unimplementedStep(spec.Storage.Driver))}
	}
}

// readyStep waits for the provisioner to be able to serve a claim.
//
// Two things, because either alone is misleading: the Deployment being
// available says the controller is running, and the StorageClass existing says
// a claim naming it will be picked up. A cluster with one and not the other is
// a cluster where volumes hang.
func readyStep(spec v1alpha1.ClusterSpec, o Options) *engine.ShellStep {
	class := fmt.Sprintf(`kubectl get storageclass %s -o jsonpath='{.metadata.name}' 2>/dev/null || true`, ClassName)
	ready := fmt.Sprintf(`kubectl -n %s get deploy local-path-provisioner `+
		`-o jsonpath='{.status.readyReplicas}' 2>/dev/null || true`, Namespace)

	// The default-class annotation is the difference between a claim that
	// names no class binding and one that waits forever, so it is checked and
	// not assumed.
	defaultCheck := ""
	if IsDefaultClass(spec) {
		defaultCheck = fmt.Sprintf(`
d=$(kubectl get storageclass %s -o jsonpath='{.metadata.annotations.storageclass\.kubernetes\.io/is-default-class}' 2>/dev/null || true)
[ "$d" = "true" ] || { echo "%s exists and is not the default class, so a claim that names no class still waits"; exit 1; }`,
			ClassName, ClassName)
	}

	return &engine.ShellStep{
		Name: "storage-ready",
		Check: kubectl + fmt.Sprintf(`c=$(%s)
[ -n "$c" ] || { echo "there is no %s StorageClass, so a claim that names one waits forever"; exit 1; }
v=$(%s)
case "${v:-0}" in
  0) echo "the local-path provisioner has no ready replica"; exit 1 ;;
esac%s
echo "%s is serving claims ($v ready)"`, class, ClassName, ready, defaultCheck, ClassName),
		Do: kubectl + fmt.Sprintf(`set -e
started=$(date +%%s)
deadline=$(( started + %d ))
while [ "$(date +%%s)" -lt "$deadline" ]; do
  v=$(%s)
  case "${v:-0}" in 0) ;; *) echo "the local-path provisioner is serving claims"; exit 0 ;; esac
  # Named so a wait can be told from a hang, which is the difference between
  # an operator leaving it alone and an operator killing it.
  pods=$(kubectl -n %s get pods --no-headers 2>/dev/null | awk '{print $1"="$3}' | tr '\n' ' ')
  echo "waiting $(( $(date +%%s) - started ))s: ${pods:-no pods yet}"
  sleep 5
done
echo "the local-path provisioner did not become Ready within %ds. The namespace holds:"
kubectl -n %s get pods 2>&1 | tail -20
kubectl -n %s describe deploy local-path-provisioner 2>&1 | tail -20
exit 1`, int(o.timeout().Seconds()), ready, Namespace,
			int(o.timeout().Seconds()), Namespace, Namespace),
		Satisfied: "%s",
		Missing:   "%s",
	}
}

// byoStep observes that the site's own CSI produced a StorageClass.
//
// Observe-only: there is nothing here for Do to do that would not be somebody
// else's driver installed behind their back. A failure names the claim that
// would hang rather than leaving it to be discovered later.
func byoStep() *engine.ShellStep {
	check := kubectl + `n=$(kubectl get storageclass --no-headers 2>/dev/null | wc -l)
[ "${n:-0}" -gt 0 ] || { echo "storage.driver is byo-csi and the cluster has no StorageClass, so every claim will wait forever"; exit 1; }
kubectl get storageclass --no-headers 2>/dev/null | awk '{print $1}' | tr '\n' ' ' | sed 's/^/the site CSI provides: /'
echo`
	return &engine.ShellStep{
		Name:      "site-csi",
		Check:     check,
		Do:        check,
		Satisfied: "%s",
		Missing:   "%s",
	}
}

// unimplementedStep fails and says what to do.
//
// A driver this release does not install has to stop the run here. Carrying on
// leaves a cluster with no StorageClass, and the report would still print the
// driver the document asked for -- which is the thing this package exists to
// stop being true.
func unimplementedStep(driver v1alpha1.StorageDriver) *engine.ShellStep {
	msg := fmt.Sprintf("storage.driver: %s is not installed by this release. "+
		"The cluster would come up with no StorageClass and anything asking for a volume "+
		"would wait forever. Set storage.driver to local-path, or to byo-csi and install "+
		"%s yourself before running this.", driver, driver)
	return &engine.ShellStep{
		Name:      "driver-" + string(driver),
		Check:     fmt.Sprintf(`echo %s; exit 1`, rke2.ShellQuote(msg)),
		Do:        fmt.Sprintf(`echo %s; exit 1`, rke2.ShellQuote(msg)),
		Satisfied: "%s",
		Missing:   "%s",
	}
}
