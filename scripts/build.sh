#!/bin/bash
# Build the malmok binary as bin/malmok-v<version>-<timestamp> and point
# bin/malmok at it. The suffix answers the question every copied-around
# binary eventually raises -- "which build is this?" -- from the filename
# alone; the symlink keeps every documented `bin/malmok` command working.
set -euo pipefail
cd "$(dirname "$0")/.."

ver=$(grep -m1 -oE '\[[0-9]+\.[0-9]+\.[0-9]+\]' CHANGELOG.md | tr -d '[]')
ts=$(date +%Y%m%d-%H%M%S)
out="bin/malmok-v${ver}-${ts}"

# Container Go, per the development policy: nothing runs on the host.
docker run --rm \
  --user "$(id -u):$(id -g)" \
  -v "$PWD:/app-local" \
  -v "$HOME/.cache/go-build:/.cache/go-build" \
  -v "$HOME/go/pkg/mod:/go/pkg/mod" \
  -e GOCACHE=/.cache/go-build \
  -e GOFLAGS=-mod=mod \
  -w /app-local \
  golang:alpine \
  go build -ldflags "-X main.version=v${ver}" -o "$out" ./cmd/malmok

ln -sfn "$(basename "$out")" bin/malmok

# Keep the last few builds and drop the rest.
#
# Every code change rebuilds, and a session of them left forty binaries at
# 16MB each. A handful is enough to go back to the build that was running
# when something was observed; beyond that they are disk nobody reads. The
# symlink's target is never removed, whatever its age.
keep=${MALMOK_KEEP_BUILDS:-5}
current=$(readlink bin/malmok)
ls -t bin/malmok-v* 2>/dev/null | tail -n +$((keep + 1)) | while read -r old_build; do
  [ "$(basename "$old_build")" = "$current" ] && continue
  rm -f "$old_build"
done

echo "$out"
