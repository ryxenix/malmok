package dataplane

import (
	"context"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ryxen/malmok/api/v1alpha1"
	"github.com/ryxen/malmok/internal/engine"
	"github.com/ryxen/malmok/internal/exec"
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

// canal-traefik configures nothing -- RKE2 ships both charts and this tool
// adds no values -- but "nothing to configure" is not "nothing to check". The
// phase used to be skipped whole, so a canal build never observed the
// dataplane its workloads depend on and reported success on the strength of
// the installer having run.
func TestTheBundledDataplaneIsStillObserved(t *testing.T) {
	spec := ciliumSpec()
	spec.Kubernetes.Dataplane.Preset = "canal-traefik"

	steps := Steps(&exec.Fake{}, spec, Options{})
	if len(steps) != 1 {
		t.Fatalf("canal produced %d steps, want the one that observes it", len(steps))
	}
	sh := steps[0].(*engine.ShellStep)
	if !strings.Contains(sh.Check, "ds/rke2-canal") || !strings.Contains(sh.Check, "coredns") {
		t.Errorf("the check does not observe canal and CoreDNS:\n%s", sh.Check)
	}
	// Nothing Cilium-shaped: this preset has no cilium-config to reconcile.
	if strings.Contains(sh.Check, "cilium") || strings.Contains(sh.Do, "cilium") {
		t.Error("the canal step reaches for Cilium")
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

// Under `set -e`, a command substitution that exits non-zero takes the whole
// script with it -- so every assignment whose command may legitimately fail
// (a resource that does not exist yet is the answer a wait loop waits for)
// has to say `|| true`. Without it the Gateway API wait never ran once on a
// fresh cluster: kubectl exited 1 because the CRD was absent, the script died
// silently, and a re-run passed only because RKE2 had applied the bundle
// meanwhile. That flakiness cost a whole IDC build.
func TestWaitLoopsSurviveTheAbsenceTheyWaitFor(t *testing.T) {
	for _, s := range Steps(&exec.Fake{}, ciliumSpec(), Options{}) {
		sh := s.(*engine.ShellStep)
		for _, script := range []string{sh.Check, sh.Do} {
			if !strings.Contains(script, "set -e") {
				continue
			}
			for _, line := range strings.Split(script, "\n") {
				line = strings.TrimSpace(line)
				// An assignment from a command that hides its stderr is one
				// where absence is expected.
				if !strings.Contains(line, "=$(") || !strings.Contains(line, "2>/dev/null") {
					continue
				}
				if !strings.Contains(line, "|| true") && !strings.HasSuffix(line, "\\") {
					t.Errorf("%s: an assignment under set -e can kill the script:\n  %s", sh.Name, line)
				}
			}
		}
	}
}

// The Cilium operator asks for two replicas that will not share a node, so on
// a one-node cluster the second is Pending for the life of the cluster -- a
// pod this tool created that can never be scheduled, which teaches operators
// to ignore Pending. One node, one operator; two nodes keep the pair.
func TestASingleNodeGetsOneOperator(t *testing.T) {
	one := ciliumSpec()
	one.Topology.Servers = []v1alpha1.NodeSpec{{Host: "10.0.0.11"}}
	values := parse(t, CiliumHelmConfig(one))["spec"].(map[string]any)["valuesContent"].(string)
	op, _ := parse(t, values)["operator"].(map[string]any)
	if op == nil || op["replicas"] != 1 {
		t.Errorf("a single-node cluster asks for %v operator replicas", op)
	}

	two := ciliumSpec()
	two.Topology.Servers = []v1alpha1.NodeSpec{{Host: "10.0.0.11"}}
	two.Topology.Agents = []v1alpha1.NodeSpec{{Host: "10.0.0.12"}}
	values = parse(t, CiliumHelmConfig(two))["spec"].(map[string]any)["valuesContent"].(string)
	if _, ok := parse(t, values)["operator"]; ok {
		t.Error("a two-node cluster overrides the chart's own replica count")
	}
}

// A preset without a Gateway controller must not be handed Gateway API work.
// cilium-traefik installs no controller: it used to get the CRDs anyway, wait
// for a GatewayClass nothing would create, and -- before even that -- sit out
// a 900-second timeout because the applied-check demanded
// enable-gateway-api=true, a value that preset never writes.
func TestCiliumWithoutAGatewayControllerSkipsGatewayWork(t *testing.T) {
	spec := ciliumSpec()
	spec.Kubernetes.Dataplane.Preset = v1alpha1.DataplaneCiliumTraefik
	spec.Gateway = v1alpha1.GatewaySpec{}

	var names []string
	for _, s := range Steps(&exec.Fake{}, spec, Options{}) {
		st := s.(*engine.ShellStep)
		names = append(names, st.Name)
		if st.Name != "cilium-applied" {
			continue
		}
		// The expectation is the document's, not cilium-gw's.
		if !strings.Contains(st.Check, "enable-gateway-api=false") {
			t.Errorf("the applied check does not expect the Gateway API to be off:\n%s", st.Check)
		}
	}
	for _, unwanted := range []string{"gateway-api-crds", "gatewayclass"} {
		if slices.Contains(names, unwanted) {
			t.Errorf("a preset with no Gateway controller runs %q; steps: %v", unwanted, names)
		}
	}

	// And the gateway preset still does all of it.
	var gwNames []string
	for _, s := range Steps(&exec.Fake{}, ciliumSpec(), Options{}) {
		gwNames = append(gwNames, s.(*engine.ShellStep).Name)
	}
	for _, wanted := range []string{"gateway-api-crds", "gatewayclass"} {
		if !slices.Contains(gwNames, wanted) {
			t.Errorf("cilium-gw lost %q; steps: %v", wanted, gwNames)
		}
	}
}

// HTTP/2 on a TLS listener is Cilium's ALPN setting, and it is off unless the
// document asks. Enabling it also enables backend protocol selection, which
// changes how the gateway talks to any Service that already declares an
// appProtocol -- a decision about somebody's running workload rather than a
// performance knob to flip for them.
func TestHTTP2IsWrittenAndObserved(t *testing.T) {
	on := true
	spec := ciliumSpec()
	spec.Gateway.HTTP2 = &on

	values := CiliumHelmConfig(spec)
	if !strings.Contains(values, "enableAlpn: true") {
		t.Errorf("the chart does not enable ALPN:\n%s", values)
	}

	// Writing the value is half of it. The step that decides whether cilium
	// already carries this configuration has to read the key too, or turning
	// HTTP/2 on in the document is satisfied by a cluster that does not have
	// it -- the same shape as a check that measures something other than what
	// it claims.
	step := ciliumAppliedStep(spec, Options{})
	if !strings.Contains(step.Check, "enable-gateway-api-alpn") {
		t.Errorf("the check does not read the alpn key:\n%s", step.Check)
	}
	if !strings.Contains(step.Check, `agrees "$al" true`) {
		t.Errorf("the check does not compare alpn against the document:\n%s", step.Check)
	}

	// And off by default.
	plain := CiliumHelmConfig(ciliumSpec())
	if strings.Contains(plain, "enableAlpn") {
		t.Errorf("ALPN is enabled without being asked for:\n%s", plain)
	}
	if !strings.Contains(ciliumAppliedStep(ciliumSpec(), Options{}).Check, `agrees "$al" false`) {
		t.Error("a document that never mentions HTTP/2 does not require it to be off")
	}
}
