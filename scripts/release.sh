#!/bin/bash
# Build the release artifacts for a tag into dist/.
#
# The same script runs in CI and on a workstation, so a maintainer can check
# what a release will contain before pushing the tag -- and so a user who
# distrusts the published binary can rebuild it and compare checksums.
#
#   scripts/release.sh v0.66.0
set -euo pipefail
cd "$(dirname "$0")/.."

tag="${1:?usage: scripts/release.sh <tag>, e.g. v0.66.0}"

# The tag is the version. A binary that reports something other than the tag
# it was released under is the kind of thing that costs an hour during an
# incident.
changelog=$(grep -m1 -oE '\[[0-9]+\.[0-9]+\.[0-9]+\]' CHANGELOG.md | tr -d '[]')
if [ "v${changelog}" != "$tag" ]; then
  echo "tag $tag does not match the newest CHANGELOG entry v${changelog}" >&2
  exit 1
fi

rm -rf dist && mkdir -p dist

# Where operators run this from. Not the nodes -- nothing is installed there.
targets="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"

for target in $targets; do
  os="${target%/*}"
  arch="${target#*/}"
  out="dist/malmok_${tag}_${os}_${arch}"
  echo "building $out"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags "-s -w -X main.version=${tag}" -o "$out" ./cmd/malmok
done

( cd dist && sha256sum malmok_* > SHA256SUMS )

echo
cat dist/SHA256SUMS
