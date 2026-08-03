package event

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const testRun = "01JBQ8F2K3M5N7P9R1S3T5V7W9"

func step(n string) Event {
	return Event{Kind: KindStep, Phase: "l1-bootstrap", Step: n, Status: StatusRunning}
}

// TestSeqIsGapless is acceptance criterion C1.
func TestSeqIsGapless(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf, testRun, WithClock(fixedClock()))

	for i := 0; i < 50; i++ {
		if _, err := w.Emit(step("s")); err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
	}

	got, err := ReadAll(&buf)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(got) != 50 {
		t.Fatalf("read %d events, want 50", len(got))
	}
	for i, e := range got {
		if want := uint64(i + 1); e.Seq != want {
			t.Fatalf("event %d has seq %d, want %d", i, e.Seq, want)
		}
	}
}

// A rejected event must not burn a sequence number: a hole would look exactly
// like lost data to a renderer (§5.6).
func TestRejectedEmitDoesNotConsumeSeq(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf, testRun, WithClock(fixedClock()))

	if _, err := w.Emit(step("first")); err != nil {
		t.Fatal(err)
	}
	bad := step("second")
	bad.Detail = "\x1b[31mred\x1b[0m"
	if _, err := w.Emit(bad); err == nil {
		t.Fatal("expected the invalid event to be rejected")
	}
	if got := w.LastSeq(); got != 1 {
		t.Fatalf("seq advanced to %d despite a rejected emit", got)
	}

	e, err := w.Emit(step("third"))
	if err != nil {
		t.Fatal(err)
	}
	if e.Seq != 2 {
		t.Fatalf("next event got seq %d, want 2", e.Seq)
	}
	if strings.Contains(buf.String(), "red") {
		t.Error("the rejected event reached the stream")
	}
}

// TestReplayReconstructsTheStream is acceptance criterion C2: everything the
// renderer needs is in the file.
func TestReplayReconstructsTheStream(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf, testRun, WithClock(fixedClock()))

	want := []Event{
		{Kind: KindRun, Status: StatusRunning, Detail: "apply started"},
		{Kind: KindPhase, Phase: "preflight", Status: StatusRunning},
		{Kind: KindProbe, Phase: "preflight", Node: "10.10.0.12", Code: "PF-204",
			Status: StatusFailed, Detail: "eBPF program load denied",
			Evidence: "bpf(BPF_PROG_LOAD): Operation not permitted"},
		{Kind: KindDecision, Phase: "plan", Code: "DG-001", Detail: "downgraded to canal-traefik"},
		{Kind: KindLog, Phase: "l1-bootstrap", Node: "10.10.0.11", Level: LevelInfo,
			Detail: "pulling rke2-runtime image"},
		{Kind: KindStep, Phase: "l1-bootstrap", Step: "rke2-server-ready",
			Status: StatusRunning, Attempt: 2, MaxAttempts: 3,
			Progress: &Progress{Done: 3, Total: 7}},
	}
	for _, e := range want {
		if _, err := w.Emit(e); err != nil {
			t.Fatalf("emit %s: %v", e.Kind, err)
		}
	}

	got, err := ReadAll(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("replayed %d events, want %d", len(got), len(want))
	}
	for i := range want {
		g, wnt := got[i], want[i]
		switch {
		case g.Kind != wnt.Kind, g.Phase != wnt.Phase, g.Step != wnt.Step,
			g.Node != wnt.Node, g.Status != wnt.Status, g.Code != wnt.Code,
			g.Detail != wnt.Detail, g.Evidence != wnt.Evidence, g.Level != wnt.Level,
			g.Attempt != wnt.Attempt, g.MaxAttempts != wnt.MaxAttempts:
			t.Errorf("event %d differs after replay:\n got %+v\nwant %+v", i, g, wnt)
		}
		if (g.Progress == nil) != (wnt.Progress == nil) {
			t.Errorf("event %d progress presence differs", i)
		} else if g.Progress != nil && *g.Progress != *wnt.Progress {
			t.Errorf("event %d progress = %+v, want %+v", i, *g.Progress, *wnt.Progress)
		}
	}
}

func TestScannerDetectsGap(t *testing.T) {
	stream := strings.Join([]string{
		`{"ts":"2026-08-03T09:04:11.220Z","run":"R","seq":1,"kind":"phase","phase":"preflight","status":"running"}`,
		`{"ts":"2026-08-03T09:04:12.000Z","run":"R","seq":2,"kind":"phase","phase":"preflight","status":"ok"}`,
		`{"ts":"2026-08-03T09:04:19.000Z","run":"R","seq":7,"kind":"phase","phase":"plan","status":"ok"}`,
	}, "\n")

	_, err := ReadAll(strings.NewReader(stream))
	if err == nil {
		t.Fatal("expected a gap to be reported")
	}
	if !IsGap(err) {
		t.Fatalf("expected ErrGap, got %T: %v", err, err)
	}
	var g *ErrGap
	if !errors.As(err, &g) {
		t.Fatal("could not unwrap ErrGap")
	}
	if g.Expected != 3 || g.Got != 7 {
		t.Errorf("gap reported expected=%d got=%d, want 3 and 7", g.Expected, g.Got)
	}
}

