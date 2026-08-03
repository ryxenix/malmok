package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Chrome is the window a graphical installer would draw, rendered in cells.
//
// THE SHAPE
//
//	┌ title bar ─────────────────────────────────────────────┐
//	│ ● step      │  Heading                                 │
//	│ ▸ step      │  Description text                        │
//	│   step      │                                          │
//	│             │  ( ) choice                              │
//	│             │  (o) choice                              │
//	│             │                          [ back ] [ next ]│
//	└────────────────────────────────────────────────────────┘
//
// A step rail on the left, a wide content pane, primary and secondary buttons
// bottom right. That is the layout of every graphical OS installer, and it
// carries over to a terminal without pretending to be a dialog box: an operator
// always sees where they are in the sequence and how much is left, which a
// series of modal boxes never shows.
//
// Colour is decoration. Every marker, bracket and focus cue is legible in
// monochrome, because the same screen has to work over IPMI.

const (
	railWidth    = 18
	minChromeW   = 72
	minChromeH   = 18
	gutter       = 2
	buttonMargin = 2
)

// Theme holds the styles the chrome draws with.
type Theme struct {
	TitleBar lipgloss.Style
	Rail     lipgloss.Style
	RailNow  lipgloss.Style
	RailDone lipgloss.Style
	RailNext lipgloss.Style
	Divider  lipgloss.Style

	Heading lipgloss.Style
	Body    lipgloss.Style
	Dim     lipgloss.Style
	Accent  lipgloss.Style
	Err     lipgloss.Style

	Choice    lipgloss.Style
	ChoiceSel lipgloss.Style
	Field     lipgloss.Style
	FieldSel  lipgloss.Style

	BtnPrimary   lipgloss.Style
	BtnSecondary lipgloss.Style
	BtnFocus     lipgloss.Style

	Bar     lipgloss.Style
	BarFill lipgloss.Style
}

// NewTheme builds the palette. mono drops every colour but keeps weight and
// reverse video, which survive on a monochrome console.
func NewTheme(mono bool) Theme {
	plain := lipgloss.NewStyle()
	if mono {
		return Theme{
			TitleBar: plain.Reverse(true).Bold(true),
			Rail:     plain,
			RailNow:  plain.Bold(true),
			RailDone: plain,
			RailNext: plain.Faint(true),
			Divider:  plain,
			Heading:  plain.Bold(true),
			Body:     plain,
			Dim:      plain.Faint(true),
			Accent:   plain.Bold(true),
			Err:      plain.Bold(true),

			Choice: plain, ChoiceSel: plain.Reverse(true),
			Field: plain, FieldSel: plain.Reverse(true),

			BtnPrimary: plain.Bold(true), BtnSecondary: plain,
			BtnFocus: plain.Reverse(true).Bold(true),
			Bar:      plain.Faint(true), BarFill: plain,
		}
	}

	var (
		accent = lipgloss.Color("#1e6fd9")
		white  = lipgloss.Color("#ffffff")
		ink    = lipgloss.Color("#d0d0d0")
		faint  = lipgloss.Color("#707070")
		ok     = lipgloss.Color("#3fa34d")
		red    = lipgloss.Color("#d05050")
	)
	base := lipgloss.NewStyle().Foreground(ink)

	return Theme{
		TitleBar: lipgloss.NewStyle().Background(accent).Foreground(white).Bold(true),
		Rail:     base,
		RailNow:  lipgloss.NewStyle().Foreground(white).Bold(true),
		RailDone: lipgloss.NewStyle().Foreground(ok),
		RailNext: lipgloss.NewStyle().Foreground(faint),
		Divider:  lipgloss.NewStyle().Foreground(faint),

		Heading: lipgloss.NewStyle().Foreground(white).Bold(true),
		Body:    base,
		Dim:     lipgloss.NewStyle().Foreground(faint),
		Accent:  lipgloss.NewStyle().Foreground(accent).Bold(true),
		Err:     lipgloss.NewStyle().Foreground(red).Bold(true),

		Choice:    base,
		ChoiceSel: lipgloss.NewStyle().Foreground(white).Bold(true),
		Field:     base,
		FieldSel:  lipgloss.NewStyle().Foreground(white),

		BtnPrimary:   lipgloss.NewStyle().Background(accent).Foreground(white).Bold(true),
		BtnSecondary: lipgloss.NewStyle().Foreground(ink),
		BtnFocus:     lipgloss.NewStyle().Background(white).Foreground(lipgloss.Color("#101010")).Bold(true),
		Bar:          lipgloss.NewStyle().Foreground(faint),
		BarFill:      lipgloss.NewStyle().Foreground(accent),
	}
}

// ---------------------------------------------------------------------------
// Frame
// ---------------------------------------------------------------------------

// RailItem is one entry in the step list.
type RailItem struct {
	Label string
	State RailState
}

// RailState is how far the operator has got.
type RailState int

const (
	RailDone RailState = iota
	RailCurrent
	RailFuture
)

// Frame is everything the chrome needs to draw one screen.
type Frame struct {
	Title   string
	Context string // right side of the title bar

	Rail []RailItem

	Heading string
	Body    string

	Buttons []Button
	Focused int

	Status string // one dim line above the buttons
}

// Button is a labelled action. Primary is drawn filled, the way a graphical
// installer marks the action that continues.
type Button struct {
	Label   string
	Primary bool
}

