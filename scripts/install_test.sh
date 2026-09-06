#!/bin/sh
set -eu

cd "$(dirname "$0")/.."

test_root=$(mktemp -d "${TMPDIR:-/tmp}/malmok-install-test.XXXXXX")
trap 'rm -rf "$test_root"' EXIT HUP INT TERM

version="v9.8.7"
case "$(uname -m)" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) echo "unsupported test architecture" >&2; exit 1 ;;
esac

release_dir="${test_root}/releases/${version}"
mkdir -p "$release_dir" "${test_root}/redirect"
: > "${test_root}/redirect/${version}"

make_asset() {
  asset="$1"
  printf '#!/bin/sh\necho %s\n' "$asset" > "${release_dir}/${asset}"
  chmod +x "${release_dir}/${asset}"
}

standard="malmok_${version}_linux_${arch}"
airgap="malmok-airgap_${version}_linux_${arch}"
make_asset "$standard"
make_asset "$airgap"

(
  cd "$release_dir"
  sha256sum "$standard" "$airgap" > SHA256SUMS
)

run_installer() {
  MALMOK_INSTALL_RELEASE_ROOT="file://${test_root}/releases" \
    MALMOK_INSTALL_LATEST_URL="file://${test_root}/redirect/${version}" \
    sh docs/install.sh "$@"
}

run_installer --bin-dir "${test_root}/bin"
cmp "${release_dir}/${standard}" "${test_root}/bin/malmok"
[ -x "${test_root}/bin/malmok" ]

run_installer --version 9.8.7 --airgap --bin-dir "${test_root}/airgap-bin"
cmp "${release_dir}/${airgap}" "${test_root}/airgap-bin/malmok"

printf '\ncorrupt\n' >> "${release_dir}/${standard}"
if run_installer --version "$version" --bin-dir "${test_root}/bad-bin" >/dev/null 2>&1; then
  echo "installer accepted an invalid checksum" >&2
  exit 1
fi

if run_installer --version not-a-version --bin-dir "${test_root}/invalid-bin" >/dev/null 2>&1; then
  echo "installer accepted an invalid version" >&2
  exit 1
fi

echo "install script tests passed"
