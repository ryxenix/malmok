package preflight

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ryxenix/malmok/api/v1alpha1"
	"github.com/ryxenix/malmok/internal/cert"
	"github.com/ryxenix/malmok/internal/codes"
	"github.com/ryxenix/malmok/internal/engine"
	"github.com/ryxenix/malmok/internal/exec"
	"github.com/ryxenix/malmok/internal/nodeprep"
	"github.com/ryxenix/malmok/internal/rke2"
)

// implementedElsewhere are the PF codes this package does not produce, with the
// reason. Anything here is a claim somebody can check.
var implementedElsewhere = map[string]string{
	// The gateway certificate gates live in internal/cert, which must not
	// import this package -- the adapter is FromCert.
	"PF-901": "internal/cert", "PF-902": "internal/cert", "PF-903": "internal/cert",
	"PF-904": "internal/cert", "PF-905": "internal/cert", "PF-906": "internal/cert",
	"PF-910": "internal/cert", "PF-911": "internal/cert", "PF-912": "internal/cert",

	// A gateway with no pinned address is a planning decision, not a
	// measurement, so it is decided where the plan is built.
	"PF-612": "internal/plan",
}

// producible collects every code this package can emit, by running the probes
// rather than by listing them: a list would drift the first time a probe was
// added and the list was not.
func producible(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}

	n, _ := ubuntuProber(t, nil)
	cap := n.Probe(context.Background())
	for id := range cap.Probes {
		out[id] = true
	}
	n.ProbePeers(context.Background(), []string{"10.0.0.12"}, &cap)
	for id := range cap.Probes {
		out[id] = true
	}

	// The cross-node and document-level probes, which take no runner.
	for _, r := range []ProbeResult{
		CheckHomogeneous(nil),
		CheckHostnames(nil),
		CheckClockSkew(nil, time.Second),
		CheckTimezones(nil),
		CheckCIDRs(baseSpec()),
	} {
		out[r.ID] = true
	}

	// The ones that reach the network from this machine.
	p := &Prober{Resolver: fakeResolver{}, Dialer: fakeDialer{}, Timeout: time.Second}
	ctx := context.Background()
	for _, r := range []ProbeResult{
		p.CheckRegistrationAddress(ctx, baseSpec()),
		p.CheckVIPFree(ctx, baseSpec(), nil),
		p.CheckProxyConnect(ctx, baseSpec()),
		p.CheckACME(ctx, baseSpec(), false),
	} {
		out[r.ID] = true
	}
	for _, r := range p.CheckRegistry(ctx, baseSpec(), RegistryCredentials{}) {
		out[r.ID] = true
	}
	for _, r := range CheckPKIMaterial(baseSpec(), PKIMaterial{}, time.Now()) {
		out[r.ID] = true
	}
	return out
}

