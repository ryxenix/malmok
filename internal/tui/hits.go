package tui

import "strings"

// Where things are on the screen.
//
// A terminal hands a click back as a row and a column and nothing else, so
// something has to remember what was drawn there. The renderer is the only
// part that knows -- it is the one placing the rail at these rows and the
// buttons at those columns -- so it records as it draws, and the update loop
// reads the record back.
//
// Rebuilt on every render rather than kept in step: a stale hit map sends a
// click to whatever used to be under the pointer, which is worse than no
// mouse at all.

// hitKind is what was drawn in a region.
type hitKind int

const (
	hitNone hitKind = iota
	// hitRail is a step in the left-hand rail.
	hitRail
	// hitButton is one of the footer's actions; hitExit is the one on the
	// left, which the frame keeps apart from the rest on purpose.
	hitButton
	hitExit
	// hitItem is a selectable row of the content column: a menu entry, a
	// radio choice, a field. The index is the screen's own cursor index, so
	// a click and a keypress arrive at the same place.
	hitItem
)

// region is a rectangle of the screen and what it stands for.
type region struct {
	row0, row1 int // inclusive
	col0, col1 int // inclusive
	kind       hitKind
	index      int
}

// hitMap is every region of the last frame drawn.
type hitMap struct {
	regions []region

	// contentRow and contentCol are where the content column starts on the
	// screen. A screen marks its own rows by their number within the body it
	// built; the two together turn that into a row of the terminal.
	contentRow, contentCol int

	// lines maps a body line to the cursor index the screen would put there,
	// which is what makes a click and a keypress arrive at the same place.
	lines map[int]int
}

func (h *hitMap) reset() {
	h.regions = h.regions[:0]
	h.contentRow, h.contentCol = 0, 0
	clear(h.lines)
}

// mark records that a run of body lines belongs to consecutive cursor
// indices. Screens call it as they build, because they are the only part that
// knows a radio group of three sits at these lines and means those choices.
func (h *hitMap) mark(line, count, index int) {
	if h == nil || count <= 0 {
		return
	}
	if h.lines == nil {
		h.lines = map[int]int{}
	}
	for i := range count {
		h.lines[line+i] = index + i
	}
}

// item reports the cursor index drawn at a screen row, if any.
func (h *hitMap) item(y int) (int, bool) {
	if h == nil || h.lines == nil || h.contentRow == 0 {
		return 0, false
	}
	i, ok := h.lines[y-h.contentRow]
	return i, ok
}

// rows records a region that spans whole rows.
func (h *hitMap) rows(row0, row1 int, kind hitKind, index int) {
	h.add(row0, row1, 0, 1<<30, kind, index)
}

func (h *hitMap) add(row0, row1, col0, col1 int, kind hitKind, index int) {
	if h == nil || row1 < row0 || col1 < col0 {
		return
	}
	h.regions = append(h.regions, region{row0, row1, col0, col1, kind, index})
}

// at reports what was drawn at a cell. Later regions win: the footer is drawn
// after the body, and a click on the footer is a click on the footer.
func (h *hitMap) at(x, y int) (hitKind, int) {
	if h == nil {
		return hitNone, 0
	}
	for i := len(h.regions) - 1; i >= 0; i-- {
		r := h.regions[i]
		if y >= r.row0 && y <= r.row1 && x >= r.col0 && x <= r.col1 {
			return r.kind, r.index
		}
	}
	return hitNone, 0
}

// radio writes a radio group and records which body line means which cursor
// index, so a click lands on the same choice the arrow keys reach.
//
// The base index is derived rather than passed: a block is drawn with the
// cursor relative to itself, and the difference from the screen's own cursor
// is exactly how many selectable rows came before it. Asking every call site
// to restate that is asking one of them to get it wrong.
func (w *Wizard) radio(b *strings.Builder, labels, notes []string, chosen, cursor, width int) {
	w.hits.mark(nextLine(b.String()), len(labels), w.cursor[w.step]-cursor)
	b.WriteString(w.theme.Radio(labels, notes, chosen, cursor, width, w.glyphs))
}

// fields writes a field block and records its rows the same way.
func (w *Wizard) fields(b *strings.Builder, labels, values []string, cursor int, editing bool, width int) {
	w.hits.mark(nextLine(b.String()), len(labels), w.cursor[w.step]-cursor)
	b.WriteString(w.theme.Fields(labels, values, cursor, editing, width, w.glyphs))
}

// nextLine is the line the next write will land on.
//
// Not lines(s): a builder that ends in a newline has finished its last line,
// so the next write starts a new one, and a builder mid-line continues the
// one it is on. Getting this wrong put every clickable row one line above
// where it was drawn -- which a hit map is exactly the wrong place to be
// approximately right.
func nextLine(s string) int {
	if s == "" {
		return 0
	}
	if strings.HasSuffix(s, "\n") {
		return lines(s)
	}
	return lines(s) - 1
}
