mock_provider "aws" {
  mock_data "aws_caller_identity" {
    defaults = { account_id = "444455556666" }
  }
  mock_resource "aws_iam_policy" {
    defaults = { arn = "arn:aws:iam::444455556666:policy/crucible-lab-boundary" }
  }
  mock_resource "aws_iam_role" {
    defaults = { arn = "arn:aws:iam::444455556666:role/crucible-lab" }
  }
}

variables {
  region = "eu-west-1"
}

run "lab_role_is_bounded_tagged_and_short_lived" {
  command = apply # mock provider only: computed ARNs get fake values, nothing reaches AWS

  assert {
    condition     = aws_iam_role.lab.max_session_duration == 3600 && aws_iam_role.lab.permissions_boundary == aws_iam_policy.boundary.arn
    error_message = "one-hour sessions inside the permission boundary"
  }
  assert {
    condition     = jsondecode(aws_iam_role.lab.assume_role_policy).Statement[0].Condition.ArnEquals["aws:PrincipalArn"] == "arn:aws:iam::444455556666:role/crucible-node" && jsondecode(aws_iam_role.lab.assume_role_policy).Statement[0].Condition.StringLike["aws:RequestTag/crucible:lab-id"] == "?*"
    error_message = "only Crucible's node role may assume it, and only with a lab-id session tag"
  }
  assert {
    condition     = alltrue([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : s.Condition.StringEquals["aws:RequestTag/crucible:lab-id"] == "$${aws:PrincipalTag/crucible:lab-id}" if s.Sid == "CreateTagged"])
    error_message = "creates must carry the session's own lab id"
  }
  assert {
    condition     = one([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : s if s.Sid == "ManageOwn"]).Condition.StringEquals["aws:ResourceTag/crucible:lab-id"] == "$${aws:PrincipalTag/crucible:lab-id}"
    error_message = "modify/delete only resources tagged with the session's lab id"
  }
  assert {
    condition     = one([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : s if s.Sid == "KeepCrucibleTags"]).Effect == "Deny"
    error_message = "crucible:* tags cannot be changed after create"
  }
  assert {
    condition     = contains(one([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : s if s.Sid == "OwnState"]).Resource, "arn:aws:s3:::crucible-444455556666-labstate/labs/$${aws:PrincipalTag/crucible:lab-id}.tfstate")
    error_message = "a lab reads and writes only its own terraform state"
  }
  assert {
    condition     = one([for s in jsondecode(aws_iam_policy.boundary.policy).Statement : s if s.Sid == "OnlyLabRegions"]).Condition.StringNotEquals["aws:RequestedRegion"] == ["eu-west-1"]
    error_message = "labs stay in the allowed regions"
  }
  assert {
    condition     = contains(one([for s in jsondecode(aws_iam_policy.boundary.policy).Statement : s if s.Sid == "SmallInstancesOnly"]).Condition.StringNotEquals["ec2:InstanceType"], "t3.micro")
    error_message = "only small instance types"
  }
  assert {
    condition     = !anytrue([for s in concat(jsondecode(aws_iam_role_policy.lab.policy).Statement, jsondecode(aws_iam_policy.boundary.policy).Statement) : anytrue([for a in flatten([lookup(s, "Action", [])]) : startswith(a, "iam:")])])
    error_message = "no IAM for lab roles"
  }
  assert {
    condition     = toset(jsondecode(aws_iam_role_policy.ops.policy).Statement[0].Action) == toset(["tag:GetResources", "s3:ListAllMyBuckets", "ce:GetCostAndUsage", "cloudtrail:LookupEvents"])
    error_message = "the ops role only reads the inventory, bucket names, costs and CloudTrail"
  }
  assert {
    condition     = aws_s3_account_public_access_block.labs.block_public_policy && aws_s3_account_public_access_block.labs.restrict_public_buckets
    error_message = "lab buckets can never be made public"
  }
  assert {
    condition     = aws_s3_bucket.state.bucket == "crucible-444455556666-labstate" && aws_s3_bucket_versioning.state.versioning_configuration[0].status == "Enabled"
    error_message = "versioned lab state bucket"
  }
}

