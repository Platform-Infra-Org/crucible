# The lab's own bucket. Crucible adds the provider (region, crucible:* default tags) and the state backend.
resource "aws_s3_bucket" "forge" {
  bucket        = "crucible-lab-${var.crucible_lab_id}"
  force_destroy = true
}
