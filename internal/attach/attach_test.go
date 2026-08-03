package attach

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"platform.ryxen.dev/platformctl/internal/event"
)

const (
	runA = "01JBQ8F2K3M5N7P9R1S3T5V7W9"
	runB = "01JBQ9ZZZZZZZZZZZZZZZZZZZZ"
)

func clock() func() time.Time {
	t := time.Date(2026, 8, 3, 9, 4, 11, 220*int(time.Millisecond), time.UTC)
	return func() time.Time { return t }
}

func writer(t *testing.T, path, run string) *event.FileWriter {
	t.Helper()
	w, err := event.OpenFile(path, run, event.WithClock(clock()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w
}

func phase(name string, st event.Status) event.Event {
	return event.Event{Kind: event.KindPhase, Phase: name, Status: st}
}

// fastFollower keeps the tests quick without making them timing-dependent:
// every assertion waits for a condition rather than for a duration.
func fastFollower(path, run string) *Follower {
	return &Follower{Path: path, Run: run, PollInterval: 5 * time.Millisecond}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ---------------------------------------------------------------------------
// Replay then tail
// ---------------------------------------------------------------------------

// §1.2: attach replays what already happened, then follows what happens next.
func TestReplayThenTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	w := writer(t, path, runA)

	for _, e := range []event.Event{
		{Kind: event.KindRun, Status: event.StatusRunning, Detail: "apply started"},
		phase("preflight", event.StatusRunning),
		phase("preflight", event.StatusOK),
	} {
		if _, err := w.Emit(e); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var sink Collector
	done := make(chan error, 1)
	go func() { done <- fastFollower(path, runA).Follow(ctx, &sink) }()

	waitFor(t, "the replay of 3 events", func() bool { return len(sink.Events()) == 3 })

	// Now write more; the follower must pick them up without restarting.
	if _, err := w.Emit(phase("plan", event.StatusRunning)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the tailed event", func() bool { return len(sink.Events()) == 4 })

	if got := sink.Resets(); got != 1 {
		t.Errorf("follower reset %d times; one initial reset was expected", got)
	}
	cancel()
	if err := <-done; err != nil && err != context.Canceled {
		t.Errorf("Follow returned %v", err)
	}
}

// §5.6: a gap means lines were lost, so the follower re-reads instead of
// drawing a screen it already knows is incomplete.
func TestGapTriggersReread(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")

	lines := []string{
		`{"ts":"2026-08-03T09:00:00.000Z","run":"` + runA + `","seq":1,"kind":"phase","phase":"preflight","status":"running"}`,
		`{"ts":"2026-08-03T09:00:01.000Z","run":"` + runA + `","seq":2,"kind":"phase","phase":"preflight","status":"ok"}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var sink Collector
	go fastFollower(path, runA).Follow(ctx, &sink)
	waitFor(t, "the initial replay", func() bool { return len(sink.Events()) == 2 })

	// Append an event whose seq skipped 3..6.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	gapLine := `{"ts":"2026-08-03T09:00:09.000Z","run":"` + runA + `","seq":7,"kind":"phase","phase":"plan","status":"ok"}` + "\n"
	if _, err := f.WriteString(gapLine); err != nil {
		t.Fatal(err)
	}
	f.Close()

	waitFor(t, "a reset caused by the gap", func() bool { return sink.Resets() >= 2 })

	// The re-read cannot conjure lines the file does not have, so the gap is
	// reported and the stream continues rather than looping forever.
	waitFor(t, "the re-read to complete", func() bool { return len(sink.Events()) == 3 })
	seqs := []uint64{}
	for _, e := range sink.Events() {
		seqs = append(seqs, e.Seq)
	}
	if len(seqs) != 3 || seqs[0] != 1 || seqs[1] != 2 || seqs[2] != 7 {
		t.Errorf("after re-read got seqs %v, want [1 2 7]", seqs)
	}

	gaps := sink.Gaps()
	if len(gaps) != 1 || !strings.Contains(gaps[0], "3->7") {
		t.Errorf("gaps = %v, want one reporting 3->7", gaps)
	}

	// And it must settle: no further resets once the gap is accepted.
	settled := sink.Resets()
	time.Sleep(50 * time.Millisecond)
	if got := sink.Resets(); got != settled {
		t.Errorf("follower kept re-reading: resets went %d -> %d", settled, got)
	}
}

// A partial trailing line is the normal state of a file being appended to. It
// must not be consumed, or the real event that follows would be skipped.
func TestPartialTrailingLineIsNotConsumed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")

	complete := `{"ts":"2026-08-03T09:00:00.000Z","run":"` + runA + `","seq":1,"kind":"phase","phase":"preflight","status":"ok"}` + "\n"
	partial := `{"ts":"2026-08-03T09:00:01.000Z","run":"` + runA + `","seq":2,"kind":"ph`
	if err := os.WriteFile(path, []byte(complete+partial), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var sink Collector
	go fastFollower(path, runA).Follow(ctx, &sink)
	waitFor(t, "the complete line", func() bool { return len(sink.Events()) == 1 })

	// Finish the partial line; it must now arrive intact.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`ase","phase":"plan","status":"ok"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	waitFor(t, "the completed line", func() bool { return len(sink.Events()) == 2 })
	if got := sink.Events()[1].Phase; got != "plan" {
		t.Errorf("second event phase = %q, want plan", got)
	}
	if got := sink.Resets(); got != 1 {
		t.Errorf("a partial line caused %d resets; it should cause none", got-1)
	}
}

func TestFollowWaitsForTheFileToAppear(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var sink Collector
	go fastFollower(path, runA).Follow(ctx, &sink)

	time.Sleep(20 * time.Millisecond) // the engine has not started yet
	w := writer(t, path, runA)
	if _, err := w.Emit(phase("preflight", event.StatusRunning)); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "events after the file appeared", func() bool { return len(sink.Events()) == 1 })
}

func TestStopOnRunEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	w := writer(t, path, runA)

	for _, e := range []event.Event{
		{Kind: event.KindRun, Status: event.StatusRunning, Detail: "apply started"},
		phase("preflight", event.StatusOK),
		{Kind: event.KindRun, Status: event.StatusOK, Detail: "apply finished"},
	} {
		if _, err := w.Emit(e); err != nil {
			t.Fatal(err)
		}
	}

	f := fastFollower(path, runA)
	f.StopOnRunEnd = true

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var sink Collector
	if err := f.Follow(ctx, &sink); err != nil {
		t.Fatalf("Follow should return cleanly when the run ends, got %v", err)
	}
	if len(sink.Events()) != 3 {
		t.Errorf("collected %d events, want 3", len(sink.Events()))
	}
}

// A fixed output.eventLog holds several runs (§1.1), so the filter has to work.
func TestRunFilter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")

	wa := writer(t, path, runA)
	if _, err := wa.Emit(phase("preflight", event.StatusOK)); err != nil {
		t.Fatal(err)
	}
	wb := writer(t, path, runB)
	if _, err := wb.Emit(phase("preflight", event.StatusOK)); err != nil {
		t.Fatal(err)
	}
	if _, err := wa.Emit(phase("plan", event.StatusOK)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var sink Collector
	go fastFollower(path, runB).Follow(ctx, &sink)
	waitFor(t, "run B's single event", func() bool { return len(sink.Events()) == 1 })

	time.Sleep(30 * time.Millisecond) // give the wrong run a chance to leak in
	if runs := sink.Runs(); len(runs) != 1 || runs[0] != runB {
		t.Errorf("collected runs %v, want only %s", runs, runB)
	}
}

func TestLatestRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	wa := writer(t, path, runA)
	if _, err := wa.Emit(phase("preflight", event.StatusOK)); err != nil {
		t.Fatal(err)
	}
	wb := writer(t, path, runB)
	if _, err := wb.Emit(phase("preflight", event.StatusOK)); err != nil {
		t.Fatal(err)
	}

	got, err := LatestRun(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != runB {
		t.Errorf("LatestRun = %s, want %s", got, runB)
	}
}

// Run ids are ULIDs, so lexical order is chronological order.
func TestLatestRunDir(t *testing.T) {
	bundle := t.TempDir()
	for _, id := range []string{runA, runB, "01JBQ0AAAAAAAAAAAAAAAAAAAA"} {
		if err := os.MkdirAll(filepath.Join(bundle, "runs", id), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got, err := LatestRunDir(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != runB {
		t.Errorf("LatestRunDir = %s, want the run ending in %s", got, runB)
	}
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

// §5.3 made concrete: the glyphs exist in the renderer and nowhere else. An
// event carrying this string would be rejected by Event.Validate.
func TestBarIsDrawnByTheRendererNotTheEngine(t *testing.T) {
	bar := Bar(8, 12, 10)
	if !strings.ContainsRune(bar, '█') {
		t.Fatalf("Bar produced %q, expected block glyphs", bar)
	}

	e := event.Event{
		TS: event.NewTimestamp(clock()()), Run: runA, Seq: 1,
		Kind: event.KindStep, Phase: "l1-bootstrap", Step: "ready",
		Status: event.StatusRunning, Detail: bar,
	}
	if errs := e.Validate(); len(errs) == 0 {
		t.Error("an event containing a drawn bar must be rejected by the schema")
	}
}

func TestBarEdgeCases(t *testing.T) {
	tests := []struct{ done, total, width int }{
		{0, 12, 10}, {12, 12, 10}, {-1, 12, 10}, {99, 12, 10},
	}
	for _, tc := range tests {
		got := Bar(tc.done, tc.total, tc.width)
		if len([]rune(got)) != tc.width+2 { // brackets
			t.Errorf("Bar(%d,%d,%d) = %q, want width %d", tc.done, tc.total, tc.width, got, tc.width)
		}
	}
	if got := Bar(1, 0, 10); got != "" {
		t.Errorf("Bar with zero total = %q, want empty", got)
	}
}

func TestTextRendererOutput(t *testing.T) {
	var buf bytes.Buffer
	r := NewTextRenderer(&buf)

	events := []event.Event{
		phase("preflight", event.StatusRunning),
		{Kind: event.KindProbe, Phase: "preflight", Node: "10.10.0.12", Code: "PF-204",
			Status: event.StatusFailed, Detail: "eBPF program load denied"},
		{Kind: event.KindLog, Phase: "preflight", Node: "10.10.0.12", Level: event.LevelInfo,
			Detail: "this is noise"},
		phase("preflight", event.StatusOK),
	}
	for _, e := range events {
		e.TS = event.NewTimestamp(clock()())
		e.Run, e.Seq = runA, 1
		if err := r.Handle(e); err != nil {
			t.Fatal(err)
		}
	}

	out := buf.String()
	if !strings.Contains(out, "PF-204") {
		t.Error("the probe code is missing from the output")
	}
	if strings.Contains(out, "this is noise") {
		t.Error("log events should be suppressed unless Verbose is set")
	}

	summary := r.Summary()
	if !strings.Contains(summary, "1 failure(s)") {
		t.Errorf("summary does not report the failure:\n%s", summary)
	}
	if !strings.Contains(summary, "10.10.0.12") {
		t.Errorf("summary does not name the failing node:\n%s", summary)
	}
}

// A failed phase or run restates the step that failed. Counting all three
// turns one incident into three entries and the summary stops being read.
func TestSummaryListsOriginatingFailuresOnly(t *testing.T) {
	var buf bytes.Buffer
	r := NewTextRenderer(&buf)

	events := []event.Event{
		{Kind: event.KindStep, Phase: "l1-bootstrap", Step: "rke2-server-ready",
			Node: "10.10.0.11", Status: event.StatusFailed, Code: "PF-601",
			Detail: "port 9345 unreachable"},
		{Kind: event.KindPhase, Phase: "l1-bootstrap", Status: event.StatusFailed, Code: "PF-601"},
		{Kind: event.KindRun, Status: event.StatusFailed, Code: "PF-601",
			Detail: "apply halted at l1-bootstrap"},
	}
	for _, e := range events {
		e.TS, e.Run, e.Seq = event.NewTimestamp(clock()()), runA, 1
		if err := r.Handle(e); err != nil {
			t.Fatal(err)
		}
	}

	summary := r.Summary()
	if !strings.Contains(summary, "1 failure(s)") {
		t.Errorf("one incident should be reported once:\n%s", summary)
	}
	if !strings.Contains(summary, "port 9345 unreachable") {
		t.Errorf("the originating failure is missing:\n%s", summary)
	}
}

func TestTextRendererResetClearsState(t *testing.T) {
	var buf bytes.Buffer
	r := NewTextRenderer(&buf)

	e := event.Event{Kind: event.KindProbe, Phase: "preflight", Node: "10.10.0.12",
		Status: event.StatusFailed, Detail: "eBPF load denied"}
	e.TS, e.Run, e.Seq, e.Code = event.NewTimestamp(clock()()), runA, 1, "PF-204"
	if err := r.Handle(e); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Summary(), "1 failure(s)") {
		t.Fatal("precondition failed")
	}

	r.Reset()
	if strings.Contains(r.Summary(), "failure(s)") {
		t.Error("Reset left failures behind; a replay would double-count them")
	}
}
