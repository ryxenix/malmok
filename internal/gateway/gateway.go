// Package gateway builds the l2-gateway phase.
//
// It creates Gateways, their listeners, the TLS Secrets those listeners serve,
// and the contract ConfigMap application charts read to find them.
//
// It does not create HTTPRoutes (ADR-006). A route belongs to the application
// that owns it: routes created here would be owned by the installer, and every
// application change would then need the installer run again.
package gateway

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ryxen/malmok/api/v1alpha1"
	"github.com/ryxen/malmok/internal/cert"
	"github.com/ryxen/malmok/internal/dataplane"
	"github.com/ryxen/malmok/internal/engine"
	"github.com/ryxen/malmok/internal/exec"
	"github.com/ryxen/malmok/internal/rke2"
)

// Phase is where these steps are filed.
const Phase = "l2-gateway"

// DefaultNamespace is where Gateways live unless the document says otherwise.
const DefaultNamespace = "gateway-system"

// DefaultContractConfigMap is the name application charts look for.
const DefaultContractConfigMap = "platform-gateway-contract"

const managedFileHeader = "# Managed by malmok. Changes here are overwritten on the next apply."

// fingerprintAnnotation is how a TLS Secret says which certificate it holds.
//
// It is what the check compares, so the private key never has to be read back
// off the cluster to decide whether the Secret is current.
const fingerprintAnnotation = "platform.ryxen.dev/fingerprint"

var kubectl = fmt.Sprintf("export PATH=$PATH:%s\nexport KUBECONFIG=%s\n", rke2.BinDir, rke2.Kubeconfig)

// Options are what the steps need beyond the document.
type Options struct {
	// Bundles are the assembled certificate bundles, keyed by the secretRef the
	// listeners reference. Assembly belongs to the caller: reading a SourceRef
	// needs the document's directory and its secret policy.
	Bundles map[string]*cert.Bundle

	// Issuer is the ClusterIssuer that signs for listeners which name no
	// Secret of their own. Empty means the document issues nothing in the
	// cluster, and such listeners get no certificate from here.
	Issuer string

	Timeout time.Duration
}

func (o Options) timeout() time.Duration {
	if o.Timeout <= 0 {
		return 5 * time.Minute
	}
	return o.Timeout
}

// Steps returns the l2-gateway catalogue.
func Steps(runner exec.Runner, spec v1alpha1.ClusterSpec, o Options) []engine.Step {
	if len(spec.Gateway.Gateways) == 0 {
		return nil
	}

	host := runner.Host()
	add := func(s *engine.ShellStep) engine.Step {
		s.Phase, s.Runner, s.Host = Phase, runner, host
		return s
	}

	var steps []engine.Step

	// The TLS Secrets come first: a Gateway whose listener references a Secret
	// that does not exist yet reports Programmed=False, and the reason names
	// the reference rather than the missing material.
	for _, ref := range sortedKeys(o.Bundles) {
		steps = append(steps, add(secretStep(ref, namespaceOf(spec), o.Bundles[ref])))
	}

	// The namespace before everything that lives in it -- the Secrets above
	// go to a fixed namespace RKE2 already has, but the certificates below do
	// not.
	steps = append(steps, add(rke2.ManifestStep(Phase, "namespaces", namespaceFile,
		NamespaceManifest(spec), "namespace "+namespaceOf(spec), o.timeout())))

	// Certificates the cluster signs, before the Gateway that terminates with
	// them. Same reason the supplied bundles come first: a listener pointing
	// at a Secret that does not exist yet reports a reference error rather
	// than a missing certificate.
	if body := ListenerCertificates(spec, o.Issuer); body != "" {
		steps = append(steps,
			add(rke2.ManifestStep(Phase, "listener-certs", listenerCertFile, body,
				firstCertificate(spec), o.timeout())),
			add(listenerCertsReadyStep(spec, o)),
		)
	}

	steps = append(steps,
		add(rke2.ManifestStep(Phase, "gateways", gatewayFile, GatewayManifest(spec),
			"gateway -n "+namespaceOf(spec)+" "+spec.Gateway.Gateways[0].Name, o.timeout())),
		add(rke2.ManifestStep(Phase, "contract", contractFile, ContractManifest(spec),
			fmt.Sprintf("configmap -n %s %s", namespaceOf(spec), contractName(spec)), o.timeout())),
	)

	// The end state worth waiting for depends on where the address comes from.
	// A load-balanced gateway is done when the controller reports Programmed
	// with a pool address. A node-ips gateway gets no pool address and Cilium
	// will not call it Programmed without one, so the end state is the thing
	// itself: the generated Service carries the nodes' addresses as
	// externalIPs, which is what makes <node>:<port> reach the listeners.
	labelled := false
	for _, gw := range spec.Gateway.Gateways {
		if gw.Exposure == v1alpha1.ExposureNodeIPs {
			if len(gw.NodeIPs) > 0 && !labelled {
				steps = append(steps, add(nodeLabelStep(spec)))
				labelled = true
			}
			steps = append(steps, add(answersStep(spec, gw, o)))
			continue
		}
		steps = append(steps, add(programmedStep(gw, namespaceOfGateway(gw), o)))
	}
	return steps
}

