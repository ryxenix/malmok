#!/bin/bash
# Run the verification matrix against the lab nodes.
#
# It WIPES BOTH MACHINES before every case. There are no default addresses on
# purpose: a default here is a loaded gun pointed at whatever happens to live
# at that address on somebody else's network. Name the nodes explicitly, and
# name only nodes you are willing to lose.
#
#   export MALMOK_LAB_SERVER=192.0.2.41 MALMOK_LAB_AGENT=192.0.2.44
#   NODE_PASSWORD=... scripts/matrix.sh                 # every case
#   NODE_PASSWORD=... scripts/matrix.sh -run idc-single # one case
#
# The air-gapped case needs RKE2's release artifacts staged on both nodes and
# MALMOK_LAB_AIRGAP_VERSION set to the release they carry. Without it that one
# case skips and says so; the rest run.
#
# MALMOK_LAB_MIRROR names a pull-through cache for the platform images, which
# are otherwise fetched again for every case because every case wipes the image
# store with the node. Stand it up first:
#
#   sudo docker compose -f test/lab/cache/compose.yaml up -d
#   MALMOK_LAB_MIRROR=192.168.88.253 NODE_PASSWORD=... scripts/matrix.sh
#
# Off by default on purpose. It puts registry.mirrors in every document, so a
# run with it on has not shown that a customer without a cache installs.
set -euo pipefail
cd "$(dirname "$0")/.."

: "${NODE_PASSWORD:?set NODE_PASSWORD to the password for the lab account}"
: "${MALMOK_LAB_SERVER:?set MALMOK_LAB_SERVER -- this machine gets wiped}"
: "${MALMOK_LAB_AGENT:?set MALMOK_LAB_AGENT -- this machine gets wiped}"
export MALMOK_LAB_SERVER MALMOK_LAB_AGENT MALMOK_LAB_AIRGAP_VERSION MALMOK_LAB_MIRROR

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
  -e MALMOK_LAB_AIRGAP_VERSION \
  -e MALMOK_LAB_MIRROR \
  -e MALMOK_BIN=/app-local/bin/malmok \
  -w /app-local \
  golang:alpine \
  go test -tags lab -count=1 -v -timeout 6h ./test/lab/ "$@"
