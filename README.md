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

Open `http://YOUR-SERVER-IP:8008`.

The default Hytale UDP range is `5520-5525`. Each running instance gets its own port from that range.

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

## First-run setup

A new installation starts with an empty server list. Sign in with the admin account you create on first launch, then use **Create your first server** to configure a Hytale instance. The guided setup covers server name, UDP port, public address, game mode, player limit, optional join password, whitelist permission, JVM memory, backups and autostart. After creation, follow the Install page to download or import authorized Hytale server files.

Existing installations keep their configured servers and legacy single-server settings are migrated automatically. The new server settings are saved separately from the Hytale world data.

Game mode, server name, message, player limit, password and join-permission requirements are applied to the Hytale config.json when the server is stopped and settings are saved or before it starts. Hytale's configuration schema can change between game updates, so keep a backup before making advanced manual edits.

## Updating the application

Open **Application settings → Application updates** and choose **Check for updates**. When a newer stable GitHub release is available, choose **Update and restart**. The updater pulls the published stable image from GitHub Container Registry, stages the existing container for rollback, and recreates the manager with its existing ports, mounts, environment, networks, and restart policy. The persistent `/data` volume is not removed. If the replacement fails to start, the updater attempts to restore the previous container.

In-container updates require the Docker socket mount shown in both Compose files. This is a powerful permission: anyone who compromises the manager can potentially gain host-level control through Docker. Use a strong admin password and keep the web dashboard on a trusted network. If you do not want to grant Docker socket access, update the image through CasaOS instead.

Stable releases are published automatically on each push to `main`. The workflow builds and publishes multi-architecture `linux/amd64` and `linux/arm64` images, updates the `latest` image, and creates a stable GitHub Release with generated notes. Version tags follow `v0.6.N` using the workflow run number. The in-app updater checks the latest stable GitHub Release and applies the `latest` container image. Ensure the GitHub Container Registry package is public so a self-hosted instance can pull release images without registry credentials.

## Networking

The web panel uses TCP `8008` on the host and container. Hytale game servers use UDP/QUIC. The Compose and CasaOS definitions publish only UDP ports `5520`, `5521`, `5522`, `5523`, `5524`, and `5525`, allowing up to six instances to use unique ports.

## Hytale files

This project does not bundle proprietary Hytale server files. Install them with the official Hytale downloader or import your own authorized `game.zip`.
