#!/usr/bin/env bash
set -euo pipefail
ready=$(kubectl -n "$LAB_NAMESPACE" get deploy forge -o jsonpath='{.status.readyReplicas}')
[ "${ready:-0}" -ge 2 ] || { echo "The forge has ${ready:-0} ready replica(s); it needs 2."; exit 1; }
echo "Two fires burn in the forge."
