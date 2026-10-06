# The shared AWS lab account (spec §2, §8.2). Apply once with `crucible aws labs-init` in the account labs run in
# (the Crucible account by default; a dedicated sandbox account is safer: set crucible_account_id). Local state,
# like deploy/aws/persistent: back it up. Never destroyed by `crucible aws teardown`.
terraform {
  required_version = ">= 1.10"
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 6.0" }
  }
}

provider "aws" {
  region = var.region
  default_tags { tags = { app = "crucible" } }
}

variable "region" {
  type        = string
  description = "Region of the lab state bucket; also the only lab region unless allowed_regions is set"
}
variable "name" {
  type    = string
  default = "crucible"
}
variable "crucible_account_id" {
  type        = string
  default     = ""
  description = "Account whose <name>-node role runs Crucible. Empty = this account."
}
variable "allowed_regions" {
  type    = list(string)
  default = []
}
variable "allowed_instance_types" {
  type    = list(string)
  default = ["t3.nano", "t3.micro", "t3a.nano", "t3a.micro", "t4g.nano", "t4g.micro"]
}

data "aws_caller_identity" "me" {}

locals {
  account   = data.aws_caller_identity.me.account_id
  crucible  = var.crucible_account_id == "" ? local.account : var.crucible_account_id
  node_role = "arn:aws:iam::${local.crucible}:role/${var.name}-node"
  regions   = length(var.allowed_regions) == 0 ? [var.region] : var.allowed_regions
  lab       = "$${aws:PrincipalTag/crucible:lab-id}" # an IAM policy variable, not a Terraform one
  state     = "arn:aws:s3:::${aws_s3_bucket.state.bucket}"
  creates   = ["RunInstances", "CreateVolume", "CreateSecurityGroup", "CreateNetworkInterface"]
  # Trust the node role by ARN through the account root, so the trust works before the role exists.
  trust = { Effect = "Allow", Principal = { AWS = "arn:aws:iam::${local.crucible}:root" } }
}

resource "aws_s3_account_public_access_block" "labs" {
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket" "state" {
  bucket = "${var.name}-${local.account}-labstate"
  lifecycle { prevent_destroy = true }
}

resource "aws_s3_bucket_versioning" "state" {
  bucket = aws_s3_bucket.state.id
  versioning_configuration { status = "Enabled" }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "state" {
  bucket = aws_s3_bucket.state.id
  rule {
    apply_server_side_encryption_by_default { sse_algorithm = "AES256" }
  }
}

resource "aws_s3_bucket_public_access_block" "state" {
  bucket                  = aws_s3_bucket.state.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_lifecycle_configuration" "state" {
  bucket = aws_s3_bucket.state.id
  rule {
    id     = "prune-old-state"
    status = "Enabled"
    filter { prefix = "labs/" }
    noncurrent_version_expiration { noncurrent_days = 7 }
  }
}

# The ceiling for every lab session: EC2 and S3 only, allowed regions only, small instances only.
resource "aws_iam_policy" "boundary" {
  name = "${var.name}-lab-boundary"
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      { Sid = "Ceiling", Effect = "Allow", Action = ["ec2:*", "s3:*", "sts:GetCallerIdentity", "tag:GetResources"], Resource = "*" },
      { Sid = "OnlyLabRegions", Effect = "Deny", NotAction = ["sts:GetCallerIdentity", "s3:ListAllMyBuckets", "s3:GetBucketLocation"],
      Resource = "*", Condition = { StringNotEquals = { "aws:RequestedRegion" = local.regions } } },
      { Sid = "SmallInstancesOnly", Effect = "Deny", Action = "ec2:RunInstances", Resource = "arn:aws:ec2:*:*:instance/*",
      Condition = { StringNotEquals = { "ec2:InstanceType" = var.allowed_instance_types } } },
    ]
  })
}

resource "aws_iam_role" "lab" {
  name                 = "${var.name}-lab"
  max_session_duration = 3600
  permissions_boundary = aws_iam_policy.boundary.arn
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [merge(local.trust, {
      Action    = ["sts:AssumeRole", "sts:TagSession"]
      Condition = { ArnEquals = { "aws:PrincipalArn" = local.node_role }, StringLike = { "aws:RequestTag/crucible:lab-id" = "?*" } }
    })]
  })
}

