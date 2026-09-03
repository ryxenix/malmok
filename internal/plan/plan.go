// Package plan turns a specification and what the nodes turned out to be into
// the configuration that will actually be installed.
//
// PURE FUNCTION
//
//	(ClusterSpec, []NodeCapability) -> Plan. No side effects, no network, no
//	clock. That is what makes the downgrade decision tree testable without a
//	cluster, and CLAUDE.md requires table-driven tests here for exactly that
//	reason: there is no excuse.
//
// The tree it implements is docs/10-preflight-plan.md.
package plan

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ryxenix/malmok/api/v1alpha1"
	"github.com/ryxenix/malmok/internal/preflight"
)

// Plan is the confirmed configuration.
type Plan struct {
	// Requested and Actual differ exactly when something was downgraded.
	Requested Requested `json:"requested"`
	Actual    Actual    `json:"actual"`

	Downgrades []Downgrade `json:"downgrades,omitempty"`
	Warnings   []Finding   `json:"warnings,omitempty"`

	// Excluded nodes are kept out of the cluster so the rest can have the
	// requested configuration (DG-003).
	Excluded []Exclusion `json:"excluded,omitempty"`

	Nodes []string `json:"nodes"`
}

// Requested is what the document asked for.
type Requested struct {
	Dataplane v1alpha1.DataplanePreset `json:"dataplane"`
	Storage   v1alpha1.StorageDriver   `json:"storage"`
}

// Actual is what will be installed.
type Actual struct {
	Dataplane v1alpha1.DataplanePreset `json:"dataplane"`
	Storage   v1alpha1.StorageDriver   `json:"storage"`
}

// Downgrade records a configuration that could not be delivered.
//
// Every field here exists because of the same question asked six months later:
// why is this cluster running Traefik when the contract said Cilium. Without
// the triggering probes and the nodes, that has no answer.
type Downgrade struct {
	Code        string   `json:"code"` // DG-001
	From        string   `json:"from"`
	To          string   `json:"to"`
	TriggeredBy []string `json:"triggeredBy"` // probe ids
	Nodes       []string `json:"nodes"`
	Detail      string   `json:"detail"`
}

// Finding is something worth recording that does not change the configuration.
type Finding struct {
	Code   string   `json:"code"`
	Nodes  []string `json:"nodes,omitempty"`
	Detail string   `json:"detail"`
}

