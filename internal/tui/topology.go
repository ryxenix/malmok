package tui

import (
	"net/netip"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"

	"platform.ryxen.dev/platformctl/internal/exec"
)

// The topology panel is the one place a diagram earns its keep.
//
// Everywhere else the screens present lists, and a list is the right shape for
// a list. But which node sits on which segment, where the join address points
// and which node failed a probe are relationships, and a list makes the reader
// reconstruct them. Drawing the segments is the difference between an operator
// checking their own entry and an operator hoping they typed it right.
//
// It renders at whatever the terminal can do: box drawing and colour where
// available, plain ASCII where not, and the same information either way.

// segment is one network the nodes sit on.
type segment struct {
	prefix netip.Prefix
	label  string // when the address could not be parsed
	rows   []topoRow
}

// topoRow is one line inside a segment.
type topoRow struct {
	marker string
	role   string
	addr   string
	note   string
	bad    bool
}

// topology groups what the wizard has collected into segments.
//
// Grouping is by /24 rather than by anything the operator declares: a customer
// site's segments are a fact about their network, and asking them to restate it
// is asking them to get it wrong.
func (w *Wizard) topology() []segment {
	byPrefix := map[netip.Prefix]*segment{}
	var unparsed *segment
	order := []netip.Prefix{}

	add := func(host, marker, role, shown string, bad bool) {
		// An empty host is a question not yet answered, not a node on an
		// unresolvable segment.
		if strings.TrimSpace(host) == "" {
			return
		}
		// shown overrides what is printed: a load balancer pool is grouped by
		// its first address but displayed as the range, which is what the
		// operator entered and what the gateway takes from.
		display := host
		if shown != "" {
			display = shown
		}
		row := topoRow{marker: marker, role: role, addr: display, bad: bad}
		// The one thing a diagram of addresses cannot show: which of them is
		// the machine the operator is sitting at. It is the difference between
		// a build that opens a connection and one that does not, and it is
		// decided by the address, so the operator has no other way to check it.
		if role != w.cat.T("topo.lbpool") && exec.IsLocal(host) {
			row.note = w.cat.T("topo.here")
		}
		addr, err := netip.ParseAddr(strings.TrimSpace(host))
		if err != nil {
			if unparsed == nil {
				unparsed = &segment{label: w.cat.T("topo.unresolved")}
			}
			unparsed.rows = append(unparsed.rows, row)
			return
		}
		p, err := addr.Prefix(24)
		if err != nil {
			return
		}
		seg, ok := byPrefix[p]
		if !ok {
			seg = &segment{prefix: p}
			byPrefix[p] = seg
			order = append(order, p)
		}
		seg.rows = append(seg.rows, row)
	}

	g := w.glyphs
	add(w.cfg.Server, g.Server, w.cat.T("topo.server"), "", false)
	for _, a := range w.cfg.Agents {
		add(a, g.Agent, w.cat.T("topo.agent"), "", false)
	}

	// The load balancer pool belongs on whichever segment it addresses, which
	// is how an operator checks it is not on the wrong one.
	for _, cidr := range w.cfg.LBPool {
		p, err := netip.ParsePrefix(strings.TrimSpace(cidr))
		if err != nil {
			continue
		}
		add(p.Addr().String(), g.Address, w.cat.T("topo.lbpool"), cidr, false)
	}

	out := make([]segment, 0, len(order)+1)
	sort.Slice(order, func(i, j int) bool { return order[i].Addr().Less(order[j].Addr()) })
	for _, p := range order {
		out = append(out, *byPrefix[p])
	}
	if unparsed != nil {
		out = append(out, *unparsed)
	}
	return out
}

// renderTopology draws the panel.
func (w *Wizard) renderTopology(width int) string {
	segs := w.topology()
	if len(segs) == 0 {
		return ""
	}

	inner := min(max(width-4, 24), 60)
	var b strings.Builder

	for i, seg := range segs {
		if i > 0 {
			b.WriteString("\n")
		}
		title := seg.label
		if title == "" {
			title = seg.prefix.String()
		}
		b.WriteString("  " + w.theme.Dim.Render(title) + "\n")
		b.WriteString("  " + w.boxLine(w.glyphs.TopLeft, w.glyphs.TopRight, inner) + "\n")

		for _, r := range seg.rows {
			b.WriteString("  " + w.theme.Divider.Render(w.glyphs.VRule) + " " +
				w.topoRow(r, inner-4) + " " +
				w.theme.Divider.Render(w.glyphs.VRule) + "\n")
		}
		b.WriteString("  " + w.boxLine(w.glyphs.BottomLeft, w.glyphs.BottomRight, inner) + "\n")
	}

	// The join address is not on any segment: it is a name every node resolves,
	// and putting it inside one of the boxes would say the opposite.
	if w.cfg.Registration != "" {
		b.WriteString("\n  " + w.theme.Dim.Render(padCells(w.cat.T("topo.join"), 8)) +
			w.theme.Body.Render(w.cfg.Registration) + "\n")
	}
	return b.String()
}

// topoRow renders one line to exactly width cells, so the box stays a box.
func (w *Wizard) topoRow(r topoRow, width int) string {
	mark := w.theme.Accent.Render(r.marker)
	if r.bad {
		mark = w.theme.Err.Render(r.marker)
	}
	body := padCells(r.role, 9) + r.addr
	if r.note != "" {
		body = padCells(body, 24) + r.note
	}
	// The marker is one cell plus its separator; the rest is the body.
	return mark + " " + padCells(truncCells(body, width-2), width-2)
}

// boxLine draws a horizontal edge, with a gradient when the terminal can show
// one. Sixteen colours cannot, and a gradient there is a band of noise.
func (w *Wizard) boxLine(left, right string, inner int) string {
	rule := strings.Repeat(w.glyphs.Rule, max(inner-2, 0))
	if !w.truecolor || w.mono {
		return w.theme.Divider.Render(left + rule + right)
	}

	from := lipgloss.Color("#1e6fd9")
	to := lipgloss.Color("#3a3a3a")
	ramp := lipgloss.Blend1D(len([]rune(rule)), from, to)

	var b strings.Builder
	b.WriteString(w.theme.Divider.Render(left))
	for i, r := range []rune(rule) {
		b.WriteString(lipgloss.NewStyle().Foreground(ramp[i]).Render(string(r)))
	}
	b.WriteString(w.theme.Divider.Render(right))
	return b.String()
}

// topologyLines is how many lines the panel will take, so a screen can decide
// whether it fits before drawing it.
func (w *Wizard) topologyLines() int {
	segs := w.topology()
	if len(segs) == 0 {
		return 0
	}
	n := 0
	for i, seg := range segs {
		if i > 0 {
			n++
		}
		n += 3 + len(seg.rows) // title, two edges, rows
	}
	if w.cfg.Registration != "" {
		n += 2
	}
	return n
}
