#!/bin/sh
if [ -f /tmp/lit ]; then
  echo "The forge is lit."
else
  echo "No fire yet: /tmp/lit is missing."
  exit 1
fi