// Render draws the whole screen at the given size.
func (t Theme) Render(f Frame, w, h int, g Glyphs) string {
	w, h = max(w, 20), max(h, 8)

	var b strings.Builder
	b.WriteString(t.titleBar(f, w))
	b.WriteString("\n")

	// The rail is dropped on a narrow terminal: knowing which choice is in
	// front of you beats knowing which step it belongs to.
	showRail := w >= minChromeW && len(f.Rail) > 0
	contentW := w - gutter*2
	if showRail {
		contentW = w - railWidth - 3 - gutter
	}

	body := t.content(f, contentW, g)
	footer := t.footer(f, w)

	bodyH := h - 1 - lines(footer)
	bodyLines := strings.Split(padTo(body, bodyH), "\n")

	if showRail {
		rail := strings.Split(padTo(t.rail(f.Rail, g), bodyH), "\n")
		for i := range bodyLines {
			left := padCells(rail[i], railWidth)
			b.WriteString(t.Rail.Render(" "+left) + t.Divider.Render(g.VRule) + " " + bodyLines[i] + "\n")
		}
	} else {
		for _, line := range bodyLines {
			b.WriteString("  " + line + "\n")
		}
	}
	b.WriteString(footer)
	return b.String()
}

func (t Theme) titleBar(f Frame, w int) string {
	left := " " + f.Title
	right := f.Context + " "
	return t.TitleBar.Render(padCells(spread(left, right, w), w))
}

func (t Theme) rail(items []RailItem, g Glyphs) string {
	var b strings.Builder
	b.WriteString("\n")
	for _, it := range items {
		switch it.State {
		case RailDone:
			b.WriteString(t.RailDone.Render(g.OK+" ") + t.Rail.Render(it.Label))
		case RailCurrent:
			b.WriteString(t.RailNow.Render(g.Running + " " + it.Label))
		default:
			b.WriteString(t.RailNext.Render("  " + it.Label))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (t Theme) content(f Frame, w int, g Glyphs) string {
	var b strings.Builder
	b.WriteString("\n")
	if f.Heading != "" {
		b.WriteString(t.Heading.Render(truncCells(f.Heading, w)) + "\n\n")
	}
	if f.Body != "" {
		b.WriteString(f.Body + "\n")
	}
	return b.String()
}

func (t Theme) footer(f Frame, w int) string {
	var b strings.Builder
	if f.Status != "" {
		b.WriteString("  " + t.Dim.Render(truncCells(f.Status, w-4)) + "\n")
	}
	if len(f.Buttons) == 0 {
		return b.String()
	}

	rendered := make([]string, len(f.Buttons))
	for i, btn := range f.Buttons {
		label := "  " + btn.Label + "  "
		switch {
		case i == f.Focused:
			rendered[i] = t.BtnFocus.Render("[" + label + "]")
		case btn.Primary:
			rendered[i] = t.BtnPrimary.Render("[" + label + "]")
		default:
			rendered[i] = t.BtnSecondary.Render("[" + label + "]")
		}
	}
	row := strings.Join(rendered, " ")
	b.WriteString(lipgloss.PlaceHorizontal(w-buttonMargin, lipgloss.Right, row) + "\n")
	return b.String()
}

// ---------------------------------------------------------------------------
// Widgets
// ---------------------------------------------------------------------------

// Radio renders a single-choice list with a description column.
func (t Theme) Radio(labels, notes []string, chosen, cursor, w int, g Glyphs) string {
	var b strings.Builder
	labelW := 0
	for _, l := range labels {
		labelW = max(labelW, cells(l))
	}

	for i, label := range labels {
		mark := "( ) "
		if i == chosen {
			mark = "(" + g.OK + ") "
		}
		line := mark + padCells(label, labelW)
		if i < len(notes) && notes[i] != "" {
			line += "  " + t.Dim.Render(truncCells(notes[i], max(w-cells(line)-4, 6)))
		}
		if i == cursor {
			b.WriteString(t.ChoiceSel.Render(g.Running+" ") + line)
		} else {
			b.WriteString("  " + t.Choice.Render(line))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Fields renders labelled inputs, the way a form does in a graphical installer.
func (t Theme) Fields(labels, values []string, cursor int, editing bool, w int, g Glyphs) string {
	var b strings.Builder
	labelW := 0
	for _, l := range labels {
		labelW = max(labelW, cells(l))
	}
	boxW := max(w-labelW-10, 12)

	for i := range labels {
		val := values[i]
		box := padCells(truncCells(val, boxW), boxW)
		if i == cursor && editing {
			box = padCells(truncCells(val+"_", boxW), boxW)
		}

		line := padCells(labels[i], labelW) + "  " + t.field(box, i == cursor, editing)
		if i == cursor {
			b.WriteString(t.ChoiceSel.Render(g.Running+" ") + line)
		} else {
			b.WriteString("  " + line)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (t Theme) field(box string, focused, editing bool) string {
	switch {
	case focused && editing:
		return t.FieldSel.Render("[" + box + "]")
	case focused:
		return t.ChoiceSel.Render("[" + box + "]")
	default:
		return t.Field.Render("[" + box + "]")
	}
}

// Progress renders a wide bar with a percentage, the way an installer shows
// long work.
func (t Theme) Progress(done, total, w int, g Glyphs) string {
	if total <= 0 {
		total = 1
	}
	inner := max(w-8, 10)
	filled := min(done*inner/total, inner)
	pct := done * 100 / total

	return t.BarFill.Render(strings.Repeat(g.BarFull, filled)) +
		t.Bar.Render(strings.Repeat(g.BarEmpty, inner-filled)) +
		t.Body.Render(" "+itoa(pct)+"%")
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func lines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(s, "\n"), "\n") + 1
}

// padTo grows or trims a block to exactly n lines.
func padTo(s string, n int) string {
	ls := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	for len(ls) < n {
		ls = append(ls, "")
	}
	return strings.Join(ls[:max(n, 0)], "\n")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
