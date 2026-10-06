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
  validation {
    # OnlyLabRegions denies every call outside these, the state bucket's included: every lab init would fail.
    condition     = length(var.allowed_regions) == 0 || contains(var.allowed_regions, var.region)
    error_message = "allowed_regions must include region (the lab state bucket's region)."
  }
}
variable "node_role_name" {
  type        = string
  default     = ""
  description = "Crucible's node role (deploy/aws/main output node_role_name; labs-init passes it). Empty = <name>-node."
}
variable "allowed_instance_types" {
  type    = list(string)
  default = ["t3.nano", "t3.micro", "t3a.nano", "t3a.micro", "t4g.nano", "t4g.micro"]
}
variable "max_volume_gib" {
  type        = number
  default     = 50
  description = "Largest EBS volume a lab may create (gp2/gp3 only, baseline IOPS and throughput)"
}

data "aws_caller_identity" "me" {}

locals {
  account   = data.aws_caller_identity.me.account_id
  crucible  = var.crucible_account_id == "" ? local.account : var.crucible_account_id
  node_role = "arn:aws:iam::${local.crucible}:role/${var.node_role_name == "" ? "${var.name}-node" : var.node_role_name}"
  regions   = length(var.allowed_regions) == 0 ? [var.region] : var.allowed_regions
  lab       = "$${aws:PrincipalTag/crucible:lab-id}" # an IAM policy variable, not a Terraform one
  state     = "arn:aws:s3:::${aws_s3_bucket.state.bucket}"
  # ponytail: no standalone CreateNetworkInterface: a lab could then launch on a detached ENI of its own, or try one
  # another lab left behind (detached ENIs do exist: a terminated instance's extra ENI, or one made by an admin).
  # Instances still get their own ENIs. Add it back with a guard.
  creates = ["RunInstances", "CreateVolume", "CreateSecurityGroup"]
  # aws_vpc_security_group_ingress_rule/_egress_rule send the provider's default_tags with the rule.
  tag_on_create = concat(local.creates, ["AuthorizeSecurityGroupIngress", "AuthorizeSecurityGroupEgress"])
  sg_rules = ["ec2:AuthorizeSecurityGroupIngress", "ec2:AuthorizeSecurityGroupEgress", "ec2:RevokeSecurityGroupIngress",
  "ec2:RevokeSecurityGroupEgress", "ec2:ModifySecurityGroupRules"]
  volume = "arn:aws:ec2:*:*:volume/*"
  # S3 calls that lock a bucket against the sweep (which runs as the lab role) or hand it to someone else.
  s3_locks = ["s3:PutBucketObjectLockConfiguration", "s3:PutObjectRetention", "s3:PutObjectLegalHold",
    "s3:BypassGovernanceRetention", "s3:PutBucketPolicy", "s3:DeleteBucketPolicy", "s3:PutBucketAcl", "s3:PutObjectAcl",
  "s3:PutObjectVersionAcl", "s3:PutBucketOwnershipControls", "s3:DeleteBucketTagging"]
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
    abort_incomplete_multipart_upload { days_after_initiation = 7 }
  }
}

