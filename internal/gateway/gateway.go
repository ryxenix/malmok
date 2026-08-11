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

	"platform.ryxen.dev/platformctl/api/v1alpha1"
	"platform.ryxen.dev/platformctl/internal/cert"
	"platform.ryxen.dev/platformctl/internal/engine"
	"platform.ryxen.dev/platformctl/internal/exec"
	"platform.ryxen.dev/platformctl/internal/rke2"
)

// Phase is where these steps are filed.
const Phase = "l2-gateway"

// DefaultNamespace is where Gateways live unless the document says otherwise.
const DefaultNamespace = "gateway-system"

// DefaultContractConfigMap is the name application charts look for.
const DefaultContractConfigMap = "platform-gateway-contract"

const managedFileHeader = "# Managed by platformctl. Changes here are overwritten on the next apply."

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

	steps = append(steps,
		add(rke2.ManifestStep(Phase, "gateways", gatewayFile, GatewayManifest(spec),
			"gateway -n "+namespaceOf(spec)+" "+spec.Gateway.Gateways[0].Name, o.timeout())),
		add(rke2.ManifestStep(Phase, "contract", contractFile, ContractManifest(spec),
			fmt.Sprintf("configmap -n %s %s", namespaceOf(spec), contractName(spec)), o.timeout())),
	)

	// Programmed is the end state worth waiting for: it means the controller
	// has an address and a listener it can actually serve on.
	for _, gw := range spec.Gateway.Gateways {
		steps = append(steps, add(programmedStep(gw, namespaceOfGateway(gw), o)))
	}
	return steps
}

// Files this phase writes.
const (
	gatewayFile  = rke2.ManifestDir + "/platformctl-gateways.yaml"
	contractFile = rke2.ManifestDir + "/platformctl-gateway-contract.yaml"
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

	if l.TLS != nil && l.Protocol == v1alpha1.ListenerHTTPS {
		b.WriteString("      tls:\n        mode: Terminate\n")
		if l.TLS.SecretRef != "" {
			b.WriteString("        certificateRefs:\n          - kind: Secret\n            name: " +
				yamlString(l.TLS.SecretRef) + "\n")
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
			if l.TLS == nil || l.TLS.SecretRef == "" || l.Protocol != v1alpha1.ListenerHTTPS {
				continue
			}
			if !seen[l.TLS.SecretRef] {
				seen[l.TLS.SecretRef] = true
				out = append(out, l.TLS.SecretRef)
			}
		}
	}
	sort.Strings(out)
	return out
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
