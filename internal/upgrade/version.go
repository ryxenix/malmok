// Package upgrade moves a cluster that already exists to a new version.
//
// It is a separate phase set rather than part of the build, but it is not a
// second installer: ADR-013 made the install step's observable "what does
// `rke2 --version` answer", which is exactly what makes installing and
// upgrading the same operation. What this package adds is the three things that
// are only true of an upgrade -- whether the step is legal from where the
// cluster already is, what order the nodes move in, and getting the workloads
// off a node before its kubelet restarts.
package upgrade

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"platform.ryxen.dev/malmok/internal/codes"
	"platform.ryxen.dev/malmok/internal/preflight"
)

// Version is an RKE2 version, comparable.
//
// The build number is part of it: v1.34.10+rke2r2 is a real release with real
// fixes over +rke2r1, and treating the suffix as decoration would make the tool
// call an upgrade "already satisfied" when it is not.
type Version struct {
	Major, Minor, Patch int
	Build               int
	// Raw is what was parsed, kept so evidence quotes the operator's own string
	// rather than a reconstruction of it.
	Raw string
}

// versionPattern is deliberately strict.
//
// A version is what an operator types under time pressure, and the failure of a
// near-miss like "1.34.10" is an installer that downloads nothing and a node
// that stays where it was. Refusing it up front costs one line; discovering it
// on the third node of six costs a maintenance window.
var versionPattern = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)\+rke2r(\d+)$`)

// ParseVersion reads an RKE2 version string.
func ParseVersion(s string) (Version, error) {
	m := versionPattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Version{}, fmt.Errorf("%q is not an RKE2 version; it has the form v1.34.10+rke2r1", s)
	}
	n := func(i int) int { v, _ := strconv.Atoi(m[i]); return v }
	return Version{Major: n(1), Minor: n(2), Patch: n(3), Build: n(4), Raw: strings.TrimSpace(s)}, nil
}

// Compare orders two versions: -1, 0 or 1.
func (v Version) Compare(o Version) int {
	for _, pair := range [][2]int{
		{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}, {v.Build, o.Build},
	} {
		switch {
		case pair[0] < pair[1]:
			return -1
		case pair[0] > pair[1]:
			return 1
		}
	}
	return 0
}

func (v Version) String() string {
	if v.Raw != "" {
		return v.Raw
	}
	return fmt.Sprintf("v%d.%d.%d+rke2r%d", v.Major, v.Minor, v.Patch, v.Build)
}

// Known reports whether anything was parsed into this version.
func (v Version) Known() bool { return v.Major > 0 || v.Minor > 0 }

// minorsApart is how many minor versions separate two releases.
//
// A major boundary counts as far apart rather than as a number: there is no
// arithmetic that makes v1.99 to v2.0 one step, and pretending otherwise would
// let the one upgrade nobody should attempt through unchallenged.
func minorsApart(from, to Version) int {
	if from.Major != to.Major {
		return 99
	}
	return to.Minor - from.Minor
}

// ---------------------------------------------------------------------------
// Preconditions
// ---------------------------------------------------------------------------

// NodeState is what one node reported before anything was changed.
type NodeState struct {
	// Host is the address the document names it by.
	Host string
	// Agent is true for a worker.
	Agent bool
	// Version is what the node is running: the kubelet version the control
	// plane reports. Zero when the cluster has nothing to say about it.
	//
	// This and not the binary on disk, because they differ exactly when an
	// upgrade is half done, and every rule below is about where the cluster is
	// now rather than where its files are.
	Version Version
	// Installed is the binary on disk, which is what the node will run after a
	// restart. Carried so a node between the two can be described.
	Installed Version
	// Ready is what the control plane says about it.
	Ready bool
	// Evidence is the raw line the node printed.
	Evidence string
}

// State is everything Check is a function of.
//
// A struct rather than a long parameter list because the point of this type is
// that the decision has no other inputs: no node is contacted here, nothing is
// read from disk, and the same state always produces the same verdict. That is
// what makes the skew rules testable in a table instead of against a cluster.
type State struct {
	Nodes []NodeState
	// LastSnapshot is the newest etcd snapshot found on a server, zero when
	// there is none.
	LastSnapshot time.Time
	// Now is the clock, passed in so the snapshot age is not a function of when
	// the test runs.
	Now time.Time
}

// SnapshotAge is how old a snapshot may be before it stops being a way back.
const SnapshotAge = 24 * time.Hour

// Check decides whether the cluster may move to target.
//
// Every rule here is one somebody paid for. The control plane moves one minor
// at a time because a server that skipped one cannot convert objects stored by
// the version it never saw; a kubelet may lag its API server and must never
// lead it; and there is no downgrade, because the API server writes storage the
// older one cannot read and a snapshot restore is the only way back.
func Check(st State, target string) []preflight.ProbeResult {
	var out []preflight.ProbeResult
	add := func(id string, ok bool, node, detail, evidence string) {
		status := preflight.StatusPass
		if !ok {
			status = preflight.StatusFail
		}
		out = append(out, preflight.ProbeResult{
			ID: id, Status: status, Severity: codes.MustLookup(id).Severity,
			Node: node, Detail: detail, Evidence: evidence,
		})
	}

	want, err := ParseVersion(target)
	if err != nil {
		add("UP-001", false, "", err.Error(), target)
		// Nothing after this can be decided: every other rule is a comparison
		// against a version that was not understood.
		return out
	}
	add("UP-001", true, "", "the target is "+want.String(), want.String())

	// The control plane's level is its oldest server. Upgrading is bounded by
	// the node that has furthest to come, not by the one that is furthest ahead.
	var lowestServer, highestServer, highestAgent Version
	var haveServer bool
	for _, n := range st.Nodes {
		if !n.Version.Known() {
			continue
		}
		if n.Agent {
			if n.Version.Compare(highestAgent) > 0 {
				highestAgent = n.Version
			}
			continue
		}
		if !haveServer || n.Version.Compare(lowestServer) < 0 {
			lowestServer = n.Version
		}
		if n.Version.Compare(highestServer) > 0 {
			highestServer = n.Version
		}
		haveServer = true
	}

	// A node already past the target would be moved backwards, which is the
	// same refusal as a downgrade and has to be reported per node: "somewhere
	// in this cluster" is not something an operator can act on.
	ahead := true
	for _, n := range st.Nodes {
		if n.Version.Known() && n.Version.Compare(want) > 0 {
			add("UP-004", false, n.Host,
				fmt.Sprintf("%s already runs %s, which is newer than %s", n.Host, n.Version, want),
				n.Evidence)
			ahead = false
		}
	}
	if ahead {
		add("UP-004", true, "", "no node is ahead of the target", "")
	}

	if haveServer && highestAgent.Known() && highestAgent.Compare(highestServer) > 0 {
		add("UP-005", false, "",
			fmt.Sprintf("an agent runs %s and the newest server runs %s; a kubelet must never lead its API server",
				highestAgent, highestServer), "")
	} else {
		add("UP-005", true, "", "no agent is ahead of the servers", "")
	}

	if haveServer {
		switch {
		case want.Compare(lowestServer) <= 0:
			add("UP-002", false, "",
				fmt.Sprintf("the servers run %s and the target is %s, which is not newer", lowestServer, want),
				lowestServer.String())
		default:
			add("UP-002", true, "",
				fmt.Sprintf("%s is newer than the %s the servers run", want, lowestServer), "")
		}

		if step := minorsApart(lowestServer, want); step > 1 {
			add("UP-003", false, "",
				fmt.Sprintf("%s to %s is %s; the control plane moves one minor version at a time",
					lowestServer, want, minorPhrase(step)), "")
		} else {
			add("UP-003", true, "", fmt.Sprintf("%s to %s is one step or less", lowestServer, want), "")
		}
	}

	// Ready before starting, because an upgrade restarts each node in turn and
	// beginning while one is already down is how a control plane loses quorum
	// inside a maintenance window.
	allReady := true
	for _, n := range st.Nodes {
		if !n.Ready {
			add("UP-101", false, n.Host, n.Host+" is not Ready", n.Evidence)
			allReady = false
		}
	}
	if allReady {
		add("UP-101", true, "", fmt.Sprintf("all %d nodes are Ready", len(st.Nodes)), "")
	}

	// Not a fault. A single-node cluster is a supported shape, and this is what
	// upgrading one means -- recorded so the restart is not a surprise later.
	if len(st.Nodes) == 1 {
		out = append(out, preflight.ProbeResult{
			ID: "UP-102", Status: preflight.StatusPass, Severity: codes.MustLookup("UP-102").Severity,
			Detail: "there is one node, so its workloads restart in place rather than moving",
		})
	}

	switch {
	case st.LastSnapshot.IsZero():
		add("UP-103", false, "", "no etcd snapshot was found; a failed control plane upgrade has no way back", "")
	case st.Now.Sub(st.LastSnapshot) > SnapshotAge:
		add("UP-103", false, "",
			fmt.Sprintf("the newest etcd snapshot is from %s, more than %s ago",
				st.LastSnapshot.Format(time.RFC3339), SnapshotAge), "")
	default:
		add("UP-103", true, "",
			"the newest etcd snapshot is from "+st.LastSnapshot.Format(time.RFC3339), "")
	}

	return out
}

func minorPhrase(step int) string {
	if step >= 99 {
		return "a major version apart"
	}
	return fmt.Sprintf("%d minor versions", step)
}

// Blocking returns the findings that stop the upgrade.
func Blocking(results []preflight.ProbeResult) []preflight.ProbeResult {
	var out []preflight.ProbeResult
	for _, r := range results {
		if r.Failed() && r.Severity == codes.SeverityBlock {
			out = append(out, r)
		}
	}
	return out
}
