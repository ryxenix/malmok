package tui

import (
	"context"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"platform.ryxen.dev/platformctl/api/v1alpha1"
	"platform.ryxen.dev/platformctl/internal/event"
)

// The wizard is the whole application: a sequence of steps with a rail showing
// where the operator is, exactly as a graphical installer does.
//
// It is also an attach.Sink. Every progress screen is drawn from the event
// stream rather than from engine internals, which is why the same install can
// be watched from another terminal and why closing this window does not end it
// (ADR-002).

// Step is one screen of the sequence.
type Step int

const (
	// StepMenu is where the tool opens. The installer used to be the whole
	// program, which put an operator on the first question of a new build with
	// no way to reach anything else -- wrong for a tool whose work is mostly
	// not first installs.
	StepMenu Step = iota
	// StepRuns lists what has been run on this machine, so the record of a
	// build is reachable without knowing `attach` exists.
	StepRuns
	StepLang
	// Profile comes before everything it decides: the network mode, the PKI
	// mode and the storage driver all follow from it, and a screen that asks
	// about a proxy before knowing whether there is one wastes a question.
	StepProfile
	StepNodes
	StepNetwork
	StepOptions
	StepRegistry
	StepPKI
	StepPreflight
	StepSummary
	StepInstall
	StepDone

	// StepOpen and StepSave are the settings flow's own ends: which document to
	// edit, and writing it back. Appended rather than inserted so the numbers of
	// the install steps do not move -- validation problems are ordered by Step,
	// and the order of a build is what that ordering means.
	StepOpen
	StepSave
)

// mode is which of the two flows the wizard is walking.
//
// The screens in the middle are the same ones. What differs is where they are
// entered from and what happens at the end: a build measures nodes and installs;
// an edit reads a document and writes it back.
type mode int

const (
	modeInstall mode = iota
	modeSettings
)

// The two flows, in the order their screens are walked.
//
// Order lives in a list rather than in the numbering, because the numbering has
// to mean something else: problems are sorted by Step so the operator walks
// forwards through a build. Two flows cannot both be expressed by one set of
// consecutive numbers, and arithmetic over step values is how a screen inserted
// in the middle silently renumbers the rail.
//
// The menu and the run list are in neither: they are where an operator arrives
// from and returns to, not stages of anything, and numbering them would make a
// two-screen detour look like part of the work.
var (
	installSteps = []Step{
		StepLang, StepProfile, StepNodes, StepNetwork, StepOptions,
		StepRegistry, StepPKI, StepPreflight, StepSummary, StepInstall, StepDone,
	}
	settingsSteps = []Step{
		StepOpen, StepProfile, StepNodes, StepNetwork, StepOptions,
		StepRegistry, StepPKI, StepSave,
	}
)

// stepKeys is the catalogue key for each screen that appears in a rail.
var stepKeys = map[Step]string{
	StepLang: "step.lang", StepProfile: "step.profile", StepNodes: "step.nodes",
	StepNetwork: "step.network", StepOptions: "step.options",
	StepRegistry: "step.registry", StepPKI: "step.pki",
	StepPreflight: "step.preflight", StepSummary: "step.summary",
	StepInstall: "step.install", StepDone: "step.done",
	StepOpen: "step.open", StepSave: "step.save",
}

// flow is the sequence the current mode walks.
func (w *Wizard) flow() []Step {
	if w.mode == modeSettings {
		return settingsSteps
	}
	return installSteps
}

// railIndex is the position of the current step within its flow, or -1 when the
// current screen is not part of one.
func (w *Wizard) railIndex() int {
	for i, st := range w.flow() {
		if st == w.step {
			return i
		}
	}
	return -1
}

// inInstallFlow reports whether the rail and the step counter apply.
func (w *Wizard) inInstallFlow() bool { return w.railIndex() >= 0 }

// focus is which half of the screen the keyboard is driving.
type focus int

const (
	focusContent focus = iota
	focusButtons
)

