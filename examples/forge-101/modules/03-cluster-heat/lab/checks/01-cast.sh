#!/bin/sh
if grep -qx 'hello crucible' /tmp/cast.txt 2>/dev/null; then
  echo "Cast in the crucible."
  exit 0
fi
echo "/tmp/cast.txt must contain exactly: hello crucible"
exit 1
