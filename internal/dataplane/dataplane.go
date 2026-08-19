// Package dataplane builds the l2-dataplane phase.
//
// ADR-004 binds the CNI, the Gateway controller and the source of load balancer
// addresses into one atomic choice, and this phase is where that becomes true
// on a cluster. It is deliberately not split: a run that installed the CNI and
// stopped would leave a Gateway that reports Accepted while nothing can give it
// an external address, which is a state that takes a long time to diagnose from
// the symptom.
//
// Everything is written into RKE2's auto-deploying manifest directory rather
// than applied with kubectl. The cluster then reconciles it on every restart
// without this tool being present, which is the difference between a cluster
// somebody can hand over and one that needs its installer kept around.
package dataplane

import (
	"fmt"
	"strings"
	"time"

	"platform.ryxen.dev/malmok/api/v1alpha1"
	"platform.ryxen.dev/malmok/internal/engine"
	"platform.ryxen.dev/malmok/internal/exec"
	"platform.ryxen.dev/malmok/internal/rke2"
)

// Phase is where these steps are filed.
const Phase = "l2-dataplane"

// GatewayAPIVersion is the Gateway API bundle installed.
//
// Pinned to what Cilium supports rather than to the newest release: Cilium
// 1.19 documents v1.4.1, and a newer bundle means CRDs whose fields the
// controller ignores -- which presents as a Gateway that accepts a
// configuration and then does not implement it.
const GatewayAPIVersion = "v1.4.1"

// gatewayAPIURL is where the bundle comes from when the site is online.
const gatewayAPIURL = "https://github.com/kubernetes-sigs/gateway-api/releases/download/" +
	GatewayAPIVersion + "/%s-install.yaml"

// Files this phase writes, all under RKE2's manifest directory.
const (
	gatewayCRDFile = rke2.ManifestDir + "/malmok-gateway-api-crds.yaml"
	ciliumCfgFile  = rke2.ManifestDir + "/malmok-cilium-config.yaml"
	lbPoolFile     = rke2.ManifestDir + "/malmok-lb-pool.yaml"
	l2PolicyFile   = rke2.ManifestDir + "/malmok-l2-announcement.yaml"
)

const managedFileHeader = "# Managed by malmok. Changes here are overwritten on the next apply."

// kubectl is the prelude every step needs: RKE2 keeps its binaries outside PATH
// and its kubeconfig outside the default location.
var kubectl = fmt.Sprintf("export PATH=$PATH:%s\nexport KUBECONFIG=%s\n", rke2.BinDir, rke2.Kubeconfig)

// Options are what the steps need beyond the document.
type Options struct {
	// ArtifactPath holds the Gateway API bundle for an airgapped site. Empty
	// means it is fetched.
	ArtifactPath string
	// Timeout bounds waiting for the cluster to converge on the new
	// configuration. Redeploying Cilium restarts every agent pod.
	Timeout time.Duration
}

func (o Options) timeout() time.Duration {
	if o.Timeout <= 0 {
		return 10 * time.Minute
	}
	return o.Timeout
}

