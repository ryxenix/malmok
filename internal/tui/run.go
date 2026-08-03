package tui

import (
	"context"
	"fmt"
	"io"

	tea "charm.land/bubbletea/v2"
)

// Options configures a screen.
type Options struct {
	RunID string
	Mode  Mode

	// ASCII forces the fallback character set. Leave false to auto-detect.
	ASCII bool

	// Lang selects the catalogue. Empty means English (ADR-009).
	Lang Lang

	// Output and Input default to the terminal. Tests supply their own.
	Output io.Writer
	Input  io.Reader
}

// Screen is a running TUI. It is an attach.Sink, so the same follower that
// feeds the text renderer feeds this.
type Screen struct {
	model   *Model
	program *tea.Program
}

// NewScreen builds a screen without starting it, so a caller can hand it to a
// follower before the terminal is taken over.
func NewScreen(ctx context.Context, o Options) (*Screen, error) {
	lang := o.Lang
	if lang == "" {
		lang = LangEN
	}
	m, err := New(o.RunID, o.Mode, o.ASCII, lang)
	if err != nil {
		return nil, err
	}

	opts := []tea.ProgramOption{tea.WithContext(ctx)}
	if o.Output != nil {
		opts = append(opts, tea.WithOutput(o.Output))
	}
	if o.Input != nil {
		opts = append(opts, tea.WithInput(o.Input))
	}

	p := tea.NewProgram(m, opts...)
	m.Attach(p)
	return &Screen{model: m, program: p}, nil
}

// Run takes over the terminal until the operator quits or ctx is cancelled.
func (s *Screen) Run() error {
	if _, err := s.program.Run(); err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	return nil
}

// Quit asks the screen to close, used when the run finishes on its own.
func (s *Screen) Quit() { s.program.Quit() }

// Sink returns the model, which implements attach.Sink. Events are posted into
// the program's own loop rather than mutating the screen from the follower's
// goroutine.
func (s *Screen) Sink() *Model { return s.model }

// Detached reports whether the operator left the engine running.
func (s *Screen) Detached() bool { return s.model.Detached() }
