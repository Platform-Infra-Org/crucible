#!/usr/bin/env bash
# helm lint + assertions on rendered manifests (no cluster needed).
set -euo pipefail
chart="$(dirname "$0")/crucible"
helm lint "$chart" --set backup.bucket=b --set backup.region=eu-west-1 --set oidc.issuer=https://sso
out=$(helm template t "$chart" --set backup.bucket=b --set backup.region=eu-west-1 --set oidc.issuer=https://sso --set host=crucible.example.com)
need() { grep -q -- "$1" <<<"$out" || { echo "missing: $1"; exit 1; }; }
need 'kind: CronJob'
need 'pg_dump -h postgres -U crucible -Fc crucible'
need "to_regclass('public.users')"                       # restore only into an empty database
need 'router.tls.certresolver: le'
need 'host: crucible.example.com'
need 'imagePullPolicy: Never'
need 'type: Recreate'
need 'CRUCIBLE_QUIZ_SECRET'
need 'list-objects-v2'
need 'max_by(Contents || `\[\]`, &Key).Key'
need -- '--single-transaction'
need 'inited=$(psql'
need 's3://b/latest/crucible-latest.dump'                 # backup keeps a never-expiring copy
need 'head-object --bucket "b" --key latest/crucible-latest.dump'
need 'latest=latest/crucible-latest.dump'
need 'cat /tmp/head.err >&2; exit 1'                      # other S3 errors fail loudly
if grep -q 'hostNetwork: true' <<<"$out"; then echo "hostNetwork must not be used"; exit 1; fi
echo "helm chart OK"
