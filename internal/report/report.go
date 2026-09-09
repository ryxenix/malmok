// Package report builds the audit report of docs/00-architecture.md §7.
//
// It reads a finished run directory and nothing else. That is the whole design:
// a report generated from live cluster state would say something different
// every time it ran, and the thing a customer receives has to be the record of
// what happened, not a view of what is true this minute.
//
// English, fixed. The customer-facing maintenance report is Korean
// and is a different document with a different audience.
package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ryxenix/malmok/api/v1alpha1"
	"github.com/ryxenix/malmok/internal/dataplane"
	"github.com/ryxenix/malmok/internal/event"
	"github.com/ryxenix/malmok/internal/plan"
	"github.com/ryxenix/malmok/internal/rke2"
	"github.com/ryxenix/malmok/internal/spec"
)

// ArtifactsDir is where a run keeps what it produced (docs/11-execute.md §1.1).
const ArtifactsDir = "artifacts"

// FileNames of the artifacts this phase writes.
const (
	AuditReportFile = "audit-report.md"
	DNSRecordFile   = "dns-records.md"
	HandoffFile     = "handoff.json"
	PlanFile        = "plan.json"
)

// Run is everything read out of a run directory.
type Run struct {
	ID     string
	Dir    string
	Spec   v1alpha1.ClusterSpec
	Plan   *plan.Plan
	Events []event.Event

	StartedAt, EndedAt time.Time
}

// Load reads a run directory.
//
// A missing plan is not an error: a run that stopped before the plan was
// generated still has probes worth reporting, and refusing to produce anything
// is the least useful response to a partial run.
func Load(dir string) (*Run, error) {
	r := &Run{Dir: dir}

	doc, err := spec.Load(spec.SnapshotPath(dir))
	if err != nil {
		return nil, fmt.Errorf("report: read the run's document: %w", err)
	}
	r.Spec = doc.Spec

	f, err := os.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("report: read the run's events: %w", err)
	}
	defer f.Close()

	sc := event.NewScanner(f)
	for sc.Scan() {
		e := sc.Event()
		r.Events = append(r.Events, e)
		if r.ID == "" {
			r.ID = e.Run
		}
		if r.StartedAt.IsZero() || e.TS.Before(r.StartedAt) {
			r.StartedAt = e.TS.Time
		}
		if e.TS.After(r.EndedAt) {
			r.EndedAt = e.TS.Time
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("report: read the run's events: %w", err)
	}

	if body, err := os.ReadFile(filepath.Join(dir, PlanFile)); err == nil {
		var p plan.Plan
		if err := json.Unmarshal(body, &p); err == nil {
			r.Plan = &p
		}
	}
	return r, nil
}

// SavePlan writes the plan into the run directory.
//
// Without it the downgrade history cannot be reported, and the downgrade
// history is the section §7 singles out: six months later nobody remembers
// which settings were chosen and which the tool decided.
func SavePlan(dir string, p *plan.Plan) error {
	body, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("report: encode the plan: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, PlanFile), append(body, '\n'), 0o644)
}

// Write produces both artifacts and returns their paths.
func Write(r *Run) ([]string, error) {
	dir := filepath.Join(r.Dir, ArtifactsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("report: create %s: %w", dir, err)
	}

	var written []string
	for name, body := range map[string]string{
		AuditReportFile: Audit(r),
		DNSRecordFile:   DNSRecords(r),
	} {
		if body == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return nil, fmt.Errorf("report: write %s: %w", p, err)
		}
		written = append(written, p)
	}
	// The handoff is the third artifact and the only one a machine reads.
	// Written unconditionally: a failed run's handoff says the run failed,
	// which an inventory needs to hear at least as much as a success.
	p, err := WriteHandoff(r)
	if err != nil {
		return nil, err
	}
	written = append(written, p)
	sort.Strings(written)
	return written, nil
}

// ---------------------------------------------------------------------------
// The audit report
// ---------------------------------------------------------------------------

