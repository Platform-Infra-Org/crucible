#!/usr/bin/env bash
# Puts AWS's Session Manager plugin where internal/awsops/blobs embeds it, so a
# release binary carries the plugin and an operator installs nothing. One
# gzipped binary per platform, checked against the SHA-256 pinned below.
#
# Usage: scripts/plugin-blobs.sh [GOOS GOARCH | --all | --clear]
#   no arguments: this machine's platform (what `make build` needs)
#   --all:        every released platform (what scripts/release-binaries.sh needs)
#   --clear:      truncate the blobs back to the committed placeholders
#
# AWS publishes no checksum file for the plugin, only GPG signatures, which
# would make gpg a prerequisite. So the checksums below were taken by hand from
# the pinned version's artifacts and are what every later fetch is held to; to
# move the pin, bump PLUGIN_VERSION and replace all four.
# Needs: curl, ar, tar, unzip, gzip.
set -euo pipefail

PLUGIN_VERSION=1.2.764.0
BASE=https://s3.amazonaws.com/session-manager-downloads/plugin/$PLUGIN_VERSION

sha256_for() { # GOOS GOARCH
  case "$1_$2" in
    linux_amd64)  echo beed4c95c42afd29756d9ecea59c3fcbf937b2c35b9ef84d12b93ac6e74726ba ;;
    linux_arm64)  echo 57d4146c3c30c944e242f319be7c3fdd8f9118a46cfd02d0e3369c77b9e9d1ec ;;
    darwin_amd64) echo a4e4c3a66e93c2c8d360516188e7650b8bd1f79cbd03a76e3b8a280b1c6c947d ;;
    darwin_arm64) echo 6131b8e605bdec5cf64fd81f988525cdc49bc2e646482aae139533d3a23f0425 ;;
    *) return 1 ;;
  esac
}

url_for() { # GOOS GOARCH
  case "$1_$2" in
    linux_amd64)  echo "$BASE/ubuntu_64bit/session-manager-plugin.deb" ;;
    linux_arm64)  echo "$BASE/ubuntu_arm64/session-manager-plugin.deb" ;;
    darwin_amd64) echo "$BASE/mac/sessionmanager-bundle.zip" ;;
    darwin_arm64) echo "$BASE/mac_arm64/sessionmanager-bundle.zip" ;;
  esac
}

root=$(cd "$(dirname "$0")/.." && pwd)
blobs=$root/internal/awsops/blobs
sha256() { if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1; }

one() { # GOOS GOARCH
  local os=$1 arch=$2 want url work
  want=$(sha256_for "$os" "$arch") || { echo "no plugin is published for $os/$arch" >&2; return 1; }
  url=$(url_for "$os" "$arch")
  work=$(mktemp -d); trap 'rm -rf "$work"' RETURN

  curl -fsSL -o "$work/pkg" "$url"
  local got; got=$(sha256 "$work/pkg")
  if [ "$got" != "$want" ]; then
    echo "$url: sha256 $got, expected $want — refusing to embed it" >&2
    return 1
  fi

  # The Linux package is an ar archive around data.tar.gz; the macOS one is a
  # plain zip. Both hold the binary and the licence files we must ship with it.
  if [ "$os" = linux ]; then
    (cd "$work" && ar x pkg data.tar.gz && tar xzf data.tar.gz)
    local d=$work/usr/local/sessionmanagerplugin
    cp "$d/bin/session-manager-plugin" "$work/plugin"
    cp "$d/LICENSE" "$work/LICENSE"; cp "$d/NOTICE" "$work/NOTICE"
  else
    (cd "$work" && unzip -qo pkg)
    cp "$work/sessionmanager-bundle/bin/session-manager-plugin" "$work/plugin"
    cp "$work/sessionmanager-bundle/LICENSE" "$work/LICENSE"
    cp "$work/sessionmanager-bundle/NOTICE" "$work/NOTICE"
  fi

  gzip -9 -c "$work/plugin" > "$blobs/session-manager-plugin_${os}_${arch}.gz"
  # Apache-2.0: the licence and notice travel with the binary. They are the
  # same text for every platform, so the last one written wins harmlessly.
  cp "$work/LICENSE" "$blobs/session-manager-plugin.LICENSE"
  cp "$work/NOTICE" "$blobs/session-manager-plugin.NOTICE"
  echo "$os/$arch: $(wc -c < "$blobs/session-manager-plugin_${os}_${arch}.gz") bytes embedded (plugin $PLUGIN_VERSION)"
}

case "${1:-}" in
  --all)
    for p in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64; do one "${p%_*}" "${p#*_}"; done ;;
  --clear)
    for p in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64; do : > "$blobs/session-manager-plugin_$p.gz"; done
    echo "blobs cleared: a build from here falls back to a plugin on PATH" ;;
  "")
    one "$(go env GOOS)" "$(go env GOARCH)" ;;
  *)
    one "$1" "${2:?GOARCH}" ;;
esac
