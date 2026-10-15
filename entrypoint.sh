#!/bin/bash
set -e
mkdir -p /data
if ! gosu realmkeeper test -w /data; then
  chown realmkeeper:realmkeeper /data
fi

# When self-updates are enabled, let the unprivileged manager access the mounted
# Docker socket. Docker socket access is effectively host-root access.
if [ -S /var/run/docker.sock ]; then
  docker_gid="$(stat -c '%g' /var/run/docker.sock)"
  user_gid="$(id -g realmkeeper)"
  if [ "$docker_gid" != "$user_gid" ]; then
    socket_group="$(getent group "$docker_gid" | cut -d: -f1 || true)"
    if [ -z "$socket_group" ]; then
      socket_group="realmkeeper-docker-socket"
      if ! getent group "$socket_group" >/dev/null; then
        groupadd --gid "$docker_gid" "$socket_group"
      fi
    fi
    usermod -a -G "$socket_group" realmkeeper
  fi
fi

exec gosu realmkeeper "$@"
