---
title: Cluster labs
roles: [author]
order: 25
---
# Cluster labs

A cluster lab installs Helm charts on a Kubernetes cluster the trainee picks from their own kubeconfig, through their
laptop agent: a kind cluster on the laptop or a remote one. It costs nothing in Crucible. Add one with the **Cluster
lab** block, then fill in the charts.

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
- **Release health.** The lab page shows each release under **On your cluster** as Ready, Starting or Failing.
- **Dependencies.** A chart may depend on others pinned to an exact version from `https://`, `oci://` or a
  `file://` folder in the lab; they are fetched when the lab starts.
- **Cluster-wide resources.** CRDs, ClusterRoles, StorageClasses and the like need `cluster_wide: true`. Lint warns,
  and the agent refuses a lab whose rendered charts create them without it. Helm never deletes CRDs from a chart's
  `crds/` folder, so they stay on the cluster after the lab: say so in the instructions.
- **Results are self-reported**, as for laptop labs: they come from the trainee's machine.
