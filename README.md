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

### Other games

- **Minecraft Java (vanilla, Paper or Fabric):**
  - choose the server type when creating the server:
    - **Vanilla:** Mojang's official server.
    - **Paper:** the newest stable PaperMC build, checked against its published SHA-256. Plugins (`.jar`) go in `plugins/`, managed from the Plugins tab.
    - **Fabric:** the Fabric server launcher with the newest stable loader. Mods (`.jar`) go in `mods/`, managed from the Mods tab. Most mods also need Fabric API.
  - EULA acceptance required
  - console and player list (`list`)
  - full snapshots, including while the server is running (autosave is paused for the copy); snapshots also include `plugins/`, `mods/` and `config/`
  - a game rules form for common `server.properties` settings
  - a Java version check before start
- **Terraria (vanilla or tModLoader):**
  - choose the server type when creating the server:
    - **Vanilla:** the official server from terraria.org (`latest` or a chosen release such as `1.4.4.9`). The Linux server runs only on x86-64 (amd64) hosts.
    - **tModLoader:** the latest (or a chosen) release from GitHub. The first start downloads the .NET runtime into the server folder, so it also runs on ARM hosts. Upload `.tmod` files from the Mods tab; HSM keeps `Mods/enabled.json` in sync. Workshop mods can be copied from a game client's `steamapps/workshop/content/1281930` folder.
  - world name, size and difficulty chosen at creation; the world is generated on first start
  - `serverconfig.txt` written before each start, keeping any extra lines you add
  - console, player list (`playing`), graceful stop (`exit` saves the world)
  - snapshots of `Worlds`, `serverconfig.txt` and `banlist.txt` (plus `Mods` and `ModConfigs` for tModLoader) while the server is stopped
  - uses TCP in the published `5520`–`5525` range
- **RuneScape: Dragonwilds:** SteamCMD install, dedicated-server settings and console output.

### Schedules and crash recovery

- **Schedules (per server):** restart, start, stop, back up or run a console command, either daily at a set time (optionally on chosen weekdays) or repeating every N minutes or hours.
  - Restarts and stops can warn players in game first, through the game's broadcast command (Minecraft and Terraria `say`, Hytale `/say`).
  - Times use the container's time zone (set `TZ` in Compose).
  - Runs missed while the manager is offline are skipped, not replayed.
- **Auto-restart on crash** (opt-in, in Settings): a server that exits with an error without being asked to stop is started again after 5, 15 and 45 seconds. It is left stopped after 3 crashes in 10 minutes.

Restoring a snapshot first saves a safety copy, then replaces the world and settings with the snapshot's contents. Files created after the snapshot are removed rather than mixed in.

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

Both supplied Compose files explicitly configure container DNS (Cloudflare `1.1.1.1` and Google `8.8.8.8`) because Hytale's OAuth/device login and token refresh require the manager container to resolve Hytale authentication hosts. Hytale server processes run inside the manager container, so this setting covers their authentication requests too. If your network blocks public DNS or requires a local DNS resolver, replace these entries with DNS servers reachable from your host network.

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

## Troubleshooting Hytale authentication

If `/auth login device` fails with `java.nio.channels.UnresolvedAddressException`, or logs show `OAuth token refresh IO error`, the server process may be unable to resolve or reach Hytale's authentication hosts from inside the manager container. First update/recreate the manager using one of the supplied Compose files so the `dns:` settings take effect. Then retry `/auth login device` and complete the newly generated code promptly. Verify with `/auth status`.

If the error persists, the DNS servers may be unreachable or outbound HTTPS may be blocked by your network. Check the router/firewall DNS and HTTPS rules, and substitute your network's reachable DNS resolver in the Compose file if public DNS is restricted. Do not delete the persistent `/data` directory or authentication files as a first troubleshooting step.

## Networking

The web panel uses TCP `8008` on the host and container. The Compose and CasaOS definitions publish:

- `5520`–`5525` over UDP and TCP, for Hytale (UDP/QUIC), Minecraft Java (TCP) and Terraria (TCP) servers. `HSM_GAME_PORT_START` and `HSM_GAME_PORT_END` must match this range.
- `7777`–`7782` over UDP, for RuneScape: Dragonwilds servers.

The manager only assigns ports from these ranges; it never changes the container's published ports. If you widen a range, add the matching `ports:` entries in Compose or the CasaOS app settings too. When the manager can read its own container through the Docker socket, a server's Overview warns if its port is not published.

## Hytale files

This project does not bundle proprietary Hytale server files. Install them with the official Hytale downloader or import your own authorized `game.zip`.

## Game artwork

Official icons and logos for Hytale, Minecraft Java, RuneScape: Dragonwilds and Terraria are bundled. A game without artwork falls back to a coloured monogram. Artwork is optional, bundled into the build, and only used when the file exists:

```text
cmd/hsm/web/assets/games/art/<game-id>-icon.webp    square, 256×256, transparent background
cmd/hsm/web/assets/games/art/<game-id>-logo.webp    the game's wordmark, shown on the Overview card
cmd/hsm/web/assets/games/art/<game-id>-banner.jpg   optional wide background, about 1200×400
```

Game IDs: `hytale`, `minecraft-java`, `runescape-dragonwilds`, `terraria`. Accepted formats: PNG, WebP, JPG/JPEG or SVG. Rebuild the image after adding files. Game logos and artwork belong to their publishers; only add artwork you are permitted to use.

## Development checks

```bash
go vet ./... && go test ./...
node --check cmd/hsm/web/app.js
# Browser regression test for server switching (needs the `playwright` npm package and Chromium):
go build -o /tmp/hsm ./cmd/hsm && node tests/browser/workspace-selection.mjs /tmp/hsm
```

CI runs the same browser test as a separate `browser` job. The browser test starts its own HSM instance against a temporary data directory and uses a fake Hytale `start.sh`, so it never downloads games or touches real server data. See `docs/audit-2026-10-workspace-selection.md` for the current audit and backlog.