// Audit renders the report of docs/00-architecture.md §7.
//
// Every section appears whether or not there is data for it. A section quietly
// omitted reads as a section that passed, and the two things a report is for
// are saying what happened and saying what is not known.
func Audit(r *Run) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# Audit report: %s\n\n", r.Spec.Metadata.Name)
	fmt.Fprintf(&b, "Run `%s`, %s to %s (%s).\n\n",
		r.ID,
		r.StartedAt.UTC().Format(time.RFC3339),
		r.EndedAt.UTC().Format(time.RFC3339),
		r.EndedAt.Sub(r.StartedAt).Round(time.Second))
	b.WriteString("Generated from the run directory alone. It is the record of what happened, " +
		"not a view of the cluster as it is now.\n\n")

	configuration(&b, r)
	downgrades(&b, r)
	preflightSection(&b, r)
	certificates(&b, r)
	network(&b, r)
	security(&b, r)
	constraints(&b, r)
	return b.String()
}

// configuration is §7's first section: profile, topology, versions.
func configuration(b *strings.Builder, r *Run) {
	s := r.Spec
	b.WriteString("## Configuration\n\n")

	profile := string(s.Metadata.Profile)
	if profile == "" || profile == string(v1alpha1.ProfileCustom) {
		profile = "custom (Tier-3, unvalidated)"
	}
	fmt.Fprintf(b, "| | |\n|---|---|\n")
	fmt.Fprintf(b, "| Profile | %s |\n", profile)
	fmt.Fprintf(b, "| Network mode | %s |\n", s.Network.Mode)
	fmt.Fprintf(b, "| Dataplane | %s |\n", s.Kubernetes.Dataplane.Preset)
	fmt.Fprintf(b, "| Storage | %s |\n", s.Storage.Driver)
	fmt.Fprintf(b, "| Registry | %s |\n", s.Registry.Mode)
	fmt.Fprintf(b, "| PKI | %s |\n", s.PKI.Mode)
	fmt.Fprintf(b, "| Registration address | %s |\n", s.Topology.RegistrationAddress)
	b.WriteString("\n### Nodes\n\n")
	b.WriteString("| Host | Role | OS | Kernel | Arch |\n|---|---|---|---|---|\n")

	facts := nodeFacts(r)
	for _, n := range allNodes(s) {
		f := facts[n.Host]
		fmt.Fprintf(b, "| %s | %s | %s | %s | %s |\n",
			n.Host, n.Role, dash(f.os), dash(f.kernel), dash(f.arch))
	}

	// The component manifest is what an upgrade is planned against, and the
	// versions are the ones this tool pinned rather than whatever is installed
	// now -- the report is a record, not an inventory.
	b.WriteString("\n### Components\n\n")
	b.WriteString("| Component | Version | Pinned by |\n|---|---|---|\n")
	fmt.Fprintf(b, "| RKE2 | %s | the document |\n", dash(r.Spec.Kubernetes.Version))
	if strings.HasPrefix(string(r.Spec.Kubernetes.Dataplane.Preset), "cilium") {
		fmt.Fprintf(b, "| Gateway API | %s | malmok (what Cilium supports) |\n",
			dataplane.GatewayAPIVersion)
	}
	if v := r.Spec.Topology.VIP; v != nil && v.Address != "" {
		fmt.Fprintf(b, "| kube-vip | %s | malmok |\n", rke2.KubeVIPVersion)
	}
	b.WriteString("\nCilium and CoreDNS are the versions RKE2 " +
		strings.TrimSpace(r.Spec.Kubernetes.Version) + " bundles; this tool does not pin them.\n\n")
}

