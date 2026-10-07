---
title: Labs on your laptop
roles: [trainee]
covers: [route:/p/:team/:training/m/:module/lab, feature:lab.local]
order: 30
---
# Labs on your laptop

A laptop lab runs in Docker on your own machine, through the Crucible agent. You do the work in terminals in the
browser; Crucible checks it.

## Before you start

[Connect your laptop](/docs/trainees/connect-your-laptop) first. If the agent isn't running, the lab page says so and
links to **Connect your laptop**.

## Lighting the lab

Open the lab from its module and choose **Ignite the forge**. Crucible prepares the lab on your laptop and shows what
it is doing. Laptop labs cost nothing, so they need no approval.

## Tasks and checks

The tasks sit on the left, one numbered pip each. Read a task, do the work in the terminal, then choose **Check**.
A passed task stays passed. Tasks usually open one at a time, in order; some labs let you take them in any order.

- **Reset scenario** puts a task's starting state back if you broke it beyond repair.
- **Skip task** appears only when a task's setup failed, so it can't hold you up. Skipping costs nothing.
- **Hint** helps when you are stuck; see [Hints and extensions](/docs/trainees/hints-and-extensions).

Laptop labs are *self-reported*: the checks run on your own machine. Some programs ask a scorer to look over your
results before the lab counts as forged.

## Terminals

**+** opens another shell, **A−** and **A+** change the text size, and **Full screen** fills the window. Copy and
paste with Ctrl+Shift+C and Ctrl+Shift+V (Cmd+C and Cmd+V on a Mac). What your terminals print is recorded, and
scorers can read it.

To leave a terminal with the keyboard, press **Ctrl+Alt+↑**, or **Ctrl+Shift+F6** if your system takes that one.

## When the lab cools down

A lab runs for a set time (often two hours) and stops early if you leave it idle. Before an idle stop Crucible asks
*Are you still there?*; choose **I'm here** to keep going. The timer shows how long is left. **End lab** stops it
yourself. Your progress is saved either way, and **Ignite again** starts a fresh lab.
