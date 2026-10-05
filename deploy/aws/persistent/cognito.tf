# Invite-only sign-in for Crucible (see docs/runbooks/cognito.md). Lives here so teardown never deletes accounts.
resource "aws_cognito_user_pool" "users" {
  name                     = var.name
  user_pool_tier           = "ESSENTIALS" # managed login pages; free up to 10,000 monthly active users
  deletion_protection      = "ACTIVE"
  username_attributes      = ["email"]
  auto_verified_attributes = ["email"]
  mfa_configuration        = "OPTIONAL"

  software_token_mfa_configuration {
    enabled = true
  }

  admin_create_user_config {
    allow_admin_create_user_only = true
    invite_message_template {
      email_subject = "You've been invited to Crucible"
      email_message = "Welcome to the forge. Sign in at https://${var.domain} with {username} and the temporary password {####}. You'll choose your own password on first sign-in."
      sms_message   = "Crucible: {username} / {####}"
    }
  }

  password_policy {
    minimum_length                   = 12
    require_lowercase                = true
    require_uppercase                = true
    require_numbers                  = true
    require_symbols                  = false
    temporary_password_validity_days = 7
  }

  account_recovery_setting {
    recovery_mechanism {
      name     = "verified_email"
      priority = 1
    }
  }

  lifecycle { prevent_destroy = true }
}

resource "aws_cognito_user_pool_domain" "login" {
  domain                = "${var.name}-${data.aws_caller_identity.me.account_id}" # https://<this>.auth.<region>.amazoncognito.com
  user_pool_id          = aws_cognito_user_pool.users.id
  managed_login_version = 2
}

resource "aws_cognito_user_pool_client" "crucible" {
  name                                 = "crucible"
  user_pool_id                         = aws_cognito_user_pool.users.id
  generate_secret                      = true
  allowed_oauth_flows_user_pool_client = true
  allowed_oauth_flows                  = ["code"]
  allowed_oauth_scopes                 = ["openid", "email", "profile"]
  callback_urls                        = ["https://${var.domain}/auth/callback"]
  logout_urls                          = ["https://${var.domain}/"]
  supported_identity_providers         = ["COGNITO"]
  explicit_auth_flows                  = ["ALLOW_REFRESH_TOKEN_AUTH"]
  prevent_user_existence_errors        = "ENABLED"
  enable_token_revocation              = true
}

# Managed login needs a style assigned to the client; start from Cognito's defaults (restyle in the console).
resource "aws_cognito_managed_login_branding" "crucible" {
  user_pool_id                = aws_cognito_user_pool.users.id
  client_id                   = aws_cognito_user_pool_client.crucible.id
  use_cognito_provided_values = true
}

output "cognito_user_pool_id" { value = aws_cognito_user_pool.users.id }
output "oidc_client_id" { value = aws_cognito_user_pool_client.crucible.id }
output "oidc_issuer" { value = "https://cognito-idp.${var.region}.amazonaws.com/${aws_cognito_user_pool.users.id}" }
output "cognito_login_url" { value = "https://${aws_cognito_user_pool_domain.login.domain}.auth.${var.region}.amazoncognito.com" }
