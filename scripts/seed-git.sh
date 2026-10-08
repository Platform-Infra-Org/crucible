#!/usr/bin/env bash
# Turns the training examples (forge-001 … forge-401) into bare git repos under .local/git (mounted at /git in the api
# container). Configuration is not in git: the stack imports examples/platform into Postgres (CRUCIBLE_SEED_DIR).
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
out="$root/.local/git"
# The api container runs as its own uid (Dockerfile: 10001) and pushes content edits into this bind mount, so on a
# Linux host the objects it wrote belong to a user we are not and cannot remove. Docker Desktop and rootless Podman
# map those writes back to us and never hit this. Ask a throwaway root container to clear what we can't.
if [ -d "$out" ] && ! rm -rf "$out" 2>/dev/null; then
  docker run --rm -v "$out:/git:z" alpine:3.22 sh -c 'rm -rf /git/* /git/.[!.]*'
  rm -rf "$out"
fi
mkdir -p "$out"
for name in forge-101 forge-102 forge-201 forge-301 forge-401 forge-103 forge-001; do
  work=$(mktemp -d)
  cp -R "$root/examples/$name/." "$work/"
  git -C "$work" init -q -b main
  git -C "$work" add -A
  git -C "$work" -c user.name=crucible -c user.email=crucible@local commit -qm "seed $name"
  git clone -q --bare "$work" "$out/$name.git"
  git -C "$out/$name.git" config core.sharedRepository world
  chmod -R a+rwX "$out/$name.git"   # the api container (uid 10001) merges content edits here
  rm -rf "$work"
done
echo "seeded $out"
