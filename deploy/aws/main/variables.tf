variable "region" { type = string }
variable "data_bucket" { type = string }
variable "name" {
  type    = string
  default = "crucible"
}
variable "domain" {
  type        = string
  default     = ""
  description = "access = public: the public hostname, e.g. crucible.example.com (point an A record at the public_ip output, or set route53_zone_id)"
  validation {
    condition     = var.access != "public" || var.domain != ""
    error_message = "access = public needs domain"
  }
}
variable "route53_zone_id" {
  type    = string
  default = ""
}
variable "acme_email" {
  type        = string
  default     = ""
  description = "access = public: the Let's Encrypt account email"
  validation {
    condition     = var.access != "public" || var.acme_email != ""
    error_message = "access = public needs acme_email (Let's Encrypt)"
  }
}
variable "access" {
  type        = string
  default     = "public"
  description = "Set by `crucible aws up` from the persistent stack: public or ssm"
  validation {
    condition     = contains(["public", "ssm"], var.access)
    error_message = "access must be public or ssm"
  }
}
variable "public_url" {
  type        = string
  default     = ""
  description = "Set by `crucible aws up` from the persistent stack. Empty (a persistent stack made before access existed): https://<domain>"
  validation {
    condition     = var.public_url == "" || can(regex("^https?://[^/]+$", var.public_url))
    error_message = "public_url must be http(s)://host[:port] with no path"
  }
}
variable "subnet_id" {
  type        = string
  default     = ""
  description = "The subnet the node runs in. Required for access = ssm (an existing workload subnet). Empty in public mode: the default VPC's first default subnet"
  validation {
    condition     = (var.access != "ssm" || var.subnet_id != "") && (var.subnet_id == "" || can(regex("^subnet-[0-9a-f]+$", var.subnet_id)))
    error_message = "access = ssm needs subnet_id (subnet-<hex>), an existing subnet the node can reach the internet from"
  }
}
variable "instance_type" {
  type    = string
  default = "t3a.xlarge"
}
variable "disk_gb" {
  type    = number
  default = 60
}
variable "k3s_version" {
  type    = string
  default = "v1.34.1+k3s1"
}
variable "sysbox_version" {
  type        = string
  default     = "0.7.1"
  description = "Sysbox CE release for AWS lab workspace pods (needs containerd >= 2.0.5, i.e. k3s >= 1.32; 0.7.1 fixes sysfs mounts on Ubuntu 24.04 + containerd 2.x)"
}
variable "sysbox_sha256_amd64" {
  type        = string
  default     = "9d6d5484f980d0a17f86c492c1262015c2afb66280bdb97215b79fde6a0261c5"
  description = "sha256 of sysbox-ce_<sysbox_version>.linux_amd64.deb (GitHub release digest); update together with sysbox_version. amd64 only: the node is x86_64 (t3a)"
}
variable "schedule_enabled" {
  type    = bool
  default = true
}
variable "schedule_timezone" {
  type    = string
  default = "Europe/Bucharest"
}
variable "wake_cron" {
  type    = string
  default = "cron(30 7 ? * MON-FRI *)"
}
variable "sleep_cron" {
  type    = string
  default = "cron(30 19 ? * MON-FRI *)"
}
variable "backup_cron" {
  type        = string
  default     = "15 19 * * *"
  description = "Kubernetes cron (node timezone = schedule_timezone); keep it before sleep_cron"
}
variable "git_credentials" {
  type        = string
  default     = ""
  sensitive   = true
  description = "Lines for git's credential store, e.g. https://bot:TOKEN@git.example.com. The token needs push (write) access to the training repos: the bot merges reviewed content edits."
}
variable "git_bot_name" {
  type    = string
  default = "Crucible Bot"
}
variable "git_bot_email" {
  type        = string
  default     = "crucible-bot@example.com"
  description = "Author email of the config commits the UI pushes. Use one your git host accepts (e.g. the bot account's verified email)."
}
# Set by `crucible aws up` from the persistent stack's Cognito outputs.
variable "oidc_issuer" { type = string }
variable "oidc_client_id" { type = string }
variable "cognito_user_pool_id" { type = string }

