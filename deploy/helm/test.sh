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
# AWS labs (M6): off by default; when on, Crucible may write the credentials secret but never read any secret.
if grep -q 'CRUCIBLE_AWS_LABS' <<<"$out"; then echo "aws labs must be opt-in"; exit 1; fi
aws="$base --set awsLabs.enabled=true --set awsLabs.labRoleArn=arn:aws:iam::1:role/l --set awsLabs.opsRoleArn=arn:aws:iam::1:role/o --set awsLabs.stateBucket=sb"
awsout=$(helm template t "$chart" $aws)
for want in 'name: CRUCIBLE_AWS_LABS, value: "1"' 'name: CRUCIBLE_AWS_LAB_ROLE_ARN' 'name: CRUCIBLE_AWS_STATE_BUCKET' 'key: INFRACOST_API_KEY, optional: true' 'name: AWS_REGION'; do
  grep -q -- "$want" <<<"$awsout" || { echo "missing with aws labs: $want"; exit 1; }
done
# fixwave I1: the runner pod holds the lab credentials, so its images are pinned by digest, never a re-pushable tag.
for img in CRUCIBLE_AWS_WORKSPACE_IMAGE CRUCIBLE_TERRAFORM_IMAGE; do
  grep -q "name: $img, value: \"[^\"]*@sha256:[0-9a-f]\{64\}\"" <<<"$awsout" || { echo "$img must be pinned by digest"; exit 1; }
done
if grep -E '^ *(workspaceImage|terraformImage):' "$chart/values.yaml" | grep -vq '@sha256:'; then echo "lab/runner image defaults must be digest-pinned"; exit 1; fi
if grep -q 'CRUCIBLE_INFRACOST' <<<"$awsout"; then echo "real aws labs must not turn infracost off"; exit 1; fi
dry=$(helm template t "$chart" $base --set awsLabs.enabled=true --set awsLabs.dryRun=true)
for want in 'name: CRUCIBLE_AWS_LABS, value: "dryrun"' 'name: CRUCIBLE_INFRACOST, value: "off"'; do
  grep -q -- "$want" <<<"$dry" || { echo "missing in dry run: $want"; exit 1; }
done
awsrbac=$(helm template t "$chart" $aws --show-only templates/rbac.yaml)
grep -A1 '^    resources: \[secrets\]' <<<"$awsrbac" | grep -q 'verbs: \[create, update\]' || { echo "secrets: create and update only"; exit 1; }
if grep -A1 'resources: \[secrets\]' <<<"$awsrbac" | grep -qE '\b(get|list|watch)\b'; then echo "crucible must never read secrets"; exit 1; fi
grep -q 'operations: \[CREATE, UPDATE\], resources: \[secrets\]' <<<"$awsrbac" || { echo "the admission policy must confine secret writes to lab namespaces"; exit 1; }
grep -q 'operations: \[DELETE\], resources: \[pods\]' <<<"$awsrbac" || { echo "the admission policy must confine pod deletes to lab namespaces"; exit 1; }
if helm template t "$chart" $aws --set clusterLabs.enabled=false >/dev/null 2>&1; then echo "aws labs need cluster labs"; exit 1; fi
if helm template t "$chart" $base --set awsLabs.enabled=true >/dev/null 2>&1; then echo "aws labs need the lab account's role ARNs"; exit 1; fi
echo "helm chart OK"
