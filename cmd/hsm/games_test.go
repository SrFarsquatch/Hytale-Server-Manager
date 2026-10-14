package main

import "testing"

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
	if !available["runescape-dragonwilds"] { t.Fatal("Dragonwilds should be available after its dedicated-server adapter was added") }
	if !available["minecraft-java"] {
		t.Error("Minecraft Java vanilla should be available after its installer and lifecycle were implemented")
	}
	if available["terraria"] {
		t.Error("Terraria should not be available before its installer and lifecycle are implemented")
	}
	if _, ok := available["terraria"]; !ok {
		t.Error("catalog is missing planned game terraria")
	}
}

func TestGameCatalogCapabilitiesDoNotOverstatePlannedGameSupport(t *testing.T) {
	for _, game := range supportedGameCatalog() {
		if game.ID == "hytale" {
			if !game.Capabilities.Console || !game.Capabilities.Backups || !game.Capabilities.FileManagement {
				t.Fatalf("Hytale capabilities unexpectedly missing: %#v", game.Capabilities)
			}
			continue
		}
		if game.ID == "runescape-dragonwilds" {
			if game.Capabilities.Backups || game.Capabilities.FileManagement || game.Capabilities.Mods || game.Capabilities.PlayerList { t.Errorf("Dragonwilds exposes unimplemented capabilities: %#v", game.Capabilities) }
			continue
		}
		if game.ID == "minecraft-java" {
			if !game.Capabilities.Console || !game.Capabilities.FileManagement || !game.Capabilities.Configuration || game.Capabilities.Mods || game.Capabilities.Backups || game.Capabilities.PlayerList {
				t.Errorf("Minecraft Java vanilla capabilities unexpected: %#v", game.Capabilities)
			}
			if len(game.Capabilities.Variants) != 1 || game.Capabilities.Variants[0] != "vanilla" { t.Errorf("Minecraft variants = %#v, want vanilla only", game.Capabilities.Variants) }
			continue
		}
		if game.Capabilities.Console || game.Capabilities.Backups || game.Capabilities.FileManagement || game.Capabilities.Mods || game.Capabilities.PlayerList {
			t.Errorf("planned game %q exposes unimplemented capabilities: %#v", game.ID, game.Capabilities)
		}
	}
}
