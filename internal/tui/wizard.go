package tui

import (
	"context"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"platform.ryxen.dev/malmok/api/v1alpha1"
	"platform.ryxen.dev/malmok/internal/event"
	"platform.ryxen.dev/malmok/internal/exec"
	"platform.ryxen.dev/malmok/internal/rke2"
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
	// Profile comes before everything it decides: the network mode, the PKI
	// mode and the storage driver all follow from it, and a screen that asks
	// about a proxy before knowing whether there is one wastes a question.
	// StepProfile is retired: the wizard composes axis by axis and the profile
	// is derived from the composition (MatchedProfile). The constant stays so
	// step numbering in state files and tests does not shift.
	StepProfile
	// StepWhere is the question the wizard used to skip: which machine is this
	// being built on. Asking for an address first meant an operator installing
	// on the machine in front of them had to read their own IP off `ip addr`
	// and type it back -- a value the tool was sitting on the whole time.
	StepWhere
	StepNodes
	StepNetwork
	StepOptions
	StepRegistry
	StepPKI
	StepGateway
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

	// StepTarget and StepUpgrade are the upgrade flow's own: which version to
	// move to, and moving there.
	StepTarget
	StepUpgrade

	// StepPrefs is how the screen looks, as opposed to what it builds. The
	// language used to be the first question of an install, which said it was
	// a property of the cluster; it is a property of the person reading, and
	// they are the same person on their twentieth run.
	StepPrefs
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
	modeUpgrade
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
		StepWhere, StepNodes, StepNetwork, StepOptions,
		StepRegistry, StepPKI, StepGateway, StepPreflight, StepSummary, StepInstall, StepDone,
	}
	settingsSteps = []Step{
		StepOpen, StepNodes, StepNetwork, StepOptions,
		StepRegistry, StepPKI, StepGateway, StepSave,
	}
	// The upgrade asks two questions and then does one thing. It does not walk
	// the configuration screens: an upgrade changes the version and nothing
	// else, and offering to edit the dataplane on the way past would invite a
	// change this flow has no way to apply.
	upgradeSteps = []Step{StepOpen, StepTarget, StepUpgrade, StepDone}
)

// stepKeys is the catalogue key for each screen that appears in a rail.
var stepKeys = map[Step]string{
	StepProfile: "step.profile", StepNodes: "step.nodes",
	StepNetwork: "step.network", StepOptions: "step.options",
	StepRegistry: "step.registry", StepPKI: "step.pki",
	StepGateway:   "step.gateway",
	StepPreflight: "step.preflight", StepSummary: "step.summary",
	StepInstall: "step.install", StepDone: "step.done",
	StepOpen: "step.open", StepSave: "step.save",
	StepTarget: "step.target", StepUpgrade: "step.upgrade",
	StepWhere: "step.where",
	StepPrefs: "step.prefs",
}

