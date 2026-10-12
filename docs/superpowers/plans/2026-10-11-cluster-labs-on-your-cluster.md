# Cluster Labs on Your Own Cluster Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace Crucible-run cluster labs with labs whose Helm charts the laptop agent installs on a Kubernetes cluster the trainee picks from their own kubeconfig, at no cost.

**Architecture:** The agent reports the kubeconfig's contexts; for a cluster lab it opens a local, token-protected Kubernetes API proxy for the chosen context (credentials never leave it), starts a toolbox container (kubectl, helm, `crucible-ready`) pointed at that proxy, and installs the lab's charts with `helm upgrade --install --wait` into `lab-<id>`. The server treats `runtime: cluster` like `local` (through the agent hub, free, self-reported), adds a built-in `ready:` check and release health, and stops pricing and running cluster labs itself. Crucible's own cluster stays only for AWS lab workspaces.

**Tech Stack:** Go 1.26 (client-go v0.34, apimachinery `util/proxy`), Docker, Helm 3 and kubectl in a toolbox image, React 19 + Vite, Playwright on kind for the end-to-end journey.

**Spec:** `docs/superpowers/specs/2026-10-11-cluster-labs-on-your-cluster-design.md`

## Global Constraints

- Every shell that builds or tests starts with `export PATH=$PWD/.local/tools/go/bin:$PWD/.local/tools:$PATH` (the system Go and Terraform are too old).
- Never create real AWS resources. Never touch `~/.kube/config` or the user's kind cluster `platform`; the end-to-end check uses `KUBECONFIG=.local/kind/admin.kubeconfig` and only creates/deletes `crucible-m4`.
- Docker hygiene: never `docker system prune`; remove only what you created.
- Tests first for behaviour changes; every security path keeps a test that fails without the fix.
- Migrations: next free numbers are `00025`, `00026`; every migration has a working `-- +goose Down`.
- Commits: `git commit -m "…" -- <paths>` (add new files with `git add <paths>` first), never `git add -A`; message ends with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Any capability change updates `docs/user` in the same commit; `go test ./internal/docs` must pass. Any UI pattern change updates `.claude/skills/crucible-ui/SKILL.md`.
- Copy is plain and from the user's side of the screen.
- Lab namespace: `lab-<id>`. Default chart timeout: `5m`. Toolbox environment: `KUBECONFIG`, `LAB_NAMESPACE`, `LAB_ID`, `LAB_RELEASES`.
- Proxy listens on `127.0.0.1` on macOS/Windows and on Docker Desktop for Linux, on the Docker bridge gateway address on other Linux; never `0.0.0.0`.
- Toolbox image comes from `CRUCIBLE_TOOLBOX_IMAGE` on the server; cluster labs are unavailable when it is unset.
- Cluster labs cost $0: estimate 0, tier `auto`, no budget.
- Cluster-scoped kinds a chart may create only with `cluster_wide: true`: CustomResourceDefinition, ClusterRole, ClusterRoleBinding, Namespace, StorageClass, PriorityClass, MutatingWebhookConfiguration, ValidatingWebhookConfiguration, PersistentVolume, IngressClass, APIService.

## Review Focus

1. **A cloud kubeconfig whose credential plugin fails** (`aws` not installed, expired SSO login): the trainee must get "can't reach <context>: …" with the plugin's message at provision, not a generic Helm error later. Pinned in Task 6 (`TestProxyProbeNamesTheContextWhenCredentialsFail`).
2. **The kubeconfig file is missing or malformed** (new laptop, half-edited file): the agent keeps running and reports no contexts; the lab page says there is no cluster to pick. Pinned in Task 5 (`TestReadContextsSurvivesMissingAndBrokenFiles`).
3. **Two cluster labs at once** (two trainings): each has its own namespace, proxy and token; ending one leaves the other working. Pinned in Task 7 (`TestTwoClusterLabsAreIndependent`).
4. **A chart that never becomes ready** (slow image pull on a laptop): provisioning fails with Helm's output and leaves nothing behind on the cluster. Pinned in Task 7 (`TestFailedInstallCleansUp`).
5. **The agent crashed mid-lab and restarts** (its start-up cleanup runs with no proxy alive): destroy must reopen the proxy from the saved spec and uninstall from the cluster. Pinned in Task 7 (`TestDestroyAfterRestartReopensTheProxy`).

---

### Task 1: The agent protocol carries contexts and cluster specs

**Files:**
- Modify: `internal/agentproto/proto.go`
- Create: `internal/agentproto/proto_test.go`

**Interfaces:**
- Produces: `ap.TContexts`, `ap.KubeContext{Name, Server string}`, `ap.Chart{Name, Path, Values string; TimeoutS int64}`, `ap.ClusterSpec{Context string; Charts []Chart; ClusterWide bool; Toolbox string}`, `ap.Msg.Cluster *ClusterSpec`, `ap.Msg.Contexts []KubeContext`, `ap.ClusterScopedKinds map[string]bool`.

- [ ] **Step 1: Write the failing test**

```go
package agentproto

import (
	"encoding/json"
	"strings"
	"testing"
)

// A cluster provision carries its spec; a laptop lab's messages look exactly as before, so old agents still work.
func TestClusterFieldsTravelAndStayOutOfOtherMessages(t *testing.T) {
	m := Msg{Type: TProvision, LabID: "0123456789ab", Cluster: &ClusterSpec{Context: "kind-dev", Toolbox: "img@sha256:x",
		Charts: []Chart{{Name: "shop", Path: "charts/shop", Values: "values/shop.yaml", TimeoutS: 300}}, ClusterWide: true}}
	b, _ := json.Marshal(m)
	var back Msg
	if err := json.Unmarshal(b, &back); err != nil || back.Cluster == nil || back.Cluster.Charts[0].TimeoutS != 300 || !back.Cluster.ClusterWide {
		t.Fatalf("round trip: %s %+v %v", b, back.Cluster, err)
	}
	local, _ := json.Marshal(Msg{Type: TProvision, LabID: "0123456789ab", Compose: "compose.yaml"})
	if strings.Contains(string(local), "cluster") || strings.Contains(string(local), "contexts") {
		t.Fatalf("laptop provision grew fields: %s", local)
	}
	hello, _ := json.Marshal(Msg{Type: THello, Contexts: []KubeContext{{Name: "kind-dev", Server: "127.0.0.1:6443"}}})
	if !strings.Contains(string(hello), `"contexts":[{"name":"kind-dev","server":"127.0.0.1:6443"}]`) {
		t.Fatalf("hello: %s", hello)
	}
	if !ClusterScopedKinds["CustomResourceDefinition"] || ClusterScopedKinds["Deployment"] {
		t.Fatal("cluster-scoped kinds")
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/agentproto`
Expected: FAIL, `undefined: ClusterSpec`.

- [ ] **Step 3: Implement**

In `internal/agentproto/proto.go`, add to the `const` block after `THello`:

```go
	TContexts  = "contexts"   // agent → API whenever the kubeconfig's contexts change: Contexts
```

Add to `Msg` after `Error`:

```go
	Cluster  *ClusterSpec  `json:"cluster,omitempty"`  // provision of a cluster lab
	Contexts []KubeContext `json:"contexts,omitempty"` // hello and contexts: the agent's kubeconfig contexts
```

Append to the file:

```go
// KubeContext is one context of the agent's kubeconfig: its name and its API server's host. Never credentials.
type KubeContext struct {
	Name   string `json:"name"`
	Server string `json:"server"`
}

// Chart is one Helm chart a cluster lab installs, in order.
type Chart struct {
	Name     string `json:"name"`             // the release name
	Path     string `json:"path"`             // a folder in the lab bundle
	Values   string `json:"values,omitempty"` // a file in the lab bundle
	TimeoutS int64  `json:"timeout_s"`        // helm --wait --timeout
}

// ClusterSpec is what a cluster lab's provision carries besides its bundle.
type ClusterSpec struct {
	Context     string  `json:"context"`
	Charts      []Chart `json:"charts"`
	ClusterWide bool    `json:"cluster_wide"`
	Toolbox     string  `json:"toolbox"` // the toolbox image the server pins
}

// ClusterScopedKinds are the kinds a chart may create only when its lab declares cluster_wide.
var ClusterScopedKinds = map[string]bool{"CustomResourceDefinition": true, "ClusterRole": true, "ClusterRoleBinding": true,
	"Namespace": true, "StorageClass": true, "PriorityClass": true, "MutatingWebhookConfiguration": true,
	"ValidatingWebhookConfiguration": true, "PersistentVolume": true, "IngressClass": true, "APIService": true}
```

- [ ] **Step 4: Run it to see it pass**

Run: `go test ./internal/agentproto && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agentproto/proto_test.go
git commit -m "feat(agentproto): contexts and cluster specs on the wire

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/agentproto/proto.go internal/agentproto/proto_test.go
```

---

### Task 2: The lab format and its lint rules

**Files:**
- Modify: `internal/content/types.go` (Lab, Script; new ClusterLab, ClusterChart)
- Modify: `internal/content/load.go` (`lab`, `script`; new `clusterLab`, `chartDeps`, `chartKinds`)
- Test: `internal/content/load_test.go` (replace `TestClusterComposeMayUseHostAccess`; add `clusterTree`, `TestClusterLabRules`, `TestClusterLabLoads`)
- Modify: `examples/forge-101/modules/03-cluster-heat/lab/` (becomes a Helm lab: `charts/forge`, `checks/01-scale.sh`, `setup/02-break-image.sh`, `tasks/01-scale.md`, `tasks/02-relight.md`)
- Modify: `internal/labs/cluster_test.go`, `internal/labs/cluster_integration_test.go`, `scripts/cluster-check.sh` (drop tests of the old compose cluster lab)

**Interfaces:**
- Consumes: `agentproto.ClusterScopedKinds` (Task 1).
- Produces: `content.Lab.Cluster *ClusterLab`; `content.ClusterLab{Charts []ClusterChart; ClusterWide bool}`; `content.ClusterChart{Name, Path, Values string; Timeout yamlx.Duration}` (Timeout defaults to 5m after Load); `content.Script.Ready []string`; `content.ToolboxService = "toolbox"` (every cluster terminal and script has `Service`/`RunIn` set to it after Load).

- [ ] **Step 1: Write the failing tests**

In `internal/content/load_test.go`, delete `TestClusterComposeMayUseHostAccess` and add:

```go
// clusterTree is base with its lab turned into a cluster lab: one chart, scripts in the toolbox.
func clusterTree(t *testing.T, override map[string]string) string {
	t.Helper()
	files := map[string]string{
		"modules/m1/lab/lab.yaml": `id: l1
runtime: cluster
cluster:
  charts:
    - {name: shop, path: charts/shop, values: values/shop.yaml}
terminals: [{name: shell}]
tasks:
  - id: t1
    instructions: tasks/t1.md
    check: {script: checks/t1.sh}
  - id: t2
    instructions: tasks/t2.md
    setup: {script: setup/t2.sh}
    check: {ready: [shop]}
`,
		"modules/m1/lab/compose.yaml":                     "<delete>",
		"modules/m1/lab/hints/sol.md":                     "<delete>",
		"modules/m1/lab/charts/shop/Chart.yaml":           "apiVersion: v2\nname: shop\nversion: 0.1.0\n",
		"modules/m1/lab/charts/shop/templates/deploy.yaml": "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: shop}\n",
		"modules/m1/lab/values/shop.yaml":                 "replicas: 1\n",
	}
	maps.Copy(files, override)
	return tree(t, files)
}

func TestClusterLabLoads(t *testing.T) {
	tr, probs := Load(clusterTree(t, nil))
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	lab := tr.Modules[0].Lab
	if c := lab.Cluster.Charts[0]; c.Timeout.D() != 5*time.Minute || c.Values != "values/shop.yaml" {
		t.Fatalf("chart defaults: %+v", c)
	}
	if lab.Terminals[0].Service != ToolboxService || lab.Tasks[0].Check.RunIn != ToolboxService || lab.Tasks[1].Setup.RunIn != ToolboxService {
		t.Fatalf("everything runs in the toolbox: %+v %+v", lab.Terminals, lab.Tasks[0].Check)
	}
	if !slices.Equal(lab.Tasks[1].Check.Ready, []string{"shop"}) {
		t.Fatalf("ready check: %+v", lab.Tasks[1].Check)
	}
}

func TestClusterLabRules(t *testing.T) {
	labWith := func(cluster, extra string) string {
		return "id: l1\nruntime: cluster\n" + cluster + "terminals: [{name: shell}]\ntasks:\n  - id: t1\n    instructions: tasks/t1.md\n    check: {script: checks/t1.sh}\n" + extra
	}
	one := "cluster:\n  charts:\n    - {name: shop, path: charts/shop}\n"
	for name, c := range map[string]struct {
		files map[string]string
		want  string
	}{
		"no cluster block": {map[string]string{"modules/m1/lab/lab.yaml": labWith("", "")}, "cluster labs now install Helm charts"},
		"compose given":    {map[string]string{"modules/m1/lab/lab.yaml": labWith(one, "compose: compose.yaml\n")}, "compose files are for laptop labs"},
		"no chart folder":  {map[string]string{"modules/m1/lab/lab.yaml": labWith("cluster:\n  charts:\n    - {name: shop, path: charts/nope}\n", "")}, "no chart folder"},
		"escaping path":    {map[string]string{"modules/m1/lab/lab.yaml": labWith("cluster:\n  charts:\n    - {name: shop, path: ../../x}\n", "")}, "must stay inside"},
		"bad release name": {map[string]string{"modules/m1/lab/lab.yaml": labWith("cluster:\n  charts:\n    - {name: Shop_1, path: charts/shop}\n", "")}, "release name"},
		"duplicate name":   {map[string]string{"modules/m1/lab/lab.yaml": labWith("cluster:\n  charts:\n    - {name: shop, path: charts/shop}\n    - {name: shop, path: charts/shop}\n", "")}, "duplicate release"},
		"terminal service": {map[string]string{"modules/m1/lab/lab.yaml": strings.Replace(labWith(one, ""), "{name: shell}", "{name: shell, service: web}", 1)}, "terminals open in the toolbox"},
		"run_in":           {map[string]string{"modules/m1/lab/lab.yaml": strings.Replace(labWith(one, ""), "{script: checks/t1.sh}", "{script: checks/t1.sh, run_in: web}", 1)}, "scripts run in the toolbox"},
		"aws block":        {map[string]string{"modules/m1/lab/lab.yaml": labWith(one, "aws: {region: eu-west-1, max_hourly_usd: 1}\n")}, "aws: is only for runtime: aws"},
		"ready unknown":    {map[string]string{"modules/m1/lab/lab.yaml": strings.Replace(labWith(one, ""), "{script: checks/t1.sh}", "{ready: [nope]}", 1)}, "ready: nope is not a release"},
		"ready and script": {map[string]string{"modules/m1/lab/lab.yaml": strings.Replace(labWith(one, ""), "{script: checks/t1.sh}", "{script: checks/t1.sh, ready: [shop]}", 1)}, "either script or ready"},
		"floating dep": {map[string]string{"modules/m1/lab/charts/shop/Chart.yaml": "apiVersion: v2\nname: shop\nversion: 0.1.0\ndependencies:\n  - {name: redis, version: \"^18.0.0\", repository: https://charts.bitnami.com/bitnami}\n"},
			"exact version"},
		"http dep": {map[string]string{"modules/m1/lab/charts/shop/Chart.yaml": "apiVersion: v2\nname: shop\nversion: 0.1.0\ndependencies:\n  - {name: redis, version: 18.0.1, repository: http://charts.example.com}\n"},
			"https://, oci:// or file://"},
		"undeclared cluster-wide": {map[string]string{"modules/m1/lab/charts/shop/templates/role.yaml": "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata: {name: r}\n"},
			"ClusterRole is cluster-wide: set cluster_wide: true"},
		"crds folder": {map[string]string{"modules/m1/lab/charts/shop/crds/x.yaml": "kind: CustomResourceDefinition\n"}, "crds/ installs cluster-wide"},
	} {
		t.Run(name, func(t *testing.T) {
			_, probs := Load(clusterTree(t, c.files))
			if !strings.Contains(fmt.Sprint(probs), c.want) {
				t.Fatalf("want a problem containing %q, got %v", c.want, probs)
			}
		})
	}
	ok := clusterTree(t, map[string]string{
		"modules/m1/lab/lab.yaml":                         labWith("cluster:\n  charts:\n    - {name: shop, path: charts/shop}\n  cluster_wide: true\n", ""),
		"modules/m1/lab/charts/shop/templates/role.yaml":  "kind: ClusterRole\nmetadata: {name: r}\n",
		"modules/m1/lab/charts/shop/Chart.yaml":           "apiVersion: v2\nname: shop\nversion: 0.1.0\ndependencies:\n  - {name: redis, version: 18.0.1, repository: oci://registry-1.docker.io/bitnamicharts}\n  - {name: lib, version: 0.1.0, repository: \"file://../lib\"}\n",
		"modules/m1/lab/charts/lib/Chart.yaml":            "apiVersion: v2\nname: lib\nversion: 0.1.0\n",
	})
	if _, probs := Load(ok); len(probs) > 0 {
		t.Fatalf("declared cluster-wide, pinned deps: %v", probs)
	}
}
```

Add `"slices"` to the test file's imports.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/content -run 'TestClusterLab'`
Expected: FAIL to compile (`undefined: ToolboxService`).

- [ ] **Step 3: Add the types**

In `internal/content/types.go`, add to `Lab` after `AWS *AWSConfig ...`:

```go
	Cluster     *ClusterLab    `yaml:"cluster"`
```

Add to `Script` after `Timeout`:

```go
	Ready   []string       `yaml:"ready"` // cluster labs, checks only: these releases (or "all") must be ready
```

Append:

```go
// ToolboxService is the one place a cluster lab's terminals and scripts run: the agent's toolbox container.
const ToolboxService = "toolbox"

// ClusterLab is a runtime: cluster lab's charts, installed in order into the lab's namespace.
type ClusterLab struct {
	Charts      []ClusterChart `yaml:"charts"`
	ClusterWide bool           `yaml:"cluster_wide"`
}

type ClusterChart struct {
	Name    string         `yaml:"name"`
	Path    string         `yaml:"path"`
	Values  string         `yaml:"values"`
	Timeout yamlx.Duration `yaml:"timeout"`
}
```

- [ ] **Step 4: Teach the loader**

In `internal/content/load.go`, in `func (l *loader) lab`:

1. Replace

```go
	if lab.Compose == "" {
		lab.Compose = "compose.yaml"
	}
```

with

