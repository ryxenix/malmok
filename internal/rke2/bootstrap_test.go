package rke2

import (
	"context"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ryxen/malmok/api/v1alpha1"
	"github.com/ryxen/malmok/internal/engine"
	"github.com/ryxen/malmok/internal/exec"
)

func serverNode() v1alpha1.NodeSpec {
	return v1alpha1.NodeSpec{Host: "192.168.88.241", Role: v1alpha1.RoleServer}
}

func clusterSpec() v1alpha1.ClusterSpec {
	n := serverNode()
	return v1alpha1.ClusterSpec{
		Network: v1alpha1.NetworkSpec{Mode: v1alpha1.NetworkOnline},
		Topology: v1alpha1.TopologySpec{
			RegistrationAddress: "k8s-api.acme.internal",
			Servers:             []v1alpha1.NodeSpec{n},
		},
		Kubernetes: v1alpha1.KubernetesSpec{
			Version:   "v1.34.10+rke2r1",
			Dataplane: v1alpha1.DataplaneSpec{Preset: "cilium-gw"},
		},
		Registry: v1alpha1.RegistrySpec{Mode: v1alpha1.RegistryEmbedded},
	}
}

// parse renders the config and reads it back, because a file RKE2 cannot parse
// is a cluster that does not start, and a string comparison would not catch it.
func parse(t *testing.T, body string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := yaml.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("the rendered config is not YAML: %v\n%s", err, body)
	}
	return out
}

func TestServerConfig(t *testing.T) {
	got := parse(t, ServerConfig(serverNode(), clusterSpec(), ""))

	// ADR-005: rke2-ingress-nginx reached EOL in March 2026 and is always off,
	// whatever the document says.
	disabled, _ := got["disable"].([]any)
	if len(disabled) == 0 || disabled[0] != "rke2-ingress-nginx" {
		t.Errorf("rke2-ingress-nginx is not disabled: %v", got["disable"])
	}

	// The registration address is what every node joins through. A certificate
	// that does not cover it fails every join with a TLS error naming the wrong
	// thing.
	sans, _ := got["tls-san"].([]any)
	if !containsAny(sans, "k8s-api.acme.internal") {
		t.Errorf("the registration address is not in tls-san: %v", sans)
	}

	if got["cni"] != "cilium" {
		t.Errorf("cni is %v, want cilium for a cilium-gw preset", got["cni"])
	}
	// The first server gets no token: RKE2 generates one, and a token in
	// cluster.yaml would be a plaintext secret in an artifact handed to
	// customers.
	if _, ok := got["token"]; ok {
		t.Error("a token was written for the first server")
	}
}

// Adding a SAN later regenerates the certificates on every existing server,
// which is what HA promotion trips over. Future servers have to be covered now.
func TestServerConfigCoversFutureServers(t *testing.T) {
	spec := clusterSpec()
	spec.Topology.Servers = append(spec.Topology.Servers,
		v1alpha1.NodeSpec{Host: "192.168.88.242"},
		v1alpha1.NodeSpec{Host: "192.168.88.243", Hostname: "rke2-server-05"})
	spec.Topology.VIP = &v1alpha1.VIPSpec{Address: "192.168.88.250"}
	spec.Topology.TLSSAN = []string{"k8s.acme.co.kr"}

	sans, _ := parse(t, ServerConfig(serverNode(), spec, ""))["tls-san"].([]any)
	for _, want := range []string{
		"k8s-api.acme.internal", "192.168.88.250", "k8s.acme.co.kr",
		"192.168.88.242", "192.168.88.243", "rke2-server-05",
	} {
		if !containsAny(sans, want) {
			t.Errorf("tls-san is missing %q: %v", want, sans)
		}
	}

	// A name appearing twice in the document must not appear twice in the file.
	seen := map[string]int{}
	for _, s := range sans {
		seen[s.(string)]++
	}
	for name, n := range seen {
		if n > 1 {
			t.Errorf("%q appears %d times in tls-san", name, n)
		}
	}
}

// Repeating a key per item would be a different document: the last one wins and
// everything before it is silently dropped.
func TestListArgumentsAreOneSequence(t *testing.T) {
	spec := clusterSpec()
	spec.Kubernetes.KubeletArgs = []string{"max-pods=250", "eviction-hard=memory.available<200Mi"}
	spec.Kubernetes.APIServerArgs = []string{"audit-log-maxage=30", "audit-log-maxbackup=10"}

	got := parse(t, ServerConfig(serverNode(), spec, ""))
	for key, want := range map[string]int{"kubelet-arg": 2, "kube-apiserver-arg": 2} {
		list, _ := got[key].([]any)
		if len(list) != want {
			t.Errorf("%s holds %d values, want %d: %v", key, len(list), want, list)
		}
	}
}

