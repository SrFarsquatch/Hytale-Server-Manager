# Audit and Milestone 1: Workspace Selection Correctness (October 2026)

Baseline: `main` at `4c5b632` (merge of PR #34, Crafty-style workspace). GitHub PR metadata
could not be fetched from this environment, so recent history was reviewed from git commits only.

## 1. Current architecture

- **Backend**: a single Go package, `cmd/hsm`. `main.go` (~2,900 lines) holds HTTP routing, auth,
  the server registry (`ControllerConfig.Servers`, persisted to `/data/config.json`), the
  `ServerManager` process supervisor, logs/SSE, backups and Hytale installers.
  `game_adapter.go` holds the `GameAdapter` seam (install detection, launch preparation, process
  spec, graceful stop), implemented by Hytale, Minecraft Java (`minecraft.go`) and Dragonwilds
  (`dragonwilds.go`). `files.go` holds the sandboxed file manager. `games.go` holds the game
  catalog. `updater.go` handles self-update through the Docker socket.
- **Server identity**: stable slug IDs (`alpha-hytale`), sent as the `X-HSM-Server` header or
  `?server=` (used by SSE). Lifecycle also has path routes, `/api/servers/{id}/start|stop|restart`.
- **Processes**: direct child processes in their own process group, with stdin for commands, a
  2,500-line ring buffer per server, and SSE fan-out. Runtime state is in memory, not persisted.
- **Frontend**: an embedded, framework-free `web/app.js` written in a dense one-line-per-function
  style, plus `index.html`, `app.css` and `panel-redesign.css`.
- **Deployment**: a multi-stage Dockerfile on a Temurin 25 JRE; Compose files for plain Docker
  and CasaOS. CI runs `go test`, `node --check`, `go vet`, the Go build, Compose validation and
  the Docker build.

## 2. What works

Hytale create/import/download/start/stop/restart, console and SSE, device auth, `/update`
shortcuts, native backups and snapshots, mods, JSON config files, per-server JVM settings,
autostart, the sandboxed file manager (with a symlink and traversal guard), updater
stale-state recovery, login rate limiting, and the Minecraft Java vanilla and Dragonwilds
installers and lifecycles. All of these are backed by real endpoints and unit tests.

## 3. Broken or incomplete (found and verified in Chromium against the `main` build)

| # | Defect | Impact | Status |
|---|--------|--------|--------|
| 1 | `renderFleet()` called an undefined `gameIcon()` | Selecting a server threw before its settings loaded. The header showed server B while the settings form still held server A's values, and saving would write A's settings onto B. The overview was stuck on "LOADING". | Fixed |
| 2 | `$('.hytale-only').forEach` (`$` returns one element, not a list) | The create-server wizard never opened | Fixed |
| 3 | `.server-overview { display:flex }` overrode `.view { display:none }` | The Overview rendered above every other tab | Fixed |
| 4 | Start and top-bar Start buttons each had two click listeners | Each Start click sent two start requests | Fixed |
| 5 | Frontend and backend capability maps disagreed, and the backend enforced none | Hytale commands (`/who`, `/auth login device`, `/update apply`, `/backup`) could be sent to Minecraft/Dragonwilds stdin. Hytale downloader/import could write into a Minecraft server's directory. | Fixed |
| 6 | API fell back to the *first* server when no ID was sent | A request missing its ID could stop or edit the wrong server | Fixed (fallback kept only when exactly one server exists) |
| 7 | Logs fully replaced every 2 s while SSE also appended | Duplicate lines, "Clear" undone within 2 s, scroll jumps | Fixed |
| 8 | Overlapping 2 s polls with no in-flight guard | Slow responses stacked and raced | Fixed |
| 9 | Create, delete and "selected server deleted elsewhere" bypassed the selection reset | Stale workspace data after these flows | Fixed |
| 10 | Mutations read `state.serverId` after an `await` | A save, upload loop or post-save refresh could apply to a newly selected server | Fixed |
| 11 | Structured JSON config editor wired to the backend but permanently hidden | Lost feature | Shown for games that declare config files (Hytale) |
| 12 | Server list showed "UDP" for Minecraft (TCP) | Misleading connection info | Fixed |

## 4. Architectural risks (not addressed in this milestone)

- `main.go` mixes routing, registry, supervisor and Hytale logic. Many files are not `gofmt`-clean, so formatting them would create noisy diffs.
- The player query writes `/who` into the Hytale console on every selection and every 30 s, which adds console noise.
- `ServerManager.Stop`'s 75 s escalation timer does not check that the process is still the one it was asked to stop. If PIDs are reused after a fast restart, it could signal an unrelated process group.
- `GET /api/settings` returns server and Dragonwilds admin passwords to the browser.
- The CasaOS compose mounts `/var/run/docker.sock` for the updater, which is effectively host root. This is documented but significant.
- Backups and snapshots are Hytale-path specific (`game/Server/...`). Minecraft has no backups yet.
- The Go module path is `github.com/example/...`, which does not match the repository.
- ~~Sidebar nav icons overlap the navigation labels~~ (resolved by the panel redesign that followed this milestone).

## 5. Milestone 1 changes (this branch)

- `cmd/hsm/games.go`: the catalog is now the single source of truth for per-game capabilities (`consoleCommands`, `configFiles`, `deviceAuth`, `gameUpdates`, protocol). `requireCapability` returns 409 for unsupported features.
- `cmd/hsm/main.go`: capabilities and protocol added to each `/api/servers` entry (additive). Enforcement on console, mods, backups/snapshots, config files, update/auth shortcuts and players. Hytale installers reject other games. The server-ID fallback is limited to single-server installs.
- `cmd/hsm/web/app.js`: one `setSelectedServer()` transition used by every flow. It aborts in-flight reads (`AbortController`), bumps a generation counter, closes SSE and clears all server-scoped state and DOM. Reads use a selection context and discard stale responses. Mutations pass the server ID captured when the user acted. Settings and status failures show a persistent error with Retry. Log streaming is SSE-first with a de-duplicated resync, and polling is used only while disconnected. Workspace tabs and controls come from backend capabilities.
- `index.html` / `panel-redesign.css`: error banner, config editor panel id, overview visibility fix, cache keys bumped.
- CI: ESLint `no-undef` check on `app.js`. This class of bug (defect #1) passed `node --check`.

## 6. Prioritised backlog

| Pri | Task | Depends on | Acceptance criteria |
|-----|------|-----------|---------------------|
| P1 | Run `tests/browser` in CI (install Playwright in the workflow) | — | Regression suite blocks merges |
| P1 | Stop escalation timer bound to the specific `exec.Cmd` | — | Unit test: restart within the timeout never signals the new process |
| P1 | Stop sending `/who` on selection; query only on demand or when the Overview is visible | — | No `/who` lines from passive browsing |
| P2 | Split `main.go` into `registry`, `supervisor`, `handlers` files (same package) and `gofmt` in a separate no-logic commit | — | No behaviour change; tests green |
| P2 | Move remaining `GameType ==` branches (install status, players, settings validation) behind adapter methods | Split | No game-ID string comparisons in handlers |
| P2 | Redact passwords from `GET /api/settings` (write-only fields) | — | Settings round-trip preserves secrets without exposing them |
| P3 | Minecraft: generic snapshot backups, structured `server.properties` editor, `list` player query | Adapter capabilities | Phase 4 exit criteria in the product manifest |
| P3 | Terraria adapter (vanilla) | Adapter cleanup | Phase 5 exit criteria |

## 7. Milestone 1 test plan and results

- `go vet ./...`, `go test ./...` (adds 7 tests in `capabilities_test.go`), `go build`: **pass**.
- `node --check` and ESLint `no-undef` on `app.js`: **pass**. The same lint flags `gameIcon` on the old file.
- `docker compose config -q` for both Compose files: **pass**.
- `node tests/browser/workspace-selection.mjs <binary>`: **15/15 pass** on this branch (twice). Against the `main` binary, 12 of the then-14 checks **fail**, which confirms the suite detects the regressions. It covers:
  - repeated Hytale ↔ Minecraft ↔ Dragonwilds switching and per-game tabs
  - delayed responses from the previous server, rapid switching, and API failure with retry
  - blocked settings saves while loading, and a server deleted elsewhere
  - a server stopped while open, console streaming with no duplicates, and commands reaching only the selected server
  - the wizard opening, mobile overflow, no page errors, and every request naming its server
- **Not run here**: Docker image build (no Docker daemon in this environment; CI runs it), real game downloads, and live CasaOS deployment. Nothing has been deployed.
