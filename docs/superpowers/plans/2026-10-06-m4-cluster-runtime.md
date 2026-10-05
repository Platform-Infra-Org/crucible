# M4 Cluster Runtime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Forge 101's new cluster lab (`runtime: cluster`) runs as one pod in its own `lab-<id>` namespace with quota, limits and a default-deny NetworkPolicy that blocks IMDS. Terminals, checks and setup scripts run through Kubernetes `exec`, so results come from the server and are not flagged self-reported.

**Architecture:** A new `ClusterRunner` in `internal/labs` implements the existing `Runner` interface (`Available/Provision/OpenPTY/RunScript/Destroy`) with client-go. Provision creates a namespace, ResourceQuota, LimitRange, NetworkPolicy and one pod that runs `dockerd` in a `docker:dind` image. In production the pod uses the `sysbox-runc` RuntimeClass and is never privileged. The runner untars the lab bundle into the pod and runs `docker compose up` there. Terminals are TTY execs of `docker compose exec <service> sh`, streamed into the existing terminal websocket through the existing `PTY` interface. Checks and setups are non-TTY execs that receive the script on stdin. Crucible-api gets a ServiceAccount and a ClusterRole, plus a ValidatingAdmissionPolicy that confines the account to lab namespaces. A sweep removes orphaned lab namespaces. Unit tests use the client-go fake clientset and a fake exec function. An opt-in `make cluster-check` runs everything for real on kind: the runner integration test with the real RBAC, then the browser e2e.

**Tech Stack:** Go 1.26, `k8s.io/client-go` + `k8s.io/api` + `k8s.io/apimachinery` (same minor version, matching k3s 1.34), `remotecommand` (WebSocket exec with SPDY fallback), Helm 3.17, kind ≥ v0.24 (NetworkPolicy support in kindnet), Playwright projects, Terraform tests with mock providers only.

**Spec:** `docs/superpowers/specs/2026-10-05-crucible-design.md` (§2 lab runtimes, §3 runner interface, §8.2 cluster runtime, §8.5 setup execution, §9.4 IMDS note, §14 security + "runner tests against kind + sysbox"). Program context: `.superpowers/sdd/program-context.md`.

## Global Constraints

- Every shell starts with `export PATH=/Users/adelin/Projects/Crucible/.local/tools/go/bin:/Users/adelin/Projects/Crucible/.local/tools:$PATH` (go1.26.8). Extra tools (a newer `kind`) go into `.local/tools/`. Never install anything system-wide.
- NEVER touch real AWS: no terraform plan/apply, no aws CLI against AWS, no docker push. `terraform test` in `deploy/aws/main` uses `mock_provider` only, which is allowed. Everything in this plan runs on Docker Desktop (arm64, 8 GiB) and kind.
- `gofmt -l .` prints nothing, `go vet ./...` is clean, and `go test -race ./...` passes. That command must not need kind: the integration test sits behind the `cluster` build tag. Docker Desktop must be running because dbtest uses testcontainers.
- Acceptance: `KEYCLOAK_PORT=8082 make local-check` still ends with `🔥 Local check passed. The forge holds.`, and `KEYCLOAK_PORT=8082 make cluster-check` ends with `🔥 Cluster check passed. The crucible holds.`
- Every commit ends with the trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- **Do not edit anything under `web/`.** Another agent owns it during M4. The lab UI already shows `lab.runtime` as a badge, and it needs nothing new.
- Spec §14: "Check scripts and lab containers never run in the API pod; cluster labs are namespace-isolated with quotas and network policies; sysbox (no privileged pods)." Privileged lab pods exist only when `CRUCIBLE_CLUSTER_PRIVILEGED=1` is set, which happens on kind dev clusters only. The Helm value that sets it is `clusterLabs.unsafePrivileged` and defaults to `false`.
- Spec §9.4: "`cluster` lab pods must be blocked from `169.254.169.254` by NetworkPolicy."
- Spec §8.5: setup output is "hidden from the trainee"; check scripts, setup scripts, hint text and quiz answers never reach the browser. The lab bundle already excludes `checks/ setup/ hints/ tasks/ lab.yaml`, and scripts travel only on exec stdin.
- Trainee-controlled text (terminal-quiz answers become `CRUCIBLE_ANSWER`) only ever travels as a single argv element. The runner never builds a shell string from it.
- Lab ids are 12 lowercase hex characters (`newLabID`). Every runner method validates the id before it calls the Kubernetes API.
- Existing behaviour stays: `e2e/tests/forge-101.spec.ts` passes unchanged, and local labs are untouched.

## Rulings made in this plan (read before starting)

1. **One pod per lab runs dockerd + `docker compose up`** (spec §8.2). Compose files are not translated into Kubernetes objects. Lab authors write one compose file, and it works for both `local` and `cluster`.
2. **Sysbox in production, privileged dind only on dev clusters.** Docker Desktop's linuxkit kernel cannot run sysbox, so kind uses a privileged `docker:dind` pod behind the explicit `CRUCIBLE_CLUSTER_PRIVILEGED=1` flag. On sysbox clusters, lab namespaces carry `pod-security.kubernetes.io/enforce: baseline`, and the admission policy refuses a lab namespace without that label. A privileged pod therefore cannot appear even if the API is misconfigured. Sysbox itself is verified on the AWS node by a human with the runbook (Task 7). The kind check proves everything else: namespaces, policies, exec, RBAC and cleanup.
3. **Egress = DNS + public internet minus private and link-local ranges.** The `except` list is `169.254.0.0/16, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10`. Ingress is denied entirely. Spec §8.2's "egress allowlist from lab.yaml" is **not** implemented. Vanilla NetworkPolicy cannot match hostnames, and labs must reach image registries. Revisit when a CNI with FQDN policies is in place.
4. **Terminals, checks and setups all run as `exec` in the `dind` container**, each wrapping `docker compose exec`. Checks still run inside the trainee's `run_in` service, as the spec says, so they are tamper-*resistant*. The trainee never sees the script and cannot report a result. They can still change the tools inside their own container. Authors who need more run the check from a separate service.
5. **Provisioning stays a goroutine.** M3 deferred "provisioning as a River job" to M4, and this plan keeps it out: every create is idempotent, the sweep already fails labs stuck in `provisioning` for 15 minutes, and Task 5 adds orphan-namespace cleanup. A River job would only add resume-after-restart, which a 2–5 minute provision does not need. `// ponytail:` note on `Start`.
6. **Pricing:** cluster labs use the same `labs.FixedRates` estimator as local labs (`CRUCIBLE_DEV_LAB_USD_PER_HOUR`, default $0, so they are auto-approved). A platform rate card comes with M6's real cloud costs.
7. **RBAC cannot be scoped by namespace label,** so the ClusterRole is cluster-wide and minimal. A `ValidatingAdmissionPolicy` (GA since Kubernetes 1.30; k3s 1.34) confines the `crucible` ServiceAccount to `lab-*` namespaces labelled `crucible.io/lab`. That covers creating pods, policies and quotas, opening `pods/exec`, and deleting namespaces.
8. **One size for every lab:** requests 500m CPU / 1 GiB; limits 2 CPU / 4 GiB / 21 GiB ephemeral; a 20 GiB `/var/lib/docker` emptyDir; quota of 1 pod. A t3a.xlarge fits about three concurrent labs at their limits. A lab-level `resources:` field is deferred (`// ponytail:` note).
9. **Testing:** unit tests use `fake.NewClientset()` plus an injected exec function. `make cluster-check` is opt-in and separate from `make local-check`. See *Why `cluster-check` is separate* below.
10. **No new web code.** The trainee sees the cluster lab through the existing lab page, and the e2e reads `self_reported` from the existing JSON API.

### Why `cluster-check` is separate from `local-check`

`local-check` already takes several minutes: compose build, Keycloak, two browser flows. Adding kind would mean creating a cluster (~1 min), pulling `docker:dind` into the node, pulling the lab images inside dockerd and running a second lab, which adds roughly 6–8 minutes on this 8 GiB machine. It would also make every M5–M7 check depend on kind and on Docker Hub reachability from inside a nested dockerd. M4 code is fully covered by fast unit tests in `go test ./...`. The cluster-only paths (real exec streams, real NetworkPolicy enforcement, real RBAC and admission) only need the expensive check when those files change, at the end of M4 and before a release. `cluster-check` reuses `local-check.sh` with `CLUSTER=1`, so the two never drift.

## Review Focus

1. **A terminal-quiz answer such as `$(touch /tmp/pwned); ' " ; rm -rf /`** must reach the check as the exact bytes in `$CRUCIBLE_ANSWER`, and nothing in it may be executed by a shell outside the script. Pinned by `TestRunScriptPassesEnvAsOneArgvElement` (Task 4) and the `answers are data` subtest of `TestClusterLabOnKind` (Task 8).
2. **A lab tries to reach IMDS, the node, the Kubernetes API, Postgres or another lab.** All of these must be blocked, while DNS and public registries stay reachable. Pinned by `TestLabNetworkPolicy` (Task 2) and the `egress` subtest (Task 8, probe pod in another namespace).
3. **The cluster has no room or the lab image cannot be pulled.** Provisioning must fail with a readable message within ~2 minutes, not leave a 15-minute spinner. Pinned by `TestProvisionFailsFast` (Task 3).
4. **The API restarts mid-provision, or a namespace delete fails.** The leftover `lab-<id>` namespace must be removed by the sweep, and namespaces of live labs must never be touched. Pinned by `TestSweepRemovesOrphanLabNamespaces` (Task 5).
5. **A check hangs (`sleep 600`) or prints megabytes, or the lab pod dies while a terminal is open.** The check must come back `timed_out` within its timeout, output is capped at 64 KiB, and the terminal closes instead of hanging. Pinned by `TestRunScriptTimeoutCapAndCancel` and `TestPTYEndsWhenExecEnds` (Task 4).

---

## File Structure

```
examples/forge-101/training.yaml                       Task 1  add module 03-cluster-heat
examples/forge-101/modules/03-cluster-heat/            Task 1  Forge 101's cluster lab (2 tasks, one with setup)
internal/labs/cluster_test.go                          Tasks 1,3  service-level cluster tests; runner lifecycle tests
go.mod, go.sum                                         Task 2  client-go, api, apimachinery
internal/labs/cluster_objects.go                       Task 2  clusterObjects(): namespace, quota, limits, netpol, pod
internal/labs/cluster_objects_test.go                  Task 2
internal/agentproto/proto.go                           Task 3  Capped (moved from internal/agent/exec.go)
internal/agent/exec.go                                 Task 3  use ap.Capped
internal/labs/cluster.go                               Task 3  ClusterRunner: New, Available, Provision, Destroy, Live, realExec
internal/labs/cluster_exec.go                          Task 4  RunScript, OpenPTY, execPTY
internal/labs/cluster_exec_test.go                     Task 4
internal/labs/service.go                               Task 5  reconcileCluster from Sweep; ponytail note on Start
cmd/crucible-api/main.go                               Task 5  CRUCIBLE_CLUSTER_LABS / CRUCIBLE_CLUSTER_PRIVILEGED wiring
deploy/helm/crucible/values.yaml                       Task 6  clusterLabs.enabled / unsafePrivileged
deploy/helm/crucible/templates/rbac.yaml               Task 6  ServiceAccount, ClusterRole(+Binding), ValidatingAdmissionPolicy(+Binding)
deploy/helm/crucible/templates/crucible.yaml           Task 6  serviceAccountName, token automount, env
deploy/helm/test.sh                                    Task 6
deploy/aws/main/{bootstrap.sh.tftpl,main.tf,variables.tf,main.tftest.hcl}  Task 7  sysbox on the k3s node
docs/runbooks/aws.md                                   Task 7  "Cluster labs (sysbox)" verification
internal/labs/cluster_integration_test.go              Task 8  //go:build cluster — real kind
scripts/cluster-check.sh, Makefile                     Task 8  kind cluster, RBAC from the chart, SA-token kubeconfigs
deploy/compose/cluster.yml                             Task 9  compose override: api joins the kind network
e2e/playwright.config.ts                               Task 9  projects local / cluster
e2e/tests/cluster-lab.spec.ts                          Task 9
scripts/local-check.sh                                 Task 9  CLUSTER=1 switch, --project
```

Task order and dependencies: Task 1 stands alone. Task 2 comes before 3, 3 before 4, and 4 before 5. Task 6 stands alone. Task 7 stands alone. Task 8 needs 1–6. Task 9 needs 8.

---

### Task 1: Forge 101 gets a cluster lab, and its checks are not self-reported

Content first. This task pins the done-when at the service level with the existing fake runner, before any Kubernetes code exists.

**Files:**
- Modify: `examples/forge-101/training.yaml`
- Create: `examples/forge-101/modules/03-cluster-heat/module.yaml`
- Create: `examples/forge-101/modules/03-cluster-heat/lab/{lab.yaml,compose.yaml}`
- Create: `examples/forge-101/modules/03-cluster-heat/lab/tasks/{01-cast.md,02-relight.md}`
- Create: `examples/forge-101/modules/03-cluster-heat/lab/checks/{01-cast.sh,02-relight.sh}`
- Create: `examples/forge-101/modules/03-cluster-heat/lab/setup/02-break-nginx.sh`
- Create: `internal/labs/cluster_test.go`