// A value carrying YAML punctuation has to survive as one value.
func TestConfigQuotesAwkwardValues(t *testing.T) {
	spec := clusterSpec()
	spec.Kubernetes.KubeletArgs = []string{"eviction-hard=memory.available<200Mi"}

	got := parse(t, ServerConfig(serverNode(), spec, "token: not-a-key"))
	if got["token"] != "token: not-a-key" {
		t.Errorf("the token was reinterpreted as YAML: %#v", got["token"])
	}
	list, _ := got["kubelet-arg"].([]any)
	if len(list) != 1 || list[0] != "eviction-hard=memory.available<200Mi" {
		t.Errorf("the kubelet argument was mangled: %v", list)
	}
}

// ADR-004 binds CNI, gateway and load balancer together, so the preset is the
// only input. Picking a CNI independently produces a cluster whose gateway
// nothing can give an address to.
func TestCNIFollowsThePreset(t *testing.T) {
	tests := map[v1alpha1.DataplanePreset]string{
		"cilium-gw":     "cilium",
		"canal-traefik": "canal",
		"":              "",
	}
	for preset, want := range tests {
		if got := cniFor(preset); got != want {
			t.Errorf("cniFor(%q) = %q, want %q", preset, got, want)
		}
	}
}

// The document may disable more, and must not be able to re-enable the one that
// is always off.
func TestDisableListCannotReenableIngressNginx(t *testing.T) {
	spec := clusterSpec()
	spec.Kubernetes.DisableBundled = []string{"rke2-metrics-server", "rke2-ingress-nginx"}

	list, _ := parse(t, ServerConfig(serverNode(), spec, ""))["disable"].([]any)
	// Four: the three that are always off on this preset (ingress-nginx by
	// ADR-005, the bundled Traefik and its CRD chart because the gateway is
	// Cilium) and the one the document added. Naming an always-off entry again
	// must not double it.
	if len(list) != 4 {
		t.Fatalf("disable holds %v", list)
	}
	if list[0] != "rke2-ingress-nginx" || !containsAny(list, "rke2-metrics-server") ||
		!containsAny(list, "rke2-traefik") {
		t.Errorf("disable is %v", list)
	}
}

// ---------------------------------------------------------------------------
// Steps
// ---------------------------------------------------------------------------

// Observe must not change anything, which is what resume depends on.
func TestBootstrapObserveChangesNothing(t *testing.T) {
	f := &exec.Fake{Default: exec.Result{ExitCode: 1}}
	for _, s := range BootstrapSteps(f, serverNode(), clusterSpec(), Options{}) {
		if _, err := s.Observe(context.Background()); err != nil {
			t.Fatalf("%s: %v", s.ID(), err)
		}
	}
	// printf is not on the list: the config check pipes the expected content
	// into cmp, which writes to a pipe and not to the node. What is forbidden
	// is redirection to a file and anything that starts or installs something.
	for _, cmd := range f.Log {
		for _, bad := range []string{
			"install.sh", "get.rke2.io", "systemctl enable", "systemctl start",
			"install -d", "chmod ", "> /etc/", "> /var/", "rm -f",
		} {
			if strings.Contains(cmd, bad) {
				t.Errorf("an Observe would have changed the node: it contains %q\n%s", bad, cmd)
			}
		}
	}
}