// gatewayNodeIPs resolves which addresses a node-ips gateway answers on.
//
// The document's list when it gives one; every node otherwise. The advertised
// nodeIP wins over the SSH address, because it is the address the document
// says the rest of the network reaches this node on.
func gatewayNodeIPs(spec v1alpha1.ClusterSpec, gw v1alpha1.Gateway) []string {
	if len(gw.NodeIPs) > 0 {
		return gw.NodeIPs
	}
	var out []string
	for _, n := range append(append([]v1alpha1.NodeSpec{},
		spec.Topology.Servers...), spec.Topology.Agents...) {
		addr := n.NodeIP
		if addr == "" {
			addr = n.Host
		}
		out = append(out, addr)
	}
	return out
}

// nodeLabelStep marks which nodes answer for the node-ips gateways.
//
// Cilium's host networking takes a label selector, so a document that names a
// subset needs the labels to exist -- and the nodes it does not name need the
// label gone, or a node removed from the document keeps answering.
func nodeLabelStep(spec v1alpha1.ClusterSpec) *engine.ShellStep {
	chosen := map[string]bool{}
	for _, gw := range spec.Gateway.Gateways {
		if gw.Exposure != v1alpha1.ExposureNodeIPs {
			continue
		}
		for _, ip := range gw.NodeIPs {
			chosen[ip] = true
		}
	}
	ips := make([]string, 0, len(chosen))
	for ip := range chosen {
		ips = append(ips, ip)
	}
	sort.Strings(ips)
	list := strings.Join(ips, " ")

	// Node names are resolved from addresses at run time, for the same reason
	// the join phase does it: the address is what the document says, and the
	// name is whatever the node happened to call itself.
	// {"\n"} is an escape kubectl's jsonpath parser reads; a real newline
	// inside the quotes is "unterminated quoted string" and produces nothing.
	// It used to be a real newline, behind 2>/dev/null, so the loop below ran
	// over an empty list, the step exited 0, and three retries reported that
	// the target state was not reached without ever saying why.
	byAddr := `kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name}{" "}{range .status.addresses[?(@.type=="InternalIP")]}{.address}{end}{"\n"}{end}'`

	return &engine.ShellStep{
		Name: "gateway-nodes",
		Check: kubectl + fmt.Sprintf(`want=%s
have=$(kubectl get nodes -l %s -o jsonpath='{range .items[*]}{range .status.addresses[?(@.type=="InternalIP")]}{.address}{end}{"\n"}{end}' | sort | tr '
' ' ' | sed 's/ $//')
[ "$have" = "$want" ] || { echo "the gateway answers on '$have', the document says '$want'"; exit 1; }
echo "the gateway nodes are $want"`, shellQuote(list), dataplane.GatewayNodeLabel),

		// Read first, then loop. A failing kubectl inside a pipeline leaves the
		// loop with nothing to read and the step with a zero exit; in an
		// assignment, set -e stops here and the reason is on stderr.
		Do: kubectl + fmt.Sprintf(`set -e
nodes=$(%s)
[ -n "$nodes" ] || { echo "the cluster reported no nodes to label"; exit 1; }
echo "$nodes" | while read -r name addr; do
  [ -n "$name" ] || continue
  case " %s " in
    *" $addr "*) kubectl label node "$name" %s=true --overwrite >/dev/null ;;
    *) kubectl label node "$name" %s- >/dev/null 2>&1 || true ;;
  esac
done`, byAddr, list, dataplane.GatewayNodeLabel, dataplane.GatewayNodeLabel),

		Satisfied: "%s",
		Missing:   "%s",
	}
}

