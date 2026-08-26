// Package tools carries the operator CLIs that an airgapped site cannot
// download.
//
// helm and k9s are fetched from a vendor's release page during an online
// install. A site with no route to the internet gets neither, which is why the
// tools step skips them off-line rather than reporting them missing forever.
//
// An airgap build embeds the tarballs instead, so the payload travels inside
// the binary. A default build embeds nothing: the tarballs are 108MB across
// the architectures, and an operator with a network should not carry them.
package tools

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// Names of the tools this package can carry.
const (
	Helm = "helm"
	K9s  = "k9s"
)

// Names lists what a full payload contains, in a fixed order.
var Names = []string{Helm, K9s}

// Arches lists the architectures a payload is built for.
//
// The node's architecture, not the operator's: a build is carried to whatever
// the customer runs, and the two are regularly not the same.
var Arches = []string{"amd64", "arm64"}

// File is the name a payload member has inside the payload directory.
//
// A flat, predictable name rather than the vendor's, which differs per tool
// and changes with their release tooling.
func File(name, arch string) string {
	return fmt.Sprintf("%s-linux-%s.tar.gz", name, arch)
}

// Tarball returns the embedded archive for one tool and architecture.
//
// The second result reports whether this build carries it. A default build
// carries nothing and answers false for everything, which is the signal a
// caller uses to fall back to downloading.
func Tarball(name, arch string) ([]byte, bool) {
	raw, err := fs.ReadFile(payload, "payload/"+File(name, arch))
	if err != nil {
		return nil, false
	}
	return raw, true
}

// Carried lists what this build actually holds, as "name/arch" pairs.
//
// Used by the version output and by tests: a build that claims to be an airgap
// build and carries nothing is worse than one that admits it, because the
// operator finds out at the customer site.
func Carried() []string {
	entries, err := fs.ReadDir(payload, "payload")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".tar.gz") {
			continue
		}
		n = strings.TrimSuffix(n, ".tar.gz")
		name, arch, ok := strings.Cut(n, "-linux-")
		if !ok {
			continue
		}
		out = append(out, name+"/"+arch)
	}
	sort.Strings(out)
	return out
}

// IsAirgapBuild reports whether this binary carries a payload at all.
func IsAirgapBuild() bool { return len(Carried()) > 0 }