// Every preflight code in the registry has to be produced by something.
//
// A code defined and never emitted is a check somebody believes exists. The
// registry is the source of truth for what the tool promises, so this test is
// what keeps the promise and the implementation from drifting apart -- the same
// failure the schema and the docs have already had twice.
func TestEveryPreflightCodeIsImplemented(t *testing.T) {
	produced := producible(t)

	var missing []string
	for _, c := range codes.All() {
		if c.Family != codes.FamilyPreflight {
			continue
		}
		if produced[c.ID] {
			continue
		}
		if where, ok := implementedElsewhere[c.ID]; ok {
			t.Logf("%s is implemented in %s", c.ID, where)
			continue
		}
		missing = append(missing, c.ID+" ("+c.Summary+")")
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("%d preflight codes are defined and never produced:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// And nothing emits a code that is not in the registry.
//
// An unregistered code reaches a customer's audit report as an identifier with
// no definition behind it, which is worse than no identifier at all.
func TestNoProbeEmitsAnUnregisteredCode(t *testing.T) {
	for id := range producible(t) {
		if _, ok := codes.Lookup(id); !ok {
			t.Errorf("%s is emitted by a probe and is not defined in internal/codes", id)
		}
	}
}

// The certificate gates have to map onto probe results cleanly, or the adapter
// is where the audit report loses its evidence.
func TestFromCertPreservesEverything(t *testing.T) {
	findings := []cert.Finding{
		{ID: "PF-902", Status: cert.StatusFail, Severity: codes.SeverityBlock,
			Reason: "CHAIN_INCOMPLETE", Detail: "the issuer is missing", Evidence: "CN=Acme CA"},
		{ID: "PF-904", Status: cert.StatusWarn, Severity: codes.SeverityWarn,
			Reason: "CERT_EXPIRING", Detail: "expires in 20 days"},
		{ID: "PF-903", Status: cert.StatusSkip, Severity: codes.SeverityInfo, Detail: "no hostname"},
	}
	got := FromCert(findings)
	if len(got) != len(findings) {
		t.Fatalf("got %d results from %d findings", len(got), len(findings))
	}

	if got[0].Status != StatusFail || got[0].Code != "CHAIN_INCOMPLETE" || got[0].Evidence != "CN=Acme CA" {
		t.Errorf("the blocking finding lost something: %+v", got[0])
	}
	// A warning is a pass that carries a message: the run was not stopped. The
	// severity is what keeps it visible in the report.
	if got[1].Status != StatusPass || got[1].Severity != codes.SeverityWarn {
		t.Errorf("the warning was mapped to %s/%s", got[1].Status, got[1].Severity)
	}
	if got[2].Status != StatusSkip {
		t.Errorf("the skip was mapped to %s", got[2].Status)
	}
}

// A probe that fails must say what to do about it. An identifier and a
// restatement of the check is a dead end for whoever is holding the pager.
func TestFailuresExplainThemselves(t *testing.T) {
	n, _ := ubuntuProber(t, map[string]exec.Result{
		"stat -fc %T /sys/fs/cgroup": {Stdout: "tmpfs\n"},
		"swapon":                     {Stdout: "/swap.img 4G\n"},
		"bpftool feature probe":      {Stdout: "DENIED bpftool restricted\n"},
		"/etc/rancher/rke2":          {Stdout: "/etc/rancher/rke2\n"},
		"ip -br link show":           {Stdout: "lo\ncilium_host\n"},
	})
	cap := n.Probe(context.Background())

	for id, r := range cap.Probes {
		if r.Status != StatusFail {
			continue
		}
		if len(r.Detail) < 40 {
			t.Errorf("%s fails with nothing to act on: %q", id, r.Detail)
		}
		if r.Code == "" {
			t.Errorf("%s fails with no reason code", id)
		}
	}
}

// The marker PF-8xx looks for has to be the one the install phases write.
//
// If the two drift, nothing fails loudly: preflight simply stops recognising
// its own work and starts refusing every run after the first, which is how
// resume and adding a node would break with no error pointing at the cause.
func TestManagedMarkerMatchesWhatTheInstallPhasesWrite(t *testing.T) {
	written := []struct {
		what string
		body string
	}{
		{"rke2 config.yaml", rke2.ServerConfig(
			v1alpha1.NodeSpec{Host: "10.0.0.11"},
			v1alpha1.ClusterSpec{Topology: v1alpha1.TopologySpec{RegistrationAddress: "k8s.acme.internal"}},
			"")},
	}
	for _, w := range written {
		if !strings.Contains(w.body, ManagedMarker) {
			t.Errorf("%s does not carry %q, so PF-802 will not recognise it:\n%s",
				w.what, ManagedMarker, w.body)
		}
	}

	// And the same for every file l0-node-prep writes.
	for _, s := range nodeprep.Steps(&exec.Fake{}, "10.0.0.11",
		v1alpha1.ClusterSpec{Registry: v1alpha1.RegistrySpec{Mode: v1alpha1.RegistryEmbedded}},
		nodeprep.TrustMaterial{}) {

		st := s.(*engine.ShellStep)
		if !strings.Contains(st.Do, ">") || st.Name == "swap" || st.Name == "datadir" {
			continue
		}
		if !strings.Contains(st.Do, ManagedMarker) {
			t.Errorf("l0-node-prep/%s writes a file without %q", st.Name, ManagedMarker)
		}
	}
}
