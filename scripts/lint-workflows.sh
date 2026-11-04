#!/usr/bin/env bash
# Lints the GitHub workflows: a guard against event text in run: scripts, then actionlint (pinned image).
set -euo pipefail
cd "$(dirname "$0")/.."
# Inline (`run: ... ${{ github.event.`) and block (`run: |` bodies) forms; pass event values through env: instead.
if awk '
  /^[[:space:]]*(- )?run:[[:space:]]*[|>]/ { match($0, /^[[:space:]]*(- )?/); ind = RLENGTH; inblk = 1; next }
  inblk && /[^[:space:]]/ { match($0, /^[[:space:]]*/); if (RLENGTH <= ind) inblk = 0 }
  /^[[:space:]]*(- )?run:/ && /\$\{\{[[:space:]]*github\.event\./ { print FILENAME ":" FNR ": " $0; bad = 1 }
  inblk && /\$\{\{[[:space:]]*github\.event\./ { print FILENAME ":" FNR ": " $0; bad = 1 }
  END { exit bad }
' .github/workflows/*.yml; then :; else
  echo "pass github.event values to run: scripts through env:, never inline" >&2; exit 1
fi
docker run --rm -v "$PWD:/repo:ro" -w /repo rhysd/actionlint:1.7.7 -color
