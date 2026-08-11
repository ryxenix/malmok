package pki

import (
	"context"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"platform.ryxen.dev/platformctl/api/v1alpha1"
	"platform.ryxen.dev/platformctl/internal/engine"
	"platform.ryxen.dev/platformctl/internal/exec"
)

func specWith(mode v1alpha1.PKIMode) v1alpha1.ClusterSpec {
	return v1alpha1.ClusterSpec{
		PKI: v1alpha1.PKISpec{Mode: mode, Domain: "acme.internal"},
		Gateway: v1alpha1.GatewaySpec{
			DomainSuffix: "acme.internal",
			Gateways: []v1alpha1.Gateway{{
				Name: "public", Address: "10.10.0.240",
				Listeners: []v1alpha1.ListenerSpec{
					{Name: "https", Protocol: v1alpha1.ListenerHTTPS, Port: 443},
				},
			}},
		},
	}
}

func material() Material {
	return Material{
		RootCert:         []byte("-----BEGIN CERTIFICATE-----\nroot\n-----END CERTIFICATE-----\n"),
		IntermediateCert: []byte("-----BEGIN CERTIFICATE-----\naW50ZXI=\n-----END CERTIFICATE-----\n"),
		IntermediateKey:  []byte("-----BEGIN PRIVATE KEY-----\nISSUINGCAPRIVATEKEY\n-----END PRIVATE KEY-----\n"),
	}
}

