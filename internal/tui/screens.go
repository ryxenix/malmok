package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"platform.ryxen.dev/platformctl/api/v1alpha1"
	"platform.ryxen.dev/platformctl/internal/event"
	"platform.ryxen.dev/platformctl/internal/spec"
)

// One function per step, each returning the Frame the chrome draws. Keeping the
// screens here and the state machine in wizard.go means a layout change never
// touches the transitions.

// choice is a selectable option with a one-line note.
type choice struct {
	id    string
	label string
	note  string // catalogue key, or literal text when derived from a baseline
}

// The Tier-1 profiles come from internal/spec rather than from a list here.
// Two lists of the same six profiles would disagree the first time one is
// edited, and the one the engine reads has to win.
func profileChoices() []choice {
	names := spec.Profiles()
	out := make([]choice, 0, len(names))
	for _, name := range names {
		b, _ := spec.BaselineFor(name)
		// Identifiers, not prose: os family, network mode, PKI and storage are
		// the same words in every language.
		out = append(out, choice{
			id: string(name), label: string(name),
			note: fmt.Sprintf("%s %s %s %s %s %s %s",
				b.OSFamily, bullet, b.NetworkMode, bullet, b.PKIMode, bullet, b.Storage),
		})
	}
	return out
}

// bullet is filled in per render so the ASCII fallback reaches these too.
var bullet = "·"

// The dataplane presets of ADR-004. CNI, Gateway and LB IP source are one
// atomic choice, which is why they are presets rather than three questions.
var dataplanes = []choice{
	{"cilium-gw", "cilium-gw", "dp.cilium_gw"},
	{"cilium-traefik", "cilium-traefik", "dp.cilium_traefik"},
	{"canal-traefik", "canal-traefik", "dp.canal"},
}

// Where images come from. Embedded first because it is the answer for most
// builds: nothing to stand up, nothing to keep alive, no credentials.
var registryModes = []choice{
	{"embedded", "embedded", "regmode.embedded"},
	{"upstream", "upstream", "regmode.upstream"},
	{"external", "external", "regmode.external"},
	{"internal", "internal", "regmode.internal"},
}

// Whether to issue certificates at all, and how. "none" first because at a
// first build the domain is usually not decided yet.
var pkiModes = []choice{
	{"none", "none", "pkimode.none"},
	{"acme-dns01", "acme-dns01", "pkimode.acme"},
	{"private-ca", "private-ca", "pkimode.ca"},
	{"byo-cert", "byo-cert", "pkimode.byo"},
}

var storages = []choice{
	{"local-path", "local-path", "st.local"},
	{"longhorn", "longhorn", "st.longhorn"},
	{"nfs", "nfs", "st.nfs"},
}

// View renders the current step.
func (w *Wizard) View() tea.View {
	// The counter and the rail belong to the install flow. Numbering the menu
	// would make arriving at the tool look like step one of a build.
	context := ""
	if w.inInstallFlow() {
		context = fmt.Sprintf("%d/%d", w.railIndex()+1, len(w.flow()))
	}
	f := Frame{
		Title:    w.cat.T("app.title"),
		Context:  context,
		Rail:     w.rail(),
		Buttons:  w.buttons(),
		Focused:  w.btn,
		Exit:     w.exitButton(),
		HideRail: w.hideRail,
	}
	if w.focus == focusButtons {
		f.Focused = w.btn
	} else {
		f.Focused = -1
	}

	body := w.contentWidth()
	switch w.step {
	case StepMenu:
		f.Heading, f.Body, f.Status = w.menuScreen(body)
	case StepRuns:
		f.Heading, f.Body, f.Status = w.runsScreen(body)
	case StepLang:
		f.Heading, f.Body, f.Status = w.langScreen(body)
	case StepNodes:
		f.Heading, f.Body, f.Status = w.formScreen(StepNodes, "nodes.heading", "nodes.help", body)
		f.Body = w.withTopology(f.Body, body)
	case StepNetwork:
		f.Heading, f.Body, f.Status = w.formScreen(StepNetwork, "net.heading", "net.help", body)
	case StepRegistry:
		f.Heading, f.Body, f.Status = w.registryScreen(body)
	case StepPKI:
		f.Heading, f.Body, f.Status = w.pkiScreen(body)
	case StepProfile:
		f.Heading, f.Body, f.Status = w.profileScreen(body)
	case StepOptions:
		f.Heading, f.Body, f.Status = w.optionsScreen(body)
	case StepPreflight:
		f.Heading, f.Body, f.Status = w.progressScreen(body, "preflight")
	case StepSummary:
		f.Heading, f.Body, f.Status = w.summaryScreen(body)
		f.Body = w.withTopology(f.Body, body)
	case StepInstall:
		f.Heading, f.Body, f.Status = w.progressScreen(body, "install")
	case StepDone:
		f.Heading, f.Body, f.Status = w.doneScreen(body)
	case StepOpen:
		f.Heading, f.Body, f.Status = w.openScreen(body)
	case StepSave:
		f.Heading, f.Body, f.Status = w.saveScreen(body)
	case StepTarget:
		f.Heading, f.Body, f.Status = w.targetScreen(body)
	case StepUpgrade:
		f.Heading, f.Body, f.Status = w.progressScreen(body, "upgrade")
	}

	// The toggles are appended to whatever the screen wanted to say, so they
	// are discoverable without a help screen nobody opens. The separator comes
	// from the glyph set rather than the catalogue: a translator has no way to
	// know whether the terminal can draw it.
	f.Status = w.keyHints(f.Status)
	// The heading carries the context now, so the title stays the tool's name:
	// calling every screen "installer" was wrong the moment the menu offered
	// anything else.

	v := tea.NewView(w.theme.Render(f, w.width, w.height, w.glyphs))
	v.AltScreen = true
	return v
}

