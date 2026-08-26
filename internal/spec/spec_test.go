package spec

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ryxen/malmok/api/v1alpha1"
)

// minimal is a document that validates, so each test can break exactly one
// thing and see only that error.
const minimal = `
apiVersion: platform.ryxen.dev/v1alpha1
kind: ClusterSpec
metadata:
  name: test
  profile: onprem-dmz
network:
  mode: proxy
  proxy:
    http: http://proxy.acme.local:3128
topology:
  registrationAddress: k8s-api.acme.internal
  servers:
    - host: 10.10.0.11
kubernetes:
  version: v1.34.5+rke2r1
  dataplane:
    preset: cilium-gw
    loadBalancerPool: ["10.10.20.240/29"]
pki:
  mode: private-ca
  domain: acme.internal
  privateCA:
    rootCert: file://./pki/root.crt
    intermediateCert: file://./pki/inter.crt
    intermediateKey: env://INTER_KEY
registry:
  mode: external
  systemDefaultRegistry: harbor.acme.internal
storage:
  driver: local-path
gateway:
  domainSuffix: acme.internal
  gateways:
    - name: public
      address: 10.10.20.241
      listeners:
        - name: https
          protocol: HTTPS
          port: 443
          hostname: "*.acme.internal"
`

func parse(t *testing.T, yaml string) *Document {
	t.Helper()
	doc, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return doc
}

func TestMinimalDocumentValidates(t *testing.T) {
	doc := parse(t, minimal)
	if _, err := doc.ApplyProfile(); err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(false); err != nil {
		t.Fatalf("the baseline document should validate: %v", err)
	}
}

// A typo that silently becomes nothing is the failure this prevents:
// `registrationAdress` would otherwise leave the field empty and surface months
// later as an unexplained rejoin.
func TestUnknownKeysAreRejected(t *testing.T) {
	_, err := Parse([]byte(minimal + "\nunexpectedTopLevel: true\n"))
	if err == nil {
		t.Fatal("an unknown key was accepted")
	}
	if !strings.Contains(err.Error(), "unexpectedTopLevel") {
		t.Errorf("the error does not name the offending key: %v", err)
	}
}

