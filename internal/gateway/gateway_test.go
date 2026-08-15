package gateway

import (
	"context"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"platform.ryxen.dev/platformctl/api/v1alpha1"
	"platform.ryxen.dev/platformctl/internal/cert"
	"platform.ryxen.dev/platformctl/internal/dataplane"
	"platform.ryxen.dev/platformctl/internal/engine"
	"platform.ryxen.dev/platformctl/internal/exec"
)

func gatewaySpec() v1alpha1.ClusterSpec {
	return v1alpha1.ClusterSpec{
		Kubernetes: v1alpha1.KubernetesSpec{
			Dataplane: v1alpha1.DataplaneSpec{Preset: "cilium-gw"},
		},
		Gateway: v1alpha1.GatewaySpec{
			DomainSuffix: "acme.internal",
			Gateways: []v1alpha1.Gateway{{
				Name: "public", Address: "192.168.88.216",
				Listeners: []v1alpha1.ListenerSpec{
					{Name: "http", Protocol: v1alpha1.ListenerHTTP, Port: 80},
					{Name: "https", Protocol: v1alpha1.ListenerHTTPS, Port: 443,
						Hostname: "*.acme.internal",
						TLS:      &v1alpha1.ListenerTLS{Source: v1alpha1.TLSFromBYO, SecretRef: "public-tls"}},
				},
			}},
		},
	}
}

