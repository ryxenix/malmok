package rke2

import (
	"strings"
	"testing"
)

// A manifest is sent as bytes, not as part of a command.
//
// It used to be quoted into both halves of the step. That is fine for the few
// kilobytes a HelmChart usually is, and it is not fine for one carrying a
// chart archive: measured against a node, a command of 128KB dies on the
// argument-length limit and one of 256KB has the connection dropped before
// anything runs. cert-manager embedded as chartContent is 200KB and the
// metrics stack is 435KB, so registry.chartDir did not work at all -- and the
// failure read as the node going away.
func TestAManifestTravelsOnStdin(t *testing.T) {
	body := "apiVersion: v1\nkind: ConfigMap\n" + strings.Repeat("# padding\n", 40000)
	s := ManifestStep("l2-test", "big", ManifestDir+"/big.yaml", body, "", 0)

	if string(s.Input) != body {
		t.Errorf("the step carries %d bytes of input, the manifest is %d", len(s.Input), len(body))
	}
	for _, program := range []string{s.Check, s.Do} {
		if strings.Contains(program, "padding") {
			t.Errorf("the manifest is inside the command:\n%.200s", program)
		}
		if len(program) > 4096 {
			t.Errorf("a command of %d bytes; the body belongs on stdin", len(program))
		}
	}
	if !strings.Contains(s.Do, "cat > ") {
		t.Error("Do does not read the manifest from stdin")
	}
	if !strings.Contains(s.Check, "cmp -s - ") {
		t.Error("Check does not compare against stdin")
	}
}
