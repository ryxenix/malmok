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

# Pinned, not "latest". The same tag has to produce the same binary next year,
# which it cannot when the payload is whatever upstream published that morning
# -- and nobody can then say what is inside a release they are auditing.
#
# The checksums are the other half, and the more important one. These binaries
# go inside a tool that runs as root on a customer's nodes, so a download that
# nothing verifies is a download anybody can substitute. SHA256SUMS over the
# finished artifacts does not cover this: it records what was built, not what
# went into it.
#
# The values come from upstream's own published sums. To bump a version, take
# them from there rather than from whatever this script happens to download.
helm_version=v4.2.4
k9s_version=v0.51.0

payload_sha() {
  case "$1" in
    # https://get.helm.sh/helm-${helm_version}-linux-<arch>.tar.gz.sha256sum
    helm-linux-amd64) echo c306b46f719b0a4da32d0f78ee21bf90ce8d602f15b22ab753f0674d1670a7f3 ;;
    helm-linux-arm64) echo 564de2191b881e9f71b5606b25345821ea1682f06ab90499d3ab22b530176da1 ;;
    # https://github.com/derailed/k9s/releases/download/${k9s_version}/checksums.sha256
    k9s-linux-amd64)  echo c3752ad51a5a4015a113819c4eeb6e55a4d0e4b8e652494797532f6fc8161dd7 ;;
    k9s-linux-arm64)  echo 3ee05c82e5f9198928a4e86133608ba6a2c10a2244d6a7789e820f78319d640c ;;
    *) return 1 ;;
  esac
}

fetch_payload() {
  name=$1
  url=$2
  want=$(payload_sha "$name") || { echo "no checksum recorded for $name" >&2; exit 1; }

  echo "fetching $name"
  curl -sfL --retry 3 --retry-delay 2 -o "${payload}/${name}.tar.gz" "$url"

  got=$(sha256sum "${payload}/${name}.tar.gz" | awk '{print $1}')
  if [ "$got" != "$want" ]; then
    echo "$name does not match the checksum recorded for it" >&2
    echo "  want $want" >&2
    echo "  got  $got" >&2
    echo >&2
    echo "Either upstream republished the file or something is wrong. Find out" >&2
    echo "which before changing the recorded value: this goes inside a binary" >&2
    echo "that runs as root on somebody else's nodes." >&2
    exit 1
  fi
}

for arch in amd64 arm64; do
  fetch_payload "helm-linux-${arch}" \
    "https://get.helm.sh/helm-${helm_version}-linux-${arch}.tar.gz"
  fetch_payload "k9s-linux-${arch}" \
    "https://github.com/derailed/k9s/releases/download/${k9s_version}/k9s_Linux_${arch}.tar.gz"
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

# The carry list ships beside the binaries. An operator planning an
# air-gapped install needs it before they have anywhere to run malmok, and
# asking them to extract it from a binary they cannot yet run is a poor answer.
# From the binary, not from internal/images/images.txt. That file is the
# chart-derived half; the storage phase renders its own manifest and names its
# own images, and a release asset missing them is a closed site with a
# provisioner that never starts.
# go run rather than one of the cross-built binaries: those are named for the
# tag and three of the four cannot execute on the machine doing the release.
go run ./cmd/malmok images > dist/images.txt

# The images themselves, one file per architecture, the way k3s and RKE2 ship
# theirs. A closed site then carries three things -- the binary, RKE2's
# artifacts and this -- instead of reading chart values and pulling twenty
# images by hand.
#
# Skippable, because it downloads over a gigabyte and a maintainer checking
# what a release will contain usually does not need it. CI never skips.
if [ "${MALMOK_SKIP_IMAGES:-}" = "1" ]; then
  echo "skipping the image bundle (MALMOK_SKIP_IMAGES=1)"
else
  ./scripts/airgap-images.sh "$tag"
fi

( cd dist && sha256sum malmok_* malmok-airgap_* malmok-images_* images.txt > SHA256SUMS )

echo
cat dist/SHA256SUMS
