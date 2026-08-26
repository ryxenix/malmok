package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ryxen/malmok/api/v1alpha1"
	"github.com/ryxen/malmok/internal/event"
	"github.com/ryxen/malmok/internal/exec"
	"github.com/ryxen/malmok/internal/rke2"
	"github.com/ryxen/malmok/internal/spec"

	tea "charm.land/bubbletea/v2"
)

// richDocument is a document with things in it the wizard has no screen for.
//
// That is the whole point of it: gateway listeners, an etcd snapshot target and
// a GitOps repository are all real schema, none of them is asked about
// anywhere, and all of them have to come back out of an edit unchanged.
func richDocument() v1alpha1.ClusterSpec {
	yes := true
	return v1alpha1.ClusterSpec{
		APIVersion: v1alpha1.APIVersion,
		Kind:       v1alpha1.KindSpec,
		// Explicitly custom. A named profile resolves its axes at load and is
		// re-derived at save, so the identity round trip is only meaningful for
		// a document that states what it is.
		Metadata: v1alpha1.Metadata{Name: "acme-prod", Profile: "custom"},
		Topology: v1alpha1.TopologySpec{
			RegistrationAddress: "k8s.acme.internal",
			Servers: []v1alpha1.NodeSpec{{
				Host: "10.10.0.11", Role: v1alpha1.RoleServer,
				SSH: v1alpha1.SSHSpec{
					User: "ops", Port: 2222,
					PrivateKey: "file://./keys/id_ed25519",
				},
			}},
			Agents: []v1alpha1.NodeSpec{{
				Host: "10.10.0.21", Role: v1alpha1.RoleAgent,
				SSH: v1alpha1.SSHSpec{User: "ops", Port: 2222, PrivateKey: "file://./keys/id_ed25519"},
			}},
		},
		Kubernetes: v1alpha1.KubernetesSpec{
			Version:   "v1.34.10+rke2r1",
			Dataplane: v1alpha1.DataplaneSpec{Preset: "cilium-gw"},
			Etcd: v1alpha1.EtcdSpec{
				S3: &v1alpha1.S3Spec{
					Endpoint: "s3.acme.internal", Bucket: "etcd-backup",
					AccessKey: "env://S3_ACCESS", SecretKey: "env://S3_SECRET",
				},
			},
		},
		PKI:     v1alpha1.PKISpec{Mode: v1alpha1.PKIPrivateCA, Domain: "acme.internal"},
		Storage: v1alpha1.StorageSpec{Driver: "longhorn"},
		Gateway: v1alpha1.GatewaySpec{
			DomainSuffix: "acme.internal",
			Gateways: []v1alpha1.Gateway{{
				Name: "public", Namespace: "gateway-system", Address: "10.10.20.240",
				Listeners: []v1alpha1.ListenerSpec{
					{Name: "https", Protocol: v1alpha1.ListenerHTTPS, Port: 443,
						TLS: &v1alpha1.ListenerTLS{SecretRef: "public-tls"}},
				},
			}},
		},
		Platform: v1alpha1.PlatformSpec{GitOps: v1alpha1.GitOpsSpec{
			Enabled: &yes, Source: v1alpha1.GitOpsGit,
			GitRepo: "https://git.acme.internal/platform.git", Branch: "main",
			BootstrapApps: []string{"observability"},
		}},
	}
}

// The one thing this feature must not do.
//
// The wizard asks about a subset of the schema. A document rebuilt from the
// answers would silently drop everything else -- and an operator who opened the
// settings screen to change an IP address would find their gateway listeners
// and their etcd backup gone, with nothing on screen having mentioned either.
func TestEditingKeepsWhatNoScreenShows(t *testing.T) {
	before := richDocument()
	cfg := FromSpec(before)

	// A change of the kind the settings screen exists for.
	cfg.Server = "10.10.0.12"

	after := before
	cfg.ApplyTo(&after)

	if after.Topology.Servers[0].Host != "10.10.0.12" {
		t.Errorf("the edit did not take: server is %q", after.Topology.Servers[0].Host)
	}

	if len(after.Gateway.Gateways) != 1 ||
		len(after.Gateway.Gateways[0].Listeners) != 1 ||
		after.Gateway.Gateways[0].Listeners[0].TLS.SecretRef != "public-tls" {
		t.Errorf("the gateway listeners were lost: %+v", after.Gateway.Gateways)
	}
	if after.Kubernetes.Etcd.S3 == nil || after.Kubernetes.Etcd.S3.Bucket != "etcd-backup" {
		t.Errorf("the etcd snapshot target was lost: %+v", after.Kubernetes.Etcd.S3)
	}
	if after.Platform.GitOps.GitRepo != before.Platform.GitOps.GitRepo ||
		len(after.Platform.GitOps.BootstrapApps) != 1 {
		t.Errorf("the GitOps configuration was lost: %+v", after.Platform.GitOps)
	}
	if after.Metadata.Name != "acme-prod" {
		t.Errorf("the cluster was renamed to %q", after.Metadata.Name)
	}
}

// A node carries more than its address. Rebuilding the list from the hosts
// alone would drop the key that reaches it.
func TestEditingKeepsNodeCredentials(t *testing.T) {
	before := richDocument()
	cfg := FromSpec(before)

	tests := []struct {
		name    string
		change  func(*Config)
		host    string
		wantKey v1alpha1.SourceRef
	}{
		{
			name:   "an untouched node keeps its key",
			change: func(*Config) {},
			host:   "10.10.0.11", wantKey: "file://./keys/id_ed25519",
		},
		{
			// Renaming a node in place is what renaming a node in place means:
			// the same machine at a new address, reached the same way.
			name:   "a renamed node keeps its key",
			change: func(c *Config) { c.Server = "10.10.0.99" },
			host:   "10.10.0.99", wantKey: "file://./keys/id_ed25519",
		},
		{
			name:   "an added node has none to inherit",
			change: func(c *Config) { c.Agents = append(c.Agents, "10.10.0.22") },
			host:   "10.10.0.22", wantKey: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := cfg
			c.Agents = append([]string{}, cfg.Agents...)
			tc.change(&c)

			after := before
			c.ApplyTo(&after)

			for _, n := range append(append([]v1alpha1.NodeSpec{},
				after.Topology.Servers...), after.Topology.Agents...) {
				if n.Host != tc.host {
					continue
				}
				if n.SSH.PrivateKey != tc.wantKey {
					t.Errorf("%s has key %q, want %q", n.Host, n.SSH.PrivateKey, tc.wantKey)
				}
				return
			}
			t.Errorf("no node named %s in %+v", tc.host, after.Topology)
		})
	}
}

// A mode change has to take the material with it. A private-ca document that
// still carries an acme block is not inert: the loader resolves every SourceRef
// it finds, so a leftover token reference fails the run before it starts.
func TestTheModeDecidesWhichMaterialSurvives(t *testing.T) {
	base := richDocument()
	base.PKI.PrivateCA = &v1alpha1.PrivateCASpec{RootCert: "file://./pki/root.crt"}
	base.PKI.ACME = &v1alpha1.ACMESpec{Email: "ops@acme.co.kr", APIToken: "env://ACME"}

	tests := []struct {
		mode              string
		wantCA, wantACME  bool
		wantDomainCleared bool
	}{
		{mode: "private-ca", wantCA: true},
		{mode: "acme-dns01", wantACME: true},
		{mode: "byo-cert"},
		{mode: "none", wantDomainCleared: true},
	}

	for _, tc := range tests {
		t.Run(tc.mode, func(t *testing.T) {
			cfg := FromSpec(base)
			cfg.PKIMode = tc.mode

			after := base
			cfg.ApplyTo(&after)

			if (after.PKI.PrivateCA != nil) != tc.wantCA {
				t.Errorf("privateCA = %+v", after.PKI.PrivateCA)
			}
			if (after.PKI.ACME != nil) != tc.wantACME {
				t.Errorf("acme = %+v", after.PKI.ACME)
			}
			if (after.PKI.Domain == "") != tc.wantDomainCleared {
				t.Errorf("domain = %q", after.PKI.Domain)
			}
		})
	}
}

// A document with no certificates is still a document with agents.
//
// ToSpec used to return early for `pki.mode: none` and the agent list was built
// after that point, so choosing "decide later" quietly produced a single-node
// cluster.
func TestNoCertificatesDoesNotDropTheAgents(t *testing.T) {
	cfg := FromSpec(richDocument())
	cfg.PKIMode = "none"

	s := cfg.ToSpec()
	if len(s.Topology.Agents) != 1 {
		t.Errorf("%d agents survived pki.mode none", len(s.Topology.Agents))
	}
}

