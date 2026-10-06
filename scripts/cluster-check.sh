#!/usr/bin/env bash
# M4 acceptance: Forge 101's cluster lab on a local kind cluster standing in for k3s. Docker Desktop cannot run
# sysbox, so lab pods are privileged dind here (CRUCIBLE_CLUSTER_PRIVILEGED=1, dev only). Everything else is real:
# namespaces, quotas, NetworkPolicy (kindnet), exec terminals, and the chart's RBAC + admission policy, because
# both the integration test and crucible-api authenticate as the crucible service account.
# KEEP=1 leaves kind (and the compose stack) running. Never touches AWS.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
# kind and kubectl use this file only, never the user's ~/.kube/config (kind would switch its current-context)
mkdir -p .local/kind
export KUBECONFIG="$root/.local/kind/admin.kubeconfig"
name=crucible-m4
ctx="kind-$name"

for bin in kind kubectl helm docker go; do
  command -v "$bin" >/dev/null || { echo "missing prerequisite: $bin"; exit 1; }
done
minor=$(kind version | sed -n 's/^kind v0\.\([0-9]*\)\..*/\1/p')
[ "${minor:-0}" -ge 24 ] || { echo "kind >= v0.24 is needed (NetworkPolicy); put it in .local/tools (see the M4 plan, Task 8)"; exit 1; }

cleanup() {
  if [ "${KEEP:-0}" != 1 ]; then
    kind delete cluster --name "$name" >/dev/null 2>&1 || true
  else
    echo "KEEP=1: kind cluster $name left running (kubectl --context $ctx …)"
  fi
}
trap cleanup EXIT

echo "== kind cluster"
kind get clusters 2>/dev/null | grep -qx "$name" || kind create cluster --name "$name" --image "${KIND_NODE_IMAGE:-kindest/node:v1.34.11}" --wait 180s

echo "== crucible service account, RBAC and admission policy from the chart"
kubectl --context "$ctx" create namespace crucible --dry-run=client -o yaml | kubectl --context "$ctx" apply -f -
render() { # $1 = true for the dev (privileged) policy, false for the production policy (baseline PSA required)
  helm template crucible deploy/helm/crucible -n crucible --set clusterLabs.unsafePrivileged="$1" \
    --set clusterLabs.iUnderstandPrivilegedLabsAreUnsafe="$1" \
    --set backup.bucket=unused --set backup.region=unused --set oidc.issuer=https://unused --show-only templates/rbac.yaml \
    | kubectl --context "$ctx" -n crucible apply -f -
}
render true

echo "== probe pod (a neighbour the lab must not reach)"
kubectl --context "$ctx" create namespace crucible-probe --dry-run=client -o yaml | kubectl --context "$ctx" apply -f -
kubectl --context "$ctx" -n crucible-probe get pod probe >/dev/null 2>&1 \
  || kubectl --context "$ctx" -n crucible-probe run probe --image=nginx:1.29-alpine --port=80
kubectl --context "$ctx" -n crucible-probe wait --for=condition=Ready pod/probe --timeout=180s
probe_ip=$(kubectl --context "$ctx" -n crucible-probe get pod probe -o jsonpath='{.status.podIP}')
api_ip=$(kubectl --context "$ctx" get svc kubernetes -o jsonpath='{.spec.clusterIP}')
node_ip=$(kubectl --context "$ctx" get node -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}')
dns_pod=$(kubectl --context "$ctx" -n kube-system get pod -l k8s-app=kube-dns -o jsonpath='{.items[0].metadata.name}')

echo "== kubeconfigs that authenticate as system:serviceaccount:crucible:crucible"
token=$(kubectl --context "$ctx" -n crucible create token crucible --duration=4h)
mkdir -p .local/kind
sa_kubeconfig() { # $1 = output file, $2 = extra kind flag (--internal for containers on the kind network)
  kind get kubeconfig --name "$name" ${2:-} > "$1"
  kubectl --kubeconfig "$1" config unset "users.$ctx.client-certificate-data" >/dev/null
  kubectl --kubeconfig "$1" config unset "users.$ctx.client-key-data" >/dev/null
  kubectl --kubeconfig "$1" config set-credentials "$ctx" --token="$token" >/dev/null
  chmod 644 "$1" # read by the api container's non-root user; .local/ is git-ignored, the token expires in 4h
}
sa_kubeconfig .local/kind/kubeconfig
sa_kubeconfig .local/kind/kubeconfig-internal --internal
who=$(kubectl --kubeconfig .local/kind/kubeconfig auth whoami 2>&1) || true
[[ $who == *system:serviceaccount:crucible:crucible* ]] \
  || { echo "kubeconfig does not authenticate as the crucible service account: $who"; exit 1; }

echo "== production admission policy (baseline PSA required): the service account cannot create a lab namespace without it"
render false
CRUCIBLE_TEST_KUBECONFIG="$root/.local/kind/kubeconfig" CRUCIBLE_TEST_STRICT=1 \
  go test -tags cluster -run TestClusterPSAPolicy -count=1 -timeout 5m -v ./internal/labs/
render true # the dev policy for the real run: lab pods are privileged on kind

echo "== cluster runner against kind"
CRUCIBLE_TEST_KUBECONFIG="$root/.local/kind/kubeconfig" CRUCIBLE_TEST_PROBE_IP="$probe_ip" CRUCIBLE_TEST_API_IP="$api_ip" \
  CRUCIBLE_TEST_NODE_IP="$node_ip" CRUCIBLE_TEST_DNS_POD="$dns_pod" CRUCIBLE_CLUSTER_PRIVILEGED=1 \
  go test -tags cluster -run TestClusterLabOnKind -count=1 -timeout 20m -v ./internal/labs/

echo "== browser: Forge 101 end to end, including the cluster lab"
CLUSTER=1 ./scripts/local-check.sh

echo "🔥 Cluster check passed. The crucible holds."
