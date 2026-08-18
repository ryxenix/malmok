package tui

import "strings"

// The explanation strip.
//
// Help text used to open every screen and hints trailed every focused field,
// which spent the content column's vertical rows on prose. All of that now
// lives in a strip above the footer: a fixed place the eye learns once, full
// width so two lines hold what a side pane needed a column for. (The side
// pane was tried and read as clutter -- a second body competing with the
// first.) On a window too narrow for the chrome the same text stays inline,
// exactly where it was.

// stripWrap caps the strip's line length. Full-bleed prose at 170 cells is a
// line the eye loses on the way back; the cap is the same judgement as
// maxContentW, a little wider because the strip is one thought, not a form.
const stripWrap = 108

// stripLines caps the strip's height so a wordy screen cannot push the
// content off the window. Three lines at stripWrap hold every current text.
const stripLines = 3

// infoActive reports whether the strip is drawn. One condition, shared with
// nothing: below the chrome's own minimum the window is in survival mode and
// the text stays inline.
func (w *Wizard) infoActive() bool {
	return w.width >= minChromeW
}

// infoPane renders the strip: the focused item's hint first -- "what goes
// here" is the pressing question -- then the screen's help and its notes,
// joined into one flowing text.
func (w *Wizard) infoPane() string {
	paras := w.infoParas()
	if len(paras) == 0 {
		return ""
	}

	sep := " " + w.glyphs.Dot + " "
	wrap := min(w.width-4, stripWrap)
	text := wrapCells(strings.Join(paras, sep), wrap)
	if ls := strings.Split(text, "\n"); len(ls) > stripLines {
		text = strings.Join(ls[:stripLines], "\n")
	}
	var b strings.Builder
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(" " + w.dim(line, wrap))
	}
	return b.String()
}

// infoParas collects what the pane says: the screen first, the focused item
// second, the standing notes last.
func (w *Wizard) infoParas() []string {
	var out []string
	add := func(s string) {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}

	// The focused field's own hint leads: on a strip read left to right,
	// "what goes here" comes before "what this screen is".
	if w.cursorIsField() {
		if h := w.fieldHint(int(w.step), w.fieldIndex()); h != "" {
			add(h)
		}
	}

	switch w.step {
	case StepMenu:
		item := menuItems[w.menu]
		key := item.HelpKey
		if item.Missing != "" {
			key = item.Missing
		}
		add(w.cat.T(key))
	case StepRuns:
		add(w.cat.T("runs.help"))
	case StepWhere:
		add(w.cat.T("where.help"))
	case StepNodes:
		add(w.cat.T(w.nodesHelp()))
	case StepNetwork:
		add(w.cat.T("net.help"))
	case StepOptions:
		add(w.cat.T("options.help"))
		add(w.cat.T("note.options"))
	case StepRegistry:
		add(w.cat.T("reg.help"))
	case StepPKI:
		add(w.cat.T("pki.help_mode"))
		if len(w.fieldsFor(StepPKI)) == 0 {
			add(w.cat.T("pki.note_later"))
		} else if isCA(w.cfg.PKIMode) {
			add(w.cat.T("pki.note_rootkey") + " (PF-706)")
		}
	case StepOpen:
		if w.mode == modeUpgrade {
			add(w.cat.T("open.help.upgrade"))
		} else {
			add(w.cat.T("open.help"))
		}
	case StepSave:
		add(w.cat.T("save.help"))
	case StepTarget:
		add(w.cat.T("target.help"))
		add(w.cat.T("target.note"))
	case StepPrefs:
		add(w.cat.T("prefs.help"))
	case StepSummary:
		add(w.cat.T("summary.help"))
	default:
		return nil
	}

	return out
}