# ABAC (spec §8.2): creates must carry the session's lab id; modify/delete only what carries it.
resource "aws_iam_role_policy" "lab" {
  role = aws_iam_role.lab.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      { Sid = "Read", Effect = "Allow", Resource = "*",
      Action = ["ec2:Describe*", "s3:ListAllMyBuckets", "s3:GetBucketLocation", "sts:GetCallerIdentity", "tag:GetResources"] },
      { Sid      = "CreateTagged", Effect = "Allow", Action = [for a in local.creates : "ec2:${a}"],
        Resource = ["arn:aws:ec2:*:*:instance/*", "arn:aws:ec2:*:*:volume/*", "arn:aws:ec2:*:*:security-group/*", "arn:aws:ec2:*:*:network-interface/*"],
      Condition = { StringEquals = { "aws:RequestTag/crucible:lab-id" = local.lab } } },
      { Sid = "LaunchFromShared", Effect = "Allow", Action = "ec2:RunInstances",
      Resource = ["arn:aws:ec2:*::image/*", "arn:aws:ec2:*:*:subnet/*", "arn:aws:ec2:*:*:key-pair/*", "arn:aws:ec2:*:*:security-group/*", "arn:aws:ec2:*:*:network-interface/*"] },
      { Sid = "CreateInShared", Effect = "Allow", Action = ["ec2:CreateSecurityGroup", "ec2:CreateNetworkInterface"],
      Resource = ["arn:aws:ec2:*:*:vpc/*", "arn:aws:ec2:*:*:subnet/*"] },
      { Sid = "TagOnCreate", Effect = "Allow", Action = "ec2:CreateTags", Resource = "*",
      Condition = { StringEquals = { "ec2:CreateAction" = local.creates } } },
      { Sid = "ManageOwn", Effect = "Allow", Resource = "*",
        Action = ["ec2:TerminateInstances", "ec2:StopInstances", "ec2:StartInstances", "ec2:RebootInstances", "ec2:DeleteVolume",
          "ec2:AttachVolume", "ec2:DetachVolume", "ec2:DeleteSecurityGroup", "ec2:AuthorizeSecurityGroupIngress",
          "ec2:AuthorizeSecurityGroupEgress", "ec2:RevokeSecurityGroupIngress", "ec2:RevokeSecurityGroupEgress",
        "ec2:ModifySecurityGroupRules", "ec2:DeleteNetworkInterface", "ec2:CreateTags", "ec2:DeleteTags"],
      Condition = { StringEquals = { "aws:ResourceTag/crucible:lab-id" = local.lab } } },
      { Sid = "KeepCrucibleTags", Effect = "Deny", Action = ["ec2:CreateTags", "ec2:DeleteTags"], Resource = "*",
      Condition = { "ForAnyValue:StringLike" = { "aws:TagKeys" = ["crucible:*"] }, Null = { "ec2:CreateAction" = "true" } } },
      { Sid = "OwnBuckets", Effect = "Allow", Action = "s3:*",
      Resource = ["arn:aws:s3:::crucible-lab-${local.lab}*", "arn:aws:s3:::crucible-lab-${local.lab}*/*"] },
      { Sid = "OwnState", Effect = "Allow", Action = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"],
      Resource = ["${local.state}/labs/${local.lab}.tfstate", "${local.state}/labs/${local.lab}.tfstate.tflock"] },
      { Sid = "ListOwnState", Effect = "Allow", Action = "s3:ListBucket", Resource = local.state,
      Condition = { StringLike = { "s3:prefix" = "labs/${local.lab}.tfstate*" } } },
    ]
  })
}

resource "aws_iam_role" "ops" {
  name = "${var.name}-lab-ops"
  assume_role_policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [merge(local.trust, { Action = "sts:AssumeRole", Condition = { ArnEquals = { "aws:PrincipalArn" = local.node_role } } })]
  })
}

resource "aws_iam_role_policy" "ops" {
  role = aws_iam_role.ops.id
  policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Allow", Action = ["tag:GetResources", "ce:GetCostAndUsage", "cloudtrail:LookupEvents"], Resource = "*" }]
  })
}

output "lab_role_arn" { value = aws_iam_role.lab.arn }
output "ops_role_arn" { value = aws_iam_role.ops.arn }
output "state_bucket" { value = aws_s3_bucket.state.bucket }
output "state_region" { value = var.region }
output "regions" { value = join(",", local.regions) }
