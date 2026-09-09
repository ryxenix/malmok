package plan

import (
	"errors"
	"testing"
	"time"

	"github.com/ryxenix/malmok/api/v1alpha1"
	"github.com/ryxenix/malmok/internal/codes"
	"github.com/ryxenix/malmok/internal/preflight"
)

// The generator is a pure function, so the table is the whole test: no cluster,
// no SSH, no clock. The tests here are table-driven for that reason.

func node(host string, role v1alpha1.NodeRole, failed ...string) preflight.NodeCapability {
	c := preflight.NodeCapability{
		Host: host, Role: role, Arch: "amd64",
		Probes:      map[string]preflight.ProbeResult{},
		CollectedAt: time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC),
	}
	// Everything the generator consults passes unless the test says otherwise.
	for _, id := range append(append([]string{}, preflight.EBPFProbes...),
		append(preflight.NetfilterProbes, preflight.LonghornProbes...)...) {
		c.Probes[id] = preflight.ProbeResult{ID: id, Status: preflight.StatusPass}
	}
	for _, id := range failed {
		c.Probes[id] = preflight.ProbeResult{
			ID: id, Status: preflight.StatusFail, Severity: codes.SeverityDegrade,
			Detail: "simulated failure",
		}
	}
	return c
}

func blocking(host, id string) preflight.NodeCapability {
	c := node(host, v1alpha1.RoleServer)
	c.Probes[id] = preflight.ProbeResult{
		ID: id, Status: preflight.StatusFail, Severity: codes.SeverityBlock,
		Detail: "hard stop",
	}
	return c
}

func spec(preset v1alpha1.DataplanePreset, storage v1alpha1.StorageDriver,
	policy v1alpha1.DowngradePolicy) v1alpha1.ClusterSpec {
	return v1alpha1.ClusterSpec{
		Kubernetes: v1alpha1.KubernetesSpec{
			Dataplane: v1alpha1.DataplaneSpec{
				Preset: preset, Fallback: v1alpha1.DataplaneCanalTraefik,
				DowngradePolicy: policy,
			},
		},
		Storage: v1alpha1.StorageSpec{Driver: storage},
		Gateway: v1alpha1.GatewaySpec{
			Gateways: []v1alpha1.Gateway{{Name: "public", Address: "10.10.20.241"}},
		},
	}
}

