# Cluster labs on your own cluster

Replaces the `cluster` lab runtime of spec §8.2 (one dind pod per lab on Crucible's own cluster, priced by
`cluster_usd_per_hour`). Owner's decisions, 2026-10-11, from a brainstorm that also weighed and rejected Argo CD
(central or per cluster): plain Helm through the laptop agent covers deploying, health checks and status with no
shared server holding rights on trainees' clusters.

## Goal

A trainee runs a cluster lab on a Kubernetes cluster of their choosing (kind/minikube/k3d/Docker Desktop on the
laptop, or a remote EKS/GKE/team cluster) through `crucible-agent` and their local kubeconfig. The lab is one or more
Helm charts. The mode costs nothing in Crucible: the trainee owns the cluster and its bill.

Success: an author writes a lab as charts plus tasks; a trainee picks a context, the agent installs the charts into a
namespace of their cluster, terminals and checks work against it, and ending the lab removes it, for laptop and remote
clusters alike. Credentials never leave the laptop.

## Decisions

| Question | Decision |
|---|---|
| Replace or add | Replace `runtime: cluster`. Crucible no longer runs cluster labs; its own cluster keeps running AWS lab workspaces and Terraform pods. |
| Clusters | Local and remote alike. |
| Argo CD | Not used. Labs about Argo CD install it with their own charts. |
| Where Helm runs | In a toolbox container the agent starts on the laptop (option A). Not in the agent (Helm SDK) nor on the server (`helm template`). |
| Terminals and checks | In the same toolbox, with kubectl access to the chosen context. |
| Scope on the cluster | The lab's own namespace `lab-<id>`; cluster-wide resources only with `cluster_wide: true`, shown to the trainee before start. |
| Choosing the cluster | Per lab, in the browser, from the contexts the agent reports (name and server host, never credentials). |
| Chart source | Folders in the lab, at the lab's exact content version; `Chart.yaml` dependencies allowed if pinned to exact versions from `https://` or `oci://`. |
| Cost | $0: no estimate, no approval, no budget, not in the Ledger. TTL, idle timeout, schedules and the kill switch still apply. |

## 1. Lab format

```yaml
id: shop-on-fire
runtime: cluster
ttl: 2h
idle_timeout: 30m
task_order: linear
cluster:
  charts:                     # installed in order into lab-<id>, removed in reverse
    - name: shop              # Helm release name
      path: charts/shop       # folder in this lab containing Chart.yaml
      values: values/shop.yaml   # optional file in this lab
      timeout: 5m             # helm --wait --timeout; default 5m
    - name: loadgen
      path: charts/loadgen
  cluster_wide: false         # true: charts may create cluster-scoped kinds; trainee consents before start
terminals:
  - { name: shell }           # every terminal opens in the toolbox
tasks:
  - id: t1-find-the-fire
    instructions: tasks/01.md
    check: { script: checks/01.sh }   # runs in the toolbox
    points: 2
  - id: t2-healthy
    instructions: tasks/02.md
    setup: { script: setup/02-break.sh }
    check: { ready: [shop] }          # built-in readiness check, or ready: all
    points: 3
```

Scripts (setup and check) run in the toolbox with `KUBECONFIG` (the chosen context, default namespace `lab-<id>`),
`LAB_NAMESPACE`, `LAB_ID` and `LAB_RELEASES` (space-separated release names).

