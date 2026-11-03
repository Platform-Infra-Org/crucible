#!/usr/bin/env bash
set -euo pipefail
id=$(aws ec2 describe-instances --filters "Name=tag:crucible:lab-id,Values=$CRUCIBLE_LAB_ID" "Name=instance-state-name,Values=running" --query 'Reservations[0].Instances[0].InstanceId' --output text)
cmd=$(aws ssm send-command --instance-ids "$id" --document-name AWS-RunShellScript --parameters 'commands=["test -s /boot/grub/grub.cfg && systemctl is-system-running --wait"]' --query Command.CommandId --output text)
aws ssm wait command-executed --command-id "$cmd" --instance-id "$id"