// flow is the sequence the current mode walks.
func (w *Wizard) flow() []Step {
	switch w.mode {
	case modeSettings:
		return settingsSteps
	case modeUpgrade:
		return upgradeSteps
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

	// Local says the first server is the machine malmok is running on.
	//
	// Chosen on a screen rather than inferred from an address, because it is
	// the first thing an operator knows and the last thing they should have to
	// spell out. It is not written into the document: the address is this
	// machine's, so a document read back here routes locally on its own, and
	// the same file copied to another machine correctly falls back to SSH
	// rather than silently installing onto the wrong box.
	Local bool

	// The axes. Composed one at a time on the screens that own them; there is
	// no screen that asks for all of them at once under a name, because the
	// six names could not cover the combinations people actually have.
	OSFamily     string
	Routing      string
	Fallback     string
	GitOpsSource string

	// PinnedGateway is a profile's own strictness about requiring a DNS record
	// before install (PF-612). Not composed; carried so a document that had it
	// keeps it.
	PinnedGateway bool

	// Profile is what the composition turned out to match. Written by
	// MatchedProfile rather than chosen, and kept here so the summary can show
	// it without recomputing.
	Profile   string
	Dataplane string
	Storage   string

	// Chosen on their own screens rather than inherited silently: whether to
	// issue certificates at all, and where images come from.
	PKIMode      string
	RegistryMode string

	// The three a profile used to decide on its own. A profile is a starting
	// point -- "검증된 기준값", as the screen says -- and a combination it does
	// not happen to contain was unreachable while these had no screen: there
	// was no way to build a homelab behind a proxy, or an air-gapped site with
	// anything but the conservative dataplane.
	NetworkMode     string
	Encrypt         bool
	DowngradePolicy string

	// Addressing the customer's network team has to agree to.
	ProxyHTTP  string
	ProxyHTTPS string
	NoProxy    []string
	LBPool     []string

	// Exposure is how the gateway is reached: node-ips, lb-pool, or none at
	// all. The default is none, because a cluster that claims an address
	// nobody assigned it is the thing IDC and air-gapped policy forbids --
	// and a first build frequently has no name to serve yet either.
	Exposure    string
	GatewayName string
	// GatewayAddress pins the address a pool gateway takes. PF-612 asks for
	// it at a customer site: the DNS record is requested before the install,
	// so the address has to be decided rather than allocated.
	GatewayAddress string

	// SourceRefs, not values. cluster.yaml is handed over at the end of the
	// engagement, so what is collected here is where to find a secret rather
	// than the secret itself.
	RegistryHost string
	RegistryUser string
	RegistryPass string
	RegistryCA   string
	// RegistryBundle is the Hauler artifact an air-gapped site carried across.
	RegistryBundle string
	// RegistryInsecure accepts a registry certificate that cannot be verified.
	RegistryInsecure bool

	NFSServer string
	NFSPath   string

	CARoot         string
	CAIntermediate string
	CAKey          string

	// BYOCert is the supplied certificate and its key, for the mode that
	// issues nothing. Selecting that mode used to show the private-CA fields,
	// which asked for an issuing CA on a build that has none.
	BYOCert string
	BYOKey  string
	BYOCA   string

	// TrustBundle distributes a private CA to every namespace. Without it the
	// nodes trust the CA and the pods do not, and the failure is an opaque
	// x509 error from inside a container.
	TrustBundle bool

	ACMEEmail    string
	ACMEServer   string
	ACMEProvider string
	ACMEToken    string

	// DocPath is the file the settings and upgrade flows read. It is a wizard
	// value rather than a document one and is never serialised -- a document
	// that recorded its own location would be wrong the moment it was copied.
	DocPath string

	// UpgradeTo is the version the upgrade flow moves to. Not written into the
	// document by the flow itself: the file describes what the cluster is, and
	// it becomes true when the last node reports the new version, not when
	// somebody types it.
	UpgradeTo string
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
	// logExpanded widens the progress screen's log tail (the Logs button).
	logExpanded bool
	runStat     event.Status

	preflight Work
	install   Work
	upgrade   Work
	workCtx   context.Context
	hideRail  bool

	// hits is where the last frame put things, so a click can be turned back
	// into the thing that was clicked.
	hits hitMap

	// hoverKind and hoverIndex are what the pointer is over. Kept apart from
	// the cursor: a pointer crossing the screen must not look like an
	// operator changing their selection.
	hoverKind  hitKind
	hoverIndex int

	// prefsErr is what went wrong saving the preferences, empty when nothing
	// did.
	prefsErr string

	// channels is what the RKE2 channel server answered at start, zero when it
	// did not. Stable is the suggestion; Space on the version field flips to
	// Latest and back for the operator who wants the newest release without
	// typing it.
	channels rke2.Channels

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
			Lang: lang,
			// The SSH convention, not an example: root on 22 is what a fresh
			// machine answers, and both are one field away.
			SSHUser: "root", SSHPort: "22",
			// Everything else that used to be seeded here -- example nodes,
			// a proxy at acme.local, a Harbor, CA references, an ACME account
			// -- is gone. An example value in an editable field reads as a
			// real one, gets accepted by habit, and produces a document that
			// names machines nobody owns; an empty field is a question, and
			// the validator asks it at the summary if it goes unanswered. The
			// server address comes from the machine itself (setLocal), the
			// version from the channel server, and an empty join address
			// already means "this server, trade stated".
			//
			// What stays seeded are defaults with defined meaning, each the
			// answer most builds want and each one screen away; what they
			// match is derived (MatchedProfile) rather than chosen.
			OSFamily: "auto", Routing: "overlay",
			NetworkMode: "online", DowngradePolicy: "confirm",
			Dataplane: "cilium-gw", Fallback: "canal-traefik", Storage: "local-path",
			PKIMode: "none", RegistryMode: "embedded",
			// No gateway until somebody asks for one. Every other default
			// here is the answer most builds want; this one is the answer
			// most networks require -- a pool address is another IP
			// answering on the segment, and the sites this tool is for
			// frequently allow only the addresses the nodes already hold.
			Exposure: "none", GatewayName: "public",
		},
	}
	// And the machine in front of the operator is the default target. An
	// installer is normally run on the machine being installed; opening on
	// "somewhere else" makes the common case the one that takes more steps.
	// A machine with no routable address cannot be a node, so it is not
	// offered as the default.
	if len(exec.LocalIPv4s()) > 0 {
		wz.setLocal(true)
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

func (w *Wizard) Init() tea.Cmd { return tea.Batch(spinEvery(), fetchChannels) }

// versionMsg carries what the channel server answered.
type versionMsg struct{ ch rke2.Channels }

// fetchChannels asks RKE2 what "current" means, on both channels.
//
// Failure is silent by design: on an air-gapped or proxied site there is no
// answer, the field stays empty, and the validator asks for it -- which is
// honest, where a stale default is a suggestion that looks like knowledge.
func fetchChannels() tea.Msg {
	ch, err := rke2.FetchChannels(context.Background())
	if err != nil {
		return nil
	}
	return versionMsg{ch: ch}
}

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

	case versionMsg:
		// Kept, so the version field can flip between the two on Space. Filled
		// only while empty: an operator who already typed a version has
		// answered the question, and the network must not overrule them.
		w.channels = msg.ch
		if w.cfg.Version == "" {
			w.cfg.Version = msg.ch.Stable
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

	case tea.MouseClickMsg:
		return w.click(msg.Mouse())

	case tea.MouseWheelMsg:
		return w.wheel(msg.Mouse())

	case tea.MouseMotionMsg:
		w.setHover(msg.Mouse())
		return w, nil
	}
	return w, nil
}