func (w *Wizard) contentWidth() int {
	if w.width >= minChromeW {
		return w.width - railWidth - 3 - gutter
	}
	return w.width - gutter*2
}

func (w *Wizard) rail() []RailItem {
	// Nothing to show outside the install flow: the menu is not a stage of a
	// build, and a rail listing eleven install steps beside it would say the
	// operator is already committed to one.
	if !w.inInstallFlow() {
		return nil
	}
	here := w.railIndex()
	flow := w.flow()

	out := make([]RailItem, len(flow))
	for i, step := range flow {
		st := RailFuture
		switch {
		case i < here:
			st = RailDone
		case i == here:
			st = RailCurrent
		}
		out[i] = RailItem{Label: w.cat.T(stepKeys[step]), State: st}
	}
	return out
}

// buttons are the actions available on the current step. Back is absent where
// going back would mean undoing work already done on a node.
func (w *Wizard) buttons() []Button {
	back := Button{Label: w.cat.T("btn.back")}

	switch w.step {
	case StepMenu:
		return []Button{{Label: w.cat.T("btn.open"), Primary: true}}
	case StepRuns:
		if len(w.runs) == 0 {
			return []Button{back}
		}
		return []Button{back, {Label: w.cat.T("btn.open"), Primary: true}}
	case StepLang:
		// Back goes to the menu rather than nowhere: an operator who chose
		// Install by mistake has to be able to leave without quitting.
		return []Button{back, {Label: w.cat.T("btn.next"), Primary: true}}
	case StepProfile, StepNodes, StepNetwork, StepOptions, StepRegistry, StepPKI:
		return []Button{back, {Label: w.cat.T("btn.next"), Primary: true}}
	case StepOpen:
		if len(w.openFiles) == 0 && strings.TrimSpace(w.cfg.DocPath) == "" {
			return []Button{back}
		}
		return []Button{back, {Label: w.cat.T("btn.load"), Primary: true}}
	case StepSave:
		// A document that does not validate must not offer to be written: the
		// operator opened this to correct one, and saving it broken is the one
		// outcome nobody asked for.
		if w.saved != "" {
			return []Button{{Label: w.cat.T("btn.close"), Primary: true}}
		}
		if _, broken := w.firstProblemStep(); broken {
			return []Button{back, {Label: w.cat.T("btn.fix"), Primary: true}}
		}
		return []Button{back, {Label: w.cat.T("btn.save"), Primary: true}}
	case StepPreflight:
		if w.busy {
			return nil
		}
		if w.workErr != nil {
			return []Button{back, {Label: w.cat.T("btn.check"), Primary: true}}
		}
		return []Button{back, {Label: w.cat.T("btn.next"), Primary: true}}
	case StepSummary:
		// An invalid document must not offer to install. Offering the fix
		// instead is the difference between a report and something the
		// operator can act on.
		if _, broken := w.firstProblemStep(); broken {
			return []Button{back, {Label: w.cat.T("btn.fix"), Primary: true}}
		}
		return []Button{back, {Label: w.cat.T("btn.install"), Primary: true}}
	case StepTarget:
		return []Button{back, {Label: w.cat.T("btn.upgrade"), Primary: true}}
	case StepInstall, StepUpgrade:
		if w.busy {
			return []Button{{Label: w.cat.T("btn.logs")}}
		}
		return []Button{{Label: w.cat.T("btn.next"), Primary: true}}
	default:
		return []Button{{Label: w.cat.T("btn.close"), Primary: true}}
	}
}

