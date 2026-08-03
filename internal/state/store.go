package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// FileName is the conventional name inside a run directory (§1.1).
const FileName = "state.json"

// Load reads a state file.
func Load(path string) (*State, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("state: read %s: %w", path, err)
	}

	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("state: parse %s: %w", path, err)
	}
	if s.Run == "" {
		return nil, fmt.Errorf("state: %s has no run id", path)
	}
	if s.Phases == nil {
		s.Phases = map[string]*Phase{}
	}
	for _, p := range s.Phases {
		if p.Steps == nil {
			p.Steps = map[string]*Step{}
		}
	}
	return &s, nil
}

// LoadIfExists returns (nil, nil) when the file is absent, which is the normal
// first-run case rather than an error.
func LoadIfExists(path string) (*State, error) {
	s, err := Load(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return s, err
}

// Save writes the state atomically: a temporary file in the same directory,
// fsynced, then renamed over the target, then the directory fsynced.
//
// Plain truncate-and-write loses the file outright if the process dies during
// it -- and this is written after every step, so "during it" is a large share
// of a three-hour install. Losing it turns a resumable run into one that has to
// start over, which is the thing this package exists to prevent.
//
// The rename must land in the same directory as the target: across filesystems
// it is a copy, not an atomic replace.
func (s *State) Save(path string) error {
	snap := s.Snapshot()

	body, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("state: marshal: %w", err)
	}
	body = append(body, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("state: create %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".state-*.json")
	if err != nil {
		return fmt.Errorf("state: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return fmt.Errorf("state: write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("state: sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("state: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("state: rename into place: %w", err)
	}

	// Without this the rename itself can be lost on power failure even though
	// the file contents were durable.
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("state: open %s for sync: %w", dir, err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		// Some filesystems refuse to fsync a directory. The rename is already
		// durable on every filesystem we support, so this is not fatal.
		return nil
	}
	return nil
}

// Path returns the state file path inside a run directory.
func Path(runDir string) string { return filepath.Join(runDir, FileName) }
