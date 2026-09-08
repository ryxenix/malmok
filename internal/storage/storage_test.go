package storage

import (
	"strings"
	"testing"

	"github.com/ryxenix/malmok/api/v1alpha1"
	"github.com/ryxenix/malmok/internal/engine"
	"github.com/ryxenix/malmok/internal/exec"
)

func specWith(driver v1alpha1.StorageDriver) v1alpha1.ClusterSpec {
	return v1alpha1.ClusterSpec{Storage: v1alpha1.StorageSpec{Driver: driver}}
}

func shellSteps(t *testing.T, steps []engine.Step) []*engine.ShellStep {
	t.Helper()
	out := make([]*engine.ShellStep, 0, len(steps))
	for _, s := range steps {
		sh, ok := s.(*engine.ShellStep)
		if !ok {
			t.Fatalf("%T is not a ShellStep", s)
		}
		out = append(out, sh)
	}
	return out
}

// The defect this package exists for: every profile fills storage.driver in,
// `malmok plan` printed it, the audit report told the customer which driver was
// installed, and no phase installed one. The cluster came up with no
// StorageClass and the failure surfaced two layers away.
func TestANamedDriverProducesWork(t *testing.T) {
	for _, driver := range []v1alpha1.StorageDriver{
		v1alpha1.StorageLocalPath, v1alpha1.StorageLonghorn,
		v1alpha1.StorageNFS, v1alpha1.StorageBYOCSI,
	} {
		steps := Steps(&exec.Fake{}, specWith(driver), Options{})
		if len(steps) == 0 {
			t.Errorf("storage.driver %q produces no steps, so the document names a driver and nothing installs or checks one", driver)
		}
	}
}

// Silence is only correct when the document said nothing.
func TestAnUnsetDriverIsLeftAlone(t *testing.T) {
	if steps := Steps(&exec.Fake{}, specWith(""), Options{}); len(steps) != 0 {
		t.Errorf("a document that names no driver got %d steps", len(steps))
	}
}

// Longhorn and NFS are in the schema, in the profiles and in the wizard, and
// this release installs neither. Saying so is the whole point: carrying on
// leaves a cluster with no StorageClass while the report still prints the
// driver that was asked for.
func TestAnUnimplementedDriverStopsTheRun(t *testing.T) {
	for _, driver := range []v1alpha1.StorageDriver{v1alpha1.StorageLonghorn, v1alpha1.StorageNFS} {
		steps := shellSteps(t, Steps(&exec.Fake{}, specWith(driver), Options{}))
		if len(steps) != 1 {
			t.Fatalf("%s: %d steps", driver, len(steps))
		}
		for _, script := range []string{steps[0].Check, steps[0].Do} {
			if !strings.Contains(script, "exit 1") {
				t.Errorf("%s: the step can succeed, so the run continues without a StorageClass", driver)
			}
			if !strings.Contains(script, "not installed by this release") {
				t.Errorf("%s: the step does not say why it stopped:\n%s", driver, script)
			}
		}
	}
}

// byo-csi installs nothing by definition, so Do must not differ from Check --
// anything else is somebody else's driver being changed behind their back.
func TestSiteOwnedCSIIsObservedAndNotInstalled(t *testing.T) {
	steps := shellSteps(t, Steps(&exec.Fake{}, specWith(v1alpha1.StorageBYOCSI), Options{}))
	if len(steps) != 1 {
		t.Fatalf("%d steps", len(steps))
	}
	if steps[0].Check != steps[0].Do {
		t.Errorf("Do differs from Check, so byo-csi changes the cluster:\n%s", steps[0].Do)
	}
	for _, verb := range []string{"apply", "create", "delete", "patch"} {
		if strings.Contains(steps[0].Do, "kubectl "+verb) {
			t.Errorf("byo-csi runs kubectl %s", verb)
		}
	}
}

// An untagged image is :latest, and :latest is a tag no bundle can carry
// because what it refers to changes. Upstream leaves the helper image that
// way; the helper pod runs on every volume create and delete, so an image that
// cannot be pulled is a volume that is never made and never removed.
func TestEveryImageIsPinned(t *testing.T) {
	body := Manifest(specWith(v1alpha1.StorageLocalPath))
	var found int
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "image: ") {
			continue
		}
		found++
		ref := strings.TrimPrefix(line, "image: ")
		name := ref[strings.LastIndex(ref, "/")+1:]
		if !strings.Contains(name, ":") {
			t.Errorf("%s carries no tag, so it means :latest and no bundle can carry it", ref)
		}
	}
	if found != 2 {
		t.Errorf("the manifest names %d images; the carry list says two", found)
	}
}

// A claim that names no class and finds no default waits forever, and every
// component this tool installs names no class.
func TestTheClassIsDefaultUnlessTheDocumentSaysOtherwise(t *testing.T) {
	const annotation = "storageclass.kubernetes.io/is-default-class"

	if !strings.Contains(Manifest(specWith(v1alpha1.StorageLocalPath)), annotation) {
		t.Error("the class is not the default, so the metrics database's claim never binds")
	}

	no := false
	spec := specWith(v1alpha1.StorageLocalPath)
	spec.Storage.DefaultClass = &no
	if strings.Contains(Manifest(spec), annotation) {
		t.Error("defaultClass: false was ignored")
	}
	// And the check must not then demand what the manifest no longer writes.
	steps := shellSteps(t, Steps(&exec.Fake{}, spec, Options{}))
	if strings.Contains(steps[len(steps)-1].Check, annotation) {
		t.Error("the check still requires the default-class annotation the manifest omits")
	}
}

// Observe must not change anything: resume depends on it (§4.2 rule 4).
func TestObserveChangesNothing(t *testing.T) {
	f := &exec.Fake{Default: exec.Result{ExitCode: 1}}
	for _, s := range Steps(f, specWith(v1alpha1.StorageLocalPath), Options{}) {
		if _, err := s.Observe(t.Context()); err != nil {
			t.Fatalf("observe: %v", err)
		}
	}
	for _, cmd := range f.Log {
		for _, verb := range []string{"kubectl apply", "kubectl create", "kubectl delete", "cat >"} {
			if strings.Contains(cmd, verb) {
				t.Errorf("observing ran %q:\n%s", verb, cmd)
			}
		}
	}
}

// The manifest is written to the node and compared against what was meant to
// be written, so it has to render the same every time.
func TestTheManifestRendersTheSameEveryTime(t *testing.T) {
	spec := specWith(v1alpha1.StorageLocalPath)
	first := Manifest(spec)
	for i := 0; i < 20; i++ {
		if got := Manifest(spec); got != first {
			t.Fatalf("run %d rendered a different manifest", i)
		}
	}
}
