#!/usr/bin/env bash
set -euo pipefail
kubectl -n "$LAB_NAMESPACE" set image deploy/forge web=nginx:does-not-exist >/dev/null
