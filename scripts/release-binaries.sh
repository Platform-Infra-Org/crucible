#!/usr/bin/env bash
# Builds the crucible binary for darwin/linux x amd64/arm64 with the version stamped, as
# <outdir>/crucible_<version>_<os>_<arch>.tar.gz, plus <outdir>/SHA256SUMS.
#   scripts/release-binaries.sh <version> <outdir>
set -euo pipefail
[ $# -eq 2 ] || { echo "usage: $0 <version> <outdir>" >&2; exit 2; }
version=$1
out=$2
[[ $version =~ ^[0-9A-Za-z.+-]+$ ]] || { echo "bad version: $version" >&2; exit 2; }
cd "$(dirname "$0")/.."
mkdir -p "$out"
out=$(cd "$out" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
for os in darwin linux; do
  for arch in amd64 arm64; do
    name="crucible_${version}_${os}_${arch}"
    mkdir -p "$work/$name"
    CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
      -ldflags "-s -w -X crucible/internal/docs.Version=$version" -o "$work/$name/crucible" ./cmd/crucible
    tar -C "$work" -czf "$out/$name.tar.gz" "$name"
  done
done
cd "$out"
if command -v sha256sum >/dev/null; then sum="sha256sum"; else sum="shasum -a 256"; fi
$sum crucible_"${version}"_*.tar.gz > SHA256SUMS
