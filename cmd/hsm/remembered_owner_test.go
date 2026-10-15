package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDragonwildsOwnerIDIsRememberedForNewServers(t *testing.T) {
	app := &App{dataDir: t.TempDir(), managers: map[string]*ServerManager{}}
	if code, out := createServer(t, app, `{"game":"runescape-dragonwilds","name":"Vale","dragonwildsOwnerId":"owner-1","dragonwildsAdminPassword":"admin"}`); code != 201 {
		t.Fatalf("first create = %d %v", code, out)
	}
	// The second server leaves the Owner ID out and gets the remembered one.
	if code, out := createServer(t, app, `{"game":"runescape-dragonwilds","name":"Second Vale","dragonwildsAdminPassword":"admin"}`); code != 201 {
		t.Fatalf("second create = %d %v", code, out)
	}
	if got := app.cfg.Servers[1].DragonwildsOwnerID; got != "owner-1" {
		t.Errorf("second server owner = %q, want owner-1", got)
	}

	rec := httptest.NewRecorder()
	app.handleGameCatalog(rec, httptest.NewRequest("GET", "/api/games", nil))
	var catalog struct {
		Defaults map[string]string `json:"defaults"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &catalog); err != nil || catalog.Defaults["dragonwildsOwnerId"] != "owner-1" {
		t.Errorf("catalog defaults = %v, %v", catalog.Defaults, err)
	}

	// A saved settings change becomes the new remembered value.
	app.sessions = map[string]Session{}
	if code, out := putSettings(t, app, app.cfg.Servers[0].ID, func(s map[string]any) { s["dragonwildsOwnerId"] = "owner-2" }); code != 200 {
		t.Fatalf("settings = %d %v", code, out)
	}
	if got := app.rememberedDragonwildsOwner(); got != "owner-2" {
		t.Errorf("remembered owner after settings save = %q", got)
	}

	// Other games are not given a Dragonwilds Owner ID.
	if code, _ := createServer(t, app, `{"game":"hytale","name":"Alpha"}`); code != 201 || app.cfg.Servers[2].DragonwildsOwnerID != "" {
		t.Errorf("Hytale server owner = %q", app.cfg.Servers[2].DragonwildsOwnerID)
	}
}

func TestLoadConfigKeepsEachGamesModeAndRemembersTheOwner(t *testing.T) {
	dir := t.TempDir()
	cfg := ControllerConfig{Servers: []ServerSettings{
		{ID: "mc", GameType: "minecraft-java", ServerName: "Blocks", Port: 25565, GameMode: "Survival", MaxPlayers: 20, MOTD: "Hi", StorageDir: filepath.Join("servers", "mc")},
		{ID: "tr", GameType: "terraria", ServerName: "Terra", Port: 5521, GameMode: "Expert", MaxPlayers: 8, MOTD: "Hi", StorageDir: filepath.Join("servers", "tr")},
		{ID: "dw", GameType: "runescape-dragonwilds", ServerName: "Vale", Port: 7777, GameMode: "Adventure", MaxPlayers: 50, StorageDir: filepath.Join("servers", "dw"), DragonwildsOwnerID: "owner-9"},
		{ID: "hy", GameType: "hytale", ServerName: "Alpha", Port: 5520, GameMode: "Survival", MaxPlayers: 20, MOTD: "Hi", StorageDir: filepath.Join("servers", "hy")},
	}}
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(dir, "controller.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	app := &App{dataDir: dir, managers: map[string]*ServerManager{}}
	if err := app.loadConfig(); err != nil {
		t.Fatal(err)
	}
	modes := map[string]string{}
	for _, s := range app.cfg.Servers {
		modes[s.ID] = s.GameMode
	}
	want := map[string]string{"mc": "Survival", "tr": "Expert", "dw": "Adventure", "hy": "Adventure"}
	for id, mode := range want {
		if modes[id] != mode {
			t.Errorf("%s game mode after load = %q, want %q", id, modes[id], mode)
		}
	}
	if dw := app.cfg.Servers[2]; dw.MaxPlayers != 6 || dw.MOTD == "" {
		t.Errorf("Dragonwilds repaired to max %d motd %q", dw.MaxPlayers, dw.MOTD)
	}
	if got := app.rememberedDragonwildsOwner(); got != "owner-9" {
		t.Errorf("remembered owner from an existing server = %q", got)
	}
}