// Steps returns the l2-dataplane catalogue.
//
// runner is a control-plane node: everything here is cluster state, so it is
// written once rather than on every node.
func Steps(runner exec.Runner, spec v1alpha1.ClusterSpec, o Options) []engine.Step {
	if !isCilium(spec) {
		// The other presets are not implemented yet, and producing an empty
		// phase silently would report success for work nobody did.
		return nil
	}

	host := runner.Host()
	add := func(s *engine.ShellStep) engine.Step {
		s.Phase, s.Runner, s.Host = Phase, runner, host
		return s
	}

	steps := []engine.Step{
		add(gatewayCRDStep(spec, o)),
		add(rke2.ManifestStep(Phase, "cilium-values", ciliumCfgFile, CiliumHelmConfig(spec),
			"helmchartconfig -n kube-system rke2-cilium", o.timeout())),
		add(ciliumAppliedStep(spec, o)),
	}
	if body := LoadBalancerPool(spec); body != "" {
		steps = append(steps, add(rke2.ManifestStep(Phase, "lb-pool", lbPoolFile, body,
			"ciliumloadbalancerippool malmok", o.timeout())))
	}
	if body := L2AnnouncementPolicy(spec); body != "" {
		steps = append(steps, add(rke2.ManifestStep(Phase, "l2-announcement", l2PolicyFile, body,
			"ciliuml2announcementpolicy malmok", o.timeout())))
	}
	// The agents have to run the configuration that was just written. RKE2's
	// helm controller upgrades the chart, but a Cilium values change lands in
	// cilium-config and the agents read that at start -- nothing rolls them.
	// The same defect the rke2 service step closes: an agent that is Running
	// is not an agent running the current configuration, and every step still
	// reports success while a feature the document asked for silently does not
	// exist. Found live: host networking enabled in cilium-config, port 80
	// bound nowhere, agents five days old.
	steps = append(steps, add(ciliumCurrentStep(o)))
	return append(steps, add(gatewayClassStep(o)))
}

// ciliumCurrentStep restarts cilium when its pods predate the configuration
// they read.
func ciliumCurrentStep(o Options) *engine.ShellStep {
	// "Every pod started after the configuration it reads was written" --
	// measured as exactly that sentence. Two earlier attempts measured
	// something adjacent and were wrong twice on this live cluster: a pod
	// listing includes terminating pods, whose old start time read a finished
	// rollout as unfinished; and the rollout-restart stamp knows nothing about
	// the rollouts the helm controller performs, so a pod rolled before the
	// new values landed carried a stamp newer than the config file while
	// running the old configuration -- the operator started with
	// hostnetwork=false under a config that said true, and generated
	// addressless listeners.
	//
	// The moment the configuration was applied is cilium-config's own
	// managedFields time: the API server stamps every write, and the helm
	// job's write is the one that matters. The pods are the running,
	// non-terminating agents, envoys and operators -- go-template, because
	// jsonpath cannot express "has no deletionTimestamp". The operator is in
	// the comparison because it holds half the configuration: it is what turns
	// a Gateway into Envoy configuration, and a stale one regenerates old
	// listeners under a current everything-else.
	applied := `kubectl -n kube-system get cm cilium-config -o jsonpath='{range .metadata.managedFields[*]}{.time}{"\n"}{end}' 2>/dev/null | sort | tail -1`
	oldest := `kubectl -n kube-system get pods -l app.kubernetes.io/part-of=cilium -o go-template='{{range .items}}{{if not .metadata.deletionTimestamp}}{{.status.startTime}}{{"\n"}}{{end}}{{end}}' 2>/dev/null | sort | head -1`

	return &engine.ShellStep{
		Name: "cilium-current",
		Check: kubectl + fmt.Sprintf(`at=$(%s)
[ -n "$at" ] || { echo "cilium-config does not exist"; exit 1; }
old=$(%s)
[ -n "$old" ] || { echo "no cilium pod is running"; exit 1; }
[ "$(date -d "$old" +%%s)" -ge "$(date -d "$at" +%%s)" ] || {
  echo "a cilium pod predates the configuration it reads"; exit 1; }
kubectl -n kube-system rollout status ds/cilium --timeout=10s >/dev/null 2>&1 || {
  echo "the cilium rollout has not finished"; exit 1; }
echo "every cilium pod started after its configuration was applied"`, applied, oldest),

		Do: kubectl + fmt.Sprintf(`set -e
kubectl -n kube-system rollout restart deploy/cilium-operator
kubectl -n kube-system rollout restart ds/cilium ds/cilium-envoy 2>/dev/null || kubectl -n kube-system rollout restart ds/cilium
kubectl -n kube-system rollout status deploy/cilium-operator --timeout=%ds
kubectl -n kube-system rollout status ds/cilium --timeout=%ds
kubectl -n kube-system rollout status ds/cilium-envoy --timeout=%ds 2>/dev/null || true`,
			int(o.timeout().Seconds()), int(o.timeout().Seconds()), int(o.timeout().Seconds())),

		Satisfied: "%s",
		Missing:   "%s",
		DoTimeout: 2*o.timeout() + time.Minute,
		// The rollout waits on its own deadline.
		Attempts: 1,
	}
}

