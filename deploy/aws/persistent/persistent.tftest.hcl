mock_provider "aws" {
  mock_data "aws_caller_identity" {
    defaults = { account_id = "123456789012" }
  }
}

variables {
  region = "eu-west-1"
  domain = "crucible.example.com"
}

run "buckets_are_private_versioned_and_expiring" {
  command = plan

  assert {
    condition     = aws_s3_bucket.data.bucket == "crucible-123456789012-data"
    error_message = "data bucket name"
  }
  assert {
    condition     = aws_s3_bucket_versioning.data.versioning_configuration[0].status == "Enabled"
    error_message = "data bucket must be versioned"
  }
  assert {
    condition     = aws_s3_bucket_public_access_block.data.block_public_acls && aws_s3_bucket_public_access_block.data.restrict_public_buckets
    error_message = "data bucket must block public access"
  }
  assert {
    condition     = one([for r in aws_s3_bucket_lifecycle_configuration.data.rule : r.expiration[0].days if r.id == "expire-snapshots"]) == 30
    error_message = "snapshots expire after 30 days"
  }
  assert {
    condition     = one([for r in aws_s3_bucket_lifecycle_configuration.data.rule : length(r.expiration) if r.filter[0].prefix == "latest/"]) == 0
    error_message = "the latest/ snapshot copy must never expire"
  }
}

run "cognito_is_invite_only_and_protected" {
  command = plan

  assert {
    condition     = aws_cognito_user_pool.users.admin_create_user_config[0].allow_admin_create_user_only
    error_message = "self sign-up must be disabled (invite-only)"
  }
  assert {
    condition     = aws_cognito_user_pool.users.deletion_protection == "ACTIVE"
    error_message = "the user pool must have deletion protection"
  }
  assert {
    condition     = toset(aws_cognito_user_pool_client.crucible.callback_urls) == toset(["https://crucible.example.com/auth/callback"])
    error_message = "callback must be exactly https://<domain>/auth/callback"
  }
  assert {
    condition     = toset(aws_cognito_user_pool_client.crucible.allowed_oauth_flows) == toset(["code"]) && aws_cognito_user_pool_client.crucible.generate_secret
    error_message = "authorization-code flow with a client secret only"
  }
}

run "secret_key_outlives_the_main_stack" {
  command = plan

  assert {
    condition     = aws_ssm_parameter.secret_key.name == "/crucible/secret_key" && aws_ssm_parameter.secret_key.type == "SecureString"
    error_message = "CRUCIBLE_SECRET_KEY must be the SecureString /<name>/secret_key, beside the node's other secrets"
  }
  assert {
    condition     = random_password.secret_key.length == 48 && !random_password.secret_key.special
    error_message = "the secret key is 48 characters with no special characters"
  }
}

run "public_access_is_unchanged" {
  command = plan

  assert {
    condition     = output.access == "public" && output.public_url == "https://crucible.example.com" && output.domain == "crucible.example.com"
    error_message = "public is the default and its URL is https://<domain>"
  }
}

run "ssm_access_uses_a_localhost_url" {
  command = plan
  variables {
    access = "ssm"
    domain = ""
  }

  assert {
    condition     = output.public_url == "http://localhost:18080" && output.local_port == 18080
    error_message = "ssm's URL is http://localhost:18080 by default"
  }
  assert {
    condition     = toset(aws_cognito_user_pool_client.crucible.callback_urls) == toset(["http://localhost:18080/auth/callback"]) && toset(aws_cognito_user_pool_client.crucible.logout_urls) == toset(["http://localhost:18080/"])
    error_message = "Cognito's callback and logout follow the public URL"
  }
  assert {
    condition     = strcontains(aws_cognito_user_pool.users.admin_create_user_config[0].invite_message_template[0].email_message, "http://localhost:18080") && strcontains(aws_cognito_user_pool.users.admin_create_user_config[0].invite_message_template[0].email_message, "crucible aws connect")
    error_message = "the invite names the localhost address and how to open the tunnel"
  }
}

run "ssm_access_ignores_a_domain" {
  command = plan
  variables {
    access     = "ssm"
    local_port = 18443
  }

  assert {
    condition     = output.public_url == "http://localhost:18443"
    error_message = "ssm ignores the domain and uses the chosen port"
  }
}

run "public_access_needs_a_domain" {
  command = plan
  variables {
    domain = ""
  }
  expect_failures = [var.domain]
}

run "local_port_must_be_a_user_port" {
  command = plan
  variables {
    access     = "ssm"
    local_port = 80
  }
  expect_failures = [var.local_port]
}

run "access_is_public_or_ssm" {
  command = plan
  variables {
    access = "vpn"
  }
  expect_failures = [var.access]
}
