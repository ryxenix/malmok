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

# The airgap builds carry helm and k9s inside the binary, because a site with
# no route to the internet cannot fetch them and the tools step consequently
# skips them. They are separate artifacts rather than the default: the payload
# is 108MB across the architectures, and an operator with a network should not
# download it to get a 15MB tool.
payload=internal/tools/payload
rm -rf "$payload" && mkdir -p "$payload"

helm_version=$(curl -sfL --retry 3 --retry-delay 2 https://get.helm.sh/helm-latest-version || echo "")
helm_version=$(echo "$helm_version" | tr -d '\r\n')
if [ -z "$helm_version" ]; then
  echo "could not ask helm which version is current" >&2
  exit 1
fi

for arch in amd64 arm64; do
  echo "fetching helm ${helm_version} ${arch}"
  curl -sfL --retry 3 --retry-delay 2 \
    -o "${payload}/helm-linux-${arch}.tar.gz" \
    "https://get.helm.sh/helm-${helm_version}-linux-${arch}.tar.gz"
  echo "fetching k9s ${arch}"
  curl -sfL --retry 3 --retry-delay 2 \
    -o "${payload}/k9s-linux-${arch}.tar.gz" \
    "https://github.com/derailed/k9s/releases/latest/download/k9s_Linux_${arch}.tar.gz"
done

for target in $targets; do
  os="${target%/*}"
  arch="${target#*/}"
  # Linux only: the payload is Linux binaries, and an airgap build for a
  # workstation would carry 108MB it can never install.
  [ "$os" = linux ] || continue
  out="dist/malmok-airgap_${tag}_${os}_${arch}"
  echo "building $out"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -tags airgap -trimpath -ldflags "-s -w -X main.version=${tag}" -o "$out" ./cmd/malmok
done

rm -rf "$payload"

( cd dist && sha256sum malmok_* malmok-airgap_* > SHA256SUMS )

echo
cat dist/SHA256SUMS