// Config is what the wizard collects. The engine consumes what it can today;
// fields it cannot are marked on screen rather than silently ignored.
type Config struct {
	Lang    Lang
	Server  string
	Agents  []string
	SSHUser string
	SSHPort string
	// SSHPassword is deliberately absent from ToSpec. cluster.yaml is an audit
	// artifact that gets handed to customers, and a plaintext credential in it
	// is a liability the schema exists to prevent -- so this is passed to the
	// preflight session directly and never serialised.
	SSHPassword string

	// Registration is what every node joins through. A VIP or DNS name, never
	// a node's own address (ADR-008).
	Registration string
	Version      string
	Domain       string

	Profile   string
	Dataplane string
	Storage   string

	// Chosen on their own screens rather than inherited silently: whether to
	// issue certificates at all, and where images come from.
	PKIMode      string
	RegistryMode string

	// Addressing the customer's network team has to agree to.
	ProxyHTTP  string
	ProxyHTTPS string
	NoProxy    []string
	LBPool     []string

	// SourceRefs, not values. cluster.yaml is handed over at the end of the
	// engagement, so what is collected here is where to find a secret rather
	// than the secret itself.
	RegistryHost string
	RegistryUser string
	RegistryPass string
	RegistryCA   string

	NFSServer string
	NFSPath   string

	CARoot         string
	CAIntermediate string
	CAKey          string

	ACMEEmail    string
	ACMEProvider string
	ACMEToken    string

	// DocPath is the file the settings flow reads and writes. It is a wizard
	// value rather than a document one and is never serialised -- a document
	// that recorded its own location would be wrong the moment it was copied.
	DocPath string
}

// Work is a long operation the wizard drives, reported through the event
// stream rather than through its return value.
type Work func(context.Context, Config) error

// Wizard is the Bubble Tea model.
type Wizard struct {
	cat    *Catalogue
	theme  Theme
	glyphs Glyphs
	ascii  bool
	mono   bool

	runID         string
	runDir        string
	width, height int

	// Taken from the run events rather than a clock of its own, so the elapsed
	// time on screen is the engine's own record and matches the event file.
	startedAt, finishedAt time.Time

	step  Step
	focus focus
	btn   int

	// content cursors, one per step that needs one
	cursor  map[Step]int
	editing bool

	cfg Config

	// live run state, folded from events
	order    []string
	phases   map[string]*phaseView
	logs     []event.Event
	failures []event.Event
	runStat  event.Status

	preflight Work
	install   Work
	workCtx   context.Context
	hideRail  bool

	// menu is the selected start-menu entry; runs is what was found on this
	// machine and runSel is the highlighted one.
	menu   int
	runs   []RunEntry
	runSel int

	// mode is which flow is being walked; doc is the loaded document the
	// settings flow edits, kept whole so the fields no screen shows survive
	// being written back.
	mode      mode
	doc       v1alpha1.ClusterSpec
	openFiles []docEntry
	openErr   string
	saveErr   string
	// saved is the path the document was written to, empty until it has been.
	saved string

	// bundle is where runs are looked for; replaying marks a screen showing a
	// run that is over rather than one in progress.
	bundle    string
	replaying bool
	truecolor bool
	tick      int
	busy      bool
	workErr   error
	aborted   bool

	mu   sync.Mutex
	prog *tea.Program
}

type phaseView struct {
	id       string
	status   event.Status
	detail   string
	code     string
	stepID   string
	node     string
	progress *event.Progress
	attempt  int
	maxTries int
}