```go
	if lab.Compose == "" && lab.Runtime != "cluster" {
		lab.Compose = "compose.yaml"
	}
```

2. Replace the `switch lab.Runtime { … }` block with:

```go
	switch lab.Runtime {
	case "local":
		if p, ok := l.file(dir, lab.Compose, lf); ok {
			var c struct {
				Services map[string]yaml.Node `yaml:"services"`
			}
			if err := yamlx.ReadLoose(p, &c); err != nil {
				l.add(p, "%v", err)
			}
			for name := range c.Services {
				services[name] = true
			}
			l.localCompose(p)
		}
	case "cluster":
		services[ToolboxService] = true
		l.clusterLab(dir, lf, lab)
	case "aws":
		services["workspace"] = true // AWS labs get one workspace container (spec §8.2)
		l.awsModule(dir, lf, lab)
	default:
		l.add(lf, "runtime must be cluster, local or aws")
	}
	if lab.AWS != nil && lab.Runtime != "aws" {
		l.add(lf, "aws: is only for runtime: aws")
	}
```

(If an `aws:` check for other runtimes already exists further down, delete the duplicate.)

3. In the terminals loop, before `if !services[term.Service]`, insert:

```go
		if lab.Runtime == "cluster" {
			if term.Service != "" {
				l.add(lf, "terminal %q: cluster lab terminals open in the toolbox; drop service", term.Name)
			}
			term.Service = ToolboxService
		}
```

Change the loop header from `for _, term := range lab.Terminals {` to `for i := range lab.Terminals {` with `term := &lab.Terminals[i]` as its first line, so the default sticks.

4. Before `if lab.Setup != nil {` insert:

```go
	if lab.Runtime == "cluster" {
		toolbox := func(what string, s *Script) {
			if s == nil {
				return
			}
			if s.RunIn != "" && s.RunIn != ToolboxService {
				l.add(lf, "%s: cluster lab scripts run in the toolbox; drop run_in", what)
			}
			s.RunIn = ToolboxService
		}
		toolbox("lab setup", lab.Setup)
		for _, t := range lab.Tasks {
			toolbox("task "+t.ID+" check", t.Check)
			toolbox("task "+t.ID+" setup", t.Setup)
		}
	}
```

5. In the task loop, replace

```go
		if t.Check != nil {
			l.script(dir, lf, where+" check", t.Check, services, 30*time.Second)
		}
```

with

```go
		if t.Check != nil {
			l.readyCheck(lf, where, lab, t.Check)
			l.script(dir, lf, where+" check", t.Check, services, 30*time.Second)
		}
```

6. At the top of `func (l *loader) script`, replace

```go
	if s.Script == "" {
		l.add(lf, "%s: script is required", what)
		return
	}
```

with

```go
	if s.Script == "" && len(s.Ready) > 0 { // a ready check: Crucible runs crucible-ready (readyCheck checked the names)
		if s.Timeout == 0 {
			s.Timeout = yamlx.Duration(def)
		}
		return
	}
	if s.Script == "" {
		l.add(lf, "%s: script is required", what)
		return
	}
```

7. Append the new loader methods:

```go
var releaseName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,51}[a-z0-9])?$`)
var exactVersion = regexp.MustCompile(`^v?\d+\.\d+\.\d+([-+][0-9A-Za-z.+-]+)?$`)
var kindLine = regexp.MustCompile(`(?m)^kind:\s*["']?([A-Za-z]+)`)

// clusterLab checks a runtime: cluster lab's charts (spec 2026-10-11 §1).
func (l *loader) clusterLab(dir, lf string, lab *Lab) {
	if lab.Compose != "" {
		l.add(lf, "cluster labs now install Helm charts; compose files are for laptop labs")
	}
	if lab.Cluster == nil || len(lab.Cluster.Charts) == 0 {
		l.add(lf, "cluster labs now install Helm charts: add cluster.charts (compose files are for laptop labs)")
		return
	}
	seen := map[string]bool{}
	for i := range lab.Cluster.Charts {
		c := &lab.Cluster.Charts[i]
		if !releaseName.MatchString(c.Name) {
			l.add(lf, "chart %q: the release name must be lowercase letters, digits and dashes, at most 53 characters", c.Name)
		}
		if seen[c.Name] {
			l.add(lf, "duplicate release name %q", c.Name)
		}
		seen[c.Name] = true
		if c.Timeout == 0 {
			c.Timeout = yamlx.Duration(5 * time.Minute)
		}
		if c.Timeout < 0 {
			l.add(lf, "chart %q: timeout must be positive", c.Name)
		}
		if c.Values != "" {
			l.file(dir, c.Values, lf)
		}
		rel := filepath.FromSlash(c.Path)
		if !filepath.IsLocal(rel) {
			l.add(lf, "chart %q: path %q must stay inside the lab", c.Name, c.Path)
			continue
		}
		chartDir := filepath.Join(dir, rel)
		if _, err := os.Stat(filepath.Join(chartDir, "Chart.yaml")); err != nil {
			l.add(lf, "chart %q: no chart folder at %s (it needs a Chart.yaml)", c.Name, c.Path)
			continue
		}
		l.chartDeps(dir, chartDir)
		if !lab.Cluster.ClusterWide {
			l.chartKinds(chartDir)
		}
	}
}

// chartDeps refuses dependencies that are not pinned to an exact version from https://, oci:// or a file:// path
// inside the lab.
func (l *loader) chartDeps(labDir, chartDir string) {
	p := filepath.Join(chartDir, "Chart.yaml")
	var c struct {
		Dependencies []struct {
			Name, Version, Repository string
		} `yaml:"dependencies"`
	}
	if err := yamlx.ReadLoose(p, &c); err != nil {
		l.add(p, "%v", err)
		return
	}
	for _, d := range c.Dependencies {
		switch {
		case strings.HasPrefix(d.Repository, "file://"):
			target, err := filepath.Rel(labDir, filepath.Join(chartDir, filepath.FromSlash(strings.TrimPrefix(d.Repository, "file://"))))
			if err != nil || !filepath.IsLocal(target) {
				l.add(p, "dependency %q: file:// must stay inside the lab", d.Name)
			}
		case strings.HasPrefix(d.Repository, "https://"), strings.HasPrefix(d.Repository, "oci://"):
		default:
			l.add(p, "dependency %q: the repository must be https://, oci:// or file://", d.Name)
		}
		if !exactVersion.MatchString(d.Version) {
			l.add(p, "dependency %q: pin an exact version (like 18.0.1), not %q", d.Name, d.Version)
		}
	}
}

// chartKinds is lint's hint for undeclared cluster-wide resources; the agent enforces it on the rendered chart.
func (l *loader) chartKinds(chartDir string) {
	if entries, _ := os.ReadDir(filepath.Join(chartDir, "crds")); len(entries) > 0 {
		l.add(filepath.Join(chartDir, "crds"), "crds/ installs cluster-wide resources: set cluster_wide: true")
	}
	_ = filepath.WalkDir(filepath.Join(chartDir, "templates"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, m := range kindLine.FindAllStringSubmatch(string(b), -1) {
			if agentproto.ClusterScopedKinds[m[1]] {
				l.add(p, "%s is cluster-wide: set cluster_wide: true", m[1])
				return nil
			}
		}
		return nil
	})
}

// readyCheck validates a ready: check: cluster labs only, either script or ready, names of this lab's releases.
func (l *loader) readyCheck(lf, where string, lab *Lab, s *Script) {
	if len(s.Ready) == 0 {
		return
	}
	if lab.Runtime != "cluster" {
		l.add(lf, "%s: ready: is for cluster labs", where)
		return
	}
	if s.Script != "" {
		l.add(lf, "%s: a check has either script or ready, not both", where)
	}
	if len(s.Ready) == 1 && s.Ready[0] == "all" {
		return
	}
	names := map[string]bool{}
	if lab.Cluster != nil {
		for _, c := range lab.Cluster.Charts {
			names[c.Name] = true
		}
	}
	for _, r := range s.Ready {
		if !names[r] {
			l.add(lf, "%s: ready: %s is not a release of this lab", where, r)
		}
	}
}
```

Add imports to `load.go` if missing: `"io/fs"`, `"regexp"`, `"crucible/internal/agentproto"`.

- [ ] **Step 5: Rewrite Forge 101's cluster lab as a Helm lab**

Every labs, learn and journey test loads `examples/forge-101`, and the new lint refuses its compose cluster lab, so the
example changes in this commit. Under `examples/forge-101/modules/03-cluster-heat/lab/`: `git rm compose.yaml
checks/01-cast.sh checks/02-relight.sh setup/02-break-nginx.sh`, `git mv tasks/01-cast.md tasks/01-scale.md`, then
write these files (scripts `chmod +x`). Points stay 2 + 3, which `internal/learn` tests count on.


`charts/forge/Chart.yaml`:

```yaml
apiVersion: v2
name: forge
description: A small web forge for the Cluster Heat lab
version: 0.1.0
appVersion: "1.27"
```

`charts/forge/values.yaml`:

```yaml
replicas: 1
image: nginx:1.27-alpine
```

`charts/forge/templates/deployment.yaml`:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: forge
  labels: {app: forge}
spec:
  replicas: {{ .Values.replicas }}
  selector:
    matchLabels: {app: forge}
  template:
    metadata:
      labels: {app: forge}
    spec:
      containers:
        - name: web
          image: {{ .Values.image }}
          ports: [{containerPort: 80}]
          readinessProbe:
            httpGet: {path: /, port: 80}
```

`charts/forge/templates/service.yaml`:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: forge
spec:
  selector: {app: forge}
  ports: [{port: 80, targetPort: 80}]
```

`lab.yaml`:

```yaml
id: crucible-heat
runtime: cluster            # Helm on a cluster of your kubeconfig, through your laptop agent; no cost
ttl: 1h
idle_timeout: 20m
idle_warning: 5m
task_order: linear
cluster:
  charts:
    - {name: forge, path: charts/forge, timeout: 3m}
terminals:
  - { name: shell }
tasks:
  - id: t1-scale
    instructions: tasks/01-scale.md
    check: { script: checks/01-scale.sh }
    points: 2
  - id: t2-relight
    instructions: tasks/02-relight.md
    setup: { script: setup/02-break-image.sh }
    check: { ready: [forge] }
    points: 3
    hints:
      - text: "Look at `kubectl describe pod -l app=forge`, then set the image back with `kubectl set image deploy/forge web=nginx:1.27-alpine`."
```

`checks/01-scale.sh` (chmod +x):

```bash
#!/usr/bin/env bash
set -euo pipefail
ready=$(kubectl -n "$LAB_NAMESPACE" get deploy forge -o jsonpath='{.status.readyReplicas}')
[ "${ready:-0}" -ge 2 ] || { echo "The forge has ${ready:-0} ready replica(s); it needs 2."; exit 1; }
echo "Two fires burn in the forge."
```

`setup/02-break-image.sh` (chmod +x):

```bash
#!/usr/bin/env bash
set -euo pipefail
kubectl -n "$LAB_NAMESPACE" set image deploy/forge web=nginx:does-not-exist >/dev/null
```

`tasks/01-scale.md`:

```markdown
# Stoke the forge

Your lab runs in its own namespace on the cluster you picked. Your terminal already points at it.

Scale the `forge` deployment to two replicas, then choose **Check**.
```

`tasks/02-relight.md`:

```markdown
# Relight the forge

