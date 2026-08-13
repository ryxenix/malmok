package tui

import (
	"sort"
	"strings"
)

// A validation message that only says what is wrong leaves the operator to
// hunt for the screen that owns it. Every problem is routed back to the step
// that can fix it, and the summary offers to go there.

// problem is one validation failure with the screen that can correct it.
type problem struct {
	Step Step
	Text string
}

// fieldOwners maps a field path prefix to the step that collects it.
//
// Longest prefix wins, so kubernetes.dataplane.loadBalancerPool routes to the
// network screen while the rest of kubernetes.dataplane routes to options.
var fieldOwners = []struct {
	prefix string
	step   Step
}{
	// There is no screen that owns metadata: the profile is derived from what
	// was composed rather than chosen, so a problem with it is a problem with
	// the composition and the summary is where the composition is shown.
	{"metadata.", StepSummary},
	{"topology.", StepNodes},
	{"kubernetes.version", StepNodes},
	{"pki.domain", StepNodes},
	{"gateway.domainSuffix", StepNodes},

	{"network.proxy", StepNetwork},
	{"network.", StepNetwork},
	{"kubernetes.dataplane.loadBalancerPool", StepNetwork},

	{"kubernetes.dataplane", StepOptions},
	{"storage.", StepOptions},

	{"registry.", StepRegistry},

	{"pki.", StepPKI},
	{"gateway.", StepPKI},
}

// ownerOf returns the step that collects the field a message is about.
func ownerOf(message string) Step {
	best, bestLen := StepSummary, -1
	for _, o := range fieldOwners {
		if strings.HasPrefix(message, o.prefix) && len(o.prefix) > bestLen {
			best, bestLen = o.step, len(o.prefix)
		}
	}
	return best
}

// problems returns the document's validation failures, each routed to the step
// that can fix it, ordered by that step so the operator walks forwards.
func (w *Wizard) problems() []problem {
	lines := w.validateConfig()
	out := make([]problem, 0, len(lines))
	for _, line := range lines {
		out = append(out, problem{Step: ownerOf(line), Text: line})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Step < out[j].Step })
	return out
}

// firstProblemStep is where "go and fix it" leads.
func (w *Wizard) firstProblemStep() (Step, bool) {
	if ps := w.problems(); len(ps) > 0 {
		return ps[0].Step, true
	}
	return 0, false
}