// ---------------------------------------------------------------------------
// Input screens
// ---------------------------------------------------------------------------

func (w *Wizard) langScreen(width int) (string, string, string) {
	body := w.dim(w.cat.T("lang.help"), width) + "\n\n" +
		w.theme.Radio(
			[]string{"English", "한국어"},
			[]string{"default", "toggle with g"},
			langIndex(w.cfg.Lang), w.cursor[StepLang], width, w.glyphs)
	return w.cat.T("lang.heading"), body, w.cat.T("hint.select")
}

func langIndex(l Lang) int {
	if l == LangKO {
		return 1
	}
	return 0
}

// formScreen renders any step whose content is a list of editable fields.
//
// One function for all of them: a screen that exists because the validator
// asks for a value should not also be a place where the layout can differ.
func (w *Wizard) formScreen(step Step, headingKey, helpKey string, width int) (string, string, string) {
	cur := w.cursor[step]
	body := w.dim(w.cat.T(helpKey), width) + "\n\n" +
		w.theme.Fields(w.labels(step), w.maskedValues(step), cur, w.editing, width, w.glyphs)

	if h := w.fieldHint(int(step), cur); h != "" {
		body += "\n" + w.dim(w.glyphs.Dot+" "+h, width)
	}

	hint := w.cat.T("hint.edit")
	if w.editing {
		hint = w.cat.T("hint.editing")
	}
	return w.cat.T(headingKey), body, hint
}

// maskedValues hides secret fields unless they are being edited. What is
// stored is a SourceRef rather than a secret, but a token typed at a customer
// site is still read over somebody's shoulder.
func (w *Wizard) maskedValues(step Step) []string {
	vals := w.values(step)
	secret := w.masked(step)
	cur := w.cursor[step]
	for i := range vals {
		if secret[i] && !(w.editing && i == cur) && vals[i] != "" {
			vals[i] = strings.Repeat("*", min(len([]rune(vals[i])), 12))
		}
	}
	return vals
}

func (w *Wizard) profileScreen(width int) (string, string, string) {
	bullet = w.glyphs.Dot
	profiles := profileChoices()
	labels, notes := make([]string, len(profiles)), make([]string, len(profiles))
	chosen := 0
	for i, p := range profiles {
		labels[i], notes[i] = p.label, p.note
		if p.id == w.cfg.Profile {
			chosen = i
		}
	}
	body := w.dim(w.cat.T("profile.help"), width) + "\n\n" +
		w.theme.Radio(labels, notes, chosen, w.cursor[StepProfile], width, w.glyphs) +
		"\n" + w.dim(w.cat.T("note.profile"), width)
	return w.cat.T("profile.heading"), body, w.cat.T("hint.select")
}

func (w *Wizard) optionsScreen(width int) (string, string, string) {
	var b strings.Builder
	b.WriteString(w.dim(w.cat.T("options.help"), width) + "\n\n")

	cur := w.cursor[StepOptions]

	b.WriteString(w.theme.Body.Render(w.cat.T("options.dataplane")) + "\n")
	b.WriteString(w.theme.Radio(labelsOf(dataplanes), notesOf(w.cat, dataplanes),
		indexOf(dataplanes, w.cfg.Dataplane), cur, width, w.glyphs))

	b.WriteString("\n" + w.theme.Body.Render(w.cat.T("options.storage")) + "\n")
	b.WriteString(w.theme.Radio(labelsOf(storages), notesOf(w.cat, storages),
		indexOf(storages, w.cfg.Storage), cur-len(dataplanes), width, w.glyphs))

	if fs := w.fieldsFor(StepOptions); len(fs) > 0 {
		b.WriteString("\n")
		b.WriteString(w.theme.Fields(w.labels(StepOptions), w.maskedValues(StepOptions),
			w.fieldIndex(), w.editing, width, w.glyphs))
	}

	b.WriteString("\n" + w.dim(w.cat.T("note.options"), width))
	return w.cat.T("options.heading"), b.String(), w.cat.T("hint.select")
}

