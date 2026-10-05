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
variable "platform_repo" {
  type      = string
  sensitive = true
}
variable "platform_branch" {
  type    = string
  default = "main"
}
variable "git_credentials" {
  type        = string
  default     = ""
  sensitive   = true
  description = "Lines for git's credential store, e.g. https://bot:TOKEN@git.example.com. The token needs push (write) access to the platform repo: the UI commits config changes."
}
# Set by `crucible aws up` from the persistent stack's Cognito outputs.
variable "oidc_issuer" { type = string }
variable "oidc_client_id" { type = string }
variable "cognito_user_pool_id" { type = string }
