output "instance_id" { value = aws_instance.node.id }
output "public_ip" { value = aws_eip.web.public_ip }
output "domain" { value = var.domain }
output "url" { value = "https://${var.domain}" }
output "region" { value = var.region }
output "data_bucket" { value = var.data_bucket }
