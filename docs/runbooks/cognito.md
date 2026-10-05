# Sign-in with Amazon Cognito: setup and user management guide

Crucible signs people in through any standard OIDC provider. On AWS we use an **Amazon Cognito user pool**. It is free up to 10,000 monthly active users, invite-only, uses email as the username, and offers optional authenticator-app MFA. Local development keeps using the Keycloak container from `deploy/compose`; Crucible's code is identical for both.

> This guide describes what to do; nothing has been created in AWS yet. The steps assume M2 is implemented (`crucible aws …` exists). Section 7 shows the same setup done by hand in the AWS console if you prefer not to use Terraform for it.

---

## 1. What gets created

`crucible aws init` (persistent stack, file `deploy/aws/persistent/cognito.tf`) creates:

| Resource | Setting | Why |
|---|---|---|
| User pool `crucible` | Essentials tier | Modern "managed login" pages. Free ≤ 10,000 monthly active users, then $0.01/user. |
| | Email is the username | Matches the emails in the platform repo's `team.yaml` files |
| | Self sign-up **off** (admin invites only) | Only people you invite can get in |
| | MFA optional (authenticator app) | Users can turn it on; you can make it mandatory later (§5) |
| | Password ≥ 12 chars, upper + lower + number; temporary password valid 7 days | |
| | Account recovery by verified email | "Forgot password" works |
| | **Deletion protection on** + Terraform `prevent_destroy` | `crucible aws teardown` can never delete your users |
| Login domain | `https://crucible-<account-id>.auth.<region>.amazoncognito.com` | Hosts the sign-in pages |
| App client `crucible` | Authorization-code flow + PKCE, client secret, scopes `openid email profile` | What Crucible uses |
| | Callback `https://<domain>/auth/callback`, sign-out URL `https://<domain>/` | Must match Crucible's public URL exactly |
| Managed login style | Cognito defaults | Restyle it in the console (§6) |

The `crucible aws up` step then wires it into Crucible automatically:
- The issuer, `https://cognito-idp.<region>.amazonaws.com/<pool-id>`, and the client id are passed to the main stack.
- The client secret is read from Cognito by Terraform and stored in SSM Parameter Store as `/crucible/oidc_client_secret`. Nobody types or copies it.

---

## 2. Before you start

- AWS CLI v2 logged in to the target account with admin rights for Cognito, S3 and IAM.
- The **final hostname** for Crucible, e.g. `crucible.example.com`. The Cognito callback is built from it. If the hostname changes later, re-run step 3 with the new `--domain`.
- Terraform ≥ 1.10.

---

## 3. Create it

```bash
go run ./cmd/crucible aws init --region eu-west-1 --domain crucible.example.com
```

Terraform shows a plan containing the two buckets, the user pool, domain, app client and login style. Type `yes`.

Check it worked:
```bash
terraform -chdir=deploy/aws/persistent output
# cognito_login_url    = "https://crucible-123456789012.auth.eu-west-1.amazoncognito.com"
# cognito_user_pool_id = "eu-west-1_AbCdEfGhI"
# oidc_client_id       = "1h2j3k4l5m6n7o8p9q0r"
# oidc_issuer          = "https://cognito-idp.eu-west-1.amazonaws.com/eu-west-1_AbCdEfGhI"

curl -s "$(terraform -chdir=deploy/aws/persistent output -raw oidc_issuer)/.well-known/openid-configuration" | jq .issuer
# "https://cognito-idp.eu-west-1.amazonaws.com/eu-west-1_AbCdEfGhI"
```

Then continue with `crucible aws up` as described in `docs/runbooks/aws.md`.

---

## 4. Invite people

A person needs **two things** to use Crucible:
1. A **Cognito account**, which lets them sign in.
2. Their **email in a team** in the platform repo (`teams/<team>/team.yaml`, and `programs/*.yaml` for trainees). This decides what they can see and do.

The email must be the same in both places. Crucible compares emails in lowercase.

### Invite with the CLI
```bash
POOL=$(terraform -chdir=deploy/aws/persistent output -raw cognito_user_pool_id)

aws cognito-idp admin-create-user \
  --user-pool-id "$POOL" \
  --username alice@example.com \
  --user-attributes Name=email,Value=alice@example.com Name=email_verified,Value=true Name=name,Value="Alice Smith" \
  --desired-delivery-mediums EMAIL
```
Alice receives "You've been invited to Crucible" with a temporary password. On first sign-in she sets her own password and can optionally enrol an authenticator app.

Setting `name` makes Crucible greet her by name; without it Crucible shows her email.

### Invite in the console
Cognito → User pools → `crucible` → **Users** → **Create user**:
- Invitation message: *Send an email invitation*
- Email address: `alice@example.com`, tick *Mark email address as verified*
- Temporary password: *Generate a password*

### Then add them to a team
In the platform repo, for example `teams/forge/team.yaml` and `teams/forge/programs/forge-101.yaml`:
```yaml
trainees: [alice@example.com]
```
Commit and push. Crucible picks it up within the sync interval (60 s), or immediately if the git webhook is configured.

