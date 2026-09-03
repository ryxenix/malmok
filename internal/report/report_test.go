package report

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ryxenix/malmok/api/v1alpha1"
	"github.com/ryxenix/malmok/internal/event"
	"github.com/ryxenix/malmok/internal/plan"
	"github.com/ryxenix/malmok/internal/spec"
)

func testSpec() v1alpha1.ClusterSpec {
	return v1alpha1.ClusterSpec{
		APIVersion: v1alpha1.APIVersion,
		Kind:       v1alpha1.KindSpec,
		Metadata:   v1alpha1.Metadata{Name: "acme-01", Profile: v1alpha1.ProfileName("onprem-dmz")},
		Network:    v1alpha1.NetworkSpec{Mode: v1alpha1.NetworkOnline},
		Topology: v1alpha1.TopologySpec{
			RegistrationAddress: "k8s-api.acme.internal",
			VIP:                 &v1alpha1.VIPSpec{Address: "10.10.0.10", Mode: "arp"},
			Servers:             []v1alpha1.NodeSpec{{Host: "10.10.0.11"}},
			Agents:              []v1alpha1.NodeSpec{{Host: "10.10.0.21"}},
		},
		Kubernetes: v1alpha1.KubernetesSpec{
			Version: "v1.34.10+rke2r1",
			Dataplane: v1alpha1.DataplaneSpec{
				Preset: "cilium-gw", LoadBalancerPool: []string{"10.10.0.240/29"},
			},
		},
		Gateway: v1alpha1.GatewaySpec{
			DomainSuffix: "acme.internal",
			Gateways: []v1alpha1.Gateway{{
				Name: "public", Address: "10.10.0.240",
				Listeners: []v1alpha1.ListenerSpec{
					{Name: "https", Protocol: v1alpha1.ListenerHTTPS, Port: 443, Hostname: "api.acme.internal"},
				},
			}},
		},
	}
}

