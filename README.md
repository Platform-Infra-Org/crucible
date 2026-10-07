# Crucible

Forge new teammates from zero to hero: reading, quizzes, and hands-on labs, all authored in git.

## Local check (no cloud needed)

Prerequisites: Docker with the compose plugin, Go 1.26, Node 22+, git.

```bash
make local-check          # build, start Postgres + Keycloak + Crucible, run the Forge 101 end-to-end test
KEEP=1 make local-check   # same, but leave the stack running at http://localhost:8080
KEYCLOAK_PORT=8082 make local-check   # if host port 8081 (Keycloak) is already taken
```

Local users (username = password): `trainee`, `senior`, `leader`, `admin`.
To try a lab by hand: log in as `trainee`, open **Connect your laptop**, generate a token and run the
printed `./bin/crucible-agent …` command in a terminal.

## Configuration notes

`crucible-api` reads `CRUCIBLE_QUIZ_SECRET`, a stable secret mixed into the per-learner quiz choice ids so the
browser cannot infer answers from them. Set it to a random value in every real deployment and keep it stable across
restarts (changing it only reshuffles quizzes that are open at that moment). If it is unset the API logs a warning and
uses a fixed development value; the local compose stack sets a dev value.

## Local labs and your laptop

`runtime: local` labs run in Docker on the trainee's laptop. Their compose file is checked against an allowlist (no privileged mode, host namespaces, published ports, writable binds, secrets), but the containers can still reach the internet and `host.docker.internal`. Use `runtime: cluster` labs when you need stricter isolation.

## Authoring

Content lives in git: see `examples/forge-101` and the spec in `docs/superpowers/specs/`.
Validate a content repo with `go run ./cmd/crucible lint <dir>`.

You can also edit a training in the app: **Edits** opens a browser editor on a draft that autosaves to the server, with
YAML completion, the same problems `crucible lint` reports, a preview of readings, quizzes and labs, and a **Blocks**
panel that adds modules, readings, questions, labs, tasks and hints through forms. Submitting pushes the draft to its
own branch for a maintainer to review; the bot merges it once approved. Nobody enrolled in a training can edit it. The
**Docs** tab explains every screen by role, and the generated *Building blocks* pages list every key a training repo
can use.

Things that catch authors out:

- **A score-rule module can get stuck.** With `completion: score` and a `threshold`, a module completes only when the
  weighted score of its completed items reaches the threshold. If a trainee has finished every item but scored under
  it, nothing is left to do and the module never completes. Keep the threshold reachable, or allow quiz retries
  (`max_attempts`). An admin can reset one trainee's attempts or final score from that trainee's submission on the
  Anvil (items with a human-scored part).
- **Quiz attempts count across content versions.** For an instant-only quiz, raising `max_attempts` in git is the
  escape hatch.
- **`crucible preview` runs the repo's scripts.** Local-lab compose files and setup scripts run on your Docker. Read
  a repo you did not write before previewing it (see below).

### Running `crucible-api` outside the image

The image sets `CRUCIBLE_GIT_CONFIG` and the local compose stack sets `CRUCIBLE_GIT_ALLOW_FILE`; a developer running
the API directly from a shell sets them by hand:

- `CRUCIBLE_GIT_ALLOW_FILE=1` lets repo URLs be local paths or `file://` (seeded repos on disk). It is off by
  default so a configured repo URL cannot read the server's own disk. Leave it unset in production.
- `CRUCIBLE_GIT_CONFIG` is the only git config the bot's git uses (credential helper, `safe.directory`); your
  `~/.gitconfig` and the system config are ignored. Point it at a file with your credential helper when the repos
  need auth, or leave it unset for none.

## Previewing content (`crucible preview`)

Needs Docker and a locally built image:

```bash
docker build -t crucible:dev .                   # once, from this repo
go run ./cmd/crucible preview path/to/your-training --free
```

It prints a sign-in link (valid for the whole run, not one-time) for http://localhost:8090 (bound to 127.0.0.1 only). Readings, quizzes and laptop
(`local`) labs run against your working tree, including setup scripts for break-fix tasks; the lab agent runs inside
the CLI. Only files git tracks or has staged are previewed (untracked files such as `.env` never are): `git add` a new
file to see it. Save a file and the preview picks it up within seconds; lint problems print in the terminal. `--free`
opens every module. Ctrl-C (or closing the terminal) removes the containers, network and volumes; after a `kill -9`, run `docker compose ls` and `docker compose -p crucible-preview-... down -v`.

Previewing someone else's repo runs its local-lab compose files and setup scripts on your Docker, gated only by the content lint: read it first.