func TestAPIVersionAndKindAreChecked(t *testing.T) {
	for _, tc := range []struct{ name, doc, want string }{
		{"wrong apiVersion", strings.Replace(minimal,
			"platform.ryxen.dev/v1alpha1", "platform.ryxen.dev/v1", 1), "apiVersion"},
		{"wrong kind", strings.Replace(minimal, "kind: ClusterSpec", "kind: Cluster", 1), "kind"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse([]byte(tc.doc)); err == nil {
				t.Fatal("expected an error")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error does not mention %s: %v", tc.want, err)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*v1alpha1.ClusterSpec)
		wantErr string
	}{
		{"name required", func(s *v1alpha1.ClusterSpec) { s.Metadata.Name = "" }, "metadata.name"},

		{"registrationAddress required",
			func(s *v1alpha1.ClusterSpec) { s.Topology.RegistrationAddress = "" },
			"registrationAddress is required"},
		{
			// ADR-008: this works until a second server is added, and then
			// every node has to re-join.
			name: "registrationAddress must not be a node's own address",
			mutate: func(s *v1alpha1.ClusterSpec) {
				s.Topology.RegistrationAddress = s.Topology.Servers[0].Host
			},
			wantErr: "own address",
		},
		{"at least one server",
			func(s *v1alpha1.ClusterSpec) { s.Topology.Servers = nil }, "at least one"},
		{
			name: "duplicate hosts",
			mutate: func(s *v1alpha1.ClusterSpec) {
				s.Topology.Agents = []v1alpha1.NodeSpec{{Host: "10.10.0.11"}}
			},
			wantErr: "appears twice",
		},
		{
			name: "duplicate hostnames",
			mutate: func(s *v1alpha1.ClusterSpec) {
				s.Topology.Servers[0].Hostname = "n1"
				s.Topology.Agents = []v1alpha1.NodeSpec{{Host: "10.10.0.12", Hostname: "n1"}}
			},
			wantErr: "PF-604",
		},
		{
			name: "kube-vip needs an address",
			mutate: func(s *v1alpha1.ClusterSpec) {
				s.Topology.VIP = &v1alpha1.VIPSpec{Provider: v1alpha1.VIPKubeVIP}
			},
			wantErr: "vip.address is required",
		},

		{"kubernetes version required",
			func(s *v1alpha1.ClusterSpec) { s.Kubernetes.Version = "" }, "kubernetes.version"},
		{
			// Without a pool the Gateway comes up with no external address and
			// looks healthy while being unreachable.
			name: "cilium needs a load balancer pool",
			mutate: func(s *v1alpha1.ClusterSpec) {
				s.Kubernetes.Dataplane.LoadBalancerPool = nil
			},
			wantErr: "loadBalancerPool is required",
		},
		{
			name: "fallback equal to preset has nowhere to go",
			mutate: func(s *v1alpha1.ClusterSpec) {
				s.Kubernetes.Dataplane.Fallback = s.Kubernetes.Dataplane.Preset
			},
			wantErr: "nowhere to go",
		},
		{
			name:    "bad CIDR",
			mutate:  func(s *v1alpha1.ClusterSpec) { s.Network.PodCIDR = "10.42.0.0" },
			wantErr: "not a CIDR",
		},
		{
			name:    "proxy mode needs a proxy",
			mutate:  func(s *v1alpha1.ClusterSpec) { s.Network.Proxy = nil },
			wantErr: "network.proxy is required",
		},

		{
			name: "private-ca needs its material",
			mutate: func(s *v1alpha1.ClusterSpec) {
				s.PKI.PrivateCA.IntermediateKey = ""
			},
			wantErr: "intermediateKey is required",
		},
		{
			name: "acme needs an email",
			mutate: func(s *v1alpha1.ClusterSpec) {
				s.PKI.Mode = v1alpha1.PKIACMEDNS01
				s.PKI.PrivateCA = nil
			},
			wantErr: "acme.email is required",
		},

		{
			// Air-gapped means nothing can be fetched. A file that does not say
			// where the images are fails on the first pull, hours in.
			name: "airgap needs somewhere to pull from",
			mutate: func(s *v1alpha1.ClusterSpec) {
				s.Network.Mode = v1alpha1.NetworkAirgap
				s.Registry.Mode = v1alpha1.RegistryInternal
				s.Registry.SystemDefaultRegistry = ""
				s.Registry.Bundle = ""
			},
			wantErr: "nowhere to pull images from",
		},

		{
			name:    "nfs needs a server and path",
			mutate:  func(s *v1alpha1.ClusterSpec) { s.Storage.Driver = v1alpha1.StorageNFS },
			wantErr: "storage.nfs.server",
		},

		{"domainSuffix required",
			func(s *v1alpha1.ClusterSpec) { s.Gateway.DomainSuffix = "" }, "domainSuffix"},
		{
			// Omitting the block is legal: an empty TLSSource inherits the
			// cluster's pki.mode, so a listener does not have to restate it.
			name: "source secret needs a secretRef",
			mutate: func(s *v1alpha1.ClusterSpec) {
				s.Gateway.Gateways[0].Listeners[0].TLS = &v1alpha1.ListenerTLS{
					Source: v1alpha1.TLSFromSecret,
				}
			},
			wantErr: "secretRef is required",
		},
		{
			// PF-903 checks the certificate SAN against this; with no hostname
			// there is nothing to check.
			name: "https listener needs a hostname",
			mutate: func(s *v1alpha1.ClusterSpec) {
				s.Gateway.Gateways[0].Listeners[0].Hostname = ""
			},
			wantErr: "PF-903",
		},
		{
			name: "duplicate listener ports",
			mutate: func(s *v1alpha1.ClusterSpec) {
				l := s.Gateway.Gateways[0].Listeners[0]
				l.Name = "https2"
				s.Gateway.Gateways[0].Listeners = append(s.Gateway.Gateways[0].Listeners, l)
			},
			wantErr: "used twice",
		},
		{
			name: "byo needs material",
			mutate: func(s *v1alpha1.ClusterSpec) {
				s.Gateway.Gateways[0].Listeners[0].TLS = &v1alpha1.ListenerTLS{
					Source: v1alpha1.TLSFromBYO, BYO: &v1alpha1.BYOMaterial{},
				}
			},
			wantErr: "needs either dir",
		},
		{
			// Reserved in the schema so adding it later is not a break;
			// accepting it now would produce a Secret nobody can explain.
			name: "pkcs12 is rejected rather than ignored",
			mutate: func(s *v1alpha1.ClusterSpec) {
				s.Gateway.Gateways[0].Listeners[0].TLS = &v1alpha1.ListenerTLS{
					Source: v1alpha1.TLSFromBYO,
					BYO:    &v1alpha1.BYOMaterial{Dir: "file://./certs", Format: "pkcs12"},
				}
			},
			wantErr: "only pem",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := parse(t, minimal)
			if _, err := doc.ApplyProfile(); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&doc.Spec)

			err := doc.Validate(false)
			if err == nil {
				t.Fatalf("expected an error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error does not contain %q:\n%v", tc.wantErr, err)
			}
		})
	}
}

