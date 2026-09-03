package gateway

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// kubectl's jsonpath parser reads \n as an escape inside a quoted string and
// rejects a real newline there: "unterminated quoted string". Both forms look
// identical in a Go raw string, which is how one reached a node -- the
// gateway-nodes step read an empty list, labelled nothing, exited 0, and
// retried three times reporting that the target state was not reached without
// ever printing the parse error, because stderr went to /dev/null.
//
// The whole tree is scanned rather than this package's own strings: the
// idiom is copied between phases, and the next copy is the one that matters.
func TestNoRawNewlineInsideJSONPath(t *testing.T) {
	root := repoRoot(t)

	// A jsonpath argument, single-quoted for the shell, up to its closing
	// quote. Non-greedy so two on one line stay two.
	expr := regexp.MustCompile(`(?s)jsonpath='(.*?)'`)

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range expr.FindAllSubmatchIndex(src, -1) {
			arg := string(src[m[2]:m[3]])
			// Only a newline inside the jsonpath's own {"..."} matters. A
			// newline between concatenated Go string pieces is source layout.
			for _, q := range regexp.MustCompile(`(?s)\{"(.*?)"\}`).FindAllStringSubmatch(arg, -1) {
				if strings.Contains(q[1], "\n") {
					rel, _ := filepath.Rel(root, path)
					line := strings.Count(string(src[:m[2]]), "\n") + 1
					t.Errorf(`%s:%d: jsonpath has a real newline inside {"..."}; write {"\n"}`, rel, line)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for range 6 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("go.mod not found above the test's directory")
	return ""
}
