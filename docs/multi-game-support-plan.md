# Multi-Game Server Support — Implementation Plan

**Status:** Planning / first migration  
**Target games:** Hytale, Minecraft Java Edition, Terraria, RuneScape: Dragonwilds  
**Initial deployment:** One Linux host via Docker/CasaOS  
**Runtime:** Direct host processes managed by the existing Go application

## Goals and non-goals

### Goals
- Add multiple game families without regressing existing Hytale servers or their data.
- Keep a common server lifecycle and dashboard while isolating game-specific behavior.
- Support vanilla/dedicated and modded variants only where the game and distribution actually support them.
- Keep persistent data under the existing `HSM_DATA_DIR` volume; upgrades must not delete or relocate user worlds.
- Make the first migration small, reversible, and testable before adding additional game installers.
- Preserve the existing single-binary Go deployment and embedded `web/*` UI during the first phases.

### Non-goals for the first migration
- No rewrite to a Node/Fastify backend or monorepo.
- No mandatory database migration; retain the current config storage until a separate decision is justified.
- No multi-host scheduler or container-per-server runtime in the MVP.
- No assumption that every game supports console commands, player counts, mods, or live configuration in the same way.

## Repository baseline

The current project is a Go application built from `cmd/hsm`, with its HTTP API and server management implementation concentrated in `cmd/hsm/main.go`. The frontend is embedded with `//go:embed web/*`. Existing server records use `ServerSettings` and are persisted in the controller configuration. Hytale-specific assumptions appear in defaults, config editing, JVM options, installation/download flows, server startup, mods, and backups.

The Docker image currently builds the Go binary and runs it in a container with persistent `/data`. CasaOS uses the same application image. The first migration must therefore avoid moving the entry point, changing the volume layout, or making a new runtime dependency mandatory.

## Architecture

Introduce a small game-adapter boundary inside the existing Go application first. Do not split packages until the boundary is proven and tests cover it.

### Shared server identity

Add a `GameType` field to each server record, serialized as `gameType`. Use stable identifiers:
- `hytale`
- `minecraft-java`
- `terraria`
- `runescape-dragonwilds`

**Backward compatibility:** when `gameType` is absent or blank in an existing config, interpret it as `hytale`. Keep existing IDs, ports, storage paths, and all Hytale settings unchanged. Saving migrated config may populate `gameType: "hytale"`, but should not otherwise rewrite server data.

Keep game-specific settings separate from shared fields. Prefer a typed shared record plus a game-specific configuration object or validated per-game settings rather than continuing to add unrelated fields to the shared struct.

### Adapter contract

The first adapter interface should describe capabilities and operations without assuming every game supports every feature. Candidate operations:

- Identity and display metadata: game ID, display name, supported server variants.
- Validation and defaults for a new server.
- Installation readiness and installation workflow.
- Build a launch command from validated configuration.
- Start, graceful stop, forced stop fallback, restart, and process exit handling.
- Console input and log/event collection where supported.
- Game-specific status/player parsing where supported.
- Configuration validation and safe application.
- Backup/restore hooks for worlds and server data.
- Mod support metadata and validation.

Represent unsupported capabilities explicitly. The UI must not show a nonfunctional Mods tab or player count for a game that does not expose those features. Shared process management, resource monitoring, path safety, log streaming, and generic snapshots should remain reusable.

### Process lifecycle and security

- Continue to launch direct child processes managed by Go; do not shell-concatenate user-controlled command strings.
- Use explicit executable and argument arrays, a controlled working directory, and a per-server environment.
- Validate paths and prevent server IDs, uploaded filenames, or config values from escaping the server's data directory.
- Ensure stop/restart targets only the process owned by that server manager.
- Use graceful termination first, then a bounded timeout and forced termination.
- Keep each server's install, runtime, world/save, config, log, mod, and backup paths isolated.
- Preserve existing Hytale process and auth behavior until adapter parity tests prove it is safe to route through the new abstraction.

## Data and API migration

1. Add `gameType` with an empty/default value compatible with old JSON.
2. Add an explicit game catalog endpoint for the UI to populate creation choices and capability flags.
3. Keep existing `/api/servers` and lifecycle endpoints compatible. Existing clients that omit `gameType` must still create Hytale servers during the compatibility period.
4. Add server-specific endpoints only when the operation cannot safely share existing routes; prefer a server ID in the path over implicit global selection for new functionality.
5. Validate every create/update request against the selected adapter before persisting.
6. Add migration tests using fixtures for legacy single-server config and multi-server config.
7. Never delete, rename, or automatically move a legacy server directory during metadata migration.