// The observable target is the version the binary reports, which is what makes
// an install and an upgrade the same operation and stops a half-finished
// extraction from counting as installed.
func TestInstallObservesTheVersionNotThePresence(t *testing.T) {
	steps := BootstrapSteps(&exec.Fake{}, serverNode(), clusterSpec(), Options{})
	check := steps[0].(*engine.ShellStep).Check
	if !strings.Contains(check, "rke2 --version") {
		t.Error("the install check does not read the version")
	}
	if !strings.Contains(check, "v1.34.10+rke2r1") {
		t.Error("the install check does not compare against the document's version")
	}

	t.Run("a different version is not satisfied", func(t *testing.T) {
		f := &exec.Fake{Responses: map[string]exec.Result{
			"rke2 --version": {ExitCode: 1, Stdout: "rke2 is v1.33.0+rke2r1, the document asks for v1.34.10+rke2r1\n"},
		}}
		s := BootstrapSteps(f, serverNode(), clusterSpec(), Options{})[0]
		obs, err := s.Observe(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if obs.Satisfied {
			t.Error("a node running another version reported as satisfied")
		}
	})
}

// ADR-013: the tarball, and an artifact path when the site is airgapped.
func TestInstallUsesTheTarballMethod(t *testing.T) {
	do := BootstrapSteps(&exec.Fake{}, serverNode(), clusterSpec(), Options{})[0].(*engine.ShellStep).Do
	if !strings.Contains(do, "INSTALL_RKE2_METHOD=tar") {
		t.Error("the install does not force the tarball method")
	}
	if !strings.Contains(do, "get.rke2.io") {
		t.Error("an online install does not fetch the installer")
	}

	air := BootstrapSteps(&exec.Fake{}, serverNode(), clusterSpec(),
		Options{ArtifactPath: "/opt/rke2-artifacts"})[0].(*engine.ShellStep).Do
	if !strings.Contains(air, "INSTALL_RKE2_ARTIFACT_PATH") {
		t.Error("an airgapped install does not read from the artifact path")
	}
	if strings.Contains(air, "get.rke2.io") {
		t.Error("an airgapped install would still reach for the network")
	}
}

// "Started" is not the target. systemd calls a unit active the moment the
// process is up, which on a first start is minutes before the API server
// answers -- and a phase that moved on there would try to join a second server
// to something that cannot accept it.
func TestServiceWaitsForReadyNotForStarted(t *testing.T) {
	s := BootstrapSteps(&exec.Fake{}, serverNode(), clusterSpec(), Options{})[2].(*engine.ShellStep)
	if !strings.Contains(s.Check, "Ready") {
		t.Error("the service check does not look at node readiness")
	}
	if !strings.Contains(s.Do, "deadline") || !strings.Contains(s.Do, "sleep") {
		t.Error("the service apply does not wait for the node to become Ready")
	}
	// A unit that died while starting has to be reported with its own logs; a
	// bare timeout tells whoever is holding the pager nothing.
	if !strings.Contains(s.Do, "journalctl") {
		t.Error("a failed start reports no logs")
	}
	if s.DoTimeout == 0 {
		t.Error("the service step has no timeout, so a hung start blocks forever")
	}
}

// The config file carries the cluster token. Its evidence reaches the audit
// report, and the report reaches the customer.
func TestConfigStepNeverPrintsItsContents(t *testing.T) {
	spec := clusterSpec()
	s := configStep(ServerConfig(serverNode(), spec, "K10secret::server:token"))
	s.Runner = &exec.Fake{Default: exec.Result{
		ExitCode: 1, Stdout: "/etc/rancher/rke2/config.yaml differs from the document\n",
	}}
	s.Host = "h"

	obs, err := s.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(obs.Detail+obs.Evidence, "K10secret") {
		t.Errorf("the token reached the event stream: %q %q", obs.Detail, obs.Evidence)
	}
	if !strings.Contains(s.Do, "chmod 0600") {
		t.Error("the config file is written without restricting its mode")
	}
}

// The step id is what the state file records, and the runner splits it on the
// first '@'.
func TestBootstrapStepIDs(t *testing.T) {
	for _, s := range BootstrapSteps(&exec.Fake{}, serverNode(), clusterSpec(), Options{}) {
		name, host, ok := strings.Cut(s.ID(), "@")
		if !ok || host != "192.168.88.241" {
			t.Errorf("%q does not end in the node", s.ID())
		}
		if strings.Contains(name, "@") {
			t.Errorf("the step part of %q contains an '@'", s.ID())
		}
		if !strings.HasPrefix(name, PhaseBootstrap+"/") {
			t.Errorf("%q is not filed under %s", s.ID(), PhaseBootstrap)
		}
	}
}

func containsAny(list []any, want string) bool {
	for _, v := range list {
		if s, ok := v.(string); ok && s == want {
			return true
		}
	}
	return false
}

// The account the tool logged in as is the account kubectl and k9s run from
// five minutes after the install finishes, and RKE2's kubeconfig is root-only.
// Without the copy the cluster looks broken from the very machine it was built
// on -- found on a live server where k9s could not connect.
func TestTheOperatorGetsAKubeconfig(t *testing.T) {
	spec := v1alpha1.ClusterSpec{
		Topology: v1alpha1.TopologySpec{RegistrationAddress: "k8s.acme.internal"},
	}
	node := v1alpha1.NodeSpec{Host: "10.0.0.11", SSH: v1alpha1.SSHSpec{User: "k8s"}}

	var step *engine.ShellStep
	for _, s := range BootstrapSteps(&exec.Fake{}, node, spec, Options{}) {
		if st, ok := s.(*engine.ShellStep); ok && st.Name == "kubeconfig" {
			step = st
		}
	}
	if step == nil {
		t.Fatal("bootstrap leaves the operator without cluster access")
	}

	// Theirs: owned by the login account, not readable by the group.
	for _, want := range []string{"u='k8s'", "install -m 600 -o \"$u\"", Kubeconfig, ".kube/config"} {
		if !strings.Contains(step.Do, want) {
			t.Errorf("the copy does not include %q:\n%s", want, step.Do)
		}
	}
	// Root needs no copy; the original is already root's to read. And on a
	// local node the document names no account, so the account is whoever sudo
	// elevated -- decided at run time, which is the only place it is known.
	for _, want := range []string{"$SUDO_USER", `"$u" != root`} {
		if !strings.Contains(step.Do, want) {
			t.Errorf("the account resolution does not handle %q:\n%s", want, step.Do)
		}
	}
	// The check compares content, so a rotated cluster CA is repaired by a
	// re-run rather than reported as satisfied.
	if !strings.Contains(step.Check, "cmp -s") {
		t.Errorf("the check does not compare content:\n%s", step.Check)
	}
	if !strings.Contains(step.Check, "stat -c %U") {
		t.Errorf("the check does not verify ownership:\n%s", step.Check)
	}
}

// Joining servers hold a kubeconfig too; agents have none to copy.
func TestJoiningServersGetAKubeconfigAndAgentsDoNot(t *testing.T) {
	spec := v1alpha1.ClusterSpec{
		Topology: v1alpha1.TopologySpec{RegistrationAddress: "k8s.acme.internal"},
	}
	has := func(role v1alpha1.NodeRole) bool {
		target := v1alpha1.NodeSpec{Host: "10.0.0.12", Role: role, SSH: v1alpha1.SSHSpec{User: "k8s"}}
		for _, s := range JoinSteps(&exec.Fake{}, &exec.Fake{}, spec, target, "token", Options{}) {
			if st, ok := s.(*engine.ShellStep); ok && st.Name == "kubeconfig" {
				return true
			}
		}
		return false
	}
	if !has(v1alpha1.RoleServer) {
		t.Error("a joining server leaves its operator without cluster access")
	}
	if has(v1alpha1.RoleAgent) {
		t.Error("an agent is given a kubeconfig it does not have")
	}
}

// RKE2 v1.36 replaced the EOL'd ingress-nginx with a bundled Traefik -- the
// succession ADR-005 predicted, under a name the disable list did not cover.
// On the preset whose gateway is Cilium it has to go: two controllers fight
// over the same Gateways, and its CRD chart tries to take Helm ownership of
// the Gateway API CRDs this tool already installed, which leaves two install
// jobs crash-looping forever. Found live after the 1.36 upgrade.
func TestBundledTraefikFollowsThePreset(t *testing.T) {
	config := func(preset v1alpha1.DataplanePreset) string {
		return ServerConfig(v1alpha1.NodeSpec{Host: "10.0.0.11"}, v1alpha1.ClusterSpec{
			Topology:   v1alpha1.TopologySpec{RegistrationAddress: "k8s.acme.internal"},
			Kubernetes: v1alpha1.KubernetesSpec{Dataplane: v1alpha1.DataplaneSpec{Preset: preset}},
		}, "")
	}

	got := config(v1alpha1.DataplaneCiliumGW)
	// Both names: the CRD chart is its own component, and disabling only the
	// chart that consumes it leaves the CRD installer crash-looping alone.
	for _, want := range []string{"rke2-traefik", "rke2-traefik-crd"} {
		if !strings.Contains(got, want) {
			t.Errorf("cilium-gw does not disable %s:\n%s", want, got)
		}
	}
	// The *-traefik presets keep it: there, the bundled Traefik is the gateway.
	for _, preset := range []v1alpha1.DataplanePreset{
		v1alpha1.DataplaneCiliumTraefik, v1alpha1.DataplaneCanalTraefik,
	} {
		if got := config(preset); strings.Contains(got, "rke2-traefik") {
			t.Errorf("%s disables the Traefik it uses as its gateway:\n%s", preset, got)
		}
	}
}

// The advertised address is the document's, not the default route's. Found on
// the first IDC install: a multi-homed node advertised its public interface
// because nothing pinned node-ip, while the operator had named the internal
// address in the document all along.
func TestNodeIPFollowsTheDocument(t *testing.T) {
	tests := []struct {
		name string
		host string
		pin  string // explicit NodeSpec.NodeIP
		want any    // expected node-ip value; nil = key absent
	}{
		{name: "an IP host pins itself", host: "192.168.0.24", want: "192.168.0.24"},
		{name: "an explicit nodeIP wins", host: "192.168.0.24", pin: "10.0.0.5", want: "10.0.0.5"},
		{name: "a hostname pins nothing", host: "cp01.acme.internal", want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n := serverNode()
			n.Host, n.NodeIP = tc.host, tc.pin
			got := parse(t, ServerConfig(n, clusterSpec(), ""))
			if got["node-ip"] != tc.want {
				t.Errorf("node-ip = %v, want %v", got["node-ip"], tc.want)
			}
		})
	}
}