func (w *Wizard) summaryScreen(width int) (string, string, string) {
	rows := [][2]string{
		{w.cat.T("nodes.server"), w.cfg.Server},
		{w.cat.T("nodes.agents"), strings.Join(w.cfg.Agents, ", ")},
		{w.cat.T("nodes.user"), w.cfg.SSHUser + ":" + w.cfg.SSHPort},
		{w.cat.T("nodes.registration"), w.cfg.Registration},
		{w.cat.T("nodes.version"), w.cfg.Version},
		{w.cat.T("nodes.domain"), w.cfg.Domain},
		{w.cat.T("profile.heading"), w.cfg.Profile},
		{w.cat.T("options.dataplane"), w.cfg.Dataplane},
		{w.cat.T("options.storage"), w.cfg.Storage},
	}

	var b strings.Builder
	b.WriteString(w.dim(w.cat.T("summary.help"), width) + "\n\n")
	for _, r := range rows {
		b.WriteString("  " + w.theme.Dim.Render(padCells(r[0], 16)) +
			w.theme.Body.Render(truncCells(r[1], width-20)) + "\n")
	}
	if n := len(w.failures); n > 0 {
		b.WriteString("\n" + w.theme.Err.Render(
			fmt.Sprintf("%s %s (%d)", w.glyphs.Failed, w.cat.T("summary.warnings"), n)))
	}

	// The document is built and validated here rather than at install time, so
	// a malformed field is reported while the operator is still in front of the
	// screen that produced it.
	b.WriteString("\n")
	if problems := w.problems(); len(problems) > 0 {
		b.WriteString(w.theme.Err.Render(w.glyphs.Failed+" "+w.cat.T("summary.invalid")) + "\n")
		// Each line says which screen fixes it. A message that only states what
		// is wrong leaves the operator pressing Back until they find it.
		for _, pr := range problems {
			label := "[" + w.cat.T(stepKeys[pr.Step]) + "] "
			b.WriteString("  " + w.theme.Body.Render(label) + "\n")
			for _, line := range strings.Split(wrapCells(pr.Text, width-6), "\n") {
				b.WriteString("    " + w.theme.Dim.Render(line) + "\n")
			}
		}
	} else {
		b.WriteString(w.theme.Accent.Render(w.glyphs.OK+" "+w.cat.T("summary.valid")) + "\n")
	}
	hint := "hint.install"
	if _, broken := w.firstProblemStep(); broken {
		hint = "hint.fix"
	}
	return w.cat.T("summary.heading"), b.String(), w.cat.T(hint)
}

// ---------------------------------------------------------------------------
// Progress and result
// ---------------------------------------------------------------------------

// progressScreen draws the phase list, the current step and the log tail —
// everything from the event stream.
func (w *Wizard) progressScreen(width int, kind string) (string, string, string) {
	var b strings.Builder

	done := 0
	for _, id := range w.order {
		if w.phases[id].status.Terminal() {
			done++
		}
	}
	b.WriteString(w.theme.Progress(done, max(len(w.order), 1), width, w.glyphs) + "\n\n")

	for _, id := range w.order {
		p := w.phases[id]

		// The marker still carries the verdict, so the screen survives a
		// monochrome console; the badge is what makes it scannable when colour
		// is there.
		mark := w.glyphs.Marker(p.status)
		if p.status == event.StatusRunning {
			mark = w.glyphs.Spin(w.tick)
		}

		kind := ""
		switch p.status {
		case event.StatusOK, event.StatusSkipped:
			kind = "ok"
		case event.StatusFailed, event.StatusBlocked:
			kind = "err"
		}

		name := w.theme.Dim.Render(padCells(id, 18))
		if p.status == event.StatusRunning {
			name = w.theme.Body.Render(padCells(id, 18))
		}
		b.WriteString(" " + mark + " " + name +
			w.theme.Badge2(w.cat.T("status."+string(p.status)), kind))

		if p.progress != nil && p.status == event.StatusRunning {
			b.WriteString(w.theme.Dim.Render(
				fmt.Sprintf("  %d/%d", p.progress.Done, p.progress.Total)))
		}
		if p.attempt > 1 {
			b.WriteString(w.theme.Err.Render(
				fmt.Sprintf("  %s %d/%d", w.cat.T("key.retry"), p.attempt, p.maxTries)))
		}
		if p.code != "" && (p.status == event.StatusFailed || p.status == event.StatusBlocked) {
			b.WriteString(w.theme.Err.Render("  " + p.code))
		}
		b.WriteString("\n")
	}

	if node := w.currentNode(); node != "" {
		b.WriteString("\n" + w.theme.Dim.Render(node) + "\n")
	}
	for _, e := range w.tailLogs(4) {
		mark := " "
		if e.Level == event.LevelWarn || e.Level == event.LevelError {
			mark = w.glyphs.Warn
		}
		b.WriteString(w.theme.Dim.Render(fmt.Sprintf(" %s %s  %s",
			mark, e.TS.Format("15:04:05"), truncCells(e.Detail, width-14))) + "\n")
	}

	heading := w.cat.T("install.heading")
	if kind == "preflight" {
		heading = w.cat.T("preflight.heading")
	}
	status := w.cat.T("hint.working")
	if !w.busy {
		status = w.cat.T("hint.finished")
	}
	return heading, b.String(), status
}

