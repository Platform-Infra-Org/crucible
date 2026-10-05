terraform {
  required_version = ">= 1.10"
  backend "s3" {} # `crucible aws up` passes bucket, key, region and use_lockfile=true
  required_providers {
    aws    = { source = "hashicorp/aws", version = "~> 6.0" }
    random = { source = "hashicorp/random", version = "~> 3.6" }
  }
}

provider "aws" {
  region = var.region
  default_tags { tags = { app = "crucible" } }
}
