#!/usr/bin/env bash
# Checks the workspace helpers without AWS: syntax, shellcheck when installed, and their argument handling.
set -euo pipefail
cd "$(dirname "$0")"
for f in lab-ssh lab-console; do
  bash -n "$f"
  if command -v shellcheck >/dev/null; then shellcheck "$f"; fi
done
# No arguments: usage on stderr, exit 2, no AWS call (aws is not on PATH here).
for f in lab-ssh lab-console; do
  set +e; out=$(PATH=/usr/bin:/bin ./"$f" 2>&1); code=$?; set -e
  [ "$code" = 2 ] || { echo "$f without arguments: exit $code, want 2"; exit 1; }
  grep -q usage <<<"$out" || { echo "$f: no usage line"; exit 1; }
done
# A malformed instance id is refused before any AWS call.
for f in lab-ssh lab-console; do
  set +e; PATH=/usr/bin:/bin ./"$f" 'i-123;reboot' >/dev/null 2>&1; code=$?; set -e
  [ "$code" = 2 ] || { echo "$f accepted a malformed instance id"; exit 1; }
done
grep -qx 'il-central-1 ec2-serial-console.il-central-1.api.aws SHA256:JR6q8v6kNNPi8+QSFQ4dj5dimNmZPTgwgsM1SNvtYyU' serial-console-endpoints
grep -qx 'eu-west-1 serial-console.ec2-instance-connect.eu-west-1.aws SHA256:h2AaGAWO4Hathhtm6ezs3Bj7udgUxi2qTrHjZAwCW6E' serial-console-endpoints
echo "workspace helpers OK"