// With kube-proxy disabled, Cilium must know the API server's direct address
// before rke2-server first starts, or bootstrap deadlocks: Cilium waits on the
// in-cluster service IP that only a running kube-proxy -- or a running Cilium
// -- would route, and the node sits NotReady until the timeout. The prestaged
// manifest must therefore land before the service step.
func TestPrestagedManifestsLandBeforeTheServiceStarts(t *testing.T) {
	o := Options{Prestage: []PrestagedManifest{{
		Name: "cilium-values", Path: ManifestDir + "/x.yaml", Body: "kubeProxyReplacement: true",
	}}}
	steps := BootstrapSteps(&exec.Fake{}, serverNode(), clusterSpec(), o)

	pre, svc := -1, -1
	for i, s := range steps {
		switch s.(*engine.ShellStep).Name {
		case "cilium-values":
			pre = i
		case "service":
			svc = i
		}
	}
	if pre == -1 {
		t.Fatal("the prestaged manifest produced no step")
	}
	if svc == -1 {
		t.Fatal("no service step")
	}
	if pre > svc {
		t.Errorf("the manifest (step %d) lands after the service starts (step %d)", pre, svc)
	}
}

// The kubeconfig step gave the operator credentials to a cluster they could
// not address: RKE2 buries kubectl in /var/lib/rancher/rke2/bin and ships no
// helm CLI at all. The tools step links kubectl onto the PATH and fetches helm
// and k9s -- online only, because a step that needs the internet on an
// airgapped site must skip, not hang.
func TestOperatorToolsFollowTheNetworkMode(t *testing.T) {
	online := opsToolsStep(true)
	if !strings.Contains(online.Do, "ln -sf "+BinDir+"/kubectl") {
		t.Errorf("kubectl is not linked onto the PATH:\n%s", online.Do)
	}
	for _, want := range []string{"k9s_Linux_", "get.helm.sh/helm-"} {
		if !strings.Contains(online.Do, want) {
			t.Errorf("online mode does not fetch %s:\n%s", want, online.Do)
		}
	}
	// helm's version comes from its own pointer file. A number written here
	// would install whatever was current the day this was typed, for as long
	// as nobody noticed.
	if !strings.Contains(online.Do, "helm-latest-version") {
		t.Errorf("the helm version is pinned in the step rather than asked for:\n%s", online.Do)
	}
	// That lookup is assigned, and an assignment whose command substitution
	// fails under set -e ends the script. This package has already shipped
	// that defect once, in a wait loop that consequently never ran.
	if !strings.Contains(online.Do, `|| echo ""`) {
		t.Errorf("the version lookup can kill the script under set -e:\n%s", online.Do)
	}

	airgap := opsToolsStep(false)
	for _, unwanted := range []string{"k9s_Linux_", "get.helm.sh"} {
		if strings.Contains(airgap.Do, unwanted) {
			t.Errorf("an airgapped site is asked to download %s", unwanted)
		}
	}
	for _, unwanted := range []string{"command -v k9s", "command -v helm"} {
		if strings.Contains(airgap.Check, unwanted) {
			t.Errorf("an airgapped site is checked for %q, which it cannot fetch", unwanted)
		}
	}
}
