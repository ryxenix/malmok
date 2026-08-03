package tui

import (
	"context"
	"sync"

	tea "charm.land/bubbletea/v2"

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
	StepLang Step = iota
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
)

var stepKeys = []string{
	"step.lang", "step.profile", "step.nodes", "step.network", "step.options",
	"step.registry", "step.pki", "step.preflight", "step.summary",
	"step.install", "step.done",
}

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
	width, height int

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

func (w *Wizard) Init() tea.Cmd { return nil }

func (w *Wizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w.width, w.height = msg.Width, msg.Height
		return w, nil

	case resetMsg:
		w.order, w.phases = nil, map[string]*phaseView{}
		w.logs, w.failures = nil, nil
		return w, nil

	case eventMsg:
		w.fold(msg.e)
		return w, nil

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
		return w, nil
	case "g":
		if cat, err := LoadCatalogue(w.cat.Other()); err == nil {
			w.cat, w.cfg.Lang = cat, cat.Lang()
		}
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
		w.btn = max(w.btn-1, 0)
	case "right", "l":
		w.btn = min(w.btn+1, len(btns)-1)
	case "enter", "space":
		return w.activate(btns[w.btn].Label)
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
		w.cursor[w.step] = max(cur-1, 0)
	case "down", "j":
		w.cursor[w.step] = min(cur+1, max(n-1, 0))
	case "enter", "space":
		return w.commitContent()
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
	case "enter", "esc":
		w.editing = false
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

// commitContent applies Enter on the content pane.
func (w *Wizard) commitContent() (tea.Model, tea.Cmd) {
	cur := w.cursor[w.step]
	switch w.step {
	case StepLang:
		lang := []Lang{LangEN, LangKO}[cur]
		if cat, err := LoadCatalogue(lang); err == nil {
			w.cat, w.cfg.Lang = cat, lang
		}
	case StepNodes, StepNetwork:
		if len(w.fieldsFor(w.step)) > 0 {
			w.editing = true
		}
	case StepRegistry:
		if cur < len(registryModes) {
			w.cfg.RegistryMode = registryModes[cur].id
		} else if len(w.fieldsFor(StepRegistry)) > 0 {
			w.editing = true
		}
	case StepPKI:
		if cur < len(pkiModes) {
			w.cfg.PKIMode = pkiModes[cur].id
		} else if len(w.fieldsFor(StepPKI)) > 0 {
			w.editing = true
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
		default:
			// The driver's own settings sit below the choice that reveals them.
			if len(w.fieldsFor(StepOptions)) > 0 {
				w.editing = true
			}
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
	case w.cat.T("btn.next"), w.cat.T("btn.install"):
		return w.next()
	}
	return w, nil
}

func (w *Wizard) back() (tea.Model, tea.Cmd) {
	if w.busy || w.step == StepLang || w.step >= StepInstall {
		return w, nil
	}
	w.step--
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
	return max(len(btns)-1, 0)
}

func (w *Wizard) next() (tea.Model, tea.Cmd) {
	switch w.step {
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
		w.step++
		w.enter()
		if w.step == StepPreflight {
			w.focus = focusButtons
			return w, w.start(w.preflight)
		}
	}
	return w, nil
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
	default:
		return 0
	}
}

// SetWorkContext gives the wizard the context its work should run under, so
// closing the screen cancels the run rather than leaving it orphaned.
func (w *Wizard) SetWorkContext(ctx context.Context) { w.workCtx = ctx }

func startStep(preflight, install Work) Step {
	if preflight == nil && install == nil {
		return StepInstall
	}
	return StepLang
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