// isCilium reports whether the document asks for the Cilium dataplane.
func isCilium(spec v1alpha1.ClusterSpec) bool {
	return strings.HasPrefix(string(spec.Kubernetes.Dataplane.Preset), "cilium")
}

// WantsGatewayAPI reports whether the preset includes a Gateway controller.
//
// ADR-004: the preset decides all three together, so this is the one place that
// reads it and every other decision follows.
func WantsGatewayAPI(spec v1alpha1.ClusterSpec) bool {
	return spec.Kubernetes.Dataplane.Preset == "cilium-gw"
}

// gatewayAPIChannel picks the bundle to install.
//
// The standard channel covers GatewayClass, Gateway, HTTPRoute, GRPCRoute and
// ReferenceGrant. TLS passthrough needs TLSRoute, which is still experimental,
// so the wider bundle is installed only when a listener actually asks for it --
// installing experimental CRDs nobody uses puts alpha types in a customer's
// cluster for no reason.
func gatewayAPIChannel(spec v1alpha1.ClusterSpec) string {
	for _, gw := range spec.Gateway.Gateways {
		for _, l := range gw.Listeners {
			if l.Protocol == v1alpha1.ListenerTLSPassthrough {
				return "experimental"
			}
		}
	}
	return "standard"
}

// gatewayCRDStep installs the Gateway API bundle.
//
// The observable is the bundle version the CRDs carry in their annotation, not
// whether a file exists: a cluster upgraded from an older bundle has the CRDs
// and the wrong fields.
func gatewayCRDStep(spec v1alpha1.ClusterSpec, o Options) *engine.ShellStep {
	channel := gatewayAPIChannel(spec)

	var fetch string
	if o.ArtifactPath != "" {
		src := fmt.Sprintf("%s/gateway-api-%s-%s-install.yaml", o.ArtifactPath, GatewayAPIVersion, channel)
		fetch = fmt.Sprintf(`[ -f %s ] || { echo "the Gateway API bundle is not in the artifact path: %s"; exit 1; }
cp %s /tmp/gateway-api.yaml`, shellQuote(src), src, shellQuote(src))
	} else {
		fetch = fmt.Sprintf(`curl -sfL %s -o /tmp/gateway-api.yaml`,
			shellQuote(fmt.Sprintf(gatewayAPIURL, channel)))
	}

	return &engine.ShellStep{
		Name: "gateway-api-crds",
		Check: kubectl + fmt.Sprintf(`have=$(kubectl get crd gatewayclasses.gateway.networking.k8s.io \
  -o jsonpath='{.metadata.annotations.gateway\.networking\.k8s\.io/bundle-version}' 2>/dev/null)
[ -n "$have" ] || { echo "the Gateway API CRDs are not installed"; exit 1; }
[ "$have" = %s ] || { echo "the Gateway API bundle is $have, and Cilium supports %s"; exit 1; }
echo "Gateway API $have (%s channel)"`, shellQuote(GatewayAPIVersion), GatewayAPIVersion, channel),

		// Writing the file is not installing the CRDs. RKE2 watches the
		// directory and applies what appears there on its own schedule, so a
		// step that returned here would hand the next one a cluster whose
		// Gateway types do not exist yet.
		Do: kubectl + fmt.Sprintf(`set -e
%s
install -d -m 0755 %s
{ printf '%%s\n' %s; cat /tmp/gateway-api.yaml; } > %s
rm -f /tmp/gateway-api.yaml
deadline=$(( $(date +%%s) + 300 ))
while [ "$(date +%%s)" -lt "$deadline" ]; do
  have=$(kubectl get crd gatewayclasses.gateway.networking.k8s.io \
    -o jsonpath='{.metadata.annotations.gateway\.networking\.k8s\.io/bundle-version}' 2>/dev/null)
  [ "$have" = %s ] && exit 0
  sleep 5
done
echo "the cluster did not apply the Gateway API bundle. The deploy controller reports:"
kubectl -n kube-system get addon 2>&1 | tail -10
journalctl -u rke2-server -n 30 --no-pager 2>&1 | grep -i -e manifest -e deploy | tail -10
exit 1`,
			fetch, rke2.ManifestDir, shellQuote(managedFileHeader), gatewayCRDFile,
			shellQuote(GatewayAPIVersion)),

		Satisfied: "%s",
		Missing:   "%s",
		Attempts:  3,
		DoTimeout: 5 * time.Minute,
	}
}