// Every problem at once. An operator who has to run the tool six times to learn
// six things about their own file stops reading the messages.
func TestValidateReportsEveryProblem(t *testing.T) {
	doc := parse(t, minimal)
	doc.Spec.Metadata.Name = ""
	doc.Spec.Kubernetes.Version = ""
	doc.Spec.Gateway.DomainSuffix = ""

	err := doc.Validate(false)
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"metadata.name", "kubernetes.version", "domainSuffix"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("only some problems were reported; %q is missing:\n%v", want, err)
		}
	}
}

// cluster.yaml is an audit artifact handed to customers. A password in it
// outlives the engagement.
func TestPlaintextSecretsAreRejected(t *testing.T) {
	doc := parse(t, minimal)
	if _, err := doc.ApplyProfile(); err != nil {
		t.Fatal(err)
	}
	doc.Spec.Registry.Password = "literal://hunter2"

	err := doc.Validate(false)
	var plain *ErrSecretInPlaintext
	if !errors.As(err, &plain) {
		t.Fatalf("expected ErrSecretInPlaintext, got %v", err)
	}
	if plain.Field != "registry.password" {
		t.Errorf("field = %q, want registry.password", plain.Field)
	}
	if err := doc.Validate(true); err != nil {
		t.Errorf("--allow-literal-secrets should permit it: %v", err)
	}
}

// ---------------------------------------------------------------------------
// SourceRef
// ---------------------------------------------------------------------------

