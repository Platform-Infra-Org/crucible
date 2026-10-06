#!/bin/sh
if aws s3api head-object --bucket "crucible-lab-$CRUCIBLE_LAB_ID" --key forged.txt >/dev/null 2>&1; then
  echo "forged.txt is in your lab bucket."
  exit 0
fi
echo "No forged.txt in s3://crucible-lab-$CRUCIBLE_LAB_ID yet."
exit 1