// NewWizard builds the model.
func NewWizard(runID string, ascii, mono bool, lang Lang, preflight, install Work) (*Wizard, error) {
	cat, err := LoadCatalogue(lang)
	if err != nil {
		return nil, err
	}
	wz := &Wizard{
		cat: cat, theme: NewTheme(mono), glyphs: GlyphsFor(ascii),
		ascii: ascii, mono: mono, runID: runID,
		width: 80, height: 24,
		cursor: map[Step]int{}, phases: map[string]*phaseView{},
		runStat:   event.StatusPending,
		preflight: preflight, install: install,
		// Watching a run somebody else started: there is nothing to collect,
		// so the wizard opens on the screen that has something to show.
		step: startStep(preflight, install),
		cfg: Config{
			Lang: lang, Server: "10.10.0.11",
			Agents:  []string{"10.10.20.21", "10.10.0.22"},
			SSHUser: "root", SSHPort: "22",
			Registration: "k8s-api.acme.internal",
			Version:      "v1.34.5+rke2r1",
			Domain:       "acme.internal",
			Profile:      "onprem-dmz", Dataplane: "cilium-gw", Storage: "longhorn",
			ProxyHTTP:  "http://proxy.acme.local:3128",
			ProxyHTTPS: "http://proxy.acme.local:3128",
			NoProxy:    []string{"10.0.0.0/8", ".acme.internal"},
			LBPool:     []string{"10.10.20.240/29"},

			RegistryHost: "harbor.acme.internal",
			RegistryUser: "env://REGISTRY_USER",
			RegistryPass: "env://REGISTRY_PASSWORD",

			NFSServer: "10.10.0.30", NFSPath: "/export/rke2",

			CARoot:         "file://./pki/root.crt",
			CAIntermediate: "file://./pki/intermediate.crt",
			CAKey:          "env://CA_INTERMEDIATE_KEY",

			ACMEEmail:    "ops@acme.co.kr",
			ACMEProvider: "cloudflare",
			ACMEToken:    "env://ACME_API_TOKEN",
		},
	}
	// The default profile's baseline applies from the start, so a screen never
	// shows a mode that the chosen profile would not use.
	wz.applyProfileDefaults()
	wz.enforceASCIILanguage()
	wz.enter()
	return wz, nil
}

// ---------------------------------------------------------------------------
// attach.Sink
// ---------------------------------------------------------------------------

type eventMsg struct{ e event.Event }
type resetMsg struct{}
type workDoneMsg struct{ err error }

func (w *Wizard) Reset()                     { w.post(resetMsg{}) }
func (w *Wizard) Handle(e event.Event) error { w.post(eventMsg{e}); return nil }

// Attach binds the program so the follower can post into its loop instead of
// mutating the screen from another goroutine.
func (w *Wizard) Attach(p *tea.Program) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.prog = p
}

func (w *Wizard) post(msg tea.Msg) {
	w.mu.Lock()
	p := w.prog
	w.mu.Unlock()
	if p != nil {
		p.Send(msg)
	}
}

// Aborted reports whether the operator stopped the run.
func (w *Wizard) Aborted() bool { return w.aborted }

// ---------------------------------------------------------------------------
// tea.Model
// ---------------------------------------------------------------------------

func (w *Wizard) Init() tea.Cmd { return spinEvery() }

// spinMsg advances the spinner. A step that takes minutes is indistinguishable
// from one that has hung unless something on screen keeps moving.
type spinMsg struct{}

func spinEvery() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return spinMsg{} })
}

func (w *Wizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w.width, w.height = msg.Width, msg.Height
		return w, nil

	case tea.ColorProfileMsg:
		// The terminal says what it can do rather than the tool guessing. A
		// gradient on a 16-colour console is a band of noise.
		w.truecolor = msg.Profile == colorprofile.TrueColor
		return w, nil

	case resetMsg:
		w.order, w.phases = nil, map[string]*phaseView{}
		w.logs, w.failures = nil, nil
		return w, nil

	case eventMsg:
		w.fold(msg.e)
		return w, nil

	case replayMsg:
		// A finished run is folded in one go. Pacing it to look live would be
		// an animation of something that already happened.
		for _, e := range msg.events {
			w.fold(e)
		}
		return w, nil

	case spinMsg:
		// Always re-armed. The view only differs while something is spinning,
		// and Bubble Tea writes nothing when the render is unchanged, so an
		// idle screen costs no output -- which matters over a serial line.
		w.tick++
		return w, spinEvery()

	case workDoneMsg:
		w.busy = false
		w.workErr = msg.err
		return w, w.advanceAfterWork()

	case tea.KeyPressMsg:
		return w.key(msg)
	}
	return w, nil
}

// ---------------------------------------------------------------------------
// Keys
// ---------------------------------------------------------------------------

