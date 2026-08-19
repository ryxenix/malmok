package dataplane

import (
	"context"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"platform.ryxen.dev/malmok/api/v1alpha1"
	"platform.ryxen.dev/malmok/internal/engine"
	"platform.ryxen.dev/malmok/internal/exec"
)

func ciliumSpec() v1alpha1.ClusterSpec {
	return v1alpha1.ClusterSpec{
		Network: v1alpha1.NetworkSpec{Mode: v1alpha1.NetworkOnline},
		Topology: v1alpha1.TopologySpec{
			RegistrationAddress: "192.168.88.210",
			VIP:                 &v1alpha1.VIPSpec{Address: "192.168.88.210", Mode: "arp"},
		},
		Kubernetes: v1alpha1.KubernetesSpec{
			Version: "v1.34.10+rke2r1",
			Dataplane: v1alpha1.DataplaneSpec{
				Preset:           "cilium-gw",
				LoadBalancerPool: []string{"192.168.88.216/29"},
			},
		},
		Gateway: v1alpha1.GatewaySpec{
			DomainSuffix: "acme.internal",
			Gateways: []v1alpha1.Gateway{{
				Name: "public", Address: "192.168.88.216",
				Listeners: []v1alpha1.ListenerSpec{
					{Name: "http", Protocol: v1alpha1.ListenerHTTP, Port: 80},
				},
			}},
		},
	}
}

// parse reads a rendered manifest back, because a file the cluster cannot parse
// is a manifest that silently does nothing, and a string comparison would not
// catch it.
func parse(t *testing.T, body string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := yaml.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("the rendered manifest is not YAML: %v\n%s", err, body)
	}
	return out
}

func TestCiliumHelmConfig(t *testing.T) {
	got := parse(t, CiliumHelmConfig(ciliumSpec()))
	if got["kind"] != "HelmChartConfig" {
		t.Fatalf("kind is %v", got["kind"])
	}

	spec, _ := got["spec"].(map[string]any)
	values, _ := spec["valuesContent"].(string)
	inner := parse(t, values)

	if inner["kubeProxyReplacement"] != true {
		t.Error("kube-proxy replacement is not enabled, so the Gateway API cannot work")
	}
	// Not a server's address and not the VIP. RKE2 runs a load balancer on
	// every node, so this bakes in no peer -- and it avoids the circle where
	// Cilium needs the API to start and kube-vip needs Cilium to route to it.
	if inner["k8sServiceHost"] != "127.0.0.1" {
		t.Errorf("k8sServiceHost is %v, which bakes a peer into every node", inner["k8sServiceHost"])
	}

	gw, _ := inner["gatewayAPI"].(map[string]any)
	if gw["enabled"] != true {
		t.Error("the Gateway API is not enabled for a cilium-gw preset")
	}
	// Enabling the API without the L7 proxy produces Gateways that are accepted
	// and never serve anything.
	if inner["l7Proxy"] != true {
		t.Error("the L7 proxy is off, so a Gateway would accept and serve nothing")
	}

	l2, _ := inner["l2announcements"].(map[string]any)
	if l2["enabled"] != true {
		t.Error("L2 announcements are off, so pool addresses would be allocated and unreachable")
	}
}

// A preset without the Gateway suffix gets the CNI and the load balancer and no
// Gateway controller.
func TestCiliumWithoutGatewayAPI(t *testing.T) {
	spec := ciliumSpec()
	spec.Kubernetes.Dataplane.Preset = "cilium"

	values := parse(t, CiliumHelmConfig(spec))["spec"].(map[string]any)["valuesContent"].(string)
	inner := parse(t, values)
	if _, ok := inner["gatewayAPI"]; ok {
		t.Error("a preset without a Gateway controller enabled the Gateway API")
	}
	if inner["kubeProxyReplacement"] != true {
		t.Error("kube-proxy replacement should still be on for Cilium")
	}
}

// BGP means the customer's routers peer, and announcing on both would leave two
// devices claiming one address.
func TestBGPSuppressesL2Announcements(t *testing.T) {
	spec := ciliumSpec()
	spec.Topology.VIP.Mode = "bgp"

	values := parse(t, CiliumHelmConfig(spec))["spec"].(map[string]any)["valuesContent"].(string)
	if _, ok := parse(t, values)["l2announcements"]; ok {
		t.Error("L2 announcements are enabled alongside BGP")
	}
	if body := L2AnnouncementPolicy(spec); body != "" {
		t.Errorf("an L2 announcement policy was rendered for a BGP document:\n%s", body)
	}
}

func TestLoadBalancerPool(t *testing.T) {
	got := parse(t, LoadBalancerPool(ciliumSpec()))
	if got["kind"] != "CiliumLoadBalancerIPPool" {
		t.Fatalf("kind is %v", got["kind"])
	}
	blocks, _ := got["spec"].(map[string]any)["blocks"].([]any)
	if len(blocks) != 1 {
		t.Fatalf("blocks is %v", blocks)
	}
	if blocks[0].(map[string]any)["cidr"] != "192.168.88.216/29" {
		t.Errorf("the block is %v", blocks[0])
	}

	// An empty pool is not the same as no pool: creating one would let a
	// Service sit Pending forever with nothing saying why.
	spec := ciliumSpec()
	spec.Kubernetes.Dataplane.LoadBalancerPool = nil
	if body := LoadBalancerPool(spec); body != "" {
		t.Errorf("a pool was rendered with no addresses reserved:\n%s", body)
	}
}

