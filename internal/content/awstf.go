package content

import "encoding/json"

// File names Crucible adds next to an aws lab's terraform/ module (spec §8.2).
const (
	LabTFFile     = "crucible.tf"
	LabTFVarsFile = "crucible.auto.tfvars.json"
)

// LabTF is added to every aws lab module. It declares the S3 backend (configured per lab at init), the AWS provider
// with the lab's region and the crucible:* default tags that IAM, the tag sweep, the reaper and the billing export rely
// on, and the variables a module may use (var.crucible_lab_id names lab buckets: crucible-lab-<id>…). Lab modules
// must not declare a provider "aws" or a backend themselves (lint). The provider version is pinned here, in the
// provider block, so apply and destroy run the same build: a required_providers block in this file would clash with a
// module's own (terraform allows one per module); terraform intersects this constraint with the module's. The pin
// matches deploy/aws's lock files; bump them together. Terraform warns that the attribute is deprecated.
const LabTF = `# Added by Crucible. Lab modules must not declare provider "aws" or a backend.
terraform {
  backend "s3" {}
}

variable "crucible_lab_id" { type = string }
variable "crucible_team" { type = string }
variable "crucible_training" { type = string }
variable "crucible_region" { type = string }
variable "crucible_vpc_id" { type = string }           # "" when Crucible has no lab network (the default VPC)
variable "crucible_subnet_ids" { type = list(string) } # [] when it has none

provider "aws" {
  version = "6.67.0"
  region  = var.crucible_region
  default_tags {
    tags = {
      "crucible:lab-id"   = var.crucible_lab_id
      "crucible:team"     = var.crucible_team
      "crucible:training" = var.crucible_training
    }
  }
}
`

// LabNetwork is where lab modules launch when the account has no default VPC (`crucible aws labs-init --vpc-id
// --subnet-ids`). Region is the region those subnets are in: a lab elsewhere gets no network.
type LabNetwork struct {
	Region    string
	VPCID     string
	SubnetIDs []string
}

// LabTFVars fills LabTF's variables. JSON, so no value can break out of its string. The network is "" and [] (never
// null) when unset or in another region, so a module's `length(var.crucible_subnet_ids) > 0 ? … : null` falls back to
// the default VPC.
func LabTFVars(labID, team, training, region string, net LabNetwork) []byte {
	vpc, subnets := "", []string{}
	if net.VPCID != "" && net.Region == region && len(net.SubnetIDs) > 0 {
		vpc, subnets = net.VPCID, net.SubnetIDs
	}
	b, _ := json.Marshal(map[string]any{"crucible_lab_id": labID, "crucible_team": team,
		"crucible_training": training, "crucible_region": region, "crucible_vpc_id": vpc, "crucible_subnet_ids": subnets})
	return b
}
