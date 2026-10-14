package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleServersCreateRejectsUnsupportedGamesWithoutSideEffects(t *testing.T) {
	for _, game := range []string{"terraria", "not-a-game"} {
		t.Run(game, func(t *testing.T) {
			root := t.TempDir()
			app := &App{dataDir: root, managers: map[string]*ServerManager{}}
			req := httptest.NewRequest("POST", "/api/servers", strings.NewReader(`{"game":"`+game+`","name":"Test server"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			app.handleServersCreate(rec, req)

			if rec.Code != 400 {
				t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
			}
			if len(app.cfg.Servers) != 0 {
				t.Fatalf("created %d server settings for unsupported game", len(app.cfg.Servers))
			}
			if len(app.managers) != 0 {
				t.Fatalf("created %d server managers for unsupported game", len(app.managers))
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("unsupported game left filesystem entries behind: %v", entries)
			}
		})
	}
}

func TestHandleServersCreateRequiresMinecraftEULAAcceptance(t *testing.T) {
	root := t.TempDir()
	app := &App{dataDir: root, managers: map[string]*ServerManager{}}
	req := httptest.NewRequest("POST", "/api/servers", strings.NewReader(`{"game":"minecraft-java","name":"Minecraft Test","minecraftVariant":"vanilla","minecraftVersion":"latest"} `))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	app.handleServersCreate(rec, req)

	if rec.Code != 400 || !strings.Contains(strings.ToLower(rec.Body.String()), "eula") {
		t.Fatalf("response = %d %s, want 400 requiring EULA acceptance", rec.Code, rec.Body.String())
	}
	assertServerCreateDidNotMutateState(t, app, root)
}

func TestHandleServersCreateAcceptsMinecraftVanillaAndPersistsConsent(t *testing.T) {
	root := t.TempDir()
	app := &App{dataDir: root, managers: map[string]*ServerManager{}}
	req := httptest.NewRequest("POST", "/api/servers", strings.NewReader(`{"game":"minecraft-java","name":"Minecraft Test","minecraftVariant":"vanilla","minecraftVersion":"latest","minecraftEulaAccepted":true,"gameMode":"Adventure","maxPlayers":20,"memoryMin":"2G","memoryMax":"4G","port":0}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	app.handleServersCreate(rec, req)

	if rec.Code != 201 {
		t.Fatalf("response = %d %s, want 201", rec.Code, rec.Body.String())
	}
	if len(app.cfg.Servers) != 1 {
		t.Fatalf("created %d server settings, want 1", len(app.cfg.Servers))
	}
	s := app.cfg.Servers[0]
	if s.GameType != "minecraft-java" || s.MinecraftVariant != "vanilla" || !s.MinecraftEulaAccepted {
		t.Fatalf("persisted Minecraft settings = %#v", s)
	}
	if _, err := os.Stat(filepath.Join(root, "controller.json")); err != nil {
		t.Fatalf("controller config was not persisted: %v", err)
	}
}

func TestHandleServersCreateRequiresDragonwildsOwnerID(t *testing.T) {
	root := t.TempDir()
	app := &App{dataDir: root, managers: map[string]*ServerManager{}}
	req := httptest.NewRequest("POST", "/api/servers", strings.NewReader(`{"game":"runescape-dragonwilds","name":"Dragon Test"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	app.handleServersCreate(rec, req)

	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "Owner ID") {
		t.Fatalf("response = %d %s, want 400 requiring Owner ID", rec.Code, rec.Body.String())
	}
	assertServerCreateDidNotMutateState(t, app, root)
}

func TestHandleServersCreateRequiresDragonwildsAdminPassword(t *testing.T) {
	root := t.TempDir()
	app := &App{dataDir: root, managers: map[string]*ServerManager{}}
	req := httptest.NewRequest("POST", "/api/servers", strings.NewReader(`{"game":"runescape-dragonwilds","name":"Dragon Test","dragonwildsOwnerId":"player-123","dragonwildsWorldName":"Dragon Test"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	app.handleServersCreate(rec, req)

	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "admin password") {
		t.Fatalf("response = %d %s, want 400 requiring admin password", rec.Code, rec.Body.String())
	}
	assertServerCreateDidNotMutateState(t, app, root)
}

func assertServerCreateDidNotMutateState(t *testing.T, app *App, root string) {
	t.Helper()
	if len(app.cfg.Servers) != 0 {
		t.Fatalf("invalid request created %d server settings", len(app.cfg.Servers))
	}
	if len(app.managers) != 0 {
		t.Fatalf("invalid request created %d server managers", len(app.managers))
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("invalid request left filesystem entries behind: %v", entries)
	}
}
