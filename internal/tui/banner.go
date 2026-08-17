package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// The wordmark on the start menu.
//
// A start menu that opens as a bare list reads as a fragment of something; the
// block-letter name is how a terminal tool says it is a product (k9s, lazygit,
// btop -- the convention is old enough to be an expectation). It appears on the
// menu only: the working screens spend their rows on work.

// banner is the wordmark, one string per row, in the block character set.
//
// Hand-drawn rather than generated so the two charsets are decided here: the
// block form for terminals that can draw it, and a plain-ASCII form for the
// consoles the a-toggle exists for -- a serial console that cannot render
// U+2588 would show the product name as mojibake, which is worse than plain
// letters.
var bannerBlock = []string{
	`███╗   ███╗ █████╗ ██╗     ███╗   ███╗ ██████╗ ██╗  ██╗`,
	`████╗ ████║██╔══██╗██║     ████╗ ████║██╔═══██╗██║ ██╔╝`,
	`██╔████╔██║███████║██║     ██╔████╔██║██║   ██║█████╔╝ `,
	`██║╚██╔╝██║██╔══██║██║     ██║╚██╔╝██║██║   ██║██╔═██╗ `,
	`██║ ╚═╝ ██║██║  ██║███████╗██║ ╚═╝ ██║╚██████╔╝██║  ██╗`,
	`╚═╝     ╚═╝╚═╝  ╚═╝╚══════╝╚═╝     ╚═╝ ╚═════╝ ╚═╝  ╚═╝`,
}

var bannerASCII = []string{
	`M   M  AAA  L     M   M  OOO  K  K`,
	`MM MM A   A L     MM MM O   O K K`,
	`M M M AAAAA L     M M M O   O KK`,
	`M   M A   A L     M   M O   O K K`,
	`M   M A   A LLLLL M   M  OOO  K  K`,
}

// bannerFor picks the charset and reports the width of the widest row.
func bannerFor(ascii bool) ([]string, int) {
	rows := bannerBlock
	if ascii {
		rows = bannerASCII
	}
	width := 0
	for _, r := range rows {
		if n := len([]rune(r)); n > width {
			width = n
		}
	}
	return rows, width
}

// renderBanner draws the wordmark centred in width, or nothing when it does
// not fit.
//
// Nothing, not a squeezed version: a wordmark that wraps is noise, and the
// terminals it does not fit on are the ones where every row is spoken for. The
// height guard keeps a short window showing the menu entries rather than the
// decoration above them.
func (w *Wizard) renderBanner(width, height int) string {
	rows, bw := bannerFor(w.ascii)
	if bw > width || height < len(rows)+16 {
		return ""
	}

	pad := strings.Repeat(" ", max((width-bw)/2, 0))
	var b strings.Builder
	for i, row := range rows {
		b.WriteString(pad + w.bannerRow(row, i, len(rows)) + "\n")
	}
	return b.String()
}

// bannerRow colours one row: a vertical gradient where the terminal can show
// one, the accent otherwise. Sixteen colours cannot blend, and a gradient
// there is a band of noise -- the same rule the topology box follows.
func (w *Wizard) bannerRow(row string, i, total int) string {
	if !w.truecolor || w.mono {
		return w.theme.Accent.Render(row)
	}
	ramp := lipgloss.Blend1D(total, lipgloss.Color("#4da3ff"), lipgloss.Color("#1e6fd9"))
	return lipgloss.NewStyle().Foreground(ramp[i]).Render(row)
}
