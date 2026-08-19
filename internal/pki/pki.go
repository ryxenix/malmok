// Package pki builds the l2-pki phase: cert-manager, the issuer the document
// asks for, and the trust distribution that makes a private CA usable.
//
// The phase is additive by grade but it decides something structural: whether
// certificates in this cluster are issued or supplied. `pki.mode` is the whole
// of that decision, and every screen here follows from it rather than from a
// separate switch somebody can set inconsistently.
//
// Nothing is applied with kubectl. Everything is written into RKE2's
// auto-deploying manifest directory, so the cluster reconciles it on restart
// without this tool present -- the difference between a cluster somebody can
// hand over and one that needs its installer kept around.
package pki

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	"platform.ryxen.dev/malmok/api/v1alpha1"
	"platform.ryxen.dev/malmok/internal/engine"
	"platform.ryxen.dev/malmok/internal/exec"
	"platform.ryxen.dev/malmok/internal/rke2"
)

// Phase is where these steps are filed.
const Phase = "l2-pki"

// Versions are pinned rather than tracked.
//
// An airgap bundle has to carry these exact images, and a floating version
// means the bundle and the manifest disagree about what is installed. Upgrading
// them is a decision with a changelog behind it, not something that happens
// because a chart repository moved.
const (
	CertManagerVersion  = "v1.21.1"
	TrustManagerVersion = "v0.24.0"
)

// Namespaces the phase creates.
const (
	Namespace       = "cert-manager"
	IssuerName      = "malmok"
	CASecretName    = "malmok-ca"
	TrustBundleName = "malmok-ca"
)

// Files this phase writes.
const (
	certManagerFile  = rke2.ManifestDir + "/malmok-cert-manager.yaml"
	issuerFile       = rke2.ManifestDir + "/malmok-issuer.yaml"
	trustManagerFile = rke2.ManifestDir + "/malmok-trust-manager.yaml"
	trustBundleFile  = rke2.ManifestDir + "/malmok-trust-bundle.yaml"
)

const managedFileHeader = "# Managed by malmok. Changes here are overwritten on the next apply."

// kubectl is the prelude the cluster-scoped steps need.
var kubectl = rke2.Kubectl

// Material is what the caller resolved from the document's SourceRefs.
//
// Resolution belongs to the loader: reading a SourceRef needs the document's
// directory and its secret policy, and the private key of an issuing CA is
// exactly the thing that must not be read by whatever happens to be nearest.
type Material struct {
	// RootCert is the private CA's root, distributed to clients.
	RootCert []byte
	// IntermediateCert and IntermediateKey are what cert-manager signs with.
	// The root key is never accepted (PF-706): it stays offline.
	IntermediateCert []byte
	IntermediateKey  []byte

	// ACMEToken is the DNS provider credential for a dns-01 solver.
	ACMEToken []byte
}

// Options are the timeouts and the airgap chart source.
type Options struct {
	// ChartRepo overrides where the charts come from. An airgapped site points
	// this at a mirror that already holds them.
	ChartRepo string
	Timeout   time.Duration
}

func (o Options) timeout() time.Duration {
	if o.Timeout <= 0 {
		return 10 * time.Minute
	}
	return o.Timeout
}

func (o Options) repo() string {
	if r := strings.TrimSpace(o.ChartRepo); r != "" {
		return r
	}
	return "https://charts.jetstack.io"
}