**Lint** (`content.Load`, so `crucible lint`, sync, the editor's Problems panel and edit submission all apply it), for
`runtime: cluster`:

- `cluster.charts` has at least one entry; each `path` is a folder in the lab with a `Chart.yaml`; `values`, if set, is
  a file in the lab; `timeout` parses and is positive.
- Release names are unique within the lab and valid DNS-1123 labels.
- `Chart.yaml` dependencies name an exact version (no ranges) and an `https://` or `oci://` repository, or a `file://`
  path inside the lab.
- No `compose`, no `aws` block, no `service` on terminals, no `run_in` on scripts.
- `check.ready` names releases of this lab (or `all`); a check has either `script` or `ready`, not both.
- If a template file mentions a cluster-scoped kind (CustomResourceDefinition, ClusterRole, ClusterRoleBinding,
  Namespace, StorageClass, PriorityClass, MutatingWebhookConfiguration, ValidatingWebhookConfiguration,
  PersistentVolume, IngressClass, APIService) and `cluster_wide` is false, lint reports it. This is a hint only; the
  agent enforces it on the rendered output (§2).
- A `runtime: cluster` lab with `compose` gets: "cluster labs now install Helm charts; compose files are for laptop
  labs".

Only chart folders and values files are sent to the agent; check and setup scripts are sent per run, as for laptop
labs; rubrics never leave the server.

## 2. The agent

**Contexts.** The agent reads the kubeconfig (`--kubeconfig` alone; else `$KUBECONFIG`, else `~/.kube/config`, plus
`~/.crucible/kubeconfig`) at start and whenever the file changes, and reports `[{name, server_host}]` to Crucible, in the
hello message and in a new `contexts` message. No credentials, tokens or certificates leave the laptop.

**A cluster of their own.** `crucible-agent cluster up [--name crucible]` makes a kind cluster through Docker (kind's
library, pinned node image) and writes its kubeconfig to `~/.crucible/kubeconfig` only, never `~/.kube/config`; the
agent offers it within its next read. `cluster down` deletes it and its entries. Both are no-ops with a message when
there is nothing to do.

**Per-lab proxy.** For each running cluster lab the agent opens a Kubernetes API proxy for the chosen context:

- It connects with client-go from the full kubeconfig, so exec credential plugins (`aws eks get-token`,
  `gke-gcloud-auth-plugin`), OIDC and token refresh work as they do for `kubectl` on the laptop.
- It serves TLS with its own self-signed certificate (kubectl and helm send a token only over https) and a random
  per-lab bearer token; requests without it get 401. It passes through watches and upgraded connections (`exec`, `attach`, `port-forward`).
- It listens where only local containers reach it: `127.0.0.1` on macOS and Windows (Docker Desktop maps it to
  `host.docker.internal`), the Docker bridge gateway address on Linux (the toolbox is started with
  `--add-host host.docker.internal:host-gateway`). Never `0.0.0.0`.
- It closes when the lab ends or the agent stops.

**Provision** (message `provision` gains a `cluster` field: charts in order with name, path, values, timeout;
`cluster_wide`; context; toolbox image; the bundle carries the chart folders and values files):

1. Unpack the bundle into the lab folder; write a kubeconfig for the toolbox: server
   `https://host.docker.internal:<port>` with the proxy's certificate as `certificate-authority-data`, the per-lab token, default namespace `lab-<id>`, mode 0600. Start the proxy.
2. Start the toolbox: `docker run -d --label crucible.lab=<id>` with the given image, as the agent's own uid and gid
   (so it can read the 0600 kubeconfig; `HOME=/tmp` for Helm's caches), the lab folder mounted read-only at `/lab`,
   `KUBECONFIG=/lab/kubeconfig`, `LAB_NAMESPACE`, `LAB_ID`, `LAB_RELEASES`.
3. In the toolbox: `kubectl create namespace lab-<id>` labelled `app.kubernetes.io/managed-by=crucible` and
   `crucible.lab=<id>`; for each chart `helm dependency build`, `helm template` and, unless `cluster_wide`, refuse if
   the output contains a cluster-scoped kind (naming the first one); then
   `helm upgrade --install <name> /lab/<path> -n lab-<id> [-f /lab/<values>] --wait --timeout <timeout>`.
4. Run the lab's setup script if any. Every step's output goes to the provisioning log.

**Terminals and scripts.** A terminal is `docker exec -it` into the toolbox (bash, prompt showing context and
namespace); a setup or check script runs the same way with the lab's environment. The existing PTY and run-script
messages carry them, aimed at the toolbox instead of a compose service.

**Destroy.** In the toolbox: `helm uninstall` each release in reverse order, then `kubectl delete namespace lab-<id>
--wait`. Then close the proxy, remove the container and the lab folder. CRDs in a chart's `crds/` folder are not
deleted (Helm never deletes them); the authors' docs say so.

**Offline and restart.** As for laptop labs: on connect the agent reports the labs it holds (toolbox containers by
label) and tears down those Crucible has ended. A lab whose TTL passed while the agent was offline is cleaned up on
reconnect; its namespace stays on the cluster until then.

## 3. The server

- `runtime: cluster` uses a new agent-backed runner: `Available` needs the user's agent online and the chosen context
  among those it reported; `Provision` sends the cluster message; `Destroy`, `RunScript`, `OpenPTY` go through the
  agent hub as for laptop labs.
- Starting a cluster lab takes `context` (required for cluster labs, refused for others). The lab row stores it in a
  new `cluster_context` column, shown on the lab, Journey and Forge Status pages; the server never connects to it.
- Quote: hourly $0; tier `auto`; no budget reservation; excluded from the Ledger.
- `CRUCIBLE_TOOLBOX_IMAGE` (a digest-pinned reference) names the toolbox image sent with every provision; the server
  refuses to start cluster labs if it is unset.
- Removed: `cluster_usd_per_hour` (settings column dropped by a migration with a Down that restores it empty; config,
  seed, export, Forge settings field, docs), the cluster-lab pricing and approval path, and the per-lab dind pod path
  for `runtime: cluster`. The namespace, pod and exec code stays for AWS labs.
- Upgrade: the migration ends running `runtime: cluster` labs (state destroyed, end reason "cluster labs moved to
  your own cluster"); on first start the server deletes their `lab-<id>` namespaces on Crucible's cluster.

## 4. Checks and health

- **Script checks**: exit 0 passes; the output is the feedback.
- **`check: { ready: [releases] | all }`**: runs `crucible-ready <releases>` in the toolbox. `crucible-ready`
  (`cmd/crucible-ready`, built into the toolbox image) reads each release's manifest (`helm get manifest`) and judges,
  once and without waiting: Deployments, StatefulSets and DaemonSets rolled out and available; Jobs complete; Pods
  ready; PVCs bound; Services with at least one ready endpoint. It exits non-zero listing what is not ready
  (`deployment/shop: 1 of 3 ready`, `pvc/data: Pending`). Authors may call it from scripts.
- **Health shown**: after provisioning and after every check, the agent runs `crucible-ready --json all` and sends the
  result; the lab page shows each release as healthy, progressing or failing with its unready resources, and leaders
  see it on the lab in Journey and Forge Status. No polling.
- **Trust**: results are self-reported, as for laptop labs; the training's "review self-reported results" option
  applies. A setup script failing twice offers the trainee a skip and emails the training's editors, as today.

## 5. User experience and safety

- **Connect your laptop** lists the contexts the agent sees and, when there are none, how to make one
  (`kind create cluster`).
- **Starting a lab**: a context picker (name and server host), remembered per browser; for `cluster_wide` labs a
  notice that the lab creates cluster-wide resources that may remain, and a box to tick before it starts. The
  provisioning log streams.
- **Errors name the fix**: agent offline; context no longer in the kubeconfig; cluster unreachable (with the API
  error); Helm's output; undeclared cluster-scoped kind (naming it, and telling the trainee to tell the training's
  editors).
- **Authors**: a *Cluster labs* docs page: the `cluster:` block, the toolbox and its environment, `ready:`,
  `cluster_wide`, and that CRDs stay. `crucible preview` remains laptop labs only.
- **Admins**: no cluster rate in Forge settings; contexts shown on labs.
- **Safety**: Crucible never connects to a trainee's cluster; credentials stay in the agent; the toolbox sees only the
  per-lab token; the proxy is local-only, per lab, per context and closed at lab end. Scripts act with the trainee's
  rights; nothing is enforced beyond the kubeconfig except the cluster-wide check at install. No guessing of
  "production" clusters by name; the picker shows the name and host.

## 6. Testing

- **Lint**: one refused example per rule in §1; the rewritten `forge-101` Helm lab passes.
- **`crucible-ready`**: fake clientset cases for each kind and state, `all`, and the output naming unready resources.
- **Agent**: contexts read without secrets and refreshed on change; provision steps in order against a fake Docker
  and toolbox; undeclared cluster-scoped kinds refused; destroy in reverse; reconnect cleanup.
- **Proxy** (against `httptest`): 401 without the token; GET, watch and an upgraded stream pass through; real
  credentials added upstream and absent from the toolbox kubeconfig; bound only to the §2 address; closed at lab end.
- **Server**: cluster labs quote $0, start without approval and touch no budget; need the agent online and a reported
  context; provisioning sends charts and values but never check scripts or rubrics (a test that fails if a check
  script is in the bundle); `cluster_usd_per_hour` gone from settings, seed, export and UI; the upgrade migration ends
  old cluster labs.
- **UI**: render tests for the context picker, the `cluster_wide` consent, and release health on the lab page.
- **End to end**: `cluster-check` (its own kind cluster `crucible-m4` and `KUBECONFIG=.local/kind/admin.kubeconfig`,
  never `~/.kube/config` or the user's `platform` cluster) gains a journey: the agent runs with that kubeconfig; the
  trainee picks `crucible-m4`, the Forge 101 Helm lab installs, `kubectl` works in the terminal, a script check and a
  `ready:` check pass, and ending the lab removes the namespace. The AWS dry-run journey is unchanged.

## Out of scope

Argo CD in any form; Crucible connecting to trainees' clusters; a toolbox chosen per lab; polling health; cluster labs
in `crucible preview`; detecting production clusters.

## Docs to update with the change

User docs (trainees: *Cluster and AWS labs*, *Connect your laptop*; authors: new *Cluster labs*; admins: Forge
settings), spec §8.2 and §9.1 (cluster rate card), the roadmap's coverage rows, `docs/test-guide.html` (run a lab on
your own kind cluster), `docs/aws-guide.html` where it describes Crucible's cluster, and the `crucible-ui` skill for any
new pattern (context picker, consent box, release health).
