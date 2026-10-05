#!/bin/sh
# Idempotent: always converges to "nginx listens on 8081" (spec §8.5).
set -e
sed -i -e 's/listen\([[:space:]]*\)80;/listen\18081;/' -e 's/\[::\]:80;/[::]:8081;/' /etc/nginx/conf.d/default.conf
nginx -s reload
