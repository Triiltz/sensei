#!/bin/sh
if [ "$(id -u)" -ne 0 ]; then
  echo "deploy.sh: must be run as root" >&2
  exit 1
fi
echo "deploying..."
