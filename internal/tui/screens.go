package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"platform.ryxen.dev/platformctl/internal/event"
)

// One function per step, each returning the Frame the chrome draws. Keeping the
// screens here and the state machine in wizard.go means a layout change never
// touches the transitions.

// choice is a selectable option with a one-line note.
type choice struct {
	id    string
	label string
	note  string // catalogue key
}

// The Tier-1 profiles of docs/00-architecture.md ADR-003, in the order the
// document lists them.
var profiles = []choice{
	{"homelab", "homelab", "profile.homelab"},
	{"company-prod", "company-prod", "profile.company"},
	{"onprem-dmz", "onprem-dmz", "profile.dmz"},
	{"airgap-ubuntu", "airgap-ubuntu", "profile.airgap_ubuntu"},
	{"airgap-rocky", "airgap-rocky", "profile.airgap_rocky"},
	{"airgap-conservative", "airgap-conservative", "profile.airgap_cons"},
}

// The dataplane presets of ADR-004. CNI, Gateway and LB IP source are one
// atomic choice, which is why they are presets rather than three questions.
var dataplanes = []choice{
	{"cilium-gw", "cilium-gw", "dp.cilium_gw"},
	{"cilium-traefik", "cilium-traefik", "dp.cilium_traefik"},
	{"canal-traefik", "canal-traefik", "dp.canal"},
}

var storages = []choice{
	{"local-path", "local-path", "st.local"},
	{"longhorn", "longhorn", "st.longhorn"},
	{"nfs", "nfs", "st.nfs"},
}

// View renders the current step.
func (w *Wizard) View() tea.View {
	f := Frame{
		Title:   w.cat.T("app.title"),
		Context: fmt.Sprintf("%d/%d", int(w.step)+1, len(stepKeys)),
		Rail:    w.rail(),
		Buttons: w.buttons(),
		Focused: w.btn,
	}
	if w.focus == focusButtons {
		f.Focused = w.btn
	} else {
		f.Focused = -1
	}

	body := w.contentWidth()
	switch w.step {
	case StepLang:
		f.Heading, f.Body, f.Status = w.langScreen(body)
	case StepNodes:
		f.Heading, f.Body, f.Status = w.nodesScreen(body)
	case StepProfile:
		f.Heading, f.Body, f.Status = w.profileScreen(body)
	case StepOptions:
		f.Heading, f.Body, f.Status = w.optionsScreen(body)
	case StepPreflight:
		f.Heading, f.Body, f.Status = w.progressScreen(body, "preflight")
	case StepSummary:
		f.Heading, f.Body, f.Status = w.summaryScreen(body)
	case StepInstall:
		f.Heading, f.Body, f.Status = w.progressScreen(body, "install")
	case StepDone:
		f.Heading, f.Body, f.Status = w.doneScreen(body)
	}

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
	out := make([]RailItem, len(stepKeys))
	for i, key := range stepKeys {
		st := RailFuture
		switch {
		case Step(i) < w.step:
			st = RailDone
		case Step(i) == w.step:
			st = RailCurrent
		}
		out[i] = RailItem{Label: w.cat.T(key), State: st}
	}
	return out
}

