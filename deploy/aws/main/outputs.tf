output "instance_id" { value = aws_instance.node.id }
output "public_ip" { value = aws_eip.web.public_ip }
output "domain" { value = var.domain }
output "url" { value = "https://${var.domain}" }
output "region" { value = var.region }
output "data_bucket" { value = var.data_bucket }
# `crucible aws up` reads it to refuse turning aws labs off by accident (labs outputs unreadable).
output "lab_role_arn" { value = var.lab_role_arn }
# `crucible aws labs-init` reads it: the lab account's roles trust exactly this role.
output "node_role_name" { value = aws_iam_role.node.name }