// writeRun lays out a run directory the way a real one looks.
func writeRun(t *testing.T, s v1alpha1.ClusterSpec, events []event.Event, p *plan.Plan) string {
	t.Helper()
	dir := t.TempDir()

	if err := spec.Save(spec.SnapshotPath(dir), s); err != nil {
		t.Fatal(err)
	}

	f, err := os.Create(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	w := event.NewWriter(f, "01JBQ8F2K3M5N7P9R1S3T5V7W9")
	for _, e := range events {
		if _, err := w.Emit(e); err != nil {
			t.Fatalf("the fixture is not a valid event: %v", err)
		}
	}
	if p != nil {
		if err := SavePlan(dir, p); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func probe(code, node string, status event.Status, detail string) event.Event {
	return event.Event{
		Kind: event.KindProbe, Phase: "preflight", Step: code,
		Code: code, Node: node, Status: status, Detail: detail,
	}
}

func load(t *testing.T, dir string) *Run {
	t.Helper()
	r, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A node's entry usually leaves the role empty and says what it is by which
// list it appears in. Flattening without that reports every agent as a server.
func TestAgentsAreNotReportedAsServers(t *testing.T) {
	dir := writeRun(t, testSpec(), []event.Event{
		probe("PF-101", "10.10.0.11", event.StatusOK, "Ubuntu 24.04.3 LTS (ubuntu)"),
	}, nil)

	got := Audit(load(t, dir))
	for _, want := range []string{"| 10.10.0.11 | server |", "| 10.10.0.21 | agent |"} {
		if !strings.Contains(got, want) {
			t.Errorf("the node table is missing %q:\n%s", want, section(got, "### Nodes"))
		}
	}
}

// Every section appears whether or not there is data for it. A section quietly
// omitted reads as a section that passed.
func TestEverySectionIsPresent(t *testing.T) {
	dir := writeRun(t, testSpec(), []event.Event{
		probe("PF-101", "10.10.0.11", event.StatusOK, "Ubuntu 24.04.3 LTS"),
	}, nil)

	got := Audit(load(t, dir))
	for _, want := range []string{
		"## Configuration", "## Downgrades", "## Preflight",
		"## Certificates", "## Network", "## Security", "## Constraints",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the report has no %s section", want)
		}
	}
}

// The downgrade section is the one §7 singles out: requested, installed, what
// triggered it and on which nodes.
func TestDowngradeHistoryCarriesItsTrigger(t *testing.T) {
	p := &plan.Plan{
		Requested: plan.Requested{Dataplane: "cilium-gw", Storage: "longhorn"},
		Actual:    plan.Actual{Dataplane: "canal-traefik", Storage: "longhorn"},
		Downgrades: []plan.Downgrade{{
			Code: "DG-001", From: "cilium-gw", To: "canal-traefik",
			TriggeredBy: []string{"PF-204"}, Nodes: []string{"10.10.0.21"},
			Detail: "the kernel refused to load an eBPF program",
		}},
	}
	dir := writeRun(t, testSpec(), []event.Event{
		probe("PF-204", "10.10.0.21", event.StatusBlocked, "eBPF program load denied"),
	}, p)

	got := section(Audit(load(t, dir)), "## Downgrades")
	for _, want := range []string{"cilium-gw", "canal-traefik", "PF-204", "10.10.0.21",
		"refused to load an eBPF program"} {
		if !strings.Contains(got, want) {
			t.Errorf("the downgrade section is missing %q:\n%s", want, got)
		}
	}
}

// A run with no plan still has probes worth reporting, and it has to say why
// the section is empty rather than imply nothing was downgraded.
func TestMissingPlanIsSaidOutLoud(t *testing.T) {
	dir := writeRun(t, testSpec(), []event.Event{
		probe("PF-101", "10.10.0.11", event.StatusOK, "Ubuntu"),
	}, nil)

	got := section(Audit(load(t, dir)), "## Downgrades")
	if !strings.Contains(got, "No plan was recorded") {
		t.Errorf("a missing plan reads as no downgrades:\n%s", got)
	}
}

// A reader who cannot tell "scanned and clean" from "never scanned" has been
// misled by the report rather than informed by it.
func TestUnimplementedWorkIsNamed(t *testing.T) {
	dir := writeRun(t, testSpec(), nil, nil)
	got := section(Audit(load(t, dir)), "## Security")

	for _, want := range []string{"CIS scan", "SBOM", "Neither is implemented"} {
		if !strings.Contains(got, want) {
			t.Errorf("the security section does not say what it did not produce (%q):\n%s", want, got)
		}
	}
}

// The same finding raised on three nodes is one constraint, not three.
func TestConstraintsAreDeduplicated(t *testing.T) {
	dir := writeRun(t, testSpec(), []event.Event{
		probe("PF-401", "10.10.0.11", event.StatusFailed, "the data directory shares the root filesystem"),
		probe("PF-401", "10.10.0.21", event.StatusFailed, "the data directory shares the root filesystem"),
	}, nil)

	got := section(Audit(load(t, dir)), "## Constraints")
	if n := strings.Count(got, "data directory shares the root filesystem"); n != 1 {
		t.Errorf("the same constraint appears %d times:\n%s", n, got)
	}
}

// A blocking check and an advisory are different outcomes and the report has to
// say which happened.
func TestFindingsDistinguishBlockingFromAdvisory(t *testing.T) {
	dir := writeRun(t, testSpec(), []event.Event{
		probe("PF-204", "10.10.0.21", event.StatusBlocked, "eBPF program load denied"),
		probe("PF-401", "10.10.0.11", event.StatusFailed, "the data directory shares the root filesystem"),
		probe("PF-101", "10.10.0.11", event.StatusOK, "Ubuntu 24.04.3 LTS"),
	}, nil)

	got := section(Audit(load(t, dir)), "## Preflight")
	if !strings.Contains(got, "1 passed, 1 worth reading, 1 blocking") {
		t.Errorf("the tally does not separate the outcomes:\n%s", got)
	}
	if !strings.Contains(got, "**PF-204** **blocking**") {
		t.Errorf("the blocking finding is not marked as such:\n%s", got)
	}
}

// Evidence is why the raw output is kept: the audit report has to be able to
// show what was actually seen.
func TestEvidenceReachesTheReport(t *testing.T) {
	e := probe("PF-204", "10.10.0.21", event.StatusBlocked, "eBPF program load denied")
	e.Evidence = "bpf_prog_load errno=1 EPERM"
	dir := writeRun(t, testSpec(), []event.Event{e}, nil)

	if got := Audit(load(t, dir)); !strings.Contains(got, "bpf_prog_load errno=1 EPERM") {
		t.Error("the evidence was dropped from the report")
	}
}

// PV-002 is the decisive certificate check, and the report has to lead with it
// rather than bury it in a table.
func TestCertificateSectionLeadsWithTheStrictProfile(t *testing.T) {
	t.Run("passing", func(t *testing.T) {
		dir := writeRun(t, testSpec(), []event.Event{
			probe("PV-002", "10.10.0.240:443", event.StatusOK, "the chain verifies on its own"),
		}, nil)
		got := section(Audit(load(t, dir)), "## Certificates")
		if !strings.Contains(got, "**The served chain verifies under the strict profile**") {
			t.Errorf("the section does not lead with PV-002:\n%s", got)
		}
		if !strings.Contains(got, "Java, Go, curl and Python") {
			t.Errorf("the section does not say what passing means:\n%s", got)
		}
	})

	t.Run("failing", func(t *testing.T) {
		dir := writeRun(t, testSpec(), []event.Event{
			probe("PV-002", "10.10.0.240:443", event.StatusBlocked, "the chain stops before its root"),
		}, nil)
		got := section(Audit(load(t, dir)), "## Certificates")
		if !strings.Contains(got, "does not verify under the strict profile") {
			t.Errorf("a failing PV-002 is not called out:\n%s", got)
		}
	})
}

// A listener that terminates TLS and was never verified is not the same as a
// cluster with nothing to verify.
func TestUnverifiedTLSIsNotReportedAsNothingToDo(t *testing.T) {
	dir := writeRun(t, testSpec(), nil, nil) // the document has an HTTPS listener
	got := section(Audit(load(t, dir)), "## Certificates")
	if !strings.Contains(got, "nothing here has been confirmed") {
		t.Errorf("an unverified HTTPS listener reads as nothing to verify:\n%s", got)
	}

	plain := testSpec()
	plain.Gateway.Gateways[0].Listeners = []v1alpha1.ListenerSpec{
		{Name: "http", Protocol: v1alpha1.ListenerHTTP, Port: 80},
	}
	got = section(Audit(load(t, writeRun(t, plain, nil, nil))), "## Certificates")
	if !strings.Contains(got, "nothing to verify") {
		t.Errorf("a cluster with no TLS listener is reported as unverified:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// The DNS record sheet
// ---------------------------------------------------------------------------

// The records have to be requested before the install: a customer's DNS change
// takes days, which is why PF-612 asks for a pinned gateway address.
func TestDNSRecordSheet(t *testing.T) {
	s := testSpec()
	s.Gateway.DNS.ExtraRecords = []v1alpha1.DNSRecord{
		{FQDN: "registry.acme.internal", Type: "A", Value: "10.10.0.30"},
	}
	dir := writeRun(t, s, nil, nil)

	got := DNSRecords(load(t, dir))
	for _, want := range []string{
		"k8s-api.acme.internal",  // the join address, which nothing works without
		"api.acme.internal",      // the listener hostname
		"*.acme.internal",        // whatever applications publish later
		"registry.acme.internal", // asked for by the document
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the sheet is missing %q:\n%s", want, got)
		}
	}
	// The join address resolves to the VIP, not to a node: ADR-008.
	if !strings.Contains(got, "| k8s-api.acme.internal | A | 10.10.0.10 |") {
		t.Errorf("the join address does not point at the VIP:\n%s", got)
	}
}

// A literal registration address needs no record, and inventing one would ask
// the customer to create something meaningless.
func TestLiteralRegistrationAddressNeedsNoRecord(t *testing.T) {
	s := testSpec()
	s.Topology.RegistrationAddress = "10.10.0.10"
	got := DNSRecords(load(t, writeRun(t, s, nil, nil)))

	if strings.Contains(got, "| 10.10.0.10 | A |") {
		t.Errorf("a record was requested for a literal address:\n%s", got)
	}
}

// A gateway with no pinned address has nothing to point a record at, and
// guessing would send the customer a record for the wrong endpoint.
func TestNoRecordsWithoutAPinnedAddress(t *testing.T) {
	s := testSpec()
	s.Topology.RegistrationAddress = "10.10.0.10"
	s.Gateway.Gateways[0].Address = ""

	if got := DNSRecords(load(t, writeRun(t, s, nil, nil))); got != "" {
		t.Errorf("a sheet was produced with nothing to point at:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// Artifacts
// ---------------------------------------------------------------------------

func TestWriteProducesEveryArtifact(t *testing.T) {
	dir := writeRun(t, testSpec(), []event.Event{
		probe("PF-101", "10.10.0.11", event.StatusOK, "Ubuntu"),
	}, nil)

	written, err := Write(load(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	// The report, the DNS sheet and the machine-readable handoff.
	if len(written) != 3 {
		t.Fatalf("wrote %v", written)
	}
	for _, p := range written {
		if filepath.Dir(p) != filepath.Join(dir, ArtifactsDir) {
			t.Errorf("%s is not in the artifacts directory", p)
		}
		body, err := os.ReadFile(p)
		if err != nil || len(body) == 0 {
			t.Errorf("%s is empty: %v", p, err)
		}
	}
}

// Two reports of the same run have to read the same way, or a diff between them
// is noise and nobody diffs them again.
func TestReportIsStable(t *testing.T) {
	dir := writeRun(t, testSpec(), []event.Event{
		probe("PF-301", "10.10.0.21", event.StatusOK, "no SELinux"),
		probe("PF-301", "10.10.0.11", event.StatusOK, "no SELinux"),
		probe("PF-401", "10.10.0.21", event.StatusFailed, "shares the root filesystem"),
		probe("PF-401", "10.10.0.11", event.StatusFailed, "shares the root filesystem"),
	}, nil)

	run := load(t, dir)
	first := Audit(run)
	for range 5 {
		if Audit(run) != first {
			t.Fatal("two renderings of one run differ")
		}
	}
}

// The timestamps come from the events, so a report of an old run says when that
// run happened rather than when the report was produced.
func TestTimesComeFromTheRun(t *testing.T) {
	dir := writeRun(t, testSpec(), []event.Event{
		probe("PF-101", "10.10.0.11", event.StatusOK, "Ubuntu"),
	}, nil)

	run := load(t, dir)
	if run.StartedAt.IsZero() || run.EndedAt.IsZero() {
		t.Fatal("the run has no times")
	}
	if time.Since(run.StartedAt) > time.Hour {
		t.Errorf("the run time is %s, which is not from the fixture", run.StartedAt)
	}
}

// section returns one heading's text, for assertions that should not match
// something in a different part of the report.
func section(report, heading string) string {
	i := strings.Index(report, heading)
	if i < 0 {
		return ""
	}
	rest := report[i+len(heading):]
	if j := strings.Index(rest, "\n## "); j >= 0 {
		return rest[:j]
	}
	return rest
}

// A node-ips gateway's DNS request is several A records under one name -- that
// is how DNS spreads traffic across the nodes, and a node that leaves takes
// exactly its record with it. The dedup key used to be FQDN/Type, which
// silently dropped every record after the first.
func TestDNSRecordsForANodeIPGateway(t *testing.T) {
	s := v1alpha1.ClusterSpec{
		Topology: v1alpha1.TopologySpec{
			Servers: []v1alpha1.NodeSpec{{Host: "10.0.0.11"}},
			Agents:  []v1alpha1.NodeSpec{{Host: "10.0.0.21", NodeIP: "10.0.0.121"}},
		},
		Gateway: v1alpha1.GatewaySpec{
			DomainSuffix: "acme.internal",
			Gateways: []v1alpha1.Gateway{{
				Name: "public", Exposure: v1alpha1.ExposureNodeIPs,
				Listeners: []v1alpha1.ListenerSpec{{
					Name: "https", Protocol: v1alpha1.ListenerHTTPS, Port: 443,
					Hostname: "apps.acme.internal",
				}},
			}},
		},
	}

	var hostRecords, wildcards []string
	for _, rec := range dnsRecords(s) {
		switch rec.FQDN {
		case "apps.acme.internal":
			hostRecords = append(hostRecords, rec.Value)
		case "*.acme.internal":
			wildcards = append(wildcards, rec.Value)
		}
	}
	for name, got := range map[string][]string{
		"apps.acme.internal": hostRecords, "*.acme.internal": wildcards,
	} {
		if len(got) != 2 || got[0] != "10.0.0.11" || got[1] != "10.0.0.121" {
			t.Errorf("%s resolves to %v, want both node addresses", name, got)
		}
	}
}
