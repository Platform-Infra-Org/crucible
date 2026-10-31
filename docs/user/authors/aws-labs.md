---
title: AWS labs
roles: [author]
order: 26
---
# AWS labs

An AWS lab builds real resources in a shared AWS account with Terraform, and the trainee works on them from a
workspace terminal that already has short-lived credentials for that lab. It costs money, so every start needs an
approval by its estimate, and budgets fail closed. Start from the **AWS lab with an S3 bucket** block, then write the
Terraform in git: Terraform files can't be edited in the browser.

## The lab file

```yaml
runtime: terraform
cloud: aws                    # the only cloud today; runtime: aws still works and means the same
aws:
  region: eu-west-1           # required
  max_hourly_usd: 0.20        # required: the estimate must fit under it
  prices:                     # only for resource types Crucible can't price: USD per hour, above 0
    aws_kinesis_stream: 0.015
  usage:                      # expected monthly usage of usage-priced resources, by resource address
    aws_s3_bucket.data: { storage_gb: 5 }
    aws_nat_gateway.out: { gb_processed: 20 }
terminals:
  - { name: workspace, service: workspace }
```

The Terraform goes in `terraform/` inside the lab folder. Crucible adds the provider and the state backend itself:

- **No `provider`, `backend` or `cloud` blocks.** Crucible writes the `aws` provider with the lab's region and tags
  every resource with `crucible:lab-id`, `crucible:team` and `crucible:training`. Use `var.crucible_region` and
  `var.crucible_lab_id` (also `var.crucible_team`, `var.crucible_training`) when you need them.
- **Where to launch.** Some accounts have no default VPC. There Crucible gives every lab `var.crucible_vpc_id` and
  `var.crucible_subnet_ids` (set by the operator with `crucible aws labs-init --vpc-id … --subnet-ids …`). Write the
  network so the lab works in either kind of account:

  ```hcl
  resource "aws_security_group" "box" {
    vpc_id = var.crucible_vpc_id != "" ? var.crucible_vpc_id : null
  }
  resource "aws_instance" "box" {
    subnet_id                   = length(var.crucible_subnet_ids) > 0 ? var.crucible_subnet_ids[0] : null
  }
  ```

  `null` falls back to the default VPC where there is one. Never write a real VPC or subnet id in a training repo.
- **Local modules only** (`source = "./modules/net"`). Registry and git modules are refused; copy one into
  `terraform/` if you need it. Providers must be HashiCorp's, and `hashicorp/local` and `hashicorp/external` are
  refused.
- **No `import` blocks.** A lab creates its own resources; it never adopts existing ones.
- **File reads name a literal path inside the module.** `file`, `templatefile`, `fileset`, `filebase64`, `fileexists`
  and the `file*sha*`/`filemd5` functions take a literal relative path: `"user_data.sh"`, `"./user_data.sh"` or
  `"${path.module}/user_data.sh"` (`fileset` also takes `path.module` itself, with a literal pattern). No absolute
  paths, `~`, `..` or computed paths. A `templatefile` target must be a regular file in `terraform/` (128 KiB at most)
  and must not read files itself.
- **Nothing generated**: no `.terraform/`, `.tfvars` or `.tfstate` files.
- **No secrets in state or outputs.** The trainee's credentials can read the lab's own Terraform state.
- The account's permission boundary decides what a lab may create: EC2 and S3 in the allowed regions, small
  instance types, gp2 or gp3 volumes up to a size limit, and lab buckets named `crucible-lab-<lab id>…`. Every
  instance needs the lab's own security group.

## How a lab is priced

Crucible prices labs itself from AWS's public list prices (on-demand, Linux, shared tenancy), refreshed every week.
For each content version it runs a read-only `terraform plan`, prices every resource the plan creates, and adds them
up per hour. The estimate is that hourly price times the lab's time limit.

**Types Crucible prices:**

