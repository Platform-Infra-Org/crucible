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
# lab-console's host key pinning, with stub ssh/ssh-keygen/aws: a wrong, missing or unpinned key never reaches aws.
t=$(mktemp -d); trap 'rm -rf "$t"' EXIT
printf '#!/bin/sh\nexit 1\n' >"$t/ssh"
printf '#!/bin/sh\necho "$*" >>"%s/aws.calls"\n' "$t" >"$t/aws"
cat >"$t/ssh-keygen" <<'STUB'
#!/bin/sh
if [ "$1" = -lf ]; then [ -n "${FAKE_FP:-}" ] && echo "256 $FAKE_FP host (ED25519)"; exit 0; fi
exit 0
STUB
chmod +x "$t/ssh" "$t/aws" "$t/ssh-keygen"
printf 'r-1 host.example SHA256:pinned\nr-2 host.example\n' >"$t/endpoints"
console() { # region fingerprint-the-endpoint-presents
  rm -f "$t/aws.calls"; set +e
  PATH="$t:/usr/bin:/bin" AWS_REGION=$1 FAKE_FP=$2 CRUCIBLE_SERIAL_ENDPOINTS="$t/endpoints" ./lab-console i-0123456789abcdef0 >/dev/null 2>&1
  code=$?; set -e
}
console r-1 SHA256:other;  { [ "$code" != 0 ] && [ ! -e "$t/aws.calls" ]; } || { echo "lab-console: wrong host key not refused"; exit 1; }
console r-1 "";            { [ "$code" != 0 ] && [ ! -e "$t/aws.calls" ]; } || { echo "lab-console: missing host key not refused"; exit 1; }
console r-9 SHA256:pinned; { [ "$code" != 0 ] && [ ! -e "$t/aws.calls" ]; } || { echo "lab-console: unknown region not refused"; exit 1; }
console r-2 "";            { [ "$code" != 0 ] && [ ! -e "$t/aws.calls" ]; } || { echo "lab-console: unpinned endpoint not refused (empty == empty)"; exit 1; }
console r-2 SHA256:x;      { [ "$code" != 0 ] && [ ! -e "$t/aws.calls" ]; } || { echo "lab-console: endpoint with no fingerprint accepted"; exit 1; }
console r-1 SHA256:pinned; [ -e "$t/aws.calls" ] || { echo "lab-console: correct host key did not reach aws"; exit 1; }
grep -qx 'il-central-1 ec2-serial-console.il-central-1.api.aws SHA256:JR6q8v6kNNPi8+QSFQ4dj5dimNmZPTgwgsM1SNvtYyU' serial-console-endpoints
grep -qx 'eu-west-1 serial-console.ec2-instance-connect.eu-west-1.aws SHA256:h2AaGAWO4Hathhtm6ezs3Bj7udgUxi2qTrHjZAwCW6E' serial-console-endpoints
# lab-ssh with a remote command, stub aws/ssh/ssh-keygen: ssh gets user@private-ip, then the command words.
s=$(mktemp -d); trap 'rm -rf "$t" "$s"' EXIT
printf '#!/bin/sh\nfor a; do echo "$a"; done >"%s/ssh.args"\n' "$s" >"$s/ssh"
printf '#!/bin/sh\ncase "$*" in *describe-instances*) echo 10.0.1.5;; esac\n' >"$s/aws"
printf '#!/bin/sh\nexit 0\n' >"$s/ssh-keygen"
chmod +x "$s/ssh" "$s/aws" "$s/ssh-keygen"
PATH="$s:/usr/bin:/bin" ./lab-ssh --direct i-0123456789abcdef0 ubuntu test -e /x >/dev/null 2>&1 || { echo "lab-ssh with a command failed"; exit 1; }
tail -4 "$s/ssh.args" | tr '\n' ' ' | grep -qx 'ubuntu@10.0.1.5 test -e /x ' || { echo "lab-ssh: ssh args: $(tr '\n' ' ' <"$s/ssh.args")"; exit 1; }
echo "workspace helpers OK"
