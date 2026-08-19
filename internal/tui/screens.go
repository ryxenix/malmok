package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"platform.ryxen.dev/platformctl/api/v1alpha1"
	"platform.ryxen.dev/platformctl/internal/event"
	"platform.ryxen.dev/platformctl/internal/exec"
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

// The OS family. `auto` is the honest default: PF-101 reads /etc/os-release,
// and a document that states a family the node does not have is a document that
// fails a check it did not need to run.
var osFamilies = []choice{
	{"auto", "auto", "os.auto"},
	{"ubuntu", "ubuntu", "os.ubuntu"},
	{"rocky", "rocky", "os.rocky"},
}

// How pod traffic crosses between nodes.
var routingModes = []choice{
	{"overlay", "overlay", "routing.overlay"},
	{"native", "native", "routing.native"},
}

// The dataplane to fall back to when the nodes cannot run the chosen one.
var fallbacks = []choice{
	{"canal-traefik", "canal-traefik", "dp.canal"},
	{"cilium-traefik", "cilium-traefik", "dp.cilium_traefik"},
	{"", "none", "fallback.none"},
}

// Where the nodes sit relative to the internet. It decides whether a proxy is
// asked for and whether images can be pulled at all, and it used to come only
// from the profile -- so "homelab behind a proxy" was a document nobody could
// produce from the wizard.
var networkModes = []choice{
	{"online", "online", "netmode.online"},
	{"proxy", "proxy", "netmode.proxy"},
	{"airgap", "airgap", "netmode.airgap"},
}

// What happens when the nodes cannot run what was asked for.
var downgradePolicies = []choice{
	{"confirm", "confirm", "dgpolicy.confirm"},
	{"auto", "auto", "dgpolicy.auto"},
	{"forbid", "forbid", "dgpolicy.forbid"},
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
		Title:     w.cat.T("app.title"),
		Crumb:     w.crumb(),
		Info:      w.frameInfo(),
		Context:   context,
		Truecolor: w.truecolor,
		Rail:      w.rail(),
		Buttons:   w.buttons(),
		Focused:   w.btn,
		Exit:      w.exitButton(),
		HideRail:  w.hideRail,
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
	case StepPrefs:
		f.Heading, f.Body, f.Status = w.prefsScreen(body)
	case StepWhere:
		f.Heading, f.Body, f.Status = w.whereScreen(body)
	case StepNodes:
		f.Heading, f.Body, f.Status = w.nodesScreen(body)
		f.Body = w.withTopology(f.Body, body)
	case StepNetwork:
		f.Heading, f.Body, f.Status = w.networkScreen(body)
	case StepRegistry:
		f.Heading, f.Body, f.Status = w.registryScreen(body)
	case StepPKI:
		f.Heading, f.Body, f.Status = w.pkiScreen(body)
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
	width := w.width - gutter*2
	if w.width >= minChromeW {
		width = w.width - railW(w.width) - 3 - gutter
	}
	// The whole pane, no cap: the layout fills at fixed proportions, and a
	// window's spare width goes into the content rather than beside it.
	return width
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
	case StepPrefs:
		// Back goes to the menu rather than nowhere: an operator who chose the
		// wrong entry has to be able to leave without quitting.
		return []Button{back, {Label: w.cat.T("btn.done"), Primary: true}}
	case StepWhere, StepNodes, StepNetwork, StepOptions, StepRegistry, StepPKI:
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
			return []Button{{Label: w.cat.T("btn.menu"), Primary: true}}
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
			// Nothing in the stream means nothing ran, so the checks did not
			// find a problem -- something stopped them from starting. Retrying
			// repeats it exactly, and the way forward is the screen that holds
			// whatever was missing, so Back is what Enter should do.
			if len(w.order) == 0 {
				return []Button{{Label: w.cat.T("btn.back"), Primary: true},
					{Label: w.cat.T("btn.check")}}
			}
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
		// The final screens return to the menu. The tool used to quit here --
		// left over from when the installer was the whole program -- which
		// threw the operator out at exactly the moment they want the run list,
		// the settings, or a second cluster. Quit stays on the bottom-left
		// exit, where leaving is a choice rather than the only way forward.
		return []Button{{Label: w.cat.T("btn.menu"), Primary: true}}
	}
}

