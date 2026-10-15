package main

import (
	"strings"
	"testing"
)

func TestSupportedGameCatalogListsGamesAndEnablesImplementedAdapters(t *testing.T) {
	games := supportedGameCatalog()
	if len(games) != 4 {
		t.Fatalf("catalog contains %d games, want 4", len(games))
	}

	available := map[string]bool{}
	for _, game := range games {
		if game.ID == "" || game.Name == "" {
			t.Fatalf("catalog entry missing identity: %#v", game)
		}
		available[game.ID] = game.Available
	}

	if !available["hytale"] {
		t.Fatal("Hytale should remain available")
	}
	if !available["runescape-dragonwilds"] {
		t.Fatal("Dragonwilds should be available after its dedicated-server adapter was added")
	}
	if !available["minecraft-java"] {
		t.Error("Minecraft Java vanilla should be available after its installer and lifecycle were implemented")
	}
	if !available["terraria"] {
		t.Error("Terraria vanilla should be available after its installer and lifecycle were implemented")
	}
}

func TestGameCatalogCapabilitiesDoNotOverstatePlannedGameSupport(t *testing.T) {
	for _, game := range supportedGameCatalog() {
		if game.ID == "hytale" {
			if !game.Capabilities.Console || !game.Capabilities.Backups || !game.Capabilities.NativeBackups || !game.Capabilities.FileManagement {
				t.Fatalf("Hytale capabilities unexpectedly missing: %#v", game.Capabilities)
			}
			continue
		}
		if game.ID == "runescape-dragonwilds" {
			// The file manager is game-neutral and was already exposed for Dragonwilds.
			// Dragonwilds is stopped with SIGINT and has no stdin command channel.
			// Snapshots and world management work while the server is stopped.
			if game.Capabilities.LiveSnapshots || game.Capabilities.NativeBackups || game.Capabilities.Mods || game.Capabilities.PlayerList || game.Capabilities.ConsoleCommands || game.Capabilities.DeviceAuth || game.Capabilities.GameUpdates || len(game.Capabilities.ConfigFiles) != 0 {
				t.Errorf("Dragonwilds exposes unimplemented capabilities: %#v", game.Capabilities)
			}
			if !game.Capabilities.Backups || !game.Capabilities.Worlds {
				t.Errorf("Dragonwilds is missing snapshots or world management: %#v", game.Capabilities)
			}
			continue
		}
		if game.ID == "minecraft-java" {
			// Vanilla Minecraft: snapshots (including live ones), player list and
			// server.properties; no mods, Hytale-native backups, auth or updater.
			c := game.Capabilities
			if !c.Console || !c.FileManagement || !c.Configuration || !c.Backups || !c.LiveSnapshots || !c.PlayerList || !c.ServerProperties ||
				c.Mods || c.NativeBackups || c.DeviceAuth || c.GameUpdates {
				t.Errorf("Minecraft Java vanilla capabilities unexpected: %#v", game.Capabilities)
			}
			if strings.Join(game.Capabilities.Variants, ",") != "vanilla,paper,fabric" {
				t.Errorf("Minecraft variants = %#v, want vanilla, paper and fabric", game.Capabilities.Variants)
			}
			continue
		}
		if game.ID == "terraria" {
			// Vanilla Terraria: snapshots while stopped, player list, commands.
			c := game.Capabilities
			if !c.Console || !c.ConsoleCommands || !c.FileManagement || !c.Configuration || !c.Backups || !c.PlayerList ||
				c.LiveSnapshots || c.Mods || c.NativeBackups || c.ServerProperties || c.DeviceAuth || c.GameUpdates || len(c.ConfigFiles) != 0 {
				t.Errorf("Terraria vanilla capabilities unexpected: %#v", c)
			}
			if strings.Join(c.Variants, ",") != "vanilla,tmodloader" {
				t.Errorf("Terraria variants = %#v, want vanilla and tmodloader", c.Variants)
			}
			continue
		}
		if game.Capabilities.Console || game.Capabilities.Backups || game.Capabilities.FileManagement || game.Capabilities.Mods || game.Capabilities.PlayerList {
			t.Errorf("planned game %q exposes unimplemented capabilities: %#v", game.ID, game.Capabilities)
		}
	}
}