// click acts on what was drawn where the pointer was.
//
// The regions come from the renderer, which is the only part that knows where
// it put things; the actions are the ones the keyboard already reaches, so a
// click and a keypress cannot disagree about what a screen does.
func (w *Wizard) click(m tea.Mouse) (tea.Model, tea.Cmd) {
	if m.Button != tea.MouseLeft {
		return w, nil
	}

	kind, index := w.hits.at(m.X, m.Y)
	if kind == hitNone {
		// The content column keeps its rows in a line map rather than as
		// regions: a screen knows which line it drew a choice on, not which
		// rectangle of the terminal that became.
		if i, ok := w.hits.item(m.Y); ok && m.X >= w.hits.contentCol {
			kind, index = hitItem, i
		}
	}

	switch kind {
	case hitExit:
		if ex := w.exitButton(); ex != nil {
			w.focus, w.btn = focusButtons, exitFocus
			return w.activate(ex.Label)
		}

	case hitButton:
		btns := w.buttons()
		if index < len(btns) {
			w.focus, w.btn = focusButtons, index
			return w.activate(btns[index].Label)
		}

	case hitRail:
		// Backwards only. A rail entry is a step, and stepping forward past
		// screens the operator has not answered would submit blanks; going
		// back to change an answer is what the rail is for.
		flow := w.flow()
		if index < len(flow) && index < w.railIndex() {
			w.step = flow[index]
			w.focus = focusContent
		}

	case hitItem:
		// The cursor goes where the pointer is, and the row acts as if it had
		// been chosen -- which for a radio is selecting it and for a field is
		// opening it. Clicking a row and pressing the key on it are the same
		// gesture said two ways.
		w.focus = focusContent
		w.setCursor(index)

		// A list of destinations is different: clicking an entry of the menu
		// or of the run list means "this one", the way it does everywhere
		// else, and making the operator click and then press Enter would be
		// this tool's own invention.
		switch w.step {
		case StepMenu, StepRuns:
			return w.next()
		}
		return w.selectUnderCursor()
	}
	return w, nil
}

// setCursor puts the cursor on an index, wherever this screen keeps it.
//
// Three screens track their own selection -- the menu, the run list -- and the
// rest share the per-step cursor. A click has to reach whichever one this
// screen reads, or the pointer moves a cursor nobody is looking at.
func (w *Wizard) setCursor(index int) {
	switch w.step {
	case StepMenu:
		if index < len(menuItems) {
			w.menu = index
		}
	case StepRuns:
		if index < len(w.runs) {
			w.runSel = index
		}
	default:
		w.cursor[w.step] = index
	}
}

