package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"platform.ryxen.dev/platformctl/internal/event"
)

// Layout floors. Below these the screen sheds detail rather than wrapping into
// noise: an IPMI console is 80x24 and that is where somebody is sitting when
// the install is going badly.
const (
	minWidth     = 80
	minLogLines  = 3
	phaseNameCol = 16
	statusCol    = 9
)

// View renders the whole screen.
//
// One string, redrawn each update. No cursor arithmetic, no partial repaint:
// over a serial line at 115200 the difference is not worth a class of bug that
// only appears on somebody else's terminal.
func (m *Model) View() tea.View {
	var b strings.Builder

	b.WriteString(m.header())
	b.WriteString("\n\n")
	b.WriteString(m.phaseTable())

	if lines := m.logCapacity(); lines > 0 {
		b.WriteString("\n")
		b.WriteString(m.logPane(lines))
	}
	if len(m.failures) > 0 {
		b.WriteString("\n")
		b.WriteString(m.failurePane())
	}
	b.WriteString("\n")
	b.WriteString(m.footer())

	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

func (m *Model) header() string {
	title := "platformctl"
	if m.runID != "" {
		title += " " + m.glyphs.Dot + " " + shortRun(m.runID)
	}

	done := 0
	for _, id := range m.order {
		if m.phases[id].status.Terminal() {
			done++
		}
	}
	stage := ""
	if len(m.order) > 0 {
		stage = fmt.Sprintf("[%d/%d] ", done, len(m.order))
	}

	right := stage + m.cat.T("header."+m.runStatusKey())
	return spread(title, right, m.width)
}

func (m *Model) runStatusKey() string {
	switch m.runStatus {
	case event.StatusOK:
		return "done"
	case event.StatusFailed, event.StatusBlocked:
		if strings.Contains(m.runDetail, "cancelled") {
			return "cancelled"
		}
		return "failed"
	default:
		return "running"
	}
}

func (m *Model) phaseTable() string {
	var b strings.Builder

	fmt.Fprintf(&b, " %s %s\n", padCells(m.cat.T("column.phase"), phaseNameCol+2),
		m.cat.T("column.status"))
	b.WriteString(" " + m.glyphs.Line(m.width-2) + "\n")

	if len(m.order) == 0 {
		b.WriteString(" " + m.cat.T("section.nothing") + "\n")
		return b.String()
	}

	for _, id := range m.order {
		p := m.phases[id]
		fmt.Fprintf(&b, " %s %s %s", m.glyphs.Marker(p.status),
			padCells(id, phaseNameCol), padCells(m.cat.T("status."+string(p.status)), statusCol))

		if p.progress != nil && p.status == event.StatusRunning {
			fmt.Fprintf(&b, " %s %d/%d", m.glyphs.Bar(p.progress.Done, p.progress.Total, 10),
				p.progress.Done, p.progress.Total)
		}
		if p.attempt > 1 {
			fmt.Fprintf(&b, "  %s %d/%d", m.cat.T("key.retry"), p.attempt, p.maxTries)
		}
		if p.code != "" && (p.status == event.StatusFailed || p.status == event.StatusBlocked) {
			fmt.Fprintf(&b, "  %s", p.code)
		}
		b.WriteString("\n")

		// The step and node under a running phase are what tells an operator
		// whether anything is happening at all.
		if p.status == event.StatusRunning && p.stepID != "" {
			where := p.stepID
			if p.node != "" {
				where += " " + m.glyphs.Dot + " " + p.node
			}
			fmt.Fprintf(&b, "   %s\n", truncCells(where, m.width-4))
		}
	}
	return b.String()
}

// logCapacity returns how many log lines fit, or zero when the pane has to go.
func (m *Model) logCapacity() int {
	used := 2 + // header + blank
		2 + // table header + rule
		len(m.order) + 2 + // rows, plus room for a running step line and slack
		2 // footer + blank
	if len(m.failures) > 0 {
		used += len(m.failures) + 2
	}
	avail := m.height - used - 2 // pane heading + rule
	if avail < minLogLines {
		return 0
	}
	if m.showLogs {
		return avail
	}
	return min(avail, 6)
}

func (m *Model) logPane(lines int) string {
	var b strings.Builder

	heading := " " + m.cat.T("section.log")
	if n := m.currentNode(); n != "" {
		heading += "  " + n
	}
	b.WriteString(heading + "\n")
	b.WriteString(" " + m.glyphs.Line(m.width-2) + "\n")

	start := max(len(m.logs)-lines, 0)
	for _, e := range m.logs[start:] {
		mark := " "
		if e.Level == event.LevelWarn || e.Level == event.LevelError {
			mark = m.glyphs.Warn
		}
		fmt.Fprintf(&b, " %s %s  %s\n", mark, e.TS.Format("15:04:05"),
			truncCells(e.Detail, m.width-14))
	}
	return b.String()
}

func (m *Model) currentNode() string {
	for _, id := range m.order {
		if p := m.phases[id]; p.status == event.StatusRunning && p.node != "" {
			return p.node
		}
	}
	return ""
}

// failurePane lists failures as plain lines. Deliberately unboxed: an operator
// selects these to paste into a ticket, and a border comes along with the
// selection.
func (m *Model) failurePane() string {
	var b strings.Builder
	fmt.Fprintf(&b, " %s (%d)\n", m.cat.T("section.failures"), len(m.failures))
	for _, e := range m.failures {
		where := e.Phase
		if e.Node != "" {
			where += " " + e.Node
		}
		fmt.Fprintf(&b, " %s %s %s %s\n", m.glyphs.Failed, padCells(e.Code, 9),
			padCells(where, 26), truncCells(e.Detail, max(m.width-42, 10)))
	}
	return b.String()
}

func (m *Model) footer() string {
	quit := m.cat.T("key.quit_owner")
	if m.mode == ModeObserver {
		quit = m.cat.T("key.quit_observer")
	}

	keys := []string{}
	if m.mode == ModeObserver {
		keys = append(keys, "d "+m.cat.T("key.detach"))
	}
	keys = append(keys,
		"l "+m.cat.T("key.logs"),
		"a "+m.cat.T("key.ascii"),
		"g "+m.cat.T("key.language"),
		"q "+quit,
	)

	line := " " + strings.Join(keys, "   ")
	if m.width < minWidth {
		line += "\n " + m.glyphs.Warn + " " + m.cat.T("hint.narrow")
	}
	return line
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func shortRun(id string) string {
	if len(id) <= 10 {
		return id
	}
	return id[:10]
}
