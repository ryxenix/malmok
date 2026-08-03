// Package preflight defines what a node turns out to be capable of.
//
// The probes themselves need SSH and a node; these types do not, which is what
// lets the plan generator be a pure function tested without either. See
// docs/10-preflight-plan.md.
package preflight

import (
	"time"

	"platform.ryxen.dev/platformctl/api/v1alpha1"
	"platform.ryxen.dev/platformctl/internal/codes"
)

// Status is the outcome of one probe.
type Status string

const (
	StatusPass Status = "pass"
	StatusFail Status = "fail"
	StatusSkip Status = "skip"
)

// ProbeResult is one measurement.
//
// Measurement, not inference: the whole point of preflight is that OS names and
// kernel version strings do not tell you what a node can do. Evidence is kept
// because the audit report has to be able to show the raw output six months
// later.
type ProbeResult struct {
	ID       string         `json:"id"`     // PF-204
	Status   Status         `json:"status"` // pass | fail | skip
	Severity codes.Severity `json:"severity"`
	Code     string         `json:"code,omitempty"`   // EBPF_LOAD_DENIED
	Detail   string         `json:"detail,omitempty"` // English, fixed
	Evidence string         `json:"evidence,omitempty"`
}

// Failed reports whether the probe found a problem.
func (r ProbeResult) Failed() bool { return r.Status == StatusFail }

// OSInfo is what the node reports about itself.
type OSInfo struct {
	Family  v1alpha1.OSFamily `json:"family"`
	Version string            `json:"version"`
	Kernel  string            `json:"kernel"`
}

// NodeCapability is one node's preflight outcome.
type NodeCapability struct {
	Host     string            `json:"host"`
	Hostname string            `json:"hostname,omitempty"`
	Role     v1alpha1.NodeRole `json:"role"`

	OS   OSInfo `json:"os"`
	Arch string `json:"arch"`

	// Probes is keyed by code, e.g. "PF-204".
	Probes map[string]ProbeResult `json:"probes"`

	CollectedAt time.Time `json:"collectedAt"`
}

// Probe returns one result.
func (c NodeCapability) Probe(id string) (ProbeResult, bool) {
	r, ok := c.Probes[id]
	return r, ok
}

// Passed reports whether a probe ran and passed. A probe that never ran is not
// a pass: the plan generator must not treat missing evidence as good news.
func (c NodeCapability) Passed(id string) bool {
	r, ok := c.Probes[id]
	return ok && r.Status == StatusPass
}

// FailedAt returns the ids of the given probes that failed on this node.
func (c NodeCapability) FailedAt(ids ...string) []string {
	var out []string
	for _, id := range ids {
		if r, ok := c.Probes[id]; ok && r.Failed() {
			out = append(out, id)
		}
	}
	return out
}

// Blocking returns every probe result on this node whose severity stops the
// plan outright.
func (c NodeCapability) Blocking() []ProbeResult {
	var out []ProbeResult
	for _, r := range c.Probes {
		if r.Failed() && r.Severity == codes.SeverityBlock {
			out = append(out, r)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Probe groups
// ---------------------------------------------------------------------------

// EBPFProbes are the checks a Cilium dataplane depends on.
//
// PF-204 is the decisive one: reading config symbols alone misses LSM policy,
// kernel lockdown and third-party security agents, all of which reject the load
// at runtime while every static indicator still looks healthy.
var EBPFProbes = []string{"PF-201", "PF-202", "PF-203", "PF-204"}

// NetfilterProbes are what the Canal fallback needs. If these fail there is
// nothing left to fall back to.
var NetfilterProbes = []string{"PF-207"}

// LonghornProbes are Longhorn's prerequisites: iscsid, the NFS client for RWX
// volumes, and multipathd not claiming the block devices.
var LonghornProbes = []string{"PF-404", "PF-405", "PF-406"}

// EBPFUsable reports whether this node can run an eBPF dataplane.
func (c NodeCapability) EBPFUsable() bool {
	for _, id := range EBPFProbes {
		if !c.Passed(id) {
			return false
		}
	}
	return true
}