// Steps returns the l2-pki catalogue.
//
// Empty when the document issues nothing. `pki.mode: none` is a real choice --
// at a first build the service domain is frequently not decided, and installing
// an issuer for a name nobody uses leaves a component somebody has to keep
// alive for no reason.
func Steps(runner exec.Runner, spec v1alpha1.ClusterSpec, m Material, o Options) []engine.Step {
	mode := spec.PKI.Mode
	if mode == "" || mode == v1alpha1.PKINone {
		return nil
	}

	host := runner.Host()
	add := func(s *engine.ShellStep) engine.Step {
		s.Phase, s.Runner, s.Host = Phase, runner, host
		return s
	}

	steps := []engine.Step{
		add(rke2.ManifestStep(Phase, "cert-manager", certManagerFile,
			CertManagerChart(spec, o), "helmchart -n kube-system cert-manager", o.timeout())),
		add(rke2.AcceptsStep(Phase, "cert-manager-ready", IssuerManifest(spec),
			"cert-manager", o.timeout())),
	}

	// The CA secret comes before the issuer that references it: an Issuer
	// pointing at a Secret that does not exist reports Ready=False with a
	// reason about the reference rather than about the missing material.
	if mode == v1alpha1.PKIPrivateCA {
		if len(m.IntermediateCert) == 0 || len(m.IntermediateKey) == 0 {
			return append(steps, engine.FailedStep{
				StepID: Phase + "/material@" + host,
				Why: "pki.mode is private-ca and no intermediate certificate and key were supplied; " +
					"cert-manager signs with the intermediate, and the root key must stay offline (PF-706)",
			})
		}
		steps = append(steps, add(caSecretStep(m)))
	}
	if mode == v1alpha1.PKIACMEDNS01 && len(m.ACMEToken) > 0 {
		steps = append(steps, add(acmeTokenStep(spec, m)))
	}

	steps = append(steps,
		add(rke2.ManifestStep(Phase, "issuer", issuerFile, IssuerManifest(spec),
			"clusterissuer "+IssuerName, o.timeout())),
		add(issuerReadyStep(o)),
	)

	// Trust distribution is what makes a private CA usable by anything that did
	// not get it from the node's own store. Without it, in-cluster clients
	// reject the CA and the failure is an opaque x509 error.
	if wantsTrustBundle(spec) {
		steps = append(steps,
			add(rke2.ManifestStep(Phase, "trust-manager", trustManagerFile, TrustManagerChart(o),
				"helmchart -n kube-system trust-manager", o.timeout())),
			// The same wait cert-manager needs, for the same reason: the chart
			// is deployed before its CRD is registered, and a Bundle applied in
			// between fails with "no matches for kind".
			add(rke2.AcceptsStep(Phase, "trust-manager-ready", TrustBundle(spec),
				"trust-manager", o.timeout())),
			add(rke2.ManifestStep(Phase, "trust-bundle", trustBundleFile, TrustBundle(spec),
				"bundle "+TrustBundleName, o.timeout())),
		)
	}
	return steps
}

// wantsTrustBundle reports whether the cluster bundle is asked for.
//
// Only meaningful with a CA nothing already trusts: distributing a public CA's
// root to every namespace is noise, and the schema's three trust stores are
// independent for exactly this reason.
func wantsTrustBundle(spec v1alpha1.ClusterSpec) bool {
	t := spec.PKI.Trust.ClusterBundle
	if t == nil || !*t {
		return false
	}
	return spec.PKI.Mode == v1alpha1.PKIPrivateCA
}

// ---------------------------------------------------------------------------
// Steps
// ---------------------------------------------------------------------------

