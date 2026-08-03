package tui

import (
	"strings"
	"testing"
	"time"

	"platform.ryxen.dev/platformctl/internal/event"
)

func ts(sec int) event.Timestamp {
	return event.NewTimestamp(time.Date(2026, 8, 3, 9, 31, sec, 0, time.UTC))
}

func model(t *testing.T, lang Lang, ascii bool, w, h int) *Model {
	t.Helper()
	m, err := New("01KZ3PTJMGR68RHFQ0V8TK2KJZ", ModeOwner, ascii, lang)
	if err != nil {
		t.Fatal(err)
	}
	m.width, m.height = w, h
	return m
}

// runInProgress is the mockup's state: three phases done, one running with a
// retry, two waiting.
func runInProgress() []event.Event {
	phase := func(id string, s event.Status) event.Event {
		return event.Event{Kind: event.KindPhase, Phase: id, Status: s, TS: ts(0)}
	}
	return []event.Event{
		{Kind: event.KindRun, Status: event.StatusRunning, TS: ts(0)},
		phase("preflight", event.StatusPending), phase("plan", event.StatusPending),
		phase("l0-node-prep", event.StatusPending), phase("l1-bootstrap", event.StatusPending),
		phase("l1-join-agent", event.StatusPending), phase("l2-dataplane", event.StatusPending),
		phase("preflight", event.StatusOK), phase("plan", event.StatusOK),
		phase("l0-node-prep", event.StatusOK), phase("l1-bootstrap", event.StatusRunning),
		{Kind: event.KindStep, Phase: "l1-bootstrap", Step: "rke2-server-ready",
			Node: "10.10.0.11", Status: event.StatusRunning, TS: ts(44),
			Progress: &event.Progress{Done: 3, Total: 7}, Attempt: 2, MaxAttempts: 3},
		{Kind: event.KindLog, Phase: "l1-bootstrap", Node: "10.10.0.11",
			Level: event.LevelInfo, TS: ts(52), Detail: "starting rke2-server"},
	}
}

func render(t *testing.T, m *Model, evs []event.Event) []string {
	t.Helper()
	for _, e := range evs {
		m.apply(e)
	}
	return strings.Split(m.View().Content, "\n")
}

// A Korean glyph occupies two terminal columns. Measuring in runes lays out
// correctly in English and wrecks every column in Korean, which would make the
// language toggle a promise the layout cannot keep.
func TestNoLineExceedsTheTerminalWidth(t *testing.T) {
	for _, lang := range []Lang{LangEN, LangKO} {
		for _, ascii := range []bool{false, true} {
			for _, w := range []int{80, 100, 120} {
				m := model(t, lang, ascii, w, 24)
				for i, line := range render(t, m, runInProgress()) {
					if got := cells(line); got > w {
						t.Errorf("lang=%s ascii=%v width=%d: line %d is %d cells\n%s",
							lang, ascii, w, i, got, line)
					}
				}
			}
		}
	}
}

// The status column has to line up in both languages, or the screen reads as
// broken in one of them.
func TestStatusColumnAligns(t *testing.T) {
	for _, lang := range []Lang{LangEN, LangKO} {
		m := model(t, lang, false, 80, 24)
		lines := render(t, m, runInProgress())

		var starts []int
		for _, line := range lines {
			if !strings.HasPrefix(line, " ✓ ") && !strings.HasPrefix(line, " ▸ ") {
				continue
			}
			// The status word begins one cell after the padded phase column.
			starts = append(starts, cells(string([]rune(line)[:3+phaseNameCol+1])))
		}
		if len(starts) < 2 {
			t.Fatalf("lang=%s: expected several phase rows, got %d", lang, len(starts))
		}
		for _, s := range starts[1:] {
			if s != starts[0] {
				t.Errorf("lang=%s: status column starts at %d and %d", lang, starts[0], s)
			}
		}
	}
}

// The screen must survive a terminal that cannot draw box glyphs -- a serial
// console, an IPMI viewer, PuTTY with the wrong codepage. That is exactly where
// somebody is sitting when an install is going badly.
func TestASCIIFallbackDropsEveryWideGlyph(t *testing.T) {
	m := model(t, LangEN, true, 80, 24)
	out := strings.Join(render(t, m, runInProgress()), "\n")

	for _, bad := range []string{"✓", "▸", "✗", "─", "█", "░", "·", "–"} {
		if strings.Contains(out, bad) {
			t.Errorf("ASCII rendering still contains %q", bad)
		}
	}
	if !strings.Contains(out, "+ preflight") || !strings.Contains(out, "> l1-bootstrap") {
		t.Errorf("ASCII markers are missing:\n%s", out)
	}
}

// The layout must not depend on colour: NO_COLOR is real and serial consoles
// are often monochrome. Every verdict has to be legible from the marker alone.
func TestVerdictIsLegibleWithoutColour(t *testing.T) {
	m := model(t, LangEN, false, 80, 24)
	evs := append(runInProgress(),
		event.Event{Kind: event.KindPhase, Phase: "l1-bootstrap",
			Status: event.StatusFailed, Code: "EX-102", TS: ts(59)})
	out := strings.Join(render(t, m, evs), "\n")

	if strings.Contains(out, "\x1b[") {
		t.Error("the screen emitted ANSI colour of its own")
	}
	if !strings.Contains(out, "✗ l1-bootstrap") {
		t.Errorf("a failed phase is not marked:\n%s", out)
	}
}

