package preflight

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ryxenix/malmok/api/v1alpha1"
	"github.com/ryxenix/malmok/internal/catalogue"
)

// CheckChartDir implements PF-710.
//
// registry.chartDir is the answer for a closed site with no registry of its
// own: the chart archives cross the gap as files and are embedded in the
// HelmChart as chartContent. The install then fetches nothing -- which is the
// point, and also why a missing archive is not a slow failure but a silent
// change of plan. Without this check the chart quietly falls back to its
// upstream repository, and on a node with no route that is a HelmChart job
// retrying against the internet until it gives up.
//
// The versions are pinned in code, so the file names are known exactly. Naming
// them is most of the check's value: an operator staging a directory should be
// told what to put in it, not that something is missing.
func CheckChartDir(spec v1alpha1.ClusterSpec, docDir string) ProbeResult {
	dir := strings.TrimSpace(spec.Registry.ChartDir)
	if dir == "" {
		return skipped("PF-710", "the document names no registry.chartDir; charts come from a repository")
	}
	wanted := catalogue.Charts(spec)
	if len(wanted) == 0 {
		return skipped("PF-710", "the document installs no chart this tool fetches")
	}

	if !filepath.IsAbs(dir) {
		dir = filepath.Join(docDir, dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return failf("PF-710", "CHART_DIR_UNREADABLE",
			"registry.chartDir %s cannot be read: %v; it holds %s", dir, err, fileList(wanted))
	}

	present := map[string]bool{}
	for _, e := range entries {
		present[e.Name()] = true
	}

	var missing, wrongVersion []string
	for _, c := range wanted {
		if present[c.File()] {
			continue
		}
		// A different version of the same chart is the mistake worth naming:
		// the directory looks right, and the install would take the pinned
		// version from a repository instead without saying so.
		var other string
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), c.Name+"-") && strings.HasSuffix(e.Name(), ".tgz") {
				other = e.Name()
			}
		}
		if other != "" {
			wrongVersion = append(wrongVersion, fmt.Sprintf("%s (found %s)", c.File(), other))
			continue
		}
		missing = append(missing, c.File())
	}

	switch {
	case len(wrongVersion) > 0:
		sort.Strings(wrongVersion)
		return failf("PF-710", "CHART_VERSION_MISMATCH",
			"registry.chartDir %s holds a different version of %s; the versions are pinned in this "+
				"release, so the archive has to match or the chart comes from its repository instead",
			dir, strings.Join(wrongVersion, ", "))
	case len(missing) > 0:
		sort.Strings(missing)
		return failf("PF-710", "CHART_ARCHIVE_MISSING",
			"registry.chartDir %s does not hold %s; without them those charts are fetched from their "+
				"repositories, which an air-gapped node cannot reach",
			dir, strings.Join(missing, ", "))
	}
	return passf("PF-710", "%s holds every chart this document installs (%s)", dir, fileList(wanted))
}

func fileList(charts []catalogue.ChartRef) string {
	names := make([]string, 0, len(charts))
	for _, c := range charts {
		names = append(names, c.File())
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
