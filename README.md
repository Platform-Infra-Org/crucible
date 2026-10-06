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

## Previewing content (`crucible preview`)

Needs Docker and a locally built image:

```bash
docker build -t crucible:dev .                   # once, from this repo
go run ./cmd/crucible preview path/to/your-training --free
```

It prints a one-time sign-in link for http://localhost:8090 (bound to 127.0.0.1 only). Readings, quizzes and laptop
(`local`) labs run against your working tree, including setup scripts for break-fix tasks; the lab agent runs inside
the CLI. Only files git tracks or has staged are previewed (untracked files such as `.env` never are): `git add` a new
file to see it. Save a file and the preview picks it up within seconds; lint problems print in the terminal. `--free`
opens every module. Ctrl-C removes the containers, network and volumes.
