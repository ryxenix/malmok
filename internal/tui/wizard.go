package tui

import (
	"context"
	"strings"
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
	StepNodes
	StepProfile
	StepOptions
	StepPreflight
	StepSummary
	StepInstall
	StepDone
)

var stepKeys = []string{
	"step.lang", "step.nodes", "step.profile", "step.options",
	"step.preflight", "step.summary", "step.install", "step.done",
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
		},
	}
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
	i := w.cursor[StepNodes]
	vals := w.nodeValues()

	switch s {
	case "enter", "esc":
		w.editing = false
	case "backspace":
		if v := vals[i]; v != "" {
			r := []rune(v)
			w.setNodeValue(i, string(r[:len(r)-1]))
		}
	default:
		// Single printable characters only. Anything else is a navigation key
		// that has no business inside a hostname.
		if len([]rune(s)) == 1 && s != "\t" {
			w.setNodeValue(i, vals[i]+s)
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
	case StepNodes:
		w.editing = true
	case StepProfile:
		if choices := profileChoices(); cur < len(choices) {
			w.cfg.Profile = choices[cur].id
			// The profile decides the validated baseline; showing the previous
			// dataplane and storage next to a new profile would misdescribe
			// what is about to be installed.
			w.applyProfileDefaults()
		}
	case StepOptions:
		if cur < len(dataplanes) {
			w.cfg.Dataplane = dataplanes[cur].id
		} else {
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

func (w *Wizard) nodeValues() []string {
	return []string{
		w.cfg.Server, strings.Join(w.cfg.Agents, ", "),
		w.cfg.SSHUser, w.cfg.SSHPort,
		w.cfg.Registration, w.cfg.Version, w.cfg.Domain,
	}
}

func (w *Wizard) setNodeValue(i int, v string) {
	switch i {
	case 0:
		w.cfg.Server = v
	case 1:
		var out []string
		for _, part := range strings.Split(v, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
		w.cfg.Agents = out
	case 2:
		w.cfg.SSHUser = v
	case 3:
		w.cfg.SSHPort = v
	case 4:
		w.cfg.Registration = v
	case 5:
		w.cfg.Version = v
	case 6:
		w.cfg.Domain = v
	}
}

// contentLen is how many rows the current step's content pane has.
func (w *Wizard) contentLen() int {
	switch w.step {
	case StepLang:
		return 2
	case StepNodes:
		return len(w.nodeValues())
	case StepProfile:
		return len(profileChoices())
	case StepOptions:
		return len(dataplanes) + len(storages)
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
