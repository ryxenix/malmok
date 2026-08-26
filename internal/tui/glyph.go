package tui

import (
	"os"
	"strings"

	"github.com/ryxen/malmok/internal/event"
)

// Glyphs is the character set the screen draws with.
//
// WHY A FALLBACK EXISTS AT ALL
//
//	The screen has to survive a serial console, an IPMI viewer and PuTTY with
//	the wrong codepage — all of which are how somebody reaches a machine at a
//	customer site when it is not going well. Box-drawing characters turn into
//	mojibake in those, and a status column of mojibake is worse than one of
//	plain ASCII.
//
// The layout is identical in both sets, only the characters differ, which is
// why the structured-plain style was chosen: nothing here depends on borders
// lining up.
type Glyphs struct {
	OK      string
	Running string
	Failed  string
	Skipped string
	Pending string
	Warn    string

	BarFull  string
	BarEmpty string
	Rule     string // horizontal
	VRule    string // vertical, for the column between rail and content
	Dot      string

	// Focus is the bar drawn down the left of the row the cursor is on. A
	// whole-row marker reads faster than an arrow at 80 columns, and it stays
	// legible when colour is gone.
	Focus string

	// SectionTick marks a group heading. Deliberately thinner than Focus: the
	// two sit in the same column, and a section that wears the cursor's bar
	// reads as a row somebody selected.
	SectionTick string

	// Partial fills a progress bar between whole cells, so a bar of ten cells
	// moves in eighty steps rather than ten. Empty when the character set
	// cannot draw them.
	Partial []string

	// Spinner turns while work runs. Without one, a step that takes minutes is
	// indistinguishable from one that has hung.
	Spinner []string

	// Box corners, for the topology panel. Nothing else draws a box: an
	// operator selects an error message to paste into a ticket, and a border
	// comes along with the selection.
	TopLeft, TopRight, BottomLeft, BottomRight string

	// Node markers. A server and an agent have to be distinguishable at a
	// glance on a monochrome console, so they differ in shape rather than
	// colour.
	Server, Agent, Address string

	// Menu icons, one per start-menu entry. Shape only -- the label carries
	// the meaning, the icon makes the list scannable.
	IconInstall, IconUpgrade, IconDoc, IconPrefs, IconRuns, IconQuit string
}

var unicodeGlyphs = Glyphs{
	OK: "✓", Running: "▸", Failed: "✗", Skipped: "–", Pending: " ", Warn: "!",
	BarFull: "█", BarEmpty: "░", Rule: "─", VRule: "│", Dot: "·",
	Focus: "▌", SectionTick: "▎",
	Partial: []string{"", "▏", "▎", "▍", "▌", "▋", "▊", "▉"},
	TopLeft: "╭", TopRight: "╮", BottomLeft: "╰", BottomRight: "╯",
	Server: "◉", Agent: "○", Address: "◈",
	Spinner:     []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
	IconInstall: "▣", IconUpgrade: "▲", IconDoc: "✎", IconPrefs: "◇", IconRuns: "≡", IconQuit: "⏻",
}

var asciiGlyphs = Glyphs{
	OK: "+", Running: ">", Failed: "x", Skipped: "-", Pending: " ", Warn: "!",
	BarFull: "#", BarEmpty: ".", Rule: "-", VRule: "|", Dot: "-",
	Focus: ">", SectionTick: "=",
	Partial: nil, // whole cells only
	TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+",
	Server: "S", Agent: "a", Address: "*",
	Spinner:     []string{"-", "\\", "|", "/"},
	IconInstall: "#", IconUpgrade: "^", IconDoc: "e", IconPrefs: "o", IconRuns: "=", IconQuit: "q",
}

// GlyphsFor picks a set. ascii forces the fallback.
func GlyphsFor(ascii bool) Glyphs {
	if ascii {
		return asciiGlyphs
	}
	return unicodeGlyphs
}

// DetectASCII reports whether the terminal should be assumed unable to render
// box-drawing characters.
//
// Detection is deliberately timid: it only forces ASCII on positive evidence,
// because guessing wrong towards ASCII makes a capable terminal look shabby
// while guessing wrong towards Unicode makes an incapable one unreadable — and
// the flag exists for the case where detection is wrong either way.
func DetectASCII(env func(string) string) bool {
	if env == nil {
		env = os.Getenv
	}
	if v := env("PLATFORMCTL_ASCII"); v != "" && v != "0" {
		return true
	}
	switch env("TERM") {
	case "dumb", "vt100", "vt102", "vt220", "ansi", "linux":
		return true
	}
	// A UTF-8 locale is the only positive signal that the terminal will render
	// these characters at all.
	for _, key := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := env(key); v != "" {
			return !strings.Contains(strings.ToUpper(v), "UTF-8") &&
				!strings.Contains(strings.ToUpper(v), "UTF8")
		}
	}
	return true // no locale set at all: assume the worst
}

// Marker returns the one-character status marker.
//
// The marker carries the verdict, not the colour. NO_COLOR is respected and
// serial consoles are often monochrome, so a screen that distinguishes success
// from failure only by colour is unreadable exactly where it matters most.
func (g Glyphs) Marker(s event.Status) string {
	switch s {
	case event.StatusOK:
		return g.OK
	case event.StatusRunning:
		return g.Running
	case event.StatusFailed, event.StatusBlocked:
		return g.Failed
	case event.StatusSkipped:
		return g.Skipped
	default:
		return g.Pending
	}
}

// Bar draws a progress bar of the given width.
func (g Glyphs) Bar(done, total, width int) string {
	if total <= 0 || width <= 0 {
		return strings.Repeat(" ", max(width, 0)+2)
	}
	if done < 0 {
		done = 0
	}
	if done > total {
		done = total
	}
	filled := done * width / total
	return "[" + strings.Repeat(g.BarFull, filled) + strings.Repeat(g.BarEmpty, width-filled) + "]"
}

// Line returns a horizontal rule.
func (g Glyphs) Line(width int) string {
	if width <= 0 {
		return ""
	}
	return strings.Repeat(g.Rule, width)
}

// SmoothBar draws a bar that moves between whole cells.
//
// Eight sub-positions per cell, so a ten-cell bar has eighty steps instead of
// ten. On a long phase the difference is between a bar that looks stuck and one
// that is visibly working.
func (g Glyphs) SmoothBar(done, total, width int) string {
	if total <= 0 || width <= 0 {
		return ""
	}
	if done < 0 {
		done = 0
	}
	if done > total {
		done = total
	}
	if len(g.Partial) == 0 {
		// Whole cells only, but the same shape: Bar draws brackets and this
		// does not, and a fallback that changes the width changes the layout.
		filled := done * width / total
		return strings.Repeat(g.BarFull, filled) + strings.Repeat(g.BarEmpty, width-filled)
	}

	eighths := done * width * 8 / total
	full, rest := eighths/8, eighths%8

	var b strings.Builder
	b.WriteString(strings.Repeat(g.BarFull, full))
	if full < width && rest > 0 {
		b.WriteString(g.Partial[rest])
		full++
	}
	b.WriteString(strings.Repeat(g.BarEmpty, max(width-full, 0)))
	return b.String()
}

// Spin returns the spinner frame for a tick.
func (g Glyphs) Spin(tick int) string {
	if len(g.Spinner) == 0 {
		return ""
	}
	if tick < 0 {
		tick = -tick
	}
	return g.Spinner[tick%len(g.Spinner)]
}
