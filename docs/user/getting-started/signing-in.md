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

1. Open Crucible. If you are not signed in, it sends you to your company sign-in page.
2. Sign in there. You come back to the Hearth.
3. Crucible knows you by your email address. Your identity provider must have verified it; if it hasn't, Crucible
   tells you so and does not let you in.

Your teams, trainings and roles all hang off that email address, so use the account your team leader enrolled.

## How long you stay signed in

A session lasts 12 hours. When it runs out, the next thing you do sends you back to the sign-in page, and you return
to the Hearth once you are through. Work you had saved stays saved; an answer you were still typing may not.

## Logging out

**Log out**, at the right end of the top bar, ends your Crucible session and takes you to the sign-in page. If your
company sign-in is still active in this browser, signing in again may need no password.
