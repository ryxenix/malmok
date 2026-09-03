package report

// The machine-readable handoff.
//
// The audit report and the DNS sheet are written for people. This file is
// written for the next tool: an inventory system that wants to know what
// cluster now exists, on which machines, built by which run -- without
// parsing markdown. The contract is loose coupling by format: malmok
// writes handoff.json, anything may read it, and neither side calls the
// other.
//
// Two rules shape every field. Nothing secret: no passwords, no resolved
// SourceRefs, no kubeconfig contents -- the kubeconfig is named by location
// only. Nothing invented: a value that was not measured or stated is omitted,
// because a consumer that cannot tell "unknown" from "empty" will bind its
// inventory to a lie.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ryxenix/malmok/api/v1alpha1"
	"github.com/ryxenix/malmok/internal/event"
	"github.com/ryxenix/malmok/internal/platform"
)

// HandoffVersion names the schema so a consumer can refuse what it does not
// understand instead of misreading it.
const (
	HandoffVersion = "malmok.dev/handoff/v1alpha1"
	HandoffKind    = "ClusterHandoff"
)

// rke2Kubeconfig is where RKE2 writes the admin kubeconfig on every server.
// Stated here rather than imported from internal/rke2, because this package
// consumes finished run directories and should not depend on the installer.
const rke2Kubeconfig = "/etc/rancher/rke2/rke2.yaml"

// Results a run can end with. "incomplete" is a run whose terminal event was
// never written -- the process died or the directory is still live -- which a
// consumer must not confuse with either verdict.
const (
	ResultSucceeded  = "succeeded"
	ResultFailed     = "failed"
	ResultIncomplete = "incomplete"
)

// Handoff is the whole document.
type Handoff struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`

	Run        HandoffRun         `json:"run"`
	Cluster    HandoffCluster     `json:"cluster"`
	Kubeconfig *HandoffKubeconfig `json:"kubeconfig,omitempty"`
	Nodes      []HandoffNode      `json:"nodes"`
	Components []HandoffComponent `json:"components,omitempty"`
	Dataplane  *HandoffDataplane  `json:"dataplane,omitempty"`
}

// HandoffRun identifies the run that produced this cluster.
type HandoffRun struct {
	ID        string    `json:"id"`
	StartedAt time.Time `json:"startedAt,omitzero"`
	EndedAt   time.Time `json:"endedAt,omitzero"`
	Result    string    `json:"result"`
}

// HandoffCluster is the cluster as the document named it.
type HandoffCluster struct {
	Name                string            `json:"name"`
	Profile             string            `json:"profile,omitempty"`
	KubernetesVersion   string            `json:"kubernetesVersion,omitempty"`
	RegistrationAddress string            `json:"registrationAddress,omitempty"`
	Annotations         map[string]string `json:"annotations,omitempty"`
}

// HandoffKubeconfig names where the kubeconfig lives. Location only, never
// content: this tool does not retrieve it, so the honest statement is the
// server it sits on and the path RKE2 wrote it to.
type HandoffKubeconfig struct {
	Node string `json:"node"`
	Path string `json:"path"`
}

// HandoffNode is one machine of the cluster.
type HandoffNode struct {
	Host     string `json:"host"`
	Role     string `json:"role,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	NodeIP   string `json:"nodeIP,omitempty"`
	// MachineUUID is the measured DMI product UUID (PF-109) -- on a VM, the
	// identity the hypervisor stamped in. Absent when the probe never ran
	// (a simulated run) or the node has no DMI.
	MachineUUID string `json:"machineUUID,omitempty"`
	// Annotations pass through NodeSpec.Annotations untouched: the caller's
	// own names for this machine, e.g. a Proxmox VMID.
	Annotations map[string]string `json:"annotations,omitempty"`
}

// HandoffComponent is one installed piece worth naming to an inventory.
type HandoffComponent struct {
	Name string `json:"name"`
	// Version is stated only where this tool decides it (RKE2). ChartVersion
	// is stated where the tool pins a chart but not the app inside it. A
	// component may carry neither: bundled Cilium's version is RKE2's
	// decision, and inventing it here would be the lie the header forbids.
	Version      string `json:"version,omitempty"`
	ChartVersion string `json:"chartVersion,omitempty"`
	Role         string `json:"role,omitempty"`
}

// HandoffDataplane is what actually carries traffic, after any downgrade.
type HandoffDataplane struct {
	Preset           string   `json:"preset"`
	Downgraded       bool     `json:"downgraded"`
	LoadBalancerPool []string `json:"loadBalancerPool,omitempty"`
}