// buttons are the actions available on the current step. Back is absent where
// going back would mean undoing work already done on a node.
func (w *Wizard) buttons() []Button {
	back := Button{Label: w.cat.T("btn.back")}
	quit := Button{Label: w.cat.T("btn.quit")}

	switch w.step {
	case StepLang:
		return []Button{quit, {Label: w.cat.T("btn.next"), Primary: true}}
	case StepNodes, StepProfile, StepOptions:
		return []Button{back, {Label: w.cat.T("btn.next"), Primary: true}}
	case StepPreflight:
		if w.busy {
			return []Button{{Label: w.cat.T("btn.abort")}}
		}
		if w.workErr != nil {
			return []Button{back, {Label: w.cat.T("btn.check"), Primary: true}}
		}
		return []Button{back, {Label: w.cat.T("btn.next"), Primary: true}}
	case StepSummary:
		return []Button{back, {Label: w.cat.T("btn.install"), Primary: true}}
	case StepInstall:
		if w.busy {
			return []Button{{Label: w.cat.T("btn.logs")}, {Label: w.cat.T("btn.abort")}}
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

func (w *Wizard) nodesScreen(width int) (string, string, string) {
	labels := []string{
		w.cat.T("nodes.server"), w.cat.T("nodes.agents"),
		w.cat.T("nodes.user"), w.cat.T("nodes.port"),
	}
	body := w.dim(w.cat.T("nodes.help"), width) + "\n\n" +
		w.theme.Fields(labels, w.nodeValues(), w.cursor[StepNodes], w.editing, width, w.glyphs)

	hint := w.cat.T("hint.edit")
	if w.editing {
		hint = w.cat.T("hint.editing")
	}
	return w.cat.T("nodes.heading"), body, hint
}

func (w *Wizard) profileScreen(width int) (string, string, string) {
	labels, notes := make([]string, len(profiles)), make([]string, len(profiles))
	chosen := 0
	for i, p := range profiles {
		labels[i], notes[i] = p.label, w.cat.T(p.note)
		if p.id == w.cfg.Profile {
			chosen = i
		}
	}
	body := w.dim(w.cat.T("profile.help"), width) + "\n\n" +
		w.theme.Radio(labels, notes, chosen, w.cursor[StepProfile], width, w.glyphs) +
		"\n" + w.dim(w.glyphs.Warn+" "+w.cat.T("note.unwired"), width)
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

	b.WriteString("\n" + w.dim(w.glyphs.Warn+" "+w.cat.T("note.unwired"), width))
	return w.cat.T("options.heading"), b.String(), w.cat.T("hint.select")
}

func (w *Wizard) summaryScreen(width int) (string, string, string) {
	rows := [][2]string{
		{w.cat.T("nodes.server"), w.cfg.Server},
		{w.cat.T("nodes.agents"), strings.Join(w.cfg.Agents, ", ")},
		{w.cat.T("nodes.user"), w.cfg.SSHUser + ":" + w.cfg.SSHPort},
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
	return w.cat.T("summary.heading"), b.String(), w.cat.T("hint.install")
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
		line := w.glyphs.Marker(p.status) + " " + padCells(id, 18) +
			w.cat.T("status."+string(p.status))
		if p.progress != nil && p.status == event.StatusRunning {
			line += fmt.Sprintf("  %d/%d", p.progress.Done, p.progress.Total)
		}
		if p.attempt > 1 {
			line += fmt.Sprintf("  %s %d/%d", w.cat.T("key.retry"), p.attempt, p.maxTries)
		}
		if p.code != "" && !p.status.Terminal() {
			line += "  " + p.code
		}
		switch p.status {
		case event.StatusOK, event.StatusSkipped:
			b.WriteString(w.theme.Dim.Render(line))
		case event.StatusFailed, event.StatusBlocked:
			b.WriteString(w.theme.Err.Render(line + "  " + p.code))
		case event.StatusRunning:
			b.WriteString(w.theme.Body.Render(line))
		default:
			b.WriteString(w.theme.Dim.Render(line))
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

	if w.workErr != nil || len(w.failures) > 0 {
		b.WriteString(w.theme.Err.Render(w.glyphs.Failed+" "+w.cat.T("done.failed")) + "\n\n")
		for _, e := range w.failures {
			where := e.Phase
			if e.Node != "" {
				where += " " + e.Node
			}
			b.WriteString("  " + w.theme.Body.Render(padCells(e.Code, 10)) +
				w.theme.Dim.Render(padCells(truncCells(where, 24), 26)) +
				w.theme.Body.Render(truncCells(e.Detail, max(width-42, 10))) + "\n")
		}
		b.WriteString("\n" + w.dim(w.cat.T("done.resume"), width))
	} else {
		b.WriteString(w.theme.Accent.Render(w.glyphs.OK+" "+w.cat.T("done.ok")) + "\n\n")
		b.WriteString(w.dim(w.cat.T("done.artifacts"), width) + "\n")
		b.WriteString("  " + w.theme.Body.Render(w.runID) + "\n")
	}
	return w.cat.T("done.heading"), b.String(), w.cat.T("hint.close")
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