func TestGenerate(t *testing.T) {
	tests := []struct {
		name string
		spec v1alpha1.ClusterSpec
		caps []preflight.NodeCapability
		opts Options

		wantDataplane v1alpha1.DataplanePreset
		wantStorage   v1alpha1.StorageDriver
		wantCodes     []string // downgrade codes, in order
		wantExcluded  []string
		wantErr       any // a pointer to the error type expected
	}{
		{
			name: "everything supported keeps the requested configuration",
			spec: spec(v1alpha1.DataplaneCiliumGW, v1alpha1.StorageLonghorn, v1alpha1.DowngradeAuto),
			caps: []preflight.NodeCapability{
				node("10.0.0.1", v1alpha1.RoleServer),
				node("10.0.0.2", v1alpha1.RoleAgent),
			},
			wantDataplane: v1alpha1.DataplaneCiliumGW,
			wantStorage:   v1alpha1.StorageLonghorn,
		},
		{
			// The decisive probe. A node whose kernel loads no eBPF program
			// cannot run Cilium whatever its config symbols say.
			name: "server without eBPF downgrades the CNI and the gateway",
			spec: spec(v1alpha1.DataplaneCiliumGW, v1alpha1.StorageLocalPath, v1alpha1.DowngradeAuto),
			caps: []preflight.NodeCapability{
				node("10.0.0.1", v1alpha1.RoleServer, "PF-204"),
				node("10.0.0.2", v1alpha1.RoleAgent),
			},
			wantDataplane: v1alpha1.DataplaneCanalTraefik,
			wantStorage:   v1alpha1.StorageLocalPath,
			wantCodes:     []string{"DG-001", "DG-002"},
		},
		{
			// DG-003: a smaller correct cluster beats a whole one quietly on
			// the fallback.
			name: "agent-only failure excludes the node and keeps the preset",
			spec: spec(v1alpha1.DataplaneCiliumGW, v1alpha1.StorageLocalPath, v1alpha1.DowngradeAuto),
			caps: []preflight.NodeCapability{
				node("10.0.0.1", v1alpha1.RoleServer),
				node("10.0.0.2", v1alpha1.RoleAgent, "PF-204"),
			},
			wantDataplane: v1alpha1.DataplaneCiliumGW,
			wantStorage:   v1alpha1.StorageLocalPath,
			wantExcluded:  []string{"10.0.0.2"},
		},
		{
			name: "every node failing falls back rather than excluding all of them",
			spec: spec(v1alpha1.DataplaneCiliumGW, v1alpha1.StorageLocalPath, v1alpha1.DowngradeAuto),
			caps: []preflight.NodeCapability{
				node("10.0.0.1", v1alpha1.RoleAgent, "PF-204"),
				node("10.0.0.2", v1alpha1.RoleAgent, "PF-202"),
			},
			wantDataplane: v1alpha1.DataplaneCanalTraefik,
			wantCodes:     []string{"DG-001", "DG-002"},
		},
		{
			name: "cilium-traefik downgrade does not report a gateway change",
			spec: spec(v1alpha1.DataplaneCiliumTraefik, v1alpha1.StorageLocalPath, v1alpha1.DowngradeAuto),
			caps: []preflight.NodeCapability{
				node("10.0.0.1", v1alpha1.RoleServer, "PF-204"),
			},
			wantDataplane: v1alpha1.DataplaneCanalTraefik,
			wantCodes:     []string{"DG-001"},
		},
		{
			name: "missing Longhorn prerequisites drop storage to local-path",
			spec: spec(v1alpha1.DataplaneCiliumGW, v1alpha1.StorageLonghorn, v1alpha1.DowngradeAuto),
			caps: []preflight.NodeCapability{
				node("10.0.0.1", v1alpha1.RoleServer),
				node("10.0.0.2", v1alpha1.RoleAgent, "PF-404"),
			},
			wantDataplane: v1alpha1.DataplaneCiliumGW,
			wantStorage:   v1alpha1.StorageLocalPath,
			wantCodes:     []string{"DG-020"},
		},
		{
			// Customer-facing profiles set confirm precisely so this stops.
			name: "confirm policy refuses to downgrade on its own",
			spec: spec(v1alpha1.DataplaneCiliumGW, v1alpha1.StorageLocalPath, v1alpha1.DowngradeConfirm),
			caps: []preflight.NodeCapability{
				node("10.0.0.1", v1alpha1.RoleServer, "PF-204"),
			},
			wantErr: &ErrNeedsApproval{},
		},
		{
			name: "confirm policy proceeds once approved",
			spec: spec(v1alpha1.DataplaneCiliumGW, v1alpha1.StorageLocalPath, v1alpha1.DowngradeConfirm),
			caps: []preflight.NodeCapability{
				node("10.0.0.1", v1alpha1.RoleServer, "PF-204"),
			},
			opts:          Options{Approve: true},
			wantDataplane: v1alpha1.DataplaneCanalTraefik,
			wantCodes:     []string{"DG-001", "DG-002"},
		},
		{
			name: "forbid policy fails outright",
			spec: spec(v1alpha1.DataplaneCiliumGW, v1alpha1.StorageLocalPath, v1alpha1.DowngradeForbid),
			caps: []preflight.NodeCapability{
				node("10.0.0.1", v1alpha1.RoleServer, "PF-204"),
			},
			wantErr: &ErrForbidden{},
		},
		{
			// block means there is no safe way forward, not "not right now",
			// so nothing is decided until it is fixed.
			name: "a blocking probe stops the plan before any downgrade is considered",
			spec: spec(v1alpha1.DataplaneCiliumGW, v1alpha1.StorageLocalPath, v1alpha1.DowngradeAuto),
			caps: []preflight.NodeCapability{
				blocking("10.0.0.1", "PF-501"),
			},
			wantErr: &ErrBlocked{},
		},
		{
			name: "canal keeps canal",
			spec: spec(v1alpha1.DataplaneCanalTraefik, v1alpha1.StorageLocalPath, v1alpha1.DowngradeAuto),
			caps: []preflight.NodeCapability{
				node("10.0.0.1", v1alpha1.RoleServer),
			},
			wantDataplane: v1alpha1.DataplaneCanalTraefik,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Generate(tc.spec, tc.caps, tc.opts)

			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("expected %T, got a plan", tc.wantErr)
				}
				switch tc.wantErr.(type) {
				case *ErrBlocked:
					var e *ErrBlocked
					if !errors.As(err, &e) {
						t.Fatalf("expected ErrBlocked, got %T: %v", err, err)
					}
				case *ErrNeedsApproval:
					var e *ErrNeedsApproval
					if !errors.As(err, &e) {
						t.Fatalf("expected ErrNeedsApproval, got %T: %v", err, err)
					}
					if e.Plan == nil {
						t.Error("ErrNeedsApproval carries no plan; the operator cannot be shown what to approve")
					}
				case *ErrForbidden:
					var e *ErrForbidden
					if !errors.As(err, &e) {
						t.Fatalf("expected ErrForbidden, got %T: %v", err, err)
					}
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Actual.Dataplane != tc.wantDataplane {
				t.Errorf("dataplane = %s, want %s", got.Actual.Dataplane, tc.wantDataplane)
			}
			if tc.wantStorage != "" && got.Actual.Storage != tc.wantStorage {
				t.Errorf("storage = %s, want %s", got.Actual.Storage, tc.wantStorage)
			}

			var codesGot []string
			for _, d := range got.Downgrades {
				codesGot = append(codesGot, d.Code)
			}
			if !equal(codesGot, tc.wantCodes) {
				t.Errorf("downgrade codes = %v, want %v", codesGot, tc.wantCodes)
			}

			var excluded []string
			for _, e := range got.Excluded {
				excluded = append(excluded, e.Node)
			}
			if !equal(excluded, tc.wantExcluded) {
				t.Errorf("excluded = %v, want %v", excluded, tc.wantExcluded)
			}
		})
	}
}

