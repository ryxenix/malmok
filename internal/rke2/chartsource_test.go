package rke2

import (
	"strings"
	"testing"
)

// A Helm repository and an OCI registry are not the same shape to the helm
// controller: one is a repo plus a bare chart name, the other is a single
// reference and no repo at all. A repo field beside an OCI chart is rejected,
// so the two are rendered differently rather than one being bent into the
// other's form.
func TestChartSourceRendersBothKindsOfMirror(t *testing.T) {
	https := ChartSource("https://charts.acme.internal", "cert-manager")
	if !strings.Contains(https, `repo: "https://charts.acme.internal"`) ||
		!strings.Contains(https, "chart: cert-manager") {
		t.Errorf("a Helm repository does not render as repo + chart:\n%s", https)
	}

	oci := ChartSource("oci://harbor.acme.internal/charts", "cert-manager")
	if strings.Contains(oci, "repo:") {
		t.Errorf("an OCI reference carries a repo field, which the controller rejects:\n%s", oci)
	}
	if !strings.Contains(oci, `chart: "oci://harbor.acme.internal/charts/cert-manager"`) {
		t.Errorf("the OCI reference is not assembled:\n%s", oci)
	}

	// A trailing slash is what somebody pastes out of a registry UI.
	if ChartSource("oci://harbor.acme.internal/charts/", "cert-manager") != oci {
		t.Error("a trailing slash produces a different reference")
	}
}
