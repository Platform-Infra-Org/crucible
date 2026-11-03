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
    cat >/etc/default/grub.d/99-crucible-serial.cfg <<'EOF'
    GRUB_TERMINAL="serial console"
    GRUB_SERIAL_COMMAND="serial --unit=0 --speed=115200"
    GRUB_TIMEOUT_STYLE=menu
    GRUB_TIMEOUT=10
    EOF
    update-grub
    # The exercise: no grub.cfg. GRUB stops at `grub rescue>` / `grub>` on the next boot.
    mv /boot/grub/grub.cfg /boot/grub/grub.cfg.lost
    reboot
  USERDATA
}

output "instance_id" {
  value = aws_instance.box.id
}
