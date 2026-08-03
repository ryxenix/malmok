package event

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Writer serialises events to an append-only JSONL stream and owns sequence
// allocation. See docs/11-execute.md §5 and §5.6.
//
// A line, once written, is never rewritten: a renderer is tailing the file, so
// rewriting is the same as corrupting its view.
type Writer struct {
	mu    sync.Mutex
	w     io.Writer
	sync  func() error
	run   string
	seq   uint64
	clock func() time.Time
}

// Option configures a Writer.
type Option func(*Writer)

// WithClock replaces the time source. Tests use it to get deterministic
// timestamps; nothing in production should.
func WithClock(f func() time.Time) Option { return func(w *Writer) { w.clock = f } }

// WithStartSeq resumes numbering after an existing stream. OpenFile sets this
// automatically; callers writing to a plain io.Writer set it themselves.
func WithStartSeq(last uint64) Option { return func(w *Writer) { w.seq = last } }

// NewWriter writes events for runID to w.
func NewWriter(w io.Writer, runID string, opts ...Option) *Writer {
	ew := &Writer{w: w, run: runID, clock: time.Now}
	for _, opt := range opts {
		opt(ew)
	}
	return ew
}

// Emit stamps e with the run id, the next sequence number and the current time,
// validates it, and appends one line.
//
// Validation happens here rather than at the boundary of some later consumer so
// that a malformed event never reaches the file. An event file is an audit
// artifact handed to a customer; it does not get to contain garbage.
func (w *Writer) Emit(e Event) (Event, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	e.Run = w.run
	e.Seq = w.seq + 1
	if e.TS.IsZero() {
		e.TS = NewTimestamp(w.clock())
	}

	if errs := e.Validate(); len(errs) > 0 {
		return e, fmt.Errorf("event %d: %w", e.Seq, errors.Join(errs...))
	}

	line, err := json.Marshal(e)
	if err != nil {
		return e, fmt.Errorf("event %d: marshal: %w", e.Seq, err)
	}
	if _, err := w.w.Write(append(line, '\n')); err != nil {
		return e, fmt.Errorf("event %d: write: %w", e.Seq, err)
	}

	// Only now is the sequence consumed. A failed Emit must not leave a hole,
	// because C1 requires the numbering to be gapless and a renderer treats a
	// gap as lost data (§5.6).
	w.seq = e.Seq

	if w.sync != nil {
		if err := w.sync(); err != nil {
			return e, fmt.Errorf("event %d: sync: %w", e.Seq, err)
		}
	}
	return e, nil
}

// LastSeq returns the sequence number of the most recently written event.
func (w *Writer) LastSeq() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.seq
}

// Run returns the run id this Writer stamps onto every event.
func (w *Writer) Run() string { return w.run }

// ---------------------------------------------------------------------------
// File-backed writer
// ---------------------------------------------------------------------------

// FileWriter is a Writer bound to a file on disk.
type FileWriter struct {
	*Writer
	f *os.File
}

// OpenFile opens path for appending and continues runID's numbering from
// whatever is already there.
//
// Resuming into an existing file is the normal case, not an edge case: an
// interrupted run reattaches to the same stream (§4.2), and when
// output.eventLog names a fixed path several runs share one file, separated by
// the run field (§1.1).
//
// If the file does not end in a newline the last line is a partial write from a
// process that died mid-emit. It is truncated, because appending after it would
// produce one corrupt line that no reader can parse.
func OpenFile(path, runID string, opts ...Option) (*FileWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("event: create directory: %w", err)
	}

	last, err := repairAndScan(path, runID)
	if err != nil {
		return nil, err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("event: open %s: %w", path, err)
	}

	opts = append([]Option{WithStartSeq(last)}, opts...)
	w := NewWriter(f, runID, opts...)
	w.sync = f.Sync
	return &FileWriter{Writer: w, f: f}, nil
}

// Close closes the underlying file.
func (fw *FileWriter) Close() error { return fw.f.Close() }

// Path returns the file being written.
func (fw *FileWriter) Path() string { return fw.f.Name() }

// repairAndScan truncates a trailing partial line and returns the highest
// sequence number already recorded for runID.
func repairAndScan(path, runID string) (uint64, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("event: open %s: %w", path, err)
	}
	defer f.Close()

	var (
		last     uint64
		complete int64 // offset just past the last newline
		r        = bufio.NewReaderSize(f, 64*1024)
	)

	// ReadBytes rather than a Scanner, because only ReadBytes distinguishes "a
	// line that ended in a newline" from "whatever was left at EOF". A Scanner
	// hands back the unterminated tail as though it were a line, and counting
	// its length would make the truncation below EXTEND the file with NUL bytes
	// instead of trimming it.
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > maxLineBytes {
			return 0, fmt.Errorf("event: %s: line exceeds %d bytes", path, maxLineBytes)
		}
		if err == nil {
			complete += int64(len(line))
			var e Event
			if jsonErr := json.Unmarshal(line, &e); jsonErr == nil {
				if e.Run == runID && e.Seq > last {
					last = e.Seq
				}
			}
			// A foreign or corrupt line is tolerated: it is not ours to
			// renumber, and it is already durably written.
			continue
		}
		if errors.Is(err, io.EOF) {
			// A non-empty tail without a newline is a partial write from a
			// process that died mid-emit. Neither counted nor parsed.
			break
		}
		return 0, fmt.Errorf("event: read %s: %w", path, err)
	}

	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, fmt.Errorf("event: seek %s: %w", path, err)
	}
	if size > complete {
		if err := f.Truncate(complete); err != nil {
			return 0, fmt.Errorf("event: truncate partial line in %s: %w", path, err)
		}
	}
	return last, nil
}