// downgrades is the section §7 singles out.
func downgrades(b *strings.Builder, r *Run) {
	b.WriteString("## Downgrades\n\n")

	if r.Plan == nil {
		b.WriteString("_No plan was recorded for this run, so no downgrade history can be shown._\n\n")
		return
	}
	if len(r.Plan.Downgrades) == 0 && len(r.Plan.Excluded) == 0 {
		fmt.Fprintf(b, "Nothing was downgraded. The cluster runs the dataplane (%s) and storage (%s) "+
			"the document asked for.\n\n", r.Plan.Actual.Dataplane, r.Plan.Actual.Storage)
	}

	for _, d := range r.Plan.Downgrades {
		fmt.Fprintf(b, "### %s: %s -> %s\n\n", d.Code, d.From, d.To)
		fmt.Fprintf(b, "| | |\n|---|---|\n")
		fmt.Fprintf(b, "| Requested | %s |\n", d.From)
		fmt.Fprintf(b, "| Installed | %s |\n", d.To)
		fmt.Fprintf(b, "| Triggered by | %s |\n", strings.Join(d.TriggeredBy, ", "))
		fmt.Fprintf(b, "| Nodes | %s |\n\n", strings.Join(d.Nodes, ", "))
		fmt.Fprintf(b, "%s\n\n", d.Detail)
	}
	for _, e := range r.Plan.Excluded {
		fmt.Fprintf(b, "### Node excluded: %s\n\n", e.Node)
		fmt.Fprintf(b, "Triggered by %s. %s\n\n", strings.Join(e.TriggeredBy, ", "), e.Detail)
	}
	for _, w := range r.Plan.Warnings {
		fmt.Fprintf(b, "- **%s** %s\n", w.Code, w.Detail)
	}
	if len(r.Plan.Warnings) > 0 {
		b.WriteString("\n")
	}
}

// preflightSection lists every probe with its evidence.
func preflightSection(b *strings.Builder, r *Run) {
	b.WriteString("## Preflight\n\n")

	probes := probeEvents(r, "PF-")
	if len(probes) == 0 {
		b.WriteString("_No preflight results were recorded for this run._\n\n")
		return
	}

	pass, advisory, blocked := 0, 0, 0
	for _, e := range probes {
		switch e.Status {
		case event.StatusBlocked:
			blocked++
		case event.StatusFailed:
			advisory++
		case event.StatusOK:
			pass++
		}
	}
	fmt.Fprintf(b, "%d checks: %d passed, %d worth reading, %d blocking, %d not applicable.\n\n",
		len(probes), pass, advisory, blocked, len(probes)-pass-advisory-blocked)

	// Only what did not pass is listed in full. A report that prints a hundred
	// passing checks is a report nobody reads to the end, and the event file is
	// alongside it for anyone who wants everything.
	b.WriteString("### Findings\n\n")
	any := false
	for _, e := range probes {
		if e.Status != event.StatusFailed && e.Status != event.StatusBlocked {
			continue
		}
		any = true
		fmt.Fprintf(b, "- **%s** %s%s\n  %s\n", e.Code, severityWord(e), nodeSuffix(e), e.Detail)
		if e.Evidence != "" {
			fmt.Fprintf(b, "  Evidence: `%s`\n", e.Evidence)
		}
	}
	if !any {
		b.WriteString("Everything measured passed.\n")
	}
	b.WriteString("\nThe full result of every check, with raw evidence, is in `events.jsonl` beside this file.\n\n")
}

// certificates covers what PF-9xx and the PV wire checks found.
func certificates(b *strings.Builder, r *Run) {
	b.WriteString("## Certificates\n\n")

	pv := probeEvents(r, "PV-")
	if len(pv) == 0 {
		b.WriteString("_No wire verification was recorded._ ")
		if !servesTLS(r.Spec) {
			b.WriteString("No listener terminates TLS, so there was nothing to verify.\n\n")
		} else {
			b.WriteString("A listener terminates TLS and the checks did not run, " +
				"which means nothing here has been confirmed against a client.\n\n")
		}
		return
	}

	// PV-002 is the one that matters: it is the strict profile, and passing it
	// means Java, Go, curl and Python accept the chain. A browser passing is
	// not evidence (ADR-010).
	for _, e := range pv {
		if e.Code != "PV-002" {
			continue
		}
		if e.Status == event.StatusOK {
			fmt.Fprintf(b, "**The served chain verifies under the strict profile** (%s): "+
				"no issuer is fetched over AIA, so Java, Go, curl and Python all accept it.\n\n", e.Node)
		} else {
			fmt.Fprintf(b, "**The served chain does not verify under the strict profile** (%s).\n\n%s\n\n",
				e.Node, e.Detail)
		}
	}

	b.WriteString("| Check | Endpoint | Result |\n|---|---|---|\n")
	for _, e := range pv {
		fmt.Fprintf(b, "| %s | %s | %s %s |\n", e.Code, dash(e.Node), severityWord(e), oneLine(e.Detail))
	}
	b.WriteString("\n")
}

