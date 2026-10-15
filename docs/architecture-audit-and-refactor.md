# Architecture Audit and Refactor Plan

## Decision

Keep the Go application and its existing embedded web UI. Do not restart from a blank repository and do not copy Crafty Controller wholesale. Use Crafty as a behavioral reference and build a game-neutral core with game-specific adapters.

This is an incremental refactor. Hytale is the compatibility baseline: existing configuration files, authentication, world data, imports, snapshots, ports, and the legacy single-server storage layout must continue to work.

## Baseline findings

- The application entry point and much of the domain/HTTP layer are concentrated in `cmd/realmkeeper/main.go`, while the existing `cmd/realmkeeper/game_adapter.go` already provides a first adapter seam for launch preparation. The seam is incomplete: `ServerManager.Installed`, `ServerManager.Start`, and `ServerManager.Stop` still branch on game type, so the supervisor is not yet game-neutral.
- The UI is embedded from `cmd/realmkeeper/web/*`; edits to the frontend must continue to be shipped in the application image, and cache keys must be bumped when static assets change.
- Server identity is passed to APIs using `X-HSM-Server`. The frontend keeps selected-server state and uses asynchronous API calls, so selection changes must be treated as a concurrency boundary.
- The README describes Hytale-specific behavior and legacy data migration. That behavior is a compatibility contract, not disposable scaffolding.
- The current Go module declaration is `github.com/example/hytale-server-manager`, which does not match the public repository path. Before changing it, search all Go files and workflows for imports or scripts that depend on the old module path.

## Target boundaries

### 1. Domain and persistence

Keep server identity, game ID, display name, per-server settings, install state, storage roots, assigned ports, and lifecycle state in a stable server record. The registry is the authoritative mapping from server ID to configuration and runtime manager.

Do not store mutable per-server UI data globally in a way that survives selection changes. Every API response and event must be scoped to an explicit server ID.

### 2. Shared process supervisor

The shared supervisor owns stdin/stdout/stderr pipes, logs, start/stop/restart orchestration, crash state, exit code, install locks, and cancellation. Game adapters choose the process specification and graceful-stop mechanism; the supervisor owns the mechanics and timeout fallback. The first code step is to remove the game-type branches from `Installed`, `Start`, and `Stop`, without moving the existing Hytale or Dragonwilds configuration/install implementations yet.

All process launch operations must be built from validated adapter output. Never accept an arbitrary executable or working directory from browser input.

### 3. Game adapter contract

Each adapter provides game metadata and game-specific operations, with capabilities declared explicitly rather than inferred from UI labels:

- validate creation settings;
- install or import server files;
- report whether the instance is installed;
- build a launch specification (executable, arguments, working directory, environment);
- request graceful shutdown, with a bounded force-stop fallback;
- read/write supported game configuration;
- expose supported management capabilities (console commands, players, mods, native backups).

Initial adapters: Hytale (preserve current behavior), RuneScape: Dragonwilds (preserve current SteamCMD/config behavior), Minecraft Java (vanilla first, then supported mod loaders), Terraria (vanilla first, then tModLoader). A UI feature must be disabled unless the selected adapter declares it supported.

### 4. HTTP/API layer

Handlers validate authentication, server ID, request shape, and authorization, then call a domain/service method. Handlers should not contain game-specific launch or filesystem logic. Preserve existing API response fields during the migration; additions should be backward-compatible.

### 5. Embedded web UI

Treat server selection as a transaction:
1. increment a selection generation;
2. set the selected server ID;
3. clear old server-scoped status, logs, files, settings, players, and install state;
4. abort prior requests where possible;
5. apply a response only if its captured server ID and generation still match the current selection;
6. reconnect the selected server's live event stream.

This is stronger than checking only whether a request completed successfully and prevents a late response from server A painting over server B.

## Migration sequence

### Step A — establish a reproducible baseline

- Record the current `main` commit and open PRs before modifying anything.
- Run `gofmt`, `go test ./...`, `go vet ./...`, JavaScript syntax checks, Compose validation, and Docker build.
- Add characterization tests for existing Hytale launch/config/data-path behavior before moving code.
- Identify any open branches/PRs not included in the default branch; do not assume they are merged.

### Step B — isolate the game contract

- Complete the existing `GameAdapter` seam so install detection, process specification, and graceful shutdown are adapter-owned; keep process supervision shared.
- Preserve the current Hytale and Dragonwilds launch behavior while moving one responsibility at a time.
- Keep existing API endpoints and persisted JSON fields stable.
- Add contract tests shared by every adapter before moving installation and configuration handlers.

### Step C — extract shared lifecycle services

Move process launch, log fan-out, lifecycle classification, and cancellation into a dedicated package only after characterization tests pass. Keep HTTP handlers and filesystem APIs working throughout.

### Step D — fix server-selection isolation

Add a single selection-generation guard (and request cancellation where practical) to the frontend data loader. Add tests for rapid A → B switching and delayed A responses. Do not make another visual redesign the primary task.

### Step E — complete one vertical slice per game

For each game, prove create → install/import → start → console/logs → stop → restart → backup/restore (where supported) → delete. Test both multiple instances of one game and mixed-game instances. Mark unsupported capabilities clearly rather than presenting nonfunctional tabs.

### Step F — deploy safely

Run the full test/build pipeline and a CasaOS smoke test. Never delete or rename the persistent `/data` directory as part of a refactor. Document whether the currently deployed image contains the tested commit.

## Definition of done for the first refactor PR

- No user-visible feature changes.
- Hytale behavior and storage paths remain unchanged.
- Adapter selection is deterministic from the persisted game ID.
- Tests prove that an unknown game ID fails closed instead of falling through to Hytale.
- Go tests, vet, build, frontend syntax, Compose validation, and Docker build pass.
- The PR describes any checks that could not be run; it does not claim live CasaOS validation without evidence.

## Important licensing boundary

Crafty Controller is useful as a product and architecture reference. Before reusing any of its source code, review its license and obligations for the intended distribution. The default approach is to implement equivalent behavior independently in this Go codebase and use original UI assets.
