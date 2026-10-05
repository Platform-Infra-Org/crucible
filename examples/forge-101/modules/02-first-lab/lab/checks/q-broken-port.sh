#!/bin/sh
# Runs in the "web" service. $CRUCIBLE_ANSWER holds the trainee's answer.
if [ "$(echo "$CRUCIBLE_ANSWER" | tr -d ' ')" = "8081" ]; then
  echo "Correct: the forge moved to 8081."
  exit 0
fi
echo "Not quite. Try: grep listen /etc/nginx/conf.d/default.conf"
exit 1
