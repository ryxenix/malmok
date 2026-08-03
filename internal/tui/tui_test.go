package tui

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"platform.ryxen.dev/platformctl/api/v1alpha1"
	"platform.ryxen.dev/platformctl/internal/event"
	"platform.ryxen.dev/platformctl/internal/spec"
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
	for s := StepLang; s <= StepDone; s++ {
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
	screen := plain(render(t, wizard(t, LangEN, false, 60, 24, StepProfile), nil))

	if strings.Contains(screen, "│") {
		t.Error("the rail survived on a narrow terminal; the content pane needs the width")
	}
	if !strings.Contains(screen, "onprem-dmz") {
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
	for s := StepLang; s <= StepDone; s++ {
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
	m.editing = true
	m.cursor[StepNodes] = 0
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
	wanted = append(wanted, stepKeys...)
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
		{"forced", map[string]string{"TERM": "xterm", "LANG": "en_US.UTF-8", "PLATFORMCTL_ASCII": "1"}, true},
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

	seen := map[Step]bool{}
	for i := 0; i < 40 && m.step != StepDone; i++ {
		seen[m.step] = true
		before := m.step

		// Whatever the primary button is on this screen, press it, then deliver
		// the result of any work it started.
		btns := m.buttons()
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
	for s := StepLang; s <= StepDone; s++ {
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

	for s := StepLang; s <= StepDone; s++ {
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

// Profile choices come from internal/spec, not from a second list here. Two
// lists of the same six profiles would disagree the first time one is edited,
// and the one the engine reads has to win.
func TestProfileChoicesComeFromTheSpecPackage(t *testing.T) {
	got := profileChoices()
	if len(got) != len(spec.Profiles()) {
		t.Fatalf("wizard offers %d profiles, spec defines %d", len(got), len(spec.Profiles()))
	}
	for _, c := range got {
		if _, ok := spec.BaselineFor(v1alpha1.ProfileName(c.id)); !ok {
			t.Errorf("wizard offers %q, which spec has no baseline for", c.id)
		}
		if c.note == "" {
			t.Errorf("%s has no summary line", c.id)
		}
	}
}

// Choosing a profile has to move its baseline into the options, or the options
// screen describes an installation that is not the one about to happen.
func TestChoosingAProfileAppliesItsBaseline(t *testing.T) {
	m := wizard(t, LangEN, false, 90, 26, StepProfile)

	choices := profileChoices()
	target := -1
	for i, c := range choices {
		if c.id == string(v1alpha1.ProfileAirgapConservative) {
			target = i
		}
	}
	if target < 0 {
		t.Fatal("the conservative profile is missing")
	}

	m.cursor[StepProfile] = target
	m.commitContent()

	b, _ := spec.BaselineFor(v1alpha1.ProfileAirgapConservative)
	if m.cfg.Dataplane != string(b.Dataplane) {
		t.Errorf("dataplane = %s, want %s from the baseline", m.cfg.Dataplane, b.Dataplane)
	}
	if m.cfg.Storage != string(b.Storage) {
		t.Errorf("storage = %s, want %s from the baseline", m.cfg.Storage, b.Storage)
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

// Every profile has to produce a valid document, not just the default one. A
// profile the wizard offers but cannot complete is a dead end an operator
// discovers after answering eight screens.
func TestEveryProfileProducesAValidDocument(t *testing.T) {
	for _, c := range profileChoices() {
		t.Run(c.id, func(t *testing.T) {
			m := wizard(t, LangEN, false, 90, 26, StepSummary)
			m.cfg.Profile = c.id
			m.applyProfileDefaults()

			if problems := m.validateConfig(); len(problems) > 0 {
				t.Errorf("profile %s cannot be completed:\n  %s",
					c.id, strings.Join(problems, "\n  "))
			}
		})
	}
}

// A screen must only ask for values that apply. An ACME profile has no
// intermediate key, and a homelab has no proxy.
func TestScreensAdaptToTheProfile(t *testing.T) {
	m := wizard(t, LangEN, false, 90, 26, StepPKI)

	m.cfg.Profile = string(v1alpha1.ProfileHomelab) // acme-dns01, online
	if got := m.labels(StepPKI); !containsAny(got, "Account email") {
		t.Errorf("an ACME profile is asked for CA material: %v", got)
	}
	if got := m.labels(StepNetwork); containsAny(got, "HTTP proxy") {
		t.Errorf("an online profile is asked about a proxy: %v", got)
	}

	m.cfg.Profile = string(v1alpha1.ProfileOnpremDMZ) // private-ca, proxy
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
	m.cfg.RegistryHost = "" // a field the validator requires
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
	m.cfg.RegistryHost = ""
	m.cfg.LBPool = nil

	ps := m.problems()
	if len(ps) < 2 {
		t.Fatalf("expected several problems, got %d", len(ps))
	}

	want := map[Step]string{StepRegistry: "systemDefaultRegistry", StepNetwork: "loadBalancerPool"}
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