## Game rollout

### 0. Hytale compatibility adapter (first migration)
- Wrap current behavior with minimal changes.
- Preserve official downloader/import workflow, auth, updater, JVM memory, config files, mods, backups, snapshots, port allocation, and existing server IDs.
- Add regression tests for config loading, missing `gameType`, server selection, start/stop dispatch, and Hytale-specific config application.
- Do not implement the other game installers in this step.

### 1. Minecraft Java Edition
- Add vanilla dedicated-server installation and launch first.
- Validate Java runtime compatibility with the selected server version; do not assume the manager container's bundled Java is suitable for every Minecraft version.
- Provide EULA acceptance as an explicit user action, `server.properties`, world path, port, online-mode and other standard configuration, logs, graceful stop, and backups.
- Add Fabric, Forge, and NeoForge as distinct server variants after vanilla lifecycle tests pass. Do not treat arbitrary JAR uploads as trusted executables.
- Use the adapter's capability metadata to expose mod management only for supported variants.

### 2. Terraria
- Add the official dedicated server workflow, world selection/creation, server configuration, port, logs, process lifecycle, and world backups.
- Add tModLoader as a distinct modded variant after the vanilla implementation is stable.
- Do not assume Terraria console semantics match Minecraft or Hytale.

### 3. RuneScape: Dragonwilds
- Verify current official dedicated-server distribution, platform support, installation method, required runtime, launch arguments, configuration schema, save locations, client/server version compatibility, and required TCP/UDP ports against current official documentation and releases before coding the installer.
- Keep SteamCMD or other installation tooling isolated to this adapter and pin/validate app identifiers from authoritative sources.
- Treat owner/player ID, admin credentials, world settings, and port allocation as validated game-specific configuration.
- Back up saves before upgrades or restores and stop the process before applying settings that are only read at startup.
- Initially support the official dedicated-server capabilities only. Do not advertise general mod installation unless a supported and repeatable Dragonwilds mod workflow is verified.

## UI changes

- Add a game selector to the create-server flow and show game identity on server cards/list rows.
- Keep the selected-server workspace pattern: Console, Files, Configuration, Backups, and game-supported Mods as tabs.
- Build tabs from adapter capability metadata, not hard-coded assumptions.
- Use game-specific create/config forms while reusing common server identity, status, resource metrics, and lifecycle controls.
- Make mobile navigation and tabs horizontally usable without hiding core start/stop/status controls.
- Preserve current Hytale routes and labels until the replacement screen has feature parity.

## First migration sequence

1. Capture the current default-branch state and run the existing Go test suite, `go vet ./...`, and build.
2. Add `GameType` with backward-compatible defaulting and tests.
3. Add a minimal adapter registry and Hytale adapter that delegates to existing behavior.
4. Route only one safe operation at a time through the adapter; start with validation/defaults, then lifecycle after tests.
5. Add a game catalog/capability response.
6. Add tests for legacy config migration and Hytale parity.
7. Build the image and verify the existing `/data` volume, CasaOS deployment, and Hytale start/stop/backup flows before merging.
8. Implement Minecraft vanilla on a follow-up branch once the adapter boundary is validated.

## Acceptance criteria for the first migration

- Existing config files without `gameType` load as Hytale servers.
- Existing server IDs, port assignments, data directories, and Hytale settings remain unchanged.
- Multiple Hytale servers still start, stop, restart, and select correctly.
- Hytale downloader/import, console, configuration, mods, backups, snapshots, and updater behavior remain intact.
- Unsupported capabilities are not presented as working features.
- `go test ./...`, `go vet ./...`, and `go build ./cmd/hsm` pass.
- Docker/CasaOS persistent data remains mounted at the same path and survives an application update/rollback.

## Risks and mitigations

- **Hytale regressions from a large refactor:** adapter wrapping is incremental and test-gated.
- **Invalid or malicious launch arguments:** use executable/argument arrays and validation; avoid shell execution.
- **Data loss from changing path conventions:** no automatic directory moves in the first migration.
- **Feature mismatch between games:** explicit capability flags and game-specific configuration.
- **Installer drift:** verify official sources and checksums/version metadata at install time where available.
- **Port conflicts and container exposure:** reserve ports centrally, validate protocols/ranges, and document required Compose/CasaOS port publishing before enabling each adapter.
- **Java/runtime mismatch:** select/check runtime per Minecraft version rather than relying on one bundled runtime.
- **Mod support overpromising:** only enable mod features for verified distributions and workflows.
