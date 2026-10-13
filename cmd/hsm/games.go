package main

import "net/http"

// GameCapability describes a feature the manager can safely expose for a game.
// Capabilities are explicit so the UI does not imply support that has not been
// implemented and verified for a particular server distribution.
type GameCapability struct {
	Console        bool     `json:"console"`
	FileManagement bool     `json:"fileManagement"`
	Backups        bool     `json:"backups"`
	Configuration  bool     `json:"configuration"`
	Mods           bool     `json:"mods"`
	PlayerList     bool     `json:"playerList"`
	Variants       []string `json:"variants"`
}

// GameCatalogEntry is metadata for the create-server UI. Available means that
// this release can create and manage the game end-to-end, not merely that the
// game is planned.
type GameCatalogEntry struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Available    bool           `json:"available"`
	Capabilities GameCapability `json:"capabilities"`
}

func supportedGameCatalog() []GameCatalogEntry {
	return []GameCatalogEntry{
		{
			ID: "hytale", Name: "Hytale", Available: true,
			Capabilities: GameCapability{
				Console: true, FileManagement: true, Backups: true,
				Configuration: true, Mods: true, PlayerList: true,
				Variants: []string{"dedicated"},
			},
		},
		{
			ID: "minecraft-java", Name: "Minecraft Java Edition", Available: false,
			Capabilities: GameCapability{Variants: []string{"vanilla", "fabric", "forge", "neoforge"}},
		},
		{
			ID: "terraria", Name: "Terraria", Available: false,
			Capabilities: GameCapability{Variants: []string{"vanilla", "tmodloader"}},
		},
		{
			ID: "runescape-dragonwilds", Name: "RuneScape: Dragonwilds", Available: true,
			Capabilities: GameCapability{
				Console: true, Configuration: true,
				Variants: []string{"dedicated"},
			},
		},
	}
}

func (a *App) handleGameCatalog(w http.ResponseWriter, r *http.Request) {
	jsonOut(w, 200, map[string]any{"games": supportedGameCatalog()})
}
