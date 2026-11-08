mock_provider "aws" {
  mock_data "aws_vpc" {
    defaults = { id = "vpc-123" }
  }
  mock_data "aws_subnet" {
    defaults = { vpc_id = "vpc-spoke" }
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
  oidc_issuer          = "https://cognito-idp.eu-west-1.amazonaws.com/eu-west-1_abc"
  oidc_client_id       = "client123"
  cognito_user_pool_id = "eu-west-1_abc"
  ghcr_pull_token      = "ghp_test"
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

run "bootstrap_admin_reaches_helm" {
  command = plan
  variables {
    bootstrap_admin = "boss@example.com"
  }
  assert {
    condition     = strcontains(aws_ssm_parameter.env.value, "BOOTSTRAP_ADMIN='boss@example.com'\n") && strcontains(aws_instance.node.user_data, "bootstrapAdmin=\"$BOOTSTRAP_ADMIN\"")
    error_message = "bootstrap_admin must reach the helm install"
  }
}

run "bootstrap_admin_must_be_an_email" {
  command = plan
  variables {
    bootstrap_admin = "not an email"
  }
  expect_failures = [var.bootstrap_admin]
}

run "bootstrap_admin_rejects_shell_characters" {
  command = plan
  variables {
    bootstrap_admin = "x$(id)@example.com"
  }
  expect_failures = [var.bootstrap_admin]
}

run "bootstrap_admin_rejects_a_quote" {
  command = plan
  variables {
    bootstrap_admin = "o'brien@example.com"
  }
  expect_failures = [var.bootstrap_admin]
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

run "aws_labs_off_by_default" {
  command = plan

  assert {
    condition     = yamldecode(aws_ssm_parameter.helm_values.value).awsLabs.enabled == false
    error_message = "aws labs stay off until the labs stack exists"
  }
  assert {
    condition     = !anytrue([for s in data.aws_iam_policy_document.node.statement : contains(s.actions, "sts:AssumeRole")])
    error_message = "the node assumes no lab role by default"
  }
  assert {
    condition     = strcontains(aws_instance.node.user_data, "/helm-values") && strcontains(aws_instance.node.user_data, "-f \"$work/values.yaml\"")
    error_message = "deploy.sh passes the helm values from SSM"
  }
  assert {
    condition     = toset(keys(aws_ssm_parameter.secret)) == toset(["oidc_client_secret", "git_credentials", "git_hook_secret", "db_password", "quiz_secret", "ghcr_pull_token"])
    error_message = "Crucible prices aws labs itself from the free AWS Price List: no pricing API key among the secrets"
  }
}

run "aws_labs_wiring" {
  command = plan
  variables {
    lab_role_arn      = "arn:aws:iam::444455556666:role/crucible-lab"
    lab_ops_role_arn  = "arn:aws:iam::444455556666:role/crucible-lab-ops"
    lab_state_bucket  = "crucible-444455556666-labstate"
    lab_state_region  = "eu-west-1"
    lab_regions       = "eu-west-1"
    lab_plan_role_arn = "arn:aws:iam::444455556666:role/crucible-lab-plan"
    lab_export_bucket = "crucible-444455556666-exports"
    lab_export_prefix = "cur"
    lab_export_name   = "crucible-labs"
  }

  assert {
    condition     = yamldecode(aws_ssm_parameter.helm_values.value).awsLabs == { enabled = true, labRoleArn = "arn:aws:iam::444455556666:role/crucible-lab", opsRoleArn = "arn:aws:iam::444455556666:role/crucible-lab-ops", stateBucket = "crucible-444455556666-labstate", stateRegion = "eu-west-1", regions = "eu-west-1", planRoleArn = "arn:aws:iam::444455556666:role/crucible-lab-plan", exportBucket = "crucible-444455556666-exports", exportPrefix = "cur", exportName = "crucible-labs", vpcId = "", subnetIds = "", instanceProfile = "", subnetCidrs = "" }
    error_message = "helm values carry the labs stack outputs"
  }
  assert {
    condition     = toset(one([for s in data.aws_iam_policy_document.node.statement : s.resources if contains(s.actions, "sts:TagSession")])) == toset(["arn:aws:iam::444455556666:role/crucible-lab", "arn:aws:iam::444455556666:role/crucible-lab-ops"])
    error_message = "the node may assume and tag sessions of exactly the lab and ops roles"
  }
  assert {
    condition     = toset(one([for s in data.aws_iam_policy_document.node.statement : s.resources if contains(s.actions, "sts:AssumeRole") && !contains(s.actions, "sts:TagSession")])) == toset(["arn:aws:iam::444455556666:role/crucible-lab-plan"])
    error_message = "the plan role has its own AssumeRole-only statement (estimates use no session tags)"
  }
  assert {
    condition     = output.lab_role_arn == "arn:aws:iam::444455556666:role/crucible-lab"
    error_message = "main records the lab role it is wired to, so `up` can refuse to drop it silently"
  }
  assert {
    condition     = output.node_role_name == "crucible-node"
    error_message = "main publishes its node role name; labs-init trusts exactly that role"
  }
}

run "aws_labs_need_the_plan_role" {
  command = plan
  variables {
    lab_role_arn      = "arn:aws:iam::444455556666:role/crucible-lab"
    lab_ops_role_arn  = "arn:aws:iam::444455556666:role/crucible-lab-ops"
    lab_state_bucket  = "crucible-444455556666-labstate"
    lab_state_region  = "eu-west-1"
    lab_export_bucket = "crucible-444455556666-exports"
    lab_export_name   = "crucible-labs"
  }
  expect_failures = [var.lab_role_arn]
}

# A labs stack made with --billing-export=false (an SCP denies the export) has empty export outputs: billed spend is off.
run "aws_labs_without_a_billing_export" {
  command = plan
  variables {
    lab_role_arn      = "arn:aws:iam::444455556666:role/crucible-lab"
    lab_ops_role_arn  = "arn:aws:iam::444455556666:role/crucible-lab-ops"
    lab_state_bucket  = "crucible-444455556666-labstate"
    lab_state_region  = "eu-west-1"
    lab_plan_role_arn = "arn:aws:iam::444455556666:role/crucible-lab-plan"
  }
  assert {
    condition     = yamldecode(aws_ssm_parameter.helm_values.value).awsLabs.enabled && yamldecode(aws_ssm_parameter.helm_values.value).awsLabs.exportBucket == "" && yamldecode(aws_ssm_parameter.helm_values.value).awsLabs.exportName == ""
    error_message = "aws labs run without a billing export"
  }
}

run "secret_key_reaches_the_api_when_init_made_it" {
  command = plan

  assert {
    condition     = strcontains(aws_instance.node.user_data, "param secret_key") && strcontains(aws_instance.node.user_data, "--from-literal=CRUCIBLE_SECRET_KEY=")
    error_message = "deploy.sh must put /<name>/secret_key into crucible-secrets as CRUCIBLE_SECRET_KEY"
  }
  assert {
    condition     = strcontains(aws_instance.node.user_data, "ParameterNotFound") && strcontains(aws_instance.node.user_data, "re-run 'crucible aws init'")
    error_message = "an install from before secret_key existed must deploy without it and say how to add it"
  }
  assert {
    condition     = contains(flatten([for s in data.aws_iam_policy_document.node.statement : s.resources if contains(s.actions, "ssm:GetParameter")]), "arn:aws:ssm:eu-west-1:*:parameter/crucible/*")
    error_message = "the node role must be able to read /<name>/secret_key"
  }
}

run "no_toolbox_image_keeps_the_release_pin" {
  command = plan

  assert {
    condition     = !contains(keys(yamldecode(aws_ssm_parameter.helm_values.value)), "toolboxImage")
    error_message = "an empty toolbox_image leaves the release's pinned toolbox (chart values): it must not override it with \"\""
  }
}

run "toolbox_image_reaches_helm" {
  command = plan
  variables {
    toolbox_image = "ghcr.io/example/crucible-toolbox@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
  }
  assert {
    condition     = yamldecode(aws_ssm_parameter.helm_values.value).toolboxImage == "ghcr.io/example/crucible-toolbox@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    error_message = "toolbox_image must reach the helm values deploy.sh passes (toolboxImage)"
  }
}

run "toolbox_image_must_be_pinned_by_digest" {
  command = plan
  variables {
    toolbox_image = "ghcr.io/example/crucible-toolbox:latest"
  }
  expect_failures = [var.toolbox_image]
}

run "toolbox_image_rejects_a_short_digest" {
  command = plan
  variables {
    toolbox_image = "ghcr.io/example/crucible-toolbox@sha256:0123"
  }
  expect_failures = [var.toolbox_image]
}

run "name_is_an_output" {
  command = plan
  assert {
    condition     = output.name == "crucible"
    error_message = "the name output carries var.name (the release pointer's SSM path)"
  }
}

run "ghcr_pull_token_rejects_a_quote" {
  command = plan
  variables {
    ghcr_pull_token = "ghp_te'st"
  }
  expect_failures = [var.ghcr_pull_token]
}

run "smtp_off_by_default_keeps_hand_added_keys" {
  command = plan

  assert {
    condition     = strcontains(aws_ssm_parameter.env.value, "SMTP_ADDR=''\n") && strcontains(aws_ssm_parameter.env.value, "SMTP_PASSWORD_SET=''\n")
    error_message = "no SMTP settings by default"
  }
  assert {
    condition     = length(aws_ssm_parameter.smtp_password) == 0
    error_message = "no SMTP password parameter unless one is set (SSM rejects empty values)"
  }
  assert {
    condition     = strcontains(aws_instance.node.user_data, "if [ -n \"$${SMTP_ADDR:-}\" ]; then smtp+=(--from-literal=CRUCIBLE_SMTP_ADDR=\"$SMTP_ADDR\"); fi") && strcontains(aws_instance.node.user_data, "\"$${smtp[@]}\"")
    error_message = "deploy.sh adds each SMTP key only when it is set, so keys an operator added by hand survive"
  }
}

run "smtp_settings_reach_crucible_secrets" {
  command = plan
  variables {
    smtp_addr     = "email-smtp.eu-west-1.amazonaws.com:587"
    smtp_from     = "Crucible <crucible@example.com>"
    smtp_username = "AKIAEXAMPLE"
    smtp_password = "smtp-pw-dddddddd"
  }
  assert {
    condition     = strcontains(aws_ssm_parameter.env.value, "SMTP_ADDR='email-smtp.eu-west-1.amazonaws.com:587'\n") && strcontains(aws_ssm_parameter.env.value, "SMTP_FROM='Crucible <crucible@example.com>'\n") && strcontains(aws_ssm_parameter.env.value, "SMTP_USERNAME='AKIAEXAMPLE'\n") && strcontains(aws_ssm_parameter.env.value, "SMTP_PASSWORD_SET='1'\n")
    error_message = "the plain SMTP settings go into /<name>/env"
  }
  assert {
    condition     = !strcontains(aws_ssm_parameter.env.value, "smtp-pw-dddddddd") && !strcontains(aws_ssm_parameter.helm_values.value, "smtp-pw-dddddddd") && !strcontains(aws_instance.node.user_data, "smtp-pw-dddddddd")
    error_message = "the SMTP password stays out of plain parameters and user data"
  }
  assert {
    condition     = aws_ssm_parameter.smtp_password[0].name == "/crucible/smtp_password" && aws_ssm_parameter.smtp_password[0].type == "SecureString" && aws_ssm_parameter.smtp_password[0].value == "smtp-pw-dddddddd"
    error_message = "the SMTP password is a SecureString /<name>/smtp_password"
  }
  assert {
    condition     = strcontains(aws_instance.node.user_data, "--from-literal=CRUCIBLE_SMTP_PASSWORD=\"$(param smtp_password)\"") && strcontains(aws_instance.node.user_data, "--from-literal=CRUCIBLE_SMTP_FROM=") && strcontains(aws_instance.node.user_data, "--from-literal=CRUCIBLE_SMTP_USERNAME=")
    error_message = "deploy.sh puts the SMTP settings into crucible-secrets"
  }
}

run "smtp_addr_must_be_host_port" {
  command = plan
  variables {
    smtp_addr = "smtp.example.com"
  }
  expect_failures = [var.smtp_addr]
}

run "smtp_from_rejects_a_quote" {
  command = plan
  variables {
    smtp_from = "x'; reboot; '"
  }
  expect_failures = [var.smtp_from]
}

run "public_access_is_unchanged" {
  command = plan

  assert {
    condition     = length(aws_eip.web) == 1 && output.url == "https://crucible.example.com" && output.access == "public"
    error_message = "public keeps its Elastic IP and https://<domain>"
  }
  assert {
    condition     = join(",", output.web_ingress_ports) == "443,80"
    error_message = "public opens exactly ports 80 and 443"
  }
  assert {
    condition     = strcontains(aws_instance.node.user_data, "certificatesResolvers") && strcontains(aws_instance.node.user_data, "redirections")
    error_message = "public keeps Traefik's Let's Encrypt resolver and HTTPS redirect"
  }
  assert {
    condition     = strcontains(aws_ssm_parameter.env.value, "ACCESS=public\n") && strcontains(aws_ssm_parameter.env.value, "PUBLIC_URL=https://crucible.example.com\n") && strcontains(aws_ssm_parameter.env.value, "HOST=crucible.example.com\n") && strcontains(aws_ssm_parameter.env.value, "TLS_ENABLED=true\n")
    error_message = "the node's env carries the public URL, host and TLS"
  }
  assert {
    condition     = aws_instance.node.tags["crucible:public-url"] == "https://crucible.example.com"
    error_message = "the node is tagged with its address"
  }
}

run "ssm_access_has_no_way_in" {
  command = plan
  variables {
    access     = "ssm"
    public_url = "http://localhost:18080"
    subnet_id  = "subnet-0fa66f46aa023ab6e"
    domain     = ""
    acme_email = ""
  }

  assert {
    condition     = length(aws_eip.web) == 0 && length(aws_route53_record.web) == 0 && output.public_ip == ""
    error_message = "ssm creates no Elastic IP and no DNS record"
  }
  assert {
    condition     = length(output.web_ingress_ports) == 0
    error_message = "access = ssm must open no inbound port"
  }
  assert {
    condition     = aws_security_group.web.vpc_id == "vpc-spoke"
    error_message = "the security group must live in the given subnet's VPC"
  }
  assert {
    condition     = aws_instance.node.subnet_id == "subnet-0fa66f46aa023ab6e" && aws_instance.node.associate_public_ip_address == false
    error_message = "the node runs in the given subnet without a public IP"
  }
  assert {
    condition     = aws_instance.node.tags["crucible:public-url"] == "http://localhost:18080" && output.url == "http://localhost:18080" && output.access == "ssm"
    error_message = "the node carries its localhost address for `crucible aws connect`"
  }
  assert {
    condition     = strcontains(aws_ssm_parameter.env.value, "PUBLIC_URL=http://localhost:18080\n") && strcontains(aws_ssm_parameter.env.value, "HOST=\n") && strcontains(aws_ssm_parameter.env.value, "TLS_ENABLED=false\n")
    error_message = "ssm: plain HTTP, any host"
  }
  assert {
    condition     = !strcontains(aws_instance.node.user_data, "certificatesResolvers") && !strcontains(aws_instance.node.user_data, "redirections")
    error_message = "ssm: no Let's Encrypt, no HTTPS redirect"
  }
}

run "ssm_access_needs_a_subnet" {
  command = plan
  variables {
    access     = "ssm"
    public_url = "http://localhost:18080"
    domain     = ""
    acme_email = ""
  }
  expect_failures = [var.subnet_id]
}

run "public_access_needs_a_domain" {
  command = plan
  variables {
    domain = ""
  }
  expect_failures = [var.domain]
}

run "public_url_defaults_to_https_domain_for_old_stacks" {
  command = plan
  variables {
    public_url = ""
  }

  assert {
    condition     = output.url == "https://crucible.example.com"
    error_message = "a persistent stack made before access existed passes no public_url: main builds https://<domain>"
  }
}

run "deploy_script_reads_the_url_from_env" {
  command = plan

  assert {
    condition     = strcontains(aws_instance.node.user_data, "publicURL=\"$${PUBLIC_URL:-https://$DOMAIN}\"") && strcontains(aws_instance.node.user_data, "host=\"$${HOST-$DOMAIN}\"") && strcontains(aws_instance.node.user_data, "tls.enabled=\"$${TLS_ENABLED:-true}\"")
    error_message = "deploy.sh takes the URL, host and TLS from /<name>/env, falling back to today's https://$DOMAIN"
  }
}

run "lab_network_reaches_helm" {
  command = plan
  variables {
    lab_role_arn      = "arn:aws:iam::444455556666:role/crucible-lab"
    lab_ops_role_arn  = "arn:aws:iam::444455556666:role/crucible-lab-ops"
    lab_state_bucket  = "crucible-444455556666-labstate"
    lab_state_region  = "il-central-1"
    lab_plan_role_arn = "arn:aws:iam::444455556666:role/crucible-lab-plan"
    lab_export_bucket = "crucible-444455556666-exports"
    lab_export_name   = "crucible-labs"
    lab_vpc_id        = "vpc-0abc"
    lab_subnet_ids    = "subnet-01,subnet-02"
  }

  assert {
    condition     = yamldecode(aws_ssm_parameter.helm_values.value).awsLabs.vpcId == "vpc-0abc" && yamldecode(aws_ssm_parameter.helm_values.value).awsLabs.subnetIds == "subnet-01,subnet-02"
    error_message = "the lab network reaches the Helm values"
  }
}

run "os_lab_inputs_reach_helm" {
  command = plan
  variables {
    lab_role_arn         = "arn:aws:iam::444455556666:role/crucible-lab"
    lab_ops_role_arn     = "arn:aws:iam::444455556666:role/crucible-lab-ops"
    lab_state_bucket     = "crucible-444455556666-labstate"
    lab_state_region     = "il-central-1"
    lab_plan_role_arn    = "arn:aws:iam::444455556666:role/crucible-lab-plan"
    lab_export_bucket    = "crucible-444455556666-exports"
    lab_export_name      = "crucible-labs"
    lab_instance_profile = "crucible-lab-instance"
    lab_subnet_cidrs     = "10.2.129.0/24"
    workspace_image      = "ghcr.io/platform-infra-org/crucible-workspace@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  }

  assert {
    condition     = yamldecode(aws_ssm_parameter.helm_values.value).awsLabs.instanceProfile == "crucible-lab-instance" && yamldecode(aws_ssm_parameter.helm_values.value).awsLabs.subnetCidrs == "10.2.129.0/24" && yamldecode(aws_ssm_parameter.helm_values.value).awsLabs.workspaceImage == var.workspace_image
    error_message = "the lab instance profile, subnet CIDRs and workspace image reach the Helm values"
  }
}

run "workspace_image_must_be_a_digest" {
  command = plan
  variables {
    workspace_image = "ghcr.io/x/y:latest"
  }
  expect_failures = [var.workspace_image]
}

run "no_workspace_image_keeps_the_chart_default" {
  command = plan
  variables {
    lab_role_arn      = "arn:aws:iam::444455556666:role/crucible-lab"
    lab_ops_role_arn  = "arn:aws:iam::444455556666:role/crucible-lab-ops"
    lab_state_bucket  = "crucible-444455556666-labstate"
    lab_state_region  = "il-central-1"
    lab_plan_role_arn = "arn:aws:iam::444455556666:role/crucible-lab-plan"
    lab_export_bucket = "crucible-444455556666-exports"
    lab_export_name   = "crucible-labs"
  }

  assert {
    condition     = !contains(keys(yamldecode(aws_ssm_parameter.helm_values.value).awsLabs), "workspaceImage")
    error_message = "an empty workspace_image leaves the chart's default"
  }
}

run "node_installs_a_ghcr_release_with_the_pull_token" {
  command = plan

  assert {
    condition     = aws_ssm_parameter.secret["ghcr_pull_token"].type == "SecureString" && aws_ssm_parameter.secret["ghcr_pull_token"].name == "/crucible/ghcr_pull_token"
    error_message = "the GHCR pull token is a SecureString at /<name>/ghcr_pull_token"
  }
  assert {
    condition     = strcontains(aws_instance.node.user_data, "GHCR refused the pull token: set a GitHub token with read:packages as ghcr_pull_token in crucible.tfvars and run crucible aws up") && strcontains(aws_instance.node.user_data, "cannot pull chart $version from GHCR: check the release exists and ghcr_pull_token has read:packages")
    error_message = "a bad token or a missing chart version stops deploy.sh with an actionable message"
  }
  assert {
    condition     = !strcontains(aws_instance.node.user_data, "--docker-password") && strcontains(aws_instance.node.user_data, "--type=kubernetes.io/dockerconfigjson") && strcontains(aws_instance.node.user_data, "helm registry logout ghcr.io >/dev/null 2>&1 || true; rm -rf")
    error_message = "the token stays out of argv; logout runs in the EXIT trap"
  }
  assert {
    condition     = !strcontains(aws_instance.node.user_data, "ghp_test")
    error_message = "the token must not appear in user data"
  }
  assert {
    condition     = alltrue([for s in ["oci://ghcr.io/platform-infra-org/charts/crucible", "helm registry login ghcr.io", "/$NAME/release", "ghcr-pull", "no release chosen: run crucible aws deploy"] : strcontains(aws_instance.node.user_data, s)])
    error_message = "deploy.sh installs the chart from GHCR at the version in /<name>/release, logged in with the pull token"
  }
  assert {
    condition     = !strcontains(aws_instance.node.user_data, "current-release") && !strcontains(aws_instance.node.user_data, "$DATA_BUCKET/releases") && !strcontains(aws_instance.node.user_data, "ctr images import")
    error_message = "no release tarballs in S3"
  }
  assert {
    condition     = alltrue([for first in ["helm registry login", "helm show chart oci://ghcr.io/platform-infra-org/charts/crucible"] : can(regex("(?s)${first}.*kubectl create namespace", aws_instance.node.user_data)) && !can(regex("(?s)kubectl create namespace.*${first}", aws_instance.node.user_data))])
    error_message = "the token is checked before anything is applied"
  }
  assert {
    condition     = alltrue([for s in data.aws_iam_policy_document.node.statement : alltrue([for r in s.resources : !strcontains(r, "releases/")])])
    error_message = "the node policy has no release-specific resources"
  }
}

run "ghcr_pull_token_is_required" {
  command = plan
  variables {
    ghcr_pull_token = ""
  }
  expect_failures = [var.ghcr_pull_token]
}

run "labs_sleep_cron_reaches_helm" {
  command = plan
  assert {
    condition     = yamldecode(aws_ssm_parameter.helm_values.value).labs.sleepSchedule == "0 19 * * 1-5"
    error_message = "the nightly bedtime reaches the chart"
  }
  assert {
    condition     = aws_instance.node.root_block_device[0].volume_size == 100
    error_message = "the node disk holds sleeping workspaces: 100 GB by default"
  }
}

run "no_bedtime_without_the_schedule" {
  command = plan
  variables { schedule_enabled = false }
  assert {
    condition     = yamldecode(aws_ssm_parameter.helm_values.value).labs.sleepSchedule == ""
    error_message = "no schedule, no nightly bedtime"
  }
}

run "labs_sleep_cron_is_a_kubernetes_cron" {
  command = plan
  variables { labs_sleep_cron = "cron(0 19 ? * MON-FRI *)" }
  expect_failures = [var.labs_sleep_cron]
}
