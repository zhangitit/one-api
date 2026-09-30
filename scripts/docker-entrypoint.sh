#!/bin/sh
set -eu

mkdir -p /data/logs
chown -R zeonexus:zeonexus /data

exec su-exec zeonexus:zeonexus /usr/local/bin/zeonexus-gateway "$@"
