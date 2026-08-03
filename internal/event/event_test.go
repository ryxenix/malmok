package event

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// fixedClock keeps timestamps deterministic. Date.now() in a test is a flaky
// test waiting for a slow CI machine.
func fixedClock() func() time.Time {
	t := time.Date(2026, 8, 3, 9, 4, 11, 220*int(time.Millisecond), time.UTC)
	return func() time.Time { return t }
}

func valid() Event {
	return Event{
		TS:     NewTimestamp(time.Date(2026, 8, 3, 9, 4, 11, 0, time.UTC)),
		Run:    "01JBQ8F2K3M5N7P9R1S3T5V7W9",
		Seq:    1,
		Kind:   KindStep,
		Phase:  "l1-bootstrap",
		Step:   "rke2-server-ready",
		Status: StatusRunning,
	}
}

// TestValidate covers docs/11-execute.md §7 group C together with the per-kind
// requirements of §5.2 and the failure rule of §6.
func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Event)
		wantErr string // substring; empty means the event must validate
	}{
		{name: "valid step", mutate: func(*Event) {}},

		// required fields
		{name: "missing run", mutate: func(e *Event) { e.Run = "" }, wantErr: "run is required"},
		{name: "seq zero", mutate: func(e *Event) { e.Seq = 0 }, wantErr: "seq is required"},
		{name: "missing ts", mutate: func(e *Event) { e.TS = Timestamp{} }, wantErr: "ts is required"},
		{name: "unknown kind", mutate: func(e *Event) { e.Kind = "banana" }, wantErr: "unknown kind"},
		{name: "unknown status", mutate: func(e *Event) { e.Status = "nearly" }, wantErr: "unknown status"},

		// C5 -- detail is ASCII English, one line
		{
			name:    "korean detail",
			mutate:  func(e *Event) { e.Detail = "노드 준비 완료" },
			wantErr: "printable ASCII",
		},
		{
			name:    "detail with newline",
			mutate:  func(e *Event) { e.Detail = "line one\nline two" },
			wantErr: "printable ASCII",
		},

		// C3 -- no terminal rendering anywhere in the stream
		{
			name:    "ansi in detail",
			mutate:  func(e *Event) { e.Detail = "\x1b[32mok\x1b[0m" },
			wantErr: "ANSI escape",
		},
		{
			name:    "ansi in evidence",
			mutate:  func(e *Event) { e.Evidence = "\x1b[1mBOLD\x1b[0m" },
			wantErr: "ANSI escape",
		},
		{
			name:    "progress bar drawn into detail",
			mutate:  func(e *Event) { e.Detail = "installing"; e.Evidence = "████████░░ 8/12" },
			wantErr: "progress-bar glyphs",
		},

		// progress is a fraction, and a sane one
		{
			name:    "progress total zero",
			mutate:  func(e *Event) { e.Progress = &Progress{Done: 1, Total: 0} },
			wantErr: "progress.total must be positive",
		},
		{
			name:    "progress done past total",
			mutate:  func(e *Event) { e.Progress = &Progress{Done: 13, Total: 12} },
			wantErr: "out of range",
		},
		{name: "progress ok", mutate: func(e *Event) { e.Progress = &Progress{Done: 8, Total: 12} }},

		// retries
		{
			name:    "attempt beyond max",
			mutate:  func(e *Event) { e.Attempt, e.MaxAttempts = 4, 3 },
			wantErr: "exceeds maxAttempts",
		},
		{name: "retry ok", mutate: func(e *Event) { e.Attempt, e.MaxAttempts = 2, 3 }},

		// C4 -- codes must exist in the registry
		{
			name:    "unregistered code",
			mutate:  func(e *Event) { e.Code = "PF-908" },
			wantErr: "not registered",
		},
		{
			name:    "retired code stays retired",
			mutate:  func(e *Event) { e.Code = "PF-999" },
			wantErr: "not registered",
		},
		{name: "registered code", mutate: func(e *Event) { e.Code = "PF-204" }},

		// §6 -- a failure without a code is untraceable and untranslatable
		{
			name:    "failed without code",
			mutate:  func(e *Event) { e.Status = StatusFailed },
			wantErr: "requires a code",
		},
		{
			name:   "failed with code",
			mutate: func(e *Event) { e.Status, e.Code = StatusFailed, "PF-601" },
		},
		{
			name:    "blocked without code",
			mutate:  func(e *Event) { e.Status = StatusBlocked },
			wantErr: "requires a code",
		},

		// per-kind requirements
		{
			name: "log without level",
			mutate: func(e *Event) {
				*e = Event{TS: e.TS, Run: e.Run, Seq: e.Seq, Kind: KindLog, Detail: "pulling image"}
			},
			wantErr: "requires a valid level",
		},
		{
			name: "log with status",
			mutate: func(e *Event) {
				*e = Event{TS: e.TS, Run: e.Run, Seq: e.Seq, Kind: KindLog,
					Level: LevelInfo, Status: StatusOK, Detail: "x"}
			},
			wantErr: "must not carry a status",
		},
		{
			name:    "level on non-log kind",
			mutate:  func(e *Event) { e.Level = LevelInfo },
			wantErr: "applies to kind=log only",
		},
		{
			name: "probe without code",
			mutate: func(e *Event) {
				*e = Event{TS: e.TS, Run: e.Run, Seq: e.Seq, Kind: KindProbe, Status: StatusOK}
			},
			wantErr: "kind=probe requires a code",
		},
		{
			name: "decision without code",
			mutate: func(e *Event) {
				*e = Event{TS: e.TS, Run: e.Run, Seq: e.Seq, Kind: KindDecision, Detail: "downgraded"}
			},
			wantErr: "kind=decision requires a code",
		},
		{
			name: "artifact without detail",
			mutate: func(e *Event) {
				*e = Event{TS: e.TS, Run: e.Run, Seq: e.Seq, Kind: KindArtifact}
			},
			wantErr: "requires detail to carry the path",
		},
		{
			name: "phase without status",
			mutate: func(e *Event) {
				*e = Event{TS: e.TS, Run: e.Run, Seq: e.Seq, Kind: KindPhase, Phase: "verify"}
			},
			wantErr: "requires a status",
		},
		{
			name: "phase without phase id",
			mutate: func(e *Event) {
				*e = Event{TS: e.TS, Run: e.Run, Seq: e.Seq, Kind: KindPhase, Status: StatusOK}
			},
			wantErr: "kind=phase requires phase",
		},
		{
			name:    "step without step id",
			mutate:  func(e *Event) { e.Step = "" },
			wantErr: "kind=step requires step",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := valid()
			tc.mutate(&e)
			errs := e.Validate()

			if tc.wantErr == "" {
				if len(errs) > 0 {
					t.Fatalf("expected valid, got %v", errors.Join(errs...))
				}
				return
			}
			joined := errors.Join(errs...)
			if joined == nil {
				t.Fatalf("expected error containing %q, got none", tc.wantErr)
			}
			if !strings.Contains(joined.Error(), tc.wantErr) {
				t.Errorf("expected error containing %q, got: %v", tc.wantErr, joined)
			}
		})
	}
}

