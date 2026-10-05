#!/bin/sh
if wget -q -T 2 -O /dev/null http://127.0.0.1:80/; then
  echo "The crucible burns bright on port 80."
  exit 0
fi
echo "nginx is not answering on port 80 yet. Did you reload it after editing?"
exit 1
