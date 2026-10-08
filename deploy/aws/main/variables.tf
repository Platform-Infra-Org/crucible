variable "region" { type = string }
variable "data_bucket" { type = string }
variable "name" {
  type    = string
  default = "crucible"
}
variable "domain" {
  type        = string
  description = "Public hostname, e.g. crucible.example.com (point an A record at the public_ip output, or set route53_zone_id)"
}
variable "route53_zone_id" {
  type    = string
  default = ""
}
variable "acme_email" {
  type        = string
  description = "Let's Encrypt account email"
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
  description = "Sysbox CE release for cluster labs (needs containerd >= 2.0.5, i.e. k3s >= 1.32; 0.7.1 fixes sysfs mounts on Ubuntu 24.04 + containerd 2.x)"
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
variable "infracost_api_key" {
  type        = string
  default     = ""
  sensitive   = true
  description = "Free key from `infracost auth login`. Without it aws labs cannot be priced, so they cannot be requested."
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
