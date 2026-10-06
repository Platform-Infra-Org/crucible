data "aws_vpc" "default" { default = true }

data "aws_subnets" "default" {
  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.default.id]
  }
  filter {
    name   = "default-for-az"
    values = ["true"]
  }
}

data "aws_ssm_parameter" "ubuntu" {
  name = "/aws/service/canonical/ubuntu/server/24.04/stable/current/amd64/hvm/ebs-gp3/ami-id"
}

resource "aws_security_group" "web" {
  name   = "${var.name}-web"
  vpc_id = data.aws_vpc.default.id
  ingress {
    description      = "HTTP (ACME challenge + redirect)"
    from_port        = 80
    to_port          = 80
    protocol         = "tcp"
    cidr_blocks      = ["0.0.0.0/0"]
    ipv6_cidr_blocks = ["::/0"]
  }
  ingress {
    description      = "HTTPS"
    from_port        = 443
    to_port          = 443
    protocol         = "tcp"
    cidr_blocks      = ["0.0.0.0/0"]
    ipv6_cidr_blocks = ["::/0"]
  }
  egress {
    from_port        = 0
    to_port          = 0
    protocol         = "-1"
    cidr_blocks      = ["0.0.0.0/0"]
    ipv6_cidr_blocks = ["::/0"]
  }
}

# --- secrets (SecureString, read by deploy.sh through the instance role) ---
data "aws_cognito_user_pool_client" "crucible" {
  user_pool_id = var.cognito_user_pool_id
  client_id    = var.oidc_client_id
}

resource "random_password" "db" {
  length  = 32
  special = false # used inside a postgres:// URL
}

resource "random_password" "quiz" {
  length  = 48
  special = false
}

resource "random_password" "hook" {
  length  = 32
  special = false
}

locals {
  params = {
    oidc_client_secret = data.aws_cognito_user_pool_client.crucible.client_secret
    platform_repo      = var.platform_repo
    git_credentials    = var.git_credentials == "" ? "none" : var.git_credentials # SSM rejects empty values
    git_hook_secret    = random_password.hook.result
    db_password        = random_password.db.result
    quiz_secret        = random_password.quiz.result
    infracost_api_key  = var.infracost_api_key == "" ? "none" : var.infracost_api_key
  }
}

# Non-secret node settings. deploy.sh re-reads this on every run, so tfvars changes reach a running node.
resource "aws_ssm_parameter" "env" {
  name  = "/${var.name}/env"
  type  = "String"
  value = <<-ENV
    REGION=${var.region}
    NAME=${var.name}
    DATA_BUCKET=${var.data_bucket}
    DOMAIN=${var.domain}
    PLATFORM_BRANCH=${var.platform_branch}
    OIDC_ISSUER=${var.oidc_issuer}
    OIDC_CLIENT_ID=${var.oidc_client_id}
    BACKUP_CRON='${var.backup_cron}'
    TIMEZONE=${var.schedule_timezone}
    GIT_BOT_NAME='${var.git_bot_name}'
    GIT_BOT_EMAIL=${var.git_bot_email}
    BOOTSTRAP_ADMIN='${var.bootstrap_admin}'
  ENV
}

# Helm values deploy.sh passes with -f. Changing them needs only `crucible aws up`, no node rebuild.
resource "aws_ssm_parameter" "helm_values" {
  name = "/${var.name}/helm-values"
  type = "String"
  value = yamlencode({
    awsLabs = {
      enabled     = var.lab_role_arn != ""
      labRoleArn  = var.lab_role_arn
      opsRoleArn  = var.lab_ops_role_arn
      stateBucket = var.lab_state_bucket
      stateRegion = var.lab_state_region
      regions     = var.lab_regions == "" ? var.region : var.lab_regions
    }
  })
}

resource "aws_ssm_parameter" "secret" {
  for_each = toset(["oidc_client_secret", "platform_repo", "git_credentials", "git_hook_secret", "db_password", "quiz_secret", "infracost_api_key"])
  name     = "/${var.name}/${each.key}"
  type     = "SecureString"
  value    = local.params[each.key]
}

# --- instance role ---
data "aws_iam_policy_document" "ec2_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

data "aws_iam_policy_document" "node" {
  statement {
    actions   = ["s3:ListBucket"]
    resources = ["arn:aws:s3:::${var.data_bucket}"]
  }
  statement {
    actions   = ["s3:GetObject"]
    resources = ["arn:aws:s3:::${var.data_bucket}/*"]
  }
  statement {
    actions   = ["s3:PutObject"]
    resources = ["arn:aws:s3:::${var.data_bucket}/snapshots/*", "arn:aws:s3:::${var.data_bucket}/latest/crucible-latest.dump", "arn:aws:s3:::${var.data_bucket}/uploads/*"]
  }
  statement {
    actions   = ["ssm:GetParameter", "ssm:GetParameters"]
    resources = ["arn:aws:ssm:${var.region}:*:parameter/${var.name}/*"]
  }
  dynamic "statement" {
    for_each = var.lab_role_arn == "" ? [] : [1]
    content {
      actions   = ["sts:AssumeRole", "sts:TagSession"] # lab sessions carry crucible:* tags
      resources = [var.lab_role_arn, var.lab_ops_role_arn]
    }
  }
}

resource "aws_iam_role" "node" {
  name               = "${var.name}-node"
  assume_role_policy = data.aws_iam_policy_document.ec2_assume.json
}

resource "aws_iam_role_policy_attachment" "ssm_core" {
  role       = aws_iam_role.node.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

resource "aws_iam_role_policy" "node" {
  role   = aws_iam_role.node.id
  policy = data.aws_iam_policy_document.node.json
}

resource "aws_iam_instance_profile" "node" {
  name = "${var.name}-node"
  role = aws_iam_role.node.name
}

# --- the node ---
resource "aws_instance" "node" {
  ami                    = data.aws_ssm_parameter.ubuntu.insecure_value
  instance_type          = var.instance_type
  subnet_id              = sort(data.aws_subnets.default.ids)[0]
  vpc_security_group_ids = [aws_security_group.web.id]
  iam_instance_profile   = aws_iam_instance_profile.node.name

  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 2 # backup/restore pods use the instance role
  }

  root_block_device {
    volume_type = "gp3"
    volume_size = var.disk_gb
    encrypted   = true
  }

  user_data = templatefile("${path.module}/bootstrap.sh.tftpl", {
    region      = var.region
    name        = var.name
    acme_email  = var.acme_email
    k3s_version = var.k3s_version
    timezone    = var.schedule_timezone

    sysbox_version      = var.sysbox_version
    sysbox_sha256_amd64 = var.sysbox_sha256_amd64
  })

  tags = { Name = var.name }

  depends_on = [aws_ssm_parameter.env, aws_ssm_parameter.secret, aws_ssm_parameter.helm_values, aws_iam_role_policy.node]

  lifecycle {
    ignore_changes = [ami, user_data] # never replace the node because Ubuntu published a new AMI
  }
}

resource "aws_eip" "web" {
  instance = aws_instance.node.id
  domain   = "vpc"
}

resource "aws_route53_record" "web" {
  count   = var.route53_zone_id == "" ? 0 : 1
  zone_id = var.route53_zone_id
  name    = var.domain
  type    = "A"
  ttl     = 300
  records = [aws_eip.web.public_ip]
}
