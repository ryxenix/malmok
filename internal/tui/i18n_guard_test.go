package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ryxen/malmok/internal/event"
)

// The run list is the screen an operator opens when something has already gone
// wrong, and it was the last place English survived the move into catalogues:
// "2 phases, 1 failed" was assembled from literals while every other line came
// from a key. This renders one against both catalogues, because a test that
// only checked the key existed would have passed all along -- nothing was
// calling T().
func TestTheRunSummarySpeaksTheChosenLanguage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	// Written through the real writer rather than by hand: the reader stops at
	// a sequence gap, so a hand-typed stream reads as a truncated run.
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := event.NewWriter(f, "01JBQ8F2K3M5N7P9R1S3T5V7W9")
	for _, e := range []event.Event{
		{Kind: event.KindPhase, Phase: "l0-node-prep", Status: event.StatusOK},
		{Kind: event.KindPhase, Phase: "l1-bootstrap", Status: event.StatusOK},
		{Kind: event.KindStep, Phase: "l2-dataplane", Step: "cilium-ready", Status: event.StatusFailed, Code: "EX-101"},
		{Kind: event.KindRun, Status: event.StatusFailed, Code: "EX-101"},
	} {
		if _, err := w.Emit(e); err != nil {
			t.Fatalf("the fixture is not a valid event: %v", err)
		}
	}
	f.Close()

	for _, tc := range []struct {
		lang Lang
		want []string
	}{
		{LangEN, []string{"2 phases", "1 failed"}},
		{LangKO, []string{"2 단계 완료", "1 실패"}},
	} {
		cat, err := LoadCatalogue(tc.lang)
		if err != nil {
			t.Fatal(err)
		}
		got := summarise(path, cat)
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("%s summary %q does not contain %q", tc.lang, got, want)
			}
		}
	}
}

// The tool was renamed, and the rename missed an environment variable: the
// documented way to force the fallback character set was still read under the
// old name, so setting it did nothing. A name that survives a rename in one
// place survives it in others, so this looks at the whole tree.
//
// The needle is assembled rather than written out, because a test that spells
// the old name fails on its own source.
func TestNothingStillAnswersToTheOldName(t *testing.T) {
	old := "platform" + "ctl"
	root := filepath.Join("..", "..")

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "bin", "out", "dist", "img":
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".go", ".yaml", ".yml", ".sh", ".md":
		default:
			return nil
		}
		// The changelog records what the tool used to be called, which is the
		// one place the old name belongs.
		if filepath.Base(path) == "CHANGELOG.md" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if strings.Contains(strings.ToLower(string(raw)), old) {
			t.Errorf("%s still refers to the old name", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