// Announcing from a worker would send traffic an extra hop to reach a service
// the worker does not host.
func TestL2PolicyAnnouncesFromTheControlPlaneOnly(t *testing.T) {
	got := parse(t, L2AnnouncementPolicy(ciliumSpec()))
	if got["kind"] != "CiliumL2AnnouncementPolicy" {
		t.Fatalf("kind is %v", got["kind"])
	}
	sel, _ := got["spec"].(map[string]any)["nodeSelector"].(map[string]any)
	labels, _ := sel["matchLabels"].(map[string]any)
	if labels["node-role.kubernetes.io/control-plane"] != "true" {
		t.Errorf("the node selector is %v", labels)
	}
}

// TLS passthrough needs TLSRoute, which is still experimental. Installing the
// wider bundle when nothing uses it puts alpha types in a customer's cluster
// for no reason.
func TestGatewayAPIChannelFollowsTheListeners(t *testing.T) {
	if got := gatewayAPIChannel(ciliumSpec()); got != "standard" {
		t.Errorf("channel is %q for HTTP listeners", got)
	}

	spec := ciliumSpec()
	spec.Gateway.Gateways[0].Listeners = append(spec.Gateway.Gateways[0].Listeners,
		v1alpha1.ListenerSpec{Name: "passthrough", Protocol: v1alpha1.ListenerTLSPassthrough, Port: 443})
	if got := gatewayAPIChannel(spec); got != "experimental" {
		t.Errorf("channel is %q for a passthrough listener", got)
	}
}

// ---------------------------------------------------------------------------
// Steps
// ---------------------------------------------------------------------------

// Observe must not change anything.
func TestObserveChangesNothing(t *testing.T) {
	f := &exec.Fake{Default: exec.Result{ExitCode: 1}}
	for _, s := range Steps(f, ciliumSpec(), Options{}) {
		if _, err := s.Observe(context.Background()); err != nil {
			t.Fatalf("%s: %v", s.ID(), err)
		}
	}
	for _, cmd := range f.Log {
		for _, bad := range []string{
			"kubectl apply", "kubectl delete", "kubectl create", "install -d",
			"curl ", "> /var/lib", "rollout restart",
		} {
			if strings.Contains(cmd, bad) {
				t.Errorf("an Observe would have changed the cluster: it contains %q\n%s", bad, cmd)
			}
		}
	}
}

// A file on disk is not a cluster that accepted it. Writing a load balancer
// pool that was never created leaves Services Pending forever with nothing
// saying why.
func TestManifestStepsWaitForTheCluster(t *testing.T) {
	byName := map[string]*engine.ShellStep{}
	for _, s := range Steps(&exec.Fake{}, ciliumSpec(), Options{}) {
		st := s.(*engine.ShellStep)
		byName[st.Name] = st
	}

	for name, resource := range map[string]string{
		"cilium-values":   "helmchartconfig",
		"lb-pool":         "ciliumloadbalancerippool",
		"l2-announcement": "ciliuml2announcementpolicy",
	} {
		st, ok := byName[name]
		if !ok {
			t.Errorf("%s produced no step", name)
			continue
		}
		if !strings.Contains(st.Check, resource) {
			t.Errorf("%s checks the file and not whether the cluster has %s", name, resource)
		}
		if !strings.Contains(st.Do, "deadline") {
			t.Errorf("%s writes the file and does not wait for the cluster", name)
		}
	}

	// The same for the CRDs: RKE2 applies the directory on its own schedule.
	if crds := byName["gateway-api-crds"]; !strings.Contains(crds.Do, "deadline") {
		t.Error("the Gateway API step returns before the CRDs exist")
	}
}

// The observable is the bundle version, not whether the CRDs exist: a cluster
// upgraded from an older bundle has the CRDs and the wrong fields.
func TestGatewayCRDStepChecksTheBundleVersion(t *testing.T) {
	crds := Steps(&exec.Fake{}, ciliumSpec(), Options{})[0].(*engine.ShellStep)
	if !strings.Contains(crds.Check, "bundle-version") {
		t.Error("the check does not read the bundle version")
	}
	if !strings.Contains(crds.Check, GatewayAPIVersion) {
		t.Errorf("the check does not compare against %s", GatewayAPIVersion)
	}
}

// An airgapped site reads the bundle from the artifact path and reaches for
// nothing.
func TestAirgapReadsTheBundleFromDisk(t *testing.T) {
	do := Steps(&exec.Fake{}, ciliumSpec(), Options{ArtifactPath: "/opt/artifacts"})[0].(*engine.ShellStep).Do
	if !strings.Contains(do, "/opt/artifacts") {
		t.Error("the artifact path is ignored")
	}
	if strings.Contains(do, "curl") {
		t.Error("an airgapped install would still reach for the network")
	}
}

// A preset this phase does not implement must produce nothing rather than an
// empty success.
func TestUnsupportedPresetProducesNoSteps(t *testing.T) {
	spec := ciliumSpec()
	spec.Kubernetes.Dataplane.Preset = "canal-traefik"
	if steps := Steps(&exec.Fake{}, spec, Options{}); len(steps) != 0 {
		t.Errorf("a preset with no implementation produced %d steps", len(steps))
	}
}

// The step id is what the state file records and the runner splits on the
// first '@'.
func TestStepIDs(t *testing.T) {
	for _, s := range Steps(&exec.Fake{}, ciliumSpec(), Options{}) {
		name, host, ok := strings.Cut(s.ID(), "@")
		if !ok || host != "fake" {
			t.Errorf("%q does not end in the node", s.ID())
		}
		if strings.Contains(name, "@") {
			t.Errorf("the step part of %q contains an '@'", s.ID())
		}
		if !strings.HasPrefix(name, Phase+"/") {
			t.Errorf("%q is not filed under %s", s.ID(), Phase)
		}
	}
}
