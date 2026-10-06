#!/usr/bin/env bash
# M1 acceptance: build everything, start Postgres + Keycloak + Crucible, and drive a trainee
# through Forge 101 using only a local (laptop) lab. KEEP=1 leaves the stack running afterwards.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

for bin in docker go node npm git; do
  command -v "$bin" >/dev/null || { echo "missing prerequisite: $bin"; exit 1; }
done
docker compose version >/dev/null || { echo "Docker compose plugin is required"; exit 1; }

echo "== build"
make build
(cd web && npm ci && npm test)

echo "== lint content"
./bin/crucible lint examples/forge-101
./bin/crucible lint examples/forge-201
./bin/crucible lint examples/forge-301
./bin/crucible lint examples/forge-401
./bin/crucible lint examples/platform

echo "== seed git repos"
./scripts/seed-git.sh

compose="docker compose -f deploy/compose/docker-compose.yml"
project=local
if [ "${CLUSTER:-0}" = 1 ]; then # scripts/cluster-check.sh: also run Forge 101's cluster lab on kind
  compose="$compose -f deploy/compose/cluster.yml"
  project=cluster
fi
cleanup() {
  if [ "${KEEP:-0}" != 1 ]; then
    $compose down -v >/dev/null 2>&1 || true
  else
    echo "KEEP=1: stack left running at http://localhost:8080 (users: trainee/trainee, leader/leader)"
  fi
}
trap cleanup EXIT

echo "== start stack"
$compose up -d --build --wait

echo "== end-to-end"
(cd e2e && npm install --no-audit --no-fund && npx playwright install chromium \
  && CRUCIBLE_AGENT="$root/bin/crucible-agent" npx playwright test --project="$project")

echo "🔥 Local check passed. The forge holds."
