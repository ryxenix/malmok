//go:build airgap

package tools

import "embed"

// The payload is downloaded by scripts/release.sh before an airgap build and
// is not in the repository: it is 108MB of somebody else's binaries, which
// belongs in a release artifact rather than in git history.
//
//go:embed payload/*.tar.gz
var payload embed.FS
