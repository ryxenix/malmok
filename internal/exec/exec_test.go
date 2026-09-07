package exec

import (
	"context"
	"strings"
	"testing"
)

// Sudo wraps every runner that reaches a node, so anything Sudo does not
// implement is something no step gets over SSH.
//
// RunStream was one of them. Embedding an interface promotes that interface's
// methods and no others, so Sudo satisfied Runner and not Streamer, and
// ShellStep's `s.Runner.(exec.Streamer)` failed for every step on every node.
// Every wait ran silently: the lines a wait prints so an operator can tell it
// from a hang were never carried out, and a wait that failed after fifteen
// minutes left nothing behind saying what it had seen. The unit tests passed
// throughout, because they hand ShellStep a runner that is not wrapped.
func TestSudoCarriesEveryCapabilityOfWhatItWraps(t *testing.T) {
	var s any = Sudo{Runner: &Fake{}}
	if _, ok := s.(Streamer); !ok {
		t.Error("Sudo is not a Streamer, so no step reached over SSH streams its progress")
	}
	if _, ok := s.(Feeder); !ok {
		t.Error("Sudo is not a Feeder, so no step reached over SSH can send a file")
	}
	if _, ok := s.(Runner); !ok {
		t.Error("Sudo is not a Runner")
	}
}

// The elevation has to be identical whichever way the command is run.
// Splitting it is what let RunStream not exist for as long as it did.
func TestEveryWayOfRunningElevatesTheSameWay(t *testing.T) {
	for _, password := range []string{"", "hunter2"} {
		plain := &Fake{}
		streamed := &streamFake{}
		ctx := context.Background()

		if _, err := (Sudo{Runner: plain, Password: password}).Run(ctx, "id -u"); err != nil {
			t.Fatal(err)
		}
		if _, err := (Sudo{Runner: streamed, Password: password}).RunStream(ctx, "id -u", nil); err != nil {
			t.Fatal(err)
		}
		if len(plain.Log) != 1 || len(streamed.Log) != 1 {
			t.Fatalf("password %q: %d and %d commands", password, len(plain.Log), len(streamed.Log))
		}
		if plain.Log[0] != streamed.Log[0] {
			t.Errorf("password %q: the streamed command elevates differently:\n  run    %s\n  stream %s",
				password, plain.Log[0], streamed.Log[0])
		}
	}
}

// streamFake is a Fake that can also stream, which the plain one deliberately
// cannot: Fake stands in for a runner with no such ability.
type streamFake struct{ Fake }

func (f *streamFake) RunStream(ctx context.Context, cmd string, onLine func(string)) (Result, error) {
	res, err := f.Run(ctx, cmd)
	if onLine != nil {
		for _, line := range strings.Split(res.Stdout, "\n") {
			if line != "" {
				onLine(line)
			}
		}
	}
	return res, err
}