// docs splits a multi-document manifest and parses each, because a file the
// cluster cannot read is a manifest that silently does nothing.
func docs(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	dec := yaml.NewDecoder(strings.NewReader(body))
	for {
		var d map[string]any
		err := dec.Decode(&d)
		if err != nil {
			break
		}
		if len(d) > 0 {
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		t.Fatalf("nothing parsed from:\n%s", body)
	}
	return out
}

func find(t *testing.T, ds []map[string]any, kind string) map[string]any {
	t.Helper()
	for _, d := range ds {
		if d["kind"] == kind {
			return d
		}
	}
	t.Fatalf("no %s in the manifest", kind)
	return nil
}

func TestGatewayManifest(t *testing.T) {
	ds := docs(t, GatewayManifest(gatewaySpec()))

	if ns := find(t, ds, "Namespace"); ns["metadata"].(map[string]any)["name"] != DefaultNamespace {
		t.Errorf("the namespace is %v", ns["metadata"])
	}

	gw := find(t, ds, "Gateway")
	spec, _ := gw["spec"].(map[string]any)
	if spec["gatewayClassName"] != "cilium" {
		t.Errorf("gatewayClassName is %v; ADR-004 derives it from the preset", spec["gatewayClassName"])
	}

	// A pinned address is the whole reason PF-612 asks for one: the DNS record
	// is requested before the install, so the address cannot be whatever
	// LB-IPAM happens to allocate.
	addrs, _ := spec["addresses"].([]any)
	if len(addrs) != 1 || addrs[0].(map[string]any)["value"] != "192.168.88.216" {
		t.Errorf("addresses is %v", addrs)
	}

	listeners, _ := spec["listeners"].([]any)
	if len(listeners) != 2 {
		t.Fatalf("listeners is %v", listeners)
	}
	https, _ := listeners[1].(map[string]any)
	tls, _ := https["tls"].(map[string]any)
	if tls["mode"] != "Terminate" {
		t.Errorf("the HTTPS listener mode is %v", tls["mode"])
	}
	refs, _ := tls["certificateRefs"].([]any)
	if len(refs) != 1 || refs[0].(map[string]any)["name"] != "public-tls" {
		t.Errorf("certificateRefs is %v", refs)
	}
}

// Which namespaces may attach a route is the multi-tenant boundary. A document
// that says nothing has to get the closed answer.
func TestAllowedRoutesDefaultsToClosed(t *testing.T) {
	ds := docs(t, GatewayManifest(gatewaySpec()))
	listeners := find(t, ds, "Gateway")["spec"].(map[string]any)["listeners"].([]any)

	for _, l := range listeners {
		allowed, _ := l.(map[string]any)["allowedRoutes"].(map[string]any)
		ns, _ := allowed["namespaces"].(map[string]any)
		if ns["from"] != "Same" {
			t.Errorf("a document that says nothing produced from: %v", ns["from"])
		}
	}
}

func TestAllowedRoutesFollowsTheDocument(t *testing.T) {
	t.Run("all", func(t *testing.T) {
		spec := gatewaySpec()
		spec.Gateway.Gateways[0].RouteNamespaces = "all"
		ds := docs(t, GatewayManifest(spec))
		l := find(t, ds, "Gateway")["spec"].(map[string]any)["listeners"].([]any)[0]
		ns := l.(map[string]any)["allowedRoutes"].(map[string]any)["namespaces"].(map[string]any)
		if ns["from"] != "All" {
			t.Errorf("from is %v", ns["from"])
		}
	})

	t.Run("selector", func(t *testing.T) {
		spec := gatewaySpec()
		spec.Gateway.Gateways[0].RouteNamespaces = "selector"
		spec.Gateway.Gateways[0].NamespaceSelector = map[string]string{"tier": "public", "team": "web"}

		ds := docs(t, GatewayManifest(spec))
		l := find(t, ds, "Gateway")["spec"].(map[string]any)["listeners"].([]any)[0]
		ns := l.(map[string]any)["allowedRoutes"].(map[string]any)["namespaces"].(map[string]any)
		if ns["from"] != "Selector" {
			t.Fatalf("from is %v", ns["from"])
		}
		labels := ns["selector"].(map[string]any)["matchLabels"].(map[string]any)
		if labels["tier"] != "public" || labels["team"] != "web" {
			t.Errorf("matchLabels is %v", labels)
		}
	})
}

// Passthrough terminates at the backend, so the gateway holds no certificate
// and must not be handed one.
func TestPassthroughListenerCarriesNoCertificate(t *testing.T) {
	spec := gatewaySpec()
	spec.Gateway.Gateways[0].Listeners = []v1alpha1.ListenerSpec{{
		Name: "passthrough", Protocol: v1alpha1.ListenerTLSPassthrough, Port: 443,
		TLS: &v1alpha1.ListenerTLS{SecretRef: "should-not-be-used"},
	}}

	ds := docs(t, GatewayManifest(spec))
	l := find(t, ds, "Gateway")["spec"].(map[string]any)["listeners"].([]any)[0]
	tls, _ := l.(map[string]any)["tls"].(map[string]any)
	if tls["mode"] != "Passthrough" {
		t.Errorf("mode is %v", tls["mode"])
	}
	if _, ok := tls["certificateRefs"]; ok {
		t.Error("a passthrough listener was given a certificate to terminate with")
	}
}

// ADR-006: this tool creates no routes, so a chart has to be told where the
// gateway is. Without the contract every chart hardcodes it, and moving a
// gateway means editing every chart.
func TestContractManifest(t *testing.T) {
	got := docs(t, ContractManifest(gatewaySpec()))[0]
	if got["kind"] != "ConfigMap" {
		t.Fatalf("kind is %v", got["kind"])
	}
	data, _ := got["data"].(map[string]any)

	for k, want := range map[string]string{
		"gatewayClassName":         "cilium",
		"domainSuffix":             "acme.internal",
		"gateway.public.namespace": DefaultNamespace,
		"gateway.public.address":   "192.168.88.216",
		"gateways":                 "public",
	} {
		if data[k] != want {
			t.Errorf("%s is %v, want %q", k, data[k], want)
		}
	}
}

// No HTTPRoute, ever (ADR-006). A route created here would be owned by the
// installer, and every application change would need the installer run again.
func TestNothingCreatesRoutes(t *testing.T) {
	for _, body := range []string{GatewayManifest(gatewaySpec()), ContractManifest(gatewaySpec())} {
		for _, kind := range []string{"HTTPRoute", "GRPCRoute", "TLSRoute", "TCPRoute"} {
			if strings.Contains(body, "kind: "+kind) {
				t.Errorf("a %s was rendered; ADR-006 makes routes the application's", kind)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// The TLS secret
// ---------------------------------------------------------------------------

func testBundle() *cert.Bundle {
	return &cert.Bundle{
		Fingerprint: "sha256:b919b24fb9bed2853c67a3d613c880e1",
		ChainPEM:    []byte("-----BEGIN CERTIFICATE-----\nleaf\n-----END CERTIFICATE-----\n"),
		KeyPEM:      []byte("-----BEGIN PRIVATE KEY-----\nSUPERSECRETKEYMATERIAL\n-----END PRIVATE KEY-----\n"),
		CAPEM:       []byte("-----BEGIN CERTIFICATE-----\nroot\n-----END CERTIFICATE-----\n"),
	}
}

// The private key must never land on the node's filesystem for the life of the
// cluster. A manifest file in RKE2's directory is readable by anything that can
// read the disk; applying it once puts the key in etcd, where it was going
// anyway, and leaves nothing behind.
func TestTLSSecretIsNeverWrittenToTheManifestDirectory(t *testing.T) {
	s := secretStep("public-tls", DefaultNamespace, testBundle())

	if strings.Contains(s.Do, "/var/lib/rancher/rke2/server/manifests") {
		t.Error("the private key would be written into the manifest directory")
	}
	if !strings.Contains(s.Do, "umask 077") {
		t.Error("the temporary file holding the key is not created unreadable")
	}
	if !strings.Contains(s.Do, "trap") {
		t.Error("the temporary file is not removed when kubectl fails")
	}
}

// The check compares the fingerprint, so the key never has to be read back off
// the cluster and the step's evidence stays safe for an audit report.
func TestTLSSecretChecksTheFingerprint(t *testing.T) {
	b := testBundle()
	s := secretStep("public-tls", DefaultNamespace, b)

	if !strings.Contains(s.Check, b.Fingerprint) {
		t.Error("the check does not compare the fingerprint")
	}
	if strings.Contains(s.Check, "SUPERSECRETKEYMATERIAL") {
		t.Error("the check would read the key back")
	}

	f := &exec.Fake{Default: exec.Result{
		ExitCode: 1, Stdout: "sha256:0000000000000000000000000000000\n",
	}}
	s.Runner, s.Host = f, "h"
	obs, err := s.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if obs.Satisfied {
		t.Error("a secret holding another certificate reported as satisfied")
	}
	if strings.Contains(obs.Detail+obs.Evidence, "SUPERSECRETKEYMATERIAL") {
		t.Errorf("key material reached the event stream: %q %q", obs.Detail, obs.Evidence)
	}
}

// A private CA is not in any public trust store, so it has to travel with the
// bundle for clients that verify against it.
func TestPrivateCATravelsWithTheSecret(t *testing.T) {
	if !strings.Contains(secretStep("r", "ns", testBundle()).Do, "ca.crt") {
		t.Error("the private CA was dropped from the secret")
	}

	b := testBundle()
	b.CAPEM = nil
	if strings.Contains(secretStep("r", "ns", b).Do, "ca.crt") {
		t.Error("a ca.crt key was emitted with no private CA to put in it")
	}
}

// ---------------------------------------------------------------------------
// Steps
// ---------------------------------------------------------------------------

// Accepted is not enough. A Gateway is Accepted when the controller agrees to
// implement it and Programmed when it has an address and a bound listener --
// and the gap is where a missing pool or an unusable certificate shows up.
func TestWaitsForProgrammedNotAccepted(t *testing.T) {
	steps := Steps(&exec.Fake{}, gatewaySpec(), Options{
		Bundles: map[string]*cert.Bundle{"public-tls": testBundle()},
	})
	last := steps[len(steps)-1].(*engine.ShellStep)

	if !strings.Contains(last.Check, "Programmed") {
		t.Error("the final check does not wait for Programmed")
	}
	if strings.Contains(last.Check, `@.type=="Accepted"`) {
		t.Error("the final check settles for Accepted")
	}
	// A failure has to show what the Gateway itself says, or the operator is
	// left guessing which of the listeners is at fault.
	if !strings.Contains(last.Do, "reason") {
		t.Error("a failed Gateway reports no conditions")
	}
}

// A listener referencing a Secret that does not exist yet reports
// Programmed=False with a reason naming the reference, not the missing
// material -- so the Secret goes in first.
func TestSecretsComeBeforeGateways(t *testing.T) {
	steps := Steps(&exec.Fake{}, gatewaySpec(), Options{
		Bundles: map[string]*cert.Bundle{"public-tls": testBundle()},
	})
	var order []string
	for _, s := range steps {
		order = append(order, s.(*engine.ShellStep).Name)
	}
	if len(order) < 2 || !strings.HasPrefix(order[0], "tls-") {
		t.Errorf("the steps run in this order: %v", order)
	}
}

func TestSecretRefs(t *testing.T) {
	got := SecretRefs(gatewaySpec())
	if len(got) != 1 || got[0] != "public-tls" {
		t.Errorf("SecretRefs = %v", got)
	}

	// A plain HTTP listener names no certificate.
	spec := gatewaySpec()
	spec.Gateway.Gateways[0].Listeners = spec.Gateway.Gateways[0].Listeners[:1]
	if got := SecretRefs(spec); len(got) != 0 {
		t.Errorf("SecretRefs = %v for an HTTP-only gateway", got)
	}
}

// Observe must not change anything.
func TestObserveChangesNothing(t *testing.T) {
	f := &exec.Fake{Default: exec.Result{ExitCode: 1}}
	for _, s := range Steps(f, gatewaySpec(), Options{
		Bundles: map[string]*cert.Bundle{"public-tls": testBundle()},
	}) {
		if _, err := s.Observe(context.Background()); err != nil {
			t.Fatalf("%s: %v", s.ID(), err)
		}
	}
	for _, cmd := range f.Log {
		for _, bad := range []string{
			"kubectl apply", "kubectl create", "kubectl delete", "install -d", "mktemp",
		} {
			if strings.Contains(cmd, bad) {
				t.Errorf("an Observe would have changed the cluster: it contains %q\n%s", bad, cmd)
			}
		}
	}
}

// A document with no gateway produces no steps rather than an empty success.
func TestNoGatewaysProducesNoSteps(t *testing.T) {
	if steps := Steps(&exec.Fake{}, v1alpha1.ClusterSpec{}, Options{}); len(steps) != 0 {
		t.Errorf("a document with no gateway produced %d steps", len(steps))
	}
}

func TestStepIDs(t *testing.T) {
	for _, s := range Steps(&exec.Fake{}, gatewaySpec(), Options{
		Bundles: map[string]*cert.Bundle{"public-tls": testBundle()},
	}) {
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

// A pool address is one more IP answering on the segment, and IDC and
// air-gapped network policy frequently allows only the addresses the nodes
// already hold. node-ips exposure answers there: Cilium's envoy binds the
// listener ports in the host namespace, so <node>:<port> is the endpoint with
// no second IP anywhere.
func TestNodeIPExposure(t *testing.T) {
	spec := v1alpha1.ClusterSpec{
		Topology: v1alpha1.TopologySpec{
			Servers: []v1alpha1.NodeSpec{{Host: "10.0.0.11"}},
			Agents:  []v1alpha1.NodeSpec{{Host: "10.0.0.21", NodeIP: "10.0.0.121"}},
		},
		Gateway: v1alpha1.GatewaySpec{
			DomainSuffix: "acme.internal",
			Gateways: []v1alpha1.Gateway{{
				Name: "public", Exposure: v1alpha1.ExposureNodeIPs,
				Listeners: []v1alpha1.ListenerSpec{{Name: "http", Protocol: v1alpha1.ListenerHTTP, Port: 80}},
			}},
		},
	}

	var answers *engine.ShellStep
	for _, s := range Steps(&exec.Fake{}, spec, Options{}) {
		if st, ok := s.(*engine.ShellStep); ok && st.Name == "answers-public" {
			answers = st
		}
		// Cilium never gives a host-networked gateway a pool address, so
		// waiting for Programmed would wait forever on a gateway that works.
		if st, ok := s.(*engine.ShellStep); ok && st.Name == "programmed-public" {
			t.Error("a node-ips gateway waits on Programmed, which never comes without a pool")
		}
	}
	if answers == nil {
		t.Fatal("nothing verifies the gateway answers on the node addresses")
	}

	// The observable is the thing itself: every named address answering on the
	// listener port. Any HTTP status counts -- a 404 from a gateway with no
	// routes is the gateway working. A patched Service is not an observable at
	// all; Cilium strips foreign fields on reconcile, verified live.
	for _, want := range []string{"http://10.0.0.11:80/", "http://10.0.0.121:80/", "curl"} {
		if !strings.Contains(answers.Check, want) {
			t.Errorf("the check does not probe %q:\n%s", want, answers.Check)
		}
	}

	// Every node answers when the document names none; a named subset is used
	// as given, with the advertised nodeIP winning over the SSH address.
	got := gatewayNodeIPs(spec, spec.Gateway.Gateways[0])
	if len(got) != 2 || got[0] != "10.0.0.11" || got[1] != "10.0.0.121" {
		t.Errorf("the default is not every node: %v", got)
	}
	spec.Gateway.Gateways[0].NodeIPs = []string{"10.0.0.121"}
	if got := gatewayNodeIPs(spec, spec.Gateway.Gateways[0]); len(got) != 1 || got[0] != "10.0.0.121" {
		t.Errorf("the subset was not honoured: %v", got)
	}

	// A subset needs the label selector's labels to exist -- and to be removed
	// from the nodes the document stopped naming.
	var label *engine.ShellStep
	for _, s := range Steps(&exec.Fake{}, spec, Options{}) {
		if st, ok := s.(*engine.ShellStep); ok && st.Name == "gateway-nodes" {
			label = st
		}
	}
	if label == nil {
		t.Fatal("a named subset writes no node labels")
	}
	if !strings.Contains(label.Do, dataplane.GatewayNodeLabel+"=true") ||
		!strings.Contains(label.Do, dataplane.GatewayNodeLabel+"-") {
		t.Errorf("the labels are not both set and cleared:\n%s", label.Do)
	}

	// And the contract publishes what DNS has to point at.
	if c := ContractManifest(spec); !strings.Contains(c, `gateway.public.address: "10.0.0.121"`) {
		t.Errorf("the contract does not name the endpoints:\n%s", c)
	}
}

// The dataplane side of the same statement: node-ips gateways put envoy in the
// host namespace, and a named subset becomes the label selector.
func TestCiliumHostNetworkFollowsExposure(t *testing.T) {
	spec := v1alpha1.ClusterSpec{
		Kubernetes: v1alpha1.KubernetesSpec{
			Dataplane: v1alpha1.DataplaneSpec{Preset: v1alpha1.DataplaneCiliumGW},
		},
		Gateway: v1alpha1.GatewaySpec{Gateways: []v1alpha1.Gateway{{
			Name: "public", Exposure: v1alpha1.ExposureNodeIPs,
			Listeners: []v1alpha1.ListenerSpec{{Name: "http", Protocol: v1alpha1.ListenerHTTP, Port: 80}},
		}}},
	}

	cfg := dataplane.CiliumHelmConfig(spec)
	if !strings.Contains(cfg, "hostNetwork:") || !strings.Contains(cfg, "enabled: true") {
		t.Errorf("node-ips exposure does not enable host networking:\n%s", cfg)
	}
	if strings.Contains(cfg, dataplane.GatewayNodeLabel) {
		t.Errorf("no subset is named and a selector exists anyway:\n%s", cfg)
	}

	spec.Gateway.Gateways[0].NodeIPs = []string{"10.0.0.11"}
	if cfg := dataplane.CiliumHelmConfig(spec); !strings.Contains(cfg, dataplane.GatewayNodeLabel) {
		t.Errorf("a named subset produces no selector:\n%s", cfg)
	}

	spec.Gateway.Gateways[0].Exposure = ""
	if cfg := dataplane.CiliumHelmConfig(spec); strings.Contains(cfg, "hostNetwork:") {
		t.Errorf("a load-balanced gateway was moved into the host namespace:\n%s", cfg)
	}
}
