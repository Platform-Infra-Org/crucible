#!/usr/bin/env bash
# Installs the repo's pinned toolchain into .local/tools: Go (.local/tools/go/bin), Terraform and kind (.local/tools).
# Each download is checked against the publisher's SHA-256, pinned below; a mismatch stops before anything is installed.
# A tool already at its pinned version is left alone. Usage: scripts/tools.sh [--dry-run] [--dest DIR]
# To bump a version, update it and its four hashes together:
#   Go:        https://go.dev/dl/?mode=json&include=all (the archive's sha256)
#   Terraform: https://releases.hashicorp.com/terraform/<v>/terraform_<v>_SHA256SUMS
#   kind:      https://github.com/kubernetes-sigs/kind/releases/download/v<v>/kind-<os>-<arch>.sha256sum
set -euo pipefail

GO_VERSION=1.26.8
TERRAFORM_VERSION=1.16.5
KIND_VERSION=0.33.0 # cluster-check needs >= 0.24

go_sha256() {
  case $1 in
    darwin-amd64) echo 186be014105aa6542b767d2c6ed5cca10a0214bdff809ef1724022a8c7894150 ;;
    darwin-arm64) echo a012b25b571bd0138a03dcd25375ceba866fe5ca822f426d2c66a4de56fd3f4b ;;
    linux-amd64) echo d0f743b33e8d8945e6b1f432edd15785c70507121d6e2a723b21285eddf8b57b ;;
    linux-arm64) echo 211ffced9dcb9633a55eac6364816ec0ddd951389a740e88fa8b3337971bdda0 ;;
  esac
}
terraform_sha256() {
  case $1 in
    darwin-amd64) echo 9809158e481cf2d3a8433e59179f397b8edb5172397711bd98f6bb32edcaf53b ;;
    darwin-arm64) echo ecdef65e24193d627f27c39baeda31295f08c938d8a3d4764f442fb916d4b7dc ;;
    linux-amd64) echo 2bc2fcfff033265c9e02ca0351f01794eb122f62a9b2a49a3294b9e49eaab5e4 ;;
    linux-arm64) echo 61a50b00485ee4810cf20581ef080fc54d34d666e175c58d9a10501c65c1ccde ;;
  esac
}
kind_sha256() {
  case $1 in
    darwin-amd64) echo 5a99f26f57246dc9319dd294803313197a0f34d33c525b3ea8b655db5916ece0 ;;
    darwin-arm64) echo 0c8c7dbe5e23594a198b786c4bc13dacc101fa6196b0cb0b23a1ca44e61f4b4f ;;
    linux-amd64) echo aee6151561422756b764a4ae28e7f44cda5af5a9eead3cc9985112b1de8d8e0d ;;
    linux-arm64) echo 20022bee6cfcd5086cb7234d218e3454e6090022f2a8f55d1fa7fcf42c3867a2 ;;
  esac
}

dry=0
dest="$(cd "$(dirname "$0")/.." && pwd)/.local/tools"
while [ $# -gt 0 ]; do
  case $1 in
    --dry-run) dry=1 ;;
    --dest) dest=${2:?--dest needs a directory}; shift ;;
    *) echo "usage: $0 [--dry-run] [--dest DIR]" >&2; exit 2 ;;
  esac
  shift
done

case $(uname -s) in Darwin) os=darwin ;; Linux) os=linux ;; *) echo "unsupported OS: $(uname -s)" >&2; exit 1 ;; esac
case $(uname -m) in x86_64 | amd64) arch=amd64 ;; arm64 | aarch64) arch=arm64 ;; *) echo "unsupported arch: $(uname -m)" >&2; exit 1 ;; esac
plat=$os-$arch

sha256() { if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1; }

work=""
cleanup() { if [ -n "$work" ]; then rm -rf "$work"; fi; }
trap cleanup EXIT

# fetch URL SHA256 FILE: downloads into the scratch dir and refuses a checksum mismatch.
fetch() {
  [ -n "$work" ] || work=$(mktemp -d)
  curl -fsSL --retry 3 -o "$work/$3" "$1"
  got=$(sha256 "$work/$3")
  if [ "$got" != "$2" ]; then
    echo "checksum mismatch for $1: got $got, want $2; nothing installed" >&2
    exit 1
  fi
}

# want NAME VERSION CURRENT URL SHA256: says what it would do or did; returns 1 when the tool is already there.
want() {
  if [ "$3" = "$2" ]; then echo "$1 $2: already in $dest"; return 1; fi
  if [ $dry = 1 ]; then echo "$1 $2: would fetch $4 (sha256 $5) into $dest"; return 1; fi
  echo "$1 $2: fetching $4"
}

[ $dry = 1 ] || mkdir -p "$dest"

cur=$(head -n1 "$dest/go/VERSION" 2>/dev/null || true)
url=https://go.dev/dl/go$GO_VERSION.$plat.tar.gz
if want go "go$GO_VERSION" "$cur" "$url" "$(go_sha256 "$plat")"; then
  fetch "$url" "$(go_sha256 "$plat")" go.tar.gz
  tar -xzf "$work/go.tar.gz" -C "$work"
  rm -rf "$dest/go"
  mv "$work/go" "$dest/go"
fi

cur=$("$dest/terraform" version 2>/dev/null | sed -n '1s/^Terraform v//p' || true)
url=https://releases.hashicorp.com/terraform/$TERRAFORM_VERSION/terraform_${TERRAFORM_VERSION}_${os}_$arch.zip
if want terraform "$TERRAFORM_VERSION" "$cur" "$url" "$(terraform_sha256 "$plat")"; then
  fetch "$url" "$(terraform_sha256 "$plat")" terraform.zip
  unzip -q -o "$work/terraform.zip" terraform -d "$work"
  install -m 0755 "$work/terraform" "$dest/terraform"
fi

cur=$("$dest/kind" version 2>/dev/null | sed -n 's/^kind v\([^ ]*\).*/\1/p' || true)
url=https://github.com/kubernetes-sigs/kind/releases/download/v$KIND_VERSION/kind-$plat
if want kind "$KIND_VERSION" "$cur" "$url" "$(kind_sha256 "$plat")"; then
  fetch "$url" "$(kind_sha256 "$plat")" kind
  install -m 0755 "$work/kind" "$dest/kind"
fi

[ $dry = 1 ] || echo "toolchain ready: export PATH=$dest/go/bin:$dest:\$PATH"
