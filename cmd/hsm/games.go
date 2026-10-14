package main

import (
	"io/fs"
	"net/http"
	"strings"
)

// GameCapability describes a feature the manager can safely expose for a game.
// Capabilities are explicit so the UI does not imply support that has not been
// implemented and verified for a particular server distribution. The same
// declaration is served to the browser and enforced by the API handlers, so a
// capability must only be set when its backend implementation exists.
type GameCapability struct {
	// Console means process output (logs and the live event stream) is available.
	Console bool `json:"console"`
	// ConsoleCommands means commands can be written to the server's stdin.
	ConsoleCommands bool `json:"consoleCommands"`
	FileManagement  bool `json:"fileManagement"`
	// Backups covers the native backup command and full offline snapshots.
	Backups bool `json:"backups"`
	// Configuration means the server settings form applies to this game.
	Configuration bool `json:"configuration"`
	// ConfigFiles lists the structured files the in-app config editor may open.
	ConfigFiles []string `json:"configFiles"`
	Mods        bool     `json:"mods"`
	PlayerList  bool     `json:"playerList"`
	DeviceAuth  bool     `json:"deviceAuth"`
	GameUpdates bool     `json:"gameUpdates"`
	Variants    []string `json:"variants"`
}

// GameCatalogEntry is metadata for the create-server UI. Available means that
// this release can create and manage the game end-to-end, not merely that the
// game is planned.
type GameCatalogEntry struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Available    bool           `json:"available"`
	Protocol     string         `json:"protocol"`
	Capabilities GameCapability `json:"capabilities"`
	Art          GameArt        `json:"art"`
}

// GameArt holds URLs for optional official artwork bundled under
// web/assets/games/art. Empty fields mean the file is absent and the browser
// falls back to a generated monogram, so a missing image is never requested.
type GameArt struct {
	Icon   string `json:"icon,omitempty"`
	Banner string `json:"banner,omitempty"`
}

var gameArtExtensions = []string{"png", "webp", "jpg", "jpeg", "svg"}

// gameArtFrom looks for <id>-icon.<ext> and <id>-banner.<ext> in fsys, which is
// rooted at the embedded web directory.
func gameArtFrom(fsys fs.FS, id string) GameArt {
	find := func(kind string) string {
		for _, ext := range gameArtExtensions {
			name := "assets/games/art/" + id + "-" + kind + "." + ext
			if info, err := fs.Stat(fsys, name); err == nil && !info.IsDir() {
				return "/" + name
			}
		}
		return ""
	}
	return GameArt{Icon: find("icon"), Banner: find("banner")}
}

func embeddedGameArt(id string) GameArt {
	web, err := fs.Sub(webFS, "web")
	if err != nil {
		return GameArt{}
	}
	return gameArtFrom(web, id)
}

var hytaleConfigFiles = []string{"config.json", "permissions.json", "whitelist.json", "bans.json"}

func supportedGameCatalog() []GameCatalogEntry {
	return []GameCatalogEntry{
		{
			ID: "hytale", Name: "Hytale", Available: true, Protocol: "udp",
			Capabilities: GameCapability{
				Console: true, ConsoleCommands: true, FileManagement: true, Backups: true,
				Configuration: true, ConfigFiles: hytaleConfigFiles, Mods: true, PlayerList: true,
				DeviceAuth: true, GameUpdates: true,
				Variants: []string{"dedicated"},
			},
		},
		{
			ID: "minecraft-java", Name: "Minecraft Java Edition", Available: true, Protocol: "tcp",
			Capabilities: GameCapability{
				Console: true, ConsoleCommands: true, FileManagement: true, Configuration: true,
				ConfigFiles: []string{}, Variants: []string{"vanilla"},
			},
		},
		{
			ID: "terraria", Name: "Terraria", Available: false, Protocol: "tcp",
			Capabilities: GameCapability{ConfigFiles: []string{}, Variants: []string{"vanilla", "tmodloader"}},
		},
		{
			// Dragonwilds is stopped with SIGINT and does not read commands from stdin.
			ID: "runescape-dragonwilds", Name: "RuneScape: Dragonwilds", Available: true, Protocol: "udp",
			Capabilities: GameCapability{
				Console: true, FileManagement: true, Configuration: true,
				ConfigFiles: []string{}, Variants: []string{"dedicated"},
			},
		},
	}
}

// normalizeGameType maps persisted game identifiers to catalog IDs. An empty
// value predates multi-game support and therefore means Hytale.
func normalizeGameType(gameType string) string {
	gameType = strings.TrimSpace(strings.ToLower(gameType))
	if gameType == "" {
		return "hytale"
	}
	return gameType
}

// gameCatalogEntryFor returns the catalog entry for an available game. Planned
// and unknown games fail closed with no capabilities.
func gameCatalogEntryFor(gameType string) (GameCatalogEntry, bool) {
	gameType = normalizeGameType(gameType)
	for _, entry := range catalogWithArt() {
		if entry.ID == gameType && entry.Available {
			return entry, true
		}
	}
	return GameCatalogEntry{ID: gameType, Name: gameType, Capabilities: GameCapability{ConfigFiles: []string{}, Variants: []string{}}}, false
}

func gameCapabilitiesFor(gameType string) GameCapability {
	entry, _ := gameCatalogEntryFor(gameType)
	return entry.Capabilities
}

func (c GameCapability) allowsConfigFile(name string) bool {
	for _, allowed := range c.ConfigFiles {
		if allowed == name {
			return true
		}
	}
	return false
}

// requireCapability writes a 409 response and returns false when the server's
// game does not support a feature. Handlers call it before touching game state
// so, for example, Hytale console commands are never written to another game.
func requireCapability(w http.ResponseWriter, s ServerSettings, feature string, supported func(GameCapability) bool) bool {
	entry, _ := gameCatalogEntryFor(s.GameType)
	if supported(entry.Capabilities) {
		return true
	}
	jsonError(w, http.StatusConflict, feature+" is not supported for "+entry.Name+" servers")
	return false
}

// catalogWithArt is the catalog as served to the browser, with artwork URLs.
func catalogWithArt() []GameCatalogEntry {
	games := supportedGameCatalog()
	for i := range games {
		games[i].Art = embeddedGameArt(games[i].ID)
	}
	return games
}

func (a *App) handleGameCatalog(w http.ResponseWriter, r *http.Request) {
	jsonOut(w, 200, map[string]any{"games": catalogWithArt()})
}
