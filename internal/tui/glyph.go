package tui

import (
	"os"
	"strings"

	"platform.ryxen.dev/platformctl/internal/event"
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
	Rule     string
	Dot      string
}

var unicodeGlyphs = Glyphs{
	OK: "✓", Running: "▸", Failed: "✗", Skipped: "–", Pending: " ", Warn: "!",
	BarFull: "█", BarEmpty: "░", Rule: "─", Dot: "·",
}

var asciiGlyphs = Glyphs{
	OK: "+", Running: ">", Failed: "x", Skipped: "-", Pending: " ", Warn: "!",
	BarFull: "#", BarEmpty: ".", Rule: "-", Dot: "-",
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
