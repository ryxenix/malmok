package engine

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEngineDoesNotImportTUI is acceptance criterion D3, and the enforcement
// CLAUDE.md promises: the dependency runs one way only.
//
// ADR-002 exists because the previous generation of this tool had installation
// logic inside its TUI, and that is what made it un-maintainable. The rule is
// not a note in a document; it is this test. A renderer subscribes to the event
// stream — it is never reached from here.
func TestEngineDoesNotImportTUI(t *testing.T) {
	const forbidden = "github.com/ryxenix/malmok/internal/tui"

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if path == forbidden || strings.HasPrefix(path, forbidden+"/") {
				t.Errorf("%s imports %s; the engine must not depend on a renderer (ADR-002)",
					e.Name(), path)
			}
		}
	}
}

// The engine must also stay free of terminal libraries. Reaching for one is how
// rendering creeps back in through a side door — a spinner here, a colour
// there — and each one is a code path that needs a terminal to finish an
// install, which §7 E1 forbids outright.
func TestEngineDoesNotImportTerminalLibraries(t *testing.T) {
	forbidden := []string{
		"github.com/charmbracelet/",
		"charm.land/",
		"github.com/spf13/cobra",
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Base(e.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			for _, bad := range forbidden {
				if strings.HasPrefix(path, bad) {
					t.Errorf("%s imports %s; the engine renders nothing", e.Name(), path)
				}
			}
		}
	}
}
