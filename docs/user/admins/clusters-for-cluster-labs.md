---
title: Clusters for cluster labs
roles: [admin]
order: 50
---
# Clusters for cluster labs

A cluster lab runs as whoever the trainee's chosen kubeconfig context logs in as. Crucible adds no service account of
its own: the laptop agent passes the trainee's own credentials through, and the lab can do exactly what that identity
can do, nothing more. This page is for whoever gives trainees a cluster to use.

The simplest answer is a cluster each: `crucible-agent cluster up` makes one on the trainee's laptop with Docker, and
they are its administrator, so every lab works. Read on if trainees share a cluster.

## What the kubeconfig needs

- **Reach.** The laptop can reach the context's API server. A server address with a path (Rancher style) works.
- **A login that works without a prompt.** Client certificates, tokens and exec plugins (`aws eks get-token`, `gcloud`,
  `kubelogin`) all work; the agent runs them for each request, so the trainee logs in first (`aws sso login`, say). If
  the login fails, the lab says it can't reach the cluster and shows the plugin's message. The old `auth-provider:`
  entries are not supported.
- **Images.** The cluster can pull the images the lab's charts use. The toolbox itself runs on the laptop.
- **Storage.** Charts that claim volumes need a default StorageClass.

## Permissions

Across the cluster:

| What | Why |
|---|---|
| `namespaces`: create, get, patch, delete | the lab's own namespace `lab-<id>`, labelled `app.kubernetes.io/managed-by=crucible` and `crucible.lab=<id>`, deleted when the lab ends |
| discovery, `/version` and `/openapi/*` (any logged-in user has these) | the agent's first check and Helm's validation |

Inside `lab-<id>`:

| What | Why |
|---|---|
| whatever the lab's charts create: create, get, list, watch, patch, update, delete | installing (`helm upgrade --install --wait`) and removing the charts |
| `secrets`: create, get, list, update, delete | Helm keeps its release records as secrets |
| deployments, stateful sets, daemon sets, replica sets, jobs, pods, services and volume claims: get, list, watch; `endpointslices`: list | waiting for the install, release health and `ready:` checks |
| whatever the lab's tasks ask the trainee to do | for example `deployments/scale`, `pods/log`, `pods/exec`, `pods/portforward` |

A lab marked `cluster_wide` also creates and deletes what its charts add across the cluster (CRDs, ClusterRoles and
the like). CRDs stay after the lab ends.

## Sharing one cluster

Kubernetes roles can't be granted "in every namespace whose name starts with `lab-`": a RoleBinding can only be made in
a namespace that already exists. So an identity that runs labs on a shared cluster needs rights across the cluster:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: { name: crucible-lab-namespaces }
rules:
  - apiGroups: [""]
    resources: [namespaces]
    verbs: [create, get, list, patch, delete]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: { name: crucible-lab-namespaces }
roleRef: { apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: crucible-lab-namespaces }
subjects: [{ kind: User, name: trainee@example.com }]   # or a ServiceAccount
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: { name: crucible-lab-admin }
roleRef: { apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: admin }   # built in
subjects: [{ kind: User, name: trainee@example.com }]
```

That makes the trainee an administrator of **every** namespace, not only their labs. You can stop them **changing**
anything outside lab namespaces with an admission policy, a rule the API server checks before it accepts a change:

```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata: { name: crucible-trainee-labs-only }
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
      - { apiGroups: ["*"], apiVersions: ["*"], operations: [CREATE, UPDATE, DELETE], resources: ["*/*"] }
      - { apiGroups: [""], apiVersions: [v1], operations: [CONNECT], resources: [pods/exec, pods/attach, pods/portforward] }
  matchConditions:
    - name: this-trainee
      expression: "request.userInfo.username == 'trainee@example.com'"
  validations:
    - expression: >-
        request.resource.resource == 'namespaces'
          ? request.name.startsWith('lab-')
          : request.namespace.startsWith('lab-')
      message: this identity may only change Crucible lab namespaces (lab-<id>)
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata: { name: crucible-trainee-labs-only }
spec:
  policyName: crucible-trainee-labs-only
  validationActions: [Deny]
```

Admission policies only see changes, never reads: the trainee can still **read** every namespace, secrets included.
The policy also refuses anything outside a namespace, so `cluster_wide` labs won't run for that identity. Where other
namespaces hold anything sensitive, give each trainee a cluster of their own (a kind cluster on the laptop, or a
vcluster on the shared one) instead.

## Pod Security

If the cluster enforces a Pod Security level by default, the lab's pods must meet it. Lab namespaces get no Pod
Security label from the agent, so the cluster's default applies.