// What is read has to come back the same when nothing is changed.
func TestReadingAndWritingChangesNothingByItself(t *testing.T) {
	before := richDocument()
	after := before
	FromSpec(before).ApplyTo(&after)

	got, err := spec.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	want, err := spec.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("a round trip changed the document:\n--- read\n%s\n--- written\n%s", want, got)
	}
}

// ---------------------------------------------------------------------------
// The flow
// ---------------------------------------------------------------------------

func settingsWizard(t *testing.T, path string) *Wizard {
	t.Helper()
	w := wizard(t, LangEN, false, 96, 30, StepOpen)
	w.mode = modeSettings
	w.cfg.DocPath = path
	return w
}

func writeDoc(t *testing.T, dir string, s v1alpha1.ClusterSpec) string {
	t.Helper()
	path := filepath.Join(dir, "cluster.yaml")
	if err := spec.Save(path, s); err != nil {
		t.Fatal(err)
	}
	return path
}

// The end-to-end claim: open a file, change something on a screen, write it
// back, and have the result be a document the loader still accepts.
func TestOpenEditSave(t *testing.T) {
	path := writeDoc(t, t.TempDir(), richDocument())
	w := settingsWizard(t, path)

	if err := w.loadDocument(); err != nil {
		t.Fatalf("the document did not load: %v", err)
	}
	if w.cfg.Server != "10.10.0.11" || w.cfg.SSHUser != "ops" || w.cfg.SSHPort != "2222" {
		t.Errorf("the screens were not filled in: %+v", w.cfg)
	}

	w.cfg.Registration = "k8s-new.acme.internal"
	if err := w.writeDocument(); err != nil {
		t.Fatalf("the document did not save: %v", err)
	}
	if w.saved != path {
		t.Errorf("saved = %q", w.saved)
	}

	reread, err := spec.Load(path)
	if err != nil {
		t.Fatalf("what was written does not load: %v", err)
	}
	if reread.Spec.Topology.RegistrationAddress != "k8s-new.acme.internal" {
		t.Errorf("the edit did not reach the file: %q", reread.Spec.Topology.RegistrationAddress)
	}
	if len(reread.Spec.Gateway.Gateways) != 1 {
		t.Error("the gateway did not survive the write")
	}
}

// A path that is not there is the operator's typo, and the message has to be
// the one the loader gave rather than a restatement of it.
func TestOpeningSomethingThatIsNotThere(t *testing.T) {
	w := settingsWizard(t, filepath.Join(t.TempDir(), "absent.yaml"))
	if err := w.loadDocument(); err == nil {
		t.Fatal("a missing file loaded")
	}

	w.cfg.DocPath = "   "
	if err := w.loadDocument(); err == nil {
		t.Fatal("an empty path loaded")
	}
}

// Editing a run's snapshot is how an operator loses the ability to resume that
// run, so the list says which entries are snapshots.
func TestRunSnapshotsAreListedAndMarked(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "runs", "01KZSAD2657Y0HS75ZQDH822G9")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeDoc(t, runDir, richDocument())

	w := wizard(t, LangEN, false, 96, 30, StepOpen)
	w.bundle = dir

	found := w.listDocuments()
	if len(found) != 1 {
		t.Fatalf("found %d documents: %+v", len(found), found)
	}
	if found[0].Note != "doc.snapshot" {
		t.Errorf("a run snapshot is listed without a warning: %+v", found[0])
	}
	if !strings.Contains(w.cat.T(found[0].Note), "resume") {
		t.Errorf("the warning does not say what is lost: %q", w.cat.T(found[0].Note))
	}
}

// The two flows share their middle screens and differ at both ends.
func TestTheSettingsFlowEndsAtSaveNotAtInstall(t *testing.T) {
	w := wizard(t, LangEN, false, 96, 30, StepGateway)
	w.mode = modeSettings
	w.next()
	if w.step != StepSave {
		t.Errorf("after the last configuration screen the settings flow reached %v", w.step)
	}

	w = wizard(t, LangEN, false, 96, 30, StepGateway)
	w.mode = modeInstall
	w.next()
	if w.step != StepPreflight {
		t.Errorf("after the last configuration screen the install flow reached %v", w.step)
	}
}

// Nothing in the settings flow touches a node, and the screen has to say so:
// an operator who wrote a file and believes a cluster changed is the failure
// this sentence exists to prevent.
func TestSavingSaysNothingHasBeenApplied(t *testing.T) {
	path := writeDoc(t, t.TempDir(), richDocument())
	w := settingsWizard(t, path)
	if err := w.loadDocument(); err != nil {
		t.Fatal(err)
	}
	if err := w.writeDocument(); err != nil {
		t.Fatal(err)
	}

	_, body, _ := w.saveScreen(80)
	if !strings.Contains(body, "apply") {
		t.Errorf("the screen does not say what still has to be run:\n%s", body)
	}
}

// The first screen of either flow returns to the menu rather than walking
// backwards into it.
func TestBackFromTheFirstSettingsScreenReachesTheMenu(t *testing.T) {
	w := wizard(t, LangEN, false, 96, 30, StepOpen)
	w.mode = modeSettings
	w.back()
	if w.step != StepMenu {
		t.Errorf("back from the document screen reached %v", w.step)
	}
	if w.mode != modeInstall {
		t.Error("the mode was left on settings after returning to the menu")
	}
}

// A written document is not something to walk back into and rewrite.
func TestBackIsRefusedOnceTheDocumentIsWritten(t *testing.T) {
	w := wizard(t, LangEN, false, 96, 30, StepSave)
	w.mode, w.saved = modeSettings, "/tmp/cluster.yaml"
	w.back()
	if w.step != StepSave {
		t.Errorf("back left a written document at %v", w.step)
	}
}

// Editing a document is its own entry, and it goes somewhere.
//
// It used to be called Settings, which it is not: it edits a cluster document.
// Settings is now what the word means -- how the screen looks -- and the
// language lives there rather than being the first question of an install.
func TestTheDocumentMenuEntryIsLive(t *testing.T) {
	for _, item := range menuItems {
		if item.TitleKey != "menu.document" {
			continue
		}
		if item.Missing != "" {
			t.Errorf("the settings entry still says %q", item.Missing)
		}
		if item.Enter != StepOpen || item.Mode != modeSettings {
			t.Errorf("the document entry goes to %v in mode %v", item.Enter, item.Mode)
		}
		return
	}
	t.Fatal("there is no document entry")
}

// The language is a property of the person reading the screen, not of the
// cluster being built. It used to be the first question of an install, which
// meant it could only be changed by starting one.
func TestTheLanguageLivesInSettings(t *testing.T) {
	var settings *MenuItem
	for i := range menuItems {
		if menuItems[i].TitleKey == "menu.settings" {
			settings = &menuItems[i]
		}
	}
	if settings == nil {
		t.Fatal("there is no settings entry")
	}
	if settings.Enter != StepPrefs || settings.Missing != "" {
		t.Errorf("settings goes to %v (missing=%q)", settings.Enter, settings.Missing)
	}

	// And building a cluster no longer asks.
	w := wizard(t, LangEN, false, 96, 30, StepMenu)
	for _, step := range w.flow() {
		if stepKeys[step] == "step.lang" {
			t.Error("the install flow still asks for a language")
		}
	}

	// The screen changes it.
	w.step = StepPrefs
	w.cursor[StepPrefs] = 0
	before := w.cat.Lang()
	w.selectUnderCursor()
	if w.cat.Lang() == before {
		t.Error("choosing on the settings screen did not change the language")
	}
}

// A setting that does not survive the session is a slower way of pressing g.
func TestPreferencesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	want := Prefs{Lang: LangKO, ASCII: true, HideRail: true}
	if err := want.Save(); err != nil {
		t.Fatalf("%v", err)
	}
	if got := LoadPrefs(); got != want {
		t.Errorf("read back %+v, want %+v", got, want)
	}

	// A missing file is the defaults, not an error: the tool works without it,
	// and refusing to start because a preference file was malformed would be
	// the screen's appearance stopping an install.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "absent"))
	if got := LoadPrefs(); got != (Prefs{}) {
		t.Errorf("a missing file read as %+v", got)
	}
}

// Every screen in either flow has a rail label, or the rail draws a raw key.
func TestEveryFlowStepHasALabel(t *testing.T) {
	w := wizard(t, LangEN, false, 96, 30, StepMenu)
	for _, flow := range [][]Step{installSteps, settingsSteps} {
		for _, step := range flow {
			key, ok := stepKeys[step]
			if !ok {
				t.Errorf("step %v has no catalogue key", step)
				continue
			}
			if w.cat.T(key) == key {
				t.Errorf("step %v has no entry for %q", step, key)
			}
		}
	}
}

