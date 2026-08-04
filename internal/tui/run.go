package tui

import (
	"context"
	"fmt"
	"io"

	tea "charm.land/bubbletea/v2"
)

// Options configures the installer screen.
type Options struct {
	RunID string
	// RunDir is where the run's artifacts landed. The final screen has to name
	// it: a run id alone tells an operator nothing they can act on.
	RunDir string

	// ASCII forces the fallback character set; leave false to auto-detect.
	ASCII bool
	// Mono drops colour. NO_COLOR and monochrome consoles are both real.
	Mono bool
	// Lang selects the catalogue. Empty means English (ADR-009).
	Lang Lang

	// HideRail drops the step list, which is the layout Proxmox and the Ubuntu
	// server installer use. With it, the layout is the Ubuntu desktop
	// installer's. Both are offered because operators arrive from both.
	HideRail bool

	// Preflight and Install are the long operations the wizard drives. Both
	// report progress through the event stream rather than through their
	// return value; the error is only used to decide what to show next.
	Preflight Work
	Install   Work

	Output io.Writer
	Input  io.Reader
}

// Screen is a running installer.
type Screen struct {
	wizard  *Wizard
	program *tea.Program
}

// NewScreen builds the screen without starting it, so a follower can be pointed
// at it before the terminal is taken over.
func NewScreen(ctx context.Context, o Options) (*Screen, error) {
	lang := o.Lang
	if lang == "" {
		lang = LangEN
	}
	wz, err := NewWizard(o.RunID, o.ASCII, o.Mono, lang, o.Preflight, o.Install)
	if err != nil {
		return nil, err
	}
	wz.hideRail = o.HideRail
	wz.runDir = o.RunDir

	opts := []tea.ProgramOption{tea.WithContext(ctx)}
	if o.Output != nil {
		opts = append(opts, tea.WithOutput(o.Output))
	}
	if o.Input != nil {
		opts = append(opts, tea.WithInput(o.Input))
	}

	p := tea.NewProgram(wz, opts...)
	wz.Attach(p)
	return &Screen{wizard: wz, program: p}, nil
}

// Run takes over the terminal until the operator closes the installer or ctx is
// cancelled.
func (s *Screen) Run() error {
	if _, err := s.program.Run(); err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	return nil
}

// Quit closes the screen from outside.
func (s *Screen) Quit() { s.program.Quit() }

// Sink returns the wizard, which implements attach.Sink. Events are posted into
// the program's own loop rather than mutating the screen from the follower's
// goroutine.
func (s *Screen) Sink() *Wizard { return s.wizard }

// Aborted reports whether the operator stopped the run.
func (s *Screen) Aborted() bool { return s.wizard.Aborted() }