func (w *Wizard) key(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s := k.String()

	// While a field is being edited the keyboard belongs to it, or typing "q"
	// into a hostname would quit the installer.
	if w.editing {
		return w.editKey(s)
	}

	switch s {
	case "ctrl+c":
		w.aborted = true
		return w, tea.Quit
	case "a":
		w.ascii = !w.ascii
		w.glyphs = GlyphsFor(w.ascii)
		w.enforceASCIILanguage()
		return w, nil
	case "g":
		if cat, err := LoadCatalogue(w.cat.Other()); err == nil {
			w.cat, w.cfg.Lang = cat, cat.Lang()
		}
		return w, nil
	case "s":
		// The step list, on or off. Some operators want to see where they are;
		// others want the width.
		w.hideRail = !w.hideRail
		return w, nil
	case "tab":
		w.focus = 1 - w.focus
		return w, nil
	case "shift+tab":
		w.focus = 1 - w.focus
		return w, nil
	}

	if w.focus == focusButtons {
		return w.buttonKey(s)
	}
	return w.contentKey(s)
}

func (w *Wizard) buttonKey(s string) (tea.Model, tea.Cmd) {
	btns := w.buttons()
	switch s {
	case "left", "h":
		// Stepping left off the first action reaches the bottom-left exit.
		if w.btn == 0 && w.exitButton() != nil {
			w.btn = exitFocus
		} else if w.btn > 0 {
			w.btn--
		}
	case "right", "l":
		if w.btn == exitFocus {
			w.btn = 0
		} else {
			w.btn = min(w.btn+1, len(btns)-1)
		}
	case "enter", "space":
		if w.btn == exitFocus {
			if ex := w.exitButton(); ex != nil {
				return w.activate(ex.Label)
			}
			return w, nil
		}
		if w.btn >= 0 && w.btn < len(btns) {
			return w.activate(btns[w.btn].Label)
		}
	case "esc":
		w.focus = focusContent
	}
	return w, nil
}

func (w *Wizard) contentKey(s string) (tea.Model, tea.Cmd) {
	n := w.contentLen()
	cur := w.cursor[w.step]

	switch s {
	case "up", "k":
		switch w.step {
		case StepMenu:
			w.menu = max(w.menu-1, 0)
		case StepRuns:
			w.runSel = max(w.runSel-1, 0)
		default:
			w.cursor[w.step] = max(cur-1, 0)
		}
	case "down", "j":
		switch w.step {
		case StepMenu:
			w.menu = min(w.menu+1, len(menuItems)-1)
		case StepRuns:
			w.runSel = min(w.runSel+1, max(len(w.runs)-1, 0))
		default:
			w.cursor[w.step] = min(cur+1, max(n-1, 0))
		}
	case "space":
		// Space selects. Enter is reserved for moving on, so the operator is
		// not made to Tab to the buttons on every screen.
		return w.selectUnderCursor()
	case "enter":
		// A field still needs Enter to open it; there is nothing else the key
		// could mean while the cursor is on one.
		if w.cursorIsField() {
			w.editing = true
			return w, nil
		}
		return w.next()
	case "esc":
		return w.back()
	}
	return w, nil
}

func (w *Wizard) editKey(s string) (tea.Model, tea.Cmd) {
	i := w.fieldIndex()
	vals := w.values(w.step)
	if i < 0 || i >= len(vals) {
		w.editing = false
		return w, nil
	}

	switch s {
	case "esc":
		// Esc closes the field and leaves the cursor on it.
		w.editing = false
	case "enter":
		// Enter commits and moves to the next field, and off the last one onto
		// the buttons. That is how every form behaves, and it means a field
		// screen can be walked with one key instead of alternating Enter and
		// Tab.
		w.editing = false
		if i+1 < len(vals) {
			w.cursor[w.step]++
			w.editing = true
		} else {
			w.focus = focusButtons
			w.btn = primaryIndex(w.buttons())
		}
	case "backspace":
		if v := vals[i]; v != "" {
			r := []rune(v)
			w.setValue(w.step, i, string(r[:len(r)-1]))
		}
	case "space":
		w.setValue(w.step, i, vals[i]+" ")
	default:
		// Single printable characters only. Anything else is a navigation key
		// that has no business inside a hostname.
		if len([]rune(s)) == 1 && s != "\t" {
			w.setValue(w.step, i, vals[i]+s)
		}
	}
	return w, nil
}