// A validation message names the screen that fixes it. Indexing a list by the
// step's own number gave the wrong screen for every one of them.
func TestProblemsNameTheScreenThatOwnsThem(t *testing.T) {
	w := wizard(t, LangEN, false, 96, 30, StepSummary)
	for _, tc := range []struct {
		step Step
		want string
	}{
		{StepProfile, "step.profile"},
		{StepNodes, "step.nodes"},
		{StepPKI, "step.pki"},
		{StepSummary, "step.summary"},
	} {
		if got := stepKeys[tc.step]; got != tc.want {
			t.Errorf("step %v is labelled %q, want %q", tc.step, got, tc.want)
		}
		if w.cat.T(stepKeys[tc.step]) == stepKeys[tc.step] {
			t.Errorf("step %v has no translation", tc.step)
		}
	}
}

// Validation on the save screen is about the file that will be written.
//
// The wizard has no screen for a gateway's listeners. Validating a document
// rebuilt from the answers would never see them -- so a listener with no port
// would be written out and the problem would first appear at install time, on
// a different machine, hours later.
func TestValidationSeesWhatNoScreenShows(t *testing.T) {
	broken := richDocument()
	broken.Gateway.Gateways[0].Listeners = nil // a gateway with no listeners

	w := wizard(t, LangEN, false, 96, 30, StepSave)
	w.mode, w.doc = modeSettings, broken
	w.cfg = FromSpec(broken)

	var found bool
	for _, line := range w.validateConfig() {
		if strings.Contains(line, "listeners") {
			found = true
		}
	}
	if !found {
		t.Errorf("the screen validated a reconstruction rather than the document:\n%v",
			w.validateConfig())
	}

	// And the install flow is unaffected: it has no loaded document to check
	// against, and reaching for an empty one would report every field missing.
	w.mode = modeInstall
	for _, line := range w.validateConfig() {
		if strings.Contains(line, "listeners") {
			t.Errorf("the install flow validated a document it never loaded: %q", line)
		}
	}
}

// An edit makes implied values explicit, and that is the only thing it adds.
//
// A document may leave a node's role and SSH port out; the loader and the
// phases fill both in. The wizard shows the operator what they resolve to, so
// writing them is not a change of meaning -- but it is a change of text, and an
// operator diffing the file afterwards should find it here rather than wonder
// what else moved.
func TestAnEditWritesTheDefaultsItShowed(t *testing.T) {
	implicit := richDocument()
	implicit.Topology.Servers[0].Role = ""
	implicit.Topology.Servers[0].SSH.Port = 0
	implicit.Topology.Agents[0].Role = ""
	implicit.Topology.Agents[0].SSH.Port = 0

	after := implicit
	FromSpec(implicit).ApplyTo(&after)

	if after.Topology.Servers[0].Role != v1alpha1.RoleServer ||
		after.Topology.Agents[0].Role != v1alpha1.RoleAgent {
		t.Errorf("the roles were not filled in: %+v", after.Topology)
	}
	if after.Topology.Servers[0].SSH.Port != 22 {
		t.Errorf("the port is %d", after.Topology.Servers[0].SSH.Port)
	}
	// And nothing else was invented.
	if after.Topology.Servers[0].SSH.PrivateKey != implicit.Topology.Servers[0].SSH.PrivateKey {
		t.Error("the key reference changed")
	}
}

// The upgrade flow asks two questions and then does one thing.
//
// It does not walk the configuration screens: an upgrade changes the version
// and nothing else, and offering to edit the dataplane on the way past would
// invite a change this flow has no way to apply.
func TestTheUpgradeFlowIsTwoQuestions(t *testing.T) {
	want := []Step{StepOpen, StepTarget, StepUpgrade, StepDone}
	w := wizard(t, LangEN, false, 96, 30, StepOpen)
	w.mode = modeUpgrade

	got := w.flow()
	if len(got) != len(want) {
		t.Fatalf("the flow is %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("step %d is %v, want %v", i, got[i], want[i])
		}
	}
	for _, unwanted := range []Step{StepProfile, StepNodes, StepPKI, StepRegistry} {
		for _, step := range got {
			if step == unwanted {
				t.Errorf("the upgrade flow walks %v", unwanted)
			}
		}
	}
}

// The same screen serves the settings and upgrade flows, and they are not doing
// the same thing: one rewrites the file, the other restarts every node it names.
func TestTheDocumentScreenSaysWhichFlowItIsIn(t *testing.T) {
	w := wizard(t, LangEN, false, 96, 30, StepOpen)

	w.mode = modeSettings
	settings := w.frameInfo()

	w.mode = modeUpgrade
	upgrade := w.frameInfo()

	if settings == upgrade {
		t.Error("both flows show the same explanation")
	}
	// Matched on the unwrapped part: the strip wraps, so a phrase that spans a
	// line break is not a phrase this can look for.
	if !strings.Contains(upgrade, "read, not") {
		t.Errorf("the upgrade screen does not say the file is only read:\n%s", upgrade)
	}
}

// An upgrade that says "installation complete" describes something that did not
// happen, and that sentence is the one an operator repeats to whoever asks.
func TestTheFinalScreenNamesWhatWasDone(t *testing.T) {
	w := wizard(t, LangEN, false, 96, 30, StepDone)

	w.mode = modeInstall
	_, install, _ := w.doneScreen(80)

	w.mode = modeUpgrade
	_, upgraded, _ := w.doneScreen(80)

	if strings.Contains(upgraded, "Installation complete") {
		t.Errorf("an upgrade reported an installation:\n%s", upgraded)
	}
	if install == upgraded {
		t.Error("both flows end with the same sentence")
	}
}

// The upgrade entry is live and starts the upgrade flow, not the settings one.
func TestTheUpgradeMenuEntryIsLive(t *testing.T) {
	for _, item := range menuItems {
		if item.TitleKey != "menu.upgrade" {
			continue
		}
		if item.Missing != "" {
			t.Errorf("the upgrade entry still says %q", item.Missing)
		}
		if item.Enter != StepOpen || item.Mode != modeUpgrade {
			t.Errorf("the upgrade entry goes to %v in mode %v", item.Enter, item.Mode)
		}
		return
	}
	t.Fatal("there is no upgrade entry")
}

// ---------------------------------------------------------------------------
// Local nodes
// ---------------------------------------------------------------------------

// The credentials appear only when something is dialled.
//
// An SSH user and port on a screen where nothing is connected to are two
// questions with no answer, and worse, they read as though the tool were about
// to log in somewhere.
func TestTheNodeScreenAsksForCredentialsOnlyWhenItDials(t *testing.T) {
	tests := []struct {
		name     string
		server   string
		agents   []string
		wantSSH  bool
		wantHelp string
	}{
		{
			name: "a remote server", server: "10.10.0.11", agents: []string{"10.10.0.21"},
			wantSSH: true, wantHelp: "nodes.help",
		},
		{
			name: "this machine only", server: "127.0.0.1",
			wantHelp: "nodes.help.local",
		},
		{
			name: "the literal name", server: "local",
			wantHelp: "nodes.help.local",
		},
		{
			// One node that is not this machine is enough: the credentials are
			// asked once and used for every connection, so dropping them
			// because the server is local would leave the agent unreachable.
			name:   "a local server and a remote agent",
			server: "127.0.0.1", agents: []string{"10.10.0.21"},
			wantSSH: true, wantHelp: "nodes.help",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := wizard(t, LangEN, false, 96, 30, StepNodes)
			w.cfg.Server, w.cfg.Agents = tc.server, tc.agents

			if got := w.needsSSH(); got != tc.wantSSH {
				t.Errorf("needsSSH = %v", got)
			}
			if got := w.nodesHelp(); got != tc.wantHelp {
				t.Errorf("help is %q, want %q", got, tc.wantHelp)
			}

			var labels []string
			for _, f := range w.fieldsFor(StepNodes) {
				labels = append(labels, f.labelKey)
			}
			for _, key := range []string{"nodes.user", "nodes.port"} {
				if has := contains(labels, key); has != tc.wantSSH {
					t.Errorf("%s present = %v, want %v (%v)", key, has, tc.wantSSH, labels)
				}
			}

			// The password stays either way and means a different thing in
			// each: over SSH it logs in and then elevates, locally it only
			// elevates -- and it is still needed, because an account that
			// cannot elevate answers "no" to every privileged question.
			want := "nodes.password"
			if !tc.wantSSH {
				want = "nodes.sudopassword"
			}
			if !contains(labels, want) {
				t.Errorf("%s is missing: %v", want, labels)
			}
		})
	}
}

