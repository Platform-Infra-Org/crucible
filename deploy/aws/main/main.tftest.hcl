mock_provider "aws" {
  mock_data "aws_vpc" {
    defaults = { id = "vpc-123" }
  }
  mock_data "aws_subnets" {
    defaults = { ids = ["subnet-a", "subnet-b"] }
  }
  mock_data "aws_ssm_parameter" {
    defaults = { insecure_value = "ami-123", value = "ami-123" }
  }
  mock_data "aws_iam_policy_document" {
    defaults = { json = "{}" }
  }
  mock_data "aws_cognito_user_pool_client" {
    defaults = { client_secret = "s3cret" }
  }
}
mock_provider "random" {}
override_resource {
  target          = random_password.db
  override_during = plan
  values          = { result = "dbpw-aaaaaaaa" }
}
override_resource {
  target          = random_password.hook
  override_during = plan
  values          = { result = "hookpw-bbbbbbbb" }
}
override_resource {
  target          = random_password.quiz
  override_during = plan
  values          = { result = "quizpw-cccccccc" }
}

variables {
  region               = "eu-west-1"
  data_bucket          = "crucible-123456789012-data"
  domain               = "crucible.example.com"
  acme_email           = "ops@example.com"
  platform_repo        = "https://git.example.com/crucible/platform.git"
  oidc_issuer          = "https://cognito-idp.eu-west-1.amazonaws.com/eu-west-1_abc"
  oidc_client_id       = "client123"
  cognito_user_pool_id = "eu-west-1_abc"
}

run "node_is_locked_down" {
  command = plan

  assert {
    condition     = aws_instance.node.metadata_options[0].http_tokens == "required" && aws_instance.node.metadata_options[0].http_put_response_hop_limit == 2
    error_message = "IMDSv2 required with hop limit 2"
  }
  assert {
    condition     = toset([for r in aws_security_group.web.ingress : r.from_port]) == toset([80, 443])
    error_message = "only ports 80 and 443 may be open"
  }
  assert {
    condition     = aws_instance.node.root_block_device[0].encrypted && aws_instance.node.root_block_device[0].volume_type == "gp3"
    error_message = "root volume must be encrypted gp3"
  }
  assert {
    condition     = !strcontains(aws_instance.node.user_data, "s3cret")
    error_message = "secrets must not appear in user data"
  }
  assert {
    condition     = !strcontains(aws_instance.node.user_data, random_password.db.result) && !strcontains(aws_instance.node.user_data, random_password.hook.result) && !strcontains(aws_instance.node.user_data, random_password.quiz.result)
    error_message = "generated passwords must not appear in user data"
  }
}

run "node_settings_come_from_ssm" {
  command = plan

  assert {
    condition     = aws_ssm_parameter.env.name == "/crucible/env" && aws_ssm_parameter.env.type == "String"
    error_message = "node settings must be a plain String parameter /<name>/env"
  }
  assert {
    condition     = strcontains(aws_ssm_parameter.env.value, "DOMAIN=crucible.example.com\n") && strcontains(aws_ssm_parameter.env.value, "BACKUP_CRON='15 19 * * *'\n")
    error_message = "env parameter must carry the node settings"
  }
  assert {
    condition     = strcontains(aws_instance.node.user_data, "--name '/crucible/env'") && !strcontains(aws_instance.node.user_data, "DOMAIN=")
    error_message = "bootstrap must fetch settings from SSM instead of baking them into user data"
  }
}

run "node_may_write_only_snapshots_latest_and_uploads" {
  command = plan

  assert {
    condition = toset(one([for s in data.aws_iam_policy_document.node.statement : s.resources if contains(s.actions, "s3:PutObject")])) == toset([
      "arn:aws:s3:::crucible-123456789012-data/snapshots/*",
      "arn:aws:s3:::crucible-123456789012-data/latest/crucible-latest.dump",
      "arn:aws:s3:::crucible-123456789012-data/uploads/*",
    ])
    error_message = "node may PutObject only on snapshots/*, latest/crucible-latest.dump and uploads/*"
  }
}

run "schedule_can_be_disabled" {
  command = plan
  variables {
    schedule_enabled = false
  }
  assert {
    condition     = length(aws_scheduler_schedule.sleep) == 0 && length(aws_scheduler_schedule.wake) == 0
    error_message = "no schedules when disabled"
  }
}

run "cluster_labs_use_sysbox" {
  command = plan

  assert {
    condition     = strcontains(aws_instance.node.user_data, "sysbox-ce_0.7.1.linux_amd64.deb") && strcontains(aws_instance.node.user_data, "config-v3.toml.tmpl") && strcontains(aws_instance.node.user_data, "sha256sum -c") && strcontains(aws_instance.node.user_data, "if install_sysbox; then")
    error_message = "the node installs a pinned, checksummed sysbox and registers it with k3s's containerd"
  }
  assert {
    condition     = strcontains(aws_instance.node.user_data, "kind: RuntimeClass") && strcontains(aws_instance.node.user_data, "handler: sysbox-runc")
    error_message = "the sysbox-runc RuntimeClass is auto-deployed by k3s"
  }
  assert {
    condition     = !strcontains(aws_instance.node.user_data, "unsafePrivileged") && !strcontains(aws_instance.node.user_data, "CRUCIBLE_CLUSTER_PRIVILEGED")
    error_message = "production never runs privileged lab pods"
  }
}