---

## 5. Everyday management

| Task | CLI | Console (User pools → `crucible` → Users) |
|---|---|---|
| Resend an expired invite | `aws cognito-idp admin-create-user --user-pool-id "$POOL" --username alice@example.com --message-action RESEND` | Select user → *Resend invitation* |
| Reset a password | `aws cognito-idp admin-reset-user-password --user-pool-id "$POOL" --username alice@example.com` | Select user → *Reset password* |
| Someone leaves: block sign-in | `aws cognito-idp admin-disable-user --user-pool-id "$POOL" --username alice@example.com`, then remove them from `team.yaml` | Select user → *Disable user access* |
| Delete for good | `aws cognito-idp admin-delete-user --user-pool-id "$POOL" --username alice@example.com` | Select user → *Delete* |
| List everyone | `aws cognito-idp list-users --user-pool-id "$POOL" --query 'Users[].Attributes[?Name==\`email\`].Value' --output text` | Users table |
| Make MFA mandatory | Change `mfa_configuration = "ON"` in `deploy/aws/persistent/cognito.tf`, re-run `crucible aws init …` | Sign-in tab → Multi-factor authentication → *Require MFA* |

Disabling someone in Cognito stops new sign-ins. Their current Crucible session lasts up to 12 hours, so also remove them from `team.yaml`: that removes their access to trainings immediately.

---

## 6. Optional: make the sign-in page look like the forge

Cognito → User pools → `crucible` → **Managed login** → select the style → **Edit**:
- Background: `#14110f`
- Primary button: `#ff7a1a`
- Upload the Crucible logo
- Page title: "Enter the Crucible"

These edits live in Cognito, not in Terraform. Terraform won't overwrite them, because it only created the style from defaults and doesn't manage its contents afterwards.

---

## 7. Alternative: create it by hand in the console

Use this if you'd rather not have Terraform own the user pool. Afterwards, give the values to the main stack yourself (end of this section).

1. Cognito → **Create user pool**. Choose *Traditional web application* and name the application `crucible`.
2. Sign-in identifier: **Email**. Self-registration: **off**. Required attributes for sign-up: email.
3. Return URL: `https://<domain>/auth/callback`. Create the pool.
4. App clients → `crucible`:
   - Confirm it has a **client secret**.
   - OAuth grant type: **Authorization code grant** only.
   - Scopes: `openid`, `email`, `profile`.
   - Allowed callback URL: `https://<domain>/auth/callback`; sign-out URL: `https://<domain>/`.
5. Sign-in tab:
   - MFA: *Optional*, authenticator apps.
   - Password policy: minimum 12 characters.
   - Account recovery: email.
6. Settings: turn on **Deletion protection**.
7. Note the pool id and app client id. The issuer is `https://cognito-idp.<region>.amazonaws.com/<pool-id>`.

To wire it in, delete `deploy/aws/persistent/cognito.tf`. Then add these outputs to `deploy/aws/persistent/main.tf` with your values, so `crucible aws up` passes them through unchanged:
```hcl
output "cognito_user_pool_id" { value = "eu-west-1_AbCdEfGhI" }
output "oidc_client_id"       { value = "1h2j3k4l5m6n7o8p9q0r" }
output "oidc_issuer"          { value = "https://cognito-idp.eu-west-1.amazonaws.com/eu-west-1_AbCdEfGhI" }
```

---

## 8. Limits worth knowing

- **Invitation emails:** Cognito's built-in email sender allows about 50 emails per day. That is plenty for under 100 people. If you ever need more, switch the pool's email to Amazon SES (Messaging tab → Email).
- **Sign-out:** Crucible's *Log out* ends the Crucible session. Your Cognito sign-in stays valid for about an hour, so clicking *Log in* again may let you straight back in without a password. Fully signing out of Cognito via its `/logout` endpoint is a small later improvement.
- **Region:** the user pool lives in the region you pass to `init`. Keep it the same as the node's region.

---

## 9. Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Cognito page says `redirect_mismatch` | Crucible's URL ≠ the callback registered in Cognito | Re-run `crucible aws init --region … --domain <exact hostname>` |
| Crucible shows "invalid id token" after sign-in | Issuer mismatch, e.g. a pool in another region | Check `terraform -chdir=deploy/aws/persistent output oidc_issuer` matches the region; re-run `crucible aws up` |
| Signed in, but the Hearth is empty | The email isn't enrolled in any program | Add the exact email to `team.yaml` and a `programs/*.yaml` |
| "Your identity provider did not send an email address" | The app client lacks the `email` scope | Restore the scopes `openid email profile` (Terraform does this) |
| Invite email never arrives | Daily email limit reached, or spam filter | Resend tomorrow, check spam, or switch to SES (§8) |
| `terraform destroy` refuses: "Instance cannot be destroyed" | Working as intended (`prevent_destroy`) | Only remove the user pool deliberately: turn off deletion protection in the console first, then edit `cognito.tf` |
