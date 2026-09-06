package preflight

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ryxenix/malmok/api/v1alpha1"
	"github.com/ryxenix/malmok/internal/pki"
)

// The version is read from the package that pins it, so this test cannot go
// stale against a bump it did not notice.
const certManagerVersionForTest = pki.CertManagerVersion

// PF-710 exists because a missing archive is not a failure, it is a silent
// change of plan: the chart falls back to its upstream repository, and on a
// closed node that is a HelmChart job retrying against the internet.
func TestCheckChartDir(t *testing.T) {
	spec := func(dir string) v1alpha1.ClusterSpec {
		s := baseSpec()
		s.PKI.Mode = v1alpha1.PKIPrivateCA
		s.Registry.ChartDir = dir
		on := false
		s.Platform.Observability.Enabled = &on
		g := false
		s.Platform.GitOps.Enabled = &g
		return s
	}

	t.Run("not asked for", func(t *testing.T) {
		if got := CheckChartDir(spec(""), ""); got.Status != StatusSkip {
			t.Fatalf("PF-710 is %s without a chartDir", got.Status)
		}
	})

	t.Run("unreadable, and says what belongs in it", func(t *testing.T) {
		got := CheckChartDir(spec(filepath.Join(t.TempDir(), "nope")), "")
		if got.Code != "CHART_DIR_UNREADABLE" {
			t.Fatalf("PF-710 is %s/%s", got.Status, got.Code)
		}
		if !strings.Contains(got.Detail, "cert-manager-") {
			t.Errorf("the finding does not name the archive to stage: %s", got.Detail)
		}
	})

	t.Run("complete", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "cert-manager-"+certManagerVersionForTest+".tgz")
		if got := CheckChartDir(spec(dir), ""); got.Failed() {
			t.Fatalf("PF-710 is %s/%s: %s", got.Status, got.Code, got.Detail)
		}
	})

	t.Run("missing one", func(t *testing.T) {
		dir := t.TempDir()
		got := CheckChartDir(spec(dir), "")
		if got.Code != "CHART_ARCHIVE_MISSING" {
			t.Fatalf("PF-710 is %s/%s", got.Status, got.Code)
		}
	})

	// The directory looks right and the install would take the pinned version
	// from a repository instead, saying nothing. That is the mistake worth
	// naming separately.
	t.Run("a different version of the same chart", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "cert-manager-v0.0.1.tgz")
		got := CheckChartDir(spec(dir), "")
		if got.Code != "CHART_VERSION_MISMATCH" {
			t.Fatalf("PF-710 is %s/%s: %s", got.Status, got.Code, got.Detail)
		}
		if !strings.Contains(got.Detail, "v0.0.1") {
			t.Errorf("the finding does not name what was found: %s", got.Detail)
		}
	})

	// A relative chartDir is relative to the document, like every other path
	// the document names.
	t.Run("relative to the document", func(t *testing.T) {
		docDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(docDir, "charts"), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(docDir, "charts"), "cert-manager-"+certManagerVersionForTest+".tgz")
		if got := CheckChartDir(spec("charts"), docDir); got.Failed() {
			t.Fatalf("PF-710 is %s/%s: %s", got.Status, got.Code, got.Detail)
		}
	})
}

func write(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("tgz"), 0o644); err != nil {
		t.Fatal(err)
	}
}
