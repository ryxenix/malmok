package observability

import (
	"strings"
	"testing"

	"github.com/ryxenix/malmok/api/v1alpha1"
)

// The operator fills these in at run time and the chart never states them, so
// nothing that reads charts can notice when they move. A stack chart bumped
// without re-reading them leaves the carry list naming versions the cluster no
// longer runs -- which on a closed site is a pod that cannot start and a list
// that says it should.
//
// There is no way to check this offline. What there is, is a way to stop the
// two drifting silently: bump the chart and this fails until somebody has
// looked at a cluster running the new one.
func TestTheOperatorImagesWereReadFromThisChart(t *testing.T) {
	if ImagesReadFromChart != StackChartVersion {
		t.Fatalf("the operator images were read under chart %s and the chart is now %s.\n"+
			"Install the stack and read them back:\n"+
			"  kubectl get pods -n observability -o jsonpath='{range .items[*]}{range .spec.containers[*]}{.image}{\"\\n\"}{end}{end}' | sort -u\n"+
			"then update internal/observability/images.go and this constant.",
			ImagesReadFromChart, StackChartVersion)
	}
}

// A carry list that names a tag nobody can pull is the failure this file
// exists to prevent, so every entry has to look like an image reference.
func TestEveryOperatorImageIsPinned(t *testing.T) {
	for _, ref := range AllImages() {
		name := ref[strings.LastIndex(ref, "/")+1:]
		if !strings.Contains(name, ":") {
			t.Errorf("%s carries no tag, so it means :latest and no bundle can carry it", ref)
		}
	}
}

// A document that installs no metrics stack should not be told to carry its
// images: over-listing costs bytes, and these are not small.
func TestImagesFollowTheDocument(t *testing.T) {
	off := v1alpha1.ClusterSpec{}
	off.Platform.Observability.Enabled = new(bool)
	if got := Images(off); len(got) != 0 {
		t.Errorf("observability is off and %d images are listed", len(got))
	}
}
