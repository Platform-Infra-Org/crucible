---
title: Signing in
roles: [everyone]
covers: [feature:auth]
order: 10
---
# Signing in

Crucible has no passwords of its own. You sign in with your company account, through your company's identity
provider (Keycloak or AWS Cognito, depending on how Crucible was set up).

## How it works

1. Open Crucible. If you are not signed in, you see the forge's gate: a hammer forging ore into a masterwork, through
   the six forge ranks. (With Calm forge or your system's reduced motion, the masterwork rests on the anvil instead.)
2. Choose **Enter the forge**. Crucible sends you to your company sign-in page.
3. Sign in there. You come back to the Hearth.
3. Crucible knows you by your email address. Your identity provider must have verified it; if it hasn't, Crucible
   tells you so and does not let you in.

Your teams, trainings and roles all hang off that email address, so use the account your team leader enrolled.

## How long you stay signed in

A session lasts 12 hours. When it runs out, the next thing you do sends you back to the sign-in page, and you return
to the Hearth once you are through. Work you had saved stays saved; an answer you were still typing may not.

## Your user card

Your icon and name sit at the right end of the top bar. Choose them to open your user card: your name, your email,
and the teams you are on with your role in each. Choose a team's name to open its page.

**Your icon** is what the card and the top bar show for you: your initials, or one of the forge's icons — a hammer,
an anvil, a flame, a sword, a shield, tongs, a helm or an ingot. Choose one and it is kept for you on every device.

## Logging out

**Log out**, at the bottom of your user card, ends your Crucible session and takes you back to the gate. If your
company sign-in is still active in this browser, **Enter the forge** may let you straight back in without a password.
