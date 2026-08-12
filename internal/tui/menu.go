package tui

import (
	"fmt"
	"strings"
)

// The start menu.
//
// The installer used to be the whole program: opening it put an operator on the
// first question of a new build with no way to reach anything else. That is
// wrong for a tool whose work is mostly not first installs -- a cluster is
// built once and then upgraded, reconfigured and looked at for years.
//
// Entries that are not implemented are listed and say so. Hiding them would
// make the tool look finished; showing them without a word would make an
// operator hunt for a screen that does not exist.

// MenuItem is one entry of the start menu.
type MenuItem struct {
	// TitleKey and HelpKey are catalogue keys.
	TitleKey, HelpKey string

	// Enter is where choosing this entry goes. StepMenu means it goes nowhere,
	// which is what an unimplemented entry does.
	Enter Step

	// Mode is the flow this entry starts. Chosen here and nowhere else: every
	// screen after the menu is shared, and a screen that had to work out which
	// flow it was in would be a second place for the two to disagree.
	Mode mode

	// Missing names what has to exist before this entry can work. Empty means
	// the entry works.
	Missing string
}

// menuItems is the menu in the order it is shown.
//
// Install first because it is what somebody opening the tool for the first time
// wants, and Logs last because it is what somebody opening it for the twentieth
// time wants and they already know where it is.
var menuItems = []MenuItem{
	{TitleKey: "menu.install", HelpKey: "menu.install.help", Enter: StepLang},
	{TitleKey: "menu.upgrade", HelpKey: "menu.upgrade.help", Enter: StepOpen, Mode: modeUpgrade},
	{TitleKey: "menu.settings", HelpKey: "menu.settings.help", Enter: StepOpen, Mode: modeSettings},
	{TitleKey: "menu.logs", HelpKey: "menu.logs.help", Enter: StepRuns},
	{TitleKey: "menu.quit", HelpKey: "menu.quit.help", Enter: StepMenu},
}

// menuScreen renders the start menu.
func (w *Wizard) menuScreen(width int) (string, string, string) {
	var b strings.Builder
	b.WriteString(w.dim(w.cat.T("menu.help"), width) + "\n\n")

	for i, item := range menuItems {
		selected := i == w.menu

		// The label is padded before it is styled: colour codes are not cells,
		// and padding a styled string makes every row a different width.
		marker := "  "
		label := w.theme.Body.Render(padCells(w.cat.T(item.TitleKey), 22))
		if selected {
			marker = w.theme.Accent.Render(w.glyphs.Focus) + " "
			label = w.theme.ChoiceSel.Render(padCells(w.cat.T(item.TitleKey), 22))
		}
		line := marker + label

		// An entry that cannot be used says so on its own line rather than
		// failing after it is chosen.
		if item.Missing != "" {
			line += w.theme.Dim.Render(w.cat.T("menu.unavailable"))
		}
		b.WriteString(line + "\n")

		if selected {
			help := item.HelpKey
			if item.Missing != "" {
				help = item.Missing
			}
			b.WriteString("  " + w.dim(wrapCells(w.cat.T(help), width-4), width) + "\n")
		}
		b.WriteString("\n")
	}

	status := ""
	if item := menuItems[w.menu]; item.Missing != "" {
		status = w.theme.Dim.Render(w.glyphs.Warn + " " + w.cat.T("menu.unavailable.status"))
	}
	return w.cat.T("menu.heading"), b.String(), status
}

// runsScreen lists the runs on this machine.
//
// The event file is the record of a build, and reading it back is what `attach`
// does from another terminal. Reaching it from the menu means an operator who
// opened the tool to find out what happened last week does not have to know
// that command exists.
func (w *Wizard) runsScreen(width int) (string, string, string) {
	if len(w.runs) == 0 {
		return w.cat.T("runs.heading"),
			w.dim(wrapCells(w.cat.T("runs.none"), width), width),
			""
	}

	var b strings.Builder
	b.WriteString(w.dim(w.cat.T("runs.help"), width) + "\n\n")

	for i, r := range w.runs {
		marker := "  "
		if i == w.runSel {
			marker = w.theme.Accent.Render(w.glyphs.Focus) + " "
		}
		label := padCells(r.ID, 28) + padCells(r.When, 22) + r.Summary
		if i == w.runSel {
			label = w.theme.ChoiceSel.Render(label)
		} else {
			label = w.theme.Body.Render(label)
		}
		b.WriteString(marker + label + "\n")
	}

	return w.cat.T("runs.heading"), b.String(),
		fmt.Sprintf("%d", len(w.runs)) + " " + w.cat.T("runs.count")
}

// RunEntry is one row of the run list.
type RunEntry struct {
	ID      string
	Dir     string
	When    string
	Summary string
}