// selectUnderCursor applies Space: it chooses the option the cursor is on and
// does nothing when the cursor is on a field.
func (w *Wizard) selectUnderCursor() (tea.Model, tea.Cmd) {
	cur := w.cursor[w.step]
	switch w.step {
	case StepOpen:
		// The list fills the field rather than loading straight away. Choosing
		// a file and reading it are two decisions, and one keystroke that did
		// both would give no chance to look at the path first.
		if i := cur - len(w.fieldsFor(StepOpen)); i >= 0 && i < len(w.openFiles) {
			w.cfg.DocPath = w.openFiles[i].Path
		}
	case StepLang:
		lang := []Lang{LangEN, LangKO}[cur]
		if cat, err := LoadCatalogue(lang); err == nil {
			w.cat, w.cfg.Lang = cat, lang
		}
	case StepRegistry:
		if cur < len(registryModes) {
			w.cfg.RegistryMode = registryModes[cur].id
		}
	case StepPKI:
		if cur < len(pkiModes) {
			w.cfg.PKIMode = pkiModes[cur].id
		}
	case StepProfile:
		if choices := profileChoices(); cur < len(choices) {
			w.cfg.Profile = choices[cur].id
			// The profile decides the validated baseline; showing the previous
			// dataplane and storage next to a new profile would misdescribe
			// what is about to be installed.
			w.applyProfileDefaults()
		}
	case StepOptions:
		switch {
		case cur < len(dataplanes):
			w.cfg.Dataplane = dataplanes[cur].id
		case cur < len(dataplanes)+len(storages):
			w.cfg.Storage = storages[cur-len(dataplanes)].id
		}
	}
	return w, nil
}

// activate runs a button.
func (w *Wizard) activate(label string) (tea.Model, tea.Cmd) {
	switch label {
	case w.cat.T("btn.back"):
		return w.back()
	case w.cat.T("btn.quit"), w.cat.T("btn.abort"):
		w.aborted = true
		return w, tea.Quit
	case w.cat.T("btn.close"):
		return w, tea.Quit
	case w.cat.T("btn.logs"):
		return w, nil
	case w.cat.T("btn.check"):
		// Re-run the checks in place. Distinct from Continue, which moves on:
		// wiring both to the same action left the operator re-checking forever.
		return w, w.start(w.preflight)
	case w.cat.T("btn.fix"):
		// Jump to the screen that owns the first problem rather than making
		// the operator press Back until they find it.
		if step, ok := w.firstProblemStep(); ok {
			w.step = step
			w.enter()
		}
		return w, nil
	case w.cat.T("btn.next"), w.cat.T("btn.install"), w.cat.T("btn.open"),
		w.cat.T("btn.load"), w.cat.T("btn.save"):
		return w.next()
	}
	return w, nil
}

func (w *Wizard) back() (tea.Model, tea.Cmd) {
	// Nothing goes back out of work already done on a node, and nothing goes
	// back from a document already written.
	if w.busy || w.step == StepMenu || w.step == StepInstall || w.step == StepDone {
		return w, nil
	}
	if w.step == StepSave && w.saved != "" {
		return w, nil
	}

	i := w.railIndex()
	// The first screen of either flow, and the run list, return to the menu
	// rather than walking backwards into it: an operator who chose the wrong
	// entry wants the menu, not the previous question of a flow they are
	// leaving.
	if i <= 0 || w.step == StepRuns {
		w.step = StepMenu
		w.mode = modeInstall
		w.enter()
		return w, nil
	}
	w.step = w.flow()[i-1]
	w.enter()
	return w, nil
}

// enter resets the per-step focus. The primary action is preselected, as it is
// in a graphical installer: Tab then Enter has to continue, not quit. Leaving
// the cursor on button zero put Quit under the most obvious keystroke.
func (w *Wizard) enter() {
	w.focus, w.editing = focusContent, false
	w.btn = primaryIndex(w.buttons())
}