**Interfaces:**
- Produces: the module id `03-cluster-heat`, lab id `crucible-heat`, terminals `shell` and `web`, tasks `t1-cast` (check prints `Cast in the crucible.`) and `t2-relight` (setup breaks nginx; check prints `The crucible burns bright on port 80.`). Tasks 8 and 9 rely on these exact strings.

- [ ] **Step 1: Write the failing test**

Create `internal/labs/cluster_test.go`:

```go
package labs

import (
	"context"
	"strings"
	"testing"
	"time"
)

// completeFirstLab marks module 02 done so the linear training unlocks 03-cluster-heat.
func (f *fx) completeFirstLab(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, it := range []string{"before-the-lab", "lab"} {
		if err := f.s.Learn.SetItem(ctx, f.u.ID, "forge", "forge-101", "02-first-lab", it, "complete", 1); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClusterLabUnavailableWithoutRunner(t *testing.T) {
	f := setup(t, true)
	f.completeFirstLab(t)
	m, err := f.s.ModuleLab(context.Background(), f.u, "forge", "forge-101", "03-cluster-heat")
	if err != nil {
		t.Fatal(err)
	}
	if m.Runtime != "cluster" || m.RuntimeReady || !strings.Contains(m.RuntimeMessage, "cluster labs are not available yet") {
		t.Fatalf("without a cluster runner the lobby must say so: %+v", m)
	}
}

func TestClusterLabChecksAreNotSelfReported(t *testing.T) {
	f := setup(t, true)
	f.completeFirstLab(t)
	f.s.Runners["cluster"], f.s.Estimators["cluster"] = f.run, f.rates
	ctx := context.Background()
	v, err := f.s.Start(ctx, f.u, "forge", "forge-101", "03-cluster-heat")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200 && v.State != Ready; i++ {
		time.Sleep(10 * time.Millisecond)
		if v, err = f.s.Get(ctx, f.u, v.ID); err != nil {
			t.Fatal(err)
		}
	}
	if v.State != Ready || v.Runtime != "cluster" || v.SelfReported {
		t.Fatalf("cluster lab view: state %s runtime %s self_reported %v", v.State, v.Runtime, v.SelfReported)
	}
	res, err := f.s.Check(ctx, f.u, v.ID, "t1-cast", "")
	if err != nil || !res.Passed {
		t.Fatalf("check: %+v %v", res, err)
	}
	var self bool
	if err := f.s.DB.QueryRow(ctx, `SELECT self_reported FROM check_runs WHERE lab_id = $1`, v.ID).Scan(&self); err != nil {
		t.Fatal(err)
	}
	if self {
		t.Fatal("cluster checks run server-side and must not be flagged self-reported")
	}
	if got := f.run.scripts[len(f.run.scripts)-1]; got.Service != "shell" || !strings.Contains(string(got.Script), "hello crucible") {
		t.Fatalf("the check must run the t1 script in the shell service: %+v", got)
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/labs/ -run 'TestClusterLab' -count=1 2>&1 | tail -5`
Expected: FAIL. The first test returns `not found` (no module `03-cluster-heat`).

- [ ] **Step 3: Add the module**

`examples/forge-101/training.yaml`: change the modules line to

```yaml
modules: [01-welcome, 02-first-lab, 03-cluster-heat]
```

`examples/forge-101/modules/03-cluster-heat/module.yaml`:

```yaml
title: "Into the Crucible: A Cluster Lab"
items:
  - lab: lab
```

`examples/forge-101/modules/03-cluster-heat/lab/lab.yaml`:

```yaml
id: crucible-heat
runtime: cluster            # runs on Crucible's cluster: nothing to install, checks run server-side
ttl: 1h
idle_timeout: 20m
idle_warning: 5m
task_order: linear
terminals:
  - { name: shell, service: shell }
  - { name: web, service: web }
tasks:
  - id: t1-cast
    instructions: tasks/01-cast.md
    check: { script: checks/01-cast.sh, run_in: shell }
    points: 2
  - id: t2-relight
    instructions: tasks/02-relight.md
    setup: { script: setup/02-break-nginx.sh, run_in: web }
    check: { script: checks/02-relight.sh, run_in: web }
    points: 3
    hints:
      - text: "Look at the `listen` lines in `/etc/nginx/conf.d/default.conf`, then `nginx -s reload`."
```

`examples/forge-101/modules/03-cluster-heat/lab/compose.yaml` (same services as the laptop lab):

```yaml
services:
  shell:
    image: alpine:3.22
    command: ["sleep", "infinity"]
  web:
    image: nginx:1.29-alpine
```

`tasks/01-cast.md`:

```markdown
# Cast in the crucible

This lab runs on Crucible's own cluster, not on your laptop, and your checks run on the server.

In the **shell** terminal, write `hello crucible` into `/tmp/cast.txt`, then press **Check**.
```

`tasks/02-relight.md`:

```markdown
# Relight the forge

Someone moved nginx off port 80 while you were away. In the **web** terminal, put it back on port 80, reload nginx, then press **Check**.
```

`checks/01-cast.sh`:

```sh
#!/bin/sh
if grep -qx 'hello crucible' /tmp/cast.txt 2>/dev/null; then
  echo "Cast in the crucible."
  exit 0
fi
echo "/tmp/cast.txt must contain exactly: hello crucible"
exit 1
```

`checks/02-relight.sh`:

```sh
#!/bin/sh
if wget -q -T 2 -O /dev/null http://127.0.0.1:80/; then
  echo "The crucible burns bright on port 80."
  exit 0
fi
echo "nginx is not answering on port 80 yet. Did you reload it after editing?"
exit 1
```

`setup/02-break-nginx.sh`: copy it from module 02 so it keeps its executable bit:

```bash
cp -p examples/forge-101/modules/02-first-lab/lab/setup/02-break-nginx.sh examples/forge-101/modules/03-cluster-heat/lab/setup/
chmod +x examples/forge-101/modules/03-cluster-heat/lab/checks/*.sh
```

- [ ] **Step 4: Run the tests, the linter and the whole suite**

Run:
```bash
go test ./internal/labs/ -run 'TestClusterLab' -count=1 -race
go build -o bin/ ./cmd/crucible && ./bin/crucible lint examples/forge-101
go test -race ./... 2>&1 | grep -v -E '^(ok|\?)'
```
Expected: PASS, lint reports no problems, and the last command prints nothing. If a learn or content test counted Forge 101's modules (two before this task), update that count to 3. Don't change behaviour to fit it. If `Start` returns `apperr.Locked`, read `learn.EnsureUnlocked` to see which module-02 item ids count as complete, and fix `completeFirstLab` to match.

- [ ] **Step 5: Commit**

```bash
git add examples/forge-101 internal/labs/cluster_test.go
git commit -m "feat(content): Forge 101 cluster lab; cluster checks are not self-reported

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: The objects of a cluster lab: namespace, quota, limits, default-deny network policy, sysbox pod

A pure builder, tested without any cluster. It brings in client-go.

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `internal/labs/cluster_objects.go`
- Create: `internal/labs/cluster_objects_test.go`

**Interfaces:**
- Produces (package `labs`, used by Tasks 3–5 and 8):
  - `const labImage = "docker:28-dind"`, `labPod = "lab"`, `labContainer = "dind"`, `labLabel = "crucible.io/lab"`, `sysboxClass = "sysbox-runc"`
  - `var blockedEgress []string`
  - `func validLabID(id string) bool`, `func labNamespace(id string) string` (returns `"lab-" + id`)
  - `type labObjects struct{ Namespace *corev1.Namespace; Quota *corev1.ResourceQuota; Limits *corev1.LimitRange; Network *networkingv1.NetworkPolicy; Pod *corev1.Pod }`
  - `func clusterObjects(id, compose string, privileged bool) labObjects`. Object names: quota `lab-quota`, limit range `lab-limits`, policy `lab-default-deny`, pod `lab`.

- [ ] **Step 1: Add the Kubernetes libraries**

Run:
```bash
go get k8s.io/client-go@v0.34 k8s.io/api@v0.34 k8s.io/apimachinery@v0.34 k8s.io/utils@latest
go mod tidy
grep -E 'k8s.io/(client-go|api|apimachinery) ' go.mod
```
Expected: all three at the same `v0.34.x`, which matches k3s `v1.34.1+k3s1` in `deploy/aws/main/variables.tf`. If `@v0.34` cannot be resolved, use the newest `v0.3x.y` that exists for all three. Never mix minors.

- [ ] **Step 2: Write the failing tests**

Create `internal/labs/cluster_objects_test.go`:

```go
package labs

