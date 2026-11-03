# One Ubuntu 24.04 machine, reachable over SSM. Crucible adds the provider and the state backend.
data "aws_ami" "ubuntu" {
  most_recent = true
  owners      = ["099720109477"]
  filter {
    name   = "name"
    values = ["ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*"]
  }
}

resource "aws_security_group" "box" {
  name_prefix = "crucible-lab-"
  vpc_id      = var.crucible_vpc_id != "" ? var.crucible_vpc_id : null
}

resource "aws_vpc_security_group_egress_rule" "all" {
  security_group_id = aws_security_group.box.id
  ip_protocol       = "-1"
  cidr_ipv4         = "0.0.0.0/0"
}

resource "aws_vpc_security_group_ingress_rule" "ssh" {
  count             = var.crucible_ssh_source_cidr != "" ? 1 : 0
  security_group_id = aws_security_group.box.id
  ip_protocol       = "tcp"
  from_port         = 22
  to_port           = 22
  cidr_ipv4         = var.crucible_ssh_source_cidr
}

resource "aws_instance" "box" {
  ami                         = data.aws_ami.ubuntu.id
  instance_type               = "t3.micro"
  subnet_id                   = length(var.crucible_subnet_ids) > 0 ? var.crucible_subnet_ids[0] : null
  vpc_security_group_ids      = [aws_security_group.box.id]
  iam_instance_profile        = var.crucible_instance_profile != "" ? var.crucible_instance_profile : null
  associate_public_ip_address = false

  root_block_device {
    volume_size = 8
    volume_type = "gp3"
  }

  metadata_options {
    http_tokens = "required"
  }

  user_data = <<-USERDATA
    #!/bin/bash
    set -eu
    # The exercise: sshd on the wrong port, no passwords, and no EC2 Instance Connect keys.
    cat >/etc/ssh/sshd_config.d/99-crucible.conf <<'EOF'
    Port 2222
    PasswordAuthentication no
    AuthorizedKeysCommand /bin/false
    AuthorizedKeysCommandUser root
    EOF
    # Ubuntu 24.04 starts sshd from a socket on port 22; hand the port to sshd itself.
    systemctl disable --now ssh.socket || true
    systemctl enable ssh.service
    systemctl restart ssh
  USERDATA
}

output "instance_id" {
  value = aws_instance.box.id
}