// ---------------------------------------------------------------------------
// Input screens
// ---------------------------------------------------------------------------

// formScreen renders any step whose content is a list of editable fields.
//
// One function for all of them: a screen that exists because the validator
// asks for a value should not also be a place where the layout can differ.
func (w *Wizard) formScreen(step Step, headingKey, helpKey string, width int) (string, string, string) {
	cur := w.cursor[step]
	body := w.inlineHelp(w.cat.T(helpKey), width) +
		w.theme.Fields(w.labels(step), w.maskedValues(step), cur, w.editing, width, w.glyphs)

	if h := w.inlineHint(int(step), cur); h != "" {
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

func (w *Wizard) optionsScreen(width int) (string, string, string) {
	var b strings.Builder
	b.WriteString(w.inlineHelp(w.cat.T("options.help"), width))

	cur := w.cursor[StepOptions]

	b.WriteString(w.theme.Section(w.cat.T("options.dataplane"), width, w.glyphs) + "\n")
	b.WriteString(w.theme.Radio(labelsOf(dataplanes), notesOf(w.cat, dataplanes),
		indexOf(dataplanes, w.cfg.Dataplane), cur, width, w.glyphs))

	b.WriteString("\n" + w.theme.Section(w.cat.T("options.storage"), width, w.glyphs) + "\n")
	b.WriteString(w.theme.Radio(labelsOf(storages), notesOf(w.cat, storages),
		indexOf(storages, w.cfg.Storage), cur-len(dataplanes), width, w.glyphs))

	// What to do when the nodes cannot run what was asked for. It belongs
	// beside the dataplane because that is what it is usually about, and it was
	// the profile's alone -- so an air-gapped build could not be told to refuse
	// a downgrade rather than confirm one.
	b.WriteString("\n" + w.theme.Section(w.cat.T("options.downgrade"), width, w.glyphs) + "\n")
	b.WriteString(w.theme.Radio(labelsOf(downgradePolicies), notesOf(w.cat, downgradePolicies),
		indexOf(downgradePolicies, w.cfg.DowngradePolicy),
		cur-len(dataplanes)-len(storages), width, w.glyphs))

	b.WriteString("\n" + w.theme.Section(w.cat.T("options.fallback"), width, w.glyphs) + "\n")
	b.WriteString(w.theme.Radio(labelsOf(fallbacks), notesOf(w.cat, fallbacks),
		indexOf(fallbacks, w.cfg.Fallback),
		cur-len(dataplanes)-len(storages)-len(downgradePolicies), width, w.glyphs))

	if fs := w.fieldsFor(StepOptions); len(fs) > 0 {
		b.WriteString("\n")
		b.WriteString(w.theme.Fields(w.labels(StepOptions), w.maskedValues(StepOptions),
			w.fieldIndex(), w.editing, width, w.glyphs))
	}

	if note := w.inlineHelp(w.cat.T("note.options"), width); note != "" {
		b.WriteString("\n" + strings.TrimSuffix(note, "\n\n"))
	}
	return w.cat.T("options.heading"), b.String(), w.cat.T("hint.select")
}

func (w *Wizard) summaryScreen(width int) (string, string, string) {
	server := w.cat.T("nodes.server")
	if w.cfg.Local {
		server = w.cat.T("nodes.thismachine")
	}
	rows := [][2]string{
		{server, w.cfg.Server},
		{w.cat.T("nodes.agents"), strings.Join(w.cfg.Agents, ", ")},
	}
	// The same rule the node screen follows: credentials appear only where
	// something is dialled. A summary that listed an SSH user for a build that
	// opens no connection would describe a step that is not going to happen.
	if w.needsSSH() {
		rows = append(rows, [2]string{w.cat.T("nodes.user"), w.cfg.SSHUser + ":" + w.cfg.SSHPort})
	}
	rows = append(rows,
		[2]string{w.cat.T("nodes.registration"), w.cfg.Registration},
		[2]string{w.cat.T("nodes.version"), w.cfg.Version},
		[2]string{w.cat.T("nodes.domain"), w.cfg.Domain},
		// The rail's label, not the screen's title. A heading reused as a row
		// label reads as a heading: in Korean this row said "프로파일 선택" --
		// "choose a profile" -- beside the profile that had been chosen.
		// What the composition turned out to be, rather than what was picked
		// before composing anything. `custom` is a true statement: Tier-3 means
		// this combination is not one CI exercises.
		[2]string{w.cat.T("summary.profile"), string(w.cfg.MatchedProfile())},
		[2]string{w.cat.T("options.dataplane"), w.cfg.Dataplane},
		[2]string{w.cat.T("options.storage"), w.cfg.Storage},
	)

	var b strings.Builder
	b.WriteString(w.inlineHelp(w.cat.T("summary.help"), width))
	// Grouped the way the screens asked: what the cluster is, then what runs
	// on it. A summary that lists nine rows in one block makes the reader do
	// the grouping the screen already knows.
	writeRows := func(section string, group [][2]string) {
		b.WriteString(w.theme.Section(section, width, w.glyphs) + "\n")
		for _, r := range group {
			if r[1] == "" {
				continue
			}
			b.WriteString("  " + w.theme.Dim.Render(padCells(r[0], 16)) +
				w.theme.Body.Render(truncCells(r[1], width-20)) + "\n")
		}
		b.WriteString("\n")
	}
	split := len(rows) - 3
	writeRows(w.cat.T("summary.cluster"), rows[:split])
	writeRows(w.cat.T("summary.platform"), rows[split:])
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

	// Work that failed before the engine emitted anything has nothing in the
	// stream to draw, and the screen was a bar at 0% with no explanation. That
	// is the shape of every credential and connection failure -- the most
	// common way a first run stops -- and the operator was left pressing a
	// retry button that failed the same way each time.
	//
	// Rendered from the return value rather than from an event because the
	// failure happened before any phase started: there is no phase to attach it
	// to and no run for the engine to have written it into.
	if w.workErr != nil && !w.busy {
		b.WriteString(w.theme.Err.Render(w.glyphs.Failed+" "+w.cat.T("progress.stopped")) + "\n")
		b.WriteString(w.dim(wrapCells(w.workErr.Error(), width), width) + "\n\n")
	}

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
	// The tail under its own header, so the eye can tell where the verdicts
	// end and the narration begins without reading either.
	if tail := w.tailLogs(4); len(tail) > 0 {
		b.WriteString("\n" + w.theme.Section(w.cat.T("progress.logs"), width, w.glyphs) + "\n")
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
	switch kind {
	case "preflight":
		heading = w.cat.T("preflight.heading")
	case "upgrade":
		// The screen an operator watches for twenty minutes should name the
		// thing that is happening; "Installing" over an upgrade does not.
		heading = w.cat.T("upgrade.heading")
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
	b.WriteString("\n" + w.theme.Section(w.cat.T("done.cluster"), width, w.glyphs) + "\n")
	for _, r := range w.builtRows() {
		b.WriteString("  " + w.theme.Dim.Render(padCells(r[0], 16)) +
			w.theme.Body.Render(truncCells(r[1], max(width-20, 10))) + "\n")
	}

	b.WriteString("\n" + w.theme.Section(w.cat.T("done.artifacts"), width, w.glyphs) + "\n")
	for _, r := range w.artifactRows() {
		b.WriteString("  " + w.theme.Dim.Render(padCells(r[0], 16)) +
			w.theme.Body.Render(truncCells(r[1], max(width-20, 10))) + "\n")
	}

	if failed {
		b.WriteString("\n" + w.theme.Err.Render(w.glyphs.SectionTick+" "+w.cat.T("done.problems")) + "\n")
		for _, e := range w.failures {
			// The node first, because it is the subject and the phase is the
			// stage. The column is fixed, so whichever comes second is what a
			// long value loses -- and on the screen that says what failed, the
			// machine it happened on is what an operator acts on. This read
			// "l1-bootstrap 10.10...." until the rail grew a step and took the
			// address with it.
			where := e.Node
			if where == "" {
				where = e.Phase
			} else if e.Phase != "" {
				where += "  " + e.Phase
			}
			// Twenty-four cells fits an IPv4 address and a phase id together,
			// which is the pair this column exists to carry. The two extra
			// cells come out of the detail, the least identifying part of the
			// line and one that was already being cut.
			b.WriteString("  " + w.theme.Body.Render(padCells(e.Code, 10)) +
				w.theme.Dim.Render(padCells(truncCells(where, 24), 26)) +
				w.theme.Body.Render(truncCells(e.Detail, max(width-42, 10))) + "\n")
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
		{w.cat.T("summary.profile"), string(w.cfg.MatchedProfile())},
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

	b.WriteString(w.inlineHelp(w.cat.T("pki.help_mode"), width))
	b.WriteString(w.theme.Radio(labelsOf(pkiModes), notesOf(w.cat, pkiModes),
		indexOf(pkiModes, w.cfg.PKIMode), cur, width, w.glyphs))

	// Trust distribution, for the one mode where l2-pki reads the answer. A
	// cluster built without it has a CA the nodes trust and the pods do not,
	// and the failure is an opaque x509 error from inside a container.
	if w.pkiExtraRows() > 0 {
		b.WriteString("\n" + w.theme.Section(w.cat.T("pki.trust"), width, w.glyphs) + "\n")
		b.WriteString(w.theme.Radio(
			[]string{w.cat.T("pki.trust.bundle"), w.cat.T("pki.trust.nodes")},
			[]string{w.cat.T("pki.trust.bundle.note"), w.cat.T("pki.trust.nodes.note")},
			boolIndex(w.cfg.TrustBundle), cur-len(pkiModes), width, w.glyphs))
	}

	if fs := w.fieldsFor(StepPKI); len(fs) > 0 {
		b.WriteString("\n")
		b.WriteString(w.theme.Fields(w.labels(StepPKI), w.maskedValues(StepPKI),
			w.fieldIndex(), w.editing, width, w.glyphs))
		if isCA(w.cfg.PKIMode) && w.frameInfo() == "" {
			b.WriteString("\n" + w.dim(
				w.glyphs.Warn+" "+w.cat.T("pki.note_rootkey")+" (PF-706)", width))
		}
	} else if w.frameInfo() == "" {
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

	b.WriteString(w.inlineHelp(w.cat.T("reg.help"), width))
	b.WriteString(w.theme.Radio(labelsOf(registryModes), notesOf(w.cat, registryModes),
		indexOf(registryModes, w.cfg.RegistryMode), cur, width, w.glyphs))

	// Whether to accept a certificate the registry cannot prove. Only for a
	// registry this document points at, and two rows rather than a toggle: a
	// decision a security review will ask about should show both answers and
	// which was taken.
	if w.registryHasAddress() {
		b.WriteString("\n" + w.theme.Section(w.cat.T("reg.tls"), width, w.glyphs) + "\n")
		b.WriteString(w.theme.Radio(
			[]string{w.cat.T("reg.tls.verify"), w.cat.T("reg.tls.insecure")},
			[]string{w.cat.T("reg.tls.verify.note"), w.cat.T("reg.tls.insecure.note")},
			boolIndex(!w.cfg.RegistryInsecure), cur-len(registryModes), width, w.glyphs))
	}

	if fs := w.fieldsFor(StepRegistry); len(fs) > 0 {
		b.WriteString("\n")
		b.WriteString(w.theme.Fields(w.labels(StepRegistry), w.maskedValues(StepRegistry),
			w.fieldIndex(), w.editing, width, w.glyphs))
		if h := w.inlineHint(int(StepRegistry), w.fieldIndex()); h != "" {
			b.WriteString("\n" + w.dim(w.glyphs.Dot+" "+h, width))
		}
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

// whereScreen asks which machine the cluster is being built on.
//
// First, because it is the first thing an operator knows and it decides what
// every screen after it has to ask. The wizard used to skip it and open on an
// address field, which meant somebody installing on the machine in front of
// them had to read their own IP off `ip addr` and type it back -- a value the
// tool was sitting on the whole time.
func (w *Wizard) whereScreen(width int) (string, string, string) {
	here := strings.Join(exec.LocalIPv4s(), ", ")
	if here == "" {
		here = w.cat.T("where.noaddress")
	}

	chosen := 1
	if w.cfg.Local {
		chosen = 0
	}
	body := w.inlineHelp(w.cat.T("where.help"), width) +
		w.theme.Radio(
			[]string{w.cat.T("where.here"), w.cat.T("where.remote")},
			[]string{here, w.cat.T("where.remote.note")},
			chosen, w.cursor[StepWhere], width, w.glyphs)

	return w.cat.T("where.heading"), body, w.cat.T("hint.select")
}

// nodesScreen is the form, with the address chooser above it when the machine
// being built on is this one.
//
// A list rather than a field: the machine reports its own addresses, and a
// multi-homed host is a real case where which one the cluster advertises
// matters (PF-609) and typing is not what should decide it.
func (w *Wizard) nodesScreen(width int) (string, string, string) {
	var b strings.Builder
	b.WriteString(w.inlineHelp(w.cat.T(w.nodesHelp()), width))

	cur := w.cursor[StepNodes]

	// The OS family belongs with the machines it describes. `auto` is the
	// default and the honest one: PF-101 reads /etc/os-release, and a document
	// that states a family the node does not have fails a check it need not
	// have run.
	b.WriteString(w.theme.Section(w.cat.T("nodes.os"), width, w.glyphs) + "\n")
	b.WriteString(w.theme.Radio(labelsOf(osFamilies), notesOf(w.cat, osFamilies),
		indexOf(osFamilies, w.cfg.OSFamily), cur, width, w.glyphs))
	b.WriteString("\n")

	// The fields come before the chooser. They are the work; the chooser is
	// one confirmation of an address the machine already knows -- and on a
	// multi-homed box it can be a long list, which must not stand between the
	// operator and the work. Found live on a box whose docker bridges put
	// thirty rows above the agent field.
	if fs := w.fieldsFor(StepNodes); len(fs) > 0 {
		vals := w.maskedValues(StepNodes)
		// The channel tag: Space flips the version between the channel
		// server's two answers, and without a word beside the value the two
		// version strings are just strings -- nobody memorises which one is
		// stable. Display only, never part of the document, and absent on a
		// hand-typed version because calling that a channel would be false.
		for i, f := range fs {
			if f.labelKey != "nodes.version" || (w.editing && w.fieldIndex() == i) {
				continue
			}
			if tag := w.channelTag(w.cfg.Version); tag != "" {
				vals[i] += "  " + w.glyphs.Dot + " " + tag
			}
		}
		b.WriteString(w.theme.Fields(w.labels(StepNodes), vals,
			w.fieldIndex(), w.editing, width, w.glyphs))
	}
	if h := w.inlineHint(int(StepNodes), w.fieldIndex()); h != "" {
		b.WriteString("\n" + w.dim(w.glyphs.Dot+" "+h, width))
	}

	if w.cfg.Local {
		addrs := exec.LocalIPv4s()
		cur -= len(osFamilies) + len(w.fieldsFor(StepNodes))
		if len(addrs) == 0 {
			// Nothing to choose from and nothing to pretend about. The address
			// is what the cluster advertises, so a machine with none is a
			// machine this cannot be built on until it has one.
			b.WriteString("\n" + w.theme.Err.Render(w.glyphs.Failed+" "+
				wrapCells(w.cat.T("where.noaddress"), width)) + "\n")
		} else {
			b.WriteString("\n" + w.theme.Section(w.cat.T("nodes.thismachine"), width, w.glyphs) + "\n")
			notes := make([]string, len(addrs))
			if len(addrs) > 1 {
				// Only worth saying where there is a decision: on a multi-homed
				// host this is the address the rest of the cluster reaches.
				for i := range notes {
					notes[i] = w.cat.T("nodes.advertised")
				}
			}
			b.WriteString(w.theme.Radio(addrs, notes, indexOfString(addrs, w.cfg.Server),
				cur, width, w.glyphs))
		}
	}

	hint := "hint.edit"
	if w.editing {
		hint = "hint.editing"
	}
	return w.cat.T("nodes.heading"), b.String(), w.cat.T(hint)
}

// indexOfString is the position of a value in a list, or -1.
func indexOfString(list []string, want string) int {
	for i, s := range list {
		if s == want {
			return i
		}
	}
	return -1
}

// networkScreen asks where the nodes sit before asking for the addresses that
// only matter once that is known.
//
// The mode used to come from the profile and from nowhere else, so a
// combination the profiles did not happen to contain -- a homelab behind a
// proxy, an air-gapped site on the full dataplane -- could not be produced from
// the wizard at all. A profile is a starting point; every field it fills has to
// stay reachable.
func (w *Wizard) networkScreen(width int) (string, string, string) {
	var b strings.Builder
	b.WriteString(w.inlineHelp(w.cat.T("net.help"), width))

	cur := w.cursor[StepNetwork]

	b.WriteString(w.theme.Section(w.cat.T("net.mode"), width, w.glyphs) + "\n")
	b.WriteString(w.theme.Radio(labelsOf(networkModes), notesOf(w.cat, networkModes),
		indexOf(networkModes, string(w.networkMode())), cur, width, w.glyphs))

	b.WriteString("\n" + w.theme.Section(w.cat.T("net.encrypt"), width, w.glyphs) + "\n")
	b.WriteString(w.theme.Radio(
		[]string{w.cat.T("net.encrypt.on"), w.cat.T("net.encrypt.off")},
		[]string{w.cat.T("net.encrypt.on.note"), w.cat.T("net.encrypt.off.note")},
		boolIndex(w.cfg.Encrypt), cur-len(networkModes), width, w.glyphs))

	b.WriteString("\n" + w.theme.Section(w.cat.T("net.routing"), width, w.glyphs) + "\n")
	b.WriteString(w.theme.Radio(labelsOf(routingModes), notesOf(w.cat, routingModes),
		indexOf(routingModes, w.cfg.Routing), cur-len(networkModes)-2, width, w.glyphs))

	if fs := w.fieldsFor(StepNetwork); len(fs) > 0 {
		b.WriteString("\n")
		b.WriteString(w.theme.Fields(w.labels(StepNetwork), w.maskedValues(StepNetwork),
			w.fieldIndex(), w.editing, width, w.glyphs))
	}
	if h := w.inlineHint(int(StepNetwork), w.fieldIndex()); h != "" {
		b.WriteString("\n" + w.dim(w.glyphs.Dot+" "+h, width))
	}

	hint := "hint.select"
	if w.editing {
		hint = "hint.editing"
	}
	return w.cat.T("net.heading"), b.String(), w.cat.T(hint)
}

// boolIndex maps a yes/no onto the two rows of a radio.
func boolIndex(on bool) int {
	if on {
		return 0
	}
	return 1
}

// crumb is where the operator is, as a path beside the product chip.
//
// The screen's own heading repeats below it, and that repetition is the point:
// the crumb is for the glance ("which flow, which screen"), the heading for
// the read. The menu gets none -- it is where paths start.
func (w *Wizard) crumb() string {
	var flow string
	switch {
	case w.step == StepMenu:
		return ""
	case w.step == StepRuns:
		return w.cat.T("menu.logs")
	case w.step == StepPrefs:
		return w.cat.T("menu.settings")
	case w.mode == modeSettings:
		flow = w.cat.T("menu.document")
	case w.mode == modeUpgrade:
		flow = w.cat.T("menu.upgrade")
	default:
		flow = w.cat.T("menu.install")
	}
	if key, ok := stepKeys[w.step]; ok {
		return flow + " " + w.glyphs.Running + " " + w.cat.T(key)
	}
	return flow
}

// frameInfo fills the explanation pane when the window can afford one.
//
// Empty otherwise, and empty for the progress screens: while phases run, the
// pane's width is better spent on the log tail, and the running step already
// explains itself.
func (w *Wizard) frameInfo() string {
	if !w.infoActive() {
		return ""
	}
	switch w.step {
	// The menu is the front door: a wordmark, six choices and an empty
	// explanation column beside them reads as clutter, not help. The progress
	// screens have the log tail, and the done screen is its own summary.
	case StepMenu, StepPreflight, StepInstall, StepUpgrade, StepDone:
		return ""
	}
	return w.infoPane()
}

// inlineHelp renders a screen's help text where there is no pane to hold it.
func (w *Wizard) inlineHelp(text string, width int) string {
	if w.frameInfo() != "" {
		return ""
	}
	return w.dim(wrapCells(text, width), width) + "\n\n"
}

// inlineHint is the focused field's hint where there is no pane to hold it.
func (w *Wizard) inlineHint(step, i int) string {
	if w.frameInfo() != "" {
		return ""
	}
	return w.fieldHint(step, i)
}
