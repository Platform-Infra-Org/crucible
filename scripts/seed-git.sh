#!/usr/bin/env bash
# Turns examples/{platform,forge-101} into bare git repos under .local/git (mounted at /git in the api container).
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
out="$root/.local/git"
rm -rf "$out"
mkdir -p "$out"
for name in platform forge-101; do
  work=$(mktemp -d)
  cp -R "$root/examples/$name/." "$work/"
  git -C "$work" init -q -b main
  git -C "$work" add -A
  git -C "$work" -c user.name=crucible -c user.email=crucible@local commit -qm "seed $name"
  git clone -q --bare "$work" "$out/$name.git"
  rm -rf "$work"
done
echo "seeded $out"
