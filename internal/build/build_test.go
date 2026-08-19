package build

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"platform.ryxen.dev/malmok/api/v1alpha1"
	"platform.ryxen.dev/malmok/internal/event"
	"platform.ryxen.dev/malmok/internal/exec"
	"platform.ryxen.dev/malmok/internal/plan"
)

func downgradingPlan() *plan.Plan {
	return &plan.Plan{
		Requested: plan.Requested{Dataplane: "cilium-gw", Storage: "longhorn"},
		Actual:    plan.Actual{Dataplane: "canal-traefik", Storage: "longhorn"},
		Downgrades: []plan.Downgrade{{
			Code: "DG-001", From: "cilium-gw", To: "canal-traefik",
			TriggeredBy: []string{"PF-204"}, Nodes: []string{"10.10.0.21"},
			Detail: "the kernel refused to load an eBPF program",
		}},
	}
}

func specWith(policy v1alpha1.DowngradePolicy) v1alpha1.ClusterSpec {
	return v1alpha1.ClusterSpec{
		Kubernetes: v1alpha1.KubernetesSpec{
			Dataplane: v1alpha1.DataplaneSpec{Preset: "cilium-gw", DowngradePolicy: policy},
		},
	}
}

// The document says who decides a downgrade, and the wizard has no way to ask.
// Choosing silently would be the tool deciding on the operator's behalf at the
// one moment they are watching the screen.
func TestDowngradePolicyIsHonoured(t *testing.T) {
	tests := []struct {
		name    string
		policy  v1alpha1.DowngradePolicy
		wantErr bool
		wantIn  string
	}{
		{name: "auto proceeds", policy: v1alpha1.DowngradeAuto},
		{
			name: "forbid stops", policy: v1alpha1.DowngradeForbid,
			wantErr: true, wantIn: "forbid",
		},
		{
			// The default for airgapped profiles, because an unnoticed
			// downgrade found months later at a customer site is expensive to
			// explain.
			name: "confirm stops and says how to accept", policy: v1alpha1.DowngradeConfirm,
			wantErr: true, wantIn: "--approve",
		},
		{
			name: "unset is treated as confirm", policy: "",
			wantErr: true, wantIn: "--approve",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := downgradeAllowed(specWith(tc.policy), downgradingPlan())
			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v", err)
			}
			if err == nil {
				return
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("the refusal does not say %q: %v", tc.wantIn, err)
			}
			// Whatever the policy, the operator has to be told what was going
			// to be changed and why.
			for _, want := range []string{"cilium-gw", "canal-traefik", "PF-204"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not name %q: %v", want, err)
				}
			}
		})
	}
}

// A plan that changes nothing is not a decision anybody has to make.
func TestNoDowngradeNeedsNoPolicy(t *testing.T) {
	for _, policy := range []v1alpha1.DowngradePolicy{
		v1alpha1.DowngradeAuto, v1alpha1.DowngradeConfirm, v1alpha1.DowngradeForbid, "",
	} {
		if err := downgradeAllowed(specWith(policy), &plan.Plan{}); err != nil {
			t.Errorf("policy %q refused a plan with no downgrade: %v", policy, err)
		}
	}
}

// A downgrade reaches the screen as an event, not only as a return value: the
// renderer is a consumer of the stream, and a decision that never entered the
// file is one the audit report cannot show.
func TestDowngradesAreEmittedAsDecisions(t *testing.T) {
	var buf bytes.Buffer
	w := event.NewWriter(&buf, "01JBQ8F2K3M5N7P9R1S3T5V7W9")

	for _, d := range downgradingPlan().Downgrades {
		if _, err := w.Emit(event.Event{
			Kind: event.KindDecision, Code: d.Code,
			Detail: d.From + " -> " + d.To + ", triggered by " + strings.Join(d.TriggeredBy, ", "),
		}); err != nil {
			t.Fatalf("the decision event is not valid: %v", err)
		}
	}

	sc := event.NewScanner(bytes.NewReader(buf.Bytes()))
	var seen int
	for sc.Scan() {
		e := sc.Event()
		seen++
		if e.Kind != event.KindDecision || e.Code != "DG-001" {
			t.Errorf("event is %s/%s", e.Kind, e.Code)
		}
	}
	if seen != 1 {
		t.Fatalf("read %d events", seen)
	}
}

// The install cannot plan against nothing. Proceeding without the checks would
// mean deciding the dataplane from the document rather than from the nodes,
// which is the one thing preflight exists to prevent.
func TestInstallRefusesWithoutPreflight(t *testing.T) {
	var buf bytes.Buffer
	// A session that already holds a connection and no findings: the checks
	// never ran, so there is nothing to plan against.
	s := &Session{}
	s.runners.ByHost = map[string]exec.Runner{"10.10.0.11": &exec.Fake{}}

	err := s.Install(context.Background(), specWith(v1alpha1.DowngradeAuto), "pw",
		event.NewWriter(&buf, "01JBQ8F2K3M5N7P9R1S3T5V7W9"), t.TempDir(), nil, false)
	if err == nil {
		t.Fatal("the install proceeded with no measurements to plan against")
	}
	if !strings.Contains(err.Error(), "checks have not run") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}