# Set by `crucible aws up` from the deploy/aws/labs outputs; empty = aws labs off.
variable "lab_role_arn" {
  type    = string
  default = ""
  validation {
    condition     = var.lab_role_arn == "" || (var.lab_plan_role_arn != "" && var.lab_export_bucket != "" && var.lab_export_name != "")
    error_message = "aws labs need the plan role and the billing export (lab_plan_role_arn, lab_export_bucket, lab_export_name): re-run `crucible aws labs-init`, then `crucible aws up`."
  }
}
variable "lab_ops_role_arn" {
  type    = string
  default = ""
}
variable "lab_state_bucket" {
  type    = string
  default = ""
}
variable "lab_state_region" {
  type    = string
  default = ""
}
variable "lab_regions" {
  type        = string
  default     = ""
  description = "Comma-separated regions aws labs may use (the labs stack's allowed_regions)"
}
variable "lab_plan_role_arn" {
  type        = string
  default     = ""
  description = "The read-only role cost estimates plan with"
}
variable "lab_export_bucket" {
  type        = string
  default     = ""
  description = "The billing export (CUR 2.0) bucket billed spend is read from"
}
variable "lab_export_prefix" {
  type    = string
  default = "cur"
}
variable "lab_export_name" {
  type    = string
  default = ""
}
variable "lab_vpc_id" {
  type        = string
  default     = ""
  description = "Set by `crucible aws up` from the labs stack: the VPC lab modules launch into (empty: the default VPC)"
}
variable "lab_subnet_ids" {
  type        = string
  default     = ""
  description = "Set by `crucible aws up` from the labs stack: comma-separated subnets lab modules launch into"
}
variable "bootstrap_admin" {
  type        = string
  default     = ""
  description = "Email of the first admin, seeded once into an empty admins table; they configure everything else in the UI. Invite the same email in Cognito first."
  validation {
    condition     = var.bootstrap_admin == "" || can(regex("^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+$", var.bootstrap_admin))
    error_message = "bootstrap_admin must be empty or a plain email address (letters, digits and . _ % + - only)."
  }
}
variable "toolbox_image" {
  type        = string
  default     = ""
  description = "Cluster labs: the toolbox image trainees' laptops pull (deploy/toolbox), pinned by digest (<image>@sha256:<64 hex>). Empty = cluster labs off."
  validation {
    condition     = var.toolbox_image == "" || can(regex("^[A-Za-z0-9][A-Za-z0-9._/:-]*@sha256:[0-9a-f]{64}$", var.toolbox_image))
    error_message = "toolbox_image must be empty or an image pinned by digest, e.g. ghcr.io/you/crucible-toolbox@sha256:<64 hex>: laptops pull it, so never a tag."
  }
}

# Email (optional). Without smtp_addr Crucible sends no email; Slack/Teams webhooks still work.
variable "smtp_addr" {
  type        = string
  default     = ""
  description = "SMTP server as host:port, e.g. email-smtp.eu-west-1.amazonaws.com:587. Empty = no email (keys added to crucible-secrets by hand are kept)."
  validation {
    condition     = var.smtp_addr == "" || can(regex("^[A-Za-z0-9.-]+:[0-9]{1,5}$", var.smtp_addr))
    error_message = "smtp_addr must be empty or host:port, e.g. smtp.example.com:587."
  }
}
variable "smtp_from" {
  type        = string
  default     = ""
  description = "Sender: an address or \"Name <address>\" (default in the API: crucible@localhost)"
  validation {
    condition     = !can(regex("['\\x00-\\x1f]", var.smtp_from))
    error_message = "smtp_from must not contain a single quote or a control character."
  }
}
variable "smtp_username" {
  type    = string
  default = ""
  validation {
    condition     = !can(regex("['\\x00-\\x1f]", var.smtp_username))
    error_message = "smtp_username must not contain a single quote or a control character."
  }
}
variable "smtp_password" {
  type        = string
  default     = ""
  sensitive   = true
  description = "Stored as the SecureString /<name>/smtp_password; deploy.sh copies it into crucible-secrets"
}
