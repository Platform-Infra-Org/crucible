package content

import "encoding/json"

// File names Crucible adds next to an aws lab's terraform/ module (spec §8.2).
const (
	LabTFFile     = "crucible.tf"
	LabTFVarsFile = "crucible.auto.tfvars.json"
)

// LabTF is added to every aws lab module. It declares the S3 backend (configured per lab at init), the AWS provider
// with the lab's region and the crucible:* default tags that IAM, the tag sweep, the reaper and Cost Explorer rely
// on, and the variables a module may use (var.crucible_lab_id names lab buckets: crucible-lab-<id>…). Lab modules
// must not declare a provider "aws" or a backend themselves (lint).
const LabTF = `# Added by Crucible. Lab modules must not declare provider "aws" or a backend.
terraform {
  backend "s3" {}
}

variable "crucible_lab_id" { type = string }
variable "crucible_team" { type = string }
variable "crucible_training" { type = string }
variable "crucible_region" { type = string }

provider "aws" {
  region = var.crucible_region
  default_tags {
    tags = {
      "crucible:lab-id"   = var.crucible_lab_id
      "crucible:team"     = var.crucible_team
      "crucible:training" = var.crucible_training
    }
  }
}
`

// LabTFVars fills LabTF's variables. JSON, so no value can break out of its string.
func LabTFVars(labID, team, training, region string) []byte {
	b, _ := json.Marshal(map[string]string{"crucible_lab_id": labID, "crucible_team": team,
		"crucible_training": training, "crucible_region": region})
	return b
}