# Fix round 1 (task-13-review C1, I1-I5): the sweep must be able to find and delete everything a lab creates.
run "a_lab_cannot_hide_lock_or_inflate_its_resources" {
  command = apply

  assert { # C1
    condition     = one([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : s if s.Sid == "NoBlankDeleteTags"]).Effect == "Deny" && one([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : s if s.Sid == "NoBlankDeleteTags"]).Action == "ec2:DeleteTags" && one([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : s if s.Sid == "NoBlankDeleteTags"]).Condition.Null["aws:TagKeys"] == "true"
    error_message = "delete-tags without --tags (it removes every tag, with no aws:TagKeys) is denied"
  }
  assert { # C1: the existing post-create guard stays
    condition     = one([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : s if s.Sid == "KeepCrucibleTags"]).Condition["ForAnyValue:StringLike"]["aws:TagKeys"] == ["crucible:*"] && toset(one([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : s if s.Sid == "KeepCrucibleTags"]).Action) == toset(["ec2:CreateTags", "ec2:DeleteTags"])
    error_message = "crucible:* tags cannot be added or removed outside a create"
  }
  assert { # I1
    condition     = one([for s in jsondecode(aws_iam_policy.boundary.policy).Statement : s if s.Sid == "SmallVolumesOnly"]).Condition.NumericGreaterThan["ec2:VolumeSize"] == 50 && one([for s in jsondecode(aws_iam_policy.boundary.policy).Statement : s if s.Sid == "SmallVolumesOnly"]).Resource == "arn:aws:ec2:*:*:volume/*" && toset(one([for s in jsondecode(aws_iam_policy.boundary.policy).Statement : s if s.Sid == "SmallVolumesOnly"]).Action) == toset(["ec2:CreateVolume", "ec2:RunInstances"])
    error_message = "volumes over 50 GiB are denied, on CreateVolume and on RunInstances"
  }
  assert { # I1
    condition     = one([for s in jsondecode(aws_iam_policy.boundary.policy).Statement : s if s.Sid == "GeneralPurposeVolumesOnly"]).Condition.StringNotEquals["ec2:VolumeType"] == ["gp2", "gp3"] && one([for s in jsondecode(aws_iam_policy.boundary.policy).Statement : s if s.Sid == "BaselineIopsOnly"]).Condition.NumericGreaterThan["ec2:VolumeIops"] == 3000 && one([for s in jsondecode(aws_iam_policy.boundary.policy).Statement : s if s.Sid == "BaselineThroughputOnly"]).Condition.NumericGreaterThan["ec2:VolumeThroughput"] == 125
    error_message = "gp2/gp3 only, no provisioned IOPS or throughput"
  }
  assert { # I1
    condition     = one([for s in jsondecode(aws_iam_policy.boundary.policy).Statement : s if s.Sid == "SharedTenancyOnly"]).Condition.StringNotEquals["ec2:Tenancy"] == "default" && one([for s in jsondecode(aws_iam_policy.boundary.policy).Statement : s if s.Sid == "NoMarketplaceImages"]).Condition.StringEquals["ec2:Owner"] == "aws-marketplace"
    error_message = "no dedicated tenancy, no Marketplace AMIs"
  }
  assert { # I2: no wildcard S3 grant, and every lock is denied in the ceiling
    condition     = !anytrue([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : contains(flatten([lookup(s, "Action", [])]), "s3:*")]) && toset(one([for s in jsondecode(aws_iam_policy.boundary.policy).Statement : s if s.Sid == "NoBucketLocks"]).Action) == toset(["s3:PutBucketObjectLockConfiguration", "s3:PutObjectRetention", "s3:PutObjectLegalHold", "s3:BypassGovernanceRetention", "s3:PutBucketPolicy", "s3:DeleteBucketPolicy", "s3:PutBucketAcl", "s3:PutObjectAcl", "s3:PutObjectVersionAcl", "s3:PutBucketOwnershipControls", "s3:DeleteBucketTagging"]) && one([for s in jsondecode(aws_iam_policy.boundary.policy).Statement : s if s.Sid == "NoBucketLocks"]).Effect == "Deny"
    error_message = "lab buckets get an allowlist; object lock, retention, bucket policies, ACLs and tag deletion are denied"
  }
  assert { # I2/I3: the allowlist grants none of the denied calls
    condition     = length(setintersection(toset(one([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : s if s.Sid == "OwnBuckets"]).Action), toset(one([for s in jsondecode(aws_iam_policy.boundary.policy).Statement : s if s.Sid == "NoBucketLocks"]).Action))) == 0
    error_message = "OwnBuckets never lists a denied S3 call"
  }
  assert { # I2: the sweep can clear termination protection, and nothing else
    condition     = one([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : s if s.Sid == "UnprotectOwnTermination"]).Condition.StringEqualsIgnoreCase["ec2:Attribute/disableApiTermination"] == "false" && length([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : s if contains(flatten([s.Action]), "ec2:ModifyInstanceAttribute")]) == 1 && alltrue([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : s.Condition.StringEquals["aws:ResourceTag/crucible:lab-id"] == "$${aws:PrincipalTag/crucible:lab-id}" if contains(flatten([s.Action]), "ec2:ModifyInstanceAttribute")])
    error_message = "ModifyInstanceAttribute only turns termination protection off, only on the lab's own instances"
  }
  assert { # I4
    condition     = one([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : s if s.Sid == "NotOtherLabsNetwork"]).Condition.Null["aws:ResourceTag/crucible:lab-id"] == "false" && one([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : s if s.Sid == "NotOtherLabsNetwork"]).Condition.StringNotEquals["aws:ResourceTag/crucible:lab-id"] == "$${aws:PrincipalTag/crucible:lab-id}" && contains(one([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : s if s.Sid == "NotOtherLabsNetwork"]).Resource, "arn:aws:ec2:*:*:security-group/*")
    error_message = "another lab's security groups and ENIs cannot be used; untagged shared ones can"
  }
  assert { # I4: no standalone ENIs (RunInstances cannot check an existing ENI's tag)
    condition     = !anytrue([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : contains(flatten([s.Action]), "ec2:CreateNetworkInterface") if s.Effect == "Allow"])
    error_message = "labs do not create standalone network interfaces"
  }
  assert { # I5
    condition     = one([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : s if s.Sid == "CreateTagged"]).Condition["ForAnyValue:StringEquals"]["aws:TagKeys"] == ["crucible:lab-id"]
    error_message = "creates must carry the exact-case crucible:lab-id key"
  }
  assert { # I3: the ops role can list buckets by name
    condition     = contains(jsondecode(aws_iam_role_policy.ops.policy).Statement[0].Action, "s3:ListAllMyBuckets")
    error_message = "the ops role lists lab buckets by name prefix"
  }
  assert { # minor: TLS-only state bucket
    condition     = jsondecode(aws_s3_bucket_policy.state.policy).Statement[0].Effect == "Deny" && jsondecode(aws_s3_bucket_policy.state.policy).Statement[0].Condition.Bool["aws:SecureTransport"] == "false"
    error_message = "the state bucket refuses plain HTTP"
  }
}

run "the_volume_cap_is_configurable" {
  command = plan
  variables {
    max_volume_gib = 20
  }
  assert {
    condition     = one([for s in jsondecode(aws_iam_policy.boundary.policy).Statement : s if s.Sid == "SmallVolumesOnly"]).Condition.NumericGreaterThan["ec2:VolumeSize"] == 20
    error_message = "max_volume_gib feeds the volume size cap"
  }
}

run "a_separate_lab_account_trusts_the_crucible_account" {
  command = plan
  variables {
    crucible_account_id = "111122223333"
    allowed_regions     = ["eu-west-1", "eu-central-1"]
  }
  assert {
    condition     = jsondecode(aws_iam_role.lab.assume_role_policy).Statement[0].Principal.AWS == "arn:aws:iam::111122223333:root" && jsondecode(aws_iam_role.lab.assume_role_policy).Statement[0].Condition.ArnEquals["aws:PrincipalArn"] == "arn:aws:iam::111122223333:role/crucible-node"
    error_message = "cross-account trust names the Crucible node role"
  }
  assert {
    condition     = output.regions == "eu-west-1,eu-central-1"
    error_message = "regions output feeds CRUCIBLE_AWS_LAB_REGIONS"
  }
}
