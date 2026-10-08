---
title: First admin, audit and runbooks
roles: [admin]
covers: [feature:audit, feature:bootstrap-admin]
order: 30
---
# First admin, audit and runbooks

## Who is an admin

Where admins are kept depends on how this Crucible was installed, and **Platform settings** on
[Forge Status](/docs/admins/forge-status-and-kill-switch) says which:

- **In Crucible's own database** — add and remove them under **Administrator → Forge settings**. See
  [Forge settings and the registry](/docs/admins/settings-and-registry).
- **In the platform repo** — admins are listed in `admins.yaml`. Change that file in git, and Crucible picks it up
  on the next sync.

Either way the last admin can't be removed, and every grant and removal is audited.

## The first admin

A new Crucible has no admins at all. Whoever installs it sets a bootstrap admin email (the
`CRUCIBLE_BOOTSTRAP_ADMIN` setting). On the very first start, and only while no admin exists yet, Crucible records
that email as the first admin — as a row in its database, or, in the git case, as a commit the bot makes to
`admins.yaml`. After that the setting does nothing: a restart never puts back an admin you removed.

That person still signs in through the company identity provider, with a verified email, like everyone else. They
configure everything else from the app.

## The audit log

Crucible records every privileged action: who did it, when, what it touched and, for changes saved to git, the
commit. That includes approvals and rejections, budget-cap overrides, scores, returns and sign-offs, score
overrides and resets, content edit decisions, roster and program changes, the kill switch, and revoked laptop
agents. The latest entries are under **Recent privileged actions** on Forge Status.

## Runbooks

Setting Crucible up is work for whoever runs it, outside the app. Two step-by-step guides live in the Crucible code
repository, under `docs/runbooks`:

- **AWS** (`aws.md`): running Crucible in your own AWS account with the `crucible aws` commands: setting it up,
  sleeping and waking it, snapshots, tearing it down, and the AWS account AWS labs run in.
- **Cognito** (`cognito.md`): signing people in with an Amazon Cognito user pool, inviting them and managing their
  accounts.
