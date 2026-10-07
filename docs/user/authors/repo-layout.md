---
title: How a training repo is laid out
roles: [author]
covers: [feature:repo.layout]
order: 10
---
# How a training repo is laid out

A training lives in its own git repository. Crucible reads it from the branch your admins registered, so what is
merged there is what trainees get (each program can also pin an older version).

```text
training.yaml              id, title, maintainers, progression, the module list
assets/                    images and fonts your readings link to
modules/
  01-first-heat/
    module.yaml            title, completion rule, the items in order
    reading/
      before-the-lab.md    a reading; its first heading is its title
    quiz.yaml              the module's quiz
    lab/
      lab.yaml             runtime, timings, terminals, tasks
      compose.yaml         the containers (laptop and cluster labs)
      tasks/               one Markdown file of instructions per task
      checks/              the scripts that check each task
      setup/               scripts that prepare a task's starting state
      hints/               hints too long to write inline
```

## The rules that matter

- `training.yaml` lists the module folders in the order trainees take them.
- `module.yaml` lists its items in order. Each item is one of `reading: <file>`, `quiz: quiz.yaml` or
  `lab: <folder>`. A module has at most one quiz and one lab.
- The lab folder can have any name; `lab` is the habit. An AWS lab keeps its Terraform in `terraform/` inside it.
- Readings link to images as `assets/...`, or to full URLs. Other relative links won't resolve in Crucible.
- Scripts and files are referenced relative to the lab folder.

## Every key

[Building blocks: every key](/docs/authors/building-blocks) lists every key Crucible reads from these files, with its
type, whether it is required and what it does.

Every block, with its form and an example: [Training](/docs/authors/blocks/training), [Module](/docs/authors/blocks/module),
[Reading](/docs/authors/blocks/reading), [Quiz](/docs/authors/blocks/quiz), [Lab](/docs/authors/blocks/lab),
[AWS](/docs/authors/blocks/aws).

## Checking your work

Run `crucible lint` on the repo before you push, and `crucible preview` to try it as a trainee. See
[Lint and preview](/docs/authors/lint-and-preview).