resource "aws_s3_bucket_policy" "state" {
  bucket = aws_s3_bucket.state.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{ Sid = "TLSOnly", Effect = "Deny", Principal = "*", Action = "s3:*", Resource = [local.state, "${local.state}/*"],
    Condition = { Bool = { "aws:SecureTransport" = "false" } } }]
  })
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
      { Sid = "SharedTenancyOnly", Effect = "Deny", Action = "ec2:RunInstances", Resource = "arn:aws:ec2:*:*:instance/*",
      Condition = { StringNotEquals = { "ec2:Tenancy" = "default" } } },
      { Sid = "NoMarketplaceImages", Effect = "Deny", Action = "ec2:RunInstances", Resource = "arn:aws:ec2:*::image/*",
      Condition = { StringEquals = { "ec2:Owner" = "aws-marketplace" } } },
      # Conditions in one statement are ANDed, so each volume limit is its own Deny.
      { Sid = "SmallVolumesOnly", Effect = "Deny", Action = ["ec2:CreateVolume", "ec2:RunInstances"], Resource = local.volume,
      Condition = { NumericGreaterThan = { "ec2:VolumeSize" = var.max_volume_gib } } },
      { Sid = "GeneralPurposeVolumesOnly", Effect = "Deny", Action = ["ec2:CreateVolume", "ec2:RunInstances"], Resource = local.volume,
      Condition = { StringNotEquals = { "ec2:VolumeType" = ["gp2", "gp3"] } } },
      { Sid = "BaselineIopsOnly", Effect = "Deny", Action = ["ec2:CreateVolume", "ec2:RunInstances"], Resource = local.volume,
      Condition = { NumericGreaterThan = { "ec2:VolumeIops" = 3000 } } },
      { Sid = "BaselineThroughputOnly", Effect = "Deny", Action = ["ec2:CreateVolume", "ec2:RunInstances"], Resource = local.volume,
      Condition = { NumericGreaterThan = { "ec2:VolumeThroughput" = 125 } } },
      { Sid = "NoBucketLocks", Effect = "Deny", Action = local.s3_locks, Resource = "*" },
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
        # TagKeys pins the exact-case key: IAM matches the key in aws:RequestTag/<key> case-insensitively, EC2 does not.
      Condition = { StringEquals = { "aws:RequestTag/crucible:lab-id" = local.lab }, "ForAnyValue:StringEquals" = { "aws:TagKeys" = ["crucible:lab-id"] } } },
      { Sid = "LaunchFromShared", Effect = "Allow", Action = "ec2:RunInstances",
      Resource = ["arn:aws:ec2:*::image/*", "arn:aws:ec2:*:*:subnet/*", "arn:aws:ec2:*:*:key-pair/*", "arn:aws:ec2:*:*:security-group/*", "arn:aws:ec2:*:*:network-interface/*"] },
      { Sid = "CreateInShared", Effect = "Allow", Action = "ec2:CreateSecurityGroup", Resource = "arn:aws:ec2:*:*:vpc/*" },
      # Another lab's security groups and ENIs are off limits.
      { Sid      = "NotOtherLabsNetwork", Effect = "Deny", Action = ["ec2:RunInstances", "ec2:CreateNetworkInterface", "ec2:AttachNetworkInterface"],
        Resource = ["arn:aws:ec2:*:*:security-group/*", "arn:aws:ec2:*:*:network-interface/*"],
      Condition = { Null = { "aws:ResourceTag/crucible:lab-id" = "false" }, StringNotEquals = { "aws:ResourceTag/crucible:lab-id" = local.lab } } },
      # Ruling O1: so are untagged ones, above all the VPC default SG every lab would otherwise share (any port open
      # between labs). A lab brings its own security group. ModifyInstanceAttribute only for groupSet, defence in depth.
      { Sid = "OwnSecurityGroupsOnly", Effect = "Deny", Action = ["ec2:RunInstances", "ec2:CreateNetworkInterface", "ec2:ModifyInstanceAttribute"],
      Resource = "arn:aws:ec2:*:*:security-group/*", Condition = { Null = { "aws:ResourceTag/crucible:lab-id" = "true" } } },
      { Sid = "TagOnCreate", Effect = "Allow", Action = "ec2:CreateTags", Resource = "*",
      Condition = { StringEquals = { "ec2:CreateAction" = local.tag_on_create } } },
      # A rule call names the group and the rule. The group must be the lab's own (ManageOwn); the rule ARN carries no
      # power of its own, and a tagged rule has no aws:ResourceTag yet at create.
      { Sid = "RulesOfOwnGroups", Effect = "Allow", Action = local.sg_rules, Resource = "arn:aws:ec2:*:*:security-group-rule/*" },
      { Sid = "ManageOwn", Effect = "Allow", Resource = "*",
        Action = concat(local.sg_rules, ["ec2:TerminateInstances", "ec2:StopInstances", "ec2:StartInstances", "ec2:RebootInstances", "ec2:DeleteVolume",
          "ec2:AttachVolume", "ec2:DetachVolume", "ec2:DeleteSecurityGroup", "ec2:DeleteNetworkInterface", "ec2:CreateTags",
        "ec2:DeleteTags"]),
      Condition = { StringEquals = { "aws:ResourceTag/crucible:lab-id" = local.lab } } },
      { Sid = "KeepCrucibleTags", Effect = "Deny", Action = ["ec2:CreateTags", "ec2:DeleteTags"], Resource = "*",
      Condition = { "ForAnyValue:StringLike" = { "aws:TagKeys" = ["crucible:*"] }, Null = { "ec2:CreateAction" = "true" } } },
      # delete-tags without --tags removes every tag and carries no aws:TagKeys, so KeepCrucibleTags never sees it.
      { Sid = "NoBlankDeleteTags", Effect = "Deny", Action = "ec2:DeleteTags", Resource = "*", Condition = { Null = { "aws:TagKeys" = "true" } } },
      # EC2 has no launch-time key to forbid --disable-api-termination, so the sweep (this role) turns it off before it
      # terminates. Only that attribute, only to false: instanceType etc. stay unmodifiable. Stop protection does not
      # block termination.
      { Sid = "UnprotectOwnTermination", Effect = "Allow", Action = "ec2:ModifyInstanceAttribute", Resource = "arn:aws:ec2:*:*:instance/*",
      Condition = { StringEquals = { "aws:ResourceTag/crucible:lab-id" = local.lab }, StringEqualsIgnoreCase = { "ec2:Attribute/disableApiTermination" = "false" } } },
      # An allowlist, not s3:*: nothing here can lock a bucket against the sweep (and NoBucketLocks denies those anyway).
      # PutBucketTagging has no tag condition keys, so the sweep finds lab buckets by name, not only by tag.
      # s3:TagResource: newer providers tag the bucket at CreateBucket (no power PutBucketTagging lacks). No
      # s3:UntagResource: it could remove crucible:* tags. ponytail: no s3:ResourceAccount pin; names are global, so a
      # bucket of that name in another account is reachable only if that account's bucket policy grants it.
      { Sid = "OwnBuckets", Effect = "Allow",
        Action = ["s3:CreateBucket", "s3:DeleteBucket", "s3:Get*", "s3:List*", "s3:PutObject", "s3:DeleteObject",
          "s3:DeleteObjectVersion", "s3:AbortMultipartUpload", "s3:PutObjectTagging", "s3:DeleteObjectTagging",
          "s3:PutBucketTagging", "s3:TagResource", "s3:PutBucketVersioning", "s3:PutEncryptionConfiguration", "s3:PutLifecycleConfiguration",
        "s3:PutBucketPublicAccessBlock"],
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
    Statement = [{ Effect = "Allow", Action = ["tag:GetResources", "s3:ListAllMyBuckets", "ce:GetCostAndUsage", "cloudtrail:LookupEvents"], Resource = "*" }]
  })
}

output "lab_role_arn" { value = aws_iam_role.lab.arn }
output "ops_role_arn" { value = aws_iam_role.ops.arn }
output "state_bucket" { value = aws_s3_bucket.state.bucket }
output "state_region" { value = var.region }
output "regions" { value = join(",", local.regions) }