| Type | Priced by |
|---|---|
| `aws_instance` | instance type, plus its root volume (8 GiB gp3 unless set), each `ebs_block_device` by size and type, and a public IPv4 unless `associate_public_ip_address = false` (set it only in an account without a default VPC; in a default VPC with no NAT, an instance without a public IP has no internet) |
| `aws_ebs_volume` | size and type, per GB-month |
| `aws_nat_gateway` | per hour, its public IPv4 (unless private or given an `allocation_id`), plus stated `gb_processed` |
| `aws_eip` | the public IPv4, per hour |
| `aws_lb`, `aws_alb` | per hour by type, plus a public IPv4 per subnet unless internal (LCU usage isn't priced) |
| `aws_s3_bucket` | stated `storage_gb`, per GB-month of S3 Standard |

**Free types** (no charge of their own): `aws_vpc`, `aws_subnet`, `aws_security_group`, `aws_security_group_rule`,
`aws_vpc_security_group_ingress_rule`, `aws_vpc_security_group_egress_rule`, `aws_route_table`,
`aws_route_table_association`, `aws_route`, `aws_internet_gateway`, `aws_default_route_table`,
`aws_default_security_group`, `aws_key_pair`, `aws_lb_listener`, `aws_lb_target_group`,
`aws_lb_target_group_attachment`, `aws_s3_bucket_versioning`, `aws_s3_bucket_public_access_block`,
`aws_s3_bucket_server_side_encryption_configuration`, `aws_s3_bucket_policy`, `aws_s3_bucket_ownership_controls`,
`aws_s3_object`, `aws_network_interface`, `aws_eip_association`, `aws_volume_attachment`, and the helpers `random_id`, `random_string`,
`random_password`, `random_pet`, `random_integer`, `tls_private_key`, `terraform_data`, `null_resource` and
`time_sleep`.

**Anything else** needs a price under `aws.prices`, in US dollars per hour, or lint refuses the lab and names the type.
A declared price never lowers Crucible's own: for a type Crucible prices (or lists as free), each resource costs the
higher of the two. A few settings Crucible can't price fail the same way until you declare the type, and then the
declared price is used: io1 and io2 volumes, gp3 with more than 3000 IOPS or 125 MB/s, dedicated tenancy, and an
instance type, volume size or `ebs_block_device` that isn't known until apply. A declared price never stands in for a
price missing from AWS's list (an instance type or region Crucible has no price for): that lab can't be requested
until the price data has it.

**Usage.** Some resources cost by how much they are used. State it under `aws.usage`, by resource address, as a
monthly amount: `storage_gb` for an `aws_s3_bucket`, `gb_processed` for an `aws_nat_gateway`. Unstated usage counts
$0 and the breakdown says *usage not stated*. An AWS lab is never priced at $0: one whose resources are all free or
unstated (a lone bucket, say) can't be requested until you state its usage or declare a price.

**The ceiling.** A lab priced above its `max_hourly_usd` can't be requested; its page says "This lab is priced at
$X/h, above its $Y/h limit; its maintainers need to make it cheaper." A failed plan, a type with no price, or AWS
price data that is missing or more than 30 days old also stops requests, never prices the lab at $0. The lab page
says why: "AWS price data is out of date; an admin needs to refresh it", or "Crucible can't price: <types>; the lab
must declare a price for them". When the lab itself fails to estimate (its plan fails, or a resource Crucible
refuses to guess), the training's editors and admins see the error under "The estimate failed"; participants only
see that the lab can't be requested right now.

**The breakdown.** The lab page shows **How this estimate was worked out** under the request button: each resource
with its price per hour, its usage per hour, and a note (*free*, *declared* or *usage not stated*). Open it to check
your lab after you push.

`crucible lint` checks all of this offline except the plan itself: it refuses an unpriced type, a bad price or usage,
and a missing region or ceiling, without an AWS account.

While a lab runs, Crucible meters what it actually built from its Terraform state, and later replaces that with what
AWS billed. Leaders see both on the [Ledger](/docs/leaders/ledger-and-content-versions).
