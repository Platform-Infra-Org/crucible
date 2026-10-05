#!/bin/sh
if [ -f /tmp/paid ]; then
  echo "Paid in full."
  exit 0
fi
echo "No /tmp/paid yet."
exit 1