// caSecretStep installs the intermediate cert-manager signs with.
//
// Applied from a temporary file under umask rather than written into the
// manifest directory: that would keep an issuing CA's private key on the node's
// disk for the life of the cluster. Applying it once puts the key in etcd,
// where it was going anyway.
func caSecretStep(m Material) *engine.ShellStep {
	manifest := fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: %s
type: kubernetes.io/tls
data:
  tls.crt: %s
  tls.key: %s
`, CASecretName, Namespace, b64(m.IntermediateCert), b64(m.IntermediateKey))

	// The fingerprint of the certificate is the observable, so a rotated CA is
	// noticed without the key ever being read back off the cluster.
	fp := fingerprint(m.IntermediateCert)

	return &engine.ShellStep{
		Name: "ca-secret",
		Check: kubectl + fmt.Sprintf(`have=$(kubectl -n %s get secret %s -o jsonpath='{.data.tls\.crt}' 2>/dev/null | base64 -d 2>/dev/null | openssl x509 -noout -fingerprint -sha256 2>/dev/null | tr -d ' ')
[ -n "$have" ] || { echo "the issuing CA secret %s/%s does not exist"; exit 1; }
[ "$have" = %s ] || { echo "%s/%s holds a different certificate"; exit 1; }
echo "%s/%s holds the issuing CA the document supplied"`,
			Namespace, CASecretName, Namespace, CASecretName, shellQuote(fp),
			Namespace, CASecretName, Namespace, CASecretName),

		Do: kubectl + fmt.Sprintf(`set -e
kubectl create namespace %s --dry-run=client -o yaml | kubectl apply -f - >/dev/null
umask 077
t=$(mktemp)
trap 'rm -f "$t"' EXIT
printf '%%s' %s > "$t"
kubectl apply -f "$t" >/dev/null`, Namespace, shellQuote(manifest)),

		Satisfied: "%s",
		Missing:   "%s",
	}
}

// acmeTokenStep installs the DNS provider credential.
func acmeTokenStep(spec v1alpha1.ClusterSpec, m Material) *engine.ShellStep {
	provider := strings.TrimSpace(spec.PKI.ACME.DNSProvider)
	name := "malmok-acme-" + provider

	manifest := fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: %s
type: Opaque
data:
  api-token: %s
`, name, Namespace, b64(m.ACMEToken))

	return &engine.ShellStep{
		Name: "acme-credential",
		Check: kubectl + fmt.Sprintf(`kubectl -n %s get secret %s >/dev/null 2>&1 || {
  echo "the %s credential %s/%s does not exist"; exit 1; }
echo "%s/%s exists"`, Namespace, name, provider, Namespace, name, Namespace, name),
		Do: kubectl + fmt.Sprintf(`set -e
kubectl create namespace %s --dry-run=client -o yaml | kubectl apply -f - >/dev/null
umask 077
t=$(mktemp)
trap 'rm -f "$t"' EXIT
printf '%%s' %s > "$t"
kubectl apply -f "$t" >/dev/null`, Namespace, shellQuote(manifest)),
		Satisfied: "%s",
		Missing:   "%s",
	}
}

// issuerReadyStep waits for cert-manager to accept the issuer.
//
// A ClusterIssuer that exists is not one that can sign: Ready=False is where a
// CA secret with a mismatched key, or an ACME account that could not be
// registered, actually shows up.
func issuerReadyStep(o Options) *engine.ShellStep {
	read := fmt.Sprintf(
		`kubectl get clusterissuer %s -o jsonpath='{range .status.conditions[?(@.type=="Ready")]}{.status}{"|"}{.message}{end}' 2>/dev/null`,
		IssuerName)

	return &engine.ShellStep{
		Name: "issuer-ready",
		Check: kubectl + fmt.Sprintf(`s=$(%s)
case "$s" in
  "True|"*) echo "the %s issuer is ready" ;;
  "") echo "the %s issuer has no status yet"; exit 1 ;;
  *) echo "the %s issuer is not ready: ${s#*|}"; exit 1 ;;
esac`, read, IssuerName, IssuerName, IssuerName),

		Do: kubectl + fmt.Sprintf(`deadline=$(( $(date +%%s) + %d ))
while [ "$(date +%%s)" -lt "$deadline" ]; do
  case "$(%s)" in "True|"*) exit 0 ;; esac
  sleep 5
done
echo "the %s issuer never became ready. It reports:"
kubectl get clusterissuer %s -o jsonpath='{range .status.conditions[*]}{.type}={.status} ({.reason}: {.message}){"\n"}{end}' 2>&1
exit 1`, int(o.timeout().Seconds()), read, IssuerName, IssuerName),

		Satisfied: "%s",
		Missing:   "%s",
		DoTimeout: o.timeout() + time.Minute,
		Attempts:  1,
	}
}

// ---------------------------------------------------------------------------
// Manifests
// ---------------------------------------------------------------------------