// answersStep waits until the gateway actually answers on the node addresses.
//
// Not the Gateway's Programmed condition, and not the generated Service:
// Cilium never gives a host-networked gateway a pool address, and it owns the
// Service too completely to carry anything of ours -- a patched externalIPs is
// stripped on the next reconcile, verified the hard way. What has to be true is
// the thing itself: every address the document names answers on the listener
// port. Any HTTP status counts, because a 404 from a gateway with no routes is
// the gateway working.
func answersStep(spec v1alpha1.ClusterSpec, gw v1alpha1.Gateway, o Options) *engine.ShellStep {
	ips := gatewayNodeIPs(spec, gw)
	port := 80
	scheme := "http"
	for _, l := range gw.Listeners {
		port = l.Port
		if l.Protocol != v1alpha1.ListenerHTTP {
			scheme = "https"
		}
		break
	}

	var probes strings.Builder
	for _, ip := range ips {
		fmt.Fprintf(&probes, `code=$(curl -sk -o /dev/null -w '%%{http_code}' --max-time 5 %s://%s:%d/ 2>/dev/null)
[ -n "$code" ] && [ "$code" != 000 ] || { echo "%s:%d does not answer"; exit 1; }
`, scheme, ip, port, ip, port)
	}

	return &engine.ShellStep{
		Name: "answers-" + gw.Name,
		Check: probes.String() +
			fmt.Sprintf(`echo "%s answers on %s port %d"`, gw.Name, strings.Join(ips, ", "), port),

		Do: fmt.Sprintf(`deadline=$(( $(date +%%s) + %d ))
while [ "$(date +%%s)" -lt "$deadline" ]; do
%s
  exit 0
done
echo "the gateway %s never answered on every node address (%s port %d)"
exit 1`, int(o.timeout().Seconds()),
			indentProbes(ips, scheme, port), gw.Name, strings.Join(ips, ", "), port),

		Satisfied: "%s",
		Missing:   "%s",
		DoTimeout: o.timeout() + time.Minute,
		Attempts:  1,
	}
}

// indentProbes renders the wait-loop body: probe every address, clear ok and
// pause on the first that does not answer.
func indentProbes(ips []string, scheme string, port int) string {
	var b strings.Builder
	for _, ip := range ips {
		// `continue`, not `break`: there is exactly one loop here, and break
		// would leave it -- which turned a probe that had not answered yet into
		// an instant failure of the whole wait, verified live.
		fmt.Fprintf(&b, `  code=$(curl -sk -o /dev/null -w '%%{http_code}' --max-time 5 %s://%s:%d/ 2>/dev/null)
  if [ -z "$code" ] || [ "$code" = 000 ]; then sleep 5; continue; fi
`, scheme, ip, port)
	}
	return b.String()
}

// Files this phase writes.
const (
	gatewayFile      = rke2.ManifestDir + "/malmok-gateways.yaml"
	contractFile     = rke2.ManifestDir + "/malmok-gateway-contract.yaml"
	listenerCertFile = rke2.ManifestDir + "/malmok-listener-certs.yaml"
	namespaceFile    = rke2.ManifestDir + "/malmok-gateway-namespaces.yaml"
)