// network covers the port matrix, DNS and what resolved.
func network(b *strings.Builder, r *Run) {
	b.WriteString("## Network\n\n")

	fmt.Fprintf(b, "| | |\n|---|---|\n")
	fmt.Fprintf(b, "| Pod CIDR | %s |\n", orDefault(r.Spec.Network.PodCIDR, "10.42.0.0/16"))
	fmt.Fprintf(b, "| Service CIDR | %s |\n", orDefault(r.Spec.Network.SvcCIDR, "10.43.0.0/16"))
	if pool := r.Spec.Kubernetes.Dataplane.LoadBalancerPool; len(pool) > 0 {
		fmt.Fprintf(b, "| Load balancer pool | %s |\n", strings.Join(pool, ", "))
	}
	if v := r.Spec.Topology.VIP; v != nil && v.Address != "" {
		fmt.Fprintf(b, "| Control plane VIP | %s (%s) |\n", v.Address, orDefault(v.Mode, "arp"))
	}
	b.WriteString("\n")

	b.WriteString("### Reachability between nodes\n\n")
	found := false
	for _, e := range probeEvents(r, "PF-60") {
		if e.Code != "PF-601" && e.Code != "PF-602" {
			continue
		}
		found = true
		fmt.Fprintf(b, "- **%s** %s%s %s\n", e.Code, severityWord(e), nodeSuffix(e), e.Detail)
	}
	if !found {
		b.WriteString("_Not measured. A single-node cluster has no peer to reach._\n")
	}
	b.WriteString("\n")

	records := dnsRecords(r.Spec)
	b.WriteString("### DNS records\n\n")
	if len(records) == 0 {
		b.WriteString("_No record is required: no gateway pins an address._\n\n")
	} else {
		b.WriteString("These have to exist in the customer's DNS. The full sheet is in `" +
			DNSRecordFile + "` beside this file.\n\n")
		b.WriteString("| FQDN | Type | Value |\n|---|---|---|\n")
		for _, rec := range records {
			fmt.Fprintf(b, "| %s | %s | %s |\n", rec.FQDN, rec.Type, rec.Value)
		}
		b.WriteString("\n")
	}
}

// security is honest about what is not implemented.
func security(b *strings.Builder, r *Run) {
	b.WriteString("## Security\n\n")

	h := r.Spec.OS.Hardening
	cis := "not requested"
	if h.CISProfile != nil && *h.CISProfile {
		cis = "requested"
	} else if h.PrepareCISPrerequisites != nil && *h.PrepareCISPrerequisites {
		cis = "prerequisites only"
	}
	fmt.Fprintf(b, "CIS profile: %s\n\n", cis)

	b.WriteString("| Check | Node | Result |\n|---|---|---|\n")
	for _, e := range probeEvents(r, "PF-30") {
		fmt.Fprintf(b, "| %s | %s | %s |\n", e.Code, dash(e.Node), oneLine(e.Detail))
	}
	b.WriteString("\n")

	// Naming what is missing beats a section that looks complete. A reader who
	// cannot tell "scanned and clean" from "never scanned" has been misled by
	// the report rather than informed by it.
	b.WriteString("**Not produced by this run:** a CIS scan result and an SBOM. " +
		"Neither is implemented yet, so their absence here says nothing about the cluster.\n\n")
}

// constraints is where anything the operator has to live with is collected.
func constraints(b *strings.Builder, r *Run) {
	b.WriteString("## Constraints\n\n")
	var items []string

	if p := r.Spec.Metadata.Profile; p == "" || p == v1alpha1.ProfileCustom {
		items = append(items, "The profile is `custom`, which is Tier-3: no validated baseline "+
			"was applied and no combination here has been tested together.")
	}
	if r.Spec.Registry.Insecure != nil && *r.Spec.Registry.Insecure {
		items = append(items, "`registry.insecure` is set: nothing authenticates the connection "+
			"every node pulls images over.")
	}
	if r.Spec.PKI.Mode == v1alpha1.PKINone {
		items = append(items, "`pki.mode` is `none`: this build issued no certificates, and any "+
			"TLS in front of it is somebody else's responsibility.")
	}

	for _, e := range r.Events {
		switch e.Code {
		case "PV-004":
			if e.Status != event.StatusOK {
				items = append(items, "Revocation distribution points are not reachable. A client "+
					"configured to hard-fail on revocation will refuse the connection, and that "+
					"check has to be disabled on the systems that call this cluster.")
			}
		case "PF-401":
			if e.Status != event.StatusOK {
				items = append(items, "The RKE2 data directory shares the root filesystem on at "+
					"least one node: an image pull that runs away fills the node rather than a "+
					"directory.")
			}
		case "PF-610":
			if e.Status != event.StatusOK {
				items = append(items, "At least one node resolves through the systemd-resolved "+
					"stub, which does not exist inside a pod's network namespace.")
			}
		}
	}
	// The same constraint can be raised by several nodes; it is one constraint.
	items = dedupe(items)

	if len(items) == 0 {
		b.WriteString("Nothing recorded.\n\n")
		return
	}
	for _, i := range items {
		fmt.Fprintf(b, "- %s\n", i)
	}
	b.WriteString("\n")
}