func primaryIndex(btns []Button) int {
	for i, b := range btns {
		if b.Primary {
			return i
		}
	}
	if len(btns) == 0 {
		// Only the bottom-left action exists, so that is what focus means.
		return exitFocus
	}
	return len(btns) - 1
}

func (w *Wizard) next() (tea.Model, tea.Cmd) {
	switch w.step {
	case StepMenu:
		item := menuItems[w.menu]
		if item.TitleKey == "menu.quit" {
			w.aborted = false
			return w, tea.Quit
		}
		// An entry with nothing behind it stays put. The screen already says
		// why, and moving to an empty page would be the tool pretending.
		if item.Missing != "" || item.Enter == StepMenu {
			return w, nil
		}
		if item.Enter == StepRuns {
			w.runs = w.loadRuns()
			w.runSel = 0
		}
		// The mode is chosen here and nowhere else. Every screen after this is
		// shared, and a screen that had to ask which flow it was in would be a
		// second place for the two to disagree.
		w.mode = modeInstall
		if item.Enter == StepOpen {
			w.mode = modeSettings
			w.openFiles = w.listDocuments()
			w.openErr, w.saveErr, w.saved = "", "", ""
		}
		w.step = item.Enter
		w.enter()
		return w, nil

	case StepRuns:
		if len(w.runs) == 0 {
			return w, nil
		}
		return w, w.openRun(w.runs[w.runSel])

	case StepOpen:
		if err := w.loadDocument(); err != nil {
			w.openErr = err.Error()
			return w, nil
		}
		w.openErr = ""
		w.advance()
		return w, nil

	case StepSave:
		// Written once. Pressing the button again on a screen that already says
		// where the file went should not rewrite it.
		if w.saved != "" {
			return w, tea.Quit
		}
		if _, broken := w.firstProblemStep(); broken {
			return w, nil
		}
		if err := w.writeDocument(); err != nil {
			w.saveErr = err.Error()
			return w, nil
		}
		w.saveErr = ""
		w.enter()
		return w, nil

	case StepPreflight:
		if w.busy {
			return w, nil
		}
		w.step = StepSummary
		w.enter()
		return w, nil
	case StepSummary:
		// The gate, not just the warning. Validating and then installing
		// anyway would make the check decoration.
		if _, broken := w.firstProblemStep(); broken {
			return w, nil
		}
		w.step = StepInstall
		w.enter()
		w.focus = focusButtons
		return w, w.start(w.install)
	case StepDone:
		return w, tea.Quit
	default:
		w.advance()
		if w.step == StepPreflight {
			w.focus = focusButtons
			return w, w.start(w.preflight)
		}
	}
	return w, nil
}

// advance moves to the next screen of the current flow.
func (w *Wizard) advance() {
	flow := w.flow()
	if i := w.railIndex(); i >= 0 && i+1 < len(flow) {
		w.step = flow[i+1]
	}
	w.enter()
}

// start runs long work off the update loop. The screen keeps redrawing from
// events while it runs.
func (w *Wizard) start(fn Work) tea.Cmd {
	if fn == nil || w.busy {
		return nil
	}
	w.busy, w.workErr = true, nil
	cfg := w.cfg
	ctx := w.workCtx
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		return workDoneMsg{err: fn(ctx, cfg)}
	}
}