// setHover records what the pointer is over, so the next frame can show it.
//
// Only what is clickable hovers. Highlighting prose the pointer happens to
// cross would teach the operator that the highlight means nothing.
func (w *Wizard) setHover(m tea.Mouse) {
	kind, index := w.hits.at(m.X, m.Y)
	if kind == hitNone {
		if i, ok := w.hits.item(m.Y); ok && m.X >= w.hits.contentCol {
			kind, index = hitItem, i
		}
	}
	w.hoverKind, w.hoverIndex = kind, index
}

// wheel moves the cursor, which is what a wheel means on a list.
func (w *Wizard) wheel(m tea.Mouse) (tea.Model, tea.Cmd) {
	switch m.Button {
	case tea.MouseWheelUp:
		return w.key(tea.KeyPressMsg{Code: tea.KeyUp})
	case tea.MouseWheelDown:
		return w.key(tea.KeyPressMsg{Code: tea.KeyDown})
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
		// The character set and the language are two settings. Changing one
		// used to change the other -- pressing `a` switched a Korean screen to
		// English, on the reasoning that a terminal which cannot draw box
		// characters cannot draw Hangul either. That is sometimes true and it
		// is never this switch's business: an operator who asked for Korean
		// asked for Korean, and one who cannot read the result can change it
		// on a screen that now exists for the purpose.
		w.ascii = !w.ascii
		w.glyphs = GlyphsFor(w.ascii)
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
	case StepPrefs:
		if cur < len(prefsRows) {
			prefsRows[cur].toggle(w)
			// Written as it is changed rather than on the way out: an operator
			// who closes the window has still made the choice.
			w.savePrefs()
		}
	case StepWhere:
		w.setLocal(cur == 0)
	case StepNodes:
		if vs := w.versionChoices(); cur < len(vs) {
			w.cfg.Version = vs[cur].id
			break
		} else if cur < len(vs)+len(osFamilies) {
			w.cfg.OSFamily = osFamilies[cur-len(vs)].id
			break
		}
		// Space on the version field flips between the channel server's two
		// answers -- stable is upstream's production judgement and the
		// default, latest is the newest release -- with typing still the
		// override. Same grammar as every other chooser, zero extra rows.
		if fi := w.fieldIndex(); fi >= 0 {
			fs := w.fieldsFor(StepNodes)
			if fi < len(fs) && fs[fi].labelKey == "nodes.version" && w.channels.Latest != "" {
				if w.cfg.Version == w.channels.Latest {
					w.cfg.Version = w.channels.Stable
				} else {
					w.cfg.Version = w.channels.Latest
				}
				break
			}
		}
		// The addresses of this machine, when it is the one being built on.
		// A list rather than a field: the machine knows them, and a multi-homed
		// host is a real case where the choice matters (PF-609) and typing is
		// not what should decide it. It sits below the fields -- the fields
		// are the work, the chooser is one confirmation, and a list long
		// enough to scroll must not stand between the operator and the work.
		cur -= len(osFamilies) + len(w.fieldsFor(StepNodes))
		if w.cfg.Local && cur >= 0 && cur < len(exec.LocalIPv4s()) {
			w.cfg.Server = exec.LocalIPv4s()[cur]
		}
	case StepOpen:
		// The list fills the field rather than loading straight away. Choosing
		// a file and reading it are two decisions, and one keystroke that did
		// both would give no chance to look at the path first.
		if i := cur - len(w.fieldsFor(StepOpen)); i >= 0 && i < len(w.openFiles) {
			w.cfg.DocPath = w.openFiles[i].Path
		}
	case StepGateway:
		if cur < len(exposures) {
			w.cfg.Exposure = exposures[cur].id
		}

	case StepRegistry:
		switch {
		case cur < len(registryModes):
			w.cfg.RegistryMode = registryModes[cur].id
		case cur < len(registryModes)+w.registryExtraRows():
			w.cfg.RegistryInsecure = cur == len(registryModes)+1
		}
	case StepPKI:
		switch {
		case cur < len(pkiModes):
			w.cfg.PKIMode = pkiModes[cur].id
		case cur < len(pkiModes)+w.pkiExtraRows():
			w.cfg.TrustBundle = cur == len(pkiModes)
		}
	case StepNetwork:
		switch {
		case cur < len(networkModes):
			w.cfg.NetworkMode = networkModes[cur].id
		case cur < len(networkModes)+2:
			// Two rows, on and off, rather than a single row that toggles: a
			// setting somebody's security team asked for should show both
			// answers and which one is chosen.
			w.cfg.Encrypt = cur == len(networkModes)
		case cur < len(networkModes)+2+len(routingModes):
			w.cfg.Routing = routingModes[cur-len(networkModes)-2].id
		}
	case StepOptions:
		switch {
		case cur < len(dataplanes):
			w.cfg.Dataplane = dataplanes[cur].id
		case cur < len(dataplanes)+len(storages):
			w.cfg.Storage = storages[cur-len(dataplanes)].id
		case cur < len(dataplanes)+len(storages)+len(downgradePolicies):
			w.cfg.DowngradePolicy = downgradePolicies[cur-len(dataplanes)-len(storages)].id
		case cur < len(dataplanes)+len(storages)+len(downgradePolicies)+len(fallbacks):
			w.cfg.Fallback = fallbacks[cur-len(dataplanes)-len(storages)-len(downgradePolicies)].id
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
	case w.cat.T("btn.menu"):
		w.returnToMenu()
		return w, nil
	case w.cat.T("btn.logs"):
		// A button that does nothing is worse than no button. This one
		// widens the log tail from a glance to a page and back.
		w.logExpanded = !w.logExpanded
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
		w.cat.T("btn.load"), w.cat.T("btn.save"), w.cat.T("btn.upgrade"),
		w.cat.T("btn.done"):
		return w.next()
	}
	return w, nil
}

func (w *Wizard) back() (tea.Model, tea.Cmd) {
	// Nothing goes back out of work already done on a node, and nothing goes
	// back from a document already written.
	if w.busy || w.step == StepMenu || w.step == StepInstall ||
		w.step == StepUpgrade || w.step == StepDone {
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
	if i <= 0 || w.step == StepRuns || w.step == StepPrefs {
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
	// A screen's content can shrink under a cursor that was left on it: the
	// node screen drops the SSH user and port when every address turns out to
	// be this machine's. A cursor past the end highlights nothing and makes
	// Enter do something other than what the screen says.
	if n := w.contentLen(); n > 0 && w.cursor[w.step] >= n {
		w.cursor[w.step] = n - 1
	}
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
		w.mode = item.Mode
		if item.Enter == StepOpen {
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

	case StepPrefs:
		// Nothing to move on to: the screen is the whole of it, and its choices
		// are saved as they are made.
		w.step = StepMenu
		w.enter()
		return w, nil

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
			w.returnToMenu()
			return w, nil
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

	case StepTarget:
		if w.busy {
			return w, nil
		}
		w.advance()
		w.focus = focusButtons
		return w, w.start(w.upgrade)

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
		w.returnToMenu()
		return w, nil
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
	case StepInstall, StepUpgrade:
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
		w.appendLog(e)
		return
	}

	// A step's verdict is the install's narration -- "swap is off and
	// /etc/fstab has no entry", "rke2-server is active" -- and it already
	// arrives in every step event's detail. Only the demo ever emitted
	// kind=log, so a real install showed an empty log pane while the
	// narration scrolled past unrendered.
	if e.Kind == event.KindStep && e.Status.Terminal() && e.Detail != "" {
		w.appendLog(e)
	}

	if (e.Status == event.StatusFailed || e.Status == event.StatusBlocked) &&
		(e.Kind == event.KindStep || e.Kind == event.KindProbe) {
		w.failures = append(w.failures, e)
	}
}

// appendLog keeps the rolling log window.
func (w *Wizard) appendLog(e event.Event) {
	w.logs = append(w.logs, e)
	if len(w.logs) > 200 {
		w.logs = w.logs[len(w.logs)-200:]
	}
}

// anyFailedStep reports whether a step -- rather than a check -- failed.
// A probe emits its warnings as failed events too, so "something failed" and
// "the build failed" are not the same question.
func (w *Wizard) anyFailedStep() bool {
	for _, e := range w.failures {
		if e.Kind == event.KindStep {
			return true
		}
	}
	return false
}

// anyBlocked reports whether a collected finding actually stops the run.
func (w *Wizard) anyBlocked() bool {
	for _, e := range w.failures {
		if e.Status == event.StatusBlocked {
			return true
		}
	}
	return false
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
	case StepNetwork:
		return len(networkModes) + 2 + len(routingModes) + len(w.fieldsFor(StepNetwork))
	case StepNodes:
		return len(w.versionChoices()) + len(osFamilies) +
			w.localAddressCount() + len(w.fieldsFor(StepNodes))
	case StepGateway:
		return len(exposures) + len(w.fieldsFor(StepGateway))
	case StepRegistry:
		return len(registryModes) + w.registryExtraRows() + len(w.fieldsFor(StepRegistry))
	case StepPKI:
		return len(pkiModes) + w.pkiExtraRows() + len(w.fieldsFor(StepPKI))
	case StepOptions:
		return len(dataplanes) + len(storages) + len(downgradePolicies) +
			len(fallbacks) + len(w.fieldsFor(StepOptions))
	case StepWhere:
		return 2
	case StepPrefs:
		return len(prefsRows)
	case StepOpen:
		return len(w.fieldsFor(StepOpen)) + len(w.openFiles)
	case StepTarget:
		return len(w.fieldsFor(StepTarget))
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
// versionChoices are the answers the channel server gave, as a chooser.
//
// A version typed by hand is still the override -- an air-gapped site
// installs what its bundle carries, and no channel knows that -- but on every
// other build the choice is between two answers upstream publishes, and
// making an operator type one of them is asking them to transcribe.
//
// Empty until the channels answer: two rows saying nothing would be worse
// than none, and the field below still takes a version.
func (w *Wizard) versionChoices() []choice {
	if w.channels.Stable == "" {
		return nil
	}
	out := []choice{{id: w.channels.Stable, label: w.channels.Stable, note: "nodes.version.stable"}}
	if w.channels.Latest != "" && w.channels.Latest != w.channels.Stable {
		out = append(out, choice{id: w.channels.Latest, label: w.channels.Latest, note: "nodes.version.latest"})
	}
	return out
}

// firstFieldIndex is the cursor index of a step's first field: everything
// before it is a choice. Derived from fieldIndex rather than restated, so a
// new choice group cannot leave the two disagreeing.
func (w *Wizard) firstFieldIndex(step Step) int {
	was := w.cursor[step]
	defer func() { w.cursor[step] = was }()

	w.cursor[step] = 0
	return -w.fieldIndexFor(step)
}

// fieldIndexFor is fieldIndex for a named step.
func (w *Wizard) fieldIndexFor(step Step) int {
	wasStep := w.step
	w.step = step
	defer func() { w.step = wasStep }()
	return w.fieldIndex()
}

func (w *Wizard) fieldIndex() int {
	i := w.cursor[w.step]
	switch w.step {
	case StepNodes:
		i -= len(w.versionChoices()) + len(osFamilies)
	case StepGateway:
		i -= len(exposures)
	case StepNetwork:
		i -= len(networkModes) + 2 + len(routingModes)
	case StepOptions:
		i -= len(dataplanes) + len(storages) + len(downgradePolicies) + len(fallbacks)
	case StepRegistry:
		i -= len(registryModes) + w.registryExtraRows()
	case StepPKI:
		i -= len(pkiModes) + w.pkiExtraRows()
	}
	return i
}

// cursorIsField reports whether the content cursor is on an editable line
// rather than on a choice.
func (w *Wizard) cursorIsField() bool {
	i := w.fieldIndex()
	return i >= 0 && i < len(w.fieldsFor(w.step))
}

// returnToMenu goes back to where the operator arrived from.
//
// The run's view state is cleared with it: the phase list, the log tail and
// the failures belong to the run that just ended, and a second install folding
// its events on top of them would draw two runs as one. What the operator
// collected -- the configuration -- stays, because walking the flow again with
// the same answers is the common case, not an accident to be wiped.
func (w *Wizard) returnToMenu() {
	w.step, w.mode = StepMenu, modeInstall
	w.order, w.phases = nil, map[string]*phaseView{}
	w.logs, w.failures = nil, nil
	w.runStat = event.StatusPending
	w.startedAt, w.finishedAt = time.Time{}, time.Time{}
	w.workErr, w.saved, w.openErr, w.saveErr = nil, "", "", ""
	w.enter()
}

// channelTag names the channel a version came from -- "stable" or "latest",
// the channel server's own words, untranslated like every other upstream
// identifier. Empty for a hand-typed version: it belongs to no channel, and
// saying otherwise would be labelling a guess.
func (w *Wizard) channelTag(version string) string {
	switch {
	case version == "":
		return ""
	case version == w.channels.Stable:
		return "stable"
	case version == w.channels.Latest:
		return "latest"
	}
	return ""
}
