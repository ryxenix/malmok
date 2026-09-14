// Command malmok builds and operates RKE2 platforms.
//
// The command tree grows as the engine does; only the subcommands that are
// actually implemented are registered, because a command that exists and does
// nothing is worse at a customer site than one that is absent.
package main

import (
	"errors"
	"fmt"
	"os"
)

// exitError lets a command choose the process's exit status.
//
// One exit status cannot say everything. An inspection that found a problem
// and an inspection that could not run both fail, and a script told only "it
// failed" has to guess which -- so a command that has more than one kind of
// answer says which one it had, and prints its own message rather than having
// "error:" put in front of a sentence that is not one.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func main() {
	err := newRootCmd().Execute()
	if err == nil {
		return
	}

	var ee *exitError
	if errors.As(err, &ee) {
		if ee.msg != "" {
			fmt.Fprintln(os.Stderr, ee.msg)
		}
		os.Exit(ee.code)
	}

	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
