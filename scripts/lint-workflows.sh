#!/usr/bin/env bash
# Lints the GitHub workflows: a guard against attacker-controlled text in run: scripts, then actionlint (pinned image).
set -euo pipefail
cd "$(dirname "$0")/.."
# Inline (`run: ... ${{ github.event.`) and block (`run: |` bodies) forms; pass event values through env: instead.
if awk '
  /^[[:space:]]*(- )?run:[[:space:]]*[|>]/ { match($0, /^[[:space:]]*(- )?/); ind = RLENGTH; inblk = 1; next }
  inblk && /[^[:space:]]/ { match($0, /^[[:space:]]*/); if (RLENGTH <= ind) inblk = 0 }
  /^[[:space:]]*(- )?run:/ && /\$\{\{[[:space:]]*(github\.event\.|github\.head_ref|inputs\.)/ { print FILENAME ":" FNR ": " $0; bad = 1 }
  inblk && /\$\{\{[[:space:]]*(github\.event\.|github\.head_ref|inputs\.)/ { print FILENAME ":" FNR ": " $0; bad = 1 }
  END { exit bad }
' .github/workflows/*.yml; then :; else
  echo "pass github.event / head_ref / inputs values to run: scripts through env:, never inline" >&2; exit 1
fi
# No cache in a job that can write (contents/packages/id-token/…: write, or write-all, its own or the workflow's):
# a cache saved by an unprivileged run (any PR) would otherwise run inside it. Reads the files' 2-space layout
# (jobs at 2, a job's keys at 4).
if awk '
  function done() { if (job != "" && cache && (jw || (!jp && tw))) { print file ": job " job " can write and uses actions/cache"; bad = 1 } }
  FNR == 1 { done(); job = ""; tw = 0; top = 0; injobs = 0 }
  /^permissions:/ { top = 1; if (/write/) tw = 1; next }
  /^[^[:space:]#]/ { top = 0 }
  top && /:[[:space:]]*write/ { tw = 1 }
  /^jobs:/ { injobs = 1; next }
  injobs && /^  [A-Za-z0-9_-]+:/ { done(); job = $1; sub(/:$/, "", job); file = FILENAME; jw = 0; jp = 0; jperm = 0; cache = 0; next }
  job != "" && /^    permissions:/ { jp = 1; jperm = 1; if (/write/) jw = 1; next }
  job != "" && /^    [^[:space:]]/ { jperm = 0 }
  jperm && /:[[:space:]]*write/ { jw = 1 }
  job != "" && /uses:[[:space:]]*actions\/cache/ { cache = 1 }
  END { done(); exit bad }
' .github/workflows/*.yml; then :; else
  echo "jobs that can write never restore a cache: install tools from scratch there" >&2; exit 1
fi
docker run --rm -v "$PWD:/repo:ro" -w /repo rhysd/actionlint:1.7.7 -color
