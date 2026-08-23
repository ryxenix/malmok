package tui

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// How the tool looks, as opposed to what it builds.
//
// The language used to be the first question of an install, which said it was a
// property of the cluster being built. It is not: it is a property of the
// person reading the screen, and they are the same person on their second run
// and their twentieth. Asking it once per install meant it could only be
// changed by starting one.
//
// These are also the three things the keyboard already toggles -- g, a and s.
// The screen exists so they can be found without knowing them, and so the
// answer survives the session.

// Prefs is what the operator chose about the screen itself.
type Prefs struct {
	// Lang is the catalogue. Empty means English (ADR-009).
	Lang Lang `yaml:"lang,omitempty"`
	// ASCII forces the fallback character set for a terminal that cannot draw
	// box characters -- a serial console, an IPMI viewer, PuTTY with the wrong
	// codepage.
	ASCII bool `yaml:"ascii,omitempty"`
	// HideRail drops the step list down the left, which is the layout Proxmox
	// and the Ubuntu server installer use.
	HideRail bool `yaml:"hideRail,omitempty"`
}

// PrefsPath is where the preferences are kept.
//
// The user's configuration directory, not the run bundle: a bundle is an audit
// artifact that gets handed to a customer, and which language the engineer who
// built it reads is not part of the record.
func PrefsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "malmok", "prefs.yaml")
}

// LoadPrefs reads the saved preferences, returning the defaults when there are
// none.
//
// A missing or unreadable file is not an error worth reporting. The tool works
// without it, and refusing to start because a preference file was malformed
// would be the screen's appearance stopping an install.
func LoadPrefs() Prefs {
	path := PrefsPath()
	if path == "" {
		return Prefs{}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Prefs{}
	}
	var p Prefs
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return Prefs{}
	}
	if p.Lang != LangEN && p.Lang != LangKO {
		p.Lang = ""
	}
	return p
}

// Save writes the preferences, and says nothing if it cannot.
//
// The same reasoning as loading: a read-only home directory is a reason for the
// choice not to persist, not a reason to interrupt what the operator was doing.
func (p Prefs) Save() error {
	path := PrefsPath()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body, err := yaml.Marshal(p)
	if err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o644)
}

// prefsRow is one setting on the screen.
type prefsRow struct {
	labelKey string
	// value renders what is currently chosen.
	value func(*Wizard) string
	// toggle moves to the next value.
	toggle func(*Wizard)
}

// prefsRows is the screen, in order.
var prefsRows = []prefsRow{
	{
		labelKey: "prefs.lang",
		value: func(w *Wizard) string {
			if w.cat.Lang() == LangKO {
				return "한국어"
			}
			return "English"
		},
		toggle: func(w *Wizard) {
			if cat, err := LoadCatalogue(w.cat.Other()); err == nil {
				w.cat, w.cfg.Lang = cat, cat.Lang()
			}
		},
	},
	{
		labelKey: "prefs.charset",
		value: func(w *Wizard) string {
			if w.ascii {
				return w.cat.T("prefs.charset.ascii")
			}
			return w.cat.T("prefs.charset.unicode")
		},
		toggle: func(w *Wizard) {
			// Only the character set. A setting that quietly changes another
			// setting is a bug however good the reasoning behind it: this used
			// to force the language to English, so choosing ASCII on the
			// settings screen moved the cursor's neighbour.
			w.ascii = !w.ascii
			w.glyphs = GlyphsFor(w.ascii)
		},
	},
	{
		labelKey: "prefs.rail",
		value: func(w *Wizard) string {
			if w.hideRail {
				return w.cat.T("prefs.rail.hidden")
			}
			return w.cat.T("prefs.rail.shown")
		},
		toggle: func(w *Wizard) { w.hideRail = !w.hideRail },
	},
}

// prefsScreen renders the settings.
func (w *Wizard) prefsScreen(width int) (string, string, string) {
	var b strings.Builder
	b.WriteString(w.inlineHelp(w.cat.T("prefs.help"), width))

	cur := w.cursor[StepPrefs]
	for i, row := range prefsRows {
		w.hits.mark(nextLine(b.String()), 1, i)

		marker := "  "
		label := padCells(w.cat.T(row.labelKey), 18)
		value := row.value(w)
		if i == cur {
			marker = w.theme.Accent.Render(w.glyphs.Focus) + " "
			b.WriteString(marker + w.theme.Body.Render(label) +
				w.theme.ChoiceSel.Render(" "+value+" ") + "\n")
			continue
		}
		b.WriteString(marker + w.theme.Dim.Render(label) + w.theme.Body.Render(value) + "\n")
	}

	if w.prefsErr != "" {
		// Said, not swallowed: an operator who chose a language and finds it
		// gone tomorrow should know the choice never reached disk.
		b.WriteString("\n" + w.theme.Err.Render(w.glyphs.Failed+" "+
			wrapCells(w.prefsErr, width)) + "\n")
	} else {
		b.WriteString("\n" + w.dim(wrapCells(w.cat.T("prefs.where")+" "+PrefsPath(), width), width) + "\n")
	}

	return w.cat.T("prefs.heading"), b.String(), w.cat.T("hint.select")
}

// savePrefs records what is on the screen.
func (w *Wizard) savePrefs() {
	err := Prefs{Lang: w.cat.Lang(), ASCII: w.ascii, HideRail: w.hideRail}.Save()
	w.prefsErr = ""
	if err != nil {
		w.prefsErr = err.Error()
	}
}