// ciliumAppliedStep waits for the running Cilium to reflect the values.
//
// A HelmChartConfig on disk is not a reconfigured dataplane. RKE2 re-runs the
// install job, which rolls every Cilium pod, and the phases after this one
// assume the new configuration is live -- a Gateway created against an agent
// that has not restarted stays Programmed=False for reasons nothing reports.
func ciliumAppliedStep(spec v1alpha1.ClusterSpec, o Options) *engine.ShellStep {
	// The observable is that cilium-config carries what THIS document's values
	// imply -- including the values that change between documents. The first
	// version checked two keys that are true on every build, so it was
	// satisfied by the previous configuration and the phase moved on before
	// the helm controller had applied the new one: a re-apply that turned host
	// networking on restarted the pods into the old config, found live when
	// the gateway never answered.
	hostnet := "false"
	if NodeIPGateways(spec) {
		hostnet = "true"
	}
	read := `kubectl -n kube-system get cm cilium-config -o jsonpath='{.data.kube-proxy-replacement}{" "}{.data.enable-gateway-api}{" "}{.data.gateway-api-hostnetwork-enabled}' 2>/dev/null`
	// An absent key reads as empty; a document that wants it off accepts both.
	want := `"true true ` + hostnet + `"`
	if hostnet == "false" {
		want = `"true true false"|"true true "|"true true"`
	}

	return &engine.ShellStep{
		Name: "cilium-applied",
		Check: kubectl + fmt.Sprintf(`v="$(%s)"
case "$v" in
  %s) ;;
  *) echo "cilium-config carries '$v', the document implies 'true true %s'"; exit 1 ;;
esac
kubectl -n kube-system rollout status ds/cilium --timeout=10s >/dev/null 2>&1 || {
  echo "cilium is reconfigured and its pods have not finished rolling"; exit 1; }
echo "cilium-config carries the configuration this document implies"`, read, want, hostnet),

		Do: kubectl + fmt.Sprintf(`deadline=$(( $(date +%%s) + %d ))
while [ "$(date +%%s)" -lt "$deadline" ]; do
  v="$(%s)"
  case "$v" in
    %s)
      kubectl -n kube-system rollout status ds/cilium --timeout=20s >/dev/null 2>&1 && exit 0 ;;
  esac
  sleep 10
done
echo "cilium did not take the new configuration within %ds. The install job reports:"
kubectl -n kube-system logs -l job-name=helm-install-rke2-cilium --tail=40 2>&1 | tail -40
exit 1`, int(o.timeout().Seconds()), read, want, int(o.timeout().Seconds())),

		Satisfied: "%s",
		Missing:   "%s",
		DoTimeout: o.timeout() + time.Minute,
		// The wait inside Apply is already bounded.
		Attempts: 1,
	}
}

