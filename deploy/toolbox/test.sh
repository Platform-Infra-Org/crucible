#!/usr/bin/env bash
# Checks the toolbox's crucible-ready shim without building the image: runs the Dockerfile's RUN line into a temp dir
# and checks the shim hands its arguments to `crucible ready`.
set -euo pipefail
cd "$(dirname "$0")"
t=$(mktemp -d); trap 'rm -rf "$t"' EXIT
line=$(grep '^RUN printf .*crucible-ready' Dockerfile) || { echo "Dockerfile: no crucible-ready shim"; exit 1; }
sh -c "$(sed "s#/usr/local/bin#$t#g" <<<"${line#RUN }")"
printf '#!/bin/sh\necho "$*" >"%s/calls"\n' "$t" >"$t/crucible"; chmod +x "$t/crucible"
PATH="$t:$PATH" "$t/crucible-ready" --namespace lab-1 'a b'
[ "$(cat "$t/calls")" = "ready --namespace lab-1 a b" ] || { echo "crucible-ready ran: $(cat "$t/calls")"; exit 1; }
echo "toolbox shim ok"
