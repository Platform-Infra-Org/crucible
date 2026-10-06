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
# Cluster labs (M4): least-privilege RBAC, confined by an admission policy to lab namespaces.
need 'serviceAccountName: crucible'
need 'kind: ClusterRole'
need 'pods/exec'
need 'kind: ValidatingAdmissionPolicy'
need "system:serviceaccount:default:crucible"
need 'pod-security.kubernetes.io/enforce'
need 'name: CRUCIBLE_CLUSTER_LABS'
need 'requests: { cpu: 500m, memory: 1Gi }, limits: { cpu: 500m, memory: 1Gi }'   # postgres: Guaranteed QoS
need 'requests: { cpu: 250m, memory: 512Mi }, limits: { cpu: 250m, memory: 512Mi }'  # api: Guaranteed QoS
base="--set backup.bucket=b --set backup.region=eu-west-1 --set oidc.issuer=https://sso"
if grep -q CRUCIBLE_CLUSTER_PRIVILEGED <<<"$out"; then echo "privileged lab pods must be opt-in"; exit 1; fi
rbac=$(helm template t "$chart" $base --show-only templates/rbac.yaml)
if grep -qE 'secrets|"\*"|- \*$|\[\*\]' <<<"$rbac"; then echo "crucible-labs must not touch secrets or use wildcards"; exit 1; fi
if grep -qE 'rolebindings|clusterroles|escalate|bind|impersonate' <<<"$rbac"; then echo "crucible-labs must not manage RBAC"; exit 1; fi
if helm template t "$chart" $base --set clusterLabs.unsafePrivileged=true >/dev/null 2>&1; then echo "unsafePrivileged must require explicit acknowledgement"; exit 1; fi
dev=$(helm template t "$chart" $base --set clusterLabs.unsafePrivileged=true --set clusterLabs.iUnderstandPrivilegedLabsAreUnsafe=true)
grep -q 'name: CRUCIBLE_CLUSTER_PRIVILEGED' <<<"$dev" || { echo "missing: dev privileged env"; exit 1; }
if grep -q 'pod-security.kubernetes.io/enforce' <<<"$dev"; then echo "dev policy must allow privileged lab pods"; exit 1; fi
off=$(helm template t "$chart" $base --set clusterLabs.enabled=false)
if grep -qE 'kind: ClusterRole|CRUCIBLE_CLUSTER_LABS' <<<"$off"; then echo "clusterLabs.enabled=false must not grant cluster access"; exit 1; fi
grep -q 'automountServiceAccountToken: false' <<<"$off" || { echo "no API token without cluster labs"; exit 1; }
if grep -q 'hostNetwork: true' <<<"$out"; then echo "hostNetwork must not be used"; exit 1; fi
need 'CRUCIBLE_BLOB_BUCKET, value: "b"'                    # uploads go to the data bucket, not the emptyDir
need 'CRUCIBLE_BLOB_REGION, value: "eu-west-1"'
echo "helm chart OK"
