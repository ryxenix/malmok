package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Terminal columns are cells, not runes, and a Korean glyph occupies two of
// them. Padding with len([]rune(s)) lays out correctly in English and pushes
// every column out of alignment in Korean — which would make the language
// toggle a cosmetic promise the layout cannot keep.
//
// Everything that positions text goes through these helpers.

// cells returns how many terminal columns s occupies.
func cells(s string) int { return ansi.StringWidth(s) }

// padCells pads s on the right to exactly n columns, truncating if it is wider.
func padCells(s string, n int) string {
	if n <= 0 {
		return ""
	}
	w := cells(s)
	if w > n {
		return truncCells(s, n)
	}
	return s + strings.Repeat(" ", n-w)
}

// truncCells shortens s to at most n columns, appending an ellipsis when there
// is room for one.
//
// It never splits a wide glyph: cutting between the halves of a Korean
// character leaves the terminal a byte sequence it cannot draw, and the rest of
// the line shifts.
func truncCells(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if cells(s) <= n {
		return s
	}
	limit := n
	suffix := ""
	if n > 3 {
		limit, suffix = n-3, "..."
	}

	var (
		b     strings.Builder
		width int
	)
	for _, r := range s {
		rw := cells(string(r))
		if width+rw > limit {
			break
		}
		b.WriteRune(r)
		width += rw
	}
	return b.String() + suffix
}

// spread places left and right at opposite ends of a line n columns wide.
func spread(left, right string, n int) string {
	gap := n - cells(left) - cells(right)
	if gap < 1 {
		return truncCells(left, max(n-cells(right)-1, 1)) + " " + right
	}
	return left + strings.Repeat(" ", gap) + right
}

// wrapCells breaks s into lines of at most n columns, on word boundaries.
//
// Help text is written as prose in the catalogue, and a translator has no way
// to know how many columns a sentence will occupy — Korean is roughly twice as
// wide per character as English, so any hand-wrapped catalogue would be wrong
// in one language or the other.
func wrapCells(s string, n int) string {
	if n <= 0 {
		return ""
	}
	var (
		out  []string
		line strings.Builder
		w    int
	)
	flush := func() {
		if line.Len() > 0 {
			out = append(out, line.String())
			line.Reset()
			w = 0
		}
	}

	for _, para := range strings.Split(s, "\n") {
		for _, word := range strings.Fields(para) {
			ww := cells(word)
			switch {
			case ww > n: // a single word longer than the line
				flush()
				out = append(out, truncCells(word, n))
			case w == 0:
				line.WriteString(word)
				w = ww
			case w+1+ww <= n:
				line.WriteString(" " + word)
				w += 1 + ww
			default:
				flush()
				line.WriteString(word)
				w = ww
			}
		}
		flush()
	}
	return strings.Join(out, "\n")
}
