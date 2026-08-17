package tui

import "strings"

// The explanation pane.
//
// Help text used to open every screen and hints trailed every focused field,
// which spent the content column's vertical rows on prose. On a window wide
// enough to afford it, all of that moves to a fixed column on the right --
// the screen's help, the focused item's hint, the notes -- and the content
// keeps its rows for the controls. On a narrow window the same text stays
// inline, exactly where it was: the pane is a use of spare width, not a
// requirement.

// infoActive reports whether the current window carries the pane.
//
// Computed the way the chrome computes it, so the wizard and the renderer
// cannot disagree about whether the inline copy should be shown.
func (w *Wizard) infoActive() bool {
	paneW := w.width - gutter*2
	if len(w.rail()) > 0 && !w.hideRail && w.width >= minChromeW {
		paneW = w.width - railWidth - 3 - gutter
	}
	return paneW >= maxContentW+infoW+3
}

// infoPane renders the explanation for the current screen, or nothing for the
// screens that have none.
func (w *Wizard) infoPane() string {
	paras := w.infoParas()
	if len(paras) == 0 {
		return ""
	}

	wrap := infoW - 2
	var b strings.Builder
	b.WriteString(w.theme.Section(w.cat.T("info.title"), infoW-1, w.glyphs) + "\n\n")
	for i, p := range paras {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(w.dim(wrapCells(p, wrap), wrap) + "\n")
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

	// The focused field's own hint, under the screen's text: what the pane is
	// for is answering "what goes here" without costing the form a row.
	if w.cursorIsField() {
		if h := w.fieldHint(int(w.step), w.fieldIndex()); h != "" {
			add(h)
		}
	}
	return out
}
