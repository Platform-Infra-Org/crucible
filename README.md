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

## Authoring

Content lives in git: see `examples/forge-101` and the spec in `docs/superpowers/specs/`.
Validate a content repo with `go run ./cmd/crucible lint <dir>`.
