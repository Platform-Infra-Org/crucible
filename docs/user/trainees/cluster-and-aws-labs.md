---
title: Cluster and AWS labs
roles: [trainee]
covers: [route:/labs, feature:lab.cluster, feature:lab.aws]
order: 50
---
# Cluster and AWS labs

Not every lab runs on your laptop. The badge in the lab's header tells you where it runs.

## Cluster labs

A cluster lab runs on a Kubernetes cluster you choose, through your laptop agent: a kind cluster on your laptop, or
a remote one you can use. Pick it under **Run it on** before you start; the list is the contexts in your kubeconfig.
The lab installs into its own namespace there, your terminals open in a toolbox pointed at it, and ending the lab
removes it. It costs nothing in Crucible: the cluster is yours.

The list follows your kubeconfig while the page is open: start the agent, or make a cluster with
`crucible-agent cluster up` (remove it later with `crucible-agent cluster down`), and it shows up without a reload. If the agent isn't running, **Connect your laptop** says how to start it.
A context you delete drops out of the list, and the lab can't start until you pick another.

On a cluster you didn't make yourself, you need to be able to create namespaces and install into them; a cluster from
`crucible-agent cluster up` gives you that. Your login must work without a prompt (run `aws sso login` or the like
first). Whoever runs a shared cluster can find the details in
[Clusters for cluster labs](/docs/admins/clusters-for-cluster-labs).

While it runs, **On your cluster** on the lab page shows each release as Ready, Starting or Failing. Results are
self-reported, as for laptop labs.

If a lab creates cluster-wide resources, it says so, and you tick a box to go ahead. Some of those (CRDs) may stay
on the cluster afterwards.

A lab that ends while your agent isn't running (its time ran out, say) stays on your cluster until the agent connects
again; then it is removed. If you deleted its cluster or its context in the meantime, the agent forgets the lab, and
whatever is left on a cluster you still have (the `lab-<id>` namespace) is yours to delete.

## AWS labs

An AWS lab builds real cloud resources in a shared AWS account, so it costs money. The lab page shows the estimate.

- If it needs an approval, the button reads **Request approval ($…)**. Your request goes to the training's admins, the
  leaders of the team that owns it, or an admin, depending on the amount, and moves up a tier if nobody answers in time. The page shows who it
  is waiting for. **Withdraw request** cancels it.
- Once it is approved, the lab starts on its own. If it was rejected, the page says who rejected it and why.

## When you can't start a lab

The lab page says why: labs are paused by an admin, the training only runs labs in certain hours (it tells you when
the next window opens), or a budget would be passed. A lab that would pass a budget cap can still be requested, but
only an admin can approve it.

## The Labs page

**Labs** in the top bar lists every lab you have started, with its state, when it started and, while it runs, when it
ends. **Open** takes you back into it.