func (w *Wizard) doneScreen(width int) (string, string, string) {
	var b strings.Builder

	failed := w.workErr != nil || len(w.failures) > 0
	// An upgrade that says "installation complete" describes something that did
	// not happen, and the sentence an operator reads at the end is the one they
	// repeat to whoever asks what was done.
	ok, stopped := "done.ok", "done.failed"
	if w.mode == modeUpgrade {
		ok, stopped = "done.upgraded", "done.upgrade_failed"
	}
	if failed {
		b.WriteString(w.theme.Err.Render(w.glyphs.Failed+" "+w.cat.T(stopped)) + "\n")
	} else {
		b.WriteString(w.theme.Accent.Render(w.glyphs.OK+" "+w.cat.T(ok)) + "\n")
	}

	// What was built, where it went, and what to run next. A final screen that
	// says only "finished" leaves the operator to guess all three.
	b.WriteString("\n" + w.theme.Body.Render(w.cat.T("done.cluster")) + "\n")
	for _, r := range w.builtRows() {
		b.WriteString("  " + w.theme.Dim.Render(padCells(r[0], 16)) +
			w.theme.Body.Render(truncCells(r[1], max(width-20, 10))) + "\n")
	}

	b.WriteString("\n" + w.theme.Body.Render(w.cat.T("done.artifacts")) + "\n")
	for _, r := range w.artifactRows() {
		b.WriteString("  " + w.theme.Dim.Render(padCells(r[0], 16)) +
			w.theme.Body.Render(truncCells(r[1], max(width-20, 10))) + "\n")
	}

	if failed {
		b.WriteString("\n" + w.theme.Err.Render(w.cat.T("done.problems")) + "\n")
		for _, e := range w.failures {
			where := e.Phase
			if e.Node != "" {
				where += " " + e.Node
			}
			b.WriteString("  " + w.theme.Body.Render(padCells(e.Code, 10)) +
				w.theme.Dim.Render(padCells(truncCells(where, 22), 24)) +
				w.theme.Body.Render(truncCells(e.Detail, max(width-40, 10))) + "\n")
		}
		b.WriteString("\n" + w.dim(w.cat.T("done.resume"), width) + "\n")
		b.WriteString("  " + w.theme.Body.Render(w.resumeCommand()) + "\n")
	} else {
		b.WriteString("\n" + w.dim(w.cat.T("done.next"), width) + "\n")
		b.WriteString("  " + w.theme.Body.Render(w.attachCommand()) + "\n")
	}

	return w.cat.T("done.heading"), b.String(), w.cat.T("hint.close")
}

// builtRows is what the cluster actually ended up being, not what was asked
// for: the phase results are the record, and a downgrade would have changed
// them.
func (w *Wizard) builtRows() [][2]string {
	nodes := 1 + len(w.cfg.Agents)
	rows := [][2]string{
		{w.cat.T("profile.heading"), w.cfg.Profile},
		{w.cat.T("done.nodes"), fmt.Sprintf("%d (%s)", nodes, w.cfg.Server)},
		{w.cat.T("options.dataplane"), w.cfg.Dataplane},
		{w.cat.T("options.storage"), w.cfg.Storage},
		{w.cat.T("step.pki"), w.pkiSummary()},
		{w.cat.T("step.registry"), w.cfg.RegistryMode},
	}
	if d := w.elapsed(); d != "" {
		rows = append(rows, [2]string{w.cat.T("done.elapsed"), d})
	}
	return rows
}