// gatewayClassStep waits for the controller to accept its own class.
//
// This is what makes ADR-004 checkable: a GatewayClass that is Accepted means a
// controller is running and willing to implement Gateways, which is the whole
// point of having chosen the preset.
func gatewayClassStep(o Options) *engine.ShellStep {
	read := `kubectl get gatewayclass cilium -o jsonpath='{range .status.conditions[?(@.type=="Accepted")]}{.status}{end}' 2>/dev/null`

	return &engine.ShellStep{
		Name: "gatewayclass",
		Check: kubectl + fmt.Sprintf(`s=$(%s)
[ "$s" = True ] || { echo "the cilium GatewayClass is not Accepted (status '$s')"; exit 1; }
echo "the cilium GatewayClass is Accepted"`, read),

		Do: kubectl + fmt.Sprintf(`deadline=$(( $(date +%%s) + %d ))
while [ "$(date +%%s)" -lt "$deadline" ]; do
  [ "$(%s)" = True ] && exit 0
  sleep 5
done
echo "the cilium GatewayClass never became Accepted. The cluster reports:"
kubectl get gatewayclass -o wide 2>&1 | tail -5
kubectl -n kube-system logs -l io.cilium/app=operator --tail=30 2>&1 | tail -30
exit 1`, int(o.timeout().Seconds()), read),

		Satisfied: "%s",
		Missing:   "%s",
		DoTimeout: o.timeout() + time.Minute,
	}
}

// ---------------------------------------------------------------------------
// Manifests
// ---------------------------------------------------------------------------

// CiliumHelmConfig renders the HelmChartConfig RKE2 merges into its Cilium
// chart.
//
// k8sServiceHost is 127.0.0.1 rather than a server's address or the VIP. RKE2
// runs a load balancer on every node that forwards to whichever server is up,
// so this bakes in no peer -- and it avoids the circle the VIP would create,
// where Cilium needs the API to start and kube-vip needs Cilium to route to it.
func CiliumHelmConfig(spec v1alpha1.ClusterSpec) string {
	var b strings.Builder
	b.WriteString(managedFileHeader + "\n")
	b.WriteString(`apiVersion: helm.cattle.io/v1
kind: HelmChartConfig
metadata:
  name: rke2-cilium
  namespace: kube-system
spec:
  valuesContent: |-
    kubeProxyReplacement: true
    k8sServiceHost: 127.0.0.1
    k8sServicePort: 6443
`)
	if WantsGatewayAPI(spec) {
		// The L7 proxy is what actually terminates a Gateway listener; enabling
		// the API without it produces Gateways that are accepted and never
		// serve anything.
		b.WriteString("    gatewayAPI:\n      enabled: true\n")
		if NodeIPGateways(spec) {
			// Envoy binds the listener ports in the node's own network
			// namespace, which is what makes <node>:<port> the endpoint with no
			// second IP on the segment. The generated Service still exists but
			// nothing waits on it for an address.
			//
			// A subset of nodes is expressed as a label selector; the gateway
			// phase labels the nodes the document lists.
			b.WriteString("      hostNetwork:\n        enabled: true\n")
			if gatewaysNameNodeSubset(spec) {
				b.WriteString("        nodes:\n          matchLabels:\n")
				b.WriteString("            " + GatewayNodeLabel + ": \"true\"\n")
			}
		}
		b.WriteString("    envoy:\n      enabled: true\n")
		if NodeIPGateways(spec) && hasPrivilegedListener(spec) {
			// A host-networked envoy binding a port below 1024 needs the
			// capability to do it, and it takes both halves: the container has
			// to be granted NET_BIND_SERVICE (the list replaces the chart's
			// defaults, so those are restated), and keepCapNetBindService has
			// to tell cilium-envoy-starter to retain it -- the starter drops
			// every capability it was not told to keep, so without the second
			// half CapBnd holds the bit, CapEff is zero, and envoy NACKs the
			// listener forever with "cannot bind: Permission denied". Both
			// halves found live, one after the other.
			b.WriteString("      securityContext:\n        capabilities:\n")
			b.WriteString("          keepCapNetBindService: true\n")
			b.WriteString("          envoy:\n")
			b.WriteString("            - NET_ADMIN\n            - SYS_ADMIN\n            - NET_BIND_SERVICE\n")
		}
		b.WriteString("    l7Proxy: true\n")
	}
	if len(spec.Kubernetes.Dataplane.LoadBalancerPool) > 0 && !usesBGP(spec) {
		// Without BGP something has to answer ARP for the pool addresses, or
		// they are allocated and unreachable -- a Service that reports an
		// external IP nothing can connect to.
		b.WriteString("    l2announcements:\n      enabled: true\n")
		// The defaults are tuned for a large cluster and produce a lease that
		// takes minutes to move; on a handful of nodes that is a long outage
		// for a failover nobody notices otherwise.
		b.WriteString("    k8sClientRateLimit:\n      qps: 20\n      burst: 40\n")
	}
	return b.String()
}

