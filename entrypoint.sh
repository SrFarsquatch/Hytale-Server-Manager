#!/bin/bash
set -e
mkdir -p /data
if ! gosu hsm test -w /data; then
  chown hsm:hsm /data
fi
exec gosu hsm "$@"