Someone changed the forge's image and its pods won't start. Find out why, put it right, and choose **Check** once
every pod is ready again.
```

- [ ] **Step 6: Drop the tests of the old compose cluster lab**

- `internal/labs/cluster_test.go`: delete `TestClusterLabChecksAreNotSelfReported` (it runs `t1-cast` in the `shell`
  service; Task 10 pins the new behaviour). Keep the rest until Task 11.
- `internal/labs/cluster_integration_test.go` (build tag `cluster`): delete `TestClusterLabOnKind` and any helper only
  it used; in `scripts/cluster-check.sh` change `-run 'TestClusterLabOnKind|TestAWSLabEgressOnKind'` to
  `-run 'TestAWSLabEgressOnKind'`. Task 14's browser journey replaces it.

- [ ] **Step 7: Run lint and every test that loads the example**

Run: `go test ./internal/content && make build && ./bin/crucible lint examples/forge-101 && go vet -tags cluster ./internal/labs && go test ./internal/labs ./internal/learn ./internal/journey`
Expected: PASS. If an older content test still uses a cluster lab with compose (search `runtime: cluster` in
`internal/content/*_test.go`), switch it to `clusterTree`.

- [ ] **Step 8: Commit**

```bash
git add examples/forge-101/modules/03-cluster-heat
git commit -m "feat(content): cluster labs are Helm charts; lint for charts, deps, ready checks and cluster-wide kinds

Forge 101's Cluster Heat becomes a Helm lab.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/content examples/forge-101/modules/03-cluster-heat internal/labs/cluster_test.go internal/labs/cluster_integration_test.go scripts/cluster-check.sh
```

---

### Task 3: `crucible-ready`, the readiness judge

**Files:**
- Create: `internal/ready/ready.go`, `internal/ready/ready_test.go`
- Create: `cmd/crucible-ready/main.go`

**Interfaces:**
- Produces: `ready.Status` (`"healthy" | "progressing" | "failing"`), `ready.Release{Name string; Status Status; Unready []string}`, `ready.Report{Releases []Release}`, `ready.Parse(manifest []byte) []ready.Object`, `ready.Judge(ctx, kubernetes.Interface, namespace, release string, objs []Object) Release`, command `crucible-ready [--json] <release…|all>` (exit 1 when anything is unready).

- [ ] **Step 1: Write the failing test**

```go
package ready

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
)

const ns = "lab-0123456789ab"

func meta(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Name: name, Namespace: ns, Generation: 1} }

func TestParseReadsKindsAndNames(t *testing.T) {
	objs := Parse([]byte("---\n# Source: shop/templates/a.yaml\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: shop\n---\nkind: Service\nmetadata: {name: shop}\n---\n"))
	if len(objs) != 2 || objs[0] != (Object{Kind: "Deployment", Name: "shop"}) || objs[1].Kind != "Service" {
		t.Fatalf("%+v", objs)
	}
}

func TestJudge(t *testing.T) {
	cs := fake.NewClientset(
		&appsv1.Deployment{ObjectMeta: meta("up"), Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](2)},
			Status: appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 2, AvailableReplicas: 2}},
		&appsv1.Deployment{ObjectMeta: meta("half"), Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](3)},
			Status: appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 3, AvailableReplicas: 1}},
		&corev1.PersistentVolumeClaim{ObjectMeta: meta("data"), Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending}},
		&batchv1.Job{ObjectMeta: meta("migrate"), Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}}},
		&corev1.Service{ObjectMeta: meta("web"), Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "web"}}},
		&corev1.Service{ObjectMeta: meta("ok"), Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "ok"}}},
		&discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "ok-x", Namespace: ns, Labels: map[string]string{"kubernetes.io/service-name": "ok"}},
			Endpoints: []discoveryv1.Endpoint{{Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(true)}}}},
	)
	ctx := context.Background()
	if r := Judge(ctx, cs, ns, "a", []Object{{"Deployment", "up"}, {"Service", "ok"}, {"ConfigMap", "c"}}); r.Status != Healthy || len(r.Unready) != 0 {
		t.Fatalf("healthy: %+v", r)
	}
	r := Judge(ctx, cs, ns, "b", []Object{{"Deployment", "half"}, {"PersistentVolumeClaim", "data"}, {"Service", "web"}})
	if r.Status != Progressing || strings.Join(r.Unready, "; ") != "deployment/half: 1 of 3 ready; persistentvolumeclaim/data: Pending; service/web: no ready endpoints" {
		t.Fatalf("progressing: %+v", r)
	}
	if r := Judge(ctx, cs, ns, "c", []Object{{"Job", "migrate"}}); r.Status != Failing || r.Unready[0] != "job/migrate: failed" {
		t.Fatalf("failing: %+v", r)
	}
	if r := Judge(ctx, cs, ns, "d", []Object{{"Deployment", "gone"}}); r.Status != Progressing || r.Unready[0] != "deployment/gone: not found" {
		t.Fatalf("missing: %+v", r)
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/ready`
Expected: FAIL, package has no Go files.

- [ ] **Step 3: Implement `internal/ready/ready.go`**

```go
// Package ready judges whether a Helm release's resources are ready, the way kubectl rollout status would, once and
// without waiting. crucible-ready runs it in a cluster lab's toolbox; the server shows its Report as release health.
package ready

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Status string

const (
	Healthy     Status = "healthy"
	Progressing Status = "progressing"
	Failing     Status = "failing"
)

type Release struct {
	Name    string   `json:"name"`
	Status  Status   `json:"status"`
	Unready []string `json:"unready"`
}

type Report struct {
	Releases []Release `json:"releases"`
}

type Object struct{ Kind, Name string }

// Parse reads the kinds and names of a release manifest (helm get manifest).
func Parse(manifest []byte) []Object {
	var out []Object
	dec := yaml.NewDecoder(bytes.NewReader(manifest))
	for {
		var d struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
		}
		if err := dec.Decode(&d); err != nil {
			return out
		}
		if d.Kind != "" && d.Metadata.Name != "" {
			out = append(out, Object{d.Kind, d.Metadata.Name})
		}
	}
}

// Judge checks each object; kinds without a notion of readiness count as ready.
func Judge(ctx context.Context, cs kubernetes.Interface, ns, release string, objs []Object) Release {
	r := Release{Name: release, Status: Healthy, Unready: []string{}}
	for _, o := range objs {
		msg, failed := judge(ctx, cs, ns, o)
		if msg == "" {
			continue
		}
		r.Unready = append(r.Unready, strings.ToLower(o.Kind)+"/"+o.Name+": "+msg)
		if failed {
			r.Status = Failing
		} else if r.Status == Healthy {
			r.Status = Progressing
		}
	}
	return r
}

func judge(ctx context.Context, cs kubernetes.Interface, ns string, o Object) (msg string, failed bool) {
	get := metav1.GetOptions{}
	notFound := func(err error) (string, bool) {
		if apierrors.IsNotFound(err) {
			return "not found", false
		}
		return err.Error(), false
	}
	switch o.Kind {
	case "Deployment":
		d, err := cs.AppsV1().Deployments(ns).Get(ctx, o.Name, get)
		if err != nil {
			return notFound(err)
		}
		want := int32(1)
		if d.Spec.Replicas != nil {
			want = *d.Spec.Replicas
		}
		if d.Status.ObservedGeneration < d.Generation || d.Status.UpdatedReplicas < want || d.Status.AvailableReplicas < want {
			return fmt.Sprintf("%d of %d ready", d.Status.AvailableReplicas, want), false
		}
	case "StatefulSet":
		s, err := cs.AppsV1().StatefulSets(ns).Get(ctx, o.Name, get)
		if err != nil {
			return notFound(err)
		}
		want := int32(1)
		if s.Spec.Replicas != nil {
			want = *s.Spec.Replicas
		}
		if s.Status.ReadyReplicas < want || s.Status.UpdatedReplicas < want {
			return fmt.Sprintf("%d of %d ready", s.Status.ReadyReplicas, want), false
		}
	case "DaemonSet":
		d, err := cs.AppsV1().DaemonSets(ns).Get(ctx, o.Name, get)
		if err != nil {
			return notFound(err)
		}
		if d.Status.NumberReady < d.Status.DesiredNumberScheduled || d.Status.UpdatedNumberScheduled < d.Status.DesiredNumberScheduled {
			return fmt.Sprintf("%d of %d ready", d.Status.NumberReady, d.Status.DesiredNumberScheduled), false
		}
	case "Job":
		j, err := cs.BatchV1().Jobs(ns).Get(ctx, o.Name, get)
		if err != nil {
			return notFound(err)
		}
		for _, c := range j.Status.Conditions {
			if c.Status == corev1.ConditionTrue && c.Type == batchv1.JobFailed {
				return "failed", true
			}
			if c.Status == corev1.ConditionTrue && c.Type == batchv1.JobComplete {
				return "", false
			}
		}
		return "running", false
	case "Pod":
		p, err := cs.CoreV1().Pods(ns).Get(ctx, o.Name, get)
		if err != nil {
			return notFound(err)
		}
		if p.Status.Phase == corev1.PodFailed {
			return "failed", true
		}
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
				return "", false
			}
		}
		return string(p.Status.Phase), false
	case "PersistentVolumeClaim":
		c, err := cs.CoreV1().PersistentVolumeClaims(ns).Get(ctx, o.Name, get)
		if err != nil {
			return notFound(err)
		}
		if c.Status.Phase != corev1.ClaimBound {
			return string(c.Status.Phase), false
		}
	case "Service":
		s, err := cs.CoreV1().Services(ns).Get(ctx, o.Name, get)
		if err != nil {
			return notFound(err)
		}
		if s.Spec.Type == corev1.ServiceTypeExternalName || len(s.Spec.Selector) == 0 {
			return "", false
		}
		slices, err := cs.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=" + o.Name})
		if err != nil {
			return err.Error(), false
		}
		for _, sl := range slices.Items {
			for _, e := range sl.Endpoints {
				if e.Conditions.Ready != nil && *e.Conditions.Ready {
					return "", false
				}
			}
		}
		return "no ready endpoints", false
	}
	return "", false
}
```

- [ ] **Step 4: Implement `cmd/crucible-ready/main.go`**

```go
// Command crucible-ready says whether a cluster lab's Helm releases are ready. It runs in the lab's toolbox:
//
//	crucible-ready [--json] <release>... | all
//
// all means every release of the lab ($LAB_RELEASES). It exits 1 when anything is not ready.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"crucible/internal/ready"
)

func main() {
	asJSON := flag.Bool("json", false, "print a JSON report")
	flag.Parse()
	names := flag.Args()
	if len(names) == 1 && names[0] == "all" {
		names = strings.Fields(os.Getenv("LAB_RELEASES"))
	}
	ns := os.Getenv("LAB_NAMESPACE")
	if len(names) == 0 || ns == "" {
		fmt.Fprintln(os.Stderr, "usage: crucible-ready [--json] <release>... | all (needs LAB_NAMESPACE)")
		os.Exit(2)
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), nil).ClientConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "crucible-ready:", err)
		os.Exit(2)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "crucible-ready:", err)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rep := ready.Report{Releases: []ready.Release{}}
	for _, n := range names {
		out, err := exec.CommandContext(ctx, "helm", "get", "manifest", n, "-n", ns).Output()
		if err != nil {
			rep.Releases = append(rep.Releases, ready.Release{Name: n, Status: ready.Failing, Unready: []string{"release not installed"}})
			continue
		}
		rep.Releases = append(rep.Releases, ready.Judge(ctx, cs, ns, n, ready.Parse(out)))
	}
	ok := true
	for _, r := range rep.Releases {
		ok = ok && r.Status == ready.Healthy
	}
	if *asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(rep)
	} else {
		for _, r := range rep.Releases {
			fmt.Printf("%s: %s\n", r.Name, r.Status)
			for _, u := range r.Unready {
				fmt.Println("  " + u)
			}
		}
	}
	if !ok {
		os.Exit(1)
	}
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/ready && go build ./cmd/crucible-ready && go vet ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/ready cmd/crucible-ready
git commit -m "feat: crucible-ready judges a Helm release's resources like kubectl rollout status

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/ready cmd/crucible-ready
```

---

### Task 4: The toolbox image

**Files:**
- Create: `deploy/toolbox/Dockerfile`
- Modify: `Makefile` (target `toolbox`)

**Interfaces:**
- Consumes: `cmd/crucible-ready` (Task 3).
- Produces: image `crucible-toolbox:<tag>` with `/usr/local/bin/{kubectl,helm,k9s,jq,crucible-ready}`, `bash`, default user-agnostic `HOME=/tmp`; `make toolbox TOOLBOX_TAG=dev`.

- [ ] **Step 1: Write the Dockerfile**

```dockerfile
# The cluster lab toolbox: what a trainee's terminals and a lab's scripts use against their cluster. The server pins it
# by digest (CRUCIBLE_TOOLBOX_IMAGE); bump these versions deliberately.
FROM golang:1.26-alpine AS ready
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/crucible-ready cmd/crucible-ready
COPY internal/ready internal/ready
RUN CGO_ENABLED=0 go build -o /crucible-ready ./cmd/crucible-ready

FROM alpine:3.22
ARG TARGETARCH
ARG KUBECTL=v1.34.1
ARG HELM=v3.19.0
ARG K9S=v0.50.15
RUN apk add --no-cache bash curl jq ca-certificates \
 && curl -fsSL "https://dl.k8s.io/release/${KUBECTL}/bin/linux/${TARGETARCH}/kubectl" -o /usr/local/bin/kubectl \
 && curl -fsSL "https://get.helm.sh/helm-${HELM}-linux-${TARGETARCH}.tar.gz" | tar -xz -C /tmp \
 && mv "/tmp/linux-${TARGETARCH}/helm" /usr/local/bin/helm \
 && curl -fsSL "https://github.com/derailed/k9s/releases/download/${K9S}/k9s_Linux_${TARGETARCH}.tar.gz" | tar -xz -C /usr/local/bin k9s \
 && chmod +x /usr/local/bin/kubectl /usr/local/bin/helm /usr/local/bin/k9s \
 && rm -rf /tmp/linux-*
COPY --from=ready /crucible-ready /usr/local/bin/crucible-ready
ENV HOME=/tmp PS1='\[\e[33m\]$CRUCIBLE_CONTEXT\[\e[0m\]:$LAB_NAMESPACE \w \$ '
CMD ["sleep", "infinity"]
```

- [ ] **Step 2: Add the make target**

Append to `Makefile` (and add `toolbox` to `.PHONY`):

```make
TOOLBOX_TAG ?= dev
toolbox:
	docker build -f deploy/toolbox/Dockerfile -t crucible-toolbox:$(TOOLBOX_TAG) .
```

- [ ] **Step 3: Build and smoke-test it**

Run: `make toolbox && docker run --rm crucible-toolbox:dev sh -c 'kubectl version --client && helm version --short && crucible-ready 2>&1 | grep -q usage && echo OK'`
Expected: versions, then `OK`.

- [ ] **Step 4: Commit**

```bash
git add deploy/toolbox/Dockerfile
git commit -m "build: the cluster lab toolbox image (kubectl, helm, k9s, jq, crucible-ready)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- deploy/toolbox/Dockerfile Makefile
```

---

### Task 5: The agent reports its kubeconfig's contexts

**Files:**
- Create: `internal/agent/kubecontexts.go`, `internal/agent/kubecontexts_test.go`
- Modify: `internal/agent/client.go` (hello carries contexts; send `contexts` on change)
- Modify: `internal/agent/client_test.go` (fake executor gains `ProvisionCluster` once Task 7 lands; nothing here)

**Interfaces:**
- Produces: `agent.LoadingRules(explicit string) *clientcmd.ClientConfigLoadingRules`, `agent.ReadContexts(explicit string) []ap.KubeContext`, `agent.Watcher{Kubeconfig string; Every time.Duration}` with `Run(ctx)`, `Current() []ap.KubeContext`, `Changes() <-chan []ap.KubeContext`; `agent.Client.Kube *Watcher` (nil = no cluster labs).

- [ ] **Step 1: Write the failing tests**

```go
package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const twoContexts = `apiVersion: v1
kind: Config
clusters:
  - name: kind
    cluster: {server: "https://127.0.0.1:52011", certificate-authority-data: "c2VjcmV0"}
  - name: eks
    cluster: {server: "https://ABC.gr7.eu-west-1.eks.amazonaws.com"}
contexts:
  - {name: kind-dev, context: {cluster: kind, user: me}}
  - {name: prod-eks, context: {cluster: eks, user: me}}
users:
  - name: me
    user: {token: s3cr3t-token}
`

func TestReadContextsReportsNamesAndHostsOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config")
	_ = os.WriteFile(p, []byte(twoContexts), 0o600)
	got := ReadContexts(p)
	if len(got) != 2 || got[0].Name != "kind-dev" || got[0].Server != "127.0.0.1:52011" || got[1].Server != "ABC.gr7.eu-west-1.eks.amazonaws.com" {
		t.Fatalf("%+v", got)
	}
	if s := strings.ToLower(fmtContexts(got)); strings.Contains(s, "s3cr3t") || strings.Contains(s, "c2vjcmv0") {
		t.Fatal("credentials leaked")
	}
}

func TestReadContextsSurvivesMissingAndBrokenFiles(t *testing.T) {
	dir := t.TempDir()
	if got := ReadContexts(filepath.Join(dir, "nope")); len(got) != 0 {
		t.Fatalf("missing: %+v", got)
	}
	broken := filepath.Join(dir, "broken")
	_ = os.WriteFile(broken, []byte("contexts: [\n"), 0o600)
	if got := ReadContexts(broken); len(got) != 0 {
		t.Fatalf("broken: %+v", got)
	}
}

func TestWatcherSendsChanges(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config")
	_ = os.WriteFile(p, []byte(twoContexts), 0o600)
	w := &Watcher{Kubeconfig: p, Every: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	if got := <-w.Changes(); len(got) != 2 {
		t.Fatalf("first: %+v", got)
	}
	_ = os.WriteFile(p, []byte(strings.Replace(twoContexts, "prod-eks", "staging", 2)), 0o600)
	select {
	case got := <-w.Changes():
		if got[1].Name != "staging" || len(w.Current()) != 2 {
			t.Fatalf("change: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no change sent")
	}
}
```

Add at the bottom of the test file:

```go
func fmtContexts(cs []ap.KubeContext) string {
	var b strings.Builder
	for _, c := range cs {
		b.WriteString(c.Name + " " + c.Server + "\n")
	}
	return b.String()
}
```

(with `ap "crucible/internal/agentproto"` imported.)

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/agent -run 'Contexts|Watcher'`
Expected: FAIL, `undefined: ReadContexts`.

- [ ] **Step 3: Implement `internal/agent/kubecontexts.go`**

```go
package agent

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/tools/clientcmd"

	ap "crucible/internal/agentproto"
)

// LoadingRules finds the kubeconfig as kubectl does: the explicit file, else $KUBECONFIG, else ~/.kube/config.
func LoadingRules(explicit string) *clientcmd.ClientConfigLoadingRules {
	r := clientcmd.NewDefaultClientConfigLoadingRules()
	r.ExplicitPath = explicit
	return r
}

// ReadContexts lists the kubeconfig's contexts, sorted, with their API server's host. A missing or broken file is no
// contexts, never an error: the agent keeps running laptop labs.
func ReadContexts(explicit string) []ap.KubeContext {
	cfg, err := LoadingRules(explicit).Load()
	if err != nil {
		return []ap.KubeContext{}
	}
	out := []ap.KubeContext{}
	for name, c := range cfg.Contexts {
		server := ""
		if cl := cfg.Clusters[c.Cluster]; cl != nil {
			if u, err := url.Parse(cl.Server); err == nil {
				server = u.Host
			}
		}
		out = append(out, ap.KubeContext{Name: name, Server: server})
	}
	slices.SortFunc(out, func(a, b ap.KubeContext) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Watcher re-reads the kubeconfig every Every and sends the contexts on Changes when they differ from the last.
type Watcher struct {
	Kubeconfig string
	Every      time.Duration

	once    sync.Once
	mu      sync.Mutex
	current []ap.KubeContext
	changes chan []ap.KubeContext
}

func (w *Watcher) init() { w.once.Do(func() { w.changes = make(chan []ap.KubeContext, 1) }) }

func (w *Watcher) Changes() <-chan []ap.KubeContext { w.init(); return w.changes }

func (w *Watcher) Current() []ap.KubeContext {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.current)
}

func (w *Watcher) Run(ctx context.Context) {
	w.init()
	first := true
	for {
		got := ReadContexts(w.Kubeconfig)
		w.mu.Lock()
		changed := first || !slices.Equal(got, w.current)
		w.current = got
		w.mu.Unlock()
		if changed {
			select { // keep only the newest list
			case <-w.changes:
			default:
			}
			w.changes <- got
		}
		first = false
		select {
		case <-ctx.Done():
			return
		case <-time.After(w.Every):
		}
	}
}
```

- [ ] **Step 4: Send contexts from the client**

In `internal/agent/client.go`:

1. Add the field to `Client`: `Kube *Watcher // nil: this agent offers no cluster labs`.
2. In `runOnce`, replace `s.send(ctx, ap.Msg{Type: ap.THello, Data: live})` with:

```go
	hello := ap.Msg{Type: ap.THello, Data: live}
	if c.Kube != nil {
		hello.Contexts = c.Kube.Current()
		go func() { // while connected, every change of the kubeconfig's contexts reaches Crucible
			for {
				select {
				case <-ctx.Done():
					return
				case cs := <-c.Kube.Changes():
					s.send(ctx, ap.Msg{Type: ap.TContexts, Contexts: cs})
				}
			}
		}()
	}
	s.send(ctx, hello)
```

(The goroutine ends with `runOnce`'s context; a reconnect starts a new one and the hello carries `Current()`.)

- [ ] **Step 5: Run the agent tests**

Run: `go test ./internal/agent && go vet ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/kubecontexts.go internal/agent/kubecontexts_test.go
git commit -m "feat(agent): report the kubeconfig's contexts (names and hosts, never credentials)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/agent/kubecontexts.go internal/agent/kubecontexts_test.go internal/agent/client.go
```

---

### Task 6: The agent's per-lab Kubernetes API proxy

**Files:**
- Create: `internal/agent/kubeproxy.go`, `internal/agent/kubeproxy_test.go`
- Create: `internal/agent/bind.go`, `internal/agent/bind_test.go`

**Interfaces:**
- Consumes: `agent.LoadingRules` (Task 5).
- Produces: `agent.StartProxy(ctx, kubeconfig, kubeContext, bind string) (*Proxy, error)`; `Proxy{Addr string /* host:port */; Token string}` with `Close() error`; `agent.BindAddr(ctx, goos string, docker func(ctx context.Context, args ...string) ([]byte, error)) (string, error)`.

- [ ] **Step 1: Write the failing proxy tests**

```go
package agent

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// upstream is a fake API server: it records the Authorization it sees, streams a watch, and upgrades /exec.
func upstream(t *testing.T) (*httptest.Server, *string) {
	seen := new(string)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = r.Header.Get("Authorization")
		switch {
		case r.URL.Path == "/version":
			fmt.Fprint(w, `{"major":"1","minor":"34"}`)
		case r.URL.Query().Get("watch") == "true":
			f := w.(http.Flusher)
			for i := 0; i < 2; i++ {
				fmt.Fprintf(w, `{"type":"ADDED","n":%d}`+"\n", i)
				f.Flush()
			}
		case r.URL.Path == "/exec":
			conn, buf, _ := w.(http.Hijacker).Hijack()
			defer conn.Close()
			buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: SPDY/3.1\r\n\r\n")
			buf.Flush()
			line, _ := buf.ReadString('\n')
			buf.WriteString("echo:" + line)
			buf.Flush()
		default:
			fmt.Fprint(w, `{"kind":"PodList"}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

func kubeconfigFor(t *testing.T, srv *httptest.Server, user string) string {
	ca := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	p := filepath.Join(t.TempDir(), "config")
	body := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters: [{name: c, cluster: {server: %q, certificate-authority-data: %s}}]
contexts: [{name: lab, context: {cluster: c, user: u}}]
users: [{name: u, user: %s}]
`, srv.URL, ca, user)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProxyAddsTheRealCredentialsAndNeedsItsToken(t *testing.T) {
	srv, seen := upstream(t)
	p, err := StartProxy(context.Background(), kubeconfigFor(t, srv, "{token: real-upstream-token}"), "lab", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	get := func(path, token string) (*http.Response, string) {
		req, _ := http.NewRequest("GET", "http://"+p.Addr+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res, string(b)
	}
	if res, _ := get("/api/v1/namespaces/x/pods", ""); res.StatusCode != 401 {
		t.Fatalf("no token: %d", res.StatusCode)
	}
	if res, _ := get("/api/v1/namespaces/x/pods", "wrong"); res.StatusCode != 401 {
		t.Fatalf("wrong token: %d", res.StatusCode)
	}
	if res, body := get("/api/v1/namespaces/x/pods", p.Token); res.StatusCode != 200 || !strings.Contains(body, "PodList") || *seen != "Bearer real-upstream-token" {
		t.Fatalf("proxied: %d %s, upstream saw %q", res.StatusCode, body, *seen)
	}
	if _, body := get("/api/v1/pods?watch=true", p.Token); strings.Count(body, "ADDED") != 2 {
		t.Fatalf("watch: %q", body)
	}
	if !strings.HasPrefix(p.Addr, "127.0.0.1:") {
		t.Fatalf("listens on %s", p.Addr)
	}
}

func TestProxyPassesUpgradedStreams(t *testing.T) {
	srv, _ := upstream(t)
	p, err := StartProxy(context.Background(), kubeconfigFor(t, srv, "{token: t}"), "lab", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	c, err := net.Dial("tcp", p.Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "POST /exec HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\nConnection: Upgrade\r\nUpgrade: SPDY/3.1\r\n\r\n", p.Token)
	r := bufio.NewReader(c)
	status, _ := r.ReadString('\n')
	if !strings.Contains(status, "101") {
		t.Fatalf("status %q", status)
	}
	for line, _ := r.ReadString('\n'); line != "\r\n"; line, _ = r.ReadString('\n') {
	}
	fmt.Fprint(c, "hello\n")
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if got, _ := r.ReadString('\n'); got != "echo:hello\n" {
		t.Fatalf("stream: %q", got)
	}
}

func TestProxyProbeNamesTheContextWhenCredentialsFail(t *testing.T) {
	srv, _ := upstream(t)
	cfg := kubeconfigFor(t, srv, "{exec: {apiVersion: client.authentication.k8s.io/v1, command: /nonexistent/aws, args: [eks, get-token], interactiveMode: Never}}")
	_, err := StartProxy(context.Background(), cfg, "lab", "127.0.0.1")
	if err == nil || !strings.Contains(err.Error(), `can't reach lab`) || !strings.Contains(err.Error(), "/nonexistent/aws") {
		t.Fatalf("err = %v", err)
	}
}

func TestProxyClosesAndUnknownContextFails(t *testing.T) {
	srv, _ := upstream(t)
	cfg := kubeconfigFor(t, srv, "{token: t}")
	if _, err := StartProxy(context.Background(), cfg, "nope", "127.0.0.1"); err == nil || !strings.Contains(err.Error(), `"nope" is not in your kubeconfig`) {
		t.Fatalf("unknown context: %v", err)
	}
	p, err := StartProxy(context.Background(), cfg, "lab", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	_ = p.Close()
	if _, err := net.DialTimeout("tcp", p.Addr, time.Second); err == nil {
		t.Fatal("still listening after Close")
	}
}
```

- [ ] **Step 2: Write the failing bind test**

```go
package agent

import (
	"context"
	"errors"
	"testing"
)

func TestBindAddrIsOnlyReachableFromLocalContainers(t *testing.T) {
	docker := func(out map[string]string) func(context.Context, ...string) ([]byte, error) {
		return func(_ context.Context, args ...string) ([]byte, error) {
			if v, ok := out[args[0]]; ok {
				return []byte(v), nil
			}
			return nil, errors.New("no docker")
		}
	}
	for name, c := range map[string]struct {
		goos string
		out  map[string]string
		want string
	}{
		"macOS":          {"darwin", nil, "127.0.0.1"},
		"Windows":        {"windows", nil, "127.0.0.1"},
		"Docker Desktop": {"linux", map[string]string{"info": "Docker Desktop\n"}, "127.0.0.1"},
		"Linux bridge":   {"linux", map[string]string{"info": "Ubuntu 24.04\n", "network": "172.17.0.1\n"}, "172.17.0.1"},
	} {
		got, err := BindAddr(context.Background(), c.goos, docker(c.out))
		if err != nil || got != c.want {
			t.Errorf("%s: %q %v", name, got, err)
		}
	}
	if _, err := BindAddr(context.Background(), "linux", docker(map[string]string{"info": "x", "network": "0.0.0.0"})); err == nil {
		t.Error("0.0.0.0 must be refused")
	}
}
```

- [ ] **Step 3: Run them to see them fail**

Run: `go test ./internal/agent -run 'Proxy|BindAddr'`
Expected: FAIL, `undefined: StartProxy`.

- [ ] **Step 4: Implement `internal/agent/kubeproxy.go`**

```go
package agent

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/apimachinery/pkg/util/proxy"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/transport"
)

// Proxy serves one context of the trainee's kubeconfig to one lab's toolbox, on a local address, behind a random
// token. It authenticates upstream with the kubeconfig itself (exec plugins, OIDC, refresh), so credentials never
// reach the container. Close it when the lab ends.
type Proxy struct {
	Addr  string
	Token string
	srv   *http.Server
}

func (p *Proxy) Close() error { return p.srv.Close() }

func StartProxy(ctx context.Context, kubeconfig, kubeContext, bind string) (*Proxy, error) {
	raw, err := LoadingRules(kubeconfig).Load()
	if err != nil {
		return nil, fmt.Errorf("reading your kubeconfig: %w", err)
	}
	if raw.Contexts[kubeContext] == nil {
		return nil, fmt.Errorf("%q is not in your kubeconfig any more; pick another cluster", kubeContext)
	}
	cfg, err := clientcmd.NewNonInteractiveClientConfig(*raw, kubeContext, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("context %q: %w", kubeContext, err)
	}
	if err := probe(ctx, cfg); err != nil {
		return nil, fmt.Errorf("can't reach %s at %s: %w", kubeContext, cfg.Host, err)
	}
	h, err := apiHandler(cfg)
	if err != nil {
		return nil, err
	}
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	tok := hex.EncodeToString(b)
	ln, err := net.Listen("tcp", net.JoinHostPort(bind, "0"))
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: requireToken(tok, h), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return &Proxy{Addr: ln.Addr().String(), Token: tok, srv: srv}, nil
}

// probe asks the API server for its version, so a cluster that is down or a credential plugin that fails is reported
// at provisioning, naming the context.
func probe(ctx context.Context, cfg *rest.Config) error {
	c := rest.CopyConfig(cfg)
	c.Timeout = 15 * time.Second
	dc, err := discovery.NewDiscoveryClientForConfig(c)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { _, err := dc.ServerVersion(); done <- err }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func requireToken(tok string, next http.Handler) http.Handler {
	want := []byte("Bearer " + tok)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r.Header.Del("Authorization") // the upstream transport adds the real credentials
		next.ServeHTTP(w, r)
	})
}

type responder struct{}

func (responder) Error(w http.ResponseWriter, _ *http.Request, err error) {
	http.Error(w, err.Error(), http.StatusBadGateway)
}

// apiHandler proxies every path to the API server, upgrades (exec, attach, port-forward) and watches included, as
// kubectl proxy does.
func apiHandler(cfg *rest.Config) (http.Handler, error) {
	target, err := url.Parse(cfg.Host)
	if err != nil {
		return nil, err
	}
	if target.Scheme == "" {
		return nil, errors.New("the cluster's server address has no scheme")
	}
	rt, err := rest.TransportFor(cfg)
	if err != nil {
		return nil, err
	}
	tc, err := cfg.TransportConfig()
	if err != nil {
		return nil, err
	}
	tlsCfg, err := transport.TLSConfigFor(tc)
	if err != nil {
		return nil, err
	}
	base := utilnet.SetOldTransportDefaults(&http.Transport{TLSClientConfig: tlsCfg,
		DialContext: (&net.Dialer{Timeout: 30 * time.Second}).DialContext})
	wrap, err := transport.HTTPWrappersForConfig(tc, proxy.MirrorRequest)
	if err != nil {
		return nil, err
	}
	h := proxy.NewUpgradeAwareHandler(target, rt, false, false, responder{})
	h.UpgradeTransport = proxy.NewUpgradeRequestRoundTripper(base, wrap)
	h.UseRequestLocation = true
	h.UseLocationHost = true
	return h, nil
}
```

- [ ] **Step 5: Implement `internal/agent/bind.go`**

```go
package agent

import (
	"context"
	"fmt"
	"net"
	"strings"
)

// BindAddr is where per-lab proxies listen so that only containers on this machine reach them: loopback where Docker
// runs in a VM that forwards host.docker.internal to it (macOS, Windows, Docker Desktop for Linux), else the Docker
// bridge's gateway (what host.docker.internal:host-gateway points to). Never 0.0.0.0.
func BindAddr(ctx context.Context, goos string, docker func(ctx context.Context, args ...string) ([]byte, error)) (string, error) {
	if goos != "linux" {
		return "127.0.0.1", nil
	}
	if info, err := docker(ctx, "info", "--format", "{{.OperatingSystem}}"); err == nil && strings.Contains(string(info), "Docker Desktop") {
		return "127.0.0.1", nil
	}
	out, err := docker(ctx, "network", "inspect", "bridge", "--format", "{{(index .IPAM.Config 0).Gateway}}")
	if err != nil {
		return "", fmt.Errorf("finding Docker's bridge address: %w", err)
	}
	ip := net.ParseIP(strings.TrimSpace(string(out)))
	if ip == nil || ip.IsUnspecified() {
		return "", fmt.Errorf("docker's bridge gateway %q is not a usable address", strings.TrimSpace(string(out)))
	}
	return ip.String(), nil
}
```

- [ ] **Step 6: Run the tests**

Run: `go mod tidy && go test ./internal/agent -run 'Proxy|BindAddr' -v && go vet ./...`
Expected: PASS. (`go mod tidy` only promotes `k8s.io/apimachinery` packages already in the module graph; review `go.mod` changes before committing.)

- [ ] **Step 7: Commit**

```bash
git add internal/agent/kubeproxy.go internal/agent/kubeproxy_test.go internal/agent/bind.go internal/agent/bind_test.go
git commit -m "feat(agent): a local, token-protected Kubernetes API proxy per cluster lab

Credentials stay with the agent (exec plugins and refresh work); the toolbox only gets the per-lab token. Listens where
only local containers reach it.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/agent/kubeproxy.go internal/agent/kubeproxy_test.go internal/agent/bind.go internal/agent/bind_test.go go.mod go.sum
```

---

### Task 7: The agent installs, serves and removes cluster labs

**Files:**
- Create: `internal/agent/cluster.go`, `internal/agent/cluster_test.go`
- Create: `internal/agent/labs.go`
- Modify: `internal/agent/compose.go` (`Executor` gains `ProvisionCluster`; `Compose.Labs` skips cluster labs)
- Modify: `internal/agent/client.go` (`handle` routes a provision with `Cluster` set)
- Modify: `internal/agent/client_test.go` (its fake executor gets a `ProvisionCluster` that records the spec)
- Modify: `cmd/crucible-agent/main.go` (`--kubeconfig`, `Labs`, `Watcher`)

**Interfaces:**
- Consumes: `ap.ClusterSpec` (Task 1), `StartProxy`, `BindAddr` (Task 6), `Watcher`, `LoadingRules` (Task 5), `Untar`, `RunLimited`, `validID`, `docker`, `ptySession` (existing).
- Produces: `Executor.ProvisionCluster(ctx, labID string, bundle []byte, spec ap.ClusterSpec) error`; `agent.Cluster{Dir, Kubeconfig string; Bind func(ctx) (string, error); Run func(ctx context.Context, args ...string) ([]byte, error)}` (Run = `docker` with args; nil = real docker) with `Provision`, `Destroy`, `RunScript`, `StartPTY`, `Labs`; `agent.Labs{Compose Compose; Cluster *Cluster}` implementing `Executor`.

- [ ] **Step 1: Write the failing tests**

```go
package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	ap "crucible/internal/agentproto"
)

// fakeDocker records docker calls and answers helm template with whatever the test set.
type fakeDocker struct {
	mu       sync.Mutex
	calls    []string
	template string
	failOn   string // a substring: the call containing it fails
}

func (f *fakeDocker) run(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := strings.Join(args, " ")
	f.calls = append(f.calls, call)
	if f.failOn != "" && strings.Contains(call, f.failOn) {
		return []byte("Error: context deadline exceeded"), errors.New("exit status 1")
	}
	if strings.Contains(call, "helm template") {
		return []byte(f.template), nil
	}
	return nil, nil
}

func (f *fakeDocker) joined() string { f.mu.Lock(); defer f.mu.Unlock(); return strings.Join(f.calls, "\n") }

func chartBundle(t *testing.T) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{"charts/shop/Chart.yaml": "name: shop\n", "values/shop.yaml": "x: 1\n"} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func testCluster(t *testing.T, f *fakeDocker) *Cluster {
	srv, _ := upstream(t)
	return &Cluster{Dir: t.TempDir(), Kubeconfig: kubeconfigFor(t, srv, "{token: real}"), Run: f.run,
		Bind: func(context.Context) (string, error) { return "127.0.0.1", nil }}
}

var spec = ap.ClusterSpec{Context: "lab", Toolbox: "toolbox@sha256:abc", Charts: []ap.Chart{
	{Name: "shop", Path: "charts/shop", Values: "values/shop.yaml", TimeoutS: 300}, {Name: "db", Path: "charts/shop", TimeoutS: 60}}}

func TestProvisionInstallsInOrderThroughTheToolbox(t *testing.T) {
	f := &fakeDocker{template: "kind: Deployment\n"}
	c := testCluster(t, f)
	id := "0123456789ab"
	if err := c.Provision(context.Background(), id, chartBundle(t), spec); err != nil {
		t.Fatal(err)
	}
	calls := f.joined()
	for _, want := range []string{
		"run -d --name crucible-lab-0123456789ab --label crucible.lab=0123456789ab",
		"-e LAB_NAMESPACE=lab-0123456789ab", "-e LAB_RELEASES=shop db", "--add-host host.docker.internal:host-gateway",
		"toolbox@sha256:abc",
		"exec crucible-lab-0123456789ab sh -c kubectl create namespace lab-0123456789ab",
		"helm upgrade --install shop /tmp/charts/shop -n lab-0123456789ab -f /lab/values/shop.yaml --wait --timeout 300s",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("missing %q in\n%s", want, calls)
		}
	}
	if strings.Index(calls, "helm upgrade --install shop") > strings.Index(calls, "helm upgrade --install db") {
		t.Error("charts install in order")
	}
	kc, err := os.ReadFile(filepath.Join(c.Dir, id, "kubeconfig"))
	if err != nil || strings.Contains(string(kc), "real") || !strings.Contains(string(kc), "http://host.docker.internal:") || !strings.Contains(string(kc), "namespace: lab-0123456789ab") {
		t.Fatalf("toolbox kubeconfig: %s %v", kc, err)
	}
	if st, _ := os.Stat(filepath.Join(c.Dir, id, "kubeconfig")); st.Mode().Perm() != 0o600 {
		t.Fatalf("kubeconfig mode %v", st.Mode())
	}
	if got := c.Labs(); len(got) != 1 || got[0] != id {
		t.Fatalf("Labs %v", got)
	}
}

func TestUndeclaredClusterWideKindsAreRefused(t *testing.T) {
	f := &fakeDocker{template: "---\nkind: ClusterRole\nmetadata: {name: r}\n"}
	err := testCluster(t, f).Provision(context.Background(), "0123456789ab", chartBundle(t), spec)
	if err == nil || !strings.Contains(err.Error(), "creates a ClusterRole") || strings.Contains(f.joined(), "helm upgrade") {
		t.Fatalf("err %v\n%s", err, f.joined())
	}
	wide := spec
	wide.ClusterWide = true
	if err := testCluster(t, &fakeDocker{template: "kind: ClusterRole\n"}).Provision(context.Background(), "0123456789ab", chartBundle(t), wide); err != nil {
		t.Fatalf("declared: %v", err)
	}
}

func TestFailedInstallCleansUp(t *testing.T) {
	f := &fakeDocker{template: "kind: Deployment\n", failOn: "helm upgrade --install db"}
	c := testCluster(t, f)
	err := c.Provision(context.Background(), "0123456789ab", chartBundle(t), spec)
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("err %v", err)
	}
	calls := f.joined()
	for _, want := range []string{"helm uninstall db", "helm uninstall shop", "kubectl delete namespace lab-0123456789ab", "rm -f crucible-lab-0123456789ab"} {
		if !strings.Contains(calls, want) {
			t.Errorf("cleanup missing %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(c.Dir, "0123456789ab")); !os.IsNotExist(err) {
		t.Error("lab folder left behind")
	}
}

func TestTwoClusterLabsAreIndependent(t *testing.T) {
	f := &fakeDocker{template: "kind: Deployment\n"}
	c := testCluster(t, f)
	a, b := "0123456789ab", "ba9876543210"
	for _, id := range []string{a, b} {
		if err := c.Provision(context.Background(), id, chartBundle(t), spec); err != nil {
			t.Fatal(err)
		}
	}
	pb := c.proxy(b)
	if err := c.Destroy(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if c.proxy(a) != nil || c.proxy(b) != pb || pb == nil {
		t.Fatal("destroying one lab touched the other's proxy")
	}
	if strings.Contains(f.joined(), "delete namespace lab-"+b) {
		t.Fatal("the other lab's namespace was deleted")
	}
	if pa, pb2 := readKubeconfig(t, c, a), readKubeconfig(t, c, b); pb2 == "" || pa != "" {
		t.Fatal("lab folders")
	}
}

func TestDestroyAfterRestartReopensTheProxy(t *testing.T) {
	f := &fakeDocker{template: "kind: Deployment\n"}
	c := testCluster(t, f)
	id := "0123456789ab"
	if err := c.Provision(context.Background(), id, chartBundle(t), spec); err != nil {
		t.Fatal(err)
	}
	restarted := &Cluster{Dir: c.Dir, Kubeconfig: c.Kubeconfig, Run: f.run, Bind: c.Bind} // a new process: no proxies
	f.calls = nil
	if err := restarted.Destroy(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	calls := f.joined()
	if !strings.Contains(calls, "helm uninstall shop") || !strings.Contains(calls, "kubectl delete namespace lab-0123456789ab") {
		t.Fatalf("no uninstall after restart:\n%s", calls)
	}
}

func readKubeconfig(t *testing.T, c *Cluster, id string) string {
	b, _ := os.ReadFile(filepath.Join(c.Dir, id, "kubeconfig"))
	return string(b)
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/agent -run 'Provision|ClusterWide|FailedInstall|TwoClusterLabs|AfterRestart'`
Expected: FAIL, `undefined: Cluster`.

- [ ] **Step 3: Implement `internal/agent/cluster.go`**

```go
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"net"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"

	ap "crucible/internal/agentproto"
)

// clusterMarker holds a cluster lab's spec (JSON) in its folder; its presence is what makes the folder a cluster lab.
const clusterMarker = ".crucible-cluster"

var chartName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,51}[a-z0-9])?$`)
var kindLine = regexp.MustCompile(`(?m)^kind:\s*["']?([A-Za-z]+)`)

// Cluster runs cluster labs: a lab's Helm charts on a context of the trainee's kubeconfig, through a per-lab proxy
// and a toolbox container named crucible-lab-<id>. Lab folders live under Dir/<id>, beside compose labs.
type Cluster struct {
	Dir        string
	Kubeconfig string                                                         // explicit kubeconfig; "" = $KUBECONFIG or ~/.kube/config
	Bind       func(ctx context.Context) (string, error)                       // where proxies listen (BindAddr)
	Run        func(ctx context.Context, args ...string) ([]byte, error)       // docker; nil = the real one

	mu      sync.Mutex
	proxies map[string]*Proxy
}

func (c *Cluster) run(ctx context.Context, args ...string) ([]byte, error) {
	if c.Run != nil {
		return c.Run(ctx, args...)
	}
	return docker(ctx, args...).CombinedOutput()
}

func (c *Cluster) proxy(id string) *Proxy {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.proxies[id]
}

func container(id string) string { return "crucible-lab-" + id }
func namespace(id string) string { return "lab-" + id }

func releases(s ap.ClusterSpec) string {
	names := make([]string, len(s.Charts))
	for i, ch := range s.Charts {
		names[i] = ch.Name
	}
	return strings.Join(names, " ")
}

func checkSpec(s ap.ClusterSpec) error {
	if s.Context == "" || s.Toolbox == "" || len(s.Charts) == 0 {
		return errors.New("invalid cluster lab: context, toolbox and charts are required")
	}
	for _, ch := range s.Charts {
		if !chartName.MatchString(ch.Name) || !filepath.IsLocal(filepath.FromSlash(ch.Path)) ||
			(ch.Values != "" && !filepath.IsLocal(filepath.FromSlash(ch.Values))) || ch.TimeoutS <= 0 {
			return fmt.Errorf("invalid chart %q", ch.Name)
		}
	}
	return nil
}

// openProxy starts the lab's proxy and writes the toolbox's kubeconfig: the proxy's address, its token, the lab's
// namespace as the default. Mode 0600: the toolbox runs as this user.
func (c *Cluster) openProxy(ctx context.Context, id string, s ap.ClusterSpec) error {
	bind, err := c.Bind(ctx)
	if err != nil {
		return err
	}
	p, err := StartProxy(ctx, c.Kubeconfig, s.Context, bind)
	if err != nil {
		return err
	}
	_, port, _ := net.SplitHostPort(p.Addr)
	kc := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters: [{name: lab, cluster: {server: "http://host.docker.internal:%s"}}]
users: [{name: lab, user: {token: %q}}]
contexts: [{name: %q, context: {cluster: lab, user: lab, namespace: %s}}]
current-context: %q
`, port, p.Token, s.Context, namespace(id), s.Context)
	if err := os.WriteFile(filepath.Join(c.Dir, id, "kubeconfig"), []byte(kc), 0o600); err != nil {
		_ = p.Close()
		return err
	}
	c.mu.Lock()
	if c.proxies == nil {
		c.proxies = map[string]*Proxy{}
	}
	c.proxies[id] = p
	c.mu.Unlock()
	return nil
}

func (c *Cluster) startToolbox(ctx context.Context, id string, s ap.ClusterSpec) error {
	_, _ = c.run(ctx, "rm", "-f", container(id))
	args := []string{"run", "-d", "--name", container(id), "--label", "crucible.lab=" + id,
		"--add-host", "host.docker.internal:host-gateway", "-v", filepath.Join(c.Dir, id) + ":/lab:ro",
		"-e", "KUBECONFIG=/lab/kubeconfig", "-e", "LAB_NAMESPACE=" + namespace(id), "-e", "LAB_ID=" + id,
		"-e", "LAB_RELEASES=" + releases(s), "-e", "CRUCIBLE_CONTEXT=" + s.Context, "-e", "HOME=/tmp"}
	if u := userFlag(); u != "" {
		args = append(args, "--user", u)
	}
	if out, err := c.run(ctx, append(args, s.Toolbox)...); err != nil {
		return fmt.Errorf("starting the lab toolbox: %w: %s", err, tail(out))
	}
	return nil
}

// userFlag runs the toolbox as this user where Docker maps uids (Linux), so it can read the 0600 kubeconfig.
func userFlag() string {
	if uid, gid := os.Getuid(), os.Getgid(); uid > 0 {
		return fmt.Sprintf("%d:%d", uid, gid)
	}
	return ""
}

func (c *Cluster) inToolbox(ctx context.Context, id, script string) ([]byte, error) {
	return c.run(ctx, "exec", container(id), "sh", "-c", script)
}

func (c *Cluster) Provision(ctx context.Context, id string, bundle []byte, s ap.ClusterSpec) (err error) {
	if err := validID(id); err != nil {
		return err
	}
	if err := checkSpec(s); err != nil {
		return err
	}
	dir := filepath.Join(c.Dir, id)
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := Untar(bundle, dir); err != nil {
		return err
	}
	spec, _ := json.Marshal(s)
	if err := os.WriteFile(filepath.Join(dir, clusterMarker), spec, 0o600); err != nil {
		return err
	}
	defer func() {
		if err != nil { // leave nothing behind: what was installed is uninstalled, the namespace goes
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
			defer cancel()
			_ = c.Destroy(cctx, id)
		}
	}()
	if err := c.openProxy(ctx, id, s); err != nil {
		return err
	}
	if err := c.startToolbox(ctx, id, s); err != nil {
		return err
	}
	ns := namespace(id)
	if out, err := c.inToolbox(ctx, id, "kubectl create namespace "+ns+" && kubectl label namespace "+ns+
		" app.kubernetes.io/managed-by=crucible crucible.lab="+id); err != nil {
		return fmt.Errorf("creating namespace %s: %w: %s", ns, err, tail(out))
	}
	for _, ch := range s.Charts {
		values := ""
		if ch.Values != "" {
			values = " -f /lab/" + ch.Values
		}
		work := "/tmp/charts/" + ch.Name // /lab is read-only; helm dependency build writes into the chart
		out, err := c.inToolbox(ctx, id, "rm -rf "+work+" && mkdir -p /tmp/charts && cp -r /lab/"+ch.Path+" "+work+
			" && helm dependency build "+work+" >/dev/null && helm template "+ch.Name+" "+work+" -n "+ns+values)
		if err != nil {
			return fmt.Errorf("rendering chart %s: %w: %s", ch.Name, err, tail(out))
		}
		if !s.ClusterWide {
			for _, m := range kindLine.FindAllStringSubmatch(string(out), -1) {
				if ap.ClusterScopedKinds[m[1]] {
					return fmt.Errorf("chart %s creates a %s, which is cluster-wide, but this lab doesn't declare cluster_wide; tell the training's editors", ch.Name, m[1])
				}
			}
		}
		if out, err := c.inToolbox(ctx, id, fmt.Sprintf("helm upgrade --install %s %s -n %s%s --wait --timeout %ds",
			ch.Name, work, ns, values, ch.TimeoutS)); err != nil {
			return fmt.Errorf("installing chart %s: %w: %s", ch.Name, err, tail(out))
		}
	}
	return nil
}

// Destroy uninstalls the charts in reverse order, deletes the namespace, then the toolbox, the proxy and the folder.
// After an agent restart there is no proxy or toolbox yet: both come back from the saved spec first.
func (c *Cluster) Destroy(ctx context.Context, id string) error {
	if err := validID(id); err != nil {
		return err
	}
	dir := filepath.Join(c.Dir, id)
	raw, err := os.ReadFile(filepath.Join(dir, clusterMarker))
	if errors.Is(err, fs.ErrNotExist) {
		return os.RemoveAll(dir)
	}
	var s ap.ClusterSpec
	if err != nil || json.Unmarshal(raw, &s) != nil {
		return fmt.Errorf("lab %s: unreadable cluster spec", id)
	}
	if c.proxy(id) == nil {
		if err := c.openProxy(ctx, id, s); err != nil {
			return err // keep the folder: a later destroy retries once the cluster is reachable
		}
	}
	if out, _ := c.run(ctx, "inspect", "-f", "{{.State.Running}}", container(id)); strings.TrimSpace(string(out)) != "true" {
		if err := c.startToolbox(ctx, id, s); err != nil {
			return err
		}
	}
	var script strings.Builder
	for _, ch := range slices.Backward(s.Charts) {
		fmt.Fprintf(&script, "helm uninstall %s -n %s --wait --ignore-not-found; ", ch.Name, namespace(id))
	}
	fmt.Fprintf(&script, "kubectl delete namespace %s --ignore-not-found --wait=true --timeout=5m", namespace(id))
	if out, err := c.inToolbox(ctx, id, script.String()); err != nil {
		return fmt.Errorf("removing lab %s from %s: %w: %s", id, s.Context, err, tail(out))
	}
	_, _ = c.run(ctx, "rm", "-f", container(id))
	c.mu.Lock()
	if p := c.proxies[id]; p != nil {
		_ = p.Close()
		delete(c.proxies, id)
	}
	c.mu.Unlock()
	return os.RemoveAll(dir)
}

func (c *Cluster) Labs() []string {
	entries, _ := os.ReadDir(c.Dir)
	ids := []string{}
	for _, e := range entries {
		if _, err := os.Stat(filepath.Join(c.Dir, e.Name(), clusterMarker)); e.IsDir() && validID(e.Name()) == nil && err == nil {
			ids = append(ids, e.Name())
		}
	}
	return ids
}

func (c *Cluster) RunScript(ctx context.Context, id string, script []byte, env map[string]string, timeout time.Duration) (ap.Msg, error) {
	if err := validID(id); err != nil {
		return ap.Msg{}, err
	}
	args := []string{"exec", "-i"}
	for k, v := range env {
		args = append(args, "-e", k+"="+v)
	}
	cmd := docker(context.Background(), append(args, container(id), "bash", "-s")...)
	cmd.Stdin = bytes.NewReader(script)
	return RunLimited(ctx, cmd, timeout)
}

func (c *Cluster) StartPTY(id string, cols, rows int) (Session, error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	cmd := docker(context.Background(), "exec", "-it", container(id), "bash", "-l")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, err
	}
	return &ptySession{f: f, cmd: cmd}, nil
}
```

- [ ] **Step 4: Route both kinds of lab**

Create `internal/agent/labs.go`:

```go
package agent

import (
	"context"
	"os"
	"path/filepath"
	"time"

	ap "crucible/internal/agentproto"
)

// Labs runs every lab on this laptop: compose labs (runtime local) and cluster labs, told apart by the cluster
// marker in the lab's folder. Both share one labs folder.
type Labs struct {
	Compose Compose
	Cluster *Cluster // nil: no kubeconfig support
}

func (l *Labs) isCluster(id string) bool {
	_, err := os.Stat(filepath.Join(l.Compose.Dir, id, clusterMarker))
	return l.Cluster != nil && err == nil
}

func (l *Labs) Provision(ctx context.Context, id string, bundle []byte, compose string) error {
	return l.Compose.Provision(ctx, id, bundle, compose)
}

func (l *Labs) ProvisionCluster(ctx context.Context, id string, bundle []byte, s ap.ClusterSpec) error {
	if l.Cluster == nil {
		return errNoKube
	}
	return l.Cluster.Provision(ctx, id, bundle, s)
}

func (l *Labs) Destroy(ctx context.Context, id string) error {
	if l.isCluster(id) {
		return l.Cluster.Destroy(ctx, id)
	}
	return l.Compose.Destroy(ctx, id)
}

func (l *Labs) RunScript(ctx context.Context, id, service string, script []byte, env map[string]string, timeout time.Duration) (ap.Msg, error) {
	if l.isCluster(id) {
		return l.Cluster.RunScript(ctx, id, script, env, timeout)
	}
	return l.Compose.RunScript(ctx, id, service, script, env, timeout)
}

func (l *Labs) StartPTY(id, service string, cols, rows int) (Session, error) {
	if l.isCluster(id) {
		return l.Cluster.StartPTY(id, cols, rows)
	}
	return l.Compose.StartPTY(id, service, cols, rows)
}

func (l *Labs) DestroyAll(ctx context.Context) {
	for _, id := range l.Labs() {
		_ = l.Destroy(ctx, id)
	}
}

func (l *Labs) Labs() []string {
	ids := l.Compose.Labs()
	if l.Cluster != nil {
		ids = append(ids, l.Cluster.Labs()...)
	}
	return ids
}
```

In `internal/agent/compose.go`:

1. Add to the `Executor` interface: `ProvisionCluster(ctx context.Context, labID string, bundle []byte, spec ap.ClusterSpec) error`.
2. In `Compose.Labs`, skip folders containing `clusterMarker`: change the condition to `if e.IsDir() && validID(e.Name()) == nil && !exists(filepath.Join(c.Dir, e.Name(), clusterMarker)) {` and add

```go
func exists(p string) bool { _, err := os.Stat(p); return err == nil }

var errNoKube = errors.New("this agent has no kubeconfig for cluster labs; start it with --kubeconfig or set KUBECONFIG")
```

In `internal/agent/client.go`, in `handle`, replace the `ap.TProvision` case with:

```go
	case ap.TProvision:
		if m.Cluster != nil {
			s.reply(ctx, m.ID, ap.Msg{}, s.exec.ProvisionCluster(ctx, m.LabID, m.Data, *m.Cluster))
			return
		}
		s.reply(ctx, m.ID, ap.Msg{}, s.exec.Provision(ctx, m.LabID, m.Data, m.Compose))
```

In `internal/agent/client_test.go`, give the fake executor type the method:

```go
func (f *fakeExec) ProvisionCluster(_ context.Context, labID string, _ []byte, s ap.ClusterSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clusterSpecs = append(f.clusterSpecs, s)
	return nil
}
```

(adding `clusterSpecs []ap.ClusterSpec` to `fakeExec`), and add a test with the file's `startAgent` harness:

```go
func TestClusterProvisionReachesTheClusterExecutor(t *testing.T) {
	fx := &fakeExec{provisioned: map[string]string{}}
	hub, stop := startAgent(t, fx)
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := hub.Call(ctx, 1, ap.Msg{Type: ap.TProvision, LabID: "0123456789ab", Cluster: &ap.ClusterSpec{Context: "kind-dev"}}); err != nil {
		t.Fatal(err)
	}
	fx.mu.Lock()
	defer fx.mu.Unlock()
	if len(fx.clusterSpecs) != 1 || fx.clusterSpecs[0].Context != "kind-dev" || len(fx.provisioned) != 0 {
		t.Fatalf("cluster %+v, compose %v", fx.clusterSpecs, fx.provisioned)
	}
}
```

- [ ] **Step 5: Wire the binary**

In `cmd/crucible-agent/main.go`, add the flag after `labs`:

```go
	kubeconfig := flag.String("kubeconfig", "", "kubeconfig for cluster labs (default: $KUBECONFIG, else ~/.kube/config)")
```

and replace the `c := &agent.Client{…}` line with:

```go
	run := func(ctx context.Context, args ...string) ([]byte, error) { return exec.CommandContext(ctx, "docker", args...).Output() }
	cluster := &agent.Cluster{Dir: *labs, Kubeconfig: *kubeconfig, Bind: func(ctx context.Context) (string, error) {
		return agent.BindAddr(ctx, runtime.GOOS, run)
	}}
	kube := &agent.Watcher{Kubeconfig: *kubeconfig, Every: 5 * time.Second}
	go kube.Run(ctx)
	c := &agent.Client{Server: strings.TrimRight(*server, "/"), Token: *token, Exec: &agent.Labs{Compose: agent.Compose{Dir: *labs}, Cluster: cluster},
		Kube: kube, Log: slog.Default()}
```

Add imports `"runtime"`, `"time"`. Update the package comment: `// Command crucible-agent runs laptop labs (Docker compose) and cluster labs (Helm on a cluster of your kubeconfig).`

- [ ] **Step 6: Run the agent tests**

Run: `go test -race ./internal/agent ./cmd/crucible-agent && go vet ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/agent/cluster.go internal/agent/cluster_test.go internal/agent/labs.go
git commit -m "feat(agent): install, serve and remove cluster labs with Helm in a toolbox

Charts install in order into lab-<id> through the per-lab proxy; undeclared cluster-wide kinds are refused on the
rendered chart; a failed install leaves nothing behind; destroy after a restart reopens the proxy from the saved spec.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/agent/cluster.go internal/agent/cluster_test.go internal/agent/labs.go internal/agent/compose.go internal/agent/client.go internal/agent/client_test.go cmd/crucible-agent/main.go
```

---

### Task 8: The hub keeps each agent's contexts

**Files:**
- Modify: `internal/agenthub/hub.go`, `internal/agenthub/hub_test.go`
- Modify: `internal/httpapi/server.go` (`/api/agent/status`)

**Interfaces:**
- Produces: `(*agenthub.Hub).Contexts(userID int64) []ap.KubeContext` (empty when offline); `/api/agent/status` → `{"online": bool, "contexts": [{name, server}]}`.

- [ ] **Step 1: Write the failing test**

Add to `internal/agenthub/hub_test.go` (its `server` helper serves the hub as user 7; `waitFor` polls):

```go
func TestHubKeepsTheAgentsContexts(t *testing.T) {
	h := New()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, server(t, h), nil)
	if err != nil {
		t.Fatal(err)
	}
	send := func(m ap.Msg) {
		b, _ := json.Marshal(m)
		if err := ws.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatal(err)
		}
	}
	send(ap.Msg{Type: ap.THello, Data: []byte("[]"), Contexts: []ap.KubeContext{{Name: "kind-dev", Server: "127.0.0.1:1"}}})
	waitFor(t, func() bool { return len(h.Contexts(7)) == 1 })
	send(ap.Msg{Type: ap.TContexts, Contexts: []ap.KubeContext{{Name: "a"}, {Name: "b"}}})
	waitFor(t, func() bool { return len(h.Contexts(7)) == 2 })
	_ = ws.CloseNow()
	waitFor(t, func() bool { return len(h.Contexts(7)) == 0 && !h.Online(7) })
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/agenthub -run Contexts`
Expected: FAIL, `h.Contexts undefined`.

- [ ] **Step 3: Implement**

In `internal/agenthub/hub.go`:

1. Add to `conn`: `contexts []ap.KubeContext` (guarded by `c.mu`).
2. In `Serve`'s read loop, replace the `if m.Type == ap.THello { … continue }` block with:

```go
		if m.Type == ap.THello || m.Type == ap.TContexts {
			c.mu.Lock()
			c.contexts = m.Contexts
			c.mu.Unlock()
			var ids []string
			if m.Type == ap.THello && h.OnHello != nil && json.Unmarshal(m.Data, &ids) == nil {
				go h.OnHello(userID, ids) // it may Call this agent, which needs this read loop running
			}
			continue
		}
```

3. Add:

```go
// Contexts are the kubeconfig contexts the user's agent last reported; none when it is offline.
func (h *Hub) Contexts(userID int64) []ap.KubeContext {
	c, err := h.get(userID)
	if err != nil {
		return []ap.KubeContext{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ap.KubeContext{}, c.contexts...)
}
```

In `internal/httpapi/server.go`, replace the `/api/agent/status` handler body with:

```go
			id := auth.UserFrom(r.Context()).ID
			httpx.JSON(w, http.StatusOK, map[string]any{"online": d.Hub.Online(id), "contexts": d.Hub.Contexts(id)})
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/agenthub ./internal/httpapi`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(agenthub): keep each agent's kubeconfig contexts; agent status lists them

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/agenthub/hub.go internal/agenthub/hub_test.go internal/httpapi/server.go
```

---

### Task 9: The server runs `runtime: cluster` through the agent

**Files:**
- Create: `internal/db/migrations/00025_lab_cluster_context.sql`
- Create: `internal/labs/agentcluster.go`, `internal/labs/agentcluster_test.go`
- Modify: `internal/labs/model.go` (`Instance.ClusterContext`)
- Modify: `internal/labs/service.go` (`instCols`, `scanInst`, `insert`, `Start`, `ModuleLab`, `View`)
- Modify: `internal/labs/http.go` (POST start body)
- Modify: `cmd/crucible-api/main.go` (register the runner when `CRUCIBLE_TOOLBOX_IMAGE` is set)

**Interfaces:**
- Consumes: `Hub.Contexts`, `Hub.Online`, `Hub.Call` (Task 8), `content.Lab.Cluster` (Task 2), `labs.Bundle`, `labs.LocalRunner`, `labProvisioner`.
- Produces: `labs.AgentClusterRunner{Hub *agenthub.Hub; Toolbox string}` (Runner + labProvisioner + `CheckContext(userID int64, name string) error` + `Contexts(userID int64) []ap.KubeContext`); `labs.StartOptions{Context string}`; `(*Service).Start(ctx, u, team, training, module string, opt StartOptions)`; `ModuleLab.ClusterWide bool`, `ModuleLab.Contexts []ap.KubeContext`; `View.ClusterContext string`.

- [ ] **Step 1: The migration**

```sql
-- +goose Up
-- The kubeconfig context a cluster lab ran on, as the trainee's agent named it. Shown only; the server never connects.
ALTER TABLE lab_instances ADD COLUMN cluster_context text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE lab_instances DROP COLUMN cluster_context;
```

- [ ] **Step 2: Write the failing runner tests**

```go
package labs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	ap "crucible/internal/agentproto"
	"crucible/internal/content"
)

// A cluster lab's provision carries the charts and values but never checks, setups or the lab file.
func TestClusterProvisionSendsChartsNeverChecks(t *testing.T) {
	tr, probs := content.Load("../../examples/forge-101")
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	lab := tr.Module("03-cluster-heat").Lab
	var sent ap.Msg
	a := AgentClusterRunner{Toolbox: "toolbox@sha256:abc", call: func(_ context.Context, _ int64, m ap.Msg) (ap.Msg, error) { sent = m; return ap.Msg{}, nil }}
	if err := a.ProvisionLab(context.Background(), &Instance{ID: "0123456789ab", UserID: 1, ClusterContext: "kind-dev"}, lab); err != nil {
		t.Fatal(err)
	}
	if sent.Type != ap.TProvision || sent.Cluster == nil || sent.Cluster.Context != "kind-dev" || sent.Cluster.Toolbox != "toolbox@sha256:abc" ||
		sent.Cluster.Charts[0].Name != lab.Cluster.Charts[0].Name || sent.Cluster.Charts[0].TimeoutS != int64(lab.Cluster.Charts[0].Timeout.D().Seconds()) {
		t.Fatalf("message %+v", sent.Cluster)
	}
	names := tarNames(t, sent.Data)
	if !strings.Contains(names, "charts/") {
		t.Fatalf("no charts in %s", names)
	}
	for _, never := range []string{"checks/", "setup/", "tasks/", "lab.yaml"} {
		if strings.Contains(names, never) {
			t.Fatalf("%s reached the agent: %s", never, names)
		}
	}
}

func TestClusterLabNeedsAContextTheAgentReported(t *testing.T) {
	a := AgentClusterRunner{Toolbox: "t", contexts: func(int64) []ap.KubeContext { return []ap.KubeContext{{Name: "kind-dev"}} },
		online: func(int64) bool { return true }}
	if err := a.CheckContext(1, "kind-dev"); err != nil {
		t.Fatal(err)
	}
	if err := a.CheckContext(1, "prod"); err == nil || !strings.Contains(err.Error(), `"prod" is not in your kubeconfig`) {
		t.Fatalf("unknown: %v", err)
	}
	if err := a.CheckContext(1, ""); err == nil || !strings.Contains(err.Error(), "pick a cluster") {
		t.Fatalf("none: %v", err)
	}
	if err := (AgentClusterRunner{}).Available(&Instance{UserID: 1}); err == nil || !strings.Contains(err.Error(), "CRUCIBLE_TOOLBOX_IMAGE") {
		t.Fatalf("no toolbox: %v", err)
	}
	off := AgentClusterRunner{Toolbox: "t", online: func(int64) bool { return false }}
	if err := off.Available(&Instance{UserID: 1}); !errors.Is(err, errAgentOffline) {
		t.Fatalf("offline: %v", err)
	}
}

func tarNames(t *testing.T, b []byte) string {
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return strings.Join(names, " ")
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
}
```

It uses Forge 101's Helm lab from Task 2.

- [ ] **Step 3: Run them to see them fail**

Run: `go test ./internal/labs -run 'ClusterProvision|ClusterLabNeeds'`
Expected: FAIL, `undefined: AgentClusterRunner`.

- [ ] **Step 4: Implement `internal/labs/agentcluster.go`**

```go
package labs

import (
	"context"
	"errors"
	"fmt"

	"crucible/internal/agenthub"
	ap "crucible/internal/agentproto"
	"crucible/internal/apperr"
	"crucible/internal/content"
)

// AgentClusterRunner runs runtime: cluster labs on a cluster of the trainee's choosing through their laptop agent:
// the agent installs the lab's charts with the toolbox image the server pins (spec 2026-10-11). Free and
// self-reported, like laptop labs. Terminals and scripts reach the toolbox through the hub as for laptop labs.
type AgentClusterRunner struct {
	Hub     *agenthub.Hub
	Toolbox string // CRUCIBLE_TOOLBOX_IMAGE; empty: cluster labs are unavailable

	// test seams; nil = the Hub
	call     func(ctx context.Context, userID int64, m ap.Msg) (ap.Msg, error)
	contexts func(userID int64) []ap.KubeContext
	online   func(userID int64) bool
}

var _ Runner = AgentClusterRunner{}
var _ labProvisioner = AgentClusterRunner{}

func (a AgentClusterRunner) local() LocalRunner { return LocalRunner{Hub: a.Hub} }

func (a AgentClusterRunner) Contexts(userID int64) []ap.KubeContext {
	if a.contexts != nil {
		return a.contexts(userID)
	}
	return a.Hub.Contexts(userID)
}

func (a AgentClusterRunner) isOnline(userID int64) bool {
	if a.online != nil {
		return a.online(userID)
	}
	return a.Hub.Online(userID)
}

func (a AgentClusterRunner) Available(inst *Instance) error {
	if a.Toolbox == "" {
		return apperr.Wrap(apperr.Unavailable, "cluster labs are not set up on this server (CRUCIBLE_TOOLBOX_IMAGE)")
	}
	if !a.isOnline(inst.UserID) {
		return errAgentOffline
	}
	if inst.ClusterContext != "" {
		return a.CheckContext(inst.UserID, inst.ClusterContext)
	}
	return nil
}

// CheckContext: the trainee picked a context their agent reported.
func (a AgentClusterRunner) CheckContext(userID int64, name string) error {
	if name == "" {
		return apperr.Wrap(apperr.Invalid, "pick a cluster from your kubeconfig to run this lab on")
	}
	for _, c := range a.Contexts(userID) {
		if c.Name == name {
			return nil
		}
	}
	return apperr.Wrap(apperr.Invalid, fmt.Sprintf("%q is not in your kubeconfig any more; pick another cluster", name))
}

func clusterSpec(lab *content.Lab, kubeContext, toolbox string) ap.ClusterSpec {
	s := ap.ClusterSpec{Context: kubeContext, Toolbox: toolbox, ClusterWide: lab.Cluster.ClusterWide}
	for _, c := range lab.Cluster.Charts {
		s.Charts = append(s.Charts, ap.Chart{Name: c.Name, Path: c.Path, Values: c.Values, TimeoutS: int64(c.Timeout.D().Seconds())})
	}
	return s
}

func (a AgentClusterRunner) ProvisionLab(ctx context.Context, inst *Instance, lab *content.Lab) error {
	if lab.Cluster == nil {
		return errors.New("not a cluster lab")
	}
	bundle, err := Bundle(lab.Dir, "cluster")
	if err != nil {
		return err
	}
	spec := clusterSpec(lab, inst.ClusterContext, a.Toolbox)
	call := a.call
	if call == nil {
		call = a.Hub.Call
	}
	_, err = call(ctx, inst.UserID, ap.Msg{Type: ap.TProvision, LabID: inst.ID, Data: bundle, Cluster: &spec})
	return err
}

func (a AgentClusterRunner) Provision(context.Context, *Instance, []byte, string) error {
	return errors.New("cluster labs provision through ProvisionLab")
}

func (a AgentClusterRunner) Destroy(ctx context.Context, inst *Instance) error { return a.local().Destroy(ctx, inst) }

func (a AgentClusterRunner) RunScript(ctx context.Context, inst *Instance, s ScriptSpec) (ScriptResult, error) {
	return a.local().RunScript(ctx, inst, s)
}

func (a AgentClusterRunner) OpenPTY(ctx context.Context, inst *Instance, service string, cols, rows int) (PTY, error) {
	return a.local().OpenPTY(ctx, inst, service, cols, rows)
}
```

- [ ] **Step 5: Store the context and accept it at start**

1. `internal/labs/model.go`: add `ClusterContext string // runtime cluster: the kubeconfig context it runs on (display only)` to `Instance`.
2. `internal/labs/service.go`:
   - Append `, cluster_context` to `instCols`; in `scanInst` scan `&in.ClusterContext` last; in `insert` add the column and `in.ClusterContext` as the last value (follow the existing `$n` numbering).
   - Add before `Start`:

```go
// StartOptions are the trainee's choices at start: the kubeconfig context for a cluster lab.
type StartOptions struct {
	Context string `json:"context"`
}

type contextRunner interface {
	CheckContext(userID int64, name string) error
	Contexts(userID int64) []ap.KubeContext
}
```

   - Change `Start`'s signature to `Start(ctx context.Context, u *auth.User, team, training, module string, opt StartOptions) (*View, error)`; after `r := s.Runners[m.Lab.Runtime]` / nil check, add:

```go
	cr, isCluster := r.(contextRunner)
	switch {
	case isCluster:
		if err := cr.CheckContext(u.ID, opt.Context); err != nil {
			return nil, err
		}
	case opt.Context != "":
		return nil, apperr.Wrap(apperr.Invalid, "only cluster labs run on a cluster you pick")
	}
```

     and set `ClusterContext: opt.Context` in the `inst := &Instance{…}` literal.
   - In `ModuleLab`, after `out := &ModuleLab{…}`, add:

```go
	if cr, ok := s.Runners[m.Lab.Runtime].(contextRunner); ok {
		out.Contexts = cr.Contexts(u.ID)
		out.ClusterWide = m.Lab.Cluster != nil && m.Lab.Cluster.ClusterWide
	}
```

     and add to the `ModuleLab` struct `ClusterWide bool \`json:"cluster_wide,omitempty"\`` and `Contexts []ap.KubeContext \`json:"contexts,omitempty"\``.
   - In `View` add `ClusterContext string \`json:"cluster_context,omitempty"\`` and set `ClusterContext: inst.ClusterContext` where `view` builds `v`.
   - Import `ap "crucible/internal/agentproto"`.
3. `internal/labs/http.go`: replace the POST `mod` handler with:

```go
	r.Post(mod, func(w http.ResponseWriter, r *http.Request) {
		var opt StartOptions
		if r.ContentLength != 0 {
			if err := httpx.Read(r, &opt); err != nil {
				httpx.Error(w, err)
				return
			}
		}
		v, err := s.Start(r.Context(), user(r), p(r, "team"), p(r, "training"), p(r, "module"), opt)
		reply(w, v, err)
	})
```

4. Update every other caller of `Start` (`grep -rn "\.Start(ctx\|\.Start(context" internal`) to pass `StartOptions{}`.
5. `cmd/crucible-api/main.go`: after `estimators := map[string]labs.Estimator{"local": rates}`, add:

```go
	if img := os.Getenv("CRUCIBLE_TOOLBOX_IMAGE"); img != "" {
		runners["cluster"], estimators["cluster"] = labs.AgentClusterRunner{Hub: hub, Toolbox: img}, labs.FixedRates{} // free: the trainee's cluster
		slog.Info("cluster labs enabled (on trainees' own clusters)", "toolbox", img)
	}
```

(The old `CRUCIBLE_CLUSTER_LABS` block still registers `runners["cluster"]` after this; Task 11 removes it. Until then, do not set both.)

- [ ] **Step 6: A start test**

Add to `internal/labs/cluster_test.go` (it has `completeFirstLab`, which unlocks module 03):

```go
func TestClusterLabStartsOnAPickedContextForFree(t *testing.T) {
	f := setup(t, true)
	f.completeFirstLab(t)
	ctx := context.Background()
	contexts := []ap.KubeContext{{Name: "kind-dev"}}
	f.s.Runners["cluster"] = AgentClusterRunner{Toolbox: "t", contexts: func(int64) []ap.KubeContext { return contexts },
		online: func(int64) bool { return true }, call: func(context.Context, int64, ap.Msg) (ap.Msg, error) { return ap.Msg{}, nil }}
	f.s.Estimators["cluster"] = FixedRates{}
	if _, err := f.s.Start(ctx, f.u, "forge", "forge-101", "03-cluster-heat", StartOptions{}); err == nil || !strings.Contains(err.Error(), "pick a cluster") {
		t.Fatalf("no context: %v", err)
	}
	v, err := f.s.Start(ctx, f.u, "forge", "forge-101", "03-cluster-heat", StartOptions{Context: "kind-dev"})
	if err != nil || v.ClusterContext != "kind-dev" || v.EstimateUSD != 0 || v.Tier != "auto" {
		t.Fatalf("start: %+v %v", v, err)
	}
	if _, err := f.s.Start(ctx, f.u, "forge", "forge-101", "02-first-lab", StartOptions{Context: "kind-dev"}); err == nil {
		t.Fatal("a laptop lab must refuse a context")
	}
}
```

- [ ] **Step 7: Run the server tests**

Run: `go vet ./... && go test -race ./internal/labs ./internal/httpapi ./cmd/crucible-api ./internal/db`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/db/migrations/00025_lab_cluster_context.sql internal/labs/agentcluster.go internal/labs/agentcluster_test.go
git commit -m "feat(labs): runtime cluster runs through the trainee's agent on a context they pick, for free

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/db/migrations/00025_lab_cluster_context.sql internal/labs cmd/crucible-api/main.go
```

---

### Task 10: Ready checks, release health, and self-reported results

**Files:**
- Modify: `internal/labs/service.go` (`runScript` body, `Check`, `provision`, `view`, `ReconcileAgent`, self-report flags)
- Modify: `internal/labs/review.go` (self-report for cluster)
- Test: `internal/labs/agentcluster_test.go`, `internal/labs/cluster_test.go`

**Interfaces:**
- Consumes: `content.Script.Ready` (Task 2), `ready.Report` (Task 3).
- Produces: `selfReported(runtime string) bool`; `scriptBody(lab *content.Lab, sc *content.Script) ([]byte, error)`; `View.Health *ready.Report`; lab event kind `health` (detail = the JSON report).

- [ ] **Step 1: Write the failing tests**

Add to `internal/labs/agentcluster_test.go`:

```go
func TestReadyCheckRunsCrucibleReady(t *testing.T) {
	b, err := scriptBody(&content.Lab{}, &content.Script{Ready: []string{"shop", "db"}})
	if err != nil || string(b) != "exec crucible-ready shop db\n" {
		t.Fatalf("%q %v", b, err)
	}
	if b, _ := scriptBody(&content.Lab{}, &content.Script{Ready: []string{"all"}}); string(b) != "exec crucible-ready all\n" {
		t.Fatalf("all: %q", b)
	}
	if !selfReported("cluster") || !selfReported("local") || selfReported("aws") {
		t.Fatal("cluster and laptop labs are self-reported")
	}
}
```

Add to `internal/labs/cluster_test.go`, with a fake call that answers the health run:

```go
func TestClusterLabHealthAfterProvisionAndChecks(t *testing.T) {
	f := setup(t, true)
	f.completeFirstLab(t)
	ctx := context.Background()
	var scripts []string
	f.s.Runners["cluster"] = AgentClusterRunner{Toolbox: "t", contexts: func(int64) []ap.KubeContext { return []ap.KubeContext{{Name: "k"}} },
		online: func(int64) bool { return true },
		call: func(_ context.Context, _ int64, m ap.Msg) (ap.Msg, error) {
			if m.Type == ap.TRunScript {
				scripts = append(scripts, string(m.Data))
				if strings.Contains(string(m.Data), "--json") {
					return ap.Msg{Data: []byte(`{"releases":[{"name":"forge","status":"progressing","unready":["deployment/forge: 1 of 2 ready"]}]}`)}, nil
				}
			}
			return ap.Msg{}, nil
		}}
	f.s.Estimators["cluster"] = FixedRates{}
	v, err := f.s.Start(ctx, f.u, "forge", "forge-101", "03-cluster-heat", StartOptions{Context: "k"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200 && v.State != Ready; i++ {
		time.Sleep(10 * time.Millisecond)
		if v, err = f.s.Get(ctx, f.u, v.ID); err != nil {
			t.Fatal(err)
		}
	}
	if v.Health == nil || v.Health.Releases[0].Status != "progressing" || !v.SelfReported {
		t.Fatalf("health after provision: %+v self-reported %v", v.Health, v.SelfReported)
	}
	if !slices.ContainsFunc(scripts, func(s string) bool { return strings.Contains(s, "crucible-ready --json all") }) {
		t.Fatalf("no health run: %q", scripts)
	}
}
```

Route `RunScript` through the seam: in `AgentClusterRunner.RunScript`, when `a.call != nil`, build the same message `LocalRunner.RunScript` does and send it through `a.call` (so the test sees it):

```go
func (a AgentClusterRunner) RunScript(ctx context.Context, inst *Instance, s ScriptSpec) (ScriptResult, error) {
	if a.call == nil {
		return a.local().RunScript(ctx, inst, s)
	}
	res, err := a.call(ctx, inst.UserID, ap.Msg{Type: ap.TRunScript, LabID: inst.ID, Service: s.Service, Data: s.Script, Env: s.Env, TimeoutMS: s.Timeout.Milliseconds()})
	return ScriptResult{ExitCode: res.ExitCode, Output: cleanText(string(res.Data)), TimedOut: res.TimedOut}, err
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/labs -run 'ReadyCheck|ClusterLabHealth'`
Expected: FAIL, `undefined: scriptBody`.

- [ ] **Step 3: Implement**

In `internal/labs/service.go`:

1. Add:

```go
// selfReported: results come from the trainee's machine (laptop labs, and cluster labs through the agent).
func selfReported(runtime string) bool { return runtime == "local" || runtime == "cluster" }

// scriptBody is what a script step runs: a ready: check runs crucible-ready in the toolbox, anything else its file.
func scriptBody(lab *content.Lab, sc *content.Script) ([]byte, error) {
	if len(sc.Ready) > 0 {
		return []byte("exec crucible-ready " + strings.Join(sc.Ready, " ") + "\n"), nil
	}
	return os.ReadFile(filepath.Join(lab.Dir, sc.Script))
}

// recordHealth runs crucible-ready over every release and keeps the report as the lab's latest health event.
func (s *Service) recordHealth(ctx context.Context, inst *Instance) {
	r, err := s.runner(inst.Runtime)
	if err != nil {
		return
	}
	res, err := r.RunScript(ctx, inst, ScriptSpec{Service: content.ToolboxService, Script: []byte("crucible-ready --json all\n"), Timeout: 30 * time.Second})
	var rep ready.Report
	if err != nil || json.Unmarshal([]byte(res.Output), &rep) != nil {
		return
	}
	b, _ := json.Marshal(rep)
	s.event(ctx, inst.ID, "health", string(b))
}

func (s *Service) health(ctx context.Context, labID string) *ready.Report {
	var detail string
	if err := s.DB.QueryRow(ctx, `SELECT detail FROM lab_events WHERE lab_id = $1 AND kind = 'health' ORDER BY id DESC LIMIT 1`, labID).Scan(&detail); err != nil {
		return nil
	}
	var rep ready.Report
	if json.Unmarshal([]byte(detail), &rep) != nil {
		return nil
	}
	return &rep
}
```

2. In `runScript`, replace `body, err := os.ReadFile(filepath.Join(lab.Dir, sc.Script))` with `body, err := scriptBody(lab, sc)`.
3. Replace each `inst.Runtime == "local"` used as a self-report flag (`view`'s `SelfReported`, `check_runs` insert in `Check`) with `selfReported(inst.Runtime)`; in `review.go` lines with `inst.Runtime != "local"` use `!selfReported(inst.Runtime)` and `SelfReported: selfReported(inst.Runtime)`; change the review prompt line to `"Results the laptop agent reported (self-reported, " + inst.Runtime + " runtime):\n"`.
4. In `provision`, after a successful setup (just before the "Apply and setup may have used up ctx" comment), add `if inst.Runtime == "cluster" { s.recordHealth(ctx, inst) }`.
5. In `Check`, after the `check_runs` insert, add `if inst.Runtime == "cluster" { s.recordHealth(ctx, inst) }`.
6. In `view`, add `if inst.Runtime == "cluster" && inst.State == Ready { v.Health = s.health(ctx, inst.ID) }` and to `View`: `Health *ready.Report \`json:"health,omitempty"\``.
7. In `ReconcileAgent`, change `runtime = 'local'` to `runtime IN ('local', 'cluster')`, and keep each lost row's runtime: for live ids with no active row, look up the runtime instead of assuming local:

```go
	for id := range live {
		if !active[id] {
			runtime := "local"
			_ = s.DB.QueryRow(ctx, `SELECT runtime FROM lab_instances WHERE id = $1`, id).Scan(&runtime)
			dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
			if err := s.destroyRuntime(dctx, &Instance{ID: id, UserID: userID, Runtime: runtime}); err != nil {
				s.Log.Warn("removing a stale lab from the laptop failed", "lab", id, "err", err)
			}
			cancel()
		}
	}
```

Imports: `"encoding/json"`, `"crucible/internal/ready"`.

- [ ] **Step 4: Run the tests**

Run: `go vet ./... && go test -race ./internal/labs ./internal/scoring`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(labs): ready checks, release health after provision and checks; cluster labs are self-reported

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/labs/service.go internal/labs/review.go internal/labs/agentcluster.go internal/labs/agentcluster_test.go internal/labs/cluster_test.go
```

---

### Task 11: Retire Crucible-run cluster labs and the cluster rate

**Files:**
- Create: `internal/db/migrations/00026_retire_crucible_cluster_labs.sql`
- Modify: `internal/config/config.go`, `internal/config/config_test.go` (drop `ClusterUSDPerHour`)
- Modify: `internal/org/settings.go`, `internal/org/api.go`, `internal/org/seed.go`, `internal/org/org.go`, their tests (drop the field and column)
- Modify: `internal/labs/model.go` (delete `PlatformRate`), its tests
- Modify: `internal/labs/service.go` (`Service.Kube *ClusterRunner`; `reconcileCluster` uses it)
- Modify: `cmd/crucible-api/main.go` (Crucible's cluster only for AWS labs: `CRUCIBLE_LAB_CLUSTER`)
- Modify: `deploy/helm/crucible/templates/crucible.yaml`, `deploy/helm/crucible/values.yaml`, `deploy/helm/test.sh`, `deploy/compose/cluster.yml`
- Modify: `web/src/pages/AdminSettings.tsx`, `web/src/pages/AdminSettings.test.tsx`, `web/src/types.ts`
- Modify: `docs/user/admins/settings-and-registry.md`

**Interfaces:**
- Produces: `labs.Service.Kube *ClusterRunner` (Crucible's own cluster; nil when neither AWS labs nor the legacy cleanup need it); env `CRUCIBLE_LAB_CLUSTER=1` (old `CRUCIBLE_CLUSTER_LABS=1` accepted with a warning).

- [ ] **Step 1: The migration**

```sql
-- +goose Up
-- Cluster labs moved to trainees' own clusters (spec 2026-10-11): labs still running on Crucible's cluster end here;
-- reconcileCluster then deletes their lab-<id> namespaces. The rate card for them goes.
UPDATE lab_instances SET state = 'destroyed', end_reason = 'cluster labs moved to your own cluster', destroyed_at = now()
  WHERE runtime = 'cluster' AND state IN ('pending_approval', 'provisioning', 'ready', 'destroying');
ALTER TABLE settings DROP COLUMN cluster_usd_per_hour;

-- +goose Down
ALTER TABLE settings ADD COLUMN cluster_usd_per_hour numeric;
```

(Check `lab_instances` has `destroyed_at`; `failProvision` above writes it.)

- [ ] **Step 2: Write the failing tests**

- In `internal/config/config_test.go`, add a case to `TestLoadRejectsBadConfig`: `"retired cluster rate": {"platform.yaml", "default_theme: forge\ncost_tiers: {tier1_usd: 5, tier2_usd: 25}\ncluster_usd_per_hour: 1\n", "cluster_usd_per_hour"}` (strict YAML refuses the unknown key once the field is gone). Delete `TestClusterRateFromConfig`.
- In `internal/org/settings_test.go`, delete assertions on `ClusterUSDPerHour`; add `TestSettingsHaveNoClusterRate`: `PUT /api/admin/settings` with `"cluster_usd_per_hour": 1` returns 400 naming the unknown field.
- In `internal/db`, add to `m23_test.go`'s file a new test `TestMigration00026EndsCrucibleClusterLabs`: down to 25, insert a `runtime='cluster', state='ready'` lab and a `runtime='local', state='ready'` lab (copy the insert columns from an existing labs test fixture), up to 26, assert the cluster lab is `destroyed` with the end reason and the local one untouched, and that `settings` has no `cluster_usd_per_hour` column (`information_schema.columns`).
- In `internal/labs/model_test.go`, delete the `PlatformRate` tests; in `internal/labs/cluster_test.go`, delete
  `TestClusterLabAboveAutoApproveNeedsApproval` (cluster labs are free now; `TestClusterLabStartsOnAPickedContextForFree` pins that).
- In `internal/org/org_test.go`, rename `TestSettingsTiersAndFreeClusterRate` to `TestSettingsTiers` and drop its cluster-rate assertions.

Run: `go test ./internal/config ./internal/org ./internal/db ./internal/labs`
Expected: FAIL (field still exists; migration missing).

- [ ] **Step 3: Remove the field everywhere**

1. `internal/config/config.go`: delete the `ClusterUSDPerHour` field from `Settings` and its validation in `Load`.
2. `internal/org/settings.go`: delete `ClusterUSDPerHour` from `SettingsBody`, its validation, its audit key, and the `cluster_usd_per_hour = $5` assignment in the `UPDATE` (renumber the following placeholders and arguments).
3. `internal/org/org.go` `settings()`: drop `cluster_usd_per_hour::float8` from the `SELECT`, the `cluster` variable, its check and `st.ClusterUSDPerHour = cluster`.
4. `internal/org/api.go` `getSettings` and `internal/org/seed.go`: drop `ClusterUSDPerHour: st.ClusterUSDPerHour,`.
5. `internal/labs/model.go`: delete `PlatformRate` and its doc comment; fix the `Estimator` comment to: `// Estimator prices one hour of a lab for approval routing and budgets (spec §9.1). Laptop and cluster labs are free; aws labs are priced by infracost on the module.`
6. `web/src/types.ts`: remove `cluster_usd_per_hour: number | null;` from the settings type. `web/src/pages/AdminSettings.tsx`: remove the `rate` state, its `<label>` (Cluster lab rate…) and `cluster_usd_per_hour` from the save body. `web/src/pages/AdminSettings.test.tsx`: drop the field from fixtures.
7. `docs/user/admins/settings-and-registry.md`: delete the sentences about the cluster lab rate ("Also here: the **cluster lab rate** per hour … no cost.") and keep "the **hours before a waiting request escalates**".

- [ ] **Step 4: Crucible's cluster only for AWS labs**

In `internal/labs/service.go`:

1. Add to `Service`: `Kube *ClusterRunner // Crucible's own cluster: aws lab workspaces, and the namespaces of retired cluster labs`.
2. In `reconcileCluster`, replace `cr, ok := s.Runners["cluster"].(*ClusterRunner); if !ok { return }` with `cr := s.Kube; if cr == nil { return }`.

In `cmd/crucible-api/main.go`, replace the `if os.Getenv("CRUCIBLE_CLUSTER_LABS") == "1" { … }` block and the AWS `cr, ok := runners["cluster"].(*labs.ClusterRunner)` lookup with:

```go
	var kube *labs.ClusterRunner
	legacy := os.Getenv("CRUCIBLE_CLUSTER_LABS") == "1"
	if legacy {
		slog.Warn("CRUCIBLE_CLUSTER_LABS is retired: cluster labs run on trainees' own clusters (CRUCIBLE_TOOLBOX_IMAGE). Crucible's cluster is used for AWS labs (CRUCIBLE_LAB_CLUSTER=1) and to clean up old cluster labs")
	}
	if os.Getenv("CRUCIBLE_LAB_CLUSTER") == "1" || legacy || os.Getenv("CRUCIBLE_AWS_LABS") != "" {
		// KUBECONFIG when set (dev), else the pod's service account (in-cluster).
		cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(),
			&clientcmd.ConfigOverrides{}).ClientConfig()
		if err != nil {
			return fmt.Errorf("lab cluster: %w", err)
		}
		privileged := os.Getenv("CRUCIBLE_CLUSTER_PRIVILEGED") == "1"
		if privileged {
			slog.Warn("CRUCIBLE_CLUSTER_PRIVILEGED=1: AWS lab workspaces run as privileged pods without sysbox. Use this on development clusters only")
		}
		if kube, err = labs.NewClusterRunner(cfg, privileged); err != nil {
			return fmt.Errorf("lab cluster: %w", err)
		}
	}
```

and in the AWS case use `ar := &labs.AWSRunner{Cluster: kube, …}` (it is non-nil there by the condition above). Pass `Kube: kube` in the `labs.Service{…}` literal.

Search for other uses: `grep -rn 'Runners\["cluster"\]' internal cmd` must only show test seams from Tasks 9–10.

- [ ] **Step 5: Deployment files**

1. `deploy/helm/crucible/templates/crucible.yaml`: replace `- { name: CRUCIBLE_CLUSTER_LABS, value: "1" }` with `- { name: CRUCIBLE_LAB_CLUSTER, value: "1" }`, and add next to the other env entries:

```yaml
            {{- with .Values.toolboxImage }}
            - { name: CRUCIBLE_TOOLBOX_IMAGE, value: {{ . | quote }} }
            {{- end }}
```

2. `deploy/helm/crucible/values.yaml`: add `toolboxImage: ""  # cluster labs: the toolbox image by digest (deploy/toolbox); empty disables them`.
3. `deploy/helm/test.sh`: replace `need 'name: CRUCIBLE_CLUSTER_LABS'` with `need 'name: CRUCIBLE_LAB_CLUSTER'`, replace `CRUCIBLE_CLUSTER_LABS` in the `clusterLabs.enabled=false` grep with `CRUCIBLE_LAB_CLUSTER`, and add a render with `--set toolboxImage=x@sha256:y` that must contain `name: CRUCIBLE_TOOLBOX_IMAGE`.
4. `deploy/compose/cluster.yml`: replace `CRUCIBLE_CLUSTER_LABS: "1"` with `CRUCIBLE_LAB_CLUSTER: "1"` and add `CRUCIBLE_TOOLBOX_IMAGE: crucible-toolbox:e2e`.

- [ ] **Step 6: Run everything this touches**

Run:

```bash
go vet ./... && go vet -tags cluster ./internal/labs && go test -race ./... \
  && bash deploy/helm/test.sh \
  && (cd web && npm test && npx tsc -b && npm run lint && npm run build)
```

Expected: PASS. (`go test ./internal/docs` is part of `./...`.)

- [ ] **Step 7: Commit**

```bash
git add internal/db/migrations/00026_retire_crucible_cluster_labs.sql
git commit -m "feat!: Crucible no longer runs or prices cluster labs; its cluster serves AWS labs

Migration 00026 ends cluster labs still running on Crucible's cluster (reconcile removes their namespaces) and drops
cluster_usd_per_hour. CRUCIBLE_LAB_CLUSTER replaces CRUCIBLE_CLUSTER_LABS (accepted with a warning).

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/db/migrations/00026_retire_crucible_cluster_labs.sql internal cmd deploy web docs/user/admins/settings-and-registry.md
```

---

### Task 12: The lab page: pick a cluster, consent, and release health

**Files:**
- Create: `web/src/lib/cluster.ts`, `web/src/lib/cluster.test.ts`
- Create: `web/src/components/ClusterPick.tsx`, `web/src/components/ReleaseHealth.tsx`, `web/src/components/ClusterPick.test.tsx`
- Modify: `web/src/types.ts` (`ModuleLab.contexts`, `ModuleLab.cluster_wide`, `LabView.cluster_context`, `LabView.health`, `KubeContext`, `Health`)
- Modify: `web/src/pages/Lab.tsx` (start panel and running lab)
- Modify: `web/src/pages/Connect.tsx` (contexts the agent sees)
- Modify: `web/src/theme/app.css`
- Modify: `.claude/skills/crucible-ui/SKILL.md`

**Interfaces:**
- Consumes: `ModuleLab.contexts`, `cluster_wide` (Task 9), `LabView.health` (Task 10), `/api/agent/status` contexts (Task 8).
- Produces: `rememberContext(name)`, `pickContext(contexts, remembered)`, `<ClusterPick>`, `<ReleaseHealth>`.

- [ ] **Step 1: Write the failing tests**

`web/src/lib/cluster.test.ts`:

```ts
import { describe, expect, test } from 'vitest'
import { pickContext } from './cluster'

describe('picking a cluster', () => {
  const cs = [{ name: 'kind-dev', server: '127.0.0.1:1' }, { name: 'prod-eks', server: 'x.eks.amazonaws.com' }]
  test('the last choice when it is still there, else nothing chosen', () => {
    expect(pickContext(cs, 'prod-eks')).toBe('prod-eks')
    expect(pickContext(cs, 'gone')).toBe('')
    expect(pickContext([cs[0]], null)).toBe('kind-dev') // one context: take it
    expect(pickContext(cs, null)).toBe('')
  })
})
```

`web/src/components/ClusterPick.test.tsx`:

```tsx
import { describe, expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { ClusterPick } from './ClusterPick'
import { ReleaseHealth } from './ReleaseHealth'

describe('cluster labs', () => {
  test('the picker names each context with its server, and a cluster-wide lab asks for consent', () => {
    const h = renderToStaticMarkup(<ClusterPick contexts={[{ name: 'kind-dev', server: '127.0.0.1:52011' }]} value="kind-dev" onChange={() => {}}
      clusterWide consent={false} onConsent={() => {}} />)
    expect(h).toContain('<option value="kind-dev" selected="">kind-dev · 127.0.0.1:52011</option>')
    expect(h).toContain('cluster-wide resources')
    expect(h).toContain('type="checkbox"')
  })
  test('no contexts says how to get one', () => {
    const h = renderToStaticMarkup(<ClusterPick contexts={[]} value="" onChange={() => {}} clusterWide={false} consent onConsent={() => {}} />)
    expect(h).toContain('kind create cluster')
  })
  test('health lists each release and what is not ready, in words', () => {
    const h = renderToStaticMarkup(<ReleaseHealth health={{ releases: [{ name: 'forge', status: 'progressing', unready: ['deployment/forge: 1 of 2 ready'] }] }} />)
    expect(h).toContain('forge')
    expect(h).toContain('Starting')
    expect(h).toContain('deployment/forge: 1 of 2 ready')
  })
})
```

Run: `cd web && npx vitest run src/lib/cluster.test.ts src/components/ClusterPick.test.tsx`
Expected: FAIL (modules missing).

- [ ] **Step 2: Types**

In `web/src/types.ts` add:

```ts
export type KubeContext = { name: string; server: string }
export type ReleaseStatus = 'healthy' | 'progressing' | 'failing'
export type Health = { releases: { name: string; status: ReleaseStatus; unready: string[] }[] }
```

Add `contexts?: KubeContext[]; cluster_wide?: boolean` to `ModuleLab`, and `cluster_context?: string; health?: Health` to `LabView`.

- [ ] **Step 3: Implement the helpers and components**

`web/src/lib/cluster.ts`:

```ts
import type { KubeContext } from '../types'

const KEY = 'crucible.kubeContext'

// rememberContext keeps the last cluster picked, in this browser only (a convenience; the agent's list is the truth).
export function rememberContext(name: string) {
  try { localStorage.setItem(KEY, name) } catch { /* private window */ }
}

export function rememberedContext(): string | null {
  try { return localStorage.getItem(KEY) } catch { return null }
}

// pickContext preselects the remembered context if the agent still reports it, or the only one there is.
export function pickContext(contexts: KubeContext[], remembered: string | null): string {
  if (remembered && contexts.some((c) => c.name === remembered)) return remembered
  return contexts.length === 1 ? contexts[0].name : ''
}
```

`web/src/components/ClusterPick.tsx`:

```tsx
import type { KubeContext } from '../types'

// ClusterPick: which context of the trainee's kubeconfig the lab runs on, and consent for a cluster-wide lab.
export function ClusterPick({ contexts, value, onChange, clusterWide, consent, onConsent }: {
  contexts: KubeContext[]; value: string; onChange: (name: string) => void; clusterWide: boolean; consent: boolean; onConsent: (ok: boolean) => void
}) {
  if (contexts.length === 0) {
    return (
      <p className="muted cluster-pick">
        Your laptop agent doesn&apos;t see a cluster. Make one with <code>kind create cluster</code>, or point the agent at
        your kubeconfig with <code>--kubeconfig</code>, and it shows up here.
      </p>
    )
  }
  return (
    <div className="stack cluster-pick">
      <label>Run it on
        <select value={value} onChange={(e) => onChange(e.target.value)}>
          <option value="">Pick a cluster…</option>
          {contexts.map((c) => <option key={c.name} value={c.name}>{c.name} · {c.server || 'no server'}</option>)}
        </select>
      </label>
      <p className="muted">The lab installs into its own namespace there and is removed when it ends.</p>
      {clusterWide && (
        <label className="check">
          <input type="checkbox" checked={consent} onChange={(e) => onConsent(e.target.checked)} />
          This lab creates cluster-wide resources (like CRDs) that may stay on the cluster after it ends.
        </label>
      )}
    </div>
  )
}
```

`web/src/components/ReleaseHealth.tsx`:

```tsx
import type { Health, ReleaseStatus } from '../types'

const words: Record<ReleaseStatus, string> = { healthy: 'Ready', progressing: 'Starting', failing: 'Failing' }

// ReleaseHealth: each Helm release of a cluster lab and what is not ready yet, as of the last check.
export function ReleaseHealth({ health }: { health: Health }) {
  return (
    <section className="panel release-health" aria-labelledby="releases">
      <h5 id="releases">On your cluster</h5>
      <ul className="facts-list">
        {health.releases.map((r) => (
          <li key={r.name}>
            <strong>{r.name}</strong> <span className={`badge ${r.status === 'healthy' ? 'complete' : r.status === 'failing' ? 'warn' : 'in_progress'}`}>{words[r.status]}</span>
            {r.unready.length > 0 && <ul className="muted">{r.unready.map((u) => <li key={u}><code>{u}</code></li>)}</ul>}
          </li>
        ))}
      </ul>
    </section>
  )
}
```

`web/src/theme/app.css`, next to the lab styles:

```css
/* A cluster lab's start panel: the context picker and the cluster-wide consent. */
.cluster-pick { margin: 0.75rem 0; max-width: 32rem; }
/* A cluster lab's releases and what is not ready yet. */
.release-health ul ul { margin: 0.25rem 0 0.5rem 1rem; }
.release-health > ul { list-style: none; padding: 0; margin: 0; }
```

- [ ] **Step 4: Use them on the lab page**

In `web/src/pages/Lab.tsx`:

1. Imports: `import { ClusterPick } from '../components/ClusterPick'`, `import { ReleaseHealth } from '../components/ReleaseHealth'`, `import { pickContext, rememberContext, rememberedContext } from '../lib/cluster'`.
2. In the component that renders the start button (where `info.runtime_ready` is used), add state:

```tsx
  const [kubeContext, setKubeContext] = useState(() => pickContext(info.contexts ?? [], rememberedContext()))
  const [consent, setConsent] = useState(false)
  const cluster = info.runtime === 'cluster'
  const clusterReady = !cluster || (kubeContext !== '' && (!info.cluster_wide || consent))
```

3. Render `{cluster && <ClusterPick contexts={info.contexts ?? []} value={kubeContext} onChange={setKubeContext} clusterWide={!!info.cluster_wide} consent={consent} onConsent={setConsent} />}` just above the start button, add `|| !clusterReady` to the button's `disabled`, and change the start call `api<LabView>(modPath, { method: 'POST' })` to:

```tsx
      if (cluster) rememberContext(kubeContext)
      setLab(await api<LabView>(modPath, { method: 'POST', json: cluster ? { context: kubeContext } : {} }))
```

4. In the running-lab view, render `{lab.health && <ReleaseHealth health={lab.health} />}` above the task list, and show the context in the lab header next to the runtime: `{lab.cluster_context && <span className="muted"> · on {lab.cluster_context}</span>}`.

In `web/src/pages/Connect.tsx`: change the status type to `{ online: boolean; contexts?: { name: string; server: string }[] }` and below the status line add:

```tsx
      <p className="muted">
        Cluster labs run on a cluster from your kubeconfig. {status?.contexts?.length
          ? <>Your agent sees: {status.contexts.map((c) => c.name).join(', ')}.</>
          : <>No clusters yet: <code>kind create cluster</code> makes one on this laptop.</>}
      </p>
```

and change the intro sentence to "Laptop labs run in Docker on your own machine through `crucible-agent`, and cluster labs on a Kubernetes cluster of your kubeconfig."

- [ ] **Step 5: Record the patterns**

Add to `.claude/skills/crucible-ui/SKILL.md`'s patterns table:

```markdown
| Pick before start | A choice a lab needs before it starts (a cluster lab's context): a `select` labelled from the user's side (*Run it on*), each option naming what it is and where (`kind-dev · 127.0.0.1:52011`), the last pick remembered per browser and preselected only if still offered; a risky variant asks with a `label.check` consent box, and the start button stays disabled until both are given. With nothing to pick, a muted line says how to get something. |
| Live status list | Several named things and their state (a lab's Helm releases): `section.panel` with an `h5`, one `li` each with the name in `strong` and a `.badge` in words (*Ready*, *Starting*, *Failing*), then what is wrong in `code`, never colour alone. |
```

- [ ] **Step 6: Run the web checks**

Run: `cd web && npm test && npx tsc -b && npm run lint && npm run build`
Expected: PASS.

- [ ] **Step 7: Look at it** (UI skill step): run the stack with Task 11's env (`CRUCIBLE_TOOLBOX_IMAGE`), start the dev server (`cd web && npx vite --port 5199 --strictPort`), sign in, open Forge 101 → Cluster Heat, and screenshot the start panel and a running lab in Forge, Anvil, Parchment and High Contrast, and at 390px; nothing may scroll sideways.

- [ ] **Step 8: Commit**

```bash
git add web/src/lib/cluster.ts web/src/lib/cluster.test.ts web/src/components/ClusterPick.tsx web/src/components/ReleaseHealth.tsx web/src/components/ClusterPick.test.tsx
git commit -m "feat(web): pick a cluster for a cluster lab, consent to cluster-wide labs, see release health

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- web/src .claude/skills/crucible-ui/SKILL.md
```

---

### Task 13: Docs and guides

**Files:**
- Create: `docs/user/authors/cluster-labs.md`
- Modify: `docs/user/trainees/cluster-and-aws-labs.md`, `docs/user/trainees/connect-your-laptop.md`
- Modify: `docs/superpowers/specs/2026-10-05-crucible-design.md` (§8.2 runtimes, §9.1 rate card)
- Modify: `docs/superpowers/plans/2026-10-05-crucible-roadmap.md` (coverage rows)
- Modify: `docs/test-guide.html`, `docs/aws-guide.html`

**Interfaces:** none (docs).

- [ ] **Step 1: Write the authors' page**

`docs/user/authors/cluster-labs.md`:

```markdown
---
title: Cluster labs
roles: [author]
order: 25
---
# Cluster labs

A cluster lab installs Helm charts on a Kubernetes cluster the trainee picks from their own kubeconfig, through their
laptop agent: a kind cluster on the laptop or a remote one. It costs nothing in Crucible.

## The lab file

```yaml
runtime: cluster
cluster:
  charts:                     # installed in this order into the lab's namespace, removed in reverse
    - name: shop              # the Helm release name
      path: charts/shop       # a folder in this lab with a Chart.yaml
      values: values/shop.yaml   # optional
      timeout: 5m             # how long helm --wait may take; 5m if left out
  cluster_wide: false         # true: the charts create cluster-wide things; the trainee agrees first
terminals:
  - { name: shell }           # every terminal opens in the toolbox
tasks:
  - id: t1
    instructions: tasks/01.md
    check: { script: checks/01.sh }
  - id: t2
    instructions: tasks/02.md
    check: { ready: [shop] }  # or ready: all
```

- **The namespace.** Each lab gets its own, `lab-<id>`. Charts use `.Release.Namespace` as usual.
- **The toolbox.** Terminals, setup scripts and checks run in a container with `kubectl`, `helm`, `k9s`, `jq` and
  `crucible-ready`, pointed at the trainee's cluster, with `LAB_NAMESPACE`, `LAB_ID` and `LAB_RELEASES` set. There
  is no `service` or `run_in` to name.
- **Ready checks.** `check: { ready: [shop] }` passes when every resource of the release is ready: deployments,
  stateful sets and daemon sets rolled out, jobs complete, pods ready, volume claims bound, services with a ready
  endpoint. A failing one says what isn't ready. Scripts can run `crucible-ready shop` too.
- **Dependencies.** A chart may depend on others pinned to an exact version from `https://`, `oci://` or a
  `file://` folder in the lab; they are fetched when the lab starts.
- **Cluster-wide resources.** CRDs, ClusterRoles, StorageClasses and the like need `cluster_wide: true`. Lint warns,
  and the agent refuses a lab whose rendered charts create them without it. Helm never deletes CRDs from a chart's
  `crds/` folder, so they stay on the cluster after the lab: say so in the instructions.
- **Results are self-reported**, as for laptop labs: they come from the trainee's machine.
```

- [ ] **Step 2: Update the trainee pages**

In `docs/user/trainees/cluster-and-aws-labs.md`, replace the cluster section with:

```markdown
## Cluster labs

A cluster lab runs on a Kubernetes cluster you choose, through your laptop agent: a kind cluster on your laptop, or
a remote one you can use. Pick it under **Run it on** before you start; the list is the contexts in your kubeconfig.
The lab installs into its own namespace there, your terminals open in a toolbox pointed at it, and ending the lab
removes it. It costs nothing in Crucible: the cluster is yours.

If a lab creates cluster-wide resources, it says so, and you tick a box to go ahead. Some of those (CRDs) may stay
on the cluster afterwards.
```

In `docs/user/trainees/connect-your-laptop.md`, add a section:

```markdown
## Cluster labs

The agent also reads your kubeconfig (`--kubeconfig`, else `KUBECONFIG`, else `~/.kube/config`) and tells Crucible
the names of its contexts and their servers, never your credentials. A cluster lab connects through the agent, so
your cloud logins (`aws`, `gcloud`) work as they do for `kubectl`. No cluster yet? `kind create cluster` makes one.
```

- [ ] **Step 3: The spec, roadmap, test guide and AWS guide**

1. Spec `2026-10-05-crucible-design.md`: in §8.2, replace the cluster runtime's description with "**cluster** — Helm charts installed by the trainee's agent on a cluster of their kubeconfig (spec `2026-10-11-cluster-labs-on-your-cluster-design.md`); free, self-reported." In §9.1, delete the `cluster_usd_per_hour` rate card sentence.
2. Roadmap: add a coverage row: `| Cluster labs on your own cluster (2026-10-11 spec) | 2026-10 | TestClusterLabRules; TestJudge; TestProxy*; TestProvisionInstallsInOrderThroughTheToolbox; TestFailedInstallCleansUp; TestTwoClusterLabsAreIndependent; TestDestroyAfterRestartReopensTheProxy; TestClusterProvisionSendsChartsNeverChecks; TestClusterLabStartsOnAPickedContextForFree; TestMigration00026EndsCrucibleClusterLabs; cluster-lab.spec.ts | P |`
3. `docs/test-guide.html`: replace the cluster lab step with one that creates a kind cluster (`kind create cluster --name crucible-try --kubeconfig ~/crucible-try.kubeconfig`), runs the agent with `--kubeconfig ~/crucible-try.kubeconfig`, picks it under **Run it on**, scales the forge, fixes the broken image, watches **On your cluster**, ends the lab and checks `kubectl get ns` shows no `lab-…` namespace; then deletes the kind cluster.
4. `docs/aws-guide.html`: where it says Crucible's k3s cluster runs cluster labs, say it runs AWS lab workspaces and Terraform runs; cluster labs run on trainees' own clusters.

- [ ] **Step 4: Run the docs test**

Run: `go test ./internal/docs`
Expected: PASS (every page covers something that exists; no route or role is uncovered).

- [ ] **Step 5: Commit**

```bash
git add docs/user/authors/cluster-labs.md
git commit -m "docs: cluster labs on your own cluster, for authors, trainees and operators

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- docs
```

---

### Task 14: The end-to-end journey on kind

**Files:**
- Modify: `e2e/tests/cluster-lab.spec.ts`
- Modify: `scripts/cluster-check.sh`

**Interfaces:**
- Consumes: everything above; `CRUCIBLE_AGENT` (the e2e already passes the built agent), the kind cluster `crucible-m4` and `.local/kind/admin.kubeconfig` that `cluster-check.sh` creates.

- [ ] **Step 1: Build the toolbox in the check**

In `scripts/cluster-check.sh`, before the compose stack starts, add:

```bash
echo "== toolbox image for cluster labs"
make toolbox TOOLBOX_TAG=e2e
```

and export the kubeconfig for the agent the spec spawns: `export CRUCIBLE_E2E_KUBECONFIG="$root/.local/kind/admin.kubeconfig"` next to the other exports. Never use `~/.kube/config`.

- [ ] **Step 2: Rewrite the spec**

`e2e/tests/cluster-lab.spec.ts`:

```ts
import { spawn, type ChildProcess } from 'node:child_process'
import { expect, test, type Page } from '@playwright/test'

async function typeIn(page: Page, tab: string, command: string) {
  await page.getByRole('tab', { name: tab, exact: true }).click()
  await page.locator(`[data-terminal="${tab}"]`).click()
  await page.keyboard.type(command)
  await page.keyboard.press('Enter')
}

const labAPI = '/api/programs/forge/forge-101/modules/03-cluster-heat/lab'
let agent: ChildProcess | undefined
test.afterAll(() => { agent?.kill('SIGTERM') })

test('Forge 101 cluster lab: Helm on your own kind cluster through the agent', async ({ page }) => {
  page.on('dialog', (d) => d.accept())
  await page.goto('/')
  await page.getByRole('button', { name: 'Enter the forge' }).click()
  await page.locator('#username').fill('trainee')
  await page.locator('#password').fill('trainee')
  await page.locator('#kc-login').click()
  await expect(page.getByRole('heading', { name: 'Hearth' })).toBeVisible()

  // Pair the agent with the kind cluster's own kubeconfig (never ~/.kube/config).
  const { token } = await (await page.request.post('/api/agent/tokens', { data: {} })).json()
  agent = spawn(process.env.CRUCIBLE_AGENT!, ['--server', 'http://localhost:8080', '--kubeconfig', process.env.CRUCIBLE_E2E_KUBECONFIG!],
    { stdio: 'inherit', env: { ...process.env, CRUCIBLE_TOKEN: token } })

  await page.getByRole('link', { name: /Forge 101/ }).click()
  await page.getByTestId('module-03-cluster-heat').getByRole('link', { name: 'Lab', exact: true }).click()
  await expect(page.getByRole('option', { name: /kind-crucible-m4/ })).toBeAttached({ timeout: 30_000 })
  await page.getByLabel('Run it on').selectOption('kind-crucible-m4')
  await page.getByRole('button', { name: 'Ignite the forge' }).click()
  await expect(page.getByRole('tab', { name: 'shell', exact: true })).toBeVisible({ timeout: 5 * 60_000 })
  await expect(page.getByText('on kind-crucible-m4')).toBeVisible()

  // Task 1: kubectl in the toolbox, a script check.
  await typeIn(page, 'shell', 'kubectl scale deploy/forge --replicas=2 && kubectl rollout status deploy/forge')
  await page.getByRole('button', { name: 'Check' }).click()
  await expect(page.getByText('Two fires burn in the forge.')).toBeVisible({ timeout: 60_000 })

  // Task 2: the setup breaks the image; a ready: check passes once it is fixed.
  await expect(page.getByText('Relight the forge')).toBeVisible({ timeout: 90_000 })
  await page.getByRole('button', { name: 'Check' }).click()
  await expect(page.getByText(/deployment\/forge: \d of 2 ready/)).toBeVisible() // health names what is not ready
  await typeIn(page, 'shell', 'kubectl set image deploy/forge web=nginx:1.27-alpine && kubectl rollout status deploy/forge')
  await page.getByRole('button', { name: 'Check' }).click()
  await expect(page.getByText(/Lab forged/)).toBeVisible({ timeout: 60_000 })

  const info = await (await page.request.get(labAPI)).json()
  expect(info.runtime).toBe('cluster')
  expect(info.lab.self_reported).toBe(true)
  expect(info.lab.cluster_context).toBe('kind-crucible-m4')
  expect(info.estimate_usd).toBe(0)

  const ns = `lab-${info.lab.id}`
  await page.getByRole('button', { name: 'End lab' }).click()
  await expect(page.getByRole('heading', { name: 'The forge has cooled' })).toBeVisible({ timeout: 3 * 60_000 })
  // The namespace is gone from the cluster.
  const { execFileSync } = await import('node:child_process')
  const left = execFileSync('kubectl', ['--kubeconfig', process.env.CRUCIBLE_E2E_KUBECONFIG!, 'get', 'ns', ns, '--ignore-not-found', '-o', 'name']).toString().trim()
  expect(left).toBe('')
})
```

- [ ] **Step 3: Run the cluster check**

Run: `KEYCLOAK_PORT=8082 make cluster-check`
Expected: the AWS dry-run journey and this cluster lab journey pass; `crucible-m4` is deleted at the end (unless `KEEP=1`).

- [ ] **Step 4: Commit**

```bash
git commit -m "test(e2e): Forge 101's cluster lab on a kind cluster through the agent

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- e2e/tests/cluster-lab.spec.ts scripts/cluster-check.sh
```
