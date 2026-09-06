package images

import (
	"strings"
	"testing"

	"github.com/ryxenix/malmok/internal/catalogue"
)

// The list is generated from the pinned chart versions and committed. A
// version bumped in Go without regenerating it would ship an operator a carry
// list for the release before this one, and they would find out at the
// customer's site. This catches that without a network.
func TestTheListMatchesThePinnedVersions(t *testing.T) {
	got := Versions()
	for _, c := range catalogue.AllCharts() {
		v, ok := got[c.Name]
		if !ok {
			t.Errorf("%s is installed by this release and has no images listed; run scripts/images.sh", c.Name)
			continue
		}
		if v != c.Version {
			t.Errorf("%s images were generated from %s, this release pins %s; run scripts/images.sh",
				c.Name, v, c.Version)
		}
	}
}

func TestEveryEntryIsAReference(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Fatal("no images are embedded")
	}
	for _, i := range all {
		if !strings.Contains(i.Ref, "/") || !strings.Contains(i.Ref, ":") {
			t.Errorf("%s: %q is not a tagged reference", i.Chart, i.Ref)
		}
	}
}

// Two charts can pull the same image and an operator wants it once.
func TestForDeduplicates(t *testing.T) {
	seen := map[string]bool{}
	for _, ref := range For(nil) {
		if seen[ref] {
			t.Errorf("%s is listed twice", ref)
		}
		seen[ref] = true
	}
}
