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
echo "$out"
