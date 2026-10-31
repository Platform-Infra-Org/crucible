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
need 'router.entrypoints: websecure'
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
need 'name: CRUCIBLE_LAB_CLUSTER'
need 'requests: { cpu: 500m, memory: 1Gi }, limits: { cpu: 500m, memory: 1Gi }'   # postgres: Guaranteed QoS
need 'requests: { cpu: 250m, memory: 512Mi }, limits: { cpu: 250m, memory: 512Mi }'  # api: Guaranteed QoS
base="--set backup.bucket=b --set backup.region=eu-west-1 --set oidc.issuer=https://sso"
# access = ssm: plain HTTP on Traefik's web entrypoint, any Host (through the tunnel it is localhost:<port>).
plain=$(helm template t "$chart" $base --set tls.enabled=false --set host= --set publicURL=http://localhost:18080)
ing=$(sed -n '/^kind: Ingress$/,/^---$/p' <<<"$plain")
grep -q 'router.entrypoints: web$' <<<"$ing" || { echo "tls off: the Ingress must use the web entrypoint"; exit 1; }
if grep -qE 'router.tls|certresolver|^ *host:' <<<"$ing"; then echo "tls off: no TLS, no cert resolver, no host rule"; exit 1; fi
grep -q 'CRUCIBLE_PUBLIC_URL, value: "http://localhost:18080"' <<<"$plain" || { echo "tls off: the API's public URL is the localhost one"; exit 1; }
if helm template t "$chart" $base --set tls.enabled=false >/dev/null 2>&1; then echo "tls off with an https publicURL must fail"; exit 1; fi
if helm template t "$chart" $base --set publicURL=http://localhost:18080 >/dev/null 2>&1; then echo "an http publicURL with tls on must fail"; exit 1; fi
if grep -q CRUCIBLE_BOOTSTRAP_ADMIN <<<"$out"; then echo "bootstrap admin is opt-in"; exit 1; fi
boot=$(helm template t "$chart" $base --set bootstrapAdmin=boss@example.com)
grep -q 'name: CRUCIBLE_BOOTSTRAP_ADMIN, value: "boss@example.com"' <<<"$boot" || { echo "missing: bootstrap admin env"; exit 1; }
if grep -q CRUCIBLE_PLATFORM <<<"$out"; then echo "configuration lives in Postgres: no platform repo"; exit 1; fi
if grep -q CRUCIBLE_PREVIEW <<<"$out"; then echo "preview mode must never be rendered by the chart"; exit 1; fi
if grep -q CRUCIBLE_CLUSTER_PRIVILEGED <<<"$out"; then echo "privileged lab pods must be opt-in"; exit 1; fi
rbac=$(helm template t "$chart" $base --show-only templates/rbac.yaml)
if grep -qE 'secrets|"\*"|- \*$|\[\*\]' <<<"$rbac"; then echo "crucible-labs must not touch secrets or use wildcards"; exit 1; fi
if grep -qE 'rolebindings|clusterroles|escalate|bind|impersonate' <<<"$rbac"; then echo "crucible-labs must not manage RBAC"; exit 1; fi
if helm template t "$chart" $base --set clusterLabs.unsafePrivileged=true >/dev/null 2>&1; then echo "unsafePrivileged must require explicit acknowledgement"; exit 1; fi
dev=$(helm template t "$chart" $base --set clusterLabs.unsafePrivileged=true --set clusterLabs.iUnderstandPrivilegedLabsAreUnsafe=true)
grep -q 'name: CRUCIBLE_CLUSTER_PRIVILEGED' <<<"$dev" || { echo "missing: dev privileged env"; exit 1; }
if grep -q 'pod-security.kubernetes.io/enforce' <<<"$dev"; then echo "dev policy must allow privileged lab pods"; exit 1; fi
off=$(helm template t "$chart" $base --set clusterLabs.enabled=false)
if grep -qE 'kind: ClusterRole|CRUCIBLE_LAB_CLUSTER' <<<"$off"; then echo "clusterLabs.enabled=false must not grant cluster access"; exit 1; fi
if grep -q CRUCIBLE_TOOLBOX_IMAGE <<<"$out"; then echo "cluster labs on trainees' clusters are opt-in (toolboxImage)"; exit 1; fi
tb=$(helm template t "$chart" $base --set toolboxImage=x@sha256:y)
grep -q 'name: CRUCIBLE_TOOLBOX_IMAGE' <<<"$tb" || { echo "missing: toolbox image env"; exit 1; }
grep -q 'automountServiceAccountToken: false' <<<"$off" || { echo "no API token without cluster labs"; exit 1; }
if grep -q 'hostNetwork: true' <<<"$out"; then echo "hostNetwork must not be used"; exit 1; fi
need 'CRUCIBLE_BLOB_BUCKET, value: "b"'                    # uploads go to the data bucket, not the emptyDir
need 'CRUCIBLE_BLOB_REGION, value: "eu-west-1"'
# AWS labs (M6): off by default; when on, Crucible may write the credentials secret but never read any secret.
if grep -q 'CRUCIBLE_AWS_LABS' <<<"$out"; then echo "aws labs must be opt-in"; exit 1; fi
aws="$base --set awsLabs.enabled=true --set awsLabs.labRoleArn=arn:aws:iam::1:role/l --set awsLabs.opsRoleArn=arn:aws:iam::1:role/o --set awsLabs.stateBucket=sb"
aws="$aws --set awsLabs.planRoleArn=arn:aws:iam::1:role/p --set awsLabs.exportBucket=eb --set awsLabs.exportName=crucible-labs"
awsout=$(helm template t "$chart" $aws)
for want in 'name: CRUCIBLE_AWS_LABS, value: "1"' 'name: CRUCIBLE_AWS_LAB_ROLE_ARN' 'name: CRUCIBLE_AWS_STATE_BUCKET' 'name: AWS_REGION' \
  'name: CRUCIBLE_AWS_PLAN_ROLE_ARN, value: "arn:aws:iam::1:role/p"' 'name: CRUCIBLE_AWS_EXPORT_BUCKET, value: "eb"' \
  'name: CRUCIBLE_AWS_EXPORT_PREFIX, value: "cur"' 'name: CRUCIBLE_AWS_EXPORT_NAME, value: "crucible-labs"'; do
  grep -q -- "$want" <<<"$awsout" || { echo "missing with aws labs: $want"; exit 1; }
done
# fixwave I1: the runner pod holds the lab credentials, so its images are pinned by digest, never a re-pushable tag.
for img in CRUCIBLE_AWS_WORKSPACE_IMAGE CRUCIBLE_TERRAFORM_IMAGE; do
  grep -q "name: $img, value: \"[^\"]*@sha256:[0-9a-f]\{64\}\"" <<<"$awsout" || { echo "$img must be pinned by digest"; exit 1; }
done
if grep -E '^ *(workspaceImage|terraformImage):' "$chart/values.yaml" | grep -vq '@sha256:'; then echo "lab/runner image defaults must be digest-pinned"; exit 1; fi
if grep -q CRUCIBLE_GIT_ALLOW_FILE <<<"$out$boot$awsout"; then echo "file git transport must never be rendered"; exit 1; fi
for k in planRoleArn exportBucket exportName; do   # real labs: no estimate without the plan role, no billed spend without the export
  if helm template t "$chart" $(sed "s/--set awsLabs.$k=[^ ]*//" <<<"$aws") >/dev/null 2>&1; then echo "aws labs need awsLabs.$k"; exit 1; fi
done
dry=$(helm template t "$chart" $base --set awsLabs.enabled=true --set awsLabs.dryRun=true)
grep -q -- 'name: CRUCIBLE_AWS_LABS, value: "dryrun"' <<<"$dry" || { echo "missing in dry run: CRUCIBLE_AWS_LABS=dryrun"; exit 1; }
awsrbac=$(helm template t "$chart" $aws --show-only templates/rbac.yaml)
docof() { sed -n "/^kind: $1\$/,/^---\$/p" <<<"$awsrbac"; }
cr=$(docof ClusterRole)
grep -A1 '^    resources: \[secrets\]' <<<"$cr" | grep -q 'verbs: \[create, update\]' || { echo "secrets: create and update only"; exit 1; }
if grep -A1 'secrets' <<<"$cr" | grep -qE '\b(get|list|watch)\b'; then echo "crucible must never read secrets cluster-wide"; exit 1; fi
if grep -q 'pods/log' <<<"$cr"; then echo "pods/log only in crucible-estimates, never cluster-wide"; exit 1; fi
if grep -qE '"\*"|- \*$|\[\*\]|rolebindings|clusterroles|escalate|impersonate' <<<"$awsrbac"; then echo "aws labs: no wildcards, no RBAC management"; exit 1; fi
grep -q 'operations: \[CREATE, UPDATE\], resources: \[secrets\]' <<<"$awsrbac" || { echo "the admission policy must confine secret writes to lab namespaces"; exit 1; }
grep -q 'operations: \[DELETE\], resources: \[pods, secrets, configmaps\]' <<<"$awsrbac" || { echo "the admission policy must confine deletes to lab namespaces and crucible-estimates"; exit 1; }
# Estimates (internal/labs aws.go Plan): plan pods run in crucible-estimates, which Helm creates labelled like the Go
# code's estimatesObjects (crucible.io/lab=estimates, Pod Security baseline); a namespaced Role grants what a plan
# run needs there and nowhere else, and the admission policy allows exactly that namespace besides lab-*.
ns=$(docof Namespace)
for want in 'name: crucible-estimates' 'crucible.io/lab: estimates' 'pod-security.kubernetes.io/enforce: baseline'; do
  grep -q -- "$want" <<<"$ns" || { echo "estimates namespace: missing $want"; exit 1; }
done
role=$(docof Role)
for want in 'namespace: crucible-estimates' 'resources: \[pods/log\]' 'resources: \[networkpolicies\]'; do
  grep -q -- "$want" <<<"$role" || { echo "estimates Role: missing $want"; exit 1; }
done
# least privilege: what aws.go Plan and reapPlans call. Pods are created, polled (get), listed and deleted; a run's
# credentials secret and module config map are only created, listed and deleted, never read back (no get or watch).
grep -A1 'resources: \[pods\]$' <<<"$role" | grep -q 'verbs: \[create, get, list, delete\]' || { echo "estimates Role: pods create, get, list, delete"; exit 1; }
grep -A1 'resources: \[secrets, configmaps\]$' <<<"$role" | grep -q 'verbs: \[create, list, delete\]' || { echo "estimates Role: secrets and config maps create, list, delete only"; exit 1; }
if grep -E 'secrets|configmaps' -A1 <<<"$role" | grep -qE '\b(get|watch)\b'; then echo "estimates Role: never get or watch secrets or config maps"; exit 1; fi
grep -A2 'resources: \[pods/log\]' <<<"$role" | grep -q 'verbs: \[get\]' || { echo "estimates Role: pods/log get only"; exit 1; }
grep -A2 'resources: \[networkpolicies\]' <<<"$role" | grep -q 'verbs: \[create\]' || { echo "estimates Role: networkpolicies create only"; exit 1; }
rb=$(docof RoleBinding)
for want in 'namespace: crucible-estimates' 'kind: Role, name: crucible-estimates' 'kind: ServiceAccount, name: crucible, namespace: default'; do
  grep -q -- "$want" <<<"$rb" || { echo "estimates RoleBinding: missing $want"; exit 1; }
done
vap=$(docof ValidatingAdmissionPolicy)
grep -q "object.metadata.name == 'crucible-estimates'" <<<"$vap" || { echo "admission: crucible-api may create crucible-estimates"; exit 1; }
grep -q "namespaceObject.metadata.name.startsWith('lab-') || namespaceObject.metadata.name == 'crucible-estimates'" <<<"$vap" ||
  { echo "admission: writes only in lab-* and crucible-estimates"; exit 1; }
if grep -qE "crucible-estimates|kind: Role$|kind: Namespace$" <<<"$rbac"; then echo "estimates RBAC only with aws labs"; exit 1; fi
if helm template t "$chart" $aws --set clusterLabs.enabled=false >/dev/null 2>&1; then echo "aws labs need cluster labs"; exit 1; fi
if helm template t "$chart" $base --set awsLabs.enabled=true >/dev/null 2>&1; then echo "aws labs need the lab account's role ARNs"; exit 1; fi
echo "helm chart OK"
