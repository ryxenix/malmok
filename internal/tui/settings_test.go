package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"platform.ryxen.dev/platformctl/api/v1alpha1"
	"platform.ryxen.dev/platformctl/internal/event"
	"platform.ryxen.dev/platformctl/internal/exec"
	"platform.ryxen.dev/platformctl/internal/spec"
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
	w := wizard(t, LangEN, false, 96, 30, StepPKI)
	w.mode = modeSettings
	w.next()
	if w.step != StepSave {
		t.Errorf("after the certificates screen the settings flow reached %v", w.step)
	}

	w = wizard(t, LangEN, false, 96, 30, StepPKI)
	w.mode = modeInstall
	w.next()
	if w.step != StepPreflight {
		t.Errorf("after the certificates screen the install flow reached %v", w.step)
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
	_, settings, _ := w.openScreen(80)

	w.mode = modeUpgrade
	_, upgrade, _ := w.openScreen(80)

	if settings == upgrade {
		t.Error("both flows show the same explanation")
	}
	// Matched on the unwrapped part: the pane wraps, so a phrase that spans a
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

	// And choosing one takes.
	addrs := exec.LocalIPv4s()
	w.cursor[StepNodes] = len(addrs) - 1
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
	w.step = StepPKI

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