import (
	"net/netip"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const testID = "0123456789ab"

func TestClusterObjectsSysbox(t *testing.T) {
	o := clusterObjects(testID, "compose.yaml", false)
	if o.Namespace.Name != "lab-"+testID || o.Namespace.Labels[labLabel] != testID ||
		o.Namespace.Labels["pod-security.kubernetes.io/enforce"] != "baseline" {
		t.Fatalf("namespace: %+v", o.Namespace.ObjectMeta)
	}
	for _, m := range []metav1.Object{o.Quota, o.Limits, o.Network, o.Pod} {
		if m.GetNamespace() != o.Namespace.Name || m.GetLabels()[labLabel] != testID {
			t.Fatalf("%s must live in the lab namespace with the lab label", m.GetName())
		}
	}
	sp := o.Pod.Spec
	if sp.RuntimeClassName == nil || *sp.RuntimeClassName != "sysbox-runc" || sp.HostUsers == nil || *sp.HostUsers {
		t.Fatalf("production lab pods use sysbox in a user namespace: %+v %+v", sp.RuntimeClassName, sp.HostUsers)
	}
	if sp.AutomountServiceAccountToken == nil || *sp.AutomountServiceAccountToken || sp.EnableServiceLinks == nil || *sp.EnableServiceLinks {
		t.Fatal("lab pods get no API token and no service env vars")
	}
	if len(sp.Containers) != 1 {
		t.Fatalf("one container: %d", len(sp.Containers))
	}
	c := sp.Containers[0]
	if c.Name != labContainer || c.Image != labImage {
		t.Fatalf("container %s %s", c.Name, c.Image)
	}
	if c.SecurityContext != nil && c.SecurityContext.Privileged != nil && *c.SecurityContext.Privileged {
		t.Fatal("never privileged with sysbox")
	}
	if !slices.Equal(c.Args, []string{"dockerd", "--host=unix:///var/run/docker.sock"}) {
		t.Fatalf("dockerd must listen on the unix socket only: %v", c.Args)
	}
	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	if env["COMPOSE_FILE"] != "/lab/compose.yaml" || env["COMPOSE_PROJECT_NAME"] != "lab" {
		t.Fatalf("compose env: %v", env)
	}
	if c.Resources.Limits.Memory().String() != "4Gi" || c.Resources.Limits.Cpu().String() != "2" {
		t.Fatalf("limits: %v", c.Resources.Limits)
	}
	if o.Quota.Spec.Hard.Pods().Value() != 1 {
		t.Fatalf("quota: %v", o.Quota.Spec.Hard)
	}
}

func TestClusterObjectsPrivilegedDev(t *testing.T) {
	o := clusterObjects(testID, "compose.yaml", true)
	if _, ok := o.Namespace.Labels["pod-security.kubernetes.io/enforce"]; ok {
		t.Fatal("dev namespaces cannot enforce baseline: the pod is privileged")
	}
	sp := o.Pod.Spec
	if sp.RuntimeClassName != nil || sp.HostUsers != nil {
		t.Fatal("kind has no sysbox runtime class and privileged pods need host users")
	}
	if p := sp.Containers[0].SecurityContext; p == nil || p.Privileged == nil || !*p.Privileged {
		t.Fatal("dev dind is privileged")
	}
}

func blockedBy(cidrs []string, ip string) bool {
	a := netip.MustParseAddr(ip)
	for _, c := range cidrs {
		if netip.MustParsePrefix(c).Contains(a) {
			return true
		}
	}
	return false
}

func TestLabNetworkPolicy(t *testing.T) {
	np := clusterObjects(testID, "compose.yaml", false).Network.Spec
	if len(np.PodSelector.MatchLabels) != 0 || len(np.PodSelector.MatchExpressions) != 0 {
		t.Fatal("the policy selects every pod in the namespace")
	}
	if !slices.Contains(np.PolicyTypes, networkingv1.PolicyTypeIngress) || !slices.Contains(np.PolicyTypes, networkingv1.PolicyTypeEgress) || len(np.Ingress) != 0 {
		t.Fatalf("default-deny both ways, no ingress rules: %+v", np)
	}
	if len(np.Egress) != 2 {
		t.Fatalf("exactly DNS + public internet: %+v", np.Egress)
	}
	dns := np.Egress[0]
	if len(dns.To) != 1 || dns.To[0].PodSelector.MatchLabels["k8s-app"] != "kube-dns" ||
		dns.To[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "kube-system" || len(dns.Ports) != 2 {
		t.Fatalf("dns rule: %+v", dns)
	}
	for _, p := range dns.Ports {
		if p.Port.IntValue() != 53 || (*p.Protocol != corev1.ProtocolUDP && *p.Protocol != corev1.ProtocolTCP) {
			t.Fatalf("dns port: %+v", p)
		}
	}
	web := np.Egress[1]
	if len(web.Ports) != 0 || len(web.To) != 1 || web.To[0].IPBlock == nil || web.To[0].IPBlock.CIDR != "0.0.0.0/0" {
		t.Fatalf("internet rule: %+v", web)
	}
	except := web.To[0].IPBlock.Except
	for _, ip := range []string{"169.254.169.254", "10.43.0.1", "10.42.0.7", "172.31.5.9", "172.18.0.2", "192.168.1.1", "100.64.0.10"} {
		if !blockedBy(except, ip) {
			t.Errorf("%s (IMDS, cluster, VPC or node) must be blocked", ip)
		}
	}
	if blockedBy(except, "104.16.0.1") || blockedBy(except, "54.230.1.1") {
		t.Error("public registries must stay reachable")
	}
}
```

- [ ] **Step 3: Run them to see them fail**

Run: `go test ./internal/labs/ -run 'TestClusterObjects|TestLabNetworkPolicy' -count=1 2>&1 | tail -4`
Expected: FAIL with `undefined: clusterObjects`.

- [ ] **Step 4: Implement the builder**

Create `internal/labs/cluster_objects.go`:

```go
package labs

import (
	"regexp"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

const (
	labImage     = "docker:28-dind" // dockerd + the compose plugin; bump deliberately (each new node pulls it once)
	labPod       = "lab"
	labContainer = "dind"
	labLabel     = "crucible.io/lab"
	sysboxClass  = "sysbox-runc"
)

// ponytail: one size for every lab (about three concurrent labs at their limits on a t3a.xlarge); add a lab.yaml
// resources block when a lab needs more.
var (
	labLimits = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("4Gi"),
		corev1.ResourceEphemeralStorage: resource.MustParse("21Gi")}
	labRequests = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("1Gi")}
	dockerDisk  = resource.MustParse("20Gi")
)

// blockedEgress can never be reached from a lab: link-local (cloud metadata, IMDS at 169.254.169.254) and every
// private range, which covers the node, the Kubernetes API, Postgres, crucible-api and other labs. There is no IPv6
// allow rule, so IPv6 (including the IPv6 IMDS endpoint) is denied as well.
var blockedEgress = []string{"169.254.0.0/16", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10"}

var labIDRe = regexp.MustCompile(`^[0-9a-f]{12}$`)

// validLabID guards every Kubernetes call: a lab id becomes part of a namespace name.
func validLabID(id string) bool { return labIDRe.MatchString(id) }

func labNamespace(id string) string { return "lab-" + id }

type labObjects struct {
	Namespace *corev1.Namespace
	Quota     *corev1.ResourceQuota
	Limits    *corev1.LimitRange
	Network   *networkingv1.NetworkPolicy
	Pod       *corev1.Pod
}

// clusterObjects is everything one cluster lab runs in (spec §8.2). privileged is only for dev clusters without
// sysbox (kind on Docker Desktop): a privileged dind pod and no Pod Security enforcement.
func clusterObjects(id, compose string, privileged bool) labObjects {
	ns := labNamespace(id)
	meta := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{labLabel: id}}
	}
	nsLabels := map[string]string{labLabel: id}
	if !privileged {
		nsLabels["pod-security.kubernetes.io/enforce"] = "baseline" // the API server refuses privileged pods here
	}
	pod := &corev1.Pod{ObjectMeta: meta(labPod), Spec: corev1.PodSpec{
		RestartPolicy:                corev1.RestartPolicyNever,
		AutomountServiceAccountToken: ptr.To(false),
		EnableServiceLinks:           ptr.To(false),
		Containers: []corev1.Container{{
			Name:  labContainer,
			Image: labImage,
			Args:  []string{"dockerd", "--host=unix:///var/run/docker.sock"}, // no TCP socket (the image default adds :2375)
			Env: []corev1.EnvVar{
				{Name: "COMPOSE_PROJECT_NAME", Value: "lab"},
				{Name: "COMPOSE_FILE", Value: "/lab/" + compose}, // every exec inherits it: plain `docker compose …` works
				{Name: "DOCKER_TLS_CERTDIR", Value: ""},
			},
			Resources: corev1.ResourceRequirements{Limits: labLimits, Requests: labRequests},
			ReadinessProbe: &corev1.Probe{
				ProbeHandler:  corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"docker", "info"}}},
				PeriodSeconds: 2, TimeoutSeconds: 5,
			},
			VolumeMounts: []corev1.VolumeMount{{Name: "docker", MountPath: "/var/lib/docker"}, {Name: "lab", MountPath: "/lab"}},
		}},
		Volumes: []corev1.Volume{
			{Name: "docker", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: ptr.To(dockerDisk)}}},
			{Name: "lab", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		},
	}}
	if privileged {
		pod.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{Privileged: ptr.To(true)}
	} else {
		pod.Spec.RuntimeClassName = ptr.To(sysboxClass)
		pod.Spec.HostUsers = ptr.To(false)
	}
	dns := networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{{
			NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}},
			PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}},
		}},
		Ports: []networkingv1.NetworkPolicyPort{
			{Protocol: ptr.To(corev1.ProtocolUDP), Port: ptr.To(intstr.FromInt32(53))},
			{Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To(intstr.FromInt32(53))},
		},
	}
	internet := networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0", Except: blockedEgress}}},
	}
	return labObjects{
		Namespace: &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: nsLabels}},
		Quota: &corev1.ResourceQuota{ObjectMeta: meta("lab-quota"), Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{
			corev1.ResourcePods:                   resource.MustParse("1"),
			corev1.ResourceRequestsCPU:            labRequests[corev1.ResourceCPU],
			corev1.ResourceRequestsMemory:         labRequests[corev1.ResourceMemory],
			corev1.ResourceLimitsCPU:              labLimits[corev1.ResourceCPU],
			corev1.ResourceLimitsMemory:           labLimits[corev1.ResourceMemory],
			corev1.ResourceLimitsEphemeralStorage: labLimits[corev1.ResourceEphemeralStorage],
		}}},
		Limits: &corev1.LimitRange{ObjectMeta: meta("lab-limits"), Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{
			Type:           corev1.LimitTypeContainer,
			Max:            corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("4Gi")},
			Default:        corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("2Gi")},
			DefaultRequest: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("512Mi")},
		}}}},
		Network: &networkingv1.NetworkPolicy{ObjectMeta: meta("lab-default-deny"), Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Egress:      []networkingv1.NetworkPolicyEgressRule{dns, internet},
		}},
		Pod: pod,
	}
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/labs/ -run 'TestClusterObjects|TestLabNetworkPolicy' -count=1 -race && go vet ./internal/labs/ && gofmt -l internal/`
Expected: PASS. vet and gofmt print nothing.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/labs/cluster_objects.go internal/labs/cluster_objects_test.go
git commit -m "feat(labs): cluster lab objects: namespace, quota, limits, default-deny egress without IMDS, sysbox pod

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: ClusterRunner lifecycle: provision, fail fast, destroy, list live labs

**Files:**
- Modify: `internal/agentproto/proto.go` (add `Capped`)
- Modify: `internal/agent/exec.go` (delete `capped`; `RunLimited` uses `ap.Capped`)
- Create: `internal/labs/cluster.go`
- Modify: `internal/labs/cluster_test.go` (append runner tests)

**Interfaces:**
- Consumes: `clusterObjects`, `validLabID`, `labNamespace`, `labPod`, `labContainer`, `labLabel` (Task 2).
- Produces:
  - `agentproto.Capped` with methods `Write(p []byte) (int, error)` and `Bytes() []byte`. It keeps the first `MaxOutput` bytes and is safe for concurrent writes.
  - `type ClusterRunner struct{ Client kubernetes.Interface; Config *rest.Config; Privileged bool; Poll time.Duration; exec execFunc }`
  - `type execFunc func(ctx context.Context, ns, pod string, cmd []string, o remotecommand.StreamOptions) error`
  - `func NewClusterRunner(cfg *rest.Config, privileged bool) (*ClusterRunner, error)`
  - `(*ClusterRunner).Available(*Instance) error`, `.Provision(ctx, *Instance, bundle []byte, compose string) error`, `.Destroy(ctx, *Instance) error`, `.Live(ctx) ([]string, error)`
  - Unexported helpers used by Task 4: `(*ClusterRunner).execFn() execFunc`, `(*ClusterRunner).run(ctx, ns string, cmd []string, stdin io.Reader, out io.Writer) error`, `tailOf(b []byte) string`
  - `var unschedulableGrace = 2 * time.Minute` (a var so tests can set it to 0)

- [ ] **Step 1: Move the output cap into agentproto**

Append to `internal/agentproto/proto.go`, adding imports `bytes` and `sync`:

```go
// Capped keeps the first MaxOutput bytes written to it and silently drops the rest. Safe for concurrent writers
// (stdout and stderr of one script).
type Capped struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *Capped) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if room := MaxOutput - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (c *Capped) Bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Clone(c.buf.Bytes())
}
```

In `internal/agent/exec.go`, delete the `capped` type and its two methods, change `out := &capped{}` in `RunLimited` to `out := &ap.Capped{}`, and drop the now-unused `sync` import.

Run: `go test ./internal/agent/ ./internal/agentproto/ -count=1 -race`
Expected: PASS. The existing agent tests cover the cap.

- [ ] **Step 2: Write the failing runner tests**

Append to `internal/labs/cluster_test.go` and merge the imports:

```go
import (
	// add to the existing block:
	"errors"
	"io"
	"slices"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
)

// onPodCreate lets a test decide what the "kubelet" reports for the lab pod.
func onPodCreate(cs *fake.Clientset, mutate func(*corev1.Pod)) {
	cs.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		mutate(a.(k8stesting.CreateAction).GetObject().(*corev1.Pod))
		return false, nil, nil // fall through: the tracker stores the mutated pod
	})
}

func podReady(p *corev1.Pod) {
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
}

type execCall struct {
	ns, pod string
	cmd     []string
	stdin   []byte
	tty     bool
}

type fakeExec struct {
	mu    sync.Mutex
	calls []execCall
	fn    func(ctx context.Context, cmd []string, o remotecommand.StreamOptions) error
}

func (f *fakeExec) exec(ctx context.Context, ns, pod string, cmd []string, o remotecommand.StreamOptions) error {
	var in []byte
	if o.Stdin != nil && !o.Tty {
		in, _ = io.ReadAll(o.Stdin)
	}
	f.mu.Lock()
	f.calls = append(f.calls, execCall{ns, pod, cmd, in, o.Tty})
	fn := f.fn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, cmd, o)
	}
	return nil
}

func (f *fakeExec) last() execCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

func testRunner(cs *fake.Clientset, fe *fakeExec) *ClusterRunner {
	return &ClusterRunner{Client: cs, exec: fe.exec, Poll: time.Millisecond}
}

func TestProvisionCreatesIsolatedLab(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	onPodCreate(cs, podReady)
	fe := &fakeExec{}
	r := testRunner(cs, fe)
	if err := r.Provision(ctx, &Instance{ID: testID}, []byte("tarball"), "compose.yaml"); err != nil {
		t.Fatal(err)
	}
	ns := "lab-" + testID
	var order []string
	for _, a := range cs.Actions() {
		if a.GetVerb() == "create" {
			order = append(order, a.GetResource().Resource)
		}
	}
	if !slices.Equal(order, []string{"namespaces", "resourcequotas", "limitranges", "networkpolicies", "pods"}) {
		t.Fatalf("the policy and quota must exist before the pod: %v", order)
	}
	if len(fe.calls) != 2 {
		t.Fatalf("exec calls: %+v", fe.calls)
	}
	untar, up := fe.calls[0], fe.calls[1]
	if untar.ns != ns || untar.pod != "lab" || !slices.Equal(untar.cmd, []string{"tar", "xzf", "-", "-C", "/lab"}) || string(untar.stdin) != "tarball" {
		t.Fatalf("untar: %+v", untar)
	}
	if !slices.Equal(up.cmd, []string{"docker", "compose", "up", "-d", "--wait"}) {
		t.Fatalf("compose up: %+v", up)
	}
	// a second attempt (sweep retry, double start) is harmless
	if err := r.Provision(ctx, &Instance{ID: testID}, []byte("tarball"), "compose.yaml"); err != nil {
		t.Fatalf("provision must be idempotent: %v", err)
	}
}

func TestProvisionRejectsBadInput(t *testing.T) {
	cs := fake.NewClientset()
	r := testRunner(cs, &fakeExec{})
	for _, tc := range []struct{ id, compose string }{{"../kube-system", "compose.yaml"}, {"ABCDEF012345", "compose.yaml"}, {testID, "../x.yaml"}, {testID, "/etc/x.yaml"}} {
		if err := r.Provision(context.Background(), &Instance{ID: tc.id}, nil, tc.compose); err == nil {
			t.Fatalf("%+v must be refused", tc)
		}
	}
	if len(cs.Actions()) != 0 {
		t.Fatalf("nothing may reach the API for bad input: %v", cs.Actions())
	}
}

func TestProvisionFailsFast(t *testing.T) {
	old := unschedulableGrace
	unschedulableGrace = 0
	t.Cleanup(func() { unschedulableGrace = old })
	cases := map[string]struct {
		mutate func(*corev1.Pod)
		want   string
	}{
		"cluster full": {func(p *corev1.Pod) {
			p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable}}
		}, "no room for another lab"},
		"image": {func(p *corev1.Pod) {
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: labContainer,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "pull access denied"}}}}
		}, "could not be started"},
		"dockerd died": {func(p *corev1.Pod) { p.Status.Phase = corev1.PodFailed; p.Status.Message = "dockerd exited" }, "stopped"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cs := fake.NewClientset()
			onPodCreate(cs, tc.mutate)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := testRunner(cs, &fakeExec{}).Provision(ctx, &Instance{ID: testID}, nil, "compose.yaml")
			if err == nil || !strings.Contains(err.Error(), tc.want) || ctx.Err() != nil {
				t.Fatalf("want a fast %q error, got %v (ctx %v)", tc.want, err, ctx.Err())
			}
		})
	}
}

