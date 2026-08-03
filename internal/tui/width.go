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
