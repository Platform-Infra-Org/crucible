#!/bin/sh
if grep -qx 'hello forge' /tmp/forged.txt 2>/dev/null; then
  echo "Your first ingot is cast."
  exit 0
fi
echo "/tmp/forged.txt must contain exactly: hello forge"
exit 1
