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
	// maxContentW caps the content column. A form is not a table: fields that
	// stretch across a wide terminal put the value a head-turn from its label.
	maxContentW = 84
)

// Theme holds the styles the chrome draws with.
type Theme struct {
	// mono is remembered so render-time decisions (the gradient rule) can
	// refuse colour the same way the styles already do.
	mono bool

	TitleBar lipgloss.Style
	// HeaderChip is the product name block at the left of the title bar, and
	// HeaderCtx the context badge at its right. The bar between them stays
	// quiet: a header that is one solid colour band reads as a warning.
	HeaderChip lipgloss.Style
	HeaderCtx  lipgloss.Style
	Rail       lipgloss.Style
	RailNow    lipgloss.Style
	RailDone   lipgloss.Style
	RailNext   lipgloss.Style
	Divider    lipgloss.Style

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

	// FocusBar is the accent down the left of the selected row.
	FocusBar lipgloss.Style
	// Badge is a small inverse label. A status that reads as a chip is found
	// faster on a busy screen than one that reads as another word.
	Badge    lipgloss.Style
	BadgeOK  lipgloss.Style
	BadgeErr lipgloss.Style
	// Key styles a keycap in the footer hint.
	Key lipgloss.Style
}

// NewTheme builds the palette. mono drops every colour but keeps weight and
// reverse video, which survive on a monochrome console.
func NewTheme(mono bool) Theme {
	plain := lipgloss.NewStyle()
	if mono {
		return Theme{
			mono:       true,
			TitleBar:   plain,
			HeaderChip: plain.Reverse(true).Bold(true),
			HeaderCtx:  plain.Reverse(true),
			Rail:       plain,
			RailNow:    plain.Bold(true),
			RailDone:   plain,
			RailNext:   plain.Faint(true),
			Divider:    plain,
			Heading:    plain.Bold(true),
			Body:       plain,
			Dim:        plain.Faint(true),
			Accent:     plain.Bold(true),
			Err:        plain.Bold(true),

			Choice: plain, ChoiceSel: plain.Reverse(true),
			Field: plain, FieldSel: plain.Reverse(true),

			BtnPrimary: plain.Bold(true), BtnSecondary: plain,
			BtnFocus: plain.Reverse(true).Bold(true),
			Bar:      plain.Faint(true), BarFill: plain,

			FocusBar: plain.Bold(true),
			Badge:    plain.Reverse(true), BadgeOK: plain.Reverse(true),
			BadgeErr: plain.Reverse(true).Bold(true),
			Key:      plain.Bold(true),
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
		TitleBar:   lipgloss.NewStyle().Foreground(faint),
		HeaderChip: lipgloss.NewStyle().Background(accent).Foreground(white).Bold(true),
		HeaderCtx:  lipgloss.NewStyle().Background(lipgloss.Color("#2a2a2a")).Foreground(ink),
		Rail:       base,
		RailNow:    lipgloss.NewStyle().Foreground(white).Bold(true),
		RailDone:   lipgloss.NewStyle().Foreground(ok),
		RailNext:   lipgloss.NewStyle().Foreground(faint),
		Divider:    lipgloss.NewStyle().Foreground(faint),

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

		FocusBar: lipgloss.NewStyle().Foreground(accent),
		Badge:    lipgloss.NewStyle().Background(lipgloss.Color("#303030")).Foreground(ink),
		BadgeOK:  lipgloss.NewStyle().Background(ok).Foreground(lipgloss.Color("#08120a")).Bold(true),
		BadgeErr: lipgloss.NewStyle().Background(red).Foreground(lipgloss.Color("#180808")).Bold(true),
		Key:      lipgloss.NewStyle().Background(lipgloss.Color("#2a2a2a")).Foreground(white),
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
	Title string
	// Crumb is where the operator is, drawn beside the product chip the way a
	// path is: "설치 › 노드".
	Crumb   string
	Context string // right side of the title bar, drawn as a badge

	// Truecolor lets the chrome draw gradients. The terminal said so
	// (tea.ColorProfileMsg); the chrome never guesses.
	Truecolor bool

	Rail []RailItem

	Heading string
	Body    string

	Buttons []Button
	Focused int

	// Exit is drawn bottom-left, away from the actions that move forward.
	// Proxmox puts Abort there for the same reason: the key that ends the
	// install should not sit next to the key that continues it.
	Exit *Button

	// Status is the key help, drawn at the very bottom under the buttons, the
	// way both Proxmox and the Ubuntu server installer place it.
	Status string

	// Info is the explanation pane down the right side: the screen's help, the
	// focused item's hint, the notes. On a wide window it takes what the
	// content cannot use, so a description no longer costs the content column
	// vertical rows. Empty means no pane -- the wizard only fills it when the
	// window can afford one, and puts the same text inline when it cannot.
	Info string

	// HideRail drops the step list. Proxmox and the Ubuntu server installer
	// have none; only the desktop installer does.
	HideRail bool
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
	// A rule under the title separates the chrome from the work, which is what
	// makes a terminal window read as an application rather than as output.
	b.WriteString(t.blendRule(w, f.Truecolor, g))
	b.WriteString("\n")

	// The chrome fills the terminal -- header, rules, rail and footer go edge
	// to edge, the way every full-screen tool's do. The content column hugs
	// the left of its pane, the way the Ubuntu installer's does: reading
	// starts where the eye starts, and a column floating in the middle of a
	// wide window reads as small and far away. It is still capped at
	// maxContentW, because a form is not a table and brackets that stretch to
	// 160 cells put the value a head-turn from its label.
	showRail := w >= minChromeW && len(f.Rail) > 0 && !f.HideRail
	paneW := w - gutter*2
	if showRail {
		paneW = w - railWidth - 3 - gutter
	}
	contentW := min(paneW, maxContentW)

	body := t.content(f, contentW, g)
	footer := t.footer(f, w, g)

	// The explanation strip sits above the footer: a fixed place the eye
	// learns once, full width so two lines hold what a side pane needed a
	// column for, and outside the content pane so the controls keep their
	// rows. Tried on the right first -- a fixed column beside the content --
	// and it read as clutter: a second body competing with the first.
	strip := ""
	if f.Info != "" {
		strip = t.Divider.Render(g.Line(w)) + "\n" + f.Info + "\n"
	}

	// The content is centred vertically in its pane as well, from its own
	// height: a menu on a 70-row window belongs in the middle of it, not
	// pinned under the header with fifty blank rows below.
	bodyH := h - 2 - lines(footer) - lines(strip)
	slack := bodyH - lines(body)
	if slack > 1 {
		body = strings.Repeat("\n", slack/2) + body
	}
	bodyLines := strings.Split(padTo(body, bodyH), "\n")

	if showRail {
		rail := strings.Split(padTo(t.rail(f.Rail, g), bodyH), "\n")
		for i := range bodyLines {
			left := padCells(rail[i], railWidth)
			b.WriteString(t.Rail.Render(" "+left) + t.Divider.Render(g.VRule) + " " +
				bodyLines[i] + "\n")
		}
	} else {
		for i := range bodyLines {
			b.WriteString("  " + bodyLines[i] + "\n")
		}
	}
	b.WriteString(strip)
	b.WriteString(footer)
	return b.String()
}

func (t Theme) titleBar(f Frame, w int) string {
	// The product is a chip, not a band. k9s, lazygit and btop all mark the
	// product at the top-left and leave the rest of the row to information;
	// a full-width solid bar spends the strongest colour on the screen saying
	// nothing.
	chip := t.HeaderChip.Render(" " + f.Title + " ")
	crumb := ""
	if f.Crumb != "" {
		crumb = " " + t.TitleBar.Render(f.Crumb)
	}
	right := ""
	if f.Context != "" {
		right = t.HeaderCtx.Render(" " + f.Context + " ")
	}

	gap := w - cells(chip) - cells(crumb) - cells(right)
	if gap < 1 {
		gap = 1
	}
	return chip + crumb + strings.Repeat(" ", gap) + right
}

// blendRule draws the rule under the header: a gradient where the terminal can
// show one, the divider colour where it cannot -- sixteen colours cannot
// blend, and the same rule the topology box and the wordmark follow.
func (t Theme) blendRule(w int, truecolor bool, g Glyphs) string {
	if !truecolor || t.mono {
		return t.Divider.Render(g.Line(w))
	}
	ramp := lipgloss.Blend1D(max(w, 2), lipgloss.Color("#1e6fd9"), lipgloss.Color("#252525"))
	var b strings.Builder
	for i := 0; i < w; i++ {
		b.WriteString(lipgloss.NewStyle().Foreground(ramp[i]).Render(g.Rule))
	}
	return b.String()
}

func (t Theme) rail(items []RailItem, g Glyphs) string {
	var b strings.Builder
	b.WriteString("\n")
	for i, it := range items {
		num := padCells(itoa(i+1), 2)
		switch it.State {
		case RailDone:
			b.WriteString(t.RailNext.Render(num) + t.RailDone.Render(g.OK+" ") + t.Rail.Render(it.Label))
		case RailCurrent:
			b.WriteString(t.FocusBar.Render(g.Focus) + t.RailNow.Render(num+" "+it.Label))
		default:
			b.WriteString(t.RailNext.Render(num + "  " + it.Label))
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

// footer draws the rule, then the action row, then the key help.
//
// The order matters: a rule above the buttons is what separates "what you are
// looking at" from "what you can do about it", and every installer an operator
// has used puts the keys last.
func (t Theme) footer(f Frame, w int, g Glyphs) string {
	var b strings.Builder
	b.WriteString(t.Divider.Render(g.Line(w)) + "\n")

	if len(f.Buttons) > 0 || f.Exit != nil {
		right := ""
		if len(f.Buttons) > 0 {
			rendered := make([]string, len(f.Buttons))
			for i, btn := range f.Buttons {
				rendered[i] = t.button(btn, i == f.Focused)
			}
			right = strings.Join(rendered, " ")
		}
		left := ""
		if f.Exit != nil {
			// Bottom-left, away from the actions that move forward: the key
			// that ends an install should not sit beside the one that
			// continues it.
			left = t.button(*f.Exit, f.Focused == exitFocus)
		}

		gap := w - buttonMargin*2 - cells(left) - cells(right)
		if gap < 1 {
			gap = 1
		}
		b.WriteString(" " + left + strings.Repeat(" ", gap) + right + " \n")
	}

	if f.Status != "" {
		b.WriteString(" " + t.Dim.Render(truncCells(f.Status, w-2)) + "\n")
	}
	return b.String()
}

// exitFocus is the Focused value that selects the bottom-left action.
const exitFocus = -2

func (t Theme) button(btn Button, focused bool) string {
	label := "  " + btn.Label + "  "
	switch {
	case focused:
		return t.BtnFocus.Render("[" + label + "]")
	case btn.Primary:
		return t.BtnPrimary.Render("[" + label + "]")
	default:
		return t.BtnSecondary.Render("[" + label + "]")
	}
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
			b.WriteString(t.FocusBar.Render(g.Focus) + " " + t.ChoiceSel.Render(line))
		} else {
			b.WriteString("  " + t.Choice.Render(line))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Section draws a group heading: an accent tick, the title, and a rule to the
// edge. Screens that stack several groups -- the dataplane, the storage, the
// downgrade policy -- read as one undifferentiated column without it; the rule
// is what makes a group scannable without reading it.
func (t Theme) Section(title string, w int, g Glyphs) string {
	head := g.SectionTick + " " + title + " "
	rule := max(w-cells(head), 0)
	return t.FocusBar.Render(g.SectionTick) + " " + t.Heading.Render(title) + " " +
		t.Divider.Render(strings.Repeat(g.Rule, rule))
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
			b.WriteString(t.FocusBar.Render(g.Focus) + " " + line)
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
	pct := done * 100 / total

	bar := g.SmoothBar(done, total, inner)
	// The filled and empty halves are styled separately, so the bar reads as
	// one object rather than as two runs of characters.
	split := 0
	for i, r := range bar {
		if r == []rune(g.BarEmpty)[0] {
			split = i
			break
		}
		split = i + len(string(r))
	}
	return t.BarFill.Render(bar[:split]) + t.Bar.Render(bar[split:]) +
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

// Badge renders a small inverse label. Statuses read as chips rather than as
// more words, which is what makes a busy screen scannable.
func (t Theme) Badge2(text string, kind string) string {
	style := t.Badge
	switch kind {
	case "ok":
		style = t.BadgeOK
	case "err":
		style = t.BadgeErr
	}
	return style.Render(" " + text + " ")
}

// Keys renders a footer hint as keycaps followed by what they do.
func (t Theme) Keys(pairs [][2]string, g Glyphs) string {
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, t.Key.Render(" "+p[0]+" ")+" "+t.Dim.Render(p[1]))
	}
	return strings.Join(parts, "   ")
}
