#!/bin/sh
# Offline on purpose: the dry-run e2e has no AWS behind it.
got=$(tr -d '[:space:]' < "$HOME/region.txt" 2>/dev/null)
if [ -z "$got" ]; then
  echo "No ~/region.txt yet."
  exit 1
fi
if [ "$got" != "$AWS_REGION" ]; then
  echo "~/region.txt says $got, but your lab runs somewhere else."
  exit 1
fi
if [ ! -s "$AWS_SHARED_CREDENTIALS_FILE" ]; then
  echo "Your lab credentials are missing; end the lab and start it again."
  exit 1
fi
echo "Your CLI points at $AWS_REGION with your lab's credentials."