// BuildHandoff derives the handoff from a run directory's contents. Pure:
// everything comes from the Run, nothing from the network or the clock, so
// two calls over the same directory produce the same document.
func BuildHandoff(r *Run) Handoff {
	h := Handoff{
		APIVersion: HandoffVersion,
		Kind:       HandoffKind,
		Run: HandoffRun{
			ID:        r.ID,
			StartedAt: r.StartedAt,
			EndedAt:   r.EndedAt,
			Result:    runResult(r),
		},
		Cluster: HandoffCluster{
			Name:                r.Spec.Metadata.Name,
			Profile:             string(r.Spec.Metadata.Profile),
			KubernetesVersion:   r.Spec.Kubernetes.Version,
			RegistrationAddress: r.Spec.Topology.RegistrationAddress,
			Annotations:         r.Spec.Metadata.Annotations,
		},
	}

	uuids := machineUUIDs(r)
	add := func(n v1alpha1.NodeSpec, role v1alpha1.NodeRole) {
		if n.Role != "" {
			role = n.Role
		}
		h.Nodes = append(h.Nodes, HandoffNode{
			Host:        n.Host,
			Role:        string(role),
			Hostname:    n.Hostname,
			NodeIP:      n.NodeIP,
			MachineUUID: uuids[n.Host],
			Annotations: n.Annotations,
		})
	}
	for _, n := range r.Spec.Topology.Servers {
		add(n, v1alpha1.RoleServer)
	}
	for _, n := range r.Spec.Topology.Agents {
		add(n, v1alpha1.RoleAgent)
	}

	// The kubeconfig sits on the first server; every server holds an
	// equivalent copy, but naming one gives a consumer a command that works.
	if len(r.Spec.Topology.Servers) > 0 {
		h.Kubeconfig = &HandoffKubeconfig{
			Node: r.Spec.Topology.Servers[0].Host,
			Path: rke2Kubeconfig,
		}
	}

	h.Components = components(r)
	h.Dataplane = handoffDataplane(r)
	return h
}

// WriteHandoff serialises the handoff beside the other artifacts and returns
// its path.
func WriteHandoff(r *Run) (string, error) {
	dir := filepath.Join(r.Dir, ArtifactsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("handoff: create %s: %w", dir, err)
	}
	body, err := json.MarshalIndent(BuildHandoff(r), "", "  ")
	if err != nil {
		return "", fmt.Errorf("handoff: encode: %w", err)
	}
	p := filepath.Join(dir, HandoffFile)
	if err := os.WriteFile(p, append(body, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("handoff: write %s: %w", p, err)
	}
	return p, nil
}

// runResult reads the verdict from the run's terminal event. The engine
// always writes one (runner.finish); a directory without one belongs to a run
// that never got to say how it ended.
func runResult(r *Run) string {
	for i := len(r.Events) - 1; i >= 0; i-- {
		e := r.Events[i]
		if e.Kind != event.KindRun || !e.Status.Terminal() {
			continue
		}
		if e.Status == event.StatusOK {
			return ResultSucceeded
		}
		return ResultFailed
	}
	return ResultIncomplete
}

// machineUUIDs collects PF-109 measurements by node. Only passes count: a
// skipped probe's detail explains why there is no UUID, and putting that
// sentence where a consumer expects an identifier would be worse than
// omitting the field.
func machineUUIDs(r *Run) map[string]string {
	out := map[string]string{}
	for _, e := range r.Events {
		if e.Kind == event.KindProbe && e.Code == "PF-109" && e.Status == event.StatusOK && e.Node != "" {
			out[e.Node] = e.Detail
		}
	}
	return out
}

// components names what this run put on the cluster, stated only as far as
// this tool decides it.
func components(r *Run) []HandoffComponent {
	var out []HandoffComponent
	if v := r.Spec.Kubernetes.Version; v != "" {
		out = append(out, HandoffComponent{Name: "rke2", Version: v})
	}

	preset := actualPreset(r)
	switch preset {
	case v1alpha1.DataplaneCiliumGW:
		// Bundled by RKE2, so its version is RKE2's decision -- named for its
		// role, not versioned.
		out = append(out, HandoffComponent{Name: "cilium", Role: "cni+gateway"})
	case v1alpha1.DataplaneCiliumTraefik:
		out = append(out, HandoffComponent{Name: "cilium", Role: "cni"})
	}

	// ArgoCD is stated only when its phase actually finished: a run that
	// stopped before l2-platform did not install it.
	if phaseSucceeded(r, platform.Phase) {
		out = append(out, HandoffComponent{Name: "argocd", ChartVersion: platform.ChartVersion, Role: "gitops"})
	}
	return out
}

// phaseSucceeded reports whether the named phase reached OK in this run.
func phaseSucceeded(r *Run, phase string) bool {
	for _, e := range r.Events {
		if e.Kind == event.KindPhase && e.Phase == phase && e.Status == event.StatusOK {
			return true
		}
	}
	return false
}

// dataplane reports what will actually carry traffic: the plan's decision
// when there is a plan, the document's request when the run stopped earlier.
func handoffDataplane(r *Run) *HandoffDataplane {
	preset := actualPreset(r)
	if preset == "" {
		return nil
	}
	d := &HandoffDataplane{
		Preset:           string(preset),
		LoadBalancerPool: r.Spec.Kubernetes.Dataplane.LoadBalancerPool,
	}
	if r.Plan != nil {
		d.Downgraded = r.Plan.Downgraded()
	}
	return d
}

// actualPreset is the dataplane after any downgrade.
func actualPreset(r *Run) v1alpha1.DataplanePreset {
	if r.Plan != nil && r.Plan.Actual.Dataplane != "" {
		return r.Plan.Actual.Dataplane
	}
	return r.Spec.Kubernetes.Dataplane.Preset
}
