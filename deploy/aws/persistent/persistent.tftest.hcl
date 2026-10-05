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