func TestProvisionReportsComposeFailure(t *testing.T) {
	cs := fake.NewClientset()
	onPodCreate(cs, podReady)
	fe := &fakeExec{fn: func(_ context.Context, cmd []string, o remotecommand.StreamOptions) error {
		if cmd[0] == "docker" {
			_, _ = o.Stdout.Write([]byte("pull access denied for nope"))
			return utilexec.CodeExitError{Err: errors.New("command terminated with exit code 1"), Code: 1}
		}
		return nil
	}}
	err := testRunner(cs, fe).Provision(context.Background(), &Instance{ID: testID}, nil, "compose.yaml")
	if err == nil || !strings.Contains(err.Error(), "docker compose up") || !strings.Contains(err.Error(), "pull access denied") {
		t.Fatalf("the trainee sees the compose log tail: %v", err)
	}
}

func labNS(id string, deleting bool) *corev1.Namespace {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "lab-" + id, Labels: map[string]string{labLabel: id}}}
	if deleting {
		now := metav1.Now()
		ns.DeletionTimestamp, ns.Finalizers = &now, []string{"kubernetes"}
	}
	return ns
}

func TestDestroyAndLive(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset(labNS(testID, false), labNS("bbbbbbbbbbbb", true),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "sneaky", Labels: map[string]string{labLabel: "cccccccccccc"}}})
	r := testRunner(cs, &fakeExec{})
	ids, err := r.Live(ctx)
	if err != nil || !slices.Equal(ids, []string{testID}) {
		t.Fatalf("live = running lab namespaces only (not terminating, not mislabelled): %v %v", ids, err)
	}
	if err := r.Destroy(ctx, &Instance{ID: testID}); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CoreV1().Namespaces().Get(ctx, "lab-"+testID, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("namespace must be gone: %v", err)
	}
	if err := r.Destroy(ctx, &Instance{ID: testID}); err != nil {
		t.Fatalf("destroying twice is fine: %v", err)
	}
	if err := r.Destroy(ctx, &Instance{ID: "default"}); err == nil {
		t.Fatal("only lab ids may be destroyed")
	}
	if _, err := cs.CoreV1().Namespaces().Get(ctx, "default", metav1.GetOptions{}); err != nil {
		t.Fatal("default must survive")
	}
}
```

- [ ] **Step 3: Run them to see them fail**

Run: `go test ./internal/labs/ -run 'TestProvision|TestDestroyAndLive' -count=1 2>&1 | tail -4`
Expected: FAIL with `undefined: ClusterRunner`.

- [ ] **Step 4: Implement the runner lifecycle**

Create `internal/labs/cluster.go`:

```go
package labs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/utils/ptr"

	ap "crucible/internal/agentproto"
)

// ClusterRunner runs each lab as one pod in its own namespace lab-<id> (spec §8.2). dockerd runs the lab's compose
// file in the pod (sysbox runtime: never privileged in production). Terminals, checks and setups are exec calls
// through the Kubernetes API, so results come from the server, never from the trainee's machine.
type ClusterRunner struct {
	Client     kubernetes.Interface
	Config     *rest.Config  // used by real exec; unit tests set exec instead
	Privileged bool          // dev clusters without sysbox only (CRUCIBLE_CLUSTER_PRIVILEGED=1)
	Poll       time.Duration // pod readiness poll interval; 0 = 2s
	exec       execFunc      // nil = realExec
}

type execFunc func(ctx context.Context, ns, pod string, cmd []string, o remotecommand.StreamOptions) error

// unschedulableGrace is how long a lab pod may wait for room before provisioning fails (a var for tests).
var unschedulableGrace = 2 * time.Minute

var errClusterFull = errors.New("the lab cluster has no room for another lab right now; try again in a few minutes")

func NewClusterRunner(cfg *rest.Config, privileged bool) (*ClusterRunner, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &ClusterRunner{Client: cs, Config: cfg, Privileged: privileged}, nil
}

// Available: the cluster is always there; capacity is checked when the pod is scheduled (errClusterFull).
func (c *ClusterRunner) Available(*Instance) error { return nil }

func (c *ClusterRunner) Provision(ctx context.Context, inst *Instance, bundle []byte, compose string) error {
	if !validLabID(inst.ID) || !filepath.IsLocal(compose) {
		return errors.New("invalid lab id or compose file name")
	}
	o := clusterObjects(inst.ID, compose, c.Privileged)
	core, ns := c.Client.CoreV1(), o.Namespace.Name
	create := []func() error{
		func() error { _, err := core.Namespaces().Create(ctx, o.Namespace, metav1.CreateOptions{}); return err },
		func() error { _, err := core.ResourceQuotas(ns).Create(ctx, o.Quota, metav1.CreateOptions{}); return err },
		func() error { _, err := core.LimitRanges(ns).Create(ctx, o.Limits, metav1.CreateOptions{}); return err },
		func() error {
			_, err := c.Client.NetworkingV1().NetworkPolicies(ns).Create(ctx, o.Network, metav1.CreateOptions{})
			return err
		},
		func() error { _, err := core.Pods(ns).Create(ctx, o.Pod, metav1.CreateOptions{}); return err }, // last: isolated from its first packet
	}
	for _, step := range create {
		if err := step(); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating the lab namespace: %w", err)
		}
	}
	if err := c.waitReady(ctx, ns); err != nil {
		return err
	}
	untar := &ap.Capped{}
	if err := c.run(ctx, ns, []string{"tar", "xzf", "-", "-C", "/lab"}, bytes.NewReader(bundle), untar); err != nil {
		return fmt.Errorf("unpacking the lab: %w: %s", err, tailOf(untar.Bytes()))
	}
	up := &ap.Capped{}
	if err := c.run(ctx, ns, []string{"docker", "compose", "up", "-d", "--wait"}, nil, up); err != nil {
		return fmt.Errorf("docker compose up: %w: %s", err, tailOf(up.Bytes()))
	}
	return nil
}