// Exclusion is a node kept out of the cluster.
type Exclusion struct {
	Node        string   `json:"node"`
	TriggeredBy []string `json:"triggeredBy"`
	Detail      string   `json:"detail"`
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

// ErrBlocked reports probes that stop the plan outright.
type ErrBlocked struct{ Findings []Finding }

func (e *ErrBlocked) Error() string {
	var b strings.Builder
	b.WriteString("plan: blocked by preflight:")
	for _, f := range e.Findings {
		fmt.Fprintf(&b, "\n  %s %v %s", f.Code, f.Nodes, f.Detail)
	}
	return b.String()
}

// ErrNeedsApproval reports a downgrade that DowngradePolicy=confirm will not
// take on its own.
//
// It carries the whole plan so the operator can be shown what they are being
// asked to accept rather than just told that something changed.
type ErrNeedsApproval struct {
	Plan       *Plan
	Downgrades []Downgrade
}

func (e *ErrNeedsApproval) Error() string {
	var b strings.Builder
	b.WriteString("plan: downgrade requires explicit approval:")
	for _, d := range e.Downgrades {
		fmt.Fprintf(&b, "\n  %s %s -> %s, triggered by %v on %v",
			d.Code, d.From, d.To, d.TriggeredBy, d.Nodes)
	}
	return b.String()
}

// ErrForbidden reports a downgrade under DowngradePolicy=forbid.
type ErrForbidden struct{ Downgrades []Downgrade }

func (e *ErrForbidden) Error() string {
	var b strings.Builder
	b.WriteString("plan: downgrade forbidden by policy:")
	for _, d := range e.Downgrades {
		fmt.Fprintf(&b, "\n  %s %s -> %s", d.Code, d.From, d.To)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Generate
// ---------------------------------------------------------------------------

// Generate produces the plan.
//
// Approve carries an operator's prior consent to a downgrade, so a second run
// after ErrNeedsApproval can proceed with the same inputs.
type Options struct {
	Approve bool
}

// Generate is the pure function.
func Generate(spec v1alpha1.ClusterSpec, caps []preflight.NodeCapability, opts Options) (*Plan, error) {
	if len(caps) == 0 {
		return nil, errors.New("plan: no node capabilities; run preflight first")
	}

	p := &Plan{
		Requested: Requested{spec.Kubernetes.Dataplane.Preset, spec.Storage.Driver},
		Actual:    Actual{spec.Kubernetes.Dataplane.Preset, spec.Storage.Driver},
	}
	for _, c := range caps {
		p.Nodes = append(p.Nodes, c.Host)
	}
	sort.Strings(p.Nodes)

	// A block-severity failure is not a downgrade candidate. "block" means
	// there is no safe way forward, not "not right now", so nothing further is
	// decided until it is fixed.
	if blocked := blockingFindings(caps); len(blocked) > 0 {
		return nil, &ErrBlocked{Findings: blocked}
	}

	p.decideDataplane(spec, caps)
	p.decideStorage(spec, caps)
	p.checkGatewayAddresses(spec)

	policy := spec.Kubernetes.Dataplane.DowngradePolicy
	if policy == "" {
		policy = v1alpha1.DowngradeAuto
	}
	if len(p.Downgrades) > 0 {
		switch policy {
		case v1alpha1.DowngradeForbid:
			return nil, &ErrForbidden{Downgrades: p.Downgrades}
		case v1alpha1.DowngradeConfirm:
			if !opts.Approve {
				return nil, &ErrNeedsApproval{Plan: p, Downgrades: p.Downgrades}
			}
		}
	}
	return p, nil
}

func blockingFindings(caps []preflight.NodeCapability) []Finding {
	byCode := map[string][]string{}
	detail := map[string]string{}
	for _, c := range caps {
		for _, r := range c.Blocking() {
			byCode[r.ID] = append(byCode[r.ID], c.Host)
			detail[r.ID] = r.Detail
		}
	}
	var out []Finding
	for code, nodes := range byCode {
		sort.Strings(nodes)
		out = append(out, Finding{Code: code, Nodes: nodes, Detail: detail[code]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// ---------------------------------------------------------------------------
// Dataplane
// ---------------------------------------------------------------------------

// decideDataplane walks the tree in docs/10-preflight-plan.md.
func (p *Plan) decideDataplane(spec v1alpha1.ClusterSpec, caps []preflight.NodeCapability) {
	preset := spec.Kubernetes.Dataplane.Preset

	if preset == v1alpha1.DataplaneCanalTraefik {
		// Nothing to fall back to: if netfilter is unavailable there is no
		// dataplane left, and that is a block rather than a downgrade.
		var failed []string
		for _, c := range caps {
			if len(c.FailedAt(preflight.NetfilterProbes...)) > 0 {
				failed = append(failed, c.Host)
			}
		}
		if len(failed) > 0 {
			sort.Strings(failed)
			p.Warnings = append(p.Warnings, Finding{
				Code: "PF-207", Nodes: failed,
				Detail: "netfilter modules unavailable; canal-traefik has no fallback",
			})
		}
		return
	}

	var unusable []preflight.NodeCapability
	triggers := map[string]bool{}
	for _, c := range caps {
		if c.EBPFUsable() {
			continue
		}
		unusable = append(unusable, c)
		for _, id := range c.FailedAt(preflight.EBPFProbes...) {
			triggers[id] = true
		}
	}
	if len(unusable) == 0 {
		return
	}

	// DG-003: if only agents cannot run eBPF, the requested configuration can
	// still be delivered by leaving them out. Better a smaller correct cluster
	// than a whole one quietly on the fallback.
	onlyAgents := true
	for _, c := range unusable {
		if c.Role != v1alpha1.RoleAgent {
			onlyAgents = false
			break
		}
	}
	remaining := len(caps) - len(unusable)
	if onlyAgents && remaining > 0 {
		for _, c := range unusable {
			p.Excluded = append(p.Excluded, Exclusion{
				Node:        c.Host,
				TriggeredBy: c.FailedAt(preflight.EBPFProbes...),
				Detail:      "cannot run an eBPF dataplane; excluded so the rest keeps the requested preset",
			})
		}
		p.Warnings = append(p.Warnings, Finding{
			Code: "DG-003", Nodes: excludedHosts(p.Excluded),
			Detail: "requested configuration retained by excluding nodes",
		})
		p.Nodes = withoutHosts(p.Nodes, excludedHosts(p.Excluded))
		return
	}

	fallback := spec.Kubernetes.Dataplane.Fallback
	if fallback == "" {
		fallback = v1alpha1.DataplaneCanalTraefik
	}
	p.Actual.Dataplane = fallback
	p.Downgrades = append(p.Downgrades, Downgrade{
		Code: "DG-001", From: string(preset), To: string(fallback),
		TriggeredBy: sortedKeys(triggers), Nodes: hostsOf(unusable),
		Detail: "eBPF unusable; CNI downgraded",
	})

	// The gateway rides on the same choice: dropping Cilium drops Cilium
	// Gateway with it (ADR-004), and that is a second thing the customer was
	// promised, so it is recorded separately.
	if preset == v1alpha1.DataplaneCiliumGW && fallback != v1alpha1.DataplaneCiliumGW {
		p.Downgrades = append(p.Downgrades, Downgrade{
			Code: "DG-002", From: "cilium-gateway", To: "traefik",
			TriggeredBy: sortedKeys(triggers), Nodes: hostsOf(unusable),
			Detail: "gateway implementation follows the CNI",
		})
	}
}

// ---------------------------------------------------------------------------
// Storage
// ---------------------------------------------------------------------------

func (p *Plan) decideStorage(spec v1alpha1.ClusterSpec, caps []preflight.NodeCapability) {
	if spec.Storage.Driver != v1alpha1.StorageLonghorn {
		return
	}

	var failed []string
	triggers := map[string]bool{}
	for _, c := range caps {
		if contains(excludedHosts(p.Excluded), c.Host) {
			continue
		}
		if ids := c.FailedAt(preflight.LonghornProbes...); len(ids) > 0 {
			failed = append(failed, c.Host)
			for _, id := range ids {
				triggers[id] = true
			}
		}
	}
	if len(failed) == 0 {
		return
	}
	sort.Strings(failed)

	p.Actual.Storage = v1alpha1.StorageLocalPath
	p.Downgrades = append(p.Downgrades, Downgrade{
		Code: "DG-020", From: string(v1alpha1.StorageLonghorn), To: string(v1alpha1.StorageLocalPath),
		TriggeredBy: sortedKeys(triggers), Nodes: failed,
		// local-path is node-local. Saying "downgraded" without saying that
		// leaves the customer believing they still have replicas.
		Detail: "Longhorn prerequisites missing; volumes will be node-local with no replicas",
	})
}

// ---------------------------------------------------------------------------
// Gateway
// ---------------------------------------------------------------------------

// checkGatewayAddresses warns when a gateway has no pinned address.
//
// The DNS record has to be requested before install starts, so the address has
// to be decided up front; letting LB-IPAM pick one is fine in a homelab and
// unacceptable where somebody else runs the DNS (PF-612).
//
// A node-ips gateway is not in that position. Nothing allocates it an address:
// Envoy binds the port in the node's own network namespace, so the addresses
// are the ones the nodes already have and the record can be written the day
// the machines are racked. Warning there taught operators to ignore PF-612 on
// the configuration where it is least likely to be wrong.
func (p *Plan) checkGatewayAddresses(spec v1alpha1.ClusterSpec) {
	var unpinned []string
	for _, gw := range spec.Gateway.Gateways {
		if gw.Exposure == v1alpha1.ExposureNodeIPs {
			continue
		}
		if gw.Address == "" {
			unpinned = append(unpinned, gw.Name)
		}
	}
	if len(unpinned) == 0 {
		return
	}
	sort.Strings(unpinned)
	p.Warnings = append(p.Warnings, Finding{
		Code:   "PF-612",
		Detail: "gateways without a pinned address: " + strings.Join(unpinned, ", "),
	})
}

// Downgraded reports whether anything differs from what was requested.
func (p *Plan) Downgraded() bool { return len(p.Downgrades) > 0 }

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func hostsOf(caps []preflight.NodeCapability) []string {
	out := make([]string, len(caps))
	for i, c := range caps {
		out[i] = c.Host
	}
	sort.Strings(out)
	return out
}

func excludedHosts(ex []Exclusion) []string {
	out := make([]string, len(ex))
	for i, e := range ex {
		out[i] = e.Node
	}
	sort.Strings(out)
	return out
}

func withoutHosts(hosts, drop []string) []string {
	var out []string
	for _, h := range hosts {
		if !contains(drop, h) {
			out = append(out, h)
		}
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
