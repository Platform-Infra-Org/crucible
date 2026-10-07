---
title: Cluster and AWS labs
roles: [trainee]
covers: [route:/labs, feature:lab.cluster, feature:lab.aws]
order: 50
---
# Cluster and AWS labs

Not every lab runs on your laptop. The badge in the lab's header tells you where it runs.

## Cluster labs

A cluster lab runs in Crucible's Kubernetes cluster. You need nothing installed: open it, choose **Ignite the
forge**, and work in the browser terminals as usual. If the cluster has no room right now, the lab fails to start;
try again a little later.

## AWS labs

An AWS lab builds real cloud resources in a shared AWS account, so it costs money. The lab page shows the estimate.

- If it needs an approval, the button reads **Request approval ($…)**. Your request goes to an approver, your team
  leader or an admin, depending on the amount, and moves up a tier if nobody answers in time. The page shows who it
  is waiting for. **Withdraw request** cancels it.
- Once it is approved, the lab starts on its own. If it was rejected, the page says who rejected it and why.

Cluster labs can need an approval too, when your Crucible prices them high enough.

## When you can't start a lab

The lab page says why: labs are paused by an admin, the program only runs labs in certain hours (it tells you when
the next window opens), or a budget would be passed. A lab that would pass a budget cap can still be requested, but
only an admin can approve it.

## The Labs page

**Labs** in the top bar lists every lab you have started, with its state, when it started and, while it runs, when it
ends. **Open** takes you back into it.