// CertManagerChart renders the HelmChart RKE2 deploys.
//
// The resource is named `cert-manager`, not `malmok-cert-manager`: RKE2
// uses the resource name as the Helm release name, and the release name
// prefixes every object the chart creates. Prefixing it would give the
// deployments names that appear in no cert-manager runbook, and an operator
// following one would not find them. What marks the install as ours is the
// header on the manifest file, which is what PF-802 reads.
//
// The name must not be changed after a cluster has been built with it. Helm
// stamps its release name onto every object it owns, and cert-manager's CRDs
// are cluster-scoped -- so a rename leaves CRDs owned by a release that no
// longer exists, and every subsequent install fails with "cannot be imported
// into the current release" until somebody deletes them by hand. Deleting the
// namespace does not help; the CRDs are not in it.
func CertManagerChart(spec v1alpha1.ClusterSpec, o Options) string {
	var b strings.Builder
	b.WriteString(managedFileHeader + "\n")
	b.WriteString(`apiVersion: helm.cattle.io/v1
kind: HelmChart
metadata:
  name: cert-manager
  namespace: kube-system
spec:
  repo: ` + yamlString(o.repo()) + `
  chart: cert-manager
  version: ` + yamlString(CertManagerVersion) + `
  targetNamespace: ` + yamlString(Namespace) + `
  createNamespace: true
  valuesContent: |-
    crds:
      enabled: true
`)
	// A private registry has to be told to every chart, or the pull fails with
	// an opaque error that names an upstream host nobody configured.
	if r := strings.TrimSpace(spec.Registry.SystemDefaultRegistry); r != "" &&
		spec.Registry.Mode != v1alpha1.RegistryEmbedded {
		b.WriteString("    image:\n      repository: " + yamlString(r+"/jetstack/cert-manager-controller") + "\n")
		b.WriteString("    webhook:\n      image:\n        repository: " +
			yamlString(r+"/jetstack/cert-manager-webhook") + "\n")
		b.WriteString("    cainjector:\n      image:\n        repository: " +
			yamlString(r+"/jetstack/cert-manager-cainjector") + "\n")
		b.WriteString("    startupapicheck:\n      image:\n        repository: " +
			yamlString(r+"/jetstack/cert-manager-startupapicheck") + "\n")
	}
	return b.String()
}

// TrustManagerChart renders the trust-manager HelmChart.
func TrustManagerChart(o Options) string {
	return managedFileHeader + `
apiVersion: helm.cattle.io/v1
kind: HelmChart
metadata:
  name: trust-manager
  namespace: kube-system
spec:
  repo: ` + yamlString(o.repo()) + `
  chart: trust-manager
  version: ` + yamlString(TrustManagerVersion) + `
  targetNamespace: ` + yamlString(Namespace) + `
  createNamespace: true
  valuesContent: |-
    app:
      trust:
        namespace: ` + Namespace + `
`
}

// IssuerManifest renders the ClusterIssuer the document's mode implies.
//
// A ClusterIssuer rather than a namespaced one: applications live in
// namespaces this tool does not know about, and an Issuer they cannot reference
// is an Issuer nobody uses.
func IssuerManifest(spec v1alpha1.ClusterSpec) string {
	var b strings.Builder
	b.WriteString(managedFileHeader + "\n")
	b.WriteString("apiVersion: cert-manager.io/v1\nkind: ClusterIssuer\nmetadata:\n")
	b.WriteString("  name: " + yamlString(IssuerName) + "\nspec:\n")

	switch spec.PKI.Mode {
	case v1alpha1.PKIPrivateCA:
		b.WriteString("  ca:\n    secretName: " + yamlString(CASecretName) + "\n")

	case v1alpha1.PKIACMEDNS01, v1alpha1.PKIACMEHTTP01:
		acme := spec.PKI.ACME
		server := "https://acme-v02.api.letsencrypt.org/directory"
		email := ""
		if acme != nil {
			if acme.Server != "" {
				server = acme.Server
			}
			email = acme.Email
		}
		b.WriteString("  acme:\n")
		b.WriteString("    server: " + yamlString(server) + "\n")
		b.WriteString("    email: " + yamlString(email) + "\n")
		// The account key is generated on first use and kept in this secret.
		// Losing it means re-registering, which counts against the CA's rate
		// limits, so it is named rather than left to a default.
		b.WriteString("    privateKeySecretRef:\n      name: " +
			yamlString(IssuerName+"-acme-account") + "\n")
		b.WriteString("    solvers:\n")
		writeSolver(&b, spec)
	}
	return b.String()
}