func TestResolve(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "value.txt"), []byte("from-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := parse(t, minimal)
	doc.Path = filepath.Join(dir, "cluster.yaml")

	t.Setenv("PLATFORMCTL_TEST_VALUE", "from-env")

	tests := []struct {
		name, ref, want, wantErr string
		secret                   bool
	}{
		{name: "file relative to the document", ref: "file://./value.txt", want: "from-file"},
		{name: "env", ref: "env://PLATFORMCTL_TEST_VALUE", want: "from-env"},
		{name: "literal", ref: "literal://plain", want: "plain"},
		{name: "empty is not an error", ref: "", want: ""},
		{name: "missing env var", ref: "env://PLATFORMCTL_ABSENT", wantErr: "is not set"},
		{name: "no scheme", ref: "/etc/passwd", wantErr: "no scheme"},
		{name: "unknown scheme", ref: "vault://secret", wantErr: "unknown scheme"},
		// Recognised and refused, rather than read as a filename.
		{name: "sops says so", ref: "sops://secrets.enc.yaml#a.b", wantErr: "not implemented"},
		{name: "literal secret refused", ref: "literal://hunter2", secret: true, wantErr: "literal://"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := doc.Resolve("field", v1alpha1.SourceRef(tc.ref), tc.secret, false)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error containing %q", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Profiles
// ---------------------------------------------------------------------------

func TestProfileFillsOnlyWhatWasLeftUnset(t *testing.T) {
	doc := parse(t, `
apiVersion: platform.ryxen.dev/v1alpha1
kind: ClusterSpec
metadata:
  name: t
  profile: airgap-rocky
topology:
  registrationAddress: k8s.internal
  servers: [{host: 10.0.0.1}]
kubernetes:
  version: v1.34.5+rke2r1
  dataplane:
    preset: canal-traefik
pki:
  domain: internal
gateway:
  domainSuffix: internal
`)
	applied, err := doc.ApplyProfile()
	if err != nil {
		t.Fatal(err)
	}

	// Explicitly chosen: untouched.
	if got := doc.Spec.Kubernetes.Dataplane.Preset; got != v1alpha1.DataplaneCanalTraefik {
		t.Errorf("the profile overwrote an explicit preset: %s", got)
	}
	// Left unset: supplied.
	if got := doc.Spec.OS.Family; got != v1alpha1.OSRocky {
		t.Errorf("os.family = %s, want rocky from the profile", got)
	}
	if got := doc.Spec.Network.Mode; got != v1alpha1.NetworkAirgap {
		t.Errorf("network.mode = %s, want airgap from the profile", got)
	}
	if got := doc.Spec.PKI.Mode; got != v1alpha1.PKIPrivateCA {
		t.Errorf("pki.mode = %s, want private-ca from the profile", got)
	}

	// Customer-facing profiles ask before downgrading: an unnoticed one
	// discovered months later is expensive to explain.
	if got := doc.Spec.Kubernetes.Dataplane.DowngradePolicy; got != v1alpha1.DowngradeConfirm {
		t.Errorf("downgradePolicy = %s, want confirm for an airgap profile", got)
	}

	if !containsStr(applied, "os.family") || !containsStr(applied, "pki.mode") {
		t.Errorf("applied list is incomplete: %v", applied)
	}
	if containsStr(applied, "kubernetes.dataplane.preset") {
		t.Errorf("applied list claims a field the operator set: %v", applied)
	}
}

// ADR-005: the engine disables it whatever the document says, and the audit
// report has to show that it did.
func TestIngressNginxIsAlwaysDisabled(t *testing.T) {
	doc := parse(t, minimal)
	applied, err := doc.ApplyProfile()
	if err != nil {
		t.Fatal(err)
	}
	if !containsStr(doc.Spec.Kubernetes.DisableBundled, "rke2-ingress-nginx") {
		t.Error("rke2-ingress-nginx was not disabled")
	}
	var recorded bool
	for _, a := range applied {
		if strings.Contains(a, "rke2-ingress-nginx") {
			recorded = true
		}
	}
	if !recorded {
		t.Errorf("disabling it was not recorded for the audit report: %v", applied)
	}
}

func TestCustomProfileSuppliesNothing(t *testing.T) {
	doc := parse(t, strings.Replace(minimal, "profile: onprem-dmz", "profile: custom", 1))
	applied, err := doc.ApplyProfile()
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 0 {
		t.Errorf("custom is Tier-3 and must supply nothing, got %v", applied)
	}
}

func TestUnknownProfileIsAnError(t *testing.T) {
	doc := parse(t, strings.Replace(minimal, "profile: onprem-dmz", "profile: nonesuch", 1))
	if _, err := doc.ApplyProfile(); err == nil {
		t.Error("expected an unknown profile to be rejected")
	}
}

// Every Tier-1 profile has to produce a document that validates on its own,
// or the baseline is not a baseline.
func TestEveryProfileBaselineIsCoherent(t *testing.T) {
	for _, name := range Profiles() {
		t.Run(string(name), func(t *testing.T) {
			b, ok := BaselineFor(name)
			if !ok {
				t.Fatal("no baseline")
			}
			if b.Dataplane == "" || b.PKIMode == "" || b.Storage == "" || b.RegistryMode == "" {
				t.Errorf("baseline leaves a required field empty: %+v", b)
			}
			if b.Fallback != "" && b.Fallback == b.Dataplane {
				t.Error("fallback is the same as the preset, so a downgrade has nowhere to go")
			}
			// The conservative profile is the safety net: it must not need eBPF.
			if name == v1alpha1.ProfileAirgapConservative && b.Dataplane != v1alpha1.DataplaneCanalTraefik {
				t.Errorf("the conservative profile uses %s; it exists so one kernel problem "+
					"cannot stop a delivery", b.Dataplane)
			}
		})
	}
}

func containsStr(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// A listener that says nothing about TLS inherits the cluster's pki.mode. That
// is the documented meaning of an empty TLSSource, and it is what keeps a
// twelve-listener gateway from restating the same line twelve times.
func TestHTTPSListenerMayInheritClusterPKI(t *testing.T) {
	doc := parse(t, minimal)
	if _, err := doc.ApplyProfile(); err != nil {
		t.Fatal(err)
	}
	doc.Spec.Gateway.Gateways[0].Listeners[0].TLS = nil

	if err := doc.Validate(false); err != nil {
		t.Errorf("omitting tls should inherit pki.mode, got: %v", err)
	}
}

// What the writer produces has to be what the reader accepts. Serialisation
// lives in this package so the two cannot drift, and this is the assertion
// that says so.
func TestRoundTrip(t *testing.T) {
	doc := parse(t, minimal)
	if _, err := doc.ApplyProfile(); err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(false); err != nil {
		t.Fatal(err)
	}

	body, err := Marshal(doc.Spec)
	if err != nil {
		t.Fatal(err)
	}

	// Strict parsing: an emitted key the loader does not know would be a silent
	// schema drift between the two halves.
	back, err := Parse(body)
	if err != nil {
		t.Fatalf("what Marshal wrote does not parse:\n%v\n%s", err, body)
	}
	if err := back.Validate(false); err != nil {
		t.Errorf("what Marshal wrote does not validate: %v", err)
	}

	for _, check := range []struct{ name, got, want string }{
		{"profile", string(back.Spec.Metadata.Profile), string(doc.Spec.Metadata.Profile)},
		{"registrationAddress", back.Spec.Topology.RegistrationAddress, doc.Spec.Topology.RegistrationAddress},
		{"version", back.Spec.Kubernetes.Version, doc.Spec.Kubernetes.Version},
		{"dataplane", string(back.Spec.Kubernetes.Dataplane.Preset), string(doc.Spec.Kubernetes.Dataplane.Preset)},
		{"pki.mode", string(back.Spec.PKI.Mode), string(doc.Spec.PKI.Mode)},
		{"registry.mode", string(back.Spec.Registry.Mode), string(doc.Spec.Registry.Mode)},
		{"domainSuffix", back.Spec.Gateway.DomainSuffix, doc.Spec.Gateway.DomainSuffix},
	} {
		if check.got != check.want {
			t.Errorf("%s survived as %q, want %q", check.name, check.got, check.want)
		}
	}
	if got, want := len(back.Spec.Topology.Servers), len(doc.Spec.Topology.Servers); got != want {
		t.Errorf("%d servers survived, want %d", got, want)
	}
}

// The identity is filled in rather than trusted: a document without it parses
// as the wrong kind, which is a confusing way to fail.
func TestMarshalStampsTheIdentity(t *testing.T) {
	body, err := Marshal(v1alpha1.ClusterSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), v1alpha1.APIVersion) ||
		!strings.Contains(string(body), v1alpha1.KindSpec) {
		t.Errorf("apiVersion or kind is missing:\n%s", body)
	}
	if !strings.HasPrefix(string(body), "# Generated by malmok.") {
		t.Error("the document does not say where it came from")
	}
}

// The run keeps its own copy, because the original may be edited before a
// resume and a run has to finish with the input it started from (§1.1).
func TestSnapshot(t *testing.T) {
	dir := t.TempDir()
	doc := parse(t, minimal)
	if _, err := doc.ApplyProfile(); err != nil {
		t.Fatal(err)
	}

	if err := Snapshot(dir, doc.Spec); err != nil {
		t.Fatal(err)
	}
	back, err := Load(SnapshotPath(dir))
	if err != nil {
		t.Fatalf("the snapshot does not load: %v", err)
	}
	if back.Spec.Metadata.Name != doc.Spec.Metadata.Name {
		t.Errorf("snapshot names the cluster %q", back.Spec.Metadata.Name)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != FileName {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// An operator's own file is preserved byte for byte: the digest the resume
// check compares is of those bytes, and a round trip through the encoder would
// change them even where nothing that matters changed.
func TestSnapshotRawPreservesTheBytes(t *testing.T) {
	dir := t.TempDir()
	raw := []byte(minimal)

	if err := SnapshotRaw(dir, raw); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(SnapshotPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Error("the snapshot differs from the file the operator supplied")
	}
}

// A registrationAddress on a node's own IP is ADR-008's trap, and it is also
// the only address some sites are allowed: an ARP VIP is a second IP answering
// on the segment, which IDC and air-gapped network policy frequently forbids,
// and a DNS name needs a zone somebody may write to. The trade is accepted by
// stating it, never by default.
func TestNodeAddressRegistrationIsAStatedTrade(t *testing.T) {
	base := func() *Document {
		return &Document{Spec: v1alpha1.ClusterSpec{
			APIVersion: v1alpha1.APIVersion, Kind: v1alpha1.KindSpec,
			Metadata: v1alpha1.Metadata{Name: "c", Profile: "custom"},
			Network:  v1alpha1.NetworkSpec{Mode: v1alpha1.NetworkOnline},
			Topology: v1alpha1.TopologySpec{
				RegistrationAddress: "10.0.0.11",
				Servers:             []v1alpha1.NodeSpec{{Host: "10.0.0.11"}},
			},
			Kubernetes: v1alpha1.KubernetesSpec{
				Version:   "v1.35.7+rke2r1",
				Dataplane: v1alpha1.DataplaneSpec{Preset: "canal-traefik"},
			},
			PKI:      v1alpha1.PKISpec{Mode: v1alpha1.PKINone},
			Storage:  v1alpha1.StorageSpec{Driver: "local-path"},
			Registry: v1alpha1.RegistrySpec{Mode: v1alpha1.RegistryEmbedded},
		}}
	}

	// Unstated, the trap stays closed.
	d := base()
	if err := d.Validate(false); err == nil || !strings.Contains(err.Error(), "acceptNodeRegistration") {
		t.Errorf("a node-address registration was not refused with the way out named: %v", err)
	}

	// Stated, it is the site's decision.
	d = base()
	d.Spec.Topology.AcceptNodeRegistration = true
	if err := d.Validate(false); err != nil {
		t.Errorf("the stated trade was still refused: %v", err)
	}

	// With a VIP there is no trade to make, and claiming both is a
	// contradiction worth stopping on.
	d = base()
	d.Spec.Topology.AcceptNodeRegistration = true
	d.Spec.Topology.VIP = &v1alpha1.VIPSpec{Provider: "kube-vip", Address: "10.0.0.200", Mode: "arp"}
	if err := d.Validate(false); err == nil || !strings.Contains(err.Error(), "VIP") {
		t.Errorf("registering on a node while a VIP exists was accepted: %v", err)
	}
}

// A first build frequently has no domain: applications are reached by address
// until DNS exists, and deferring certificates (pki.mode none) defers the
// naming question with them. A document with no domainSuffix and HTTP-only
// listeners has to validate, or the wizard's own "decide later" answer produces
// a file the validator refuses.
func TestAFirstBuildNeedsNoDomain(t *testing.T) {
	d := &Document{Spec: v1alpha1.ClusterSpec{
		APIVersion: v1alpha1.APIVersion, Kind: v1alpha1.KindSpec,
		Metadata: v1alpha1.Metadata{Name: "first", Profile: "custom"},
		Network:  v1alpha1.NetworkSpec{Mode: v1alpha1.NetworkOnline},
		Topology: v1alpha1.TopologySpec{
			RegistrationAddress: "k8s.acme.internal",
			Servers:             []v1alpha1.NodeSpec{{Host: "10.0.0.11"}},
		},
		Kubernetes: v1alpha1.KubernetesSpec{
			Version: "v1.36.3+rke2r1",
			Dataplane: v1alpha1.DataplaneSpec{
				Preset:           "cilium-gw",
				LoadBalancerPool: []string{"10.0.0.240/29"},
			},
		},
		PKI:      v1alpha1.PKISpec{Mode: v1alpha1.PKINone},
		Storage:  v1alpha1.StorageSpec{Driver: "local-path"},
		Registry: v1alpha1.RegistrySpec{Mode: v1alpha1.RegistryEmbedded},
		Gateway: v1alpha1.GatewaySpec{
			// No domainSuffix: the gateway is reached by address.
			Gateways: []v1alpha1.Gateway{{
				Name: "public",
				Listeners: []v1alpha1.ListenerSpec{{
					Name: "http", Protocol: v1alpha1.ListenerHTTP, Port: 80,
				}},
				RouteNamespaces: "all",
			}},
		},
	}}
	if err := d.Validate(false); err != nil {
		t.Errorf("an address-first build was refused: %v", err)
	}

	// The moment certificates are issued there are names, and the suffix is
	// what the contract hands to application charts.
	d.Spec.PKI.Mode = v1alpha1.PKIPrivateCA
	d.Spec.PKI.PrivateCA = &v1alpha1.PrivateCASpec{
		RootCert: "file://r.crt", IntermediateCert: "file://i.crt", IntermediateKey: "env://K",
	}
	d.Spec.PKI.Domain = "acme.internal"
	if err := d.Validate(false); err == nil || !strings.Contains(err.Error(), "domainSuffix") {
		t.Errorf("issuing certificates without a domain suffix was accepted: %v", err)
	}
}