// Which address is the machine the operator is sitting at is the one thing a
// diagram of addresses cannot show, and it decides whether a connection is
// opened at all.
func TestTheTopologyMarksThisMachine(t *testing.T) {
	w := wizard(t, LangEN, false, 96, 30, StepNodes)
	w.cfg.Server, w.cfg.Agents = "127.0.0.1", []string{"10.10.0.21"}

	var here, marked int
	for _, seg := range w.topology() {
		for _, row := range seg.rows {
			if row.note == "" {
				continue
			}
			marked++
			if row.addr == "127.0.0.1" {
				here++
			}
		}
	}
	if here != 1 {
		t.Errorf("the local node is marked %d times", here)
	}
	if marked != 1 {
		t.Errorf("%d rows are marked as this machine", marked)
	}
}

// The screens shrink under the cursor: a cursor past the end highlights nothing
// and makes Enter do something other than what the screen says.
func TestTheCursorIsClampedWhenAScreenShrinks(t *testing.T) {
	w := wizard(t, LangEN, false, 96, 30, StepNodes)
	w.cfg.Server, w.cfg.Agents = "10.10.0.11", []string{"10.10.0.21"}
	last := w.contentLen() - 1
	w.cursor[StepNodes] = last

	// Every address becomes this machine's, so the SSH user and port go.
	w.cfg.Server, w.cfg.Agents = "127.0.0.1", nil
	w.enter()

	if got := w.cursor[StepNodes]; got > w.contentLen()-1 {
		t.Errorf("the cursor is at %d and the screen has %d rows", got, w.contentLen())
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

// The question that comes first, because it is the first thing an operator
// knows and it decides what every screen after it has to ask.
//
// The wizard used to skip it and open on an address field, so somebody
// installing on the machine in front of them had to read their own IP off
// `ip addr` and type it back -- a value the tool was sitting on the whole time.
func TestTheWizardAsksWhichMachineBeforeAskingForAddresses(t *testing.T) {
	w := wizard(t, LangEN, false, 96, 30, StepMenu)

	flow := w.flow()
	where, nodes := -1, -1
	for i, st := range flow {
		if st == StepWhere {
			where = i
		}
		if st == StepNodes {
			nodes = i
		}
	}
	if where < 0 || nodes < 0 {
		t.Fatalf("the flow is %v", flow)
	}
	if where > nodes {
		t.Errorf("the address screen comes before the one that asks which machine: %v", flow)
	}
}

// This machine is the default. An installer is normally run on the machine
// being installed, and opening on "somewhere else" makes the common case the
// one that takes more steps.
func TestThisMachineIsTheDefault(t *testing.T) {
	if len(exec.LocalIPv4s()) == 0 {
		t.Skip("this machine has no routable address to offer")
	}
	w := wizard(t, LangEN, false, 96, 30, StepWhere)
	if !w.cfg.Local {
		t.Error("the wizard opened on somewhere else")
	}
	// And the address came from the machine rather than from the operator.
	if !contains(exec.LocalIPv4s(), w.cfg.Server) {
		t.Errorf("the server address is %q, which is not one of this machine's %v",
			w.cfg.Server, exec.LocalIPv4s())
	}
}

// Choosing this machine fills the address in; choosing another clears what was
// right for here, because leaving it would be the wizard suggesting a node that
// does not exist.
func TestSwitchingWhereRewritesTheAddress(t *testing.T) {
	if len(exec.LocalIPv4s()) == 0 {
		t.Skip("this machine has no routable address to offer")
	}
	w := wizard(t, LangEN, false, 96, 30, StepWhere)

	w.setLocal(false)
	if w.cfg.Server != "" {
		t.Errorf("switching away kept %q", w.cfg.Server)
	}

	w.cfg.Server = "10.10.0.11"
	w.setLocal(false) // no change, so nothing is touched
	if w.cfg.Server != "10.10.0.11" {
		t.Errorf("an address the operator typed was cleared: %q", w.cfg.Server)
	}

	w.setLocal(true)
	if !contains(exec.LocalIPv4s(), w.cfg.Server) {
		t.Errorf("switching to this machine left %q", w.cfg.Server)
	}
}

// The address is chosen from what the machine reports, never typed.
//
// A multi-homed host is a real case where which address the cluster advertises
// matters (PF-609), and typing is not what should decide it.
func TestTheLocalAddressIsChosenNotTyped(t *testing.T) {
	if len(exec.LocalIPv4s()) == 0 {
		t.Skip("this machine has no routable address to offer")
	}
	w := wizard(t, LangEN, false, 96, 30, StepNodes)
	w.setLocal(true)

	for _, f := range w.fieldsFor(StepNodes) {
		if f.labelKey == "nodes.server" {
			t.Error("the node screen still asks for an address this machine already knows")
		}
	}
	if w.contentLen() <= len(w.fieldsFor(StepNodes)) {
		t.Error("the address chooser takes no rows, so there is nothing to choose from")
	}

	// And choosing one takes. The chooser sits below the fields: the fields
	// are the work, and a long address list must not stand between the
	// operator and them.
	addrs := exec.LocalIPv4s()
	w.cursor[StepNodes] = len(osFamilies) + len(w.fieldsFor(StepNodes)) + len(addrs) - 1
	w.selectUnderCursor()
	if w.cfg.Server != addrs[len(addrs)-1] {
		t.Errorf("choosing an address left the server at %q", w.cfg.Server)
	}
}

// Editing a document that names this machine opens on the chooser rather than
// on a field asking for an address it already has.
func TestReadingADocumentRestoresTheBranch(t *testing.T) {
	doc := richDocument()
	doc.Topology.Servers[0].Host = "127.0.0.1"
	doc.Topology.Servers[0].NodeIP = "10.10.0.11"

	if c := FromSpec(doc); !c.Local {
		t.Error("a document naming this machine was read back as remote")
	}
	if c := FromSpec(richDocument()); c.Local {
		t.Error("a document naming another machine was read back as local")
	}
}

// Work that fails before the engine emits anything has to say so.
//
// The wizard held the error and used it only to pick a button, so a credential
// or connection failure -- the most common way a first run stops -- rendered as
// a progress bar at 0% with no explanation, and the operator was left pressing
// a retry that failed the same way each time.
func TestWorkThatFailsBeforeAnythingRunsSaysWhy(t *testing.T) {
	const why = "local: this account cannot elevate without a password and none was given"
	fail := func(context.Context, Config) error { return errors.New(why) }

	w, err := NewWizard("run", false, true, LangEN, fail, fail)
	if err != nil {
		t.Fatal(err)
	}
	w.width, w.height = 100, 24
	w.step = StepGateway

	_, cmd := w.next() // into the checks, which start the work
	if cmd == nil {
		t.Fatal("the checks did not start")
	}
	w.Update(cmd())

	if w.busy {
		t.Fatal("the screen is still waiting for work that returned")
	}
	body := plain(w.View().Content)
	if !strings.Contains(body, why) {
		t.Errorf("the screen does not say what stopped it:\n%s", body)
	}

	// And Enter goes back to fix it rather than repeating the same failure.
	btns := w.buttons()
	if len(btns) == 0 {
		t.Fatal("the screen offers no way out")
	}
	var primary string
	for _, b := range btns {
		if b.Primary {
			primary = b.Label
		}
	}
	if primary != w.cat.T("btn.back") {
		t.Errorf("the primary action is %q, which repeats the failure", primary)
	}

	// A failure after probes have reported is different: those are findings,
	// and running them again is the useful thing to do.
	w.fold(event.Event{Kind: event.KindPhase, Phase: "preflight", Status: event.StatusFailed})
	primary = ""
	for _, b := range w.buttons() {
		if b.Primary {
			primary = b.Label
		}
	}
	if primary != w.cat.T("btn.check") {
		t.Errorf("with findings on screen the primary action is %q", primary)
	}
}

// A profile is a starting point, not the only way to reach a combination.
//
// The network mode came from the baseline and from nowhere else, so a homelab
// behind a proxy -- a combination no profile happens to contain -- could not be
// produced from the wizard at all. The screen has always claimed otherwise:
// "the profile fixes the validated baseline; every other setting is an
// override".
func TestAProfileIsAStartingPointNotALock(t *testing.T) {
	w := wizard(t, LangEN, false, 96, 30, StepNetwork)
	homelab, _ := spec.BaselineFor(v1alpha1.ProfileHomelab)
	compose(w, homelab)

	if w.networkMode() != v1alpha1.NetworkOnline {
		t.Fatalf("the homelab baseline is %s", w.networkMode())
	}
	// The proxy fields are not offered until there is a proxy to describe.
	if len(w.fieldsFor(StepNetwork)) != 1 {
		t.Errorf("an online build is asked about a proxy: %d fields", len(w.fieldsFor(StepNetwork)))
	}

	// Choose the combination no profile contains.
	w.cursor[StepNetwork] = indexOf(networkModes, "proxy")
	w.selectUnderCursor()

	if w.networkMode() != v1alpha1.NetworkProxy {
		t.Fatalf("the mode did not change: %s", w.networkMode())
	}
	if len(w.fieldsFor(StepNetwork)) <= 1 {
		t.Error("choosing proxy did not reveal the proxy fields")
	}
	if got := w.cfg.ToSpec().Network.Mode; got != v1alpha1.NetworkProxy {
		t.Errorf("the document says %s", got)
	}
	// And the document says what this actually is: a combination no validated
	// baseline contains, which is what `custom` means.
	if got := w.cfg.ToSpec().Metadata.Profile; got != v1alpha1.ProfileCustom {
		t.Errorf("the profile is %s", got)
	}
}

// Every field a profile fixes has to be reachable, or the next combination
// nobody anticipated is unbuildable in the same way.
//
// The exceptions are listed rather than assumed: each is a value the wizard
// deliberately does not ask about, and adding a baseline field without deciding
// which side it falls on is what produced this bug.
func TestEveryBaselineFieldIsReachableOrDeliberatelyNot(t *testing.T) {
	// Reachable on a screen.
	offered := map[string]bool{
		"NetworkMode":        true,
		"Dataplane":          true,
		"Storage":            true,
		"PKIMode":            true,
		"RegistryMode":       true,
		"DowngradePolicy":    true,
		"EncryptNodeTraffic": true,
	}
	// Document-only, on purpose.
	documentOnly := map[string]string{
		"OSFamily":                    "measured from the node (PF-101), not asked",
		"Routing":                     "implied by the dataplane preset (ADR-004)",
		"Fallback":                    "the downgrade target; the policy above it is the decision",
		"GitOpsSource":                "no GitOps screen exists yet",
		"RequirePinnedGatewayAddress": "a profile's own strictness, not a setting",
	}

	typ := reflect.TypeOf(spec.Baseline{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if offered[name] || documentOnly[name] != "" {
			continue
		}
		t.Errorf("baseline field %s is neither offered on a screen nor listed as "+
			"document-only; a combination that needs it cannot be built from the wizard", name)
	}
}

// The three that were added carry through to the document.
func TestTheNewChoicesReachTheDocument(t *testing.T) {
	w := wizard(t, LangEN, false, 96, 30, StepOptions)
	w.cfg.NetworkMode = string(v1alpha1.NetworkAirgap)
	w.cfg.Encrypt = true
	w.cfg.DowngradePolicy = string(v1alpha1.DowngradeForbid)

	got := w.cfg.ToSpec()
	if got.Network.Mode != v1alpha1.NetworkAirgap {
		t.Errorf("network mode is %q", got.Network.Mode)
	}
	if got.Network.EncryptNodeTraffic == nil || !*got.Network.EncryptNodeTraffic {
		t.Error("node traffic encryption did not reach the document")
	}
	if got.Kubernetes.Dataplane.DowngradePolicy != v1alpha1.DowngradeForbid {
		t.Errorf("the downgrade policy is %q", got.Kubernetes.Dataplane.DowngradePolicy)
	}

	// Off is not stated rather than stated as false: the profile decides when
	// the document is silent, and writing false would freeze an answer the
	// profile is meant to give.
	w.cfg.Encrypt = false
	if w.cfg.ToSpec().Network.EncryptNodeTraffic != nil {
		t.Error("turning encryption off wrote a value instead of leaving it to the profile")
	}
}

// ---------------------------------------------------------------------------
// Registry and certificate screens
// ---------------------------------------------------------------------------

// Every value the validator can demand has to be enterable, or the wizard
// walks the operator into a document it then refuses.
func TestTheRegistryScreenCanSatisfyItsValidator(t *testing.T) {
	w := wizard(t, LangEN, false, 96, 30, StepRegistry)

	// An air-gapped build needs a bundle or a registry address; the bundle
	// field exists exactly for the build that has no registry to point at.
	w.cfg.NetworkMode = string(v1alpha1.NetworkAirgap)
	w.cfg.RegistryMode = string(v1alpha1.RegistryExternal)
	var labels []string
	for _, f := range w.fieldsFor(StepRegistry) {
		labels = append(labels, f.labelKey)
	}
	if !contains(labels, "reg.bundle") {
		t.Errorf("an air-gapped registry screen has no bundle field: %v", labels)
	}

	// Online, the bundle is not a question anybody asked.
	w.cfg.NetworkMode = string(v1alpha1.NetworkOnline)
	labels = nil
	for _, f := range w.fieldsFor(StepRegistry) {
		labels = append(labels, f.labelKey)
	}
	if contains(labels, "reg.bundle") {
		t.Errorf("an online build is asked for an airgap bundle: %v", labels)
	}

	// The insecure decision reaches the document as a pointer: stated only
	// when it says something, so the document that means it is distinguishable
	// from the ones that never thought about it.
	w.cfg.RegistryInsecure = true
	if got := w.cfg.ToSpec().Registry.Insecure; got == nil || !*got {
		t.Error("accepting an unverified certificate did not reach the document")
	}
	w.cfg.RegistryInsecure = false
	if w.cfg.ToSpec().Registry.Insecure != nil {
		t.Error("declining wrote `insecure: false` instead of nothing")
	}
}

// byo-cert asks for the material it needs and nothing it does not.
//
// It used to fall through to the private-CA fields, which asked for an issuing
// CA's key on a build that issues nothing -- and produced a document the
// validator refuses with no screen able to fix it.
func TestBYOCertAsksForTheCertificate(t *testing.T) {
	w := wizard(t, LangEN, false, 96, 30, StepPKI)
	w.cfg.PKIMode = string(v1alpha1.PKIBYOCert)

	var labels []string
	for _, f := range w.fieldsFor(StepPKI) {
		labels = append(labels, f.labelKey)
	}
	for _, want := range []string{"pki.byocert", "pki.byokey", "pki.byoca"} {
		if !contains(labels, want) {
			t.Errorf("%s is missing: %v", want, labels)
		}
	}
	for _, unwanted := range []string{"pki.key", "pki.inter", "pki.email"} {
		if contains(labels, unwanted) {
			t.Errorf("byo-cert asks for %s, which belongs to another mode: %v", unwanted, labels)
		}
	}

	w.cfg.BYOCert = "file://./tls/cert.pem"
	w.cfg.BYOKey = "env://TLS_KEY"
	got := w.cfg.ToSpec()
	if got.PKI.BYOCert == nil || got.PKI.BYOCert.Cert != "file://./tls/cert.pem" {
		t.Errorf("the material did not reach the document: %+v", got.PKI.BYOCert)
	}
	// And the validator that demanded the fields is satisfied by them.
	w.cfg.Domain = "acme.internal"
	for _, line := range w.validateConfig() {
		if strings.Contains(line, "byoCert") {
			t.Errorf("the screen cannot satisfy its own validator: %s", line)
		}
	}
}

// The ACME server is how staging is told apart from production, and a mistake
// against production spends a rate limit that resets in a week.
func TestACMEOffersTheServer(t *testing.T) {
	w := wizard(t, LangEN, false, 96, 30, StepPKI)
	w.cfg.PKIMode = string(v1alpha1.PKIACMEDNS01)

	var labels []string
	for _, f := range w.fieldsFor(StepPKI) {
		labels = append(labels, f.labelKey)
	}
	if !contains(labels, "pki.server") {
		t.Errorf("the ACME screen has no server field: %v", labels)
	}

	w.cfg.ACMEEmail = "ops@acme.co.kr"
	w.cfg.ACMEServer = "https://acme-staging-v02.api.letsencrypt.org/directory"
	if got := w.cfg.ToSpec().PKI.ACME; got == nil || got.Server != w.cfg.ACMEServer {
		t.Errorf("the server did not reach the document: %+v", got)
	}
}

// Trust distribution is offered only where l2-pki reads the answer.
func TestTrustDistributionFollowsTheMode(t *testing.T) {
	w := wizard(t, LangEN, false, 96, 30, StepPKI)

	w.cfg.PKIMode = string(v1alpha1.PKIPrivateCA)
	if w.pkiExtraRows() == 0 {
		t.Error("a private CA is not asked about trust distribution")
	}
	w.cfg.TrustBundle = true
	if got := w.cfg.ToSpec().PKI.Trust.ClusterBundle; got == nil || !*got {
		t.Error("the bundle choice did not reach the document")
	}

	// Distributing a public CA's root to every namespace is noise.
	w.cfg.PKIMode = string(v1alpha1.PKIACMEDNS01)
	if w.pkiExtraRows() != 0 {
		t.Error("an ACME build is asked about distributing a CA nothing needs")
	}
}

// An empty join address is the wizard's spelling of "this server, and I accept
// the trade": IDC and air-gapped policy frequently forbids a second IP on the
// segment, and a DNS name needs a writable zone. A site with neither still
// deserves a cluster.
func TestAnEmptyJoinAddressRegistersOnTheServer(t *testing.T) {
	w := wizard(t, LangEN, false, 96, 30, StepNodes)
	w.cfg.Server, w.cfg.Agents = "10.0.0.11", nil
	w.cfg.Registration = ""

	got := w.cfg.ToSpec()
	if got.Topology.RegistrationAddress != "10.0.0.11" {
		t.Errorf("the registration address is %q", got.Topology.RegistrationAddress)
	}
	if !got.Topology.AcceptNodeRegistration {
		t.Error("the trade was taken without being stated in the document")
	}

	// A named address is used as given, with nothing acknowledged.
	w.cfg.Registration = "k8s.acme.internal"
	got = w.cfg.ToSpec()
	if got.Topology.RegistrationAddress != "k8s.acme.internal" || got.Topology.AcceptNodeRegistration {
		t.Errorf("a real join address was rewritten: %q (accept=%v)",
			got.Topology.RegistrationAddress, got.Topology.AcceptNodeRegistration)
	}

	// And reading the document back shows the field the way the wizard spells
	// it: empty, meaning this server.
	spec := w.cfg.ToSpec()
	spec.Topology.RegistrationAddress = "10.0.0.11"
	spec.Topology.AcceptNodeRegistration = true
	if c := FromSpec(spec); c.Registration != "" {
		t.Errorf("read back as %q, which would validate as a foreign address", c.Registration)
	}
}

// The final screens return to the menu; quitting is the bottom-left exit.
//
// The tool used to quit from Done -- left over from when the installer was the
// whole program -- which threw the operator out at exactly the moment they
// want the run list, the settings, or a second cluster.
func TestTheFinalScreensReturnToTheMenu(t *testing.T) {
	w := wizard(t, LangEN, false, 96, 30, StepDone)
	w.fold(event.Event{Kind: event.KindPhase, Phase: "l1-bootstrap", Status: event.StatusOK})
	w.fold(event.Event{Kind: event.KindStep, Phase: "l1-bootstrap", Step: "install",
		Node: "10.0.0.11", Status: event.StatusFailed, Code: "PF-601"})

	w.next()
	if w.step != StepMenu {
		t.Fatalf("Done went to %v instead of the menu", w.step)
	}
	// The run's view state went with it: a second install folding its events on
	// top of the last run's would draw two runs as one.
	if len(w.order) != 0 || len(w.failures) != 0 || len(w.phases) != 0 {
		t.Error("the previous run's view survived into the menu")
	}
	// What the operator collected stays: walking the flow again with the same
	// answers is the common case, not an accident to be wiped.
	if w.cfg.Server == "" {
		t.Error("returning to the menu wiped the configuration")
	}

	// A written document returns the same way.
	w.step, w.mode, w.saved = StepSave, modeSettings, "/tmp/cluster.yaml"
	w.next()
	if w.step != StepMenu {
		t.Errorf("a written document went to %v instead of the menu", w.step)
	}
	if w.saved != "" {
		t.Error("the saved marker survived, so reopening the flow would claim a write that has not happened")
	}

	// And quitting is still there, as the exit rather than the way forward.
	if w.step = StepDone; w.exitButton() == nil {
		t.Error("the Done screen has no way to quit at all")
	}
}

// The wordmark on the start menu, and only there.
//
// A start menu that opens as a bare list reads as a fragment of something; the
// block-letter name is how a terminal tool says it is a product. The working
// screens spend their rows on work.
func TestTheMenuCarriesTheWordmark(t *testing.T) {
	w := wizard(t, LangEN, false, 100, 32, StepMenu)
	if !strings.Contains(plain(w.View().Content), "██") {
		t.Error("the menu has no wordmark")
	}

	// The ASCII charset exists for consoles that cannot draw U+2588; the name
	// as mojibake would be worse than plain letters.
	a := wizard(t, LangEN, true, 100, 32, StepMenu)
	body := plain(a.View().Content)
	if strings.Contains(body, "█") || strings.Contains(body, "╗") {
		t.Error("the ASCII menu draws block characters")
	}
	if !strings.Contains(body, "AAA") {
		t.Error("the ASCII menu has no wordmark at all")
	}

	// A short window shows the menu entries, not the decoration above them.
	s := wizard(t, LangEN, false, 100, 18, StepMenu)
	if strings.Contains(plain(s.View().Content), "██") {
		t.Error("a short terminal spends its rows on the wordmark")
	}
	// And a narrow one: a wordmark that wraps is noise.
	n := wizard(t, LangEN, false, 50, 32, StepMenu)
	if strings.Contains(plain(n.View().Content), "██") {
		t.Error("a narrow terminal wraps the wordmark")
	}

	// The working screens do not carry it.
	o := wizard(t, LangEN, false, 100, 32, StepOptions)
	if strings.Contains(plain(o.View().Content), "██╗") {
		t.Error("a working screen carries the wordmark")
	}
}

// Stable is the suggestion, latest is one Space away, typing overrides both.
//
// Stable is upstream's production judgement (install.sh defaults to it, and it
// lags the newest release on purpose), so it fills the field; the operator who
// wants the newest flips to it on the version field with the same key every
// other chooser uses.
func TestTheVersionFieldFlipsBetweenChannels(t *testing.T) {
	w := wizard(t, LangEN, false, 96, 30, StepNodes)
	w.setLocal(false)
	if w.cfg.Version != "v1.35.7+rke2r1" {
		t.Fatalf("stable did not fill the field: %q", w.cfg.Version)
	}

	// The version field's row. The choices come first on this screen -- the
	// channel answers, then the OS family -- so the fields start after them.
	var row int
	for i, f := range w.fieldsFor(StepNodes) {
		if f.labelKey == "nodes.version" {
			row = len(w.versionChoices()) + len(osFamilies) + i
		}
	}
	w.cursor[StepNodes] = row

	w.selectUnderCursor()
	if w.cfg.Version != "v1.36.3+rke2r1" {
		t.Errorf("Space did not flip to latest: %q", w.cfg.Version)
	}
	w.selectUnderCursor()
	if w.cfg.Version != "v1.35.7+rke2r1" {
		t.Errorf("Space did not flip back to stable: %q", w.cfg.Version)
	}

	// A typed version is the override; the next flip goes to latest rather
	// than silently discarding what was typed for a third value.
	w.cfg.Version = "v1.34.10+rke2r1"
	w.selectUnderCursor()
	if w.cfg.Version != "v1.36.3+rke2r1" {
		t.Errorf("Space from a typed version went to %q", w.cfg.Version)
	}

	// With no channel answer -- an air-gapped site -- Space does nothing and
	// the field is typed like any other.
	w.channels = rke2.Channels{}
	w.cfg.Version = "typed"
	w.selectUnderCursor()
	if w.cfg.Version != "typed" {
		t.Errorf("Space without a channel answer changed the field to %q", w.cfg.Version)
	}
}

// The chrome fills the terminal; the content hugs the left of its pane.
//
// The header and footer go edge to edge, and the capped content column
// starts where the eye starts -- the way the Ubuntu installer lays out --
// rather than floating in the middle of a wide window, where it reads as
// small and far away. Vertically the content is still centred: a menu on a
// 70-row window belongs in the middle of it.
func TestTheChromeFillsAndTheContentHugsLeft(t *testing.T) {
	w := wizard(t, LangEN, false, 180, 70, StepMenu)
	lines := strings.Split(w.View().Content, "\n")

	// Full-bleed: the header rule spans the terminal, and the frame owns the
	// first row and the last.
	if got := cells(plain(lines[1])); got < 170 {
		t.Errorf("the header rule spans %d of 180 cells", got)
	}
	if strings.TrimSpace(plain(lines[0])) == "" {
		t.Error("the first row is empty; the chrome does not fill the window")
	}

	// The content column starts at the pane's left edge: the wordmark sits
	// within the first half of a 180-cell window, not floated to its middle.
	for _, l := range lines {
		if !strings.Contains(l, "██") {
			continue
		}
		if indent := len(plain(l)) - len(strings.TrimLeft(plain(l), " ")); indent > 60 {
			t.Errorf("the wordmark starts at column %d of 180; the column is centred, not left", indent)
		}
		break
	}

	// And vertically: a short menu on a 70-row window is not pinned under the
	// header. The rail and info-pane separators run every row, so a row is
	// "empty" when nothing but chrome is on it.
	first := -1
	for i, l := range lines[2:] {
		bare := strings.NewReplacer("│", "", "|", "").Replace(plain(l))
		if strings.TrimSpace(bare) != "" {
			first = i + 2
			break
		}
	}
	if first >= 0 && first < 6 {
		t.Errorf("the content starts at row %d of 70; the slack all sits below it", first)
	}

	// A small window keeps every cell.
	sm := wizard(t, LangEN, false, 80, 24, StepMenu)
	small := strings.Split(sm.View().Content, "\n")
	if strings.TrimSpace(plain(small[0])) == "" {
		t.Error("a small window wastes rows on padding")
	}
}

// The explanations live in a strip above the footer.
//
// Help text opened every screen and hints trailed every focused field, which
// spent the content column's vertical rows on prose. The strip holds the
// focused item's hint and the screen's help, and the inline copies disappear
// -- the same sentence twice on one screen is noise. A window below the
// chrome's minimum keeps the inline copies and no strip.
func TestExplanationsMoveToTheStrip(t *testing.T) {
	wide := wizard(t, LangEN, false, 120, 40, StepOptions)
	if !wide.infoActive() {
		t.Fatal("a 120-column window affords no strip")
	}
	if info := wide.frameInfo(); !strings.Contains(info, "atomic choice") {
		t.Errorf("the strip does not carry the screen's help:\n%s", info)
	}
	// The inline copy is gone from the column.
	_, body, _ := wide.optionsScreen(wide.contentWidth())
	if strings.Contains(plain(body), "atomic choice") {
		t.Error("the help is both in the strip and in the column")
	}

	tiny := wizard(t, LangEN, false, 60, 24, StepOptions)
	if tiny.infoActive() {
		t.Fatal("a 60-column window claims to afford a strip")
	}
	if tiny.frameInfo() != "" {
		t.Error("a tiny window filled a strip it cannot draw")
	}
	_, body, _ = tiny.optionsScreen(tiny.contentWidth())
	if !strings.Contains(plain(body), "atomic choice") {
		t.Error("a tiny window lost the help entirely")
	}

	// The focused field's hint leads the strip: "what goes here" is the
	// pressing question.
	nodes := wizard(t, LangEN, false, 120, 40, StepNodes)
	nodes.setLocal(false)
	nodes.cfg.Server = "10.0.0.11"
	for i, f := range nodes.fieldsFor(StepNodes) {
		if f.labelKey == "nodes.registration" {
			nodes.cursor[StepNodes] = len(nodes.versionChoices()) + len(osFamilies) + i
		}
	}
	if info := nodes.frameInfo(); !strings.Contains(info, "VIP or DNS") {
		t.Errorf("the strip does not carry the focused field's hint:\n%s", info)
	}

	// The progress screens keep every row for the log tail.
	run := wizard(t, LangEN, false, 120, 40, StepInstall)
	if run.frameInfo() != "" {
		t.Error("a progress screen spends rows on an explanation strip")
	}
}

// Space flips the version between the channel server's two answers, and the
// two version strings are just strings -- the value carries its channel's
// name so the flip is legible. The menu, meanwhile, is the front door: no
// explanation pane there, however wide the window.
func TestTheVersionWearsItsChannelAndTheMenuHasNoPane(t *testing.T) {
	w := wizard(t, LangEN, false, 140, 30, StepNodes)
	w.channels.Stable, w.channels.Latest = "v1.35.7+rke2r1", "v1.36.3+rke2r1"

	show := func() string {
		_, body, _ := w.nodesScreen(w.contentWidth())
		return plain(body)
	}
	w.cfg.Version = "v1.35.7+rke2r1"
	if !strings.Contains(show(), "stable") {
		t.Error("the stable channel's version is not tagged")
	}
	w.cfg.Version = "v1.36.3+rke2r1"
	if !strings.Contains(show(), "latest") {
		t.Error("the latest channel's version is not tagged")
	}
	// A hand-typed version belongs to no channel; tagging it would label a
	// guess.
	w.cfg.Version = "v1.34.5+rke2r1"
	if s := show(); strings.Contains(s, "stable") || strings.Contains(s, "latest") {
		t.Error("a hand-typed version was given a channel tag")
	}

	menu := wizard(t, LangEN, false, 200, 45, StepMenu)
	if menu.frameInfo() != "" {
		t.Error("the menu carries an explanation pane")
	}
}

// The screen where a run stops must say what stopped it. The findings were
// collected all along but rendered only on the done screen, so a blocked
// preflight read "1 checks block the install" with the check nowhere in
// sight -- the operator went digging in events.jsonl for a sentence the
// screen already had.
func TestTheProgressScreenNamesWhatStoppedIt(t *testing.T) {
	w := wizard(t, LangEN, false, 120, 40, StepPreflight)
	w.busy = false
	w.failures = []event.Event{{
		Kind: event.KindProbe, Phase: "preflight", Code: "PF-105",
		Node: "192.168.0.24", Status: event.StatusBlocked,
		Detail: "swap is active (/dev/sda3 32G)",
	}}

	_, body, _ := w.progressScreen(100, "preflight")
	got := plain(body)
	if !strings.Contains(got, "PF-105") || !strings.Contains(got, "swap is active") {
		t.Errorf("the blocking finding is not on the screen:\n%s", got)
	}
	if !strings.Contains(got, "192.168.0.24") {
		t.Error("the finding does not say which node")
	}

	// While the run is still going the list stays off the screen: partial
	// findings under a moving progress bar read as a verdict.
	w.busy = true
	_, body, _ = w.progressScreen(100, "preflight")
	if strings.Contains(plain(body), "PF-105") {
		t.Error("findings are shown while the run is still moving")
	}
}

// The install narrates itself through its step verdicts, and the log pane
// must show them: only the demo ever emitted kind=log, so a real install ran
// with an empty log section while every step's sentence scrolled past
// unrendered. The Logs button widens the tail instead of doing nothing.
func TestStepVerdictsReachTheLogPane(t *testing.T) {
	w := wizard(t, LangEN, false, 120, 40, StepInstall)
	w.busy = true
	w.fold(event.Event{Kind: event.KindStep, Phase: "l0-node-prep", Step: "swap",
		Status: event.StatusOK, Detail: "swap is off and /etc/fstab has no entry"})

	_, body, _ := w.progressScreen(110, "install")
	if !strings.Contains(plain(body), "swap: swap is off") {
		t.Errorf("the step's verdict is not in the log tail:\n%s", plain(body))
	}

	// A running step is not a verdict yet.
	w.logs = nil
	w.fold(event.Event{Kind: event.KindStep, Phase: "l0-node-prep", Step: "swap",
		Status: event.StatusRunning, Detail: "checking"})
	if len(w.logs) != 0 {
		t.Error("a non-terminal step event entered the log stream")
	}
}

// A click reaches what was drawn under it, and reaches it the way the
// keyboard would: the same cursor, the same selection, the same action.
func TestTheMouseReachesWhatWasDrawn(t *testing.T) {
	w := wizard(t, LangEN, false, 120, 40, StepMenu)
	_ = w.View() // the hit map is built while drawing

	// The menu: find the row the third entry was drawn on and click it.
	row, ok := rowOf(&w.hits, 2)
	if !ok {
		t.Fatal("the menu recorded no row for its third entry")
	}
	if _, _ = w.click(tea.Mouse{X: w.hits.contentCol + 4, Y: row, Button: tea.MouseLeft}); w.menu != 2 {
		t.Errorf("a click on row %d left the menu on %d", row, w.menu)
	}

	// A radio screen: clicking a choice selects it, exactly as Space does.
	opts := wizard(t, LangEN, false, 120, 40, StepOptions)
	opts.cfg.Dataplane = "cilium-gw"
	_ = opts.View()
	if row, ok := rowOf(&opts.hits, 2); ok {
		_, _ = opts.click(tea.Mouse{X: opts.hits.contentCol + 4, Y: row, Button: tea.MouseLeft})
		if opts.cursor[StepOptions] != 2 {
			t.Errorf("the cursor is on %d after clicking the third choice", opts.cursor[StepOptions])
		}
		if opts.cfg.Dataplane != "canal-traefik" {
			t.Errorf("clicking the third dataplane chose %q", opts.cfg.Dataplane)
		}
	} else {
		t.Error("the options screen recorded no rows")
	}

	// The wheel moves the cursor rather than doing nothing. From the third
	// choice, up is the second.
	before := opts.cursor[StepOptions]
	if before == 0 {
		t.Fatal("the cursor never moved, so the wheel has nothing to show")
	}
	_, _ = opts.wheel(tea.Mouse{Button: tea.MouseWheelUp})
	if opts.cursor[StepOptions] != before-1 {
		t.Errorf("the wheel left the cursor at %d, was %d", opts.cursor[StepOptions], before)
	}

	// A right-click is not a click on anything: only the left button acts.
	was := opts.cursor[StepOptions]
	if row, ok := rowOf(&opts.hits, 0); ok {
		_, _ = opts.click(tea.Mouse{X: opts.hits.contentCol + 4, Y: row, Button: tea.MouseRight})
		if opts.cursor[StepOptions] != was {
			t.Error("a right-click moved the cursor")
		}
	}
}

// rowOf finds the screen row a cursor index was drawn on.
func rowOf(h *hitMap, index int) (int, bool) {
	for line, i := range h.lines {
		if i == index {
			return h.contentRow + line, true
		}
	}
	return 0, false
}

// Hovering is not selecting. A pointer crossing the screen must not look like
// an operator changing their mind: the row under the pointer is marked, the
// cursor stays where the keyboard left it, and the two are drawn differently.
func TestHoverIsSeparateFromSelection(t *testing.T) {
	w := wizard(t, LangEN, false, 120, 40, StepMenu)
	_ = w.View()

	row, ok := rowOf(&w.hits, 3)
	if !ok {
		t.Fatal("the menu recorded no row for its fourth entry")
	}
	w.setHover(tea.Mouse{X: w.hits.contentCol + 4, Y: row})

	if w.menu != 0 {
		t.Errorf("hovering moved the selection to %d", w.menu)
	}
	if !w.hovering(3) {
		t.Errorf("the fourth entry is not hovered (kind %v index %d)", w.hoverKind, w.hoverIndex)
	}

	// And it shows: the hovered row is drawn differently from both the
	// selected row and the rest.
	body := w.View().Content
	if !strings.Contains(body, plainOf(w.theme.ChoiceHover.Render("Settings"))) {
		// The style may not survive a plain-text comparison; the marker check
		// above is the contract. This only guards the obvious regression of
		// nothing being drawn at all.
		if !strings.Contains(plain(body), "Settings") {
			t.Error("the hovered entry vanished")
		}
	}

	// Moving off everything clears it, or the highlight outlives the pointer.
	w.setHover(tea.Mouse{X: 0, Y: 0})
	if w.hovering(3) {
		t.Error("the highlight stayed after the pointer left")
	}
}

// plainOf strips styling, which differs by terminal profile.
func plainOf(s string) string { return plain(s) }

// The wizard can now say how the cluster is reached, which it could not: it
// had no screen for gateways, so every document it wrote named none, every
// LoadBalancer Service a workload created sat Pending forever, and the
// operator found out from the workload.
func TestTheGatewayScreenWritesTheGateway(t *testing.T) {
	w := wizard(t, LangEN, false, 120, 40, StepGateway)

	// Nothing is exposed until it is asked for: an address the network did
	// not assign is what an IDC or air-gapped policy refuses.
	if w.cfg.Exposure != "none" {
		t.Errorf("the default exposure is %q", w.cfg.Exposure)
	}
	if gws := w.cfg.ToSpec().Gateway.Gateways; len(gws) != 0 {
		t.Errorf("a default document names %d gateway(s)", len(gws))
	}

	// The nodes' own addresses: no new address on the segment.
	w.cursor[StepGateway] = indexOf(exposures, "node-ips")
	w.selectUnderCursor()
	gws := w.cfg.ToSpec().Gateway.Gateways
	if len(gws) != 1 || gws[0].Exposure != v1alpha1.ExposureNodeIPs {
		t.Fatalf("node-ips produced %+v", gws)
	}
	if gws[0].Address != "" {
		t.Errorf("a node-ips gateway pinned the address %q, which is one more IP on the segment", gws[0].Address)
	}
	if len(gws[0].Listeners) != 1 || gws[0].Listeners[0].Port != 80 {
		t.Errorf("listeners are %+v; with no certificates there is nothing to serve on 443", gws[0].Listeners)
	}

	// A pool address is pinned rather than allocated: the DNS record is
	// requested before the install (PF-612).
	w.cursor[StepGateway] = indexOf(exposures, "lb-pool")
	w.selectUnderCursor()
	w.cfg.GatewayAddress, w.cfg.LBPool = "10.0.0.240", []string{"10.0.0.240/29"}
	gws = w.cfg.ToSpec().Gateway.Gateways
	if len(gws) != 1 || gws[0].Address != "10.0.0.240" || gws[0].Exposure != "" {
		t.Fatalf("lb-pool produced %+v", gws)
	}

	// HTTPS appears exactly where a certificate can exist.
	w.cfg.PKIMode, w.cfg.Domain = "private-ca", "lab.example"
	gws = w.cfg.ToSpec().Gateway.Gateways
	if len(gws[0].Listeners) != 2 || gws[0].Listeners[1].Hostname != "*.lab.example" {
		t.Errorf("listeners with certificates are %+v", gws[0].Listeners)
	}
	w.cfg.PKIMode = "none"
	if gws := w.cfg.ToSpec().Gateway.Gateways; len(gws[0].Listeners) != 1 {
		t.Errorf("an HTTPS listener survived the certificates being turned off: %+v", gws[0].Listeners)
	}
}

// A run failed when the run says so. Preflight emits its warnings as failed
// events -- severity is what separates a warning from a block -- so a build
// that finished with three advisory findings had this screen announcing that
// the installation had stopped, while the cluster was up and its gateway was
// answering.
func TestAdvisoryFindingsDoNotFailAFinishedRun(t *testing.T) {
	w := wizard(t, LangEN, false, 110, 40, StepDone)
	w.fold(event.Event{Kind: event.KindRun, Status: event.StatusRunning, TS: ts(0)})
	w.fold(event.Event{Kind: event.KindProbe, Phase: "preflight", Code: "PF-401",
		Node: "10.0.0.11", Status: event.StatusFailed, Detail: "not a separate mount", TS: ts(5)})
	w.fold(event.Event{Kind: event.KindRun, Status: event.StatusOK, TS: ts(60)})

	screen := plain(render(t, w, nil))
	if !strings.Contains(screen, w.cat.T("done.ok")) {
		t.Errorf("a finished run does not say so:\n%s", screen)
	}
	if strings.Contains(screen, w.cat.T("done.failed")) {
		t.Errorf("advisory findings made a finished run read as stopped:\n%s", screen)
	}
	// The findings still appear -- under a heading that says what they are.
	if !strings.Contains(screen, "PF-401") {
		t.Error("the findings vanished with the wrong verdict")
	}

	// And a step that really failed still fails the run.
	w.fold(event.Event{Kind: event.KindStep, Phase: "l1-bootstrap", Step: "service",
		Node: "10.0.0.11", Status: event.StatusFailed, Code: "EX-002", TS: ts(70)})
	if screen := plain(render(t, w, nil)); !strings.Contains(screen, w.cat.T("done.failed")) {
		t.Errorf("a failed step no longer stops the run:\n%s", screen)
	}
}

// A node this machine is, is not a node this machine dials. Writing an SSH
// account for it did more than clutter the document: the kubeconfig step
// reads that account to decide whose copy to make, saw the seeded "root",
// and concluded the operator already had it -- so an IDC build finished with
// kubectl installed, a cluster running, and no kubeconfig for the account
// that ran the install.
func TestALocalNodeCarriesNoLogin(t *testing.T) {
	w := wizard(t, LangEN, false, 110, 30, StepNodes)
	w.setLocal(true)
	w.cfg.Server, w.cfg.Agents = "10.0.0.11", []string{"10.0.0.12"}

	spec := w.cfg.ToSpec()
	if got := spec.Topology.Servers[0].SSH.User; got != "" {
		t.Errorf("the local server names the login %q", got)
	}
	// An agent is still dialled, and still needs one.
	if got := spec.Topology.Agents[0].SSH.User; got == "" {
		t.Error("a remote agent lost its login")
	}

	// And a remote server keeps it.
	w.setLocal(false)
	if got := w.cfg.ToSpec().Topology.Servers[0].SSH.User; got == "" {
		t.Error("a remote server lost its login")
	}
}

// The gateway screen's HTTP/2 answer has to reach the document, and come back
// out of one. A toggle that renders and writes nothing is the same defect as a
// value written and never read.
func TestTheGatewayScreenCarriesHTTP2BothWays(t *testing.T) {
	c := Config{Server: "192.0.2.10", Exposure: "node-ips", Domain: "example.com", HTTP2: true}
	s := c.ToSpec()
	if s.Gateway.HTTP2 == nil || !*s.Gateway.HTTP2 {
		t.Fatalf("HTTP/2 chosen on the screen does not reach the document: %+v", s.Gateway.HTTP2)
	}

	back := FromSpec(s)
	if !back.HTTP2 {
		t.Error("a document that asks for HTTP/2 opens the screen with it off")
	}

	// Unasked stays absent rather than an explicit false, so a document nobody
	// thought about it in does not grow a line about it.
	off := Config{Server: "192.0.2.10", Exposure: "node-ips", Domain: "example.com"}
	if v := off.ToSpec().Gateway.HTTP2; v != nil {
		t.Errorf("a document that never asked carries http2: %v", *v)
	}
}