// writeSolver renders the challenge the document asked for.
func writeSolver(b *strings.Builder, spec v1alpha1.ClusterSpec) {
	if spec.PKI.Mode == v1alpha1.PKIACMEHTTP01 {
		// The class rather than a gateway reference: cert-manager's HTTP-01
		// solver creates its own Ingress or Gateway, and ADR-006 keeps routes
		// out of this tool's hands either way.
		b.WriteString("      - http01:\n          gatewayHTTPRoute:\n            parentRefs:\n")
		for _, gw := range spec.Gateway.Gateways {
			ns := gw.Namespace
			if ns == "" {
				ns = "gateway-system"
			}
			b.WriteString("              - name: " + yamlString(gw.Name) + "\n")
			b.WriteString("                namespace: " + yamlString(ns) + "\n")
			b.WriteString("                kind: Gateway\n")
		}
		return
	}

	provider := ""
	if spec.PKI.ACME != nil {
		provider = strings.TrimSpace(spec.PKI.ACME.DNSProvider)
	}
	b.WriteString("      - dns01:\n")
	switch provider {
	case "cloudflare":
		b.WriteString("          cloudflare:\n            apiTokenSecretRef:\n")
		b.WriteString("              name: " + yamlString("malmok-acme-cloudflare") + "\n")
		b.WriteString("              key: api-token\n")
	case "route53":
		b.WriteString("          route53:\n            region: us-east-1\n")
	default:
		// An unknown provider is written as a webhook solver rather than
		// guessed at: cert-manager has a plugin for most of them, and inventing
		// a configuration for one this tool has never seen would produce an
		// issuer that is Ready and cannot solve.
		b.WriteString("          webhook:\n            groupName: " +
			yamlString("acme."+provider) + "\n")
		b.WriteString("            solverName: " + yamlString(provider) + "\n")
	}
}

// TrustBundle distributes the private CA to every namespace.
//
// docs/00-architecture.md TrustSpec: the three trust stores are independent and
// each fails differently. This is the in-cluster one -- without it, a client in
// a pod rejects the CA and the error names a certificate rather than a missing
// bundle.
func TrustBundle(spec v1alpha1.ClusterSpec) string {
	return managedFileHeader + `
apiVersion: trust.cert-manager.io/v1alpha1
kind: Bundle
metadata:
  name: ` + yamlString(TrustBundleName) + `
spec:
  sources:
    - secret:
        name: ` + yamlString(CASecretName) + `
        key: tls.crt
  target:
    configMap:
      key: ca.crt
    namespaceSelector:
      matchLabels: {}
`
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func yamlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// b64 encodes material for a Secret.
func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// fingerprint renders a certificate's SHA-256 the way openssl prints it, so the
// check can compare against what the cluster reports without parsing.
//
// The certificate is the observable and the key is not: a rotated CA is noticed
// without the private key ever being read back off the cluster, which keeps the
// step's evidence safe to put in an audit report.
func fingerprint(pemBytes []byte) string {
	blk, _ := pem.Decode(pemBytes)
	if blk == nil {
		return ""
	}
	sum := sha256.Sum256(blk.Bytes)

	var b strings.Builder
	b.WriteString("sha256Fingerprint=")
	for i, x := range sum {
		if i > 0 {
			b.WriteString(":")
		}
		fmt.Fprintf(&b, "%02X", x)
	}
	return b.String()
}
