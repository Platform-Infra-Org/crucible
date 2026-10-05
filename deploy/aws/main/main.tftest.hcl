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
