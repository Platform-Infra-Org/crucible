# Created once per account with `crucible aws init`. Never destroyed by teardown.
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

variable "region" { type = string }
variable "name" {
  type    = string
  default = "crucible"
}
variable "domain" {
  type        = string
  description = "Crucible's public hostname; used for the Cognito callback URL"
}

data "aws_caller_identity" "me" {}

locals {
  prefix = "${var.name}-${data.aws_caller_identity.me.account_id}"
}

resource "aws_s3_bucket" "state" {
  bucket = "${local.prefix}-tfstate"
  lifecycle { prevent_destroy = true }
}

resource "aws_s3_bucket_versioning" "state" {
  bucket = aws_s3_bucket.state.id
  versioning_configuration { status = "Enabled" }
}

resource "aws_s3_bucket_public_access_block" "state" {
  bucket                  = aws_s3_bucket.state.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# snapshots/  nightly pg_dump files (30 days)
# latest/     copy of the newest dump; never expires, so a long teardown still restores
# releases/   image tarballs + Helm chart per git sha
# current-release  sha the node deploys on boot
resource "aws_s3_bucket" "data" {
  bucket = "${local.prefix}-data"
  lifecycle { prevent_destroy = true }
}

resource "aws_s3_bucket_versioning" "data" {
  bucket = aws_s3_bucket.data.id
  versioning_configuration { status = "Enabled" }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "data" {
  bucket = aws_s3_bucket.data.id
  rule {
    apply_server_side_encryption_by_default { sse_algorithm = "AES256" }
  }
}

resource "aws_s3_bucket_public_access_block" "data" {
  bucket                  = aws_s3_bucket.data.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_lifecycle_configuration" "data" {
  bucket = aws_s3_bucket.data.id
  rule {
    id     = "expire-snapshots"
    status = "Enabled"
    filter { prefix = "snapshots/" }
    expiration { days = 30 }
    noncurrent_version_expiration { noncurrent_days = 7 }
  }
  rule {
    id     = "expire-releases"
    status = "Enabled"
    filter { prefix = "releases/" }
    expiration { days = 60 } # the running node keeps its image locally; `crucible aws up` republishes
    noncurrent_version_expiration { noncurrent_days = 7 }
  }
  rule {
    id     = "prune-old-latest" # the current latest/ dump is kept forever; only overwritten copies go
    status = "Enabled"
    filter { prefix = "latest/" }
    noncurrent_version_expiration { noncurrent_days = 7 }
  }
}

output "region" { value = var.region }
output "state_bucket" { value = aws_s3_bucket.state.bucket }
output "data_bucket" { value = aws_s3_bucket.data.bucket }