// LoadBalancerPool renders the addresses Cilium hands to Services.
//
// Empty when the document reserves none: an empty pool is not the same as no
// pool, and creating one would let a Service sit Pending forever with nothing
// saying why.
func LoadBalancerPool(spec v1alpha1.ClusterSpec) string {
	pools := spec.Kubernetes.Dataplane.LoadBalancerPool
	if len(pools) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(managedFileHeader + "\n")
	b.WriteString(`apiVersion: cilium.io/v2
kind: CiliumLoadBalancerIPPool
metadata:
  name: malmok
spec:
  blocks:
`)
	for _, cidr := range pools {
		b.WriteString("    - cidr: " + yamlString(strings.TrimSpace(cidr)) + "\n")
	}
	return b.String()
}

// L2AnnouncementPolicy makes the pool addresses answer on the wire.
//
// Only for ARP. BGP means the customer's routers have been configured to peer,
// which is not something this tool can do or verify from here, and announcing
// on both would produce two devices claiming one address.
func L2AnnouncementPolicy(spec v1alpha1.ClusterSpec) string {
	if len(spec.Kubernetes.Dataplane.LoadBalancerPool) == 0 || usesBGP(spec) {
		return ""
	}

	var b strings.Builder
	b.WriteString(managedFileHeader + "\n")
	b.WriteString(`apiVersion: cilium.io/v2alpha1
kind: CiliumL2AnnouncementPolicy
metadata:
  name: malmok
spec:
  # Announced from the control plane only. A worker answering for a service
  # address it does not host sends traffic on an extra hop for no reason.
  nodeSelector:
    matchLabels:
      node-role.kubernetes.io/control-plane: "true"
  externalIPs: true
  loadBalancerIPs: true
`)
	return b.String()
}

// usesBGP reports whether the document announces routes rather than answering
// ARP.
func usesBGP(spec v1alpha1.ClusterSpec) bool {
	v := spec.Topology.VIP
	return v != nil && strings.EqualFold(v.Mode, "bgp")
}

func yamlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// GatewayNodeLabel marks the nodes a node-ips gateway answers on, when the
// document names a subset rather than every node.
const GatewayNodeLabel = "malmok.io/gateway"

// NodeIPGateways reports whether any gateway answers on node addresses.
func NodeIPGateways(spec v1alpha1.ClusterSpec) bool {
	for _, gw := range spec.Gateway.Gateways {
		if gw.Exposure == v1alpha1.ExposureNodeIPs {
			return true
		}
	}
	return false
}

// gatewaysNameNodeSubset reports whether any node-ips gateway restricts which
// nodes answer. Cilium's host networking is cluster-wide configuration, so one
// subset means the selector -- and the labels the gateway phase writes --
// decide for all of them.
func gatewaysNameNodeSubset(spec v1alpha1.ClusterSpec) bool {
	for _, gw := range spec.Gateway.Gateways {
		if gw.Exposure == v1alpha1.ExposureNodeIPs && len(gw.NodeIPs) > 0 {
			return true
		}
	}
	return false
}

// hasPrivilegedListener reports whether any gateway listens below 1024, which
// is where binding in the host namespace needs NET_BIND_SERVICE.
func hasPrivilegedListener(spec v1alpha1.ClusterSpec) bool {
	for _, gw := range spec.Gateway.Gateways {
		for _, l := range gw.Listeners {
			if l.Port > 0 && l.Port < 1024 {
				return true
			}
		}
	}
	return false
}
