#!/usr/bin/env bash
set -euo pipefail
id=$(aws ec2 describe-instances --filters "Name=tag:crucible:lab-id,Values=$CRUCIBLE_LAB_ID" "Name=instance-state-name,Values=running" --query 'Reservations[0].Instances[0].InstanceId' --output text)
# Passes only when the machine was broken on purpose (marker), grub.cfg no longer has the bad root, and it booted.
cmd=$(aws ssm send-command --instance-ids "$id" --document-name AWS-RunShellScript --parameters 'commands=["test -e /var/lib/crucible-lab/broken && test -s /boot/grub/grub.cfg && ! grep -q 00000000-0000-0000-0000-000000000000 /boot/grub/grub.cfg && systemctl is-system-running --wait | grep -q -e running -e degraded"]' --query Command.CommandId --output text)
aws ssm wait command-executed --command-id "$cmd" --instance-id "$id"
