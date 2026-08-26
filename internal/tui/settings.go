package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ryxen/malmok/internal/spec"
)

// Editing a document that already exists.
//
// The install flow builds a specification out of answers; this reads one back,
// walks the same screens with the values already in them, and writes it out.
// The screens in between are shared on purpose -- a second set of fields for
// the same values is a second place for the validation to disagree with itself.
//
// What makes it safe is that the loaded document is kept whole. The wizard has
// no screen for gateway listeners, etcd snapshots or the GitOps repository, and
// Config.ApplyTo writes only over what it owns. A document rebuilt from the
// answers would have deleted everything the operator was never shown.

// docEntry is one document offered on the open screen.
type docEntry struct {
	Path string
	When string
	// Note warns about a file that is not an ordinary document, or is empty.
	Note string
}

// listDocuments finds the documents worth offering.
//
// The working directory and the bundle's run directories, which are the two
// places a cluster.yaml is: the one being worked on, and the snapshots the runs
// kept. Nothing recursive -- a wizard that walked the filesystem would turn a
// misdirected search into a long pause with no explanation.
func (w *Wizard) listDocuments() []docEntry {
	seen := map[string]bool{}
	var out []docEntry

	add := func(path, note string) {
		abs, err := filepath.Abs(path)
		if err != nil || seen[abs] {
			return
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			return
		}
		seen[abs] = true
		out = append(out, docEntry{
			Path: path,
			When: info.ModTime().Local().Format("2006-01-02 15:04"),
			Note: note,
		})
	}

	for _, pattern := range []string{"*.yaml", "*.yml"} {
		matches, _ := filepath.Glob(pattern)
		for _, m := range matches {
			add(m, "")
		}
	}

	// A run's snapshot is a document, and it is also the thing a resume
	// compares against. Offering it without saying so would let an operator
	// edit away their own ability to resume the run it belongs to.
	root := w.bundle
	if root == "" {
		root = "./out"
	}
	entries, _ := os.ReadDir(filepath.Join(root, "runs"))
	for _, e := range entries {
		if e.IsDir() {
			add(filepath.Join(root, "runs", e.Name(), spec.FileName), "doc.snapshot")
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// openScreen asks which document to edit.
func (w *Wizard) openScreen(width int) (string, string, string) {
	// The same screen serves both flows and they are not doing the same thing:
	// one is about to rewrite this file, the other is about to restart every
	// node the file names.
	help := "open.help"
	if w.mode == modeUpgrade {
		help = "open.help.upgrade"
	}

	var b strings.Builder
	b.WriteString(w.inlineHelp(w.cat.T(help), width))

	// The path field first, because typing one is always available even when
	// nothing was found to list.
	cur := w.cursor[StepOpen]
	w.fields(&b,
		w.labels(StepOpen), w.maskedValues(StepOpen), cur, w.editing, width)

	if len(w.openFiles) > 0 {
		b.WriteString("\n" + w.dim(w.cat.T("open.found"), width) + "\n")
		// The path is given whatever the date does not need, and the warning
		// goes on its own line: a note long enough to say what is lost is long
		// enough to run out of the pane if it shares a row.
		pathWidth := max(width-24, 16)
		for i, d := range w.openFiles {
			marker := "  "
			label := padCells(truncCells(d.Path, pathWidth), pathWidth) + d.When
			if cur == i+1 {
				marker = w.theme.Accent.Render(w.glyphs.Focus) + " "
				b.WriteString(marker + w.theme.ChoiceSel.Render(label) + "\n")
			} else {
				b.WriteString(marker + w.theme.Body.Render(label) + "\n")
			}
			if d.Note != "" {
				b.WriteString("    " + w.dim(wrapCells(w.cat.T(d.Note), width-4), width) + "\n")
			}
		}
	} else {
		b.WriteString("\n" + w.dim(wrapCells(w.cat.T("open.none"), width), width) + "\n")
	}

	status := w.cat.T("open.hint")
	if w.openErr != "" {
		// The loader's own words. A document that will not parse says which
		// line it gave up on, and restating that as "could not be read" throws
		// away the only part an operator can act on.
		return w.cat.T("open.heading"), b.String() + "\n" +
			w.theme.Err.Render(w.glyphs.Failed+" "+wrapCells(w.openErr, width)), status
	}
	return w.cat.T("open.heading"), b.String(), status
}

// saveScreen shows what is about to be written, and then what was.
func (w *Wizard) saveScreen(width int) (string, string, string) {
	var b strings.Builder

	if w.saved != "" {
		b.WriteString(w.theme.Accent.Render(w.glyphs.OK+" "+w.cat.T("save.written")) + "\n")
		b.WriteString(w.theme.Body.Render("  "+w.saved) + "\n\n")
		b.WriteString(w.dim(wrapCells(w.cat.T("save.after"), width), width) + "\n")
		return w.cat.T("save.heading"), b.String(), ""
	}

	b.WriteString(w.inlineHelp(w.cat.T("save.help"), width))
	for _, r := range append(w.builtRows(), [2]string{w.cat.T("save.path"), w.cfg.DocPath}) {
		b.WriteString("  " + w.theme.Dim.Render(padCells(r[0], 16)) +
			w.theme.Body.Render(truncCells(r[1], max(width-20, 10))) + "\n")
	}

	// The same validation the summary screen runs, for the same reason: a
	// malformed field is worth saying while the operator is still in front of
	// the screen that produced it.
	if problems := w.problems(); len(problems) > 0 {
		b.WriteString("\n" + w.theme.Err.Render(w.glyphs.Failed+" "+w.cat.T("summary.invalid")) + "\n")
		for _, pr := range problems {
			b.WriteString("  " + w.theme.Body.Render("["+w.cat.T(stepKeys[pr.Step])+"] ") + "\n")
			for _, line := range strings.Split(wrapCells(pr.Text, width-6), "\n") {
				b.WriteString("    " + w.theme.Dim.Render(line) + "\n")
			}
		}
	} else {
		b.WriteString("\n" + w.theme.Accent.Render(w.glyphs.OK+" "+w.cat.T("summary.valid")) + "\n")
	}

	if w.saveErr != "" {
		b.WriteString("\n" + w.theme.Err.Render(w.glyphs.Failed+" "+wrapCells(w.saveErr, width)) + "\n")
	}

	hint := "save.hint"
	if _, broken := w.firstProblemStep(); broken {
		hint = "hint.fix"
	}
	return w.cat.T("save.heading"), b.String(), w.cat.T(hint)
}

// loadDocument reads the chosen file into the wizard's values.
//
// Both halves matter: the whole document is kept so that writing it back
// preserves what no screen shows, and the subset the screens do show is copied
// into Config so the operator edits values rather than retyping them.
func (w *Wizard) loadDocument() error {
	path := strings.TrimSpace(w.cfg.DocPath)
	if path == "" {
		return fmt.Errorf("%s", w.cat.T("open.err.empty"))
	}

	doc, err := spec.Load(path)
	if err != nil {
		return err
	}
	// The profile's own values are filled in before the screens read them.
	//
	// The wizard composes axis by axis and the profile is what the composition
	// matches, so a document that left its axes to the profile would come back
	// with them empty -- and be written out as `custom`, having silently lost
	// the baseline it was relying on. Resolving first means the screens show
	// what the document effectively is, and writing it back says the same
	// thing.
	if _, err := doc.ApplyProfile(); err != nil {
		return err
	}

	lang, pass := w.cfg.Lang, w.cfg.SSHPassword
	w.doc = doc.Spec
	w.cfg = FromSpec(doc.Spec)
	// Neither is in a document: the language belongs to this session and the
	// password is deliberately never serialised.
	w.cfg.Lang, w.cfg.SSHPassword, w.cfg.DocPath = lang, pass, path

	// The cursors are per step and the previous flow left its own behind. A
	// screen that opened with the cursor on a row the new document does not
	// have would look like a selection nobody made.
	w.cursor = map[Step]int{}
	return nil
}

// writeDocument renders the edited document back to where it came from.
func (w *Wizard) writeDocument() error {
	path := strings.TrimSpace(w.cfg.DocPath)
	if path == "" {
		return fmt.Errorf("%s", w.cat.T("open.err.empty"))
	}

	// Applied onto the document that was loaded, not onto an empty one. This is
	// the line that keeps a gateway listener the wizard never asked about.
	edited := w.doc
	w.cfg.ApplyTo(&edited)

	if err := spec.Save(path, edited); err != nil {
		return err
	}
	// Kept, so a later edit in the same session starts from what was written
	// rather than from what was read.
	w.doc = edited
	w.saved = path
	return nil
}

// targetScreen asks which version to move to.
//
// It shows what the document says the cluster is on now, and nothing else: an
// upgrade changes the version, and a screen that also offered the dataplane and
// the storage driver would invite a change this flow has no way to apply.
func (w *Wizard) targetScreen(width int) (string, string, string) {
	var b strings.Builder
	b.WriteString(w.inlineHelp(w.cat.T("target.help"), width))

	from := w.doc.Kubernetes.Version
	if from == "" {
		from = w.cat.T("target.unknown")
	}
	for _, r := range [][2]string{
		{w.cat.T("target.document"), w.cfg.DocPath},
		{w.cat.T("target.from"), from},
	} {
		b.WriteString("  " + w.theme.Dim.Render(padCells(r[0], 16)) +
			w.theme.Body.Render(truncCells(r[1], max(width-20, 10))) + "\n")
	}

	b.WriteString("\n")
	cur := w.cursor[StepTarget]
	w.fields(&b,
		w.labels(StepTarget), w.maskedValues(StepTarget), cur, w.editing, width)

	if h := w.inlineHint(int(StepTarget), cur); h != "" {
		b.WriteString("\n" + w.dim(w.glyphs.Dot+" "+h, width))
	}

	// Said before it happens rather than after. Every node restarts, one at a
	// time, and an operator who did not expect that finds out from a workload
	// rather than from this screen.
	if note := w.inlineHelp(w.cat.T("target.note"), width); note != "" {
		b.WriteString("\n\n" + strings.TrimSuffix(note, "\n\n"))
	}

	hint := "hint.edit"
	if w.editing {
		hint = "hint.editing"
	}
	return w.cat.T("target.heading"), b.String(), w.cat.T(hint)
}
