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

- **Nothing — the repository is public.** Anyone can read it, so Crucible needs no credential. Note that Crucible
  still cannot *write* to it, so approved edits will not merge.
- **An access token.** For a private repository, or any repository whose content people will edit in Crucible.

The token needs to **read and write** the repository's contents. Write matters: merging an approved edit pushes a
commit. A read-only token looks fine until the first edit is approved, and then the merge fails.

- **GitHub**: Settings → Developer settings → Fine-grained tokens, limited to that one repository,
  *Contents: Read and write*. An organisation may have to approve it. Note that these tokens expire — a year at
  most, and sooner if your organisation says so.
- **GitLab**: a project access token with `write_repository`.
- The user name is optional: GitHub ignores it, GitLab wants any non-empty name.

Crucible stores the token encrypted and never shows it again — not on this page, not through its API, not in the
audit log, which records only that a credential was set. To change it, choose **Replace credential** and type a new
one; leaving the box empty keeps the one already stored.

An instance with no `CRUCIBLE_SECRET_KEY` cannot keep a credential at all. The page says so, and only public
repositories can be connected until whoever runs this Crucible sets that key.

## What the status means

- **Reachable** — Crucible listed the repository's branches with the credential it has.
- **Cannot be read** — with the reason. A repository that has just been connected or changed says **Not checked
  yet** until something reads from it; **Test connection** checks it now.

Testing checks that Crucible can *read*. Whether it can *write* is proven the first time an approved edit merges.

## Disconnecting

**Disconnect** removes the connection; the repository itself is untouched. A repository that a training still reads
from cannot be disconnected — delete the training first, on
[Manage trainings](/docs/leaders/manage-trainings).

## Moving to another instance

An export carries the connections but not their credentials: those are sealed with this instance's key, which the
new one does not have. After an import, each repository shows **No credential** and is reconnected there. See
[Forge settings](/docs/admins/settings-and-registry).
