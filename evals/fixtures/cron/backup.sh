#!/bin/sh
set -u
# Nightly backup, run by cron at 02:00.
cp -r data "$DEST/backup-$(date +%F)"
