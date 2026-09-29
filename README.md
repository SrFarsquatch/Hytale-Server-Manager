# Hytale Server Manager

A self-hosted web panel for running multiple Hytale dedicated servers from one Linux host. It is designed for Docker and CasaOS and is not affiliated with or endorsed by Hypixel Studios.

## What it manages

- Multiple Hytale server processes at the same time
- Separate world data, ports, configs, mods, logs and backups per server
- Start, stop and restart controls
- Live console and server commands
- Hytale device authentication
- Official Hytale downloader/import workflow
- Hytale updater commands
- Native backups and full offline snapshots
- `config.json`, `permissions.json`, `whitelist.json` and `bans.json`
- Per-server JVM memory and autostart settings
- Linux amd64 and arm64 builds

## Run with Docker Compose

```bash
git clone https://github.com/SrFarsquatch/Hytale-Server-Manager.git
cd Hytale-Server-Manager
./install.sh
```

Open `http://YOUR-SERVER-IP:8090`.

The default Hytale UDP range is `5520-5539`. Each running instance gets its own port from that range.

## CasaOS

Use `docker-compose.casaos.yml`. It pulls the published multi-architecture image from GitHub Container Registry. New installs store persistent data in:

```text
/DATA/AppData/hytale-server-manager
```

## Existing Orbis v0.4 data

The backend still accepts the old `ORBIS_*` environment variables, `X-Orbis-Server` header and `orbis_session` cookie for migration compatibility. If you already have data under an older bind mount, point the new container at that same directory instead of creating a new empty volume.

## Server layout

The first server remains compatible with the original single-server storage layout. Additional servers are kept under:

```text
/data/servers/<server-id>/
```

Each instance keeps its own `game/`, imports, snapshots, Hytale world/universe data, mods, configuration and backups.

## Networking

The web panel uses TCP `8090` on the host. Hytale game servers use UDP/QUIC. The default Compose file publishes UDP `5520-5539` so up to 20 instances can be assigned unique ports without changing the container definition.

## Hytale files

This project does not bundle proprietary Hytale server files. Install them with the official Hytale downloader or import your own authorized `game.zip`.