// ---------------------------------------------------------------------------
// The DNS record sheet
// ---------------------------------------------------------------------------

// Record is one line of the sheet the customer's DNS team receives.
type Record struct {
	FQDN, Type, Value, Zone, Why string
}

// DNSRecords renders the sheet.
//
// It exists because the records have to be requested before the install, not
// after: a customer's DNS change can take days, and PF-612 asks for a pinned
// gateway address for exactly this reason.
func DNSRecords(r *Run) string {
	records := dnsRecords(r.Spec)
	if len(records) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# DNS records: %s\n\n", r.Spec.Metadata.Name)
	b.WriteString("These records have to exist in the customer's DNS for the cluster to be " +
		"reachable by name. Nothing here is created by this tool.\n\n")
	b.WriteString("| FQDN | Type | Value | Zone | Why |\n|---|---|---|---|---|\n")
	for _, rec := range records {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n",
			rec.FQDN, rec.Type, rec.Value, dash(rec.Zone), rec.Why)
	}
	b.WriteString("\n")
	return b.String()
}

// dnsRecords derives what has to exist from the document.
func dnsRecords(s v1alpha1.ClusterSpec) []Record {
	var out []Record
	seen := map[string]bool{}
	add := func(rec Record) {
		// The value is part of the identity: one name resolving to several
		// addresses is a real request -- it is exactly how a node-ips gateway
		// with two nodes is spelled -- and a key without it silently dropped
		// every A record after the first.
		key := rec.FQDN + "/" + rec.Type + "/" + rec.Value
		if rec.FQDN == "" || rec.Value == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, rec)
	}

	// The registration address, when it is a name rather than a literal. Every
	// node joins through it, so it is the one record nothing works without.
	if a := strings.TrimSpace(s.Topology.RegistrationAddress); a != "" && !isAddress(a) {
		value := a
		if v := s.Topology.VIP; v != nil && v.Address != "" {
			value = v.Address
		}
		add(Record{FQDN: a, Type: "A", Value: value, Why: "every node joins through this name"})
	}

	for _, gw := range s.Gateway.Gateways {
		// A node-ips gateway's address is the nodes' own, and the sheet asks
		// for one A record per node under the same name: several answers for
		// one name is how DNS spreads traffic across them, and a node that
		// leaves takes exactly its record with it.
		addrs := []string{gw.Address}
		if gw.Exposure == v1alpha1.ExposureNodeIPs {
			addrs = gatewayNodeAddresses(s, gw)
		}
		if len(addrs) == 0 || addrs[0] == "" {
			continue
		}
		for _, addr := range addrs {
			for _, l := range gw.Listeners {
				name := strings.TrimSpace(l.Hostname)
				if name == "" {
					continue
				}
				why := fmt.Sprintf("gateway %s, listener %s", gw.Name, l.Name)
				add(Record{FQDN: name, Type: "A", Value: addr, Zone: gw.Zone, Why: why})
			}
			// A wildcard for the domain suffix covers whatever the applications
			// publish later, which is what stops a DNS request per deployment.
			if s.Gateway.DomainSuffix != "" {
				add(Record{
					FQDN: "*." + s.Gateway.DomainSuffix, Type: "A", Value: addr, Zone: gw.Zone,
					Why: fmt.Sprintf("applications published on gateway %s", gw.Name),
				})
			}
		}
	}

	for _, rec := range s.Gateway.DNS.ExtraRecords {
		t := rec.Type
		if t == "" {
			t = "A"
		}
		add(Record{FQDN: rec.FQDN, Type: t, Value: rec.Value, Zone: rec.Zone, Why: "requested by the document"})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].FQDN < out[j].FQDN })
	return out
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

