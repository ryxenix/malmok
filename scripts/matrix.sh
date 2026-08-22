#!/bin/bash
# Run the verification matrix against the lab nodes.
#
# It wipes both machines before every case, so it is pointed at the throwaway
# segment and nothing else. NODE_PASSWORD must be set; the addresses default
# to the lab pair and can be overridden.
#
#   NODE_PASSWORD=... scripts/matrix.sh                 # every case
#   NODE_PASSWORD=... scripts/matrix.sh -run idc-single # one case
set -euo pipefail
cd "$(dirname "$0")/.."

: "${NODE_PASSWORD:?set NODE_PASSWORD to the password for the lab account}"
export MALMOK_LAB_SERVER="${MALMOK_LAB_SERVER:-192.168.88.241}"
export MALMOK_LAB_AGENT="${MALMOK_LAB_AGENT:-192.168.88.244}"

# The binary the operator runs, not a fresh build of maybe-different code.
[ -x bin/malmok ] || { echo "no bin/malmok -- run scripts/build.sh first" >&2; exit 1; }

# The host toolchain would do, but the container is what the policy says and
# what everyone has. SSH goes out from the container the same way it does from
# the tool itself.
docker run --rm \
  --user "$(id -u):$(id -g)" \
  -v "$PWD:/app-local" \
  -v "$HOME/.cache/go-build:/.cache/go-build" \
  -v "$HOME/go/pkg/mod:/go/pkg/mod" \
  -e GOCACHE=/.cache/go-build \
  -e GOFLAGS=-mod=mod \
  -e NODE_PASSWORD \
  -e MALMOK_LAB_SERVER \
  -e MALMOK_LAB_AGENT \
  -e MALMOK_BIN=/app-local/bin/malmok \
  -w /app-local \
  golang:alpine \
  go test -tags lab -count=1 -v -timeout 6h ./test/lab/ "$@"