// A downgrade with no triggering probe and no node list cannot answer the
// question it exists to answer.
func TestEveryDowngradeNamesItsCauseAndNodes(t *testing.T) {
	got, err := Generate(
		spec(v1alpha1.DataplaneCiliumGW, v1alpha1.StorageLonghorn, v1alpha1.DowngradeAuto),
		[]preflight.NodeCapability{
			node("10.0.0.1", v1alpha1.RoleServer, "PF-204"),
			node("10.0.0.2", v1alpha1.RoleAgent, "PF-404"),
		}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Downgrades) == 0 {
		t.Fatal("expected downgrades")
	}
	for _, d := range got.Downgrades {
		if len(d.TriggeredBy) == 0 {
			t.Errorf("%s names no triggering probe", d.Code)
		}
		if len(d.Nodes) == 0 {
			t.Errorf("%s names no nodes", d.Code)
		}
		if d.From == "" || d.To == "" || d.From == d.To {
			t.Errorf("%s records %q -> %q", d.Code, d.From, d.To)
		}
		if _, ok := codes.Lookup(d.Code); !ok {
			t.Errorf("%s is not in the registry", d.Code)
		}
	}
}

// The generator must not consult the world. Running it twice on the same input
// has to produce the same answer.
func TestGenerateIsDeterministic(t *testing.T) {
	s := spec(v1alpha1.DataplaneCiliumGW, v1alpha1.StorageLonghorn, v1alpha1.DowngradeAuto)
	caps := []preflight.NodeCapability{
		node("10.0.0.3", v1alpha1.RoleAgent, "PF-204"),
		node("10.0.0.1", v1alpha1.RoleServer, "PF-204"),
		node("10.0.0.2", v1alpha1.RoleAgent, "PF-404"),
	}

	first, err := Generate(s, caps, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		again, err := Generate(s, caps, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if len(again.Downgrades) != len(first.Downgrades) {
			t.Fatalf("run %d produced %d downgrades, first produced %d",
				i, len(again.Downgrades), len(first.Downgrades))
		}
		for j := range first.Downgrades {
			a, b := first.Downgrades[j], again.Downgrades[j]
			if a.Code != b.Code || !equal(a.TriggeredBy, b.TriggeredBy) || !equal(a.Nodes, b.Nodes) {
				t.Fatalf("run %d differs at downgrade %d:\n %+v\n %+v", i, j, a, b)
			}
		}
	}
}

// A probe that never ran is not a pass: missing evidence must not read as good
// news.
func TestMissingProbeIsNotAPass(t *testing.T) {
	c := node("10.0.0.1", v1alpha1.RoleServer)
	delete(c.Probes, "PF-204")

	got, err := Generate(
		spec(v1alpha1.DataplaneCiliumGW, v1alpha1.StorageLocalPath, v1alpha1.DowngradeAuto),
		[]preflight.NodeCapability{c}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Actual.Dataplane != v1alpha1.DataplaneCanalTraefik {
		t.Errorf("dataplane = %s; a missing eBPF probe was treated as a pass", got.Actual.Dataplane)
	}
}

func TestUnpinnedGatewayAddressIsReported(t *testing.T) {
	s := spec(v1alpha1.DataplaneCiliumGW, v1alpha1.StorageLocalPath, v1alpha1.DowngradeAuto)
	s.Gateway.Gateways = []v1alpha1.Gateway{{Name: "public"}, {Name: "internal", Address: "10.0.0.9"}}

	got, err := Generate(s, []preflight.NodeCapability{node("10.0.0.1", v1alpha1.RoleServer)}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, w := range got.Warnings {
		if w.Code == "PF-612" {
			found = true
			if !contains([]string{w.Detail}, "gateways without a pinned address: public") {
				t.Errorf("warning does not name the gateway: %q", w.Detail)
			}
		}
	}
	if !found {
		t.Errorf("no PF-612 warning: %+v", got.Warnings)
	}
}

func TestNoCapabilitiesIsAnError(t *testing.T) {
	if _, err := Generate(spec(v1alpha1.DataplaneCiliumGW, v1alpha1.StorageLocalPath, ""), nil, Options{}); err == nil {
		t.Error("expected an error when preflight has not run")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// PF-612 asks for an address that can be put in a DNS record before the
// install. A node-ips gateway answers on the nodes' own addresses, so that is
// already true and there is nothing to pin -- the same false premise that made
// PF-708 block a working install.
func TestPF612SkipsANodeIPsGateway(t *testing.T) {
	spec := v1alpha1.ClusterSpec{}
	spec.Gateway.Gateways = []v1alpha1.Gateway{
		{Name: "public", Exposure: v1alpha1.ExposureNodeIPs},
	}
	p := &Plan{}
	p.checkGatewayAddresses(spec)
	for _, w := range p.Warnings {
		if w.Code == "PF-612" {
			t.Errorf("a node-ips gateway is warned about: %s", w.Detail)
		}
	}

	// A gateway that takes an allocated address still has to name one.
	spec.Gateway.Gateways = []v1alpha1.Gateway{
		{Name: "public", Exposure: v1alpha1.ExposureLoadBalancer},
	}
	p = &Plan{}
	p.checkGatewayAddresses(spec)
	found := false
	for _, w := range p.Warnings {
		if w.Code == "PF-612" {
			found = true
		}
	}
	if !found {
		t.Error("an unpinned loadBalancer gateway is not warned about")
	}
}
