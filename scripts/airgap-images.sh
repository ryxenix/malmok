#!/bin/bash
# Build the air-gapped image bundle for a tag into dist/.
#
#   scripts/airgap-images.sh v0.94.0
#
# One file per architecture, the way k3s and RKE2 ship theirs: an operator
# going to a closed site downloads the binary, RKE2's own artifacts and this,
# and carries three things instead of reading chart values and pulling twenty
# images by hand.
#
# What goes in is exactly what `malmok images` says, so the list, the bundle
# and the tool cannot disagree -- the list is generated from the same binary
# that installs.
#
# On the node the file goes where RKE2's containerd looks:
#
#   /var/lib/rancher/rke2/agent/images/
#
# Everything there is imported when RKE2 starts, so it has to be in place
# before the service does, or the service restarted after.
set -euo pipefail
cd "$(dirname "$0")/.."

tag="${1:?usage: scripts/airgap-images.sh <tag>, e.g. v0.94.0}"
mkdir -p dist

# GitHub refuses a release asset over 2 GiB. Better to find out here, where the
# message can say what to do, than at the upload step of a tagged release.
limit=$(( 2 * 1024 * 1024 * 1024 ))

# The list comes from the binary. images.txt is only the chart-derived half:
# the storage phase renders its own manifest and names its own images.
# A built binary when there is one -- the lab stages bundles from a workstation
# that keeps its Go toolchain in a container. CI has neither a binary at this
# point nor a reason to avoid the toolchain.
if [ -x bin/malmok ]; then
  mapfile -t refs < <(./bin/malmok images)
else
  mapfile -t refs < <(go run ./cmd/malmok images)
fi
[ "${#refs[@]}" -gt 0 ] || { echo "malmok images listed nothing" >&2; exit 1; }
echo "${#refs[@]} images"

# pull fetches one image for one architecture, and says why when it cannot.
#
# The first version treated every failure as a missing architecture, so a
# registry refusing a request -- "toomanyrequests: Rate exceeded" from ECR
# Public, on a CI runner whose address is shared with everybody else's -- was
# reported as "has no linux/amd64 build" and an instruction to stop publishing
# amd64. The refusal is transient and is retried with a growing wait; the
# missing-architecture verdict is given only when the registry says exactly
# that; anything else is printed as the registry said it.
pull() {
  local ref=$1 arch=$2 out="" attempt
  for attempt in $(seq 1 "${MALMOK_PULL_ATTEMPTS:-6}"); do
    if out=$(docker pull -q --platform "linux/${arch}" "$ref" 2>&1); then
      return 0
    fi
    case "$out" in
      *"no matching manifest"*|*"does not match the specified platform"*|*"no match for platform"*)
        echo "$out" >&2
        echo >&2
        echo "$ref has no linux/${arch} build, so the ${arch} bundle would be" >&2
        echo "incomplete. Fix the component or stop publishing ${arch}; do not" >&2
        echo "ship a bundle with a hole in it." >&2
        exit 1
        ;;
    esac
    echo "  attempt ${attempt}: $(printf '%s' "$out" | tail -1)" >&2
    [ "$attempt" -lt "${MALMOK_PULL_ATTEMPTS:-6}" ] && sleep $((attempt * 15))
  done
  echo >&2
  echo "$ref could not be pulled for linux/${arch}. The last thing the registry said:" >&2
  echo "$out" >&2
  exit 1
}

# Both, for a release. Overridable so the lab can stage the one architecture
# its nodes have without pulling twenty images twice.
for arch in ${MALMOK_IMAGE_ARCHES:-amd64 arm64}; do
  out="dist/malmok-images_${tag}_linux_${arch}.tar"

  # --platform, so an amd64 runner produces a bundle an arm64 node can import.
  # An image with no build for the architecture is a failure and not a warning:
  # a carry list missing one entry is discovered in a room with no way to fetch
  # what is missing, which is the whole thing this file exists to prevent.
  for ref in "${refs[@]}"; do
    echo "pulling ${arch} ${ref}"
    pull "$ref" "$arch"
  done

  echo "saving $out"
  docker save -o "$out" "${refs[@]}"

  # -12 rather than -19: the last levels buy a few per cent for several minutes
  # each, on a file whose cost is the download and not the disk.
  zstd -q -12 --rm -f "$out"
  out="${out}.zst"

  size=$(stat -c %s "$out")
  echo "$out  $(( size / 1024 / 1024 ))MB"
  if [ "$size" -gt "$limit" ]; then
    echo >&2
    echo "$out is over the 2 GiB a GitHub release asset can be." >&2
    echo "Split it -- one bundle per phase (storage, pki, observability," >&2
    echo "gitops) matches how malmok images -f already filters." >&2
    exit 1
  fi

  # Freeing each architecture's images before the next keeps a CI runner's
  # disk out of it: twenty images twice over is more than one has spare.
  docker rmi -f "${refs[@]}" >/dev/null 2>&1 || true
done