// secretStep installs one TLS Secret.
//
// Applied from stdin rather than written into the manifest directory, and the
// difference matters: a manifest file holds the private key on the node's disk
// for the life of the cluster, readable by anything that can read the
// filesystem. Applying it once puts the key in etcd, where it was going anyway,
// and leaves nothing behind.
//
// The cost is that RKE2 does not reconcile it -- which is the right trade: a
// Secret does not drift on its own, and re-running the phase reinstates it.
func secretStep(ref, namespace string, b *cert.Bundle) *engine.ShellStep {
	fingerprint := b.Fingerprint
	crt := base64.StdEncoding.EncodeToString(b.ChainPEM)
	key := base64.StdEncoding.EncodeToString(b.KeyPEM)

	var extra string
	if len(b.CAPEM) > 0 {
		// A private CA has to travel with the bundle: clients that verify
		// against it need it, and it is not in any public trust store.
		extra = "\n  ca.crt: " + base64.StdEncoding.EncodeToString(b.CAPEM)
	}

	manifest := fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: %s
  annotations:
    %s: %s
type: kubernetes.io/tls
data:
  tls.crt: %s
  tls.key: %s%s
`, ref, namespace, fingerprintAnnotation, yamlString(fingerprint), crt, key, extra)

	read := fmt.Sprintf(`kubectl -n %s get secret %s -o jsonpath='{.metadata.annotations.%s}' 2>/dev/null`,
		namespace, ref, strings.ReplaceAll(fingerprintAnnotation, ".", `\.`))

	return &engine.ShellStep{
		Name: "tls-" + ref,
		// The fingerprint is the observable. Comparing it means a renewed
		// certificate is detected without the key ever being read back, and the
		// step's evidence stays safe to put in an audit report.
		Check: kubectl + fmt.Sprintf(`have=$(%s)
[ -n "$have" ] || { echo "the TLS secret %s/%s does not exist"; exit 1; }
[ "$have" = %s ] || { echo "%s/%s holds a different certificate ($have)"; exit 1; }
echo "%s/%s holds the certificate the document assembled"`,
			read, namespace, ref, shellQuote(fingerprint), namespace, ref, namespace, ref),

		// umask keeps the temporary file unreadable, and it is removed whether
		// or not kubectl succeeds.
		Do: kubectl + fmt.Sprintf(`set -e
kubectl create namespace %s --dry-run=client -o yaml | kubectl apply -f - >/dev/null
umask 077
t=$(mktemp)
trap 'rm -f "$t"' EXIT
printf '%%s' %s > "$t"
kubectl apply -f "$t" >/dev/null`, namespace, shellQuote(manifest)),

		Satisfied: "%s",
		Missing:   "%s",
	}
}

// programmedStep waits for the controller to serve the Gateway.
//
// Accepted is not enough. A Gateway is Accepted when the controller agrees to
// implement it and Programmed when it actually has an address and a listener
// bound -- and the gap between them is where a missing load balancer pool or a
// certificate the listener cannot use shows up.
func programmedStep(gw v1alpha1.Gateway, namespace string, o Options) *engine.ShellStep {
	read := fmt.Sprintf(
		`kubectl -n %s get gateway %s -o jsonpath='{range .status.conditions[?(@.type=="Programmed")]}{.status}{end}' 2>/dev/null`,
		namespace, gw.Name)
	addr := fmt.Sprintf(`kubectl -n %s get gateway %s -o jsonpath='{.status.addresses[0].value}' 2>/dev/null`,
		namespace, gw.Name)

	return &engine.ShellStep{
		Name: "programmed-" + gw.Name,
		Check: kubectl + fmt.Sprintf(`s=$(%s)
[ "$s" = True ] || { echo "the gateway %s/%s is not Programmed (status '$s')"; exit 1; }
echo "%s/%s is Programmed at $(%s)"`, read, namespace, gw.Name, namespace, gw.Name, addr),

		Do: kubectl + fmt.Sprintf(`deadline=$(( $(date +%%s) + %d ))
while [ "$(date +%%s)" -lt "$deadline" ]; do
  [ "$(%s)" = True ] && exit 0
  sleep 5
done
echo "the gateway %s/%s never became Programmed. It reports:"
kubectl -n %s get gateway %s -o jsonpath='{range .status.conditions[*]}{.type}={.status} ({.reason}: {.message}){"\n"}{end}' 2>&1
kubectl -n %s get gateway %s -o wide 2>&1 | tail -3
exit 1`, int(o.timeout().Seconds()), read, namespace, gw.Name, namespace, gw.Name, namespace, gw.Name),

		Satisfied: "%s",
		Missing:   "%s",
		DoTimeout: o.timeout() + time.Minute,
	}
}

// ---------------------------------------------------------------------------
// Manifests
// ---------------------------------------------------------------------------

// NamespaceManifest renders the namespaces the gateways live in.
//
// Its own file, applied before anything that goes inside them. It used to be
// the first object of the gateway manifest, which was fine until something
// else in the phase needed the namespace first: a listener's Certificate is
// rejected outright for a namespace nobody has created, and the phase failed
// on its own ordering.
func NamespaceManifest(spec v1alpha1.ClusterSpec) string {
	seen := map[string]bool{}
	var b strings.Builder
	b.WriteString(managedFileHeader + "\n")
	for _, gw := range spec.Gateway.Gateways {
		ns := namespaceOfGateway(gw)
		if seen[ns] {
			continue
		}
		seen[ns] = true
		b.WriteString("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: " + yamlString(ns) + "\n---\n")
	}
	return b.String()
}

// GatewayManifest renders the namespace and every Gateway the document names.
func GatewayManifest(spec v1alpha1.ClusterSpec) string {
	var b strings.Builder
	b.WriteString(managedFileHeader + "\n")

	seen := map[string]bool{}
	for _, gw := range spec.Gateway.Gateways {
		ns := namespaceOfGateway(gw)
		if !seen[ns] {
			seen[ns] = true
			b.WriteString("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: " + yamlString(ns) + "\n---\n")
		}
	}

	class := gatewayClass(spec)
	for i, gw := range spec.Gateway.Gateways {
		if i > 0 {
			b.WriteString("---\n")
		}
		b.WriteString("apiVersion: gateway.networking.k8s.io/v1\nkind: Gateway\nmetadata:\n")
		b.WriteString("  name: " + yamlString(gw.Name) + "\n")
		b.WriteString("  namespace: " + yamlString(namespaceOfGateway(gw)) + "\n")
		if gw.Zone != "" {
			b.WriteString("  labels:\n    platform.ryxen.dev/zone: " + yamlString(gw.Zone) + "\n")
		}
		b.WriteString("spec:\n  gatewayClassName: " + yamlString(class) + "\n")

		// A pinned address is the whole reason PF-612 asks for one: the DNS
		// record has to be requested before the install, so the address cannot
		// be whatever LB-IPAM happens to allocate.
		if gw.Address != "" {
			b.WriteString("  addresses:\n    - type: IPAddress\n      value: " + yamlString(gw.Address) + "\n")
		}

		b.WriteString("  listeners:\n")
		for _, l := range gw.Listeners {
			writeListener(&b, gw, l)
		}
	}
	return b.String()
}

// writeListener renders one listener.
func writeListener(b *strings.Builder, gw v1alpha1.Gateway, l v1alpha1.ListenerSpec) {
	b.WriteString("    - name: " + yamlString(l.Name) + "\n")
	b.WriteString("      protocol: " + yamlString(string(l.Protocol)) + "\n")
	b.WriteString("      port: " + fmt.Sprint(l.Port) + "\n")
	if l.Hostname != "" {
		b.WriteString("      hostname: " + yamlString(l.Hostname) + "\n")
	}

	if l.Protocol == v1alpha1.ListenerHTTPS {
		b.WriteString("      tls:\n        mode: Terminate\n")
		if ref := ListenerSecret(gw, l); ref != "" {
			b.WriteString("        certificateRefs:\n          - kind: Secret\n            name: " +
				yamlString(ref) + "\n")
		}
	}
	if l.TLS != nil && l.Protocol == v1alpha1.ListenerTLSPassthrough {
		// Passthrough terminates at the backend, so the gateway holds no
		// certificate and must not be given one.
		b.WriteString("      tls:\n        mode: Passthrough\n")
	}

	writeAllowedRoutes(b, gw)
}

// writeAllowedRoutes renders the isolation boundary.
//
// Which namespaces may attach a route to a gateway is the whole of the
// multi-tenant question: "all" means any namespace can publish on the
// customer's public gateway, which is convenient and is the thing a security
// review objects to. The default is the gateway's own namespace, so a document
// that says nothing gets the closed answer rather than the open one.
func writeAllowedRoutes(b *strings.Builder, gw v1alpha1.Gateway) {
	b.WriteString("      allowedRoutes:\n        namespaces:\n")

	switch strings.ToLower(strings.TrimSpace(gw.RouteNamespaces)) {
	case "all":
		b.WriteString("          from: All\n")

	case "selector":
		b.WriteString("          from: Selector\n          selector:\n            matchLabels:\n")
		keys := make([]string, 0, len(gw.NamespaceSelector))
		for k := range gw.NamespaceSelector {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString("              " + k + ": " + yamlString(gw.NamespaceSelector[k]) + "\n")
		}

	default:
		b.WriteString("          from: Same\n")
	}
}

// ContractManifest renders what application charts read to find the gateway.
//
// ADR-006: this tool does not create routes, so a chart has to be told the
// gateway's name, namespace and domain. Without the contract every chart
// hardcodes them, and moving a gateway means editing every chart.
func ContractManifest(spec v1alpha1.ClusterSpec) string {
	ns := namespaceOf(spec)

	var b strings.Builder
	b.WriteString(managedFileHeader + "\n")
	b.WriteString("apiVersion: v1\nkind: ConfigMap\nmetadata:\n")
	b.WriteString("  name: " + yamlString(contractName(spec)) + "\n")
	b.WriteString("  namespace: " + yamlString(ns) + "\n")
	b.WriteString("data:\n")
	b.WriteString("  gatewayClassName: " + yamlString(gatewayClass(spec)) + "\n")
	b.WriteString("  domainSuffix: " + yamlString(spec.Gateway.DomainSuffix) + "\n")

	names := make([]string, 0, len(spec.Gateway.Gateways))
	for _, gw := range spec.Gateway.Gateways {
		names = append(names, gw.Name)
		prefix := "  gateway." + gw.Name + "."
		b.WriteString(prefix + "namespace: " + yamlString(namespaceOfGateway(gw)) + "\n")
		if gw.Address != "" {
			b.WriteString(prefix + "address: " + yamlString(gw.Address) + "\n")
		}
		// A node-ips gateway's address is the nodes' own, and the contract is
		// where a DNS request or an app chart learns what to point at.
		if gw.Exposure == v1alpha1.ExposureNodeIPs {
			b.WriteString(prefix + "address: " +
				yamlString(strings.Join(gatewayNodeIPs(spec, gw), ",")) + "\n")
		}
		if gw.Zone != "" {
			b.WriteString(prefix + "zone: " + yamlString(gw.Zone) + "\n")
		}
	}
	b.WriteString("  gateways: " + yamlString(strings.Join(names, ",")) + "\n")
	return b.String()
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// gatewayClass is what the document asked for, or what the preset implies.
//
// ADR-004 makes the preset the single input, so deriving it here is what stops
// a document naming a class no controller implements.
func gatewayClass(spec v1alpha1.ClusterSpec) string {
	if c := strings.TrimSpace(spec.Gateway.GatewayClass); c != "" {
		return c
	}
	preset := string(spec.Kubernetes.Dataplane.Preset)
	switch {
	case strings.HasPrefix(preset, "cilium"):
		return "cilium"
	case strings.Contains(preset, "traefik"):
		return "traefik"
	}
	return ""
}

func namespaceOf(spec v1alpha1.ClusterSpec) string {
	for _, gw := range spec.Gateway.Gateways {
		if gw.Namespace != "" {
			return gw.Namespace
		}
	}
	return DefaultNamespace
}

func namespaceOfGateway(gw v1alpha1.Gateway) string {
	if gw.Namespace != "" {
		return gw.Namespace
	}
	return DefaultNamespace
}

func contractName(spec v1alpha1.ClusterSpec) string {
	if n := strings.TrimSpace(spec.Gateway.ContractConfigMap); n != "" {
		return n
	}
	return DefaultContractConfigMap
}

// SecretRefs lists the TLS secrets the listeners reference, in a stable order.
//
// The caller assembles a bundle for each; a listener naming a secret nobody
// assembled is a Gateway that will never be Programmed.
func SecretRefs(spec v1alpha1.ClusterSpec) []string {
	seen := map[string]bool{}
	var out []string
	for _, gw := range spec.Gateway.Gateways {
		for _, l := range gw.Listeners {
			if l.Protocol != v1alpha1.ListenerHTTPS {
				continue
			}
			ref := ListenerSecret(gw, l)
			if ref != "" && !seen[ref] {
				seen[ref] = true
				out = append(out, ref)
			}
		}
	}
	sort.Strings(out)
	return out
}

// ListenerCertificates renders a cert-manager Certificate for every HTTPS
// listener the cluster signs for.
//
// The Secret a Certificate writes and the Secret its Gateway references are
// the same string on purpose: the schema says a listener without a tls block
// inherits the cluster's pki.mode, and nothing implemented that -- the
// Gateway came up referencing a Secret nobody created, the controller called
// it Programmed, and every handshake was reset.
//
// Empty when nothing issues in the cluster: byo-cert supplies its own
// material and `none` has no domain yet.
func ListenerCertificates(spec v1alpha1.ClusterSpec, issuer string) string {
	if issuer == "" {
		return ""
	}
	listeners := IssuedListeners(spec)
	if len(listeners) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(managedFileHeader + "\n")
	for i, l := range listeners {
		if i > 0 {
			b.WriteString("---\n")
		}
		b.WriteString("apiVersion: cert-manager.io/v1\nkind: Certificate\nmetadata:\n")
		b.WriteString("  name: " + yamlString(l.Secret) + "\n")
		b.WriteString("  namespace: " + yamlString(l.Namespace) + "\n")
		b.WriteString("spec:\n")
		b.WriteString("  secretName: " + yamlString(l.Secret) + "\n")
		b.WriteString("  dnsNames:\n")
		for _, host := range l.Hostnames {
			b.WriteString("    - " + yamlString(host) + "\n")
		}
		b.WriteString("  issuerRef:\n    kind: ClusterIssuer\n    name: " + yamlString(issuer) + "\n")
	}
	return b.String()
}

// firstCertificate names one object the manifest step can look for, which is
// what tells it the cluster took the file rather than merely holding it.
func firstCertificate(spec v1alpha1.ClusterSpec) string {
	l := IssuedListeners(spec)
	if len(l) == 0 {
		return ""
	}
	return "certificate -n " + l[0].Namespace + " " + l[0].Secret
}

// listenerCertsReadyStep waits for the certificates to be signed.
//
// A Certificate object is a request; the Secret appears when cert-manager has
// signed it, and the listener terminates against that Secret. "The object
// exists" is not the state anything downstream needs.
func listenerCertsReadyStep(spec v1alpha1.ClusterSpec, o Options) *engine.ShellStep {
	var checks []string
	for _, l := range IssuedListeners(spec) {
		checks = append(checks, fmt.Sprintf(
			`s=$(kubectl -n %s get certificate %s -o jsonpath='{range .status.conditions[?(@.type=="Ready")]}{.status}{end}' 2>/dev/null || true)
[ "$s" = True ] || { echo "the certificate %s/%s is not Ready (status '$s')"; exit 1; }`,
			l.Namespace, l.Secret, l.Namespace, l.Secret))
	}
	ready := strings.Join(checks, "\n") + "\necho \"every listener certificate is signed\""
	quiet := "(" + strings.Join(checks, " && ") + ") >/dev/null 2>&1"

	return &engine.ShellStep{
		Name:  "listener-certs-ready",
		Check: kubectl + ready,
		Do: kubectl + fmt.Sprintf(`deadline=$(( $(date +%%s) + %d ))
started=$(date +%%s)
while [ "$(date +%%s)" -lt "$deadline" ]; do
  if %s
  then exit 0
  fi
  # Signing is usually seconds and occasionally not: an issuer that cannot
  // sign looks exactly like one that has not signed yet, until this says
  # which certificates are still waiting.
  echo "waiting $(( $(date +%%s) - started ))s: $(kubectl get certificate -A --no-headers 2>/dev/null | awk '$3!="True" {print $1"/"$2}' | tr '\n' ' ')"
  sleep 5
done
echo "a listener certificate was never signed. The cluster reports:"
kubectl get certificate -A 2>&1 | tail -5
kubectl -n cert-manager logs -l app.kubernetes.io/name=cert-manager --tail=20 2>&1 | tail -20
exit 1`, int(o.timeout().Seconds()), quiet),

		Satisfied: "%s",
		Missing:   "%s",
		DoTimeout: o.timeout() + time.Minute,
		Attempts:  1,
	}
}

// IssuedListener is one HTTPS listener whose certificate the cluster issues.
type IssuedListener struct {
	// Secret is where the certificate has to land: the same name the Gateway
	// references, or the listener terminates against nothing.
	Secret string
	// Namespace is the Gateway's, because a Gateway may only reference a
	// Secret beside it without a ReferenceGrant.
	Namespace string
	// Hostnames are what the certificate must cover.
	Hostnames []string
}

// IssuedListeners lists the HTTPS listeners that need a certificate issued in
// the cluster: the ones that name no Secret of their own and supply no
// material, which the schema says inherit the cluster's pki.mode.
//
// Exported for l2-pki, which owns issuance. The gateway phase must not issue
// anything itself -- it runs after PKI, and a Gateway that referenced a Secret
// its own phase created would have no way to wait for a certificate that
// takes a moment to sign.
func IssuedListeners(spec v1alpha1.ClusterSpec) []IssuedListener {
	var out []IssuedListener
	for _, gw := range spec.Gateway.Gateways {
		for _, l := range gw.Listeners {
			if l.Protocol != v1alpha1.ListenerHTTPS {
				continue
			}
			// A listener that names an existing Secret, or hands over its own
			// material, is already answered.
			if l.TLS != nil && (l.TLS.Source == v1alpha1.TLSFromSecret || l.TLS.Source == v1alpha1.TLSFromBYO) {
				continue
			}
			hosts := []string{l.Hostname}
			if l.Hostname == "" {
				hosts = []string{spec.Gateway.DomainSuffix}
			}
			out = append(out, IssuedListener{
				Secret:    ListenerSecret(gw, l),
				Namespace: namespaceOfGateway(gw),
				Hostnames: hosts,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Secret < out[j].Secret })
	return out
}

// ListenerSecret names the Secret an HTTPS listener terminates with.
//
// The document's name when it gives one, and a derived one otherwise -- a
// listener that omits the tls block inherits the cluster's pki.mode, and
// something still has to name the Secret that inheritance lands in. It was
// nameless before, so the listener got no certificateRefs, Cilium programmed
// the gateway anyway, and every handshake was reset by a listener holding no
// certificate. Found by building an HTTPS listener for the first time.
func ListenerSecret(gw v1alpha1.Gateway, l v1alpha1.ListenerSpec) string {
	if l.Protocol != v1alpha1.ListenerHTTPS {
		return ""
	}
	if l.TLS != nil && l.TLS.SecretRef != "" {
		return l.TLS.SecretRef
	}
	return gw.Name + "-" + l.Name + "-tls"
}

func sortedKeys(m map[string]*cert.Bundle) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func yamlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
