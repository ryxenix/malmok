package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ryxen/malmok/api/v1alpha1"
	"github.com/ryxen/malmok/internal/event"
	"github.com/ryxen/malmok/internal/plan"
	"github.com/ryxen/malmok/internal/platform"
)

// runEnded is the terminal event the engine writes for every run.
func runEnded(status event.Status) event.Event {
	e := event.Event{Kind: event.KindRun, Status: status, Detail: "run completed"}
	if status == event.StatusFailed {
		e.Code, e.Detail = "EX-101", "phase failed"
	}
	return e
}

func phaseOK(name string) event.Event {
	return event.Event{Kind: event.KindPhase, Phase: name, Status: event.StatusOK}
}

// The whole point of the file: a consumer reads the cluster, its machines and
// their durable identities out of one JSON document, without parsing markdown.
func TestHandoffDescribesTheCluster(t *testing.T) {
	tests := []struct {
		name   string
		spec   func() v1alpha1.ClusterSpec
		events []event.Event
		plan   *plan.Plan
		check  func(t *testing.T, h Handoff)
	}{
		{
			name: "a successful build",
			spec: testSpec,
			events: []event.Event{
				probe("PF-109", "10.10.0.11", event.StatusOK, "9e107d9d-372b-4a6e-b2f8-4c6e1b2f8c6e"),
				probe("PF-109", "10.10.0.21", event.StatusOK, "0b1c2d3e-4f5a-6b7c-8d9e-0f1a2b3c4d5e"),
				phaseOK(platform.Phase),
				runEnded(event.StatusOK),
			},
			plan: &plan.Plan{
				Requested: plan.Requested{Dataplane: v1alpha1.DataplaneCiliumGW},
				Actual:    plan.Actual{Dataplane: v1alpha1.DataplaneCiliumGW},
			},
			check: func(t *testing.T, h Handoff) {
				if h.APIVersion != HandoffVersion || h.Kind != HandoffKind {
					t.Errorf("schema identity = %s/%s", h.APIVersion, h.Kind)
				}
				if h.Run.Result != ResultSucceeded {
					t.Errorf("result = %s", h.Run.Result)
				}
				if h.Cluster.Name != "acme-01" || h.Cluster.KubernetesVersion != "v1.34.10+rke2r1" {
					t.Errorf("cluster = %+v", h.Cluster)
				}
				if len(h.Nodes) != 2 {
					t.Fatalf("nodes = %+v", h.Nodes)
				}
				if h.Nodes[0].Role != "server" || h.Nodes[1].Role != "agent" {
					t.Errorf("roles = %s, %s", h.Nodes[0].Role, h.Nodes[1].Role)
				}
				if h.Nodes[0].MachineUUID != "9e107d9d-372b-4a6e-b2f8-4c6e1b2f8c6e" ||
					h.Nodes[1].MachineUUID != "0b1c2d3e-4f5a-6b7c-8d9e-0f1a2b3c4d5e" {
					t.Errorf("the measured UUIDs did not reach their nodes: %+v", h.Nodes)
				}
				if h.Kubeconfig == nil || h.Kubeconfig.Node != "10.10.0.11" ||
					h.Kubeconfig.Path != "/etc/rancher/rke2/rke2.yaml" {
					t.Errorf("kubeconfig = %+v", h.Kubeconfig)
				}
				byName := map[string]HandoffComponent{}
				for _, c := range h.Components {
					byName[c.Name] = c
				}
				if byName["rke2"].Version != "v1.34.10+rke2r1" {
					t.Errorf("rke2 = %+v", byName["rke2"])
				}
				if byName["cilium"].Role != "cni+gateway" || byName["cilium"].Version != "" {
					t.Errorf("cilium = %+v (its version is RKE2's decision, not ours)", byName["cilium"])
				}
				if byName["argocd"].ChartVersion != platform.ChartVersion {
					t.Errorf("argocd = %+v", byName["argocd"])
				}
				if h.Dataplane == nil || h.Dataplane.Preset != "cilium-gw" || h.Dataplane.Downgraded {
					t.Errorf("dataplane = %+v", h.Dataplane)
				}
			},
		},
		{
			// An inventory needs "this run failed" at least as much as a
			// success; a missing file would read as a missing run.
			name:   "a failed build still says how it ended",
			spec:   testSpec,
			events: []event.Event{runEnded(event.StatusFailed)},
			check: func(t *testing.T, h Handoff) {
				if h.Run.Result != ResultFailed {
					t.Errorf("result = %s", h.Run.Result)
				}
			},
		},
		{
			// No terminal event means the process died mid-run. That is
			// neither verdict, and inventing one would bind the consumer to
			// a guess.
			name:   "a run that never got to say how it ended",
			spec:   testSpec,
			events: []event.Event{probe("PF-101", "10.10.0.11", event.StatusOK, "Ubuntu")},
			check: func(t *testing.T, h Handoff) {
				if h.Run.Result != ResultIncomplete {
					t.Errorf("result = %s", h.Run.Result)
				}
			},
		},
		{
			// A skipped PF-109 explains why there is no UUID; that sentence
			// must not end up where a consumer expects an identifier.
			name: "a node without DMI omits the UUID",
			spec: testSpec,
			events: []event.Event{
				probe("PF-109", "10.10.0.11", event.StatusSkipped, "product_uuid could not be read"),
				runEnded(event.StatusOK),
			},
			check: func(t *testing.T, h Handoff) {
				if h.Nodes[0].MachineUUID != "" {
					t.Errorf("machineUUID = %q, want it absent", h.Nodes[0].MachineUUID)
				}
			},
		},
		{
			// The caller's own name for the machine -- a Proxmox VMID --
			// passes through untouched, node and cluster level alike.
			name: "annotations pass through",
			spec: func() v1alpha1.ClusterSpec {
				s := testSpec()
				s.Metadata.Annotations = map[string]string{"customer": "acme"}
				s.Topology.Servers[0].Annotations = map[string]string{
					"platform.ryxen.dev/proxmox-vmid": "104",
				}
				return s
			},
			events: []event.Event{runEnded(event.StatusOK)},
			check: func(t *testing.T, h Handoff) {
				if h.Cluster.Annotations["customer"] != "acme" {
					t.Errorf("cluster annotations = %+v", h.Cluster.Annotations)
				}
				if h.Nodes[0].Annotations["platform.ryxen.dev/proxmox-vmid"] != "104" {
					t.Errorf("node annotations = %+v", h.Nodes[0].Annotations)
				}
			},
		},
		{
			// After a downgrade the handoff reports what actually carries
			// traffic, not what was asked for.
			name: "a downgraded dataplane reports the actual preset",
			spec: testSpec,
			events: []event.Event{
				runEnded(event.StatusOK),
			},
			plan: &plan.Plan{
				Requested:  plan.Requested{Dataplane: v1alpha1.DataplaneCiliumGW},
				Actual:     plan.Actual{Dataplane: v1alpha1.DataplaneCanalTraefik},
				Downgrades: []plan.Downgrade{{Code: "DG-001"}},
			},
			check: func(t *testing.T, h Handoff) {
				if h.Dataplane == nil || h.Dataplane.Preset != "canal-traefik" || !h.Dataplane.Downgraded {
					t.Errorf("dataplane = %+v", h.Dataplane)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeRun(t, tc.spec(), tc.events, tc.plan)
			tc.check(t, BuildHandoff(load(t, dir)))
		})
	}
}

// Nothing secret leaves through this file. The spec's SSH block carries
// SourceRefs and, under --allow-literal-secrets, may carry literal values;
// none of it belongs to an inventory, and this pins the whole document --
// every field, present and future -- to that rule.
func TestHandoffCarriesNoSecrets(t *testing.T) {
	s := testSpec()
	s.Topology.Servers[0].SSH = v1alpha1.SSHSpec{
		User:           "k8s",
		Password:       "literal://hunter2-ssh",
		BecomePassword: "env://SUDO_SECRET",
		PrivateKey:     "file:///home/k8s/.ssh/id_ed25519",
	}
	dir := writeRun(t, s, []event.Event{runEnded(event.StatusOK)}, nil)

	body, err := json.Marshal(BuildHandoff(load(t, dir)))
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"hunter2", "SUDO_SECRET", "id_ed25519", "literal://", "env://", "file://", "password"} {
		if strings.Contains(strings.ToLower(string(body)), strings.ToLower(leak)) {
			t.Errorf("the handoff leaks %q:\n%s", leak, body)
		}
	}
}

// WriteHandoff is what report.Write calls; the file lands beside the other
// artifacts and reads back as the same document.
func TestHandoffIsWrittenAsAnArtifact(t *testing.T) {
	dir := writeRun(t, testSpec(), []event.Event{runEnded(event.StatusOK)}, nil)

	p, err := WriteHandoff(load(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, ArtifactsDir, HandoffFile); p != want {
		t.Fatalf("wrote to %s, want %s", p, want)
	}
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var h Handoff
	if err := json.Unmarshal(body, &h); err != nil {
		t.Fatalf("the file does not read back as a handoff: %v", err)
	}
	if h.Run.Result != ResultSucceeded || len(h.Nodes) != 2 {
		t.Errorf("read back %+v", h)
	}
}
