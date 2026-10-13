---
title: Repositories
roles: [admin]
covers: [route:/admin/repos]
order: 25
---
# Repositories

**Administrator → Repositories** is where training content comes from. A repository is connected once; every
training that reads from it shares that connection, so a credential is entered once and rotated in one place. One
repository can hold several trainings, a folder each.

## Connecting one

Give the repository's URL and say how Crucible should sign in:

- **No credential of its own.** Crucible adds no credential: a public repository is read as it is. This is not a
  promise that Crucible cannot write. An `https://` repository may still be reached with this instance's own git
  credentials, if whoever runs Crucible set them, and then edits can be pushed with those. **Test connection** shows
  which: *Can write* or *Read only*.
- **An access token.** For a private `https://` repository, or any whose content people will edit in Crucible.
- **A deploy key Crucible makes.** For an `ssh://` or `git@host:path` repository. Crucible generates the pair,
  keeps the private half and shows you the public half to add at your host. Nothing secret ever leaves your side,
  the key can be shown again whenever you need it, and unlike a token it does not expire.

The credential needs to **read and write** the repository's contents if anyone will edit this training's content in
Crucible. Write is needed earlier than you might expect: **submitting** an edit for review pushes it to a
`crucible/edit/…` branch of your repository, before any reviewer sees it. With read-only access, syncing, reading,
quizzes and labs all work, and so does the editor itself — drafts are kept in Crucible — but the author loses their
work at the moment they submit it. Connect it read-only only if you mean the content to be authored in git.

- **GitHub**: Settings → Developer settings → Fine-grained tokens, limited to that one repository,
  *Contents: Read and write*. An organisation may have to approve it. Note that these tokens expire — a year at
  most, and sooner if your organisation says so.
- **GitLab**: a project access token with `write_repository`.
- The user name is optional: GitHub ignores it, GitLab wants any non-empty name.

## Deploy keys

Choose **A deploy key Crucible makes** and connect. The repository then shows a public key with **Copy the key**.
Add it at your host **with write access** — GitHub: Settings → Deploy keys → Add deploy key, tick *Allow write
access*; GitLab: Settings → Repository → Deploy keys, with *Grant write permissions*. Then **Test connection**.

A deploy key belongs to one repository, which is what Crucible wants: each connection gets its own key, and you
revoke it by deleting it at the host. Choosing **Replace credential** and saving again makes a fresh pair, so the
old key stops working — add the new one.

The first time Crucible reaches a host it records that host's fingerprint and refuses it if it ever changes
afterwards. The fingerprint is kept in Crucible's database, so a restart or a rebuilt server still holds it. Only a
host met for the first time in the last minute or so before a restart is met afresh, as if for the first time.

Crucible stores the token encrypted and never shows it again — not on this page, not through its API, not in the
audit log, which records only that a credential was set. To change it, choose **Replace credential** and type a new
one; leaving the box empty keeps the one already stored.

An instance with no `CRUCIBLE_SECRET_KEY` cannot keep a credential at all. The page says so, and repositories can be
connected only with no credential of Crucible's own until whoever runs this Crucible sets that key. On AWS,
`crucible aws init` makes it; an install from before that re-runs `init` (see the AWS runbook). Changing or losing
the key makes every stored token and deploy key unreadable, and those repositories are reconnected.

## What the status means

- **Reachable** — Crucible listed the repository's branches with the credential it has. Beside it, **Can write** or
  **Read only** says whether the repository would take a push from Crucible.
- **Cannot be read** — with the reason. A repository that has just been connected or changed says **Not checked
  yet** until something reads from it; **Test connection** checks it now.

Testing checks that Crucible can *read*, and then whether it may *write*: it asks the host to start a push and stops
before sending anything, so nothing in the repository changes. **Read only** means edits made in Crucible cannot be
submitted; give the token or deploy key write access and test again.

## Disconnecting

**Disconnect** removes the connection; the repository itself is untouched. A repository that a training still reads
from cannot be disconnected — delete the training first, on
[Manage trainings](/docs/leaders/manage-trainings).

## Moving to another instance

An export carries the connections but not their credentials: those are sealed with this instance's key, which the
new one does not have. After an import, each repository shows **No credential** and is reconnected there. See
[Forge settings](/docs/admins/settings-and-registry).
