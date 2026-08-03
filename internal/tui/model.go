// Package tui renders a run. It subscribes to the engine's event stream and
// draws it; it owns no installation logic (ADR-002).
//
// The engine does not import this package and never will —
// internal/engine/arch_test.go fails the build if it does. Everything on screen
// is derived from events, so the same run can be watched from another terminal
// with `platformctl attach`, and closing this window does not end the install.
package tui

import (
	"sync"

	tea "charm.land/bubbletea/v2"

	"platform.ryxen.dev/platformctl/internal/event"
)

// Mode decides what quitting means, which is not the same question in the two
// places this screen is used.
type Mode int

const (
	// ModeOwner is `apply --tui`: the engine is in this process, so quitting
	// aborts the run. Saying "quit" without saying that would be a trap.
	ModeOwner Mode = iota
	// ModeObserver is `attach --tui`: the engine is elsewhere and outlives this
	// window.
	ModeObserver
)

// logRing is the number of log lines kept. A real install emits far more than
// a screen can hold, and scrollback belongs to the event file rather than to
// this process's memory.
const logRing = 500

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

// Model is the Bubble Tea model. It is also an attach.Sink: the follower
// goroutine hands it events, which it forwards into the program's own loop
// rather than mutating state from another goroutine.
type Model struct {
	cat    *Catalogue
	glyphs Glyphs
	ascii  bool
	mode   Mode
	runID  string

	width, height int

	order    []string
	phases   map[string]*phaseView
	logs     []event.Event
	failures []event.Event

	runStatus event.Status
	runDetail string
	showLogs  bool
	detached  bool
	quitting  bool

	// prog is set once the program is running so Sink methods can post into it.
	mu   sync.Mutex
	prog *tea.Program
}

// New builds a model.
func New(runID string, mode Mode, ascii bool, lang Lang) (*Model, error) {
	cat, err := LoadCatalogue(lang)
	if err != nil {
		return nil, err
	}
	return &Model{
		cat: cat, glyphs: GlyphsFor(ascii), ascii: ascii, mode: mode, runID: runID,
		width: 80, height: 24,
		phases: map[string]*phaseView{}, runStatus: event.StatusRunning,
	}, nil
}

// ---------------------------------------------------------------------------
// attach.Sink
// ---------------------------------------------------------------------------

type eventMsg struct{ e event.Event }
type resetMsg struct{}

// Reset discards accumulated state; a replay follows.
func (m *Model) Reset() { m.post(resetMsg{}) }

// Handle receives one event from the follower.
func (m *Model) Handle(e event.Event) error {
	m.post(eventMsg{e})
	return nil
}

// Attach binds the running program so events can be posted into it.
func (m *Model) Attach(p *tea.Program) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prog = p
}

func (m *Model) post(msg tea.Msg) {
	m.mu.Lock()
	p := m.prog
	m.mu.Unlock()
	if p != nil {
		p.Send(msg)
	}
}

// ---------------------------------------------------------------------------
// tea.Model
// ---------------------------------------------------------------------------

func (m *Model) Init() tea.Cmd { return nil }

// Update folds one message into the screen state.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case resetMsg:
		m.order, m.phases = nil, map[string]*phaseView{}
		m.logs, m.failures = nil, nil
		m.runStatus, m.runDetail = event.StatusRunning, ""
		return m, nil

	case eventMsg:
		m.apply(msg.e)
		return m, nil

	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m *Model) key(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "q", "ctrl+c", "esc":
		m.quitting = true
		return m, tea.Quit
	case "d":
		// Detach is only meaningful when the engine is somewhere else. In owner
		// mode the process exiting would take the engine with it, so the key
		// does nothing rather than pretending.
		if m.mode == ModeObserver {
			m.detached, m.quitting = true, true
			return m, tea.Quit
		}
	case "l":
		m.showLogs = !m.showLogs
	case "a":
		m.ascii = !m.ascii
		m.glyphs = GlyphsFor(m.ascii)
	case "g":
		if cat, err := LoadCatalogue(m.cat.Other()); err == nil {
			m.cat = cat
		}
	}
	return m, nil
}

// apply folds one event into the screen. Everything drawn comes from here, so
// anything the screen needs and the stream lacks shows up as a missing field
// rather than as a call back into the engine.
func (m *Model) apply(e event.Event) {
	switch e.Kind {
	case event.KindRun:
		m.runStatus, m.runDetail = e.Status, e.Detail

	case event.KindPhase:
		p := m.phase(e.Phase)
		p.status = e.Status
		p.code = e.Code
		if e.Status != event.StatusRunning || e.Detail != "" {
			p.detail = e.Detail
		}
		if e.Status.Terminal() {
			p.stepID, p.node, p.progress, p.attempt, p.maxTries = "", "", nil, 0, 0
		}

	case event.KindStep, event.KindProbe:
		if e.Phase != "" {
			p := m.phase(e.Phase)
			p.stepID, p.node = e.Step, e.Node
			p.progress, p.attempt, p.maxTries = e.Progress, e.Attempt, e.MaxAttempts
			if e.Detail != "" {
				p.detail = e.Detail
			}
		}

	case event.KindDecision:
		if e.Phase != "" {
			m.phase(e.Phase).detail = e.Code + " " + e.Detail
		}
		m.appendLog(e)

	case event.KindLog:
		m.appendLog(e)
		return
	}

	if e.Status == event.StatusFailed || e.Status == event.StatusBlocked {
		if e.Kind == event.KindStep || e.Kind == event.KindProbe {
			m.failures = append(m.failures, e)
		}
	}
}

func (m *Model) phase(id string) *phaseView {
	p, ok := m.phases[id]
	if !ok {
		p = &phaseView{id: id, status: event.StatusPending}
		m.phases[id] = p
		m.order = append(m.order, id)
	}
	return p
}

func (m *Model) appendLog(e event.Event) {
	m.logs = append(m.logs, e)
	if len(m.logs) > logRing {
		m.logs = m.logs[len(m.logs)-logRing:]
	}
}

// Detached reports whether the operator left the run running.
func (m *Model) Detached() bool { return m.detached }
