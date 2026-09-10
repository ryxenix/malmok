package observability

import "github.com/ryxenix/malmok/api/v1alpha1"

// The images the VictoriaMetrics operator pulls, which no chart states.
//
// The stack chart templates VMSingle, VMAgent, VMAlert and VMAlertmanager as
// custom resources. Their images are filled in by the operator at run time:
// the chart's values carry a tag and put the repository in a renovate comment,
// so rendering the chart -- which is how the rest of the carry list is
// generated -- finds nothing.
//
// That hole was invisible online, where the node simply fetches them, and
// fatal on a closed site. The air-gapped lab case installed RKE2, Cilium, the
// gateway and local-path from carried files and then sat for fifteen minutes
// on a metrics database that could not pull its own image.
//
// These are read from a cluster that ran the stack, not from the chart. The
// chart does not say, and a value guessed out of a comment is a value
// discovered wrong in a room with no way to fetch what is missing.
const (
	// MetricsImage is what a VMSingle runs.
	MetricsImage = "victoriametrics/victoria-metrics:v1.150.0"
	// AgentImage is what a VMAgent runs.
	AgentImage = "victoriametrics/vmagent:v1.150.0"
	// AlertImage is what a VMAlert runs.
	AlertImage = "victoriametrics/vmalert:v1.150.0"
	// AlertmanagerImage is what a VMAlertmanager runs. Prometheus' own, not
	// VictoriaMetrics'.
	AlertmanagerImage = "prom/alertmanager:v0.32.1"
	// ConfigReloaderImage is the sidecar every one of them carries.
	ConfigReloaderImage = "victoriametrics/operator:config-reloader-v0.74.0"
)

// ImagesReadFromChart is the chart version the images above were observed
// under.
//
// The guard is a test: bump StackChartVersion without re-reading them and the
// build fails, because a stack that moved and a list that did not is the same
// hole in a different place. Re-read them from a cluster that has run the
// stack -- that is what the chart cannot tell you.
const ImagesReadFromChart = "0.91.2"

// AllImages is every image this phase pulls that no chart names.
func AllImages() []string {
	return []string{
		MetricsImage, AgentImage, AlertImage, AlertmanagerImage, ConfigReloaderImage,
	}
}

// Images are the ones a document actually needs.
func Images(spec v1alpha1.ClusterSpec) []string {
	if !Enabled(spec) {
		return nil
	}
	return AllImages()
}
