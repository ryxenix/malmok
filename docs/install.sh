#!/bin/sh
set -eu

repo="ryxenix/malmok"
version="latest"
bin_dir="/usr/local/bin"
flavour="malmok"
release_root=${MALMOK_INSTALL_RELEASE_ROOT:-"https://github.com/${repo}/releases/download"}
latest_url=${MALMOK_INSTALL_LATEST_URL:-"https://github.com/${repo}/releases/latest"}

die() {
  echo "install-malmok: $*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Install Malmok from its GitHub release.

Usage: install.sh [--version vX.Y.Z] [--bin-dir DIR] [--airgap]
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version)
      [ "$#" -ge 2 ] || { echo "--version requires a value" >&2; exit 2; }
      version="$2"
      shift 2
      ;;
    --bin-dir)
      [ "$#" -ge 2 ] || { echo "--bin-dir requires a value" >&2; exit 2; }
      bin_dir="$2"
      shift 2
      ;;
    --airgap)
      flavour="malmok-airgap"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

for command_name in curl awk mktemp install; do
  command -v "$command_name" >/dev/null 2>&1 || die "${command_name} is required"
done

kernel=$(uname -s)
machine=$(uname -m)

case "$kernel" in
  Linux) os="linux" ;;
  Darwin) os="darwin" ;;
  *) die "unsupported operating system: ${kernel}" ;;
esac

case "$machine" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) die "unsupported architecture: ${machine}" ;;
esac

if [ "$flavour" = "malmok-airgap" ] && [ "$os" != "linux" ]; then
  die "the air-gap binary is available for Linux only"
fi

if [ "$version" = "latest" ]; then
  release_url=$(curl -fsSL -o /dev/null -w '%{url_effective}' \
    "$latest_url")
  version=${release_url##*/}
else
  case "$version" in v*) ;; *) version="v${version}" ;; esac
fi

awk -v value="$version" 'BEGIN {
  exit(value ~ /^v[0-9]+[.][0-9]+[.][0-9]+$/ ? 0 : 1)
}' || die "invalid release version: ${version}"

asset="${flavour}_${version}_${os}_${arch}"
base_url="${release_root}/${version}"
tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/malmok-install.XXXXXX")
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM

echo "Downloading Malmok ${version} for ${os}/${arch}..."
curl -fsSL --retry 3 -o "${tmp_dir}/${asset}" "${base_url}/${asset}"
curl -fsSL --retry 3 -o "${tmp_dir}/SHA256SUMS" "${base_url}/SHA256SUMS"

expected=$(awk -v name="$asset" '$2 == name || $2 == "*" name { print $1; exit }' \
  "${tmp_dir}/SHA256SUMS")
[ -n "$expected" ] || die "release checksum does not list ${asset}"

if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "${tmp_dir}/${asset}" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "${tmp_dir}/${asset}" | awk '{print $1}')
else
  die "sha256sum or shasum is required"
fi

[ "$actual" = "$expected" ] || die "checksum verification failed for ${asset}"

if [ ! -d "$bin_dir" ]; then
  mkdir -p "$bin_dir" 2>/dev/null || true
fi

if [ -d "$bin_dir" ] && [ -w "$bin_dir" ]; then
  install -m 0755 "${tmp_dir}/${asset}" "${bin_dir}/malmok"
elif command -v sudo >/dev/null 2>&1; then
  sudo mkdir -p "$bin_dir"
  sudo install -m 0755 "${tmp_dir}/${asset}" "${bin_dir}/malmok"
else
  die "${bin_dir} is not writable; use --bin-dir with a writable directory"
fi

echo "Installed Malmok ${version} to ${bin_dir}/malmok"