// waitReady polls the lab pod until dockerd answers (readiness probe), failing fast on states that won't heal.
func (c *ClusterRunner) waitReady(ctx context.Context, ns string) error {
	poll := c.Poll
	if poll == 0 {
		poll = 2 * time.Second
	}
	var unschedulableSince time.Time
	for {
		p, err := c.Client.CoreV1().Pods(ns).Get(ctx, labPod, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("reading the lab pod: %w", err)
		}
		if p.Status.Phase == corev1.PodFailed {
			return fmt.Errorf("the lab pod stopped: %s %s", p.Status.Reason, p.Status.Message)
		}
		for _, cs := range p.Status.ContainerStatuses {
			if w := cs.State.Waiting; w != nil {
				switch w.Reason {
				case "ImagePullBackOff", "InvalidImageName", "CreateContainerConfigError", "CreateContainerError":
					return fmt.Errorf("the lab image could not be started (%s): %s", w.Reason, w.Message)
				}
			}
		}
		for _, cond := range p.Status.Conditions {
			switch {
			case cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue:
				return nil
			case cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse && cond.Reason == corev1.PodReasonUnschedulable:
				if unschedulableSince.IsZero() {
					unschedulableSince = time.Now()
				}
				if time.Since(unschedulableSince) >= unschedulableGrace {
					return errClusterFull
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// Destroy deletes the lab namespace; Kubernetes removes everything in it. A missing namespace is success.
func (c *ClusterRunner) Destroy(ctx context.Context, inst *Instance) error {
	if !validLabID(inst.ID) {
		return errors.New("invalid lab id")
	}
	err := c.Client.CoreV1().Namespaces().Delete(ctx, labNamespace(inst.ID),
		metav1.DeleteOptions{PropagationPolicy: ptr.To(metav1.DeletePropagationBackground)})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// Live lists the ids of labs that still have a namespace that is not already being deleted.
func (c *ClusterRunner) Live(ctx context.Context) ([]string, error) {
	list, err := c.Client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{LabelSelector: labLabel})
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, ns := range list.Items {
		id := ns.Labels[labLabel]
		if ns.DeletionTimestamp == nil && validLabID(id) && ns.Name == labNamespace(id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (c *ClusterRunner) execFn() execFunc {
	if c.exec != nil {
		return c.exec
	}
	return c.realExec
}

// run execs cmd in the lab's dind container without a TTY; stdout and stderr both go to out.
func (c *ClusterRunner) run(ctx context.Context, ns string, cmd []string, stdin io.Reader, out io.Writer) error {
	return c.execFn()(ctx, ns, labPod, cmd, remotecommand.StreamOptions{Stdin: stdin, Stdout: out, Stderr: out})
}

// realExec is `kubectl exec`: WebSocket first, SPDY for older API servers or proxies that refuse the upgrade.
func (c *ClusterRunner) realExec(ctx context.Context, ns, pod string, cmd []string, o remotecommand.StreamOptions) error {
	req := c.Client.CoreV1().RESTClient().Post().Resource("pods").Namespace(ns).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{Container: labContainer, Command: cmd, Stdin: o.Stdin != nil,
			Stdout: o.Stdout != nil, Stderr: o.Stderr != nil, TTY: o.Tty}, scheme.ParameterCodec)
	ws, err := remotecommand.NewWebSocketExecutor(c.Config, "GET", req.URL().String())
	if err != nil {
		return err
	}
	spdy, err := remotecommand.NewSPDYExecutor(c.Config, "POST", req.URL())
	if err != nil {
		return err
	}
	ex, err := remotecommand.NewFallbackExecutor(ws, spdy, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	})
	if err != nil {
		return err
	}
	return ex.StreamWithContext(ctx, o)
}

// tailOf keeps the end of a command's output for an error message the trainee sees.
func tailOf(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 2000 {
		s = "…" + s[len(s)-2000:]
	}
	return s
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/labs/ -run 'TestProvision|TestDestroyAndLive|TestClusterObjects' -count=1 -race && go vet ./... && gofmt -l .`
Expected: PASS, and no output from vet or gofmt. If the fake tracker drops the status set by the reactor, so that the pod never looks Ready, change `onPodCreate` to update the status from a goroutine instead: `cs.CoreV1().Pods(ns).UpdateStatus(...)` after the create. Don't change `waitReady`.

- [ ] **Step 6: Commit**

```bash
git add internal/agentproto/proto.go internal/agent/exec.go internal/labs/cluster.go internal/labs/cluster_test.go
git commit -m "feat(labs): cluster runner lifecycle: isolated namespace, fail-fast provisioning, destroy, live list

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Checks, setups and terminals over exec

**Files:**
- Create: `internal/labs/cluster_exec.go`
- Create: `internal/labs/cluster_exec_test.go`

**Interfaces:**
- Consumes: `ClusterRunner`, `execFn`, `fakeExec`, `testRunner`, `testID` (Tasks 2–3), `ScriptSpec`/`ScriptResult`/`PTY` (`internal/labs/model.go`), `ap.Capped`, `ap.MaxOutput`.
- Produces: `(*ClusterRunner).RunScript(ctx, *Instance, ScriptSpec) (ScriptResult, error)` and `(*ClusterRunner).OpenPTY(ctx, *Instance, service string, cols, rows int) (PTY, error)`. With these, `*ClusterRunner` satisfies `labs.Runner`. `var _ Runner = (*ClusterRunner)(nil)` sits in the file.

- [ ] **Step 1: Write the failing tests**

Create `internal/labs/cluster_exec_test.go`:

```go
package labs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"

	ap "crucible/internal/agentproto"
)

func TestRunScriptPassesEnvAsOneArgvElement(t *testing.T) {
	fe := &fakeExec{}
	r := testRunner(fake.NewClientset(), fe)
	evil := `$(touch /tmp/pwned); ' " ; rm -rf /`
	_, err := r.RunScript(context.Background(), &Instance{ID: testID},
		ScriptSpec{Service: "web", Script: []byte("echo hi"), Env: map[string]string{"CRUCIBLE_ANSWER": evil, "A": "1"}, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	c := fe.last()
	want := []string{"timeout", "-s", "KILL", "35", "docker", "compose", "exec", "-T",
		"-e", "A=1", "-e", "CRUCIBLE_ANSWER=" + evil, "web", "sh", "-s"}
	if !slices.Equal(c.cmd, want) || c.tty || string(c.stdin) != "echo hi" || c.ns != "lab-"+testID {
		t.Fatalf("argv/stdin:\n got %q\nwant %q\nstdin %q", c.cmd, want, c.stdin)
	}
}

func TestRunScriptExitCodes(t *testing.T) {
	fe := &fakeExec{fn: func(_ context.Context, _ []string, o remotecommand.StreamOptions) error {
		_, _ = o.Stdout.Write([]byte("nope"))
		return utilexec.CodeExitError{Err: errors.New("command terminated with exit code 3"), Code: 3}
	}}
	res, err := testRunner(fake.NewClientset(), fe).RunScript(context.Background(), &Instance{ID: testID},
		ScriptSpec{Service: "shell", Script: []byte("exit 3"), Timeout: time.Second})
	if err != nil || res.ExitCode != 3 || res.Output != "nope" || res.TimedOut {
		t.Fatalf("%+v %v", res, err)
	}
	fe.fn = func(context.Context, []string, remotecommand.StreamOptions) error { return errors.New("pods \"lab\" not found") }
	if _, err := testRunner(fake.NewClientset(), fe).RunScript(context.Background(), &Instance{ID: testID},
		ScriptSpec{Service: "shell", Timeout: time.Second}); err == nil {
		t.Fatal("an exec that could not run is an error, not a failed check")
	}
}

func TestRunScriptTimeoutCapAndCancel(t *testing.T) {
	hang := &fakeExec{fn: func(ctx context.Context, _ []string, _ remotecommand.StreamOptions) error { <-ctx.Done(); return ctx.Err() }}
	start := time.Now()
	res, err := testRunner(fake.NewClientset(), hang).RunScript(context.Background(), &Instance{ID: testID},
		ScriptSpec{Service: "shell", Script: []byte("sleep 600"), Timeout: 50 * time.Millisecond})
	if err != nil || !res.TimedOut || res.ExitCode != -1 || !strings.HasSuffix(res.Output, "[crucible] script timed out") || time.Since(start) > 2*time.Second {
		t.Fatalf("timeout: %+v %v after %v", res, err, time.Since(start))
	}

	flood := &fakeExec{fn: func(_ context.Context, _ []string, o remotecommand.StreamOptions) error {
		_, _ = o.Stdout.Write(bytes.Repeat([]byte("x"), 100<<10))
		return nil
	}}
	res, err = testRunner(fake.NewClientset(), flood).RunScript(context.Background(), &Instance{ID: testID},
		ScriptSpec{Service: "shell", Timeout: time.Second})
	if err != nil || len(res.Output) != ap.MaxOutput {
		t.Fatalf("output cap: %d %v", len(res.Output), err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	res, err = testRunner(fake.NewClientset(), hang).RunScript(ctx, &Instance{ID: testID},
		ScriptSpec{Service: "shell", Timeout: 10 * time.Second})
	if err == nil || res.TimedOut {
		t.Fatalf("a cancelled request is an error, not a timed-out check: %+v %v", res, err)
	}
}

func TestRunScriptRejectsBadTargets(t *testing.T) {
	fe := &fakeExec{}
	r := testRunner(fake.NewClientset(), fe)
	for _, tc := range []struct{ id, svc string }{{"x", "shell"}, {testID, ""}, {testID, "-it"}} {
		if _, err := r.RunScript(context.Background(), &Instance{ID: tc.id}, ScriptSpec{Service: tc.svc, Timeout: time.Second}); err == nil {
			t.Fatalf("%+v must be refused", tc)
		}
		if _, err := r.OpenPTY(context.Background(), &Instance{ID: tc.id}, tc.svc, 80, 24); err == nil {
			t.Fatalf("pty %+v must be refused", tc)
		}
	}
	if len(fe.calls) != 0 {
		t.Fatal("nothing may be executed")
	}
}

// echoUpper is a fake shell: it upper-cases stdin to stdout and records terminal sizes.
func echoUpper(sizes chan remotecommand.TerminalSize) func(context.Context, []string, remotecommand.StreamOptions) error {
	return func(_ context.Context, _ []string, o remotecommand.StreamOptions) error {
		go func() {
			for s := o.TerminalSizeQueue.Next(); s != nil; s = o.TerminalSizeQueue.Next() {
				sizes <- *s
			}
		}()
		buf := make([]byte, 64)
		for {
			n, err := o.Stdin.Read(buf)
			if err != nil {
				return nil
			}
			_, _ = o.Stdout.Write(bytes.ToUpper(buf[:n]))
		}
	}
}

func TestPTYStreamsAndResizes(t *testing.T) {
	sizes := make(chan remotecommand.TerminalSize, 8)
	fe := &fakeExec{fn: echoUpper(sizes)}
	p, err := testRunner(fake.NewClientset(), fe).OpenPTY(context.Background(), &Instance{ID: testID}, "shell", 120, 32)
	if err != nil {
		t.Fatal(err)
	}
	if s := <-sizes; s.Width != 120 || s.Height != 32 {
		t.Fatalf("initial size %+v", s)
	}
	if _, err := p.Write([]byte("hi\n")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	n, err := p.Read(buf)
	if err != nil || string(buf[:n]) != "HI\n" {
		t.Fatalf("read %q %v", buf[:n], err)
	}
	_ = p.Resize(100, 40)
	if s := <-sizes; s.Width != 100 || s.Height != 40 {
		t.Fatalf("resize %+v", s)
	}
	c := fe.last()
	if !c.tty || !slices.Equal(c.cmd[:4], []string{"docker", "compose", "exec", "shell"}) {
		t.Fatalf("pty exec: %+v", c)
	}
	_ = p.Close()
	_ = p.Close()
	_ = p.Resize(10, 10) // after close: no panic, no block
	if _, err := p.Read(buf); err == nil {
		t.Fatal("read after close must fail")
	}
}

func TestPTYEndsWhenExecEnds(t *testing.T) {
	fe := &fakeExec{fn: func(context.Context, []string, remotecommand.StreamOptions) error { return errors.New("pod gone") }}
	p, err := testRunner(fake.NewClientset(), fe).OpenPTY(context.Background(), &Instance{ID: testID}, "shell", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := io.ReadAll(p); done <- err }()
	select {
	case <-done: // the websocket handler sees the read end and closes the terminal
	case <-time.After(2 * time.Second):
		t.Fatal("terminal must end when the exec stream ends")
	}
	if _, err := p.Write([]byte("x")); err == nil {
		t.Fatal("write after the stream ended must fail")
	}
	_ = p.Close()
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/labs/ -run 'TestRunScript|TestPTY' -count=1 2>&1 | tail -4`
Expected: FAIL with `r.RunScript undefined`.

- [ ] **Step 3: Implement**

Create `internal/labs/cluster_exec.go`:

```go
package labs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"

	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"

	ap "crucible/internal/agentproto"
)

var _ Runner = (*ClusterRunner)(nil)

// validService refuses names docker compose would parse as flags. Service names come from lab.yaml (validated
// against the compose file by the loader), never from the browser.
func validService(s string) bool { return s != "" && !strings.HasPrefix(s, "-") }

// RunScript runs a check or setup out of band (spec §8.5): the script travels on stdin and is never stored in the
// lab. Env values (e.g. the trainee's quiz answer) are single argv elements, never shell text.
func (c *ClusterRunner) RunScript(ctx context.Context, inst *Instance, s ScriptSpec) (ScriptResult, error) {
	if !validLabID(inst.ID) || !validService(s.Service) {
		return ScriptResult{}, errors.New("invalid lab id or service")
	}
	// `timeout` inside the pod kills the compose client if our stream dies first.
	// ponytail: like local labs, the process inside the service may linger until the lab is destroyed.
	secs := int(math.Ceil(s.Timeout.Seconds())) + 5
	cmd := []string{"timeout", "-s", "KILL", strconv.Itoa(secs), "docker", "compose", "exec", "-T"}
	for _, k := range slices.Sorted(maps.Keys(s.Env)) {
		cmd = append(cmd, "-e", k+"="+s.Env[k])
	}
	cmd = append(cmd, s.Service, "sh", "-s")
	sctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	out := &ap.Capped{}
	err := c.execFn()(sctx, labNamespace(inst.ID), labPod, cmd,
		remotecommand.StreamOptions{Stdin: bytes.NewReader(s.Script), Stdout: out, Stderr: out})
	var exit utilexec.ExitError
	switch {
	case ctx.Err() != nil:
		return ScriptResult{}, ctx.Err() // the caller gave up: not the script's fault
	case sctx.Err() != nil:
		return ScriptResult{ExitCode: -1, TimedOut: true, Output: string(out.Bytes()) + "\n[crucible] script timed out"}, nil
	case errors.As(err, &exit):
		return ScriptResult{ExitCode: exit.ExitStatus(), Output: string(out.Bytes())}, nil
	case err != nil:
		return ScriptResult{}, err
	}
	return ScriptResult{Output: string(out.Bytes())}, nil
}

// OpenPTY opens a login shell in a compose service over a TTY exec. The stream runs until Close or until the
// exec ends; a dead stream surfaces as a read error, which closes the trainee's terminal websocket.
func (c *ClusterRunner) OpenPTY(ctx context.Context, inst *Instance, service string, cols, rows int) (PTY, error) {
	if !validLabID(inst.ID) || !validService(service) {
		return nil, errors.New("invalid lab id or service")
	}
	ctx, cancel := context.WithCancel(ctx)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &execPTY{in: inW, out: outR, sizes: make(chan remotecommand.TerminalSize, 1), done: make(chan struct{}), cancel: cancel}
	p.sizes <- remotecommand.TerminalSize{Width: uint16(cols), Height: uint16(rows)}
	cmd := []string{"docker", "compose", "exec", service, "sh", "-c",
		"if command -v bash >/dev/null 2>&1; then exec bash -l; else exec sh -l; fi"}
	go func() {
		err := c.execFn()(ctx, labNamespace(inst.ID), labPod, cmd,
			remotecommand.StreamOptions{Stdin: inR, Stdout: outW, Tty: true, TerminalSizeQueue: p})
		if err == nil {
			err = io.EOF
		}
		outW.CloseWithError(err)
		inR.CloseWithError(err)
	}()
	return p, nil
}

type execPTY struct {
	in     *io.PipeWriter
	out    *io.PipeReader
	sizes  chan remotecommand.TerminalSize // holds at most the latest size the stream has not picked up
	done   chan struct{}
	once   sync.Once
	cancel context.CancelFunc
}

func (p *execPTY) Read(b []byte) (int, error)  { return p.out.Read(b) }
func (p *execPTY) Write(b []byte) (int, error) { return p.in.Write(b) }

// Resize keeps only the newest size; it never blocks (one caller: the websocket read loop).
func (p *execPTY) Resize(cols, rows int) error {
	select {
	case <-p.sizes:
	default:
	}
	select {
	case p.sizes <- remotecommand.TerminalSize{Width: uint16(cols), Height: uint16(rows)}:
	default:
	}
	return nil
}

// Next implements remotecommand.TerminalSizeQueue; nil ends the size stream.
func (p *execPTY) Next() *remotecommand.TerminalSize {
	select {
	case s := <-p.sizes:
		return &s
	case <-p.done:
		return nil
	}
}

func (p *execPTY) Close() error {
	p.once.Do(func() {
		close(p.done)
		p.cancel()
		_ = p.in.Close()
		_ = p.out.Close()
	})
	return nil
}
```

- [ ] **Step 4: Run the tests (race detector on)**

Run: `go test ./internal/labs/ -run 'TestRunScript|TestPTY|TestProvision|TestDestroy|TestClusterObjects|TestLabNetwork' -count=3 -race && go vet ./... && gofmt -l .`
Expected: PASS three times in a row. vet and gofmt print nothing.

- [ ] **Step 5: Commit**

```bash
git add internal/labs/cluster_exec.go internal/labs/cluster_exec_test.go
git commit -m "feat(labs): server-side checks, setups and terminals for cluster labs over exec

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Wire the cluster runtime into the API and sweep orphaned lab namespaces

**Files:**
- Modify: `internal/labs/service.go` (`Sweep` calls `reconcileCluster`; add `reconcileCluster`; update the ponytail comment in `Start`)
- Modify: `internal/labs/cluster_test.go` (append the sweep test)
- Modify: `cmd/crucible-api/main.go`

**Interfaces:**
- Consumes: `(*ClusterRunner).Live`, `.Destroy`, `NewClusterRunner` (Task 3).
- Produces:
  - Environment variables for Tasks 6, 8 and 9:
    - `CRUCIBLE_CLUSTER_LABS=1` enables the `cluster` runtime. The cluster config comes from `KUBECONFIG` if set, otherwise from in-cluster.
    - `CRUCIBLE_CLUSTER_PRIVILEGED=1` is for dev clusters only.
  - `(*Service).reconcileCluster(ctx)`

- [ ] **Step 1: Write the failing test**

Append to `internal/labs/cluster_test.go`:

```go
func TestSweepRemovesOrphanLabNamespaces(t *testing.T) {
	f := setup(t, true)
	ctx := context.Background()
	now := f.clk.Now()
	inst := func(module string, st State) *Instance {
		return &Instance{ID: newLabID(), UserID: f.u.ID, Team: "forge", Training: "forge-101", Module: module, SHA: "abc",
			Runtime: "cluster", State: st, CreatedAt: now, LastActivityAt: now, TTL: time.Hour, IdleTimeout: 30 * time.Minute, Tier: "auto"}
	}
	live, over := inst("03-cluster-heat", Ready), inst("02-first-lab", Destroyed)
	for _, in := range []*Instance{live, over} {
		if err := f.s.insert(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	stranger := newLabID() // a namespace with no lab row at all
	cs := fake.NewClientset(labNS(live.ID, false), labNS(over.ID, false), labNS(stranger, false))
	cr := &ClusterRunner{Client: cs}
	f.s.Runners["cluster"] = cr
	f.s.Sweep(ctx)
	ids, err := cr.Live(ctx)
	if err != nil || !slices.Equal(ids, []string{live.ID}) {
		t.Fatalf("only the running lab keeps its namespace: %v %v", ids, err)
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/labs/ -run TestSweepRemovesOrphanLabNamespaces -count=1 2>&1 | tail -4`
Expected: FAIL: `only the running lab keeps its namespace` (three ids).

- [ ] **Step 3: Implement the reconcile**

In `internal/labs/service.go`, add `"slices"` to the imports and add below `Sweep`:

```go
// reconcileCluster deletes lab namespaces whose lab is over or unknown: a destroy that failed, a lab ended while
// the API restarted mid-provision, or a leftover from a deleted database. Namespaces of labs that are provisioning,
// ready or being destroyed are never touched.
func (s *Service) reconcileCluster(ctx context.Context) {
	cr, ok := s.Runners["cluster"].(*ClusterRunner)
	if !ok {
		return
	}
	ids, err := cr.Live(ctx)
	if err != nil {
		s.Log.Warn("listing lab namespaces failed", "err", err)
		return
	}
	if len(ids) == 0 {
		return
	}
	rows, err := s.DB.Query(ctx, `SELECT id FROM lab_instances WHERE id = ANY($1) AND state IN ('provisioning', 'ready', 'destroying')`, ids)
	if err != nil {
		s.Log.Error("lab namespace reconcile query failed", "err", err)
		return
	}
	active, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		s.Log.Error("lab namespace reconcile query failed", "err", err)
		return
	}
	for _, id := range ids {
		if slices.Contains(active, id) {
			continue
		}
		if err := cr.Destroy(ctx, &Instance{ID: id, Runtime: "cluster"}); err != nil {
			s.Log.Warn("removing an orphaned lab namespace failed", "lab", id, "err", err)
			continue
		}
		s.Log.Info("removed an orphaned lab namespace", "lab", id)
	}
}
```

At the end of `Sweep`, after the `for _, inst := range due { … }` loop, add:

```go
	s.reconcileCluster(ctx)
```

In `Start`, replace the comment on the `go s.provision(...)` line with:

```go
		// ponytail: a goroutine, not a River job: creates are idempotent, the sweep fails provisioning stuck > 15 min
		// and removes orphaned lab namespaces (M4 ruling 5). Make it a job if API restarts mid-provision become common.
		go s.provision(context.WithoutCancel(ctx), inst, m.Lab)
```

- [ ] **Step 4: Wire the API**

In `cmd/crucible-api/main.go`, add the import `"k8s.io/client-go/tools/clientcmd"`. Replace the `labSvc := &labs.Service{…}` construction with:

```go
	runners := map[string]labs.Runner{"local": labs.LocalRunner{Hub: hub}}
	estimators := map[string]labs.Estimator{"local": rates}
	if os.Getenv("CRUCIBLE_CLUSTER_LABS") == "1" {
		// KUBECONFIG when set (dev), else the pod's service account (in-cluster).
		cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(),
			&clientcmd.ConfigOverrides{}).ClientConfig()
		if err != nil {
			return fmt.Errorf("cluster labs: %w", err)
		}
		privileged := os.Getenv("CRUCIBLE_CLUSTER_PRIVILEGED") == "1"
		if privileged {
			slog.Warn("CRUCIBLE_CLUSTER_PRIVILEGED=1: cluster labs run as privileged pods without sysbox. Use this on development clusters only")
		}
		cr, err := labs.NewClusterRunner(cfg, privileged)
		if err != nil {
			return fmt.Errorf("cluster labs: %w", err)
		}
		runners["cluster"], estimators["cluster"] = cr, rates // M4 ruling 6: priced like local labs until M6
		slog.Info("cluster labs enabled", "api", cfg.Host, "privileged", privileged)
	}
	labSvc := &labs.Service{Notify: notifySvc, DB: pool, Learn: learnSvc, Runners: runners, Estimators: estimators,
		Now: time.Now, Log: slog.Default()}
```

- [ ] **Step 5: Run the tests and build**

Run:
```bash
go test ./internal/labs/ -count=1 -race
go build ./... && go vet ./... && gofmt -l .
```
Expected: PASS, and no output from the rest.

- [ ] **Step 6: Commit**

```bash
git add internal/labs/service.go internal/labs/cluster_test.go cmd/crucible-api/main.go go.mod go.sum
git commit -m "feat(labs): enable the cluster runtime in crucible-api; sweep orphaned lab namespaces

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Helm: service account, least-privilege ClusterRole, admission policy that confines it to lab namespaces

**Files:**
- Modify: `deploy/helm/crucible/values.yaml`
- Create: `deploy/helm/crucible/templates/rbac.yaml`
- Modify: `deploy/helm/crucible/templates/crucible.yaml` (pod spec + env)
- Modify: `deploy/helm/test.sh`

**Interfaces:**
- Consumes: `CRUCIBLE_CLUSTER_LABS` / `CRUCIBLE_CLUSTER_PRIVILEGED` (Task 5); namespace label `crucible.io/lab`, namespace name `lab-<id>`, PSA label (Task 2).
- Produces: ServiceAccount `crucible` in the release namespace; ClusterRole/Binding `crucible-labs`; ValidatingAdmissionPolicy/Binding `crucible-labs-only`; values `clusterLabs.enabled` (default `true`) and `clusterLabs.unsafePrivileged` (default `false`). Task 8 renders `--show-only templates/rbac.yaml` into kind.

- [ ] **Step 1: Write the failing chart assertions**

In `deploy/helm/test.sh`, insert before `if grep -q 'hostNetwork: true'`:

```bash
# Cluster labs (M4): least-privilege RBAC, confined by an admission policy to lab namespaces.
need 'serviceAccountName: crucible'
need 'kind: ClusterRole'
need 'pods/exec'
need 'kind: ValidatingAdmissionPolicy'
need "system:serviceaccount:default:crucible"
need 'pod-security.kubernetes.io/enforce'
need 'name: CRUCIBLE_CLUSTER_LABS'
if grep -q CRUCIBLE_CLUSTER_PRIVILEGED <<<"$out"; then echo "privileged lab pods must be opt-in"; exit 1; fi
rbac=$(helm template t "$chart" --set backup.bucket=b --set backup.region=eu-west-1 --set oidc.issuer=https://sso --show-only templates/rbac.yaml)
if grep -qE 'secrets|"\*"|- \*$|\[\*\]' <<<"$rbac"; then echo "crucible-labs must not touch secrets or use wildcards"; exit 1; fi
if grep -qE 'rolebindings|clusterroles|escalate|bind|impersonate' <<<"$rbac"; then echo "crucible-labs must not manage RBAC"; exit 1; fi
dev=$(helm template t "$chart" --set backup.bucket=b --set backup.region=eu-west-1 --set oidc.issuer=https://sso --set clusterLabs.unsafePrivileged=true)
grep -q 'name: CRUCIBLE_CLUSTER_PRIVILEGED' <<<"$dev" || { echo "missing: dev privileged env"; exit 1; }
if grep -q 'pod-security.kubernetes.io/enforce' <<<"$dev"; then echo "dev policy must allow privileged lab pods"; exit 1; fi
off=$(helm template t "$chart" --set backup.bucket=b --set backup.region=eu-west-1 --set oidc.issuer=https://sso --set clusterLabs.enabled=false)
if grep -qE 'kind: ClusterRole|CRUCIBLE_CLUSTER_LABS' <<<"$off"; then echo "clusterLabs.enabled=false must not grant cluster access"; exit 1; fi
grep -q 'automountServiceAccountToken: false' <<<"$off" || { echo "no API token without cluster labs"; exit 1; }
```

Run: `bash deploy/helm/test.sh`
Expected: FAIL with `missing: serviceAccountName: crucible`.

- [ ] **Step 2: Values**

Append to `deploy/helm/crucible/values.yaml`:

```yaml
clusterLabs:
  enabled: true            # runtime: cluster labs; nodes need the sysbox-runc RuntimeClass (docs/runbooks/aws.md)
  unsafePrivileged: false  # dev clusters without sysbox only (kind): privileged lab pods. Never in production
```

- [ ] **Step 3: RBAC and admission policy**

Create `deploy/helm/crucible/templates/rbac.yaml`:

```yaml
apiVersion: v1
kind: ServiceAccount
metadata: { name: crucible }
automountServiceAccountToken: false   # the Deployment opts in only when cluster labs are on
{{- if .Values.clusterLabs.enabled }}
---
# What the cluster runner does (internal/labs/cluster.go). RBAC cannot be scoped by namespace label, so the
# ValidatingAdmissionPolicy below confines every write to lab-<id> namespaces.
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: { name: crucible-labs }
rules:
  - apiGroups: [""]
    resources: [namespaces]
    verbs: [get, list, create, delete]
  - apiGroups: [""]
    resources: [pods]
    verbs: [get, list, create]
  - apiGroups: [""]
    resources: [pods/exec]
    verbs: [get, create]          # WebSocket exec authorizes get (and create on newer API servers); SPDY uses create
  - apiGroups: [""]
    resources: [resourcequotas, limitranges]
    verbs: [create]
  - apiGroups: [networking.k8s.io]
    resources: [networkpolicies]
    verbs: [create]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: { name: crucible-labs }
roleRef: { apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: crucible-labs }
subjects:
  - { kind: ServiceAccount, name: crucible, namespace: {{ .Release.Namespace }} }
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata: { name: crucible-labs-only }
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
      - { apiGroups: [""], apiVersions: [v1], operations: [CREATE, DELETE], resources: [namespaces] }
      - { apiGroups: [""], apiVersions: [v1], operations: [CREATE], resources: [pods, resourcequotas, limitranges] }
      - { apiGroups: [""], apiVersions: [v1], operations: [CONNECT], resources: [pods/exec] }
      - { apiGroups: [networking.k8s.io], apiVersions: [v1], operations: [CREATE], resources: [networkpolicies] }
  matchConditions:
    - name: crucible-api-only
      expression: "request.userInfo.username == 'system:serviceaccount:{{ .Release.Namespace }}:crucible'"
  variables:
    - name: target
      expression: "request.operation == 'CREATE' ? object : oldObject"
  validations:
    - expression: >-
        request.resource.resource != 'namespaces' ||
        (variables.target.metadata.name.startsWith('lab-') && has(variables.target.metadata.labels) &&
         'crucible.io/lab' in variables.target.metadata.labels)
      message: crucible-api may only create and delete lab-<id> namespaces
    - expression: >-
        request.resource.resource == 'namespaces' ||
        (namespaceObject != null && has(namespaceObject.metadata.labels) && 'crucible.io/lab' in namespaceObject.metadata.labels)
      message: crucible-api may only create lab pods, policies and terminals inside lab namespaces
    {{- if not .Values.clusterLabs.unsafePrivileged }}
    - expression: >-
        request.resource.resource != 'namespaces' || request.operation != 'CREATE' ||
        (has(object.metadata.labels) && 'pod-security.kubernetes.io/enforce' in object.metadata.labels &&
         object.metadata.labels['pod-security.kubernetes.io/enforce'] == 'baseline')
      message: lab namespaces must enforce Pod Security "baseline" (no privileged lab pods)
    {{- end }}
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata: { name: crucible-labs-only }
spec:
  policyName: crucible-labs-only
  validationActions: [Deny]
{{- end }}
```

- [ ] **Step 4: Use the account in the Deployment**

In `deploy/helm/crucible/templates/crucible.yaml`, under the pod `spec:` (directly above `initContainers:`), add:

```yaml
      serviceAccountName: crucible
      automountServiceAccountToken: {{ .Values.clusterLabs.enabled }}
```

At the end of the `api` container's `env:` list (after the `CRUCIBLE_SMTP_PASSWORD` entry), add:

```yaml
            {{- if .Values.clusterLabs.enabled }}
            - { name: CRUCIBLE_CLUSTER_LABS, value: "1" }
            {{- if .Values.clusterLabs.unsafePrivileged }}
            - { name: CRUCIBLE_CLUSTER_PRIVILEGED, value: "1" }
            {{- end }}
            {{- end }}
```

- [ ] **Step 5: Run the chart test**

Run: `bash deploy/helm/test.sh`
Expected: `helm chart OK`. The CEL expressions are only parsed by a real API server, and Task 8 applies this file to kind. If a grep in Step 1 trips on a word inside a comment, reword the comment. Don't loosen the check.

- [ ] **Step 6: Commit**

```bash
git add deploy/helm
git commit -m "feat(helm): crucible service account with least-privilege lab RBAC confined by an admission policy

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: The AWS node runs sysbox (offline-tested; verified on the node by a human)

Spec §9.4: "Cloud-init installs k3s, sysbox and the Helm release." Nothing here may run against AWS. `terraform test` uses the existing mock providers.

**Files:**
- Modify: `deploy/aws/main/variables.tf` (add `sysbox_version`)
- Modify: `deploy/aws/main/main.tf` (`templatefile` map: `sysbox_version = var.sysbox_version`)
- Modify: `deploy/aws/main/bootstrap.sh.tftpl`
- Modify: `deploy/aws/main/main.tftest.hcl`
- Modify: `docs/runbooks/aws.md`

**Interfaces:**
- Produces: RuntimeClass `sysbox-runc` (handler `sysbox-runc`) on the k3s node, which Task 2's pods reference.

- [ ] **Step 1: Write the failing terraform test**

Append to `deploy/aws/main/main.tftest.hcl`:

```hcl
run "cluster_labs_use_sysbox" {
  command = plan

  assert {
    condition     = strcontains(aws_instance.node.user_data, "sysbox-ce_0.6.7") && strcontains(aws_instance.node.user_data, "config-v3.toml.tmpl")
    error_message = "the node installs sysbox and registers it with k3s's containerd"
  }
  assert {
    condition     = strcontains(aws_instance.node.user_data, "kind: RuntimeClass") && strcontains(aws_instance.node.user_data, "handler: sysbox-runc")
    error_message = "the sysbox-runc RuntimeClass is auto-deployed by k3s"
  }
  assert {
    condition     = !strcontains(aws_instance.node.user_data, "unsafePrivileged") && !strcontains(aws_instance.node.user_data, "CRUCIBLE_CLUSTER_PRIVILEGED")
    error_message = "production never runs privileged lab pods"
  }
}
```

Run: `terraform -chdir=deploy/aws/main init -backend=false >/dev/null && terraform -chdir=deploy/aws/main test`
Expected: FAIL in `cluster_labs_use_sysbox`. Mock providers mean no AWS call and no credentials are needed.

- [ ] **Step 2: Variable and template input**

`deploy/aws/main/variables.tf`, after `k3s_version`:

```hcl
variable "sysbox_version" {
  type        = string
  default     = "0.6.7"
  description = "Sysbox CE release for cluster labs (needs containerd >= 2.0.5, i.e. k3s >= 1.32)"
}
```

`deploy/aws/main/main.tf`: in the `templatefile(...)` map, add `sysbox_version = var.sysbox_version`.

- [ ] **Step 3: Install sysbox before k3s starts**

In `deploy/aws/main/bootstrap.sh.tftpl`, insert directly before `retry curl -sfL https://get.k3s.io -o /tmp/k3s-install.sh`:

```bash
# Sysbox: cluster labs run dockerd in a sysbox pod, never a privileged pod (spec §8.2, §14).
# k3s reads config-v3.toml.tmpl (containerd 2.x) at start; the base template keeps every k3s default.
retry curl -fsSL "https://downloads.nestybox.com/sysbox/releases/v${sysbox_version}/sysbox-ce_${sysbox_version}-0.linux_amd64.deb" -o /tmp/sysbox.deb
apt-get -o DPkg::Lock::Timeout=600 install -y /tmp/sysbox.deb
systemctl enable --now sysbox
mkdir -p /var/lib/rancher/k3s/agent/etc/containerd
cat > /var/lib/rancher/k3s/agent/etc/containerd/config-v3.toml.tmpl <<'TOML'
{{ template "base" . }}

[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.'sysbox-runc']
  runtime_type = "io.containerd.runc.v2"

[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.'sysbox-runc'.options]
  BinaryName = "/usr/bin/sysbox-runc"
  SystemdCgroup = {{ .SystemdCgroup }}
TOML
cat > /var/lib/rancher/k3s/server/manifests/sysbox-runtimeclass.yaml <<'YAML'
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: sysbox-runc
handler: sysbox-runc
YAML
```

Before you commit, compare the `.deb` URL with the Sysbox CE release page (`github.com/nestybox/sysbox/releases` → "sysbox-ce_<ver>-0.linux_amd64.deb"). Also compare the containerd plugin path and `.SystemdCgroup` with the k3s docs ("Advanced → Configuring containerd") for k3s v1.34. If either differs, fix it here and in the test strings. This is a documentation check only. Do not run anything against AWS.

- [ ] **Step 4: Runbook**

Add to `docs/runbooks/aws.md` before `## Troubleshooting`:

```markdown
## Cluster labs (sysbox)

Cloud-init installs Sysbox CE and registers the `sysbox-runc` runtime with k3s's containerd. It also creates the `sysbox-runc` RuntimeClass, and the chart turns on `clusterLabs.enabled`. Lab pods run in `lab-<id>` namespaces with Pod Security `baseline`, a quota, and a NetworkPolicy that blocks `169.254.169.254` (IMDS) and every private range.

A node built before M4 has no sysbox, because `user_data` changes never replace the node. Rebuild it with `crucible aws teardown` and then `crucible aws up` (data is restored from the latest snapshot).

Verify after `up` (over SSM, as root):
1. `systemctl is-active sysbox` prints `active`, and `k3s kubectl get runtimeclass sysbox-runc` lists it.
2. Start Forge 101's "Into the Crucible" lab in the browser. Then `k3s kubectl get pods -A -l crucible.io/lab` shows one `lab` pod `Running`, and `k3s kubectl -n lab-<id> get pod lab -o jsonpath='{.spec.runtimeClassName}'` prints `sysbox-runc`.
3. IMDS is blocked: `k3s kubectl -n lab-<id> exec lab -c dind -- wget -T 3 -qO- http://169.254.169.254/latest/meta-data/` fails with a timeout.
4. End the lab. The namespace disappears within a minute (`k3s kubectl get ns -l crucible.io/lab`).

**Lab stuck in "provisioning" or failing with "could not be started":** run `k3s kubectl -n lab-<id> describe pod lab`. `no runtime for "sysbox-runc"` means containerd did not load the template: check `/var/lib/rancher/k3s/agent/etc/containerd/config.toml` for the `sysbox-runc` block, then run `systemctl restart k3s`.
```

- [ ] **Step 5: Run the offline checks**

Run: `terraform -chdir=deploy/aws/main fmt -check && terraform -chdir=deploy/aws/main validate && terraform -chdir=deploy/aws/main test`
Expected: all `run` blocks pass, including `cluster_labs_use_sysbox`.

- [ ] **Step 6: Commit**

```bash
git add deploy/aws/main docs/runbooks/aws.md
git commit -m "feat(aws): install sysbox on the k3s node and register the sysbox-runc RuntimeClass

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: `make cluster-check`: the runner against real kind, with the chart's RBAC and admission policy

**Files:**
- Create: `internal/labs/cluster_integration_test.go` (`//go:build cluster`)
- Create: `scripts/cluster-check.sh`
- Modify: `Makefile`

**Interfaces:**
- Consumes: everything from Tasks 1–6. Env for the test, set by the script:
  - `CRUCIBLE_TEST_KUBECONFIG` (kubeconfig with the `crucible` ServiceAccount token, so RBAC and admission are real)
  - `CRUCIBLE_TEST_PROBE_IP` (IP of an nginx pod in namespace `crucible-probe`)
  - `CRUCIBLE_CLUSTER_PRIVILEGED=1`
- Produces:
  - kind cluster `crucible-m4`
  - `.local/kind/kubeconfig` (host) and `.local/kind/kubeconfig-internal` (server `https://crucible-m4-control-plane:6443`, for containers on the `kind` docker network), both authenticating as the `crucible` ServiceAccount. Task 9 mounts the internal one.
  - The script runs `CLUSTER=1 ./scripts/local-check.sh` as its last step. Task 9 adds that switch, so until Task 9 lands, the script stops after the integration test (Step 3 says how).

- [ ] **Step 1: Get a kind with NetworkPolicy support**

kind on PATH is v0.23.0, and kindnet enforces NetworkPolicy only from v0.24. Download the latest release into the git-ignored tools dir. Nothing goes system-wide.

```bash
v=$(curl -fsSL https://api.github.com/repos/kubernetes-sigs/kind/releases/latest | sed -n 's/.*"tag_name": "\(v[^"]*\)".*/\1/p')
curl -fsSLo .local/tools/kind "https://kind.sigs.k8s.io/dl/$v/kind-darwin-arm64" && chmod +x .local/tools/kind
kind version
```
Expected: `kind v0.2[4-9]…` or newer, found first on PATH (`.local/tools` comes first).

- [ ] **Step 2: Write the integration test**

Create `internal/labs/cluster_integration_test.go`:

```go
//go:build cluster

package labs

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// TestClusterLabOnKind runs Forge 101's cluster lab on a real cluster as the crucible service account
// (scripts/cluster-check.sh sets the environment). Never runs in `go test ./...` (build tag cluster).
func TestClusterLabOnKind(t *testing.T) {
	kc := os.Getenv("CRUCIBLE_TEST_KUBECONFIG")
	if kc == "" {
		t.Skip("run through make cluster-check")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kc)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewClusterRunner(cfg, os.Getenv("CRUCIBLE_CLUSTER_PRIVILEGED") == "1")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	dir := "../../examples/forge-101/modules/03-cluster-heat/lab"
	bundle, err := Bundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	inst := &Instance{ID: newLabID(), Runtime: "cluster"}
	t.Cleanup(func() { _ = r.Destroy(context.Background(), inst) })
	if err := r.Provision(ctx, inst, bundle, "compose.yaml"); err != nil {
		t.Fatalf("provision: %v", err)
	}
	script := func(svc, body string, env map[string]string, timeout time.Duration) ScriptResult {
		t.Helper()
		res, err := r.RunScript(ctx, inst, ScriptSpec{Service: svc, Script: []byte(body), Env: env, Timeout: timeout})
		if err != nil {
			t.Fatalf("run %q: %v", body, err)
		}
		return res
	}
	check, _ := os.ReadFile(filepath.Join(dir, "checks/01-cast.sh"))

	t.Run("check fails before the trainee acts", func(t *testing.T) {
		if res := script("shell", string(check), nil, 30*time.Second); res.ExitCode == 0 {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("terminal over exec, then the server-side check passes", func(t *testing.T) {
		p, err := r.OpenPTY(ctx, inst, "shell", 100, 30)
		if err != nil {
			t.Fatal(err)
		}
		var mu sync.Mutex
		var seen bytes.Buffer
		go func() {
			buf := make([]byte, 4096)
			for {
				n, err := p.Read(buf)
				mu.Lock()
				seen.Write(buf[:n])
				mu.Unlock()
				if err != nil {
					return
				}
			}
		}()
		_, _ = p.Write([]byte("echo 'hello crucible' > /tmp/cast.txt; echo DONE-$((40+2))\n"))
		deadline := time.Now().Add(30 * time.Second)
		for {
			mu.Lock()
			ok := strings.Contains(seen.String(), "DONE-42") // the echoed command line shows $((40+2)), not 42
			mu.Unlock()
			if ok {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("no shell output: %q", seen.String())
			}
			time.Sleep(200 * time.Millisecond)
		}
		_ = p.Close()
		if res := script("shell", string(check), nil, 30*time.Second); res.ExitCode != 0 || !strings.Contains(res.Output, "Cast in the crucible.") {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("answers are data, never shell", func(t *testing.T) {
		evil := `$(touch /tmp/pwned); ' " ; rm -rf /`
		res := script("shell", `printf '%s' "$CRUCIBLE_ANSWER"; test ! -e /tmp/pwned`, map[string]string{"CRUCIBLE_ANSWER": evil}, 30*time.Second)
		if res.ExitCode != 0 || res.Output != evil {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("a hanging check times out", func(t *testing.T) {
		start := time.Now()
		res := script("shell", "sleep 120", nil, 2*time.Second)
		if !res.TimedOut || time.Since(start) > 20*time.Second {
			t.Fatalf("%+v after %v", res, time.Since(start))
		}
	})
	t.Run("egress", func(t *testing.T) {
		probe := os.Getenv("CRUCIBLE_TEST_PROBE_IP")
		for _, target := range []string{"http://" + probe + "/", "http://169.254.169.254/latest/meta-data/"} {
			if res := script("shell", "wget -q -T 3 -O /dev/null "+target+" && echo REACHED", nil, 30*time.Second); res.ExitCode == 0 {
				t.Errorf("%s must be unreachable from a lab: %+v", target, res)
			}
		}
		if res := script("shell", "nslookup kubernetes.default.svc.cluster.local", nil, 30*time.Second); res.ExitCode != 0 {
			t.Errorf("DNS must work (image pulls need it): %+v", res)
		}
	})
	t.Run("dockerd is not on the network", func(t *testing.T) {
		var out bytes.Buffer
		if err := r.run(ctx, labNamespace(inst.ID), []string{"netstat", "-ltn"}, nil, &out); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), ":2375") || strings.Contains(out.String(), ":2376") {
			t.Fatalf("docker API listening on TCP:\n%s", out.String())
		}
	})
	t.Run("the service account is confined to lab namespaces", func(t *testing.T) {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "escape"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "x", Image: "busybox"}}}}
		if _, err := r.Client.CoreV1().Pods("default").Create(ctx, pod, metav1.CreateOptions{}); err == nil {
			_ = r.Client.CoreV1().Pods("default").Delete(ctx, "escape", metav1.DeleteOptions{})
			t.Error("creating a pod outside a lab namespace must be refused")
		}
		if err := r.Client.CoreV1().Namespaces().Delete(ctx, "crucible-probe", metav1.DeleteOptions{}); err == nil {
			t.Error("deleting a non-lab namespace must be refused")
		}
	})
	t.Run("destroy", func(t *testing.T) {
		if err := r.Destroy(ctx, inst); err != nil {
			t.Fatal(err)
		}
		if ids, err := r.Live(ctx); err != nil || slices.Contains(ids, inst.ID) {
			t.Fatalf("destroyed lab still live: %v %v", ids, err)
		}
		if err := r.Destroy(ctx, inst); err != nil {
			t.Fatalf("second destroy: %v", err)
		}
	})
}
```

Check that the default suite does not compile it: `go vet ./... && go test ./internal/labs/ -run TestClusterLabOnKind -count=1` reports `ok` with no tests run. Check that it compiles with the tag: `go vet -tags cluster ./internal/labs/`.

- [ ] **Step 3: The script**

Create `scripts/cluster-check.sh` (then `chmod +x`):

```bash
#!/usr/bin/env bash
# M4 acceptance: Forge 101's cluster lab on a local kind cluster standing in for k3s. Docker Desktop cannot run
# sysbox, so lab pods are privileged dind here (CRUCIBLE_CLUSTER_PRIVILEGED=1, dev only). Everything else is real:
# namespaces, quotas, NetworkPolicy (kindnet), exec terminals, and the chart's RBAC + admission policy, because
# both the integration test and crucible-api authenticate as the crucible service account.
# KEEP=1 leaves kind (and the compose stack) running. Never touches AWS.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
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
kind get clusters 2>/dev/null | grep -qx "$name" || kind create cluster --name "$name" --wait 120s

echo "== crucible service account, RBAC and admission policy from the chart"
kubectl --context "$ctx" create namespace crucible --dry-run=client -o yaml | kubectl --context "$ctx" apply -f -
helm template crucible deploy/helm/crucible -n crucible --set clusterLabs.unsafePrivileged=true \
  --set backup.bucket=unused --set backup.region=unused --set oidc.issuer=https://unused --show-only templates/rbac.yaml \
  | kubectl --context "$ctx" -n crucible apply -f -

echo "== probe pod (a neighbour the lab must not reach)"
kubectl --context "$ctx" create namespace crucible-probe --dry-run=client -o yaml | kubectl --context "$ctx" apply -f -
kubectl --context "$ctx" -n crucible-probe get pod probe >/dev/null 2>&1 \
  || kubectl --context "$ctx" -n crucible-probe run probe --image=nginx:1.29-alpine --port=80
kubectl --context "$ctx" -n crucible-probe wait --for=condition=Ready pod/probe --timeout=180s
probe_ip=$(kubectl --context "$ctx" -n crucible-probe get pod probe -o jsonpath='{.status.podIP}')

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
kubectl --kubeconfig .local/kind/kubeconfig auth whoami | grep -q 'system:serviceaccount:crucible:crucible' \
  || { echo "kubeconfig does not authenticate as the crucible service account"; exit 1; }

echo "== cluster runner against kind"
CRUCIBLE_TEST_KUBECONFIG="$root/.local/kind/kubeconfig" CRUCIBLE_TEST_PROBE_IP="$probe_ip" CRUCIBLE_CLUSTER_PRIVILEGED=1 \
  go test -tags cluster -run TestClusterLabOnKind -count=1 -timeout 20m -v ./internal/labs/

echo "== browser: Forge 101 end to end, including the cluster lab"
CLUSTER=1 ./scripts/local-check.sh

echo "🔥 Cluster check passed. The crucible holds."
```

Until Task 9 lands, comment out the two lines under `== browser` to run this task's check, and restore them in Task 9.

`Makefile`: add `cluster-check` to `.PHONY` and add:

```make
cluster-check:
	./scripts/cluster-check.sh
```

- [ ] **Step 4: Run it**

Run: `KEYCLOAK_PORT=8082 make cluster-check 2>&1 | tail -30` (browser step commented out)
Expected: every `TestClusterLabOnKind/*` subtest shows `--- PASS`, with `ok crucible/internal/labs`. The first run pulls `docker:dind`, `alpine` and `nginx` and takes a few minutes.

Troubleshooting, in order:
- **Provision fails with `forbidden`.** The ClusterRole or the policy is wrong. Run `kubectl --context kind-crucible-m4 get validatingadmissionpolicy crucible-labs-only -o yaml` and read `status.typeChecking`. Fix `rbac.yaml` and re-run `bash deploy/helm/test.sh`.
- **dockerd never becomes ready.** Run `kubectl --context kind-crucible-m4 -n lab-<id> logs lab -c dind`. Overlay errors mean the emptyDir is not on the kind node's `/var` volume. Report them, and don't switch storage drivers silently.
- **The egress subtest reaches the probe.** Check kindnet's version: `kubectl -n kube-system get ds kindnet -o yaml | grep image`. A kind older than v0.24 does not enforce policies.
- **`nslookup` fails.** The DNS rule's labels don't match: `kubectl -n kube-system get pods --show-labels | grep dns`.

- [ ] **Step 5: Commit**

```bash
git add internal/labs/cluster_integration_test.go scripts/cluster-check.sh Makefile
git commit -m "test(labs): cluster-check runs the cluster runner on kind as the crucible service account

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: Browser e2e: a trainee runs Forge 101's cluster lab, and its checks are not self-reported

**Files:**
- Create: `deploy/compose/cluster.yml`
- Modify: `scripts/local-check.sh`
- Modify: `e2e/playwright.config.ts`
- Create: `e2e/tests/cluster-lab.spec.ts`
- Modify: `scripts/cluster-check.sh` (restore the browser step)

**Interfaces:**
- Consumes: `.local/kind/kubeconfig-internal` and the kind docker network `kind` (Task 8); module `03-cluster-heat` strings (Task 1); `GET /api/programs/{team}/{training}/modules/{module}/lab` → `{ lab: { id, self_reported, … } }` (existing).
- Produces: Playwright projects `local` (everything except `cluster-lab.spec.ts`) and `cluster` (only `cluster-lab.spec.ts`, depends on `local`, which completes modules 01–02 first).

- [ ] **Step 1: Playwright projects**

`e2e/playwright.config.ts`: add to the exported config object:

```ts
  projects: [
    { name: 'local', testIgnore: /cluster-lab\.spec\.ts/ },
    // Needs make cluster-check (kind). Runs after `local`, which completes modules 01–02 of the linear training.
    { name: 'cluster', testMatch: /cluster-lab\.spec\.ts/, dependencies: ['local'] },
  ],
```

- [ ] **Step 2: The cluster spec**

Create `e2e/tests/cluster-lab.spec.ts`:

```ts
import { expect, test, type Page } from '@playwright/test'

async function typeIn(page: Page, tab: string, command: string) {
  await page.getByRole('tab', { name: tab, exact: true }).click()
  await page.locator(`[data-terminal="${tab}"]`).click()
  await page.keyboard.type(command)
  await page.keyboard.press('Enter')
}

const labAPI = '/api/programs/forge/forge-101/modules/03-cluster-heat/lab'

test('Forge 101 cluster lab: terminals over exec, checks run by the server', async ({ page }) => {
  page.on('dialog', (d) => d.accept())
  await page.goto('/')
  await page.locator('#username').fill('trainee')
  await page.locator('#password').fill('trainee')
  await page.locator('#kc-login').click()
  await expect(page.getByRole('heading', { name: 'Hearth' })).toBeVisible()

  // Module 03 is unlocked because the `local` project finished module 02. No laptop agent is running.
  await page.getByRole('link', { name: /Forge 101/ }).click()
  await expect(page.getByTestId('module-03-cluster-heat')).toHaveAttribute('data-locked', 'false')
  await page.getByTestId('module-03-cluster-heat').getByRole('link', { name: 'Lab', exact: true }).click()
  await page.getByRole('button', { name: 'Ignite the forge' }).click()
  // First run: kind pulls docker:dind, then dockerd pulls alpine + nginx inside the lab pod.
  await expect(page.getByRole('tab', { name: 'shell', exact: true })).toBeVisible({ timeout: 8 * 60_000 })

  // Task 1: the terminal is a Kubernetes exec stream; the check runs server-side.
  await typeIn(page, 'shell', "echo 'hello crucible' > /tmp/cast.txt")
  await page.getByRole('button', { name: 'Check' }).click()
  await expect(page.getByText('Cast in the crucible.')).toBeVisible()

  // Task 2: the setup (exec, out of band) breaks nginx; fix it in the web terminal.
  await expect(page.getByText('Someone moved nginx')).toBeVisible({ timeout: 90_000 })
  await typeIn(page, 'web', "sed -i 's/8081;/80;/g' /etc/nginx/conf.d/default.conf && nginx -s reload")
  await page.getByRole('button', { name: 'Check' }).click()
  await expect(page.getByText('The crucible burns bright on port 80.')).toBeVisible()
  await expect(page.getByText(/Lab forged/)).toBeVisible()

  // Done-when: checks are not self-reported.
  const info = await (await page.request.get(labAPI)).json()
  expect(info.runtime).toBe('cluster')
  expect(info.lab.self_reported).toBe(false)

  await page.getByRole('button', { name: 'End lab' }).click()
  await expect(page.getByRole('heading', { name: 'The forge has cooled' })).toBeVisible({ timeout: 60_000 })
})
```

- [ ] **Step 3: Compose override and the `CLUSTER=1` switch**

Create `deploy/compose/cluster.yml`:

```yaml
# make cluster-check only: crucible-api joins kind's docker network and runs cluster labs there as the crucible
# service account (token kubeconfig written by scripts/cluster-check.sh). Privileged dind: kind has no sysbox.
services:
  api:
    environment:
      CRUCIBLE_CLUSTER_LABS: "1"
      CRUCIBLE_CLUSTER_PRIVILEGED: "1"
      KUBECONFIG: /kube/config
    volumes: ["../../.local/kind/kubeconfig-internal:/kube/config:ro"]
    networks: [default, kind]
networks:
  kind: { external: true }
```

In `scripts/local-check.sh`:
- Replace `compose="docker compose -f deploy/compose/docker-compose.yml"` with:

```bash
compose="docker compose -f deploy/compose/docker-compose.yml"
project=local
if [ "${CLUSTER:-0}" = 1 ]; then # scripts/cluster-check.sh: also run Forge 101's cluster lab on kind
  compose="$compose -f deploy/compose/cluster.yml"
  project=cluster
fi
```

- Change `npx playwright test)` at the end of the e2e block to `npx playwright test --project="$project")`.

In `scripts/cluster-check.sh`, restore the two lines under `== browser`.

- [ ] **Step 4: Run both acceptance checks**

Run:
```bash
KEYCLOAK_PORT=8082 make local-check 2>&1 | tail -6
KEYCLOAK_PORT=8082 make cluster-check 2>&1 | tail -12
```
Expected: the first ends with `🔥 Local check passed. The forge holds.` (the `local` project only, no kind involved). The second shows the kind subtests passing, Playwright reporting the `local` tests plus `cluster-lab.spec.ts` passed, `🔥 Local check passed…`, and finally `🔥 Cluster check passed. The crucible holds.`

If the cluster spec fails, rerun with `KEEP=1 KEYCLOAK_PORT=8082 make cluster-check`. Then inspect `docker compose -f deploy/compose/docker-compose.yml -f deploy/compose/cluster.yml logs api` and `kubectl --context kind-crucible-m4 get ns -l crucible.io/lab`, plus the Playwright trace in `e2e/test-results/`. `cluster labs: … no such host` in the api log means the api container is not on the `kind` network. `forbidden` means RBAC: the api uses the same SA token as the integration test.

- [ ] **Step 5: Final hygiene**

Run: `gofmt -l . ; go vet ./... ; go vet -tags cluster ./internal/labs/ ; go test -race ./... 2>&1 | grep -v -E '^(ok|\?)' ; bash deploy/helm/test.sh`
Expected: no output except `helm chart OK`.

- [ ] **Step 6: Commit**

```bash
git add deploy/compose/cluster.yml scripts/local-check.sh scripts/cluster-check.sh e2e/playwright.config.ts e2e/tests/cluster-lab.spec.ts
git commit -m "test(e2e): a trainee runs Forge 101's cluster lab on kind; checks are not self-reported

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Spec coverage (M4)

| Spec item | Task |
|---|---|
| §2 lab runtime `cluster` (sysbox pod) | 2, 3, 7 |
| §3 runner interface: `ClusterRunner` (Available/Provision/OpenPTY/RunScript/Destroy) | 3, 4 |
| §8.2 namespace `lab-<id>`, ResourceQuota, default-deny NetworkPolicy | 2, 3 |
| §8.2 one sysbox pod runs dockerd + `docker compose up` | 2, 3, 7 |
| §8.2 terminals = exec into named services, proxied over WebSocket | 4 (existing `/terminals/{name}/ws` handler unchanged), 8, 9 |
| §8.2 checks via exec in `run_in` service, tamper-resistant; not self-reported | 1, 4, 8, 9 |
| §8.5 setup runs out of band (exec), output hidden; retry/notify unchanged | 4 (via existing `runSetup`), 9 |
| §8.1 failed → destroy; stuck destroys cleaned | 3 (fail fast), 5 (orphan sweep) |
| §9.4 lab pods blocked from 169.254.169.254; cloud-init installs sysbox | 2, 7, 8 |
| §14 no check scripts or lab containers in the API pod; quotas + network policies; no privileged pods | 2, 6, 7 |
| §14 testing: runner tests against kind (+ sysbox on the real node) | 3, 4 (fake clientset), 8, 9 (kind), 7 (runbook, sysbox) |
| §14 sample repo has a cluster lab | 1 |
| M2 carry-over: pods can reach IMDS until M4 NetworkPolicy | 2, 7 |

**Deliberately not in M4 (and where they land):**
- lab.yaml egress allowlist: needs a CNI with FQDN policies (ruling 3).
- Per-lab resources: ponytail note in `cluster_objects.go`.
- Platform rate card for cluster labs: M6.
- Provisioning as a River job: ruling 5.
- Sysbox under kind: impossible on Docker Desktop. Verified on the AWS node by the runbook.
- Terminal transcripts: M5.
- AWS workspace pod: M6 reuses `ClusterRunner` with a different pod.
- Container registry/ECR for multi-node: roadmap "Known decisions".