// advanceAfterWork moves on once a step's work finishes.
func (w *Wizard) advanceAfterWork() tea.Cmd {
	switch w.step {
	case StepPreflight:
		w.btn, w.focus = primaryIndex(w.buttons()), focusButtons
	case StepInstall:
		w.step = StepDone
		w.btn, w.focus = primaryIndex(w.buttons()), focusButtons
	}
	return nil
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

func (w *Wizard) fold(e event.Event) {
	switch e.Kind {
	case event.KindRun:
		w.runStat = e.Status
		if w.startedAt.IsZero() {
			w.startedAt = e.TS.Time
		}
		if e.Status.Terminal() {
			w.finishedAt = e.TS.Time
		}

	case event.KindPhase:
		p := w.phase(e.Phase)
		p.status, p.code = e.Status, e.Code
		if e.Status.Terminal() {
			p.stepID, p.node, p.progress, p.attempt = "", "", nil, 0
		}

	case event.KindStep, event.KindProbe:
		if e.Phase != "" {
			p := w.phase(e.Phase)
			p.stepID, p.node = e.Step, e.Node
			p.progress, p.attempt, p.maxTries = e.Progress, e.Attempt, e.MaxAttempts
			if e.Detail != "" {
				p.detail = e.Detail
			}
		}

	case event.KindLog, event.KindDecision:
		w.logs = append(w.logs, e)
		if len(w.logs) > 200 {
			w.logs = w.logs[len(w.logs)-200:]
		}
		return
	}

	if (e.Status == event.StatusFailed || e.Status == event.StatusBlocked) &&
		(e.Kind == event.KindStep || e.Kind == event.KindProbe) {
		w.failures = append(w.failures, e)
	}
}

func (w *Wizard) phase(id string) *phaseView {
	p, ok := w.phases[id]
	if !ok {
		p = &phaseView{id: id, status: event.StatusPending}
		w.phases[id] = p
		w.order = append(w.order, id)
	}
	return p
}

// ---------------------------------------------------------------------------
// Node field helpers
// ---------------------------------------------------------------------------

// contentLen is how many rows the current step's content pane has.
func (w *Wizard) contentLen() int {
	switch w.step {
	case StepLang:
		return 2
	case StepNodes, StepNetwork:
		return len(w.fieldsFor(w.step))
	case StepRegistry:
		return len(registryModes) + len(w.fieldsFor(StepRegistry))
	case StepPKI:
		return len(pkiModes) + len(w.fieldsFor(StepPKI))
	case StepProfile:
		return len(profileChoices())
	case StepOptions:
		return len(dataplanes) + len(storages) + len(w.fieldsFor(StepOptions))
	case StepOpen:
		return len(w.fieldsFor(StepOpen)) + len(w.openFiles)
	default:
		return 0
	}
}

// SetWorkContext gives the wizard the context its work should run under, so
// closing the screen cancels the run rather than leaving it orphaned.
func (w *Wizard) SetWorkContext(ctx context.Context) { w.workCtx = ctx }

// startStep decides which screen the tool opens on.
//
// With no work to drive, this is `attach`: there is nothing to collect and the
// only useful screen is the one showing somebody else's run. Otherwise it is
// the menu, because a tool whose work is mostly not first installs should not
// open on the first question of one.
func startStep(preflight, install Work) Step {
	if preflight == nil && install == nil {
		return StepInstall
	}
	return StepMenu
}

// fieldIndex maps the content cursor onto the step's field list. On the options
// screen the fields sit below two radio groups, so the cursor has to be shifted
// past them.
func (w *Wizard) fieldIndex() int {
	i := w.cursor[w.step]
	switch w.step {
	case StepOptions:
		i -= len(dataplanes) + len(storages)
	case StepRegistry:
		i -= len(registryModes)
	case StepPKI:
		i -= len(pkiModes)
	}
	return i
}

// enforceASCIILanguage falls back to English whenever the character set does.
//
// The ASCII glyphs exist for a terminal that cannot draw box characters -- a
// serial console, an IPMI viewer, PuTTY with the wrong codepage. None of those
// can draw Hangul either, so a Korean screen there is unreadable in a way the
// glyph fallback cannot fix. Tying the two keeps the fallback honest instead of
// half-working.
func (w *Wizard) enforceASCIILanguage() {
	if !w.ascii || w.cat.Lang() == LangEN {
		return
	}
	if cat, err := LoadCatalogue(LangEN); err == nil {
		w.cat, w.cfg.Lang = cat, LangEN
	}
}

// cursorIsField reports whether the content cursor is on an editable line
// rather than on a choice.
func (w *Wizard) cursorIsField() bool {
	i := w.fieldIndex()
	return i >= 0 && i < len(w.fieldsFor(w.step))
}