// A fixed output.eventLog accumulates several runs in one file (§1.1). Sequence
// numbers only ever increase within a run, so continuity must be tracked per
// run or interleaved runs would look like gaps.
func TestSeqIsTrackedPerRun(t *testing.T) {
	stream := strings.Join([]string{
		`{"ts":"2026-08-03T09:00:00.000Z","run":"A","seq":1,"kind":"phase","phase":"preflight","status":"ok"}`,
		`{"ts":"2026-08-03T09:00:01.000Z","run":"B","seq":1,"kind":"phase","phase":"preflight","status":"ok"}`,
		`{"ts":"2026-08-03T09:00:02.000Z","run":"A","seq":2,"kind":"phase","phase":"plan","status":"ok"}`,
		`{"ts":"2026-08-03T09:00:03.000Z","run":"B","seq":2,"kind":"phase","phase":"plan","status":"ok"}`,
	}, "\n")

	all, err := ReadAll(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("interleaved runs should not read as a gap: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("read %d events, want 4", len(all))
	}

	onlyA, err := ReadRun(strings.NewReader(stream), "A")
	if err != nil {
		t.Fatal(err)
	}
	if len(onlyA) != 2 {
		t.Fatalf("run A has %d events, want 2", len(onlyA))
	}

	runs, err := Runs(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0] != "A" || runs[1] != "B" {
		t.Errorf("Runs() = %v, want [A B] in first-appearance order", runs)
	}
}

// Resuming appends to the existing stream, so numbering has to continue rather
// than restart (§4.2).
func TestOpenFileResumesSequence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")

	first, err := OpenFile(path, testRun, WithClock(fixedClock()))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := first.Emit(step("s")); err != nil {
			t.Fatal(err)
		}
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := OpenFile(path, testRun, WithClock(fixedClock()))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	e, err := second.Emit(step("after-resume"))
	if err != nil {
		t.Fatal(err)
	}
	if e.Seq != 4 {
		t.Fatalf("resumed at seq %d, want 4", e.Seq)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := ReadAll(f); err != nil {
		t.Errorf("stream is not continuous after resume: %v", err)
	}
}

// A process killed mid-emit leaves a partial line. Appending after it would
// produce one unparseable line and break every reader, so the partial line is
// truncated on open.
func TestOpenFileTruncatesPartialLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")

	w, err := OpenFile(path, testRun, WithClock(fixedClock()))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := w.Emit(step("s")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"ts":"2026-08-03T09:04:13.000Z","run":"` + testRun + `","seq":3,"kind":"st`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	reopened, err := OpenFile(path, testRun, WithClock(fixedClock()))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	e, err := reopened.Emit(step("after-crash"))
	if err != nil {
		t.Fatal(err)
	}
	if e.Seq != 3 {
		t.Fatalf("got seq %d after truncating the partial line, want 3", e.Seq)
	}

	rf, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	got, err := ReadAll(rf)
	if err != nil {
		t.Fatalf("file is not parseable after repair: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("read %d events, want 3", len(got))
	}
}

// The engine emits from several goroutines: nodes are probed and prepared in
// parallel. Sequence allocation must stay unique and gapless under that.
func TestConcurrentEmit(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf, testRun, WithClock(fixedClock()))

	const n = 200
	var wg sync.WaitGroup
	seqs := make(chan uint64, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e, err := w.Emit(step("parallel"))
			if err != nil {
				t.Errorf("emit: %v", err)
				return
			}
			seqs <- e.Seq
		}()
	}
	wg.Wait()
	close(seqs)

	seen := make(map[uint64]bool, n)
	for s := range seqs {
		if seen[s] {
			t.Fatalf("sequence %d was allocated twice", s)
		}
		seen[s] = true
	}
	for i := uint64(1); i <= n; i++ {
		if !seen[i] {
			t.Fatalf("sequence %d was never allocated", i)
		}
	}
}

func TestOpenFileCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs", "01J", "events.jsonl")
	w, err := OpenFile(path, testRun, WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("OpenFile did not create the run directory: %v", err)
	}
	defer w.Close()
	if _, err := w.Emit(step("s")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("event file was not created: %v", err)
	}
}