func (w *Wizard) pkiSummary() string {
	if w.cfg.PKIMode == string(v1alpha1.PKINone) || w.cfg.PKIMode == "" {
		return w.cat.T("done.pki_none")
	}
	return w.cfg.PKIMode + " " + w.glyphs.Dot + " " + w.cfg.Domain
}

// artifactRows names every file the run produced. These are what an operator
// carries away, and what the handover document points at.
func (w *Wizard) artifactRows() [][2]string {
	dir := w.runDir
	if dir == "" {
		dir = w.runID
	}
	return [][2]string{
		{w.cat.T("done.rundir"), dir},
		{"cluster.yaml", filepath.Join(dir, "cluster.yaml")},
		{"events.jsonl", filepath.Join(dir, "events.jsonl")},
		{"state.json", filepath.Join(dir, "state.json")},
	}
}

// elapsed is the engine's own record, taken from the run events rather than a
// clock here, so it matches the event file an operator may be reading beside
// this screen.
func (w *Wizard) elapsed() string {
	if w.startedAt.IsZero() || w.finishedAt.IsZero() {
		return ""
	}
	d := w.finishedAt.Sub(w.startedAt).Round(time.Second)
	if d < 0 {
		return ""
	}
	return d.String()
}

func (w *Wizard) resumeCommand() string {
	return "platformctl apply -f cluster.yaml --resume " + w.runID
}

func (w *Wizard) attachCommand() string {
	return "platformctl attach --run " + w.runID
}

func (w *Wizard) currentNode() string {
	for _, id := range w.order {
		if p := w.phases[id]; p.status == event.StatusRunning && p.node != "" {
			return p.stepID + " " + w.glyphs.Dot + " " + p.node
		}
	}
	return ""
}

func (w *Wizard) tailLogs(n int) []event.Event {
	if len(w.logs) <= n {
		return w.logs
	}
	return w.logs[len(w.logs)-n:]
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func labelsOf(cs []choice) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.label
	}
	return out
}

func notesOf(cat *Catalogue, cs []choice) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = cat.T(c.note)
	}
	return out
}

func indexOf(cs []choice, id string) int {
	for i, c := range cs {
		if c.id == id {
			return i
		}
	}
	return -1
}

// dim renders help prose wrapped to the content width.
func (w *Wizard) dim(text string, width int) string {
	var out []string
	for _, line := range strings.Split(wrapCells(text, width), "\n") {
		out = append(out, w.theme.Dim.Render(line))
	}
	return strings.Join(out, "\n")
}

// pkiScreen asks whether to issue certificates before asking anything about
// how. At a first build the answer is often "not yet": the service domain has
// not been decided and DNS has not been delegated, and a placeholder there
// becomes a certificate for a name nobody serves.
func (w *Wizard) pkiScreen(width int) (string, string, string) {
	cur := w.cursor[StepPKI]
	var b strings.Builder

	b.WriteString(w.dim(w.cat.T("pki.help_mode"), width) + "\n\n")
	b.WriteString(w.theme.Radio(labelsOf(pkiModes), notesOf(w.cat, pkiModes),
		indexOf(pkiModes, w.cfg.PKIMode), cur, width, w.glyphs))

	if fs := w.fieldsFor(StepPKI); len(fs) > 0 {
		b.WriteString("\n")
		b.WriteString(w.theme.Fields(w.labels(StepPKI), w.maskedValues(StepPKI),
			w.fieldIndex(), w.editing, width, w.glyphs))
		if isCA(w.cfg.PKIMode) {
			b.WriteString("\n" + w.dim(
				w.glyphs.Warn+" "+w.cat.T("pki.note_rootkey")+" (PF-706)", width))
		}
	} else {
		// Nothing is being issued, so there is no account and no CA to
		// describe. Saying what happens instead beats an empty pane.
		b.WriteString("\n" + w.dim(w.cat.T("pki.note_later"), width))
	}

	hint := w.cat.T("hint.select")
	if w.editing {
		hint = w.cat.T("hint.editing")
	}
	return w.cat.T("pki.heading"), b.String(), hint
}