// A 24-line terminal has to fit the phase tree and the footer. The log pane is
// what gives way, because knowing which phase you are in matters more than the
// last three lines of output.
func TestShortTerminalKeepsTheTreeAndFooter(t *testing.T) {
	for _, h := range []int{24, 20, 16, 12} {
		m := model(t, LangEN, false, 80, h)
		lines := render(t, m, runInProgress())
		out := strings.Join(lines, "\n")

		if !strings.Contains(out, "l2-dataplane") {
			t.Errorf("height=%d: the phase tree was dropped", h)
		}
		if !strings.Contains(out, "q ") {
			t.Errorf("height=%d: the footer was dropped", h)
		}
		if h <= 16 && strings.Contains(out, "LOG") {
			t.Errorf("height=%d: the log pane should have given way", h)
		}
	}
}

// Failures are printed as plain lines so an operator can select them into a
// ticket without dragging a border along.
func TestFailuresAreUnboxedAndCarryTheirCode(t *testing.T) {
	m := model(t, LangEN, false, 80, 30)
	evs := append(runInProgress(),
		event.Event{Kind: event.KindStep, Phase: "l1-bootstrap", Step: "rke2-server-ready",
			Node: "10.10.0.11", Status: event.StatusFailed, Code: "PF-601", TS: ts(59),
			Detail: "port 9345 unreachable from 10.10.20.21"})
	out := strings.Join(render(t, m, evs), "\n")

	if !strings.Contains(out, "PF-601") {
		t.Errorf("the failure code is missing:\n%s", out)
	}
	if !strings.Contains(out, "port 9345 unreachable") {
		t.Errorf("the failure detail is missing:\n%s", out)
	}
	for _, border := range []string{"│", "┌", "└", "├"} {
		if strings.Contains(out, border) {
			t.Errorf("the failure pane drew a border (%s), which breaks copy-paste", border)
		}
	}
}

// Quitting means different things in the two places this screen is used, and
// the footer has to say which one applies.
func TestFooterNamesWhatQuittingDoes(t *testing.T) {
	owner := model(t, LangEN, false, 80, 24)
	if got := owner.footer(); !strings.Contains(got, "abort") || strings.Contains(got, "d detach") {
		t.Errorf("owner footer = %q; it must warn that quitting aborts and offer no detach", got)
	}

	obs, err := New("run", ModeObserver, false, LangEN)
	if err != nil {
		t.Fatal(err)
	}
	obs.width = 80
	if got := obs.footer(); !strings.Contains(got, "detach") || !strings.Contains(got, "keeps running") {
		t.Errorf("observer footer = %q; it must offer detach and say the engine survives", got)
	}
}

func TestCatalogues(t *testing.T) {
	en, err := LoadCatalogue(LangEN)
	if err != nil {
		t.Fatal(err)
	}
	ko, err := LoadCatalogue(LangKO)
	if err != nil {
		t.Fatal(err)
	}

	// Both catalogues must define the same keys, or toggling the language
	// silently replaces words with raw key names.
	for key := range en.table {
		if _, ok := ko.table[key]; !ok {
			t.Errorf("ko is missing %q", key)
		}
	}
	for key := range ko.table {
		if _, ok := en.table[key]; !ok {
			t.Errorf("en is missing %q", key)
		}
	}

	// Every status the engine can emit needs a word.
	for _, s := range []event.Status{
		event.StatusPending, event.StatusRunning, event.StatusOK,
		event.StatusSkipped, event.StatusFailed, event.StatusBlocked,
	} {
		key := "status." + string(s)
		if en.T(key) == key {
			t.Errorf("no English word for %s", key)
		}
	}

	// Codes are identifiers, never translated.
	for key, val := range ko.table {
		if strings.Contains(val, "PF-") || strings.Contains(val, "EX-") {
			t.Errorf("catalogue entry %q embeds a code: %q", key, val)
		}
	}
}

func TestDetectASCII(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"utf8 xterm", map[string]string{"TERM": "xterm-256color", "LANG": "en_US.UTF-8"}, false},
		{"korean utf8", map[string]string{"TERM": "xterm", "LANG": "ko_KR.UTF-8"}, false},
		{"serial vt220", map[string]string{"TERM": "vt220", "LANG": "en_US.UTF-8"}, true},
		{"dumb", map[string]string{"TERM": "dumb"}, true},
		{"latin1", map[string]string{"TERM": "xterm", "LANG": "en_US.ISO-8859-1"}, true},
		{"no locale", map[string]string{"TERM": "xterm"}, true},
		{"forced", map[string]string{"TERM": "xterm", "LANG": "en_US.UTF-8", "PLATFORMCTL_ASCII": "1"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DetectASCII(func(k string) string { return tc.env[k] })
			if got != tc.want {
				t.Errorf("DetectASCII = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTruncCellsNeverSplitsAWideGlyph(t *testing.T) {
	s := "설치가 진행되는 중입니다"
	for n := 1; n <= cells(s)+2; n++ {
		got := truncCells(s, n)
		if cells(got) > n {
			t.Errorf("truncCells(%d) produced %d cells: %q", n, cells(got), got)
		}
	}
}
