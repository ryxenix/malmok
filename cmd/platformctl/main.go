// Command platformctl builds and operates RKE2 platforms.
//
// The command tree grows as the engine does; only the subcommands that are
// actually implemented are registered, because a command that exists and does
// nothing is worse at a customer site than one that is absent.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
