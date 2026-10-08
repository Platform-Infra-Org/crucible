---
title: Lint and preview
roles: [author]
covers: [feature:cli.lint, feature:cli.preview]
order: 40
---
# Lint and preview

The `crucible` command-line tool lets you check and try a training on your own machine before anyone else sees it.

## crucible lint

```sh
crucible lint path/to/training-repo
```

Runs the same checks Crucible runs when it loads the training, and lists every problem with its file. It also runs
`shellcheck` on your scripts when you have it installed, and prices AWS labs with `infracost` when you have it and an
API key. It ends with *Ready for the forge* when all is well, and exits non-zero otherwise, so it fits in CI.

Point it at a platform repo instead and it checks the platform config.

## crucible preview

```sh
crucible preview path/to/training-repo [--port 8090] [--free]
```

Starts a private Crucible on your machine with just this training, and prints a link to open it. You take the
training as a trainee would: readings, quizzes, and laptop labs with their setup scripts, run on your own Docker.

- Save a file and the preview picks it up within a few seconds. Files git doesn't track are left out, so
  `git add` new files first; an untracked secret never reaches the preview.
- `--free` opens every module at once, whatever the training's progression.
- Ctrl-C stops the preview and removes everything it started.

Preview needs Docker and the Crucible image (`crucible:dev` by default; your admins can tell you where to get it).