// registryScreen offers the source before the details, since three of the four
// need no details at all.
func (w *Wizard) registryScreen(width int) (string, string, string) {
	cur := w.cursor[StepRegistry]
	var b strings.Builder

	b.WriteString(w.dim(w.cat.T("reg.help"), width) + "\n\n")
	b.WriteString(w.theme.Radio(labelsOf(registryModes), notesOf(w.cat, registryModes),
		indexOf(registryModes, w.cfg.RegistryMode), cur, width, w.glyphs))

	if fs := w.fieldsFor(StepRegistry); len(fs) > 0 {
		b.WriteString("\n")
		b.WriteString(w.theme.Fields(w.labels(StepRegistry), w.maskedValues(StepRegistry),
			w.fieldIndex(), w.editing, width, w.glyphs))
	} else {
		b.WriteString("\n" + w.dim(w.cat.T("reg.note_none"), width))
	}

	hint := w.cat.T("hint.select")
	if w.editing {
		hint = w.cat.T("hint.editing")
	}
	return w.cat.T("reg.heading"), b.String(), hint
}

// exitButton is the bottom-left action: the one that ends things. It says
// "abort" while work is running and "quit" otherwise, because those are
// different promises.
func (w *Wizard) exitButton() *Button {
	if w.step == StepDone {
		return nil
	}
	label := w.cat.T("btn.quit")
	if w.busy {
		label = w.cat.T("btn.abort")
	}
	return &Button{Label: label}
}

// keyHints renders the footer as keycaps rather than prose. A key drawn as a
// cap is found without reading the sentence around it, which is the difference
// between a hint somebody uses and one they skim past.
func (w *Wizard) keyHints(context string) string {
	var pairs [][2]string

	switch {
	case w.step == StepMenu || w.step == StepRuns:
		// A list is moved through vertically and has no step list behind it.
		// Offering the install flow's keys here points at screens that are not
		// there.
		pairs = [][2]string{
			{w.capVert(), w.cat.T("cap.move")},
			{w.capEnter(), w.cat.T("cap.activate")},
		}
		if w.step == StepRuns {
			pairs = append(pairs, [2]string{"esc", w.cat.T("cap.menu")})
		}

	case w.editing:
		pairs = [][2]string{
			{w.capEnter(), w.cat.T("cap.nextfield")},
			{"esc", w.cat.T("cap.done")},
		}
	case w.focus == focusButtons:
		pairs = [][2]string{
			{w.capHoriz(), w.cat.T("cap.move")},
			{w.capEnter(), w.cat.T("cap.activate")},
			{"tab", w.cat.T("cap.content")},
		}
	case w.contentLen() == 0:
		// Nothing to choose on this screen, so the only keys that mean
		// anything are the ones on the buttons.
		pairs = [][2]string{
			{w.capHoriz(), w.cat.T("cap.move")},
			{w.capEnter(), w.cat.T("cap.activate")},
		}
	case w.cursorIsField():
		pairs = [][2]string{
			{w.capVert(), w.cat.T("cap.move")},
			{w.capEnter(), w.cat.T("cap.edit")},
			{"tab", w.cat.T("cap.buttons")},
		}
	default:
		pairs = [][2]string{
			{w.capVert(), w.cat.T("cap.move")},
			{"space", w.cat.T("cap.select")},
			{w.capEnter(), w.cat.T("cap.continue")},
		}
	}
	// The rail toggle is only offered where there is a rail. Advertising a key
	// that does nothing is how a footer stops being read.
	if w.inInstallFlow() {
		pairs = append(pairs, [2]string{"s", w.cat.T("hint.toggle_steps")})
	}
	return w.theme.Keys(pairs, w.glyphs)
}

// The keycap symbols are glyphs like any other: a terminal that cannot draw a
// box character cannot draw an arrow either.
func (w *Wizard) capEnter() string {
	if w.ascii {
		return "enter"
	}
	return "↵"
}

func (w *Wizard) capVert() string {
	if w.ascii {
		return "up/dn"
	}
	return "↑↓"
}

func (w *Wizard) capHoriz() string {
	if w.ascii {
		return "lt/rt"
	}
	return "←→"
}

// withTopology appends the diagram when there is room for it.
//
// It is an aid, not the content: on a short terminal the fields an operator is
// filling in matter more than a picture of what they have filled in so far.
func (w *Wizard) withTopology(body string, width int) string {
	need := w.topologyLines()
	if need == 0 {
		return body
	}
	// Title, blank, footer rules and the button row, plus what the body
	// already occupies.
	used := 6 + lines(body)
	if w.height-used < need {
		return body
	}
	return body + "\n" + w.renderTopology(width)
}
