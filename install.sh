#!/bin/bash
set -euo pipefail
mkdir -p data/tools data/servers
printf 'Building and starting Hytale Server Manager...\n'
docker compose up -d --build
printf '\nOpen http://%s:8090\n' "$(hostname -I 2>/dev/null | awk '{print $1}' || echo YOUR-SERVER-IP)"
printf 'Hytale instances can use UDP %s-%s by default.\n' "${HYTALE_PORT_START:-5520}" "${HYTALE_PORT_END:-5539}"
