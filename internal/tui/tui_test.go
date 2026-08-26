package tui

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ryxen/malmok/api/v1alpha1"
	"github.com/ryxen/malmok/internal/event"
	"github.com/ryxen/malmok/internal/rke2"
	"github.com/ryxen/malmok/internal/spec"
)

func ts(sec int) event.Timestamp {
	return event.NewTimestamp(time.Date(2026, 8, 3, 9, 31, sec, 0, time.UTC))
}

func wizard(t *testing.T, lang Lang, ascii bool, w, h int, step Step) *Wizard {
	t.Helper()
	m, err := NewWizard("01KZ3PTJMGR68RHFQ0V8TK2KJZ", ascii, true, lang, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.width, m.height, m.step = w, h, step
	// What the channel server would have answered. The version is discovered
	// rather than seeded, and the tests are not on the network.
	m.Update(versionMsg{ch: rke2.Channels{Stable: "v1.35.7+rke2r1", Latest: "v1.36.3+rke2r1"}})
	return m
}

// runInProgress is the state the mockup shows: three phases done, one running
// with a retry, two waiting.
func runInProgress() []event.Event {
	phase := func(id string, s event.Status) event.Event {
		return event.Event{Kind: event.KindPhase, Phase: id, Status: s, TS: ts(0)}
	}
	return []event.Event{
		{Kind: event.KindRun, Status: event.StatusRunning, TS: ts(0)},
		phase("preflight", event.StatusPending), phase("plan", event.StatusPending),
		phase("l0-node-prep", event.StatusPending), phase("l1-bootstrap", event.StatusPending),
		phase("l1-join-agent", event.StatusPending), phase("l2-dataplane", event.StatusPending),
		phase("preflight", event.StatusOK), phase("plan", event.StatusOK),
		phase("l0-node-prep", event.StatusOK), phase("l1-bootstrap", event.StatusRunning),
		{Kind: event.KindStep, Phase: "l1-bootstrap", Step: "rke2-server-ready",
			Node: "10.10.0.11", Status: event.StatusRunning, TS: ts(44),
			Progress: &event.Progress{Done: 3, Total: 7}, Attempt: 2, MaxAttempts: 3},
		{Kind: event.KindLog, Phase: "l1-bootstrap", Node: "10.10.0.11",
			Level: event.LevelInfo, TS: ts(52), Detail: "starting rke2-server"},
	}
}

func render(t *testing.T, m *Wizard, evs []event.Event) string {
	t.Helper()
	for _, e := range evs {
		m.fold(e)
	}
	return m.View().Content
}

var sgr = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func plain(s string) string { return sgr.ReplaceAllString(s, "") }

// allSteps renders every screen, so a layout rule is checked against the whole
// wizard rather than against whichever screen was in mind when it was written.
func allSteps(t *testing.T, lang Lang, ascii bool, w, h int) map[Step]string {
	t.Helper()
	out := map[Step]string{}
	for s := StepProfile; s <= StepDone; s++ {
		out[s] = render(t, wizard(t, lang, ascii, w, h, s), runInProgress())
	}
	return out
}

// A Korean glyph occupies two terminal columns. Measuring in runes lays out
// correctly in English and wrecks every column in Korean, which would make the
// language toggle a promise the layout cannot keep.
func TestNoLineExceedsTheTerminalWidth(t *testing.T) {
	for _, lang := range []Lang{LangEN, LangKO} {
		for _, ascii := range []bool{false, true} {
			for _, w := range []int{80, 100, 120} {
				for step, screen := range allSteps(t, lang, ascii, w, 24) {
					for i, line := range strings.Split(screen, "\n") {
						if got := cells(plain(line)); got > w {
							t.Errorf("lang=%s ascii=%v width=%d step=%d: line %d is %d cells\n%s",
								lang, ascii, w, step, i, got, plain(line))
						}
					}
				}
			}
		}
	}
}

// The phase id is padded to a fixed cell width, so the status word begins at
// the same column on every row in both languages.
func TestPhaseStatusColumnAligns(t *testing.T) {
	for _, lang := range []Lang{LangEN, LangKO} {
		screen := plain(render(t, wizard(t, lang, false, 90, 26, StepInstall), runInProgress()))

		var starts []int
		for _, line := range strings.Split(screen, "\n") {
			i := strings.Index(line, "│")
			if i < 0 {
				continue
			}
			body := line[i+len("│"):]
			for _, id := range []string{"preflight", "l1-bootstrap", "l2-dataplane"} {
				at := strings.Index(body, id)
				if at < 0 {
					continue
				}
				starts = append(starts, cells(body[:at+len(id)])-cells(id))
			}
		}
		if len(starts) < 3 {
			t.Fatalf("lang=%s: expected three phase rows, got %d", lang, len(starts))
		}
		for _, s := range starts[1:] {
			if s != starts[0] {
				t.Errorf("lang=%s: phase column starts at %d and %d", lang, starts[0], s)
			}
		}
	}
}

// The screen must survive a terminal that cannot draw box glyphs -- a serial
// console, an IPMI viewer, PuTTY with the wrong codepage. That is exactly where
// somebody is sitting when an install is going badly.
func TestASCIIFallbackDropsEveryWideGlyph(t *testing.T) {
	screens := allSteps(t, LangEN, true, 90, 26)
	for step, screen := range screens {
		for _, bad := range []string{"✓", "▸", "✗", "─", "│", "█", "░", "·", "–"} {
			if strings.Contains(screen, bad) {
				t.Errorf("step=%d: ASCII rendering still contains %q", step, bad)
			}
		}
	}
	if !strings.Contains(plain(screens[StepInstall]), "+ preflight") {
		t.Errorf("ASCII markers are missing:\n%s", plain(screens[StepInstall]))
	}
}

// Colour is decoration. Weight and reverse video survive on a monochrome
// console; an RGB sequence does not, so the mono theme must not emit one.
func TestMonoThemeEmitsNoColour(t *testing.T) {
	colour := regexp.MustCompile(`\x1b\[[0-9;]*(?:38|48);`)
	for step, screen := range allSteps(t, LangEN, false, 90, 26) {
		if colour.MatchString(screen) {
			t.Errorf("step=%d: the mono theme emitted a colour sequence", step)
		}
	}
}

// A verdict has to be readable from the marker alone.
func TestVerdictIsLegibleFromTheMarker(t *testing.T) {
	screen := plain(render(t, wizard(t, LangEN, false, 90, 26, StepInstall),
		append(runInProgress(), event.Event{Kind: event.KindPhase, Phase: "l1-bootstrap",
			Status: event.StatusFailed, Code: "EX-102", TS: ts(59)})))

	if !strings.Contains(screen, "✗ l1-bootstrap") {
		t.Errorf("a failed phase is not marked:\n%s", screen)
	}
}

// Below the width floor the rail is dropped before the content: knowing which
// choice is in front of you beats knowing which step it belongs to.
func TestNarrowTerminalDropsTheRailNotTheContent(t *testing.T) {
	screen := plain(render(t, wizard(t, LangEN, false, 60, 24, StepOptions), nil))

	if strings.Contains(screen, "│") {
		t.Error("the rail survived on a narrow terminal; the content pane needs the width")
	}
	if !strings.Contains(screen, "cilium-gw") {
		t.Errorf("the choices were dropped instead:\n%s", screen)
	}
}

// At and above the documented floor the whole phase list and the buttons fit.
func TestChromeFloorFitsTheWholeRun(t *testing.T) {
	for _, h := range []int{minChromeH, 20, 24, 30} {
		screen := plain(render(t, wizard(t, LangEN, false, 90, h, StepInstall), runInProgress()))

		if !strings.Contains(screen, "l2-dataplane") {
			t.Errorf("height=%d: the phase list was truncated", h)
		}
		if !strings.Contains(screen, "[") {
			t.Errorf("height=%d: the button row was dropped", h)
		}
	}
}

// Failures are listed as plain rows so an operator can select them into a
// ticket without dragging a border along.
func TestFinishedScreenListsFailuresUnboxed(t *testing.T) {
	screen := plain(render(t, wizard(t, LangEN, false, 90, 26, StepDone),
		append(runInProgress(), event.Event{
			Kind: event.KindStep, Phase: "l1-bootstrap", Step: "rke2-server-ready",
			Node: "10.10.0.11", Status: event.StatusFailed, Code: "PF-601", TS: ts(59),
			Detail: "port 9345 unreachable from 10.10.20.21"})))

	if !strings.Contains(screen, "PF-601") {
		t.Errorf("the failure code is missing:\n%s", screen)
	}
	if !strings.Contains(screen, "port 9345 unreachable") {
		t.Errorf("the failure detail is missing:\n%s", screen)
	}
	// The rail divider is legitimate; a box around the failure list is not.
	for _, border := range []string{"┌", "└", "├", "┐", "┘"} {
		if strings.Contains(screen, border) {
			t.Errorf("a border (%s) was drawn, which breaks copy-paste", border)
		}
	}
}

// Every step must offer a way forward, or the operator is stuck on it.
func TestEveryStepOffersAWayForward(t *testing.T) {
	for s := StepProfile; s <= StepDone; s++ {
		if got := wizard(t, LangEN, false, 90, 26, s).buttons(); len(got) == 0 {
			t.Errorf("step %d has no buttons", s)
		}
	}
}

// Back must not be offered while nodes are being changed: there is nothing to
// go back to once a step has run.
func TestBackIsAbsentWhileInstalling(t *testing.T) {
	m := wizard(t, LangEN, false, 90, 26, StepInstall)
	m.busy = true
	for _, b := range m.buttons() {
		if b.Label == m.cat.T("btn.back") {
			t.Error("Back is offered while an install is running")
		}
	}
}

// Typing has to reach the field rather than the shortcut table, or a hostname
// containing "a" would toggle the character set.
func TestEditingCapturesShortcutKeys(t *testing.T) {
	m := wizard(t, LangEN, false, 90, 26, StepNodes)
	// By name, not by position: this test is about what a field does with the
	// keys, and it should not fail the next time a field moves.
	m.setLocal(false)
	m.editing = true
	m.cursor[StepNodes] = fieldRow(m, StepNodes, "nodes.server")
	m.cfg.Server = ""

	for _, key := range []string{"a", "g", "q", "1", "0", "."} {
		m.key(fakeKey(key))
	}
	if m.cfg.Server != "agq10." {
		t.Errorf("field captured %q, want the literal keystrokes", m.cfg.Server)
	}
	if m.ascii {
		t.Error(`typing "a" toggled the character set instead of entering the field`)
	}
}

func TestCatalogues(t *testing.T) {
	en, err := LoadCatalogue(LangEN)
	if err != nil {
		t.Fatal(err)
	}
	ko, err := LoadCatalogue(LangKO)
	if err != nil {
		t.Fatal(err)
	}

	// Both catalogues must define the same keys, or toggling the language
	// silently replaces words with raw key names.
	for key := range en.table {
		if _, ok := ko.table[key]; !ok {
			t.Errorf("ko is missing %q", key)
		}
	}
	for key := range ko.table {
		if _, ok := en.table[key]; !ok {
			t.Errorf("en is missing %q", key)
		}
	}

	// Every key the screens ask for has to exist in both languages.
	var wanted []string
	for _, key := range stepKeys {
		wanted = append(wanted, key)
	}
	for _, s := range []event.Status{
		event.StatusPending, event.StatusRunning, event.StatusOK,
		event.StatusSkipped, event.StatusFailed, event.StatusBlocked,
	} {
		wanted = append(wanted, "status."+string(s))
	}
	for _, c := range append(append([]choice{}, dataplanes...), storages...) {
		wanted = append(wanted, c.note)
	}
	for _, key := range wanted {
		for _, cat := range []*Catalogue{en, ko} {
			if cat.T(key) == key {
				t.Errorf("%s has no entry for %q", cat.Lang(), key)
			}
		}
	}

	// Codes are identifiers, never translated.
	for key, val := range ko.table {
		if strings.Contains(val, "PF-") || strings.Contains(val, "EX-") {
			t.Errorf("catalogue entry %q embeds a code: %q", key, val)
		}
	}
}

func TestDetectASCII(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"utf8 xterm", map[string]string{"TERM": "xterm-256color", "LANG": "en_US.UTF-8"}, false},
		{"korean utf8", map[string]string{"TERM": "xterm", "LANG": "ko_KR.UTF-8"}, false},
		{"serial vt220", map[string]string{"TERM": "vt220", "LANG": "en_US.UTF-8"}, true},
		{"dumb", map[string]string{"TERM": "dumb"}, true},
		{"latin1", map[string]string{"TERM": "xterm", "LANG": "en_US.ISO-8859-1"}, true},
		{"no locale", map[string]string{"TERM": "xterm"}, true},
		{"forced", map[string]string{"TERM": "xterm", "LANG": "en_US.UTF-8", "MALMOK_ASCII": "1"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectASCII(func(k string) string { return tc.env[k] }); got != tc.want {
				t.Errorf("DetectASCII = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTruncCellsNeverSplitsAWideGlyph(t *testing.T) {
	s := "설치가 진행되는 중입니다"
	for n := 1; n <= cells(s)+2; n++ {
		if got := truncCells(s, n); cells(got) > n {
			t.Errorf("truncCells(%d) produced %d cells: %q", n, cells(got), got)
		}
	}
}

// fakeKey builds the key message the terminal would produce for a single
// printable character, so the key path is exercised end to end rather than the
// edit handler being called directly.
func fakeKey(s string) tea.KeyPressMsg {
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

// press activates a button and runs whatever command it returned, which is
// what the Bubble Tea loop would do.
func (w *Wizard) press(label string) tea.Msg {
	_, cmd := w.activate(label)
	if cmd == nil {
		return nil
	}
	return cmd()
}

// TestWizardWalksToTheEnd drives the whole sequence with Continue and asserts
// it terminates.
//
// This is the test that was missing: Continue and "Retry checks" were wired to
// the same action, so pressing Continue on the checks screen re-ran the checks
// instead of moving on and the wizard could never be finished.
func TestWizardWalksToTheEnd(t *testing.T) {
	var ran []string
	work := func(name string) Work {
		return func(context.Context, Config) error { ran = append(ran, name); return nil }
	}

	m, err := NewWizard("run", false, true, LangEN, work("preflight"), work("install"))
	if err != nil {
		t.Fatal(err)
	}
	m.width, m.height = 90, 26
	// The version is discovered, not seeded, and this test is not on the
	// network -- answer as the channel server would.
	m.Update(versionMsg{ch: rke2.Channels{Stable: "v1.35.7+rke2r1", Latest: "v1.36.3+rke2r1"}})

	seen := map[Step]bool{}
	for i := 0; i < 40 && m.step != StepDone; i++ {
		seen[m.step] = true
		before := m.step

		// Whatever the primary button is on this screen, press it, then deliver
		// the result of any work it started.
		btns := m.buttons()
		if len(btns) == 0 {
			t.Fatalf("step %d offers no way forward", before)
		}
		msg := m.press(btns[len(btns)-1].Label)
		seen[m.step] = true
		if done, ok := msg.(workDoneMsg); ok {
			m.Update(done)
			seen[m.step] = true
		}

		if m.step == before {
			t.Fatalf("stuck on step %d: the primary button did not advance", before)
		}
	}

	if m.step != StepDone {
		t.Fatalf("wizard never reached the final step, stopped at %d", m.step)
	}
	// Every step of the flow, which is the list rather than the enum: the enum
	// also numbers screens that belong to other flows.
	for _, s := range installSteps {
		if !seen[s] && s != StepDone {
			t.Errorf("step %d was skipped", s)
		}
	}
	if len(ran) != 2 || ran[0] != "preflight" || ran[1] != "install" {
		t.Errorf("work ran as %v, want preflight then install exactly once each", ran)
	}
}

// Retrying the checks must re-run them, and must not move on.
func TestRetryChecksStaysOnTheChecksScreen(t *testing.T) {
	runs := 0
	m, err := NewWizard("run", false, true, LangEN,
		func(context.Context, Config) error { runs++; return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.step = StepPreflight

	m.press(m.cat.T("btn.check"))
	if m.step != StepPreflight {
		t.Errorf("Retry moved to step %d; it must stay put", m.step)
	}
	if !m.busy {
		t.Error("Retry did not start the checks")
	}
}

// Watching a run somebody else started has nothing to collect, so the wizard
// opens where there is something to see.
func TestObserverOpensOnProgress(t *testing.T) {
	m, err := NewWizard("run", false, true, LangEN, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.step != StepInstall {
		t.Errorf("observer opened on step %d, want the progress screen", m.step)
	}
}

// Tab then Enter has to continue the install, not end it. The primary action is
// preselected on every screen, as it is in a graphical installer; leaving the
// cursor on button zero put Quit under the most obvious keystroke.
func TestPrimaryActionIsPreselected(t *testing.T) {
	work := func(context.Context, Config) error { return nil }
	m, err := NewWizard("run", false, true, LangEN, work, work)
	if err != nil {
		t.Fatal(err)
	}
	m.width, m.height = 90, 26

	for s := StepProfile; s <= StepDone; s++ {
		m.step = s
		m.enter()
		btns := m.buttons()
		if m.btn < 0 || m.btn >= len(btns) {
			t.Fatalf("step %d: focus %d is outside %d buttons", s, m.btn, len(btns))
		}
		got := btns[m.btn].Label
		if got == m.cat.T("btn.quit") || got == m.cat.T("btn.abort") {
			t.Errorf("step %d preselects %q; Tab then Enter would end the run", s, got)
		}
	}
}

// A field the operator can break has to be reported on the screen that
// produced it, not hours later.
func TestSummaryReportsAnInvalidDocument(t *testing.T) {
	m := wizard(t, LangEN, false, 90, 26, StepSummary)
	// ADR-008: the join address must not be a node's own.
	m.cfg.Registration = m.cfg.Server

	problems := m.validateConfig()
	if len(problems) == 0 {
		t.Fatal("a join address equal to the server was accepted")
	}
	var named bool
	for _, p := range problems {
		if strings.Contains(p, "ADR-008") {
			named = true
		}
	}
	if !named {
		t.Errorf("the problem does not explain itself: %v", problems)
	}
}

// What the screens collect has to become a document the real validator
// accepts. The wizard grew a screen for every group of values that came back
// missing, so this is the assertion that the set is now complete.
func TestCollectedValuesBuildAValidDocument(t *testing.T) {
	m := wizard(t, LangEN, false, 90, 26, StepSummary)

	if problems := m.validateConfig(); len(problems) > 0 {
		t.Errorf("the default configuration does not validate:\n  %s",
			strings.Join(problems, "\n  "))
	}
}

// compose sets the axes the way a baseline has them, which is what an operator
// does one screen at a time.
//
// The wizard no longer offers the baselines as a list; it composes, and the
// profile is what the composition turned out to match.
func compose(m *Wizard, b spec.Baseline) {
	m.cfg.OSFamily = string(b.OSFamily)
	m.cfg.NetworkMode = string(b.NetworkMode)
	m.cfg.Routing = string(b.Routing)
	m.cfg.Dataplane = string(b.Dataplane)
	m.cfg.Fallback = string(b.Fallback)
	m.cfg.DowngradePolicy = string(b.DowngradePolicy)
	m.cfg.PKIMode = string(b.PKIMode)
	m.cfg.Storage = string(b.Storage)
	m.cfg.RegistryMode = string(b.RegistryMode)
	m.cfg.GitOpsSource = string(b.GitOpsSource)
	m.cfg.Encrypt = b.EncryptNodeTraffic
	m.cfg.PinnedGateway = b.RequirePinnedGatewayAddress
}

// Every validated combination has to be reachable by composing, and has to be
// recognised as itself once composed.
//
// The profile is derived now: composing the homelab baseline axis by axis has
// to produce a document that says homelab, or the audit report would call a
// CI-exercised combination `custom`.
func TestEveryProfileIsReachableByComposing(t *testing.T) {
	for _, name := range spec.Profiles() {
		b, ok := spec.BaselineFor(name)
		if !ok {
			t.Fatalf("no baseline for %s", name)
		}
		t.Run(string(name), func(t *testing.T) {
			m := wizard(t, LangEN, false, 90, 26, StepSummary)
			compose(m, b)

			if got := m.cfg.MatchedProfile(); got != name {
				t.Errorf("composing the %s baseline matched %s", name, got)
			}

			// The material an operator types. It used to come from example
			// seeds -- a Harbor at acme.internal, CA references to files
			// nobody has -- which meant this test was asserting that fake
			// data validates. These are the fields each baseline genuinely
			// demands, filled the way an operator would fill them.
			if b.PKIMode != v1alpha1.PKINone {
				m.cfg.Domain = "acme.internal"
			}
			if b.PKIMode == v1alpha1.PKIPrivateCA {
				m.cfg.CARoot = "file://./pki/root.crt"
				m.cfg.CAIntermediate = "file://./pki/inter.crt"
				m.cfg.CAKey = "env://CA_KEY"
			}
			// Both ACME modes need an account address. Only the DNS one needs
			// a provider and a credential to write the record with -- HTTP-01
			// proves control by answering on port 80, which is why it is the
			// default: it asks the operator for nothing they do not already
			// have.
			if b.PKIMode == v1alpha1.PKIACMEDNS01 || b.PKIMode == v1alpha1.PKIACMEHTTP01 {
				m.cfg.ACMEEmail = "ops@acme.co.kr"
			}
			if b.PKIMode == v1alpha1.PKIACMEDNS01 {
				m.cfg.ACMEProvider = "cloudflare"
				m.cfg.ACMEToken = "env://ACME_API_TOKEN"
			}
			if b.Storage == v1alpha1.StorageNFS {
				m.cfg.NFSServer, m.cfg.NFSPath = "10.0.0.30", "/export"
			}
			if b.NetworkMode == v1alpha1.NetworkProxy {
				m.cfg.ProxyHTTP = "http://proxy.acme.local:3128"
			}
			if b.NetworkMode == v1alpha1.NetworkAirgap {
				m.cfg.RegistryBundle = "/srv/bundle.tar.zst"
			}
			if b.RegistryMode == v1alpha1.RegistryExternal || b.RegistryMode == v1alpha1.RegistryInternal {
				m.cfg.RegistryHost = "harbor.acme.internal"
			}

			if problems := m.validateConfig(); len(problems) > 0 {
				t.Errorf("%s cannot be completed:\n  %s",
					name, strings.Join(problems, "\n  "))
			}
		})
	}
}

// And a combination none of them contains is custom rather than a lie.
func TestAnUnlistedCombinationIsCustom(t *testing.T) {
	m := wizard(t, LangEN, false, 90, 26, StepSummary)
	b, _ := spec.BaselineFor(v1alpha1.ProfileHomelab)
	compose(m, b)

	m.cfg.NetworkMode = string(v1alpha1.NetworkProxy) // a homelab behind a proxy
	if got := m.cfg.MatchedProfile(); got != v1alpha1.ProfileCustom {
		t.Errorf("a combination no profile contains was reported as %s", got)
	}
}

// A screen must only ask for values that apply. An ACME profile has no
// intermediate key, and a homelab has no proxy.
func TestScreensAdaptToTheProfile(t *testing.T) {
	m := wizard(t, LangEN, false, 90, 26, StepPKI)

	homelab, _ := spec.BaselineFor(v1alpha1.ProfileHomelab) // pki none, online
	compose(m, homelab)
	if got := m.labels(StepPKI); len(got) != 0 {
		t.Errorf("a profile that issues nothing still asks about certificates: %v", got)
	}
	m.cfg.PKIMode = string(v1alpha1.PKIACMEDNS01)
	if got := m.labels(StepPKI); !containsAny(got, "Account email") {
		t.Errorf("an ACME choice is not asked for an account: %v", got)
	}
	if got := m.labels(StepNetwork); containsAny(got, "HTTP proxy") {
		t.Errorf("an online profile is asked about a proxy: %v", got)
	}

	dmz, _ := spec.BaselineFor(v1alpha1.ProfileOnpremDMZ) // private-ca, proxy
	compose(m, dmz)
	if got := m.labels(StepPKI); !containsAny(got, "Intermediate key ref") {
		t.Errorf("a private-CA profile is not asked for CA material: %v", got)
	}
	if got := m.labels(StepNetwork); !containsAny(got, "HTTP proxy") {
		t.Errorf("a proxy profile is not asked about the proxy: %v", got)
	}
}

// Secrets are references, and even a reference typed at a customer site is
// read over somebody's shoulder.
func TestSecretFieldsAreMaskedUntilEdited(t *testing.T) {
	m := wizard(t, LangEN, false, 90, 26, StepRegistry)
	// The seeds are gone -- an example credential in an editable field reads
	// as a real one -- so the masking is exercised with a value the operator
	// would have typed.
	m.cfg.RegistryMode = string(v1alpha1.RegistryExternal)
	m.cfg.RegistryPass = "env://REGISTRY_PASSWORD"
	m.cfg.RegistryMode = string(v1alpha1.RegistryExternal) // the mode with credentials

	secret := m.masked(StepRegistry)
	idx := -1
	for i, s := range secret {
		if s {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("no field on the registry screen is marked secret")
	}

	if got := m.maskedValues(StepRegistry)[idx]; !strings.HasPrefix(got, "*") {
		t.Errorf("secret field shows %q unmasked", got)
	}
	m.editing, m.cursor[StepRegistry] = true, idx
	if got := m.maskedValues(StepRegistry)[idx]; strings.HasPrefix(got, "*") {
		t.Error("the field stays masked while being edited, so it cannot be corrected")
	}
}

func containsAny(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// An invalid document must not be installable. Validating and then installing
// anyway would make the check decoration -- which is what it was until this
// test existed.
func TestInvalidDocumentCannotBeInstalled(t *testing.T) {
	var started bool
	m, err := NewWizard("run", false, true, LangEN, nil,
		func(context.Context, Config) error { started = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	m.width, m.height, m.step = 90, 26, StepSummary
	// A registry that points somewhere needs an address; clearing it is the
	// smallest way to make the document invalid.
	m.cfg.RegistryMode = string(v1alpha1.RegistryExternal)
	m.cfg.RegistryHost = ""
	m.enter()

	for _, b := range m.buttons() {
		if b.Label == m.cat.T("btn.install") {
			t.Error("an invalid document still offers Install")
		}
	}

	// Even reached directly, the transition has to refuse.
	m.next()
	if m.step == StepInstall || started {
		t.Error("the install ran against a document the validator rejected")
	}
}

// A message that only says what is wrong leaves the operator pressing Back
// until they find the screen. Every problem carries the step that fixes it,
// and the primary button goes there.
func TestProblemsAreRoutedToTheScreenThatFixesThem(t *testing.T) {
	m := wizard(t, LangEN, false, 90, 26, StepSummary)
	m.cfg.RegistryMode = string(v1alpha1.RegistryExternal)
	m.cfg.RegistryHost = ""
	m.cfg.Storage = string(v1alpha1.StorageNFS)

	ps := m.problems()
	if len(ps) < 2 {
		t.Fatalf("expected several problems, got %d", len(ps))
	}

	want := map[Step]string{StepRegistry: "systemDefaultRegistry", StepOptions: "storage.nfs"}
	for step, needle := range want {
		var found bool
		for _, p := range ps {
			if p.Step == step && strings.Contains(p.Text, needle) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is not routed to step %d: %+v", needle, step, ps)
		}
	}

	// Ordered by screen so the operator walks forwards, not back and forth.
	for i := 1; i < len(ps); i++ {
		if ps[i].Step < ps[i-1].Step {
			t.Errorf("problems are out of screen order: %+v", ps)
		}
	}

	// The primary button takes them there.
	m.focus = focusButtons
	m.btn = primaryIndex(m.buttons())
	m.press(m.buttons()[m.btn].Label)
	if m.step != ps[0].Step {
		t.Errorf("the fix button went to step %d, want %d", m.step, ps[0].Step)
	}
}

// Every field the validator can complain about has to belong to a screen, or
// its message is a dead end.
func TestEveryValidationMessageHasAnOwningScreen(t *testing.T) {
	messages := []string{
		"topology.registrationAddress is required",
		"kubernetes.version is required",
		"kubernetes.dataplane.loadBalancerPool is required with preset cilium-gw",
		"kubernetes.dataplane.preset is required",
		"network.proxy is required when network.mode is proxy",
		"pki.acme.email is required with mode acme-dns01",
		"pki.privateCA is required with mode private-ca",
		"pki.domain is required",
		"registry.systemDefaultRegistry is required with mode external",
		"storage.nfs.server and .path are required with driver nfs",
		"gateway.domainSuffix is required",
	}
	for _, msg := range messages {
		if got := ownerOf(msg); got == StepSummary {
			t.Errorf("no screen owns %q; the operator is told what is wrong and not where", msg)
		}
	}
}

// Issuing certificates is a choice, not an assumption. At a first build the
// service domain is often not decided, and a placeholder becomes a certificate
// for a name nobody serves.
func TestCertificateIssuanceIsOptional(t *testing.T) {
	m := wizard(t, LangEN, false, 90, 26, StepPKI)
	m.cfg.PKIMode = string(v1alpha1.PKINone)
	m.cfg.Domain = ""

	if got := m.fieldsFor(StepPKI); len(got) != 0 {
		t.Errorf("mode none still asks for certificate material: %d fields", len(got))
	}
	if problems := m.validateConfig(); len(problems) > 0 {
		t.Errorf("issuing nothing should be a valid configuration:\n  %s",
			strings.Join(problems, "\n  "))
	}

	// And the document says so, rather than carrying a domain nobody serves.
	if got := m.cfg.ToSpec().PKI.Domain; got != "" {
		t.Errorf("pki.domain = %q with mode none", got)
	}
}

// Choosing to issue turns the account questions back on.
func TestChoosingToIssueAsksForTheAccount(t *testing.T) {
	m := wizard(t, LangEN, false, 90, 26, StepPKI)

	for _, tc := range []struct{ mode, want string }{
		{string(v1alpha1.PKIACMEDNS01), "Account email"},
		{string(v1alpha1.PKIPrivateCA), "Intermediate key ref"},
	} {
		m.cfg.PKIMode = tc.mode
		if got := m.labels(StepPKI); !containsAny(got, tc.want) {
			t.Errorf("mode %s does not ask for %q: %v", tc.mode, tc.want, got)
		}
	}
}

// The embedded mirror is the default: nothing to stand up, nothing to keep
// alive for the life of the cluster, no credentials to manage.
func TestEmbeddedRegistryIsTheDefaultForOnlineProfiles(t *testing.T) {
	for _, name := range []v1alpha1.ProfileName{
		v1alpha1.ProfileHomelab, v1alpha1.ProfileCompanyProd,
	} {
		b, ok := spec.BaselineFor(name)
		if !ok {
			t.Fatalf("no baseline for %s", name)
		}
		if b.RegistryMode != v1alpha1.RegistryEmbedded {
			t.Errorf("%s defaults to registry %s, want embedded", name, b.RegistryMode)
		}
	}
}

// A registry mode that points nowhere has nothing to ask about.
func TestRegistryDetailsOnlyWhereThereIsARegistry(t *testing.T) {
	m := wizard(t, LangEN, false, 90, 26, StepRegistry)

	for _, mode := range []v1alpha1.RegistryMode{
		v1alpha1.RegistryEmbedded, v1alpha1.RegistryUpstream, v1alpha1.RegistryInternal,
	} {
		m.cfg.RegistryMode = string(mode)
		if got := m.fieldsFor(StepRegistry); len(got) != 0 {
			t.Errorf("mode %s asks for %d details it cannot use", mode, len(got))
		}
	}
	m.cfg.RegistryMode = string(v1alpha1.RegistryExternal)
	if got := m.fieldsFor(StepRegistry); len(got) == 0 {
		t.Error("mode external does not ask where the registry is")
	}
}

// Glyphs belong to the character set, not the catalogue. A translator has no
// way to know whether the terminal can draw one, and a catalogue string with a
// bullet in it defeats the ASCII fallback wholesale.
func TestCataloguesContainNoGlyphs(t *testing.T) {
	// English only. The Korean catalogue is never shown on a terminal that
	// cannot draw these, because the ASCII fallback forces English -- a console
	// that mangles a box character mangles Hangul too.
	for _, lang := range []Lang{LangEN} {
		cat, err := LoadCatalogue(lang)
		if err != nil {
			t.Fatal(err)
		}
		for key, val := range cat.table {
			for _, bad := range []string{"·", "✓", "▸", "✗", "─", "│", "█", "░", "–"} {
				if strings.Contains(val, bad) {
					t.Errorf("%s: %q contains the glyph %q; use Glyphs at render time",
						lang, key, bad)
				}
			}
		}
	}
}

// Two settings, and changing one must not change the other.
//
// The character set used to take the language with it, on the reasoning that a
// terminal which cannot draw a box character cannot draw Hangul either. That is
// sometimes true and it was never this switch's business: on the settings
// screen, choosing ASCII moved the row above the cursor and the operator was
// left looking at English they had not asked for. Somebody who cannot read the
// result can change it on the screen that now exists for the purpose.
func TestTheCharacterSetLeavesTheLanguageAlone(t *testing.T) {
	m, err := NewWizard("run", true, true, LangKO, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.cat.Lang() != LangKO {
		t.Errorf("starting in ASCII changed the catalogue to %s", m.cat.Lang())
	}

	n, err := NewWizard("run", false, true, LangKO, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	n.key(fakeKey("a"))
	if !n.ascii {
		t.Fatal("the a key did not change the character set")
	}
	if n.cat.Lang() != LangKO {
		t.Errorf("toggling ASCII changed the catalogue to %s", n.cat.Lang())
	}

	// And the same through the settings screen, which is where an operator
	// meets these as two named rows.
	n.step = StepPrefs
	for i, row := range prefsRows {
		if row.labelKey != "prefs.charset" {
			continue
		}
		n.cursor[StepPrefs] = i
		before := n.cat.Lang()
		n.selectUnderCursor()
		if n.cat.Lang() != before {
			t.Errorf("the character set row changed the language to %s", n.cat.Lang())
		}
		if n.ascii {
			t.Error("the character set row did not change the character set")
		}
		return
	}
	t.Fatal("there is no character set row")
}

// A final screen that says only "finished" leaves the operator to work out
// what was built, where it went and what to run next. All three have to be on
// it.
func TestFinishedScreenCarriesTheInstallation(t *testing.T) {
	m := wizard(t, LangEN, false, 100, 34, StepDone)
	m.runDir = "/srv/out/runs/01KZ3Z5GAPJJKBA4AR49G8DE4E"
	m.fold(event.Event{Kind: event.KindRun, Status: event.StatusRunning, TS: ts(0)})
	m.fold(event.Event{Kind: event.KindRun, Status: event.StatusOK, TS: ts(45)})

	screen := plain(render(t, m, nil))

	for _, want := range []string{
		m.cfg.Profile,                  // what was built
		m.cfg.Dataplane, m.cfg.Storage, //
		m.cfg.Server,             // and where
		"/srv/out/runs/01KZ3Z5G", // the run directory, not just the id
		"cluster.yaml", "events.jsonl", "state.json",
		"malmok attach --run", // what to run next
		"45s",                 // how long it took
	} {
		if !strings.Contains(screen, want) {
			t.Errorf("the finished screen does not mention %q:\n%s", want, screen)
		}
	}
}

// A failed run has to say what failed and hand back the exact command that
// continues it. "Installation stopped" on its own is not actionable.
func TestFailedScreenNamesTheCauseAndTheWayForward(t *testing.T) {
	m := wizard(t, LangEN, false, 100, 34, StepDone)
	m.runDir = "/srv/out/runs/01KZ"
	m.fold(event.Event{
		Kind: event.KindStep, Phase: "l1-bootstrap", Step: "rke2-server-ready",
		Node: "10.10.0.11", Status: event.StatusFailed, Code: "PF-601", TS: ts(30),
		Detail: "port 9345 unreachable from 10.10.20.21"})

	screen := plain(render(t, m, nil))

	// The node is named on the failure line itself, not only in the summary
	// above it: that line is what an operator reads to know which machine to
	// go to.
	for _, want := range []string{
		"PF-601", "l1-bootstrap", "10.10.0.11", "port 9345 unreachable",
		"--resume " + m.runID,
	} {
		if !strings.Contains(screen, want) {
			t.Errorf("the failed screen does not mention %q:\n%s", want, screen)
		}
	}
}

// Deferring certificates has to be legible on the final screen: an operator
// reading it months later needs to know why there is no TLS.
func TestFinishedScreenSaysWhenNothingWasIssued(t *testing.T) {
	m := wizard(t, LangEN, false, 100, 34, StepDone)
	m.cfg.PKIMode = string(v1alpha1.PKINone)

	if got := plain(render(t, m, nil)); !strings.Contains(got, "add certificates later") {
		t.Errorf("the finished screen does not explain the missing certificates:\n%s", got)
	}
}

// Space chooses, Enter moves on. Making Enter select meant Tabbing to the
// buttons on every screen, which is eleven extra keystrokes on a first build.
func TestSpaceSelectsAndEnterAdvances(t *testing.T) {
	m := wizard(t, LangEN, false, 90, 26, StepOptions)

	// Space on a choice selects it and stays put.
	m.cursor[StepOptions] = len(dataplanes) - 1
	m.key(fakeKey("space"))
	if m.step != StepOptions {
		t.Errorf("Space moved to step %d; it must select in place", m.step)
	}
	if m.cfg.Dataplane != dataplanes[len(dataplanes)-1].id {
		t.Errorf("Space did not select: dataplane is %s", m.cfg.Dataplane)
	}

	// Enter on a choice moves on without touching the selection.
	before := m.cfg.Dataplane
	m.key(fakeKey("enter"))
	if m.step == StepOptions {
		t.Error("Enter did not advance from a choice screen")
	}
	if m.cfg.Dataplane != before {
		t.Errorf("Enter changed the selection from %s to %s", before, m.cfg.Dataplane)
	}
}

// A field still needs Enter to open it: there is nothing else the key could
// mean while the cursor is on one.
func TestEnterOpensAFieldRatherThanAdvancing(t *testing.T) {
	m := wizard(t, LangEN, false, 90, 26, StepNodes)
	m.setLocal(false)
	m.cursor[StepNodes] = m.firstFieldIndex(StepNodes)

	m.key(fakeKey("enter"))
	if !m.editing {
		t.Error("Enter on a field did not start editing")
	}
	if m.step != StepNodes {
		t.Errorf("Enter on a field advanced to step %d", m.step)
	}
}

// On a screen that mixes choices and fields, the key means whichever the
// cursor is on.
func TestMixedScreenKeysFollowTheCursor(t *testing.T) {
	m := wizard(t, LangEN, false, 90, 26, StepOptions)
	m.cfg.Storage = string(v1alpha1.StorageNFS) // reveals the NFS fields

	// On a radio row.
	m.cursor[StepOptions] = 0
	if m.cursorIsField() {
		t.Error("a dataplane row is reported as a field")
	}

	// On a field row below every choice group. The groups above the fields are
	// the dataplane, the storage, the downgrade policy and its fallback.
	m.cursor[StepOptions] = len(dataplanes) + len(storages) + len(downgradePolicies) + len(fallbacks)
	if !m.cursorIsField() {
		t.Error("the NFS server row is not reported as a field")
	}
	m.key(fakeKey("enter"))
	if !m.editing {
		t.Error("Enter on the NFS row did not start editing")
	}
}

// Enter walks a form: commit, next field, and off the last one onto the
// buttons. Alternating Enter and Tab on every field is eleven screens of
// friction on a first build.
func TestEnterWalksTheFormOntoTheButtons(t *testing.T) {
	m := wizard(t, LangEN, false, 90, 26, StepNodes)
	m.setLocal(false)
	m.cursor[StepNodes] = m.firstFieldIndex(StepNodes)
	n := len(m.fieldsFor(StepNodes))
	if n < 2 {
		t.Fatalf("expected several fields, got %d", n)
	}

	m.key(fakeKey("enter")) // open the first field
	if !m.editing {
		t.Fatal("Enter did not open the first field")
	}

	for i := 0; i < n-1; i++ {
		m.key(fakeKey("enter"))
		// The fields sit below the operating system rows, so the cursor is
		// offset by them.
		if want := m.firstFieldIndex(StepNodes) + i + 1; m.cursor[StepNodes] != want {
			t.Fatalf("Enter moved the cursor to %d, want %d", m.cursor[StepNodes], want)
		}
		if !m.editing {
			t.Fatalf("field %d did not open", m.cursor[StepNodes])
		}
	}

	// Off the last one.
	m.key(fakeKey("enter"))
	if m.editing {
		t.Error("the form stayed in edit mode past the last field")
	}
	if m.focus != focusButtons {
		t.Error("Enter on the last field did not reach the buttons")
	}
	if btns := m.buttons(); m.btn < 0 || m.btn >= len(btns) || !btns[m.btn].Primary {
		t.Error("the primary action is not preselected after leaving the form")
	}
}

// The smooth bar moves between whole cells, so a ten-cell bar has eighty steps
// instead of ten. On a long phase that is the difference between a bar that
// looks stuck and one that is visibly working.
func TestSmoothBarMovesBetweenCells(t *testing.T) {
	g := GlyphsFor(false)
	seen := map[string]bool{}
	for done := 0; done <= 80; done++ {
		bar := g.SmoothBar(done, 80, 10)
		if got := cells(bar); got != 10 {
			t.Fatalf("SmoothBar(%d/80) is %d cells wide, want 10: %q", done, got, bar)
		}
		seen[bar] = true
	}
	if len(seen) < 40 {
		t.Errorf("only %d distinct bars over 80 steps; the partial cells are not being used", len(seen))
	}

	// ASCII has no partial cells and must still be exactly the right width.
	a := GlyphsFor(true)
	for done := 0; done <= 80; done++ {
		if got := cells(a.SmoothBar(done, 80, 10)); got != 10 {
			t.Fatalf("ASCII SmoothBar(%d/80) is %d cells wide", done, got)
		}
	}
}

// A step that takes minutes is indistinguishable from one that has hung unless
// something keeps moving.
func TestSpinnerAdvancesAndFallsBack(t *testing.T) {
	g := GlyphsFor(false)
	first := g.Spin(0)
	var moved bool
	for i := 1; i < len(g.Spinner); i++ {
		if g.Spin(i) != first {
			moved = true
		}
	}
	if !moved {
		t.Error("the spinner does not turn")
	}
	if got := GlyphsFor(true).Spin(0); got == "" || cells(got) != 1 {
		t.Errorf("the ASCII spinner is %q; it has to be one cell", got)
	}
}

// The footer says what the keys do here, not what they do somewhere else.
// "space to choose" on a screen with nothing to choose is noise that teaches
// the operator to stop reading the line.
func TestKeyHintsFollowTheScreen(t *testing.T) {
	choose := wizard(t, LangEN, false, 96, 24, StepOptions)
	if got := choose.keyHints(""); !strings.Contains(got, "choose") {
		t.Errorf("a choice screen does not offer Space: %q", got)
	}

	form := wizard(t, LangEN, false, 96, 24, StepNodes)
	form.setLocal(false)
	// On a field row: the operating system rows sit above the fields.
	form.cursor[StepNodes] = form.firstFieldIndex(StepNodes)
	if got := form.keyHints(""); !strings.Contains(got, "edit") {
		t.Errorf("a form screen does not offer edit: %q", got)
	}

	run := wizard(t, LangEN, false, 96, 24, StepInstall)
	got := run.keyHints("")
	if strings.Contains(got, "choose") {
		t.Errorf("the progress screen offers Space with nothing to choose: %q", got)
	}
}

// Keycaps are glyphs like any other: a terminal that cannot draw a box
// character cannot draw an arrow either.
func TestKeycapsFallBackToASCII(t *testing.T) {
	m := wizard(t, LangEN, true, 96, 24, StepProfile)
	got := m.keyHints("")
	for _, bad := range []string{"↑", "↓", "←", "→", "↵"} {
		if strings.Contains(got, bad) {
			t.Errorf("ASCII hints still contain %q: %q", bad, got)
		}
	}
	if !strings.Contains(got, "enter") {
		t.Errorf("the ASCII hints do not name Enter: %q", got)
	}
}

// A box has to be a box. Every line of the topology panel is the same width,
// or the diagram reads as broken and the operator stops trusting what it says.
func TestTopologyPanelIsRectangular(t *testing.T) {
	for _, lang := range []Lang{LangEN, LangKO} {
		for _, ascii := range []bool{false, true} {
			for _, width := range []int{60, 76, 96} {
				m := wizard(t, lang, ascii, width+20, 40, StepNodes)
				panel := plain(m.renderTopology(width))
				if panel == "" {
					t.Fatal("the panel is empty")
				}

				var boxWidth int
				for _, line := range strings.Split(panel, "\n") {
					l := strings.TrimRight(line, " ")
					if l == "" || !strings.ContainsAny(l, "│|+╭╰") {
						continue
					}
					if boxWidth == 0 {
						boxWidth = cells(strings.TrimLeft(l, " "))
						continue
					}
					if got := cells(strings.TrimLeft(l, " ")); got != boxWidth {
						t.Errorf("lang=%s ascii=%v width=%d: box line is %d cells, others are %d\n%s",
							lang, ascii, width, got, boxWidth, panel)
						break
					}
				}
			}
		}
	}
}

// The diagram groups by segment because that is a fact about the customer's
// network. Asking them to restate it is asking them to get it wrong.
func TestTopologyGroupsNodesBySegment(t *testing.T) {
	m := wizard(t, LangEN, false, 96, 40, StepNodes)
	m.cfg.Server = "10.10.0.11"
	m.cfg.Agents = []string{"10.10.20.21", "10.10.0.22"}
	m.cfg.LBPool = []string{"10.10.20.240/29"}

	segs := m.topology()
	if len(segs) != 2 {
		t.Fatalf("got %d segments, want 2", len(segs))
	}
	if got := segs[0].prefix.String(); got != "10.10.0.0/24" {
		t.Errorf("first segment is %s", got)
	}
	if len(segs[0].rows) != 2 {
		t.Errorf("the first segment holds %d rows, want the server and one agent", len(segs[0].rows))
	}
	// The pool sits on the segment it addresses, which is how an operator sees
	// it is not on the wrong one.
	var pooled bool
	for _, r := range segs[1].rows {
		if strings.Contains(r.addr, "/29") {
			pooled = true
		}
	}
	if !pooled {
		t.Errorf("the load balancer range is not on its own segment: %+v", segs[1].rows)
	}
}

// A hostname is not an address, and pretending otherwise would put it on a
// segment it does not belong to.
func TestTopologySeparatesNonAddresses(t *testing.T) {
	m := wizard(t, LangEN, false, 96, 40, StepNodes)
	m.cfg.Server = "node1.acme.internal"
	m.cfg.Agents = []string{"10.10.0.22"}
	m.cfg.LBPool = nil

	segs := m.topology()
	if len(segs) != 2 {
		t.Fatalf("got %d segments, want an addressed one and an unresolved one", len(segs))
	}
	last := segs[len(segs)-1]
	if last.label == "" {
		t.Error("the unresolved group has no label")
	}
	if last.rows[0].addr != "node1.acme.internal" {
		t.Errorf("the hostname landed as %q", last.rows[0].addr)
	}
}

// The tool used to open on the first question of a new build. A start menu is
// what makes upgrade, settings and the run log reachable at all, so the first
// screen must not be an install step.
func TestOpensOnTheMenu(t *testing.T) {
	// Work functions are what separates a wizard from the observer `attach`
	// puts up, and only the wizard has a menu to open on.
	noop := func(context.Context, Config) error { return nil }
	w, err := NewWizard("01JBQ8F2K3M5N7P9R1S3T5V7W9", false, false, LangEN, noop, noop)
	if err != nil {
		t.Fatal(err)
	}
	if w.step != StepMenu {
		t.Errorf("the wizard opens on step %v, want the menu", w.step)
	}
}

// The rail and the step counter belong to the install flow. Numbering the menu
// would make arriving at the tool look like step one of a build.
func TestMenuHasNoRailOrCounter(t *testing.T) {
	for _, step := range []Step{StepMenu, StepRuns} {
		m := wizard(t, LangEN, false, 96, 30, step)
		if len(m.rail()) != 0 {
			t.Errorf("%v shows a rail", step)
		}
		if got := plain(m.View().Content); strings.Contains(got, "/11") {
			t.Errorf("%v shows a step counter", step)
		}
		// The rail toggle would do nothing here, and a footer that advertises
		// keys that do nothing stops being read.
		if got := plain(m.View().Content); strings.Contains(got, "steps") {
			t.Errorf("%v offers the rail toggle:\n%s", step, got)
		}
	}

	install := wizard(t, LangEN, false, 96, 30, StepNodes)
	if len(install.rail()) == 0 {
		t.Error("the install flow lost its rail")
	}
	// Nodes is the second screen: the counter says which of how many, and the
	// count comes from the flow rather than from a number written here, so a
	// screen added to the flow does not make this a lie.
	want := fmt.Sprintf("2/%d", len(installSteps))
	if got := plain(install.View().Content); !strings.Contains(got, want) {
		t.Errorf("the install flow lost its counter:\n%s", got)
	}
}

// An entry with nothing behind it says so on screen and goes nowhere. Hiding it
// would make the tool look finished; letting it open an empty screen would make
// an operator hunt for something that does not exist.
func TestUnimplementedMenuEntriesSaySo(t *testing.T) {
	for i, item := range menuItems {
		if item.Missing == "" {
			continue
		}
		m := wizard(t, LangEN, false, 96, 30, StepMenu)
		m.menu = i

		got := plain(m.View().Content)
		if !strings.Contains(got, "not built yet") {
			t.Errorf("%s is not marked as unavailable:\n%s", item.TitleKey, got)
		}
		// The help text has to say what is missing, not just that something is.
		if !strings.Contains(got, "Not implemented") {
			t.Errorf("%s does not say what is missing:\n%s", item.TitleKey, got)
		}

		before := m.step
		m.next()
		if m.step != before {
			t.Errorf("%s moved to %v", item.TitleKey, m.step)
		}
	}
}

// Install is what somebody opening the tool for the first time wants, so it is
// what the cursor starts on.
func TestInstallIsTheDefaultEntry(t *testing.T) {
	m := wizard(t, LangEN, false, 96, 30, StepMenu)
	if menuItems[m.menu].TitleKey != "menu.install" {
		t.Errorf("the menu opens on %s", menuItems[m.menu].TitleKey)
	}
	m.next()
	if m.step != StepWhere {
		t.Errorf("choosing Install went to %v", m.step)
	}
}

// An operator who chose the wrong entry has to be able to leave without
// quitting, and leaving means the menu rather than the previous question of a
// flow they are abandoning.
func TestBackFromTheFlowReturnsToTheMenu(t *testing.T) {
	m := wizard(t, LangEN, false, 96, 30, StepProfile)
	m.back()
	if m.step != StepMenu {
		t.Errorf("back from the first install step went to %v", m.step)
	}

	runs := wizard(t, LangEN, false, 96, 30, StepRuns)
	runs.back()
	if runs.step != StepMenu {
		t.Errorf("back from the run list went to %v", runs.step)
	}

	// The menu is the root: there is nowhere further back.
	menu := wizard(t, LangEN, false, 96, 30, StepMenu)
	menu.back()
	if menu.step != StepMenu {
		t.Errorf("back from the menu went to %v", menu.step)
	}
}

// A machine with no runs has to say so rather than show an empty list, and it
// must not offer to open nothing.
func TestEmptyRunListSaysSo(t *testing.T) {
	m := wizard(t, LangEN, false, 96, 24, StepRuns)
	got := plain(m.View().Content)
	if !strings.Contains(got, "No run has been recorded") {
		t.Errorf("an empty run list is blank:\n%s", got)
	}
	for _, b := range m.buttons() {
		if b.Primary {
			t.Error("an empty run list offers to open something")
		}
	}
}

// Every button a screen draws has to do something when it is pressed.
//
// The start menu shipped with an Open button that activate did not know about:
// it rendered, it took focus, and pressing it did nothing at all. A button that
// looks live and is not is worse than a missing one, because the operator
// concludes the tool is stuck rather than that the feature is absent.
func TestEveryButtonIsWired(t *testing.T) {
	steps := []Step{
		StepMenu, StepRuns, StepProfile, StepNodes, StepNetwork,
		StepOptions, StepRegistry, StepPKI, StepPreflight, StepSummary,
		StepInstall, StepDone, StepOpen, StepSave, StepTarget, StepUpgrade,
		StepPrefs,
	}

	// The labels activate knows. Anything a screen draws must be among them.
	known := map[string]bool{}
	for _, key := range []string{
		"btn.back", "btn.quit", "btn.abort", "btn.close", "btn.logs",
		"btn.check", "btn.fix", "btn.next", "btn.install", "btn.open",
		"btn.load", "btn.save", "btn.upgrade", "btn.done", "btn.menu",
	} {
		m := wizard(t, LangEN, false, 96, 30, StepMenu)
		known[m.cat.T(key)] = true
	}

	for _, step := range steps {
		m := wizard(t, LangEN, false, 96, 30, step)
		buttons := m.buttons()
		if b := m.exitButton(); b != nil {
			buttons = append(buttons, *b)
		}
		for _, b := range buttons {
			if !known[b.Label] {
				t.Errorf("step %v draws a button %q that activate does not handle", step, b.Label)
			}
		}
	}
}

// fieldRow is the cursor index of a named field on a step.
func fieldRow(w *Wizard, step Step, labelKey string) int {
	for i, f := range w.fieldsFor(step) {
		if f.labelKey == labelKey {
			return w.firstFieldIndex(step) + i
		}
	}
	return -1
}