// TestScreenElementsFromADR002 is acceptance criterion C6: every screen element
// in the ADR-002 mockup must be drawable from the stream alone. If one of these
// cannot be expressed as a valid event, the schema is short and the TUI would
// have to reach into the engine for the value -- which is how ADR-002 breaks in
// practice.
func TestScreenElementsFromADR002(t *testing.T) {
	tests := []struct {
		screen string
		ev     Event
	}{
		{
			screen: "[########..] 8/12  10.10.0.11 probing PF-204",
			ev: Event{Kind: KindProbe, Phase: "preflight", Node: "10.10.0.11",
				Code: "PF-204", Status: StatusRunning, Progress: &Progress{Done: 8, Total: 12}},
		},
		{
			screen: "x 10.10.0.12 (eBPF)",
			ev: Event{Kind: KindProbe, Phase: "preflight", Node: "10.10.0.12",
				Code: "PF-204", Status: StatusFailed,
				Detail:   "loading a minimal eBPF program failed",
				Evidence: "bpf(BPF_PROG_LOAD): Operation not permitted"},
		},
		{
			screen: "downgrade: cilium-gw -> canal-traefik",
			ev: Event{Kind: KindDecision, Phase: "plan", Code: "DG-001",
				Detail: "requested cilium-gw downgraded to canal-traefik; triggered by PF-204 on 10.10.0.12"},
		},
		{
			screen: "L0 node prep      done",
			ev:     Event{Kind: KindPhase, Phase: "l0-node-prep", Status: StatusOK},
		},
		{
			screen: "L1 rke2 server    ####.. retry 1/3",
			ev: Event{Kind: KindStep, Phase: "l1-bootstrap", Step: "rke2-server-ready",
				Node: "10.10.0.11", Status: StatusRunning,
				Attempt: 1, MaxAttempts: 3, Progress: &Progress{Done: 3, Total: 7}},
		},
		{
			screen: "L2 addons         pending",
			ev:     Event{Kind: KindPhase, Phase: "l2-platform", Status: StatusPending},
		},
		{
			screen: "[log] 10.10.0.11 pulling rke2-runtime...",
			ev: Event{Kind: KindLog, Phase: "l1-bootstrap", Node: "10.10.0.11",
				Level: LevelInfo, Detail: "pulling rke2-runtime image"},
		},
		{
			screen: "done: artifact path",
			ev:     Event{Kind: KindArtifact, Detail: "./out/runs/01J/artifacts/audit-report.md"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.screen, func(t *testing.T) {
			e := tc.ev
			e.TS = NewTimestamp(fixedClock()())
			e.Run = "01JBQ8F2K3M5N7P9R1S3T5V7W9"
			e.Seq = 1
			if errs := e.Validate(); len(errs) > 0 {
				t.Errorf("screen element is not expressible: %v", errors.Join(errs...))
			}
		})
	}
}

func TestTimestampMarshalsWithMillisecondPrecision(t *testing.T) {
	ts := NewTimestamp(time.Date(2026, 8, 3, 9, 4, 11, 220999999, time.UTC))
	got, err := ts.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	want := `"2026-08-03T09:04:11.220Z"`
	if string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestTimestampRoundTrip(t *testing.T) {
	orig := NewTimestamp(time.Date(2026, 8, 3, 9, 4, 11, 220*int(time.Millisecond), time.UTC))
	raw, err := orig.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var back Timestamp
	if err := back.UnmarshalJSON(raw); err != nil {
		t.Fatal(err)
	}
	if !back.Equal(orig.Time) {
		t.Errorf("round trip changed the value: %v -> %v", orig.Time, back.Time)
	}
}

func TestStatusTerminal(t *testing.T) {
	tests := map[Status]bool{
		StatusPending: false, StatusRunning: false,
		StatusOK: true, StatusSkipped: true, StatusFailed: true, StatusBlocked: true,
	}
	for s, want := range tests {
		if got := s.Terminal(); got != want {
			t.Errorf("%s.Terminal() = %v, want %v", s, got, want)
		}
	}
}
