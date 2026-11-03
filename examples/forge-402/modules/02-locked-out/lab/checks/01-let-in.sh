#!/usr/bin/env bash
set -euo pipefail
id=$(aws ec2 describe-instances --filters "Name=tag:crucible:lab-id,Values=$CRUCIBLE_LAB_ID" "Name=instance-state-name,Values=running" --query 'Reservations[0].Instances[0].InstanceId' --output text)
timeout 10 lab-ssh --direct "$id" true