type facts struct{ os, kernel, arch string }

// nodeFacts recovers what each node turned out to be from its probe results.
func nodeFacts(r *Run) map[string]facts {
	out := map[string]facts{}
	for _, e := range r.Events {
		if e.Kind != event.KindProbe || e.Node == "" {
			continue
		}
		f := out[e.Node]
		switch e.Code {
		case "PF-101":
			f.os = e.Detail
		case "PF-102":
			f.arch = e.Detail
		case "PF-103":
			// "kernel 6.8.0-107-generic; what it can actually do is ..."
			if _, rest, ok := strings.Cut(e.Detail, "kernel "); ok {
				f.kernel = strings.TrimSpace(strings.SplitN(rest, ";", 2)[0])
			}
		}
		out[e.Node] = f
	}
	return out
}

// probeEvents returns the probe results whose code starts with prefix, in code
// then node order so two reports of the same run read the same way.
func probeEvents(r *Run, prefix string) []event.Event {
	var out []event.Event
	for _, e := range r.Events {
		if e.Kind == event.KindProbe && strings.HasPrefix(e.Code, prefix) {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Code != out[j].Code {
			return out[i].Code < out[j].Code
		}
		return out[i].Node < out[j].Node
	})
	return out
}

// severityWord renders an outcome for a reader rather than for a machine.
func severityWord(e event.Event) string {
	switch e.Status {
	case event.StatusBlocked:
		return "**blocking**"
	case event.StatusFailed:
		return "worth reading"
	case event.StatusSkipped:
		return "not applicable"
	}
	return "passed"
}

func nodeSuffix(e event.Event) string {
	if e.Node == "" {
		return ""
	}
	return " on " + e.Node
}

// allNodes flattens the document's two lists with the role each one implies.
//
// The role has to be filled in here: a node's entry usually leaves it empty and
// says what it is by which list it appears in, so flattening without this
// reports every agent as a server.
func allNodes(s v1alpha1.ClusterSpec) []v1alpha1.NodeSpec {
	var out []v1alpha1.NodeSpec
	for _, n := range s.Topology.Servers {
		if n.Role == "" {
			n.Role = v1alpha1.RoleServer
		}
		out = append(out, n)
	}
	for _, n := range s.Topology.Agents {
		if n.Role == "" {
			n.Role = v1alpha1.RoleAgent
		}
		out = append(out, n)
	}
	return out
}

func servesTLS(s v1alpha1.ClusterSpec) bool {
	for _, gw := range s.Gateway.Gateways {
		for _, l := range gw.Listeners {
			if l.Protocol == v1alpha1.ListenerHTTPS || l.Protocol == v1alpha1.ListenerTLSPassthrough {
				return true
			}
		}
	}
	return false
}

// isAddress reports whether a value is a literal rather than a name.
func isAddress(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' && r != ':' &&
			!(r >= 'a' && r <= 'f') && !(r >= 'A' && r <= 'F') {
			return false
		}
	}
	return strings.ContainsAny(s, ".:")
}

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def + " (default)"
	}
	return s
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "|", "\\|")), " ")
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// gatewayNodeAddresses resolves which addresses a node-ips gateway answers on.
//
// The same resolution the gateway phase uses: the document's list when it gives
// one, every node otherwise, with the advertised nodeIP winning over the SSH
// address. Duplicated here rather than imported because report must stay
// buildable from a run directory alone.
func gatewayNodeAddresses(s v1alpha1.ClusterSpec, gw v1alpha1.Gateway) []string {
	if len(gw.NodeIPs) > 0 {
		return gw.NodeIPs
	}
	var out []string
	for _, n := range append(append([]v1alpha1.NodeSpec{},
		s.Topology.Servers...), s.Topology.Agents...) {
		addr := n.NodeIP
		if addr == "" {
			addr = n.Host
		}
		out = append(out, addr)
	}
	return out
}