func docs(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	dec := yaml.NewDecoder(strings.NewReader(body))
	for {
		var d map[string]any
		if err := dec.Decode(&d); err != nil {
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

func names(steps []engine.Step) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		if st, ok := s.(*engine.ShellStep); ok {
			out = append(out, st.Name)
			continue
		}
		out = append(out, s.ID())
	}
	return out
}

// `pki.mode: none` is a real choice: at a first build the service domain is
// frequently not decided, and an issuer for a name nobody uses is a component
// somebody has to keep alive for no reason.
func TestNoPKIProducesNoSteps(t *testing.T) {
	for _, mode := range []v1alpha1.PKIMode{"", v1alpha1.PKINone} {
		if steps := Steps(&exec.Fake{}, specWith(mode), Material{}, Options{}); len(steps) != 0 {
			t.Errorf("mode %q produced %d steps", mode, len(steps))
		}
	}
}

// A ClusterIssuer rather than a namespaced one: applications live in namespaces
// this tool does not know about, and an Issuer they cannot reference is one
// nobody uses.
func TestPrivateCAIssuer(t *testing.T) {
	got := docs(t, IssuerManifest(specWith(v1alpha1.PKIPrivateCA)))[0]
	if got["kind"] != "ClusterIssuer" {
		t.Fatalf("kind is %v", got["kind"])
	}
	ca, _ := got["spec"].(map[string]any)["ca"].(map[string]any)
	if ca["secretName"] != CASecretName {
		t.Errorf("the issuer signs with %v", ca["secretName"])
	}
}

func TestACMEIssuer(t *testing.T) {
	t.Run("dns-01", func(t *testing.T) {
		spec := specWith(v1alpha1.PKIACMEDNS01)
		spec.PKI.ACME = &v1alpha1.ACMESpec{Email: "ops@acme.co.kr", DNSProvider: "cloudflare"}

		acme, _ := docs(t, IssuerManifest(spec))[0]["spec"].(map[string]any)["acme"].(map[string]any)
		if acme["email"] != "ops@acme.co.kr" {
			t.Errorf("email is %v", acme["email"])
		}
		// Losing the account key means re-registering, which counts against the
		// CA's rate limits, so where it lives is named rather than defaulted.
		ref, _ := acme["privateKeySecretRef"].(map[string]any)
		if ref["name"] == nil || ref["name"] == "" {
			t.Error("the ACME account key has no named secret")
		}

		solvers, _ := acme["solvers"].([]any)
		if len(solvers) != 1 {
			t.Fatalf("solvers is %v", solvers)
		}
		dns, _ := solvers[0].(map[string]any)["dns01"].(map[string]any)
		cf, _ := dns["cloudflare"].(map[string]any)
		if cf == nil {
			t.Fatalf("the cloudflare solver is missing: %v", dns)
		}
	})

	t.Run("http-01 points at the document's gateways", func(t *testing.T) {
		spec := specWith(v1alpha1.PKIACMEHTTP01)
		spec.PKI.ACME = &v1alpha1.ACMESpec{Email: "ops@acme.co.kr"}

		acme, _ := docs(t, IssuerManifest(spec))[0]["spec"].(map[string]any)["acme"].(map[string]any)
		solvers, _ := acme["solvers"].([]any)
		http01, _ := solvers[0].(map[string]any)["http01"].(map[string]any)
		route, _ := http01["gatewayHTTPRoute"].(map[string]any)
		parents, _ := route["parentRefs"].([]any)
		if len(parents) != 1 || parents[0].(map[string]any)["name"] != "public" {
			t.Errorf("parentRefs is %v", parents)
		}
	})

	// Inventing a configuration for a provider this tool has never seen would
	// produce an issuer that is Ready and cannot solve.
	t.Run("an unknown provider becomes a webhook solver", func(t *testing.T) {
		spec := specWith(v1alpha1.PKIACMEDNS01)
		spec.PKI.ACME = &v1alpha1.ACMESpec{Email: "ops@acme.co.kr", DNSProvider: "gabia"}

		body := IssuerManifest(spec)
		if !strings.Contains(body, "webhook") || !strings.Contains(body, "gabia") {
			t.Errorf("an unknown provider was guessed at:\n%s", body)
		}
	})
}

// The intermediate is what cert-manager signs with. A run that reached the
// issuer with no material would install one that cannot sign and report
// success.
func TestPrivateCAWithoutMaterialFails(t *testing.T) {
	steps := Steps(&exec.Fake{}, specWith(v1alpha1.PKIPrivateCA), Material{}, Options{})

	last, ok := steps[len(steps)-1].(engine.FailedStep)
	if !ok {
		t.Fatalf("the steps are %v", names(steps))
	}

	// Unsatisfiable by construction. A shell-based version of this passed
	// against a fake runner, which is exactly the kind of step whose contract
	// must not depend on anything that could answer differently.
	obs, err := last.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if obs.Satisfied {
		t.Error("a step with no material reported satisfied")
	}
	if last.Apply(context.Background()) == nil {
		t.Error("applying a step with no material succeeded")
	}
	if !strings.Contains(obs.Detail, "root key must stay offline") {
		t.Errorf("the failure does not say why the root is not wanted: %s", obs.Detail)
	}
}

// The issuing CA's private key must not sit on a node's disk for the life of
// the cluster. Applying it once puts it in etcd, where it was going anyway.
func TestIssuingKeyIsNeverWrittenToTheManifestDirectory(t *testing.T) {
	s := caSecretStep(material())

	if strings.Contains(s.Do, "/var/lib/rancher/rke2/server/manifests") {
		t.Error("the issuing key would be written into the manifest directory")
	}
	if !strings.Contains(s.Do, "umask 077") || !strings.Contains(s.Do, "trap") {
		t.Error("the temporary file holding the key is not protected or not removed")
	}
	// The check compares the certificate, never the key.
	if strings.Contains(s.Check, "tls\\.key") || strings.Contains(s.Check, "ISSUINGCAPRIVATEKEY") {
		t.Error("the check would read the private key back")
	}
	if !strings.Contains(s.Check, "fingerprint") {
		t.Error("the check does not compare a fingerprint, so a rotated CA is not noticed")
	}
}

// A key that reached the event stream reaches the audit report, and the report
// reaches the customer.
func TestKeyMaterialNeverReachesTheEventStream(t *testing.T) {
	s := caSecretStep(material())
	s.Runner = &exec.Fake{Default: exec.Result{
		ExitCode: 1, Stdout: "the issuing CA secret cert-manager/platformctl-ca does not exist\n",
	}}
	s.Host = "h"

	obs, err := s.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(obs.Detail+obs.Evidence, "ISSUINGCAPRIVATEKEY") {
		t.Errorf("key material reached the stream: %q %q", obs.Detail, obs.Evidence)
	}
}

// The secret comes before the issuer that references it: an Issuer pointing at
// a Secret that does not exist reports a reason about the reference rather than
// about the missing material.
func TestSecretComesBeforeTheIssuer(t *testing.T) {
	got := names(Steps(&exec.Fake{}, specWith(v1alpha1.PKIPrivateCA), material(), Options{}))

	secret, issuer := -1, -1
	for i, n := range got {
		switch n {
		case "ca-secret":
			secret = i
		case "issuer":
			issuer = i
		}
	}
	if secret < 0 || issuer < 0 || secret > issuer {
		t.Errorf("the steps run in this order: %v", got)
	}
}

// Ready replicas are not the target.
//
// cert-manager's CRDs are validated through a webhook whose serving certificate
// is issued after the pods start and whose CA bundle cainjector writes into the
// webhook configuration afterwards. Between those moments every pod reports
// ready and the API server cannot call the webhook -- which is what happened on
// a live cluster: the readiness step passed and the next step died on "x509:
// certificate signed by unknown authority".
func TestWaitsUntilCertManagerAcceptsAnObject(t *testing.T) {
	steps := Steps(&exec.Fake{}, specWith(v1alpha1.PKIPrivateCA), material(), Options{})
	var ready *engine.ShellStep
	for _, s := range steps {
		if st, ok := s.(*engine.ShellStep); ok && st.Name == "cert-manager-ready" {
			ready = st
		}
	}
	if ready == nil {
		t.Fatal("nothing waits for cert-manager")
	}

	// The observable is a dry run of the very object the next step creates: it
	// goes through the API server, the webhook and cert-manager's validation,
	// which is the whole path that has to work.
	if !strings.Contains(ready.Check, "--dry-run=server") {
		t.Error("the check does not exercise the admission path")
	}
	if !strings.Contains(ready.Check, "ClusterIssuer") {
		t.Error("the check dry-runs something other than what is about to be created")
	}
	// The two failures that look alike and are not: a webhook that cannot be
	// called is transient, a refusal is not.
	for _, want := range []string{"failed calling webhook", "unknown authority", "no matches for kind"} {
		if !strings.Contains(ready.Check, want) {
			t.Errorf("the check does not recognise %q", want)
		}
	}
}

// An issuer that exists is not one that can sign: Ready=False is where a CA
// secret with a mismatched key or an unregistered ACME account shows up.
func TestWaitsForTheIssuerToBeReady(t *testing.T) {
	steps := Steps(&exec.Fake{}, specWith(v1alpha1.PKIPrivateCA), material(), Options{})
	last := steps[len(steps)-1].(*engine.ShellStep)
	if last.Name != "issuer-ready" {
		t.Fatalf("the last step is %q", last.Name)
	}
	if !strings.Contains(last.Check, "Ready") {
		t.Error("the check does not read the Ready condition")
	}
	// The reason has to reach the operator, or a failure is just a boolean.
	if !strings.Contains(last.Do, "message") {
		t.Error("a failing issuer reports no reason")
	}
}

// Distributing a public CA's root to every namespace is noise: the three trust
// stores are independent, and this one exists for a CA nothing already trusts.
func TestTrustBundleOnlyForAPrivateCA(t *testing.T) {
	yes := true

	private := specWith(v1alpha1.PKIPrivateCA)
	private.PKI.Trust.ClusterBundle = &yes
	if !contains(names(Steps(&exec.Fake{}, private, material(), Options{})), "trust-bundle") {
		t.Error("a private CA with clusterBundle produced no bundle")
	}

	acme := specWith(v1alpha1.PKIACMEDNS01)
	acme.PKI.ACME = &v1alpha1.ACMESpec{Email: "ops@acme.co.kr", DNSProvider: "cloudflare"}
	acme.PKI.Trust.ClusterBundle = &yes
	if contains(names(Steps(&exec.Fake{}, acme, Material{}, Options{})), "trust-bundle") {
		t.Error("a publicly trusted CA was distributed to every namespace")
	}

	off := specWith(v1alpha1.PKIPrivateCA)
	if contains(names(Steps(&exec.Fake{}, off, material(), Options{})), "trust-bundle") {
		t.Error("a bundle was distributed without being asked for")
	}
}

// Pinned rather than tracked: an airgap bundle carries these exact images, and
// a floating version means the bundle and the manifest disagree.
func TestChartVersionsArePinned(t *testing.T) {
	chart := docs(t, CertManagerChart(specWith(v1alpha1.PKIPrivateCA), Options{}))[0]
	spec, _ := chart["spec"].(map[string]any)
	if spec["version"] != CertManagerVersion {
		t.Errorf("cert-manager version is %v", spec["version"])
	}
	if spec["chart"] != "cert-manager" || spec["repo"] == "" {
		t.Errorf("the chart reference is %v", spec)
	}
	// The CRDs come with the chart: installing them separately means two things
	// that can disagree about the same types.
	values, _ := spec["valuesContent"].(string)
	if !strings.Contains(values, "crds:") {
		t.Errorf("the chart does not install its CRDs:\n%s", values)
	}
}

// A private registry has to be told to every chart, or the pull fails with an
// opaque error naming an upstream host nobody configured.
func TestPrivateRegistryReachesTheChart(t *testing.T) {
	spec := specWith(v1alpha1.PKIPrivateCA)
	spec.Registry = v1alpha1.RegistrySpec{
		Mode: v1alpha1.RegistryExternal, SystemDefaultRegistry: "harbor.acme.internal",
	}
	body := CertManagerChart(spec, Options{})
	for _, want := range []string{"harbor.acme.internal/jetstack/cert-manager-controller",
		"harbor.acme.internal/jetstack/cert-manager-webhook"} {
		if !strings.Contains(body, want) {
			t.Errorf("the chart is missing %q:\n%s", want, body)
		}
	}

	// The embedded mirror serves what the nodes already have, so rewriting the
	// image host would point at a registry that is not there.
	spec.Registry.Mode = v1alpha1.RegistryEmbedded
	if strings.Contains(CertManagerChart(spec, Options{}), "harbor.acme.internal") {
		t.Error("the embedded mirror had image hosts rewritten for it")
	}
}

// Observe must not change anything.
func TestObserveChangesNothing(t *testing.T) {
	f := &exec.Fake{Default: exec.Result{ExitCode: 1}}
	spec := specWith(v1alpha1.PKIPrivateCA)
	yes := true
	spec.PKI.Trust.ClusterBundle = &yes

	for _, s := range Steps(f, spec, material(), Options{}) {
		if _, err := s.Observe(context.Background()); err != nil {
			t.Fatalf("%s: %v", s.ID(), err)
		}
	}
	for _, cmd := range f.Log {
		for _, bad := range []string{
			"kubectl apply", "kubectl create", "kubectl delete", "install -d", "mktemp", "> /var/lib",
		} {
			if !strings.Contains(cmd, bad) {
				continue
			}
			// A server-side dry run is the exception and the only one: the API
			// server runs admission and validation and discards the result,
			// which is exactly what makes it a measurement rather than a
			// change. cert-manager's own `cmctl check api` does the same thing.
			if bad == "kubectl apply" && strings.Contains(cmd, "--dry-run=server") {
				continue
			}
			t.Errorf("an Observe would have changed the cluster: it contains %q\n%s", bad, cmd)
		}
	}
}

func TestStepIDs(t *testing.T) {
	for _, s := range Steps(&exec.Fake{}, specWith(v1alpha1.PKIPrivateCA), material(), Options{}) {
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

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// The Helm release name is part of the cluster's identity, not a detail.
//
// Helm stamps it onto every object it owns and cert-manager's CRDs are
// cluster-scoped, so changing it leaves CRDs owned by a release that no longer
// exists and every later install fails until somebody deletes them by hand --
// which deleting the namespace does not do. Verified the hard way on a live
// cluster.
func TestReleaseNamesAreTheUpstreamOnes(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{CertManagerChart(specWith(v1alpha1.PKIPrivateCA), Options{}), "cert-manager"},
		{TrustManagerChart(Options{}), "trust-manager"},
	} {
		meta, _ := docs(t, tc.body)[0]["metadata"].(map[string]any)
		if meta["name"] != tc.want {
			t.Errorf("the release is named %v, want %q -- a prefix would give every object "+
				"a name that appears in no upstream runbook", meta["name"], tc.want)
		}
	}
}
