package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigFreshInstallStartsWithoutServers(t *testing.T) {
	dir := t.TempDir()
	a := &App{dataDir: dir}
	if err := a.loadConfig(); err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if len(a.cfg.Servers) != 0 {
		t.Fatalf("fresh install created %d servers, want none", len(a.cfg.Servers))
	}
	b, err := os.ReadFile(filepath.Join(dir, "controller.json"))
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	var saved ControllerConfig
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatalf("decode saved config: %v", err)
	}
	if len(saved.Servers) != 0 {
		t.Fatalf("saved config contains %d servers, want none", len(saved.Servers))
	}
}

func TestLoadConfigPreservesExistingServers(t *testing.T) {
	dir := t.TempDir()
	cfg := ControllerConfig{Servers: []ServerSettings{
		defaultServerSettings("friends", "Friends World", 5521, "servers/friends"),
	}}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "controller.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	a := &App{dataDir: dir}
	if err := a.loadConfig(); err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if len(a.cfg.Servers) != 1 || a.cfg.Servers[0].ID != "friends" || a.cfg.Servers[0].ServerName != "Friends World" {
		t.Fatalf("existing server was not preserved: %#v", a.cfg.Servers)
	}
}

func TestLoadConfigPreservesExistingGameSettings(t *testing.T) {
	dir := t.TempDir()
	controller := []byte(`{"servers":[{"id":"main","serverName":"Old World","port":5520,"memoryMin":"2G","memoryMax":"4G","backupFrequency":30,"storageDir":"."}]}`)
	if err := os.WriteFile(filepath.Join(dir, "controller.json"), controller, 0600); err != nil {
		t.Fatal(err)
	}
	gameDir := filepath.Join(dir, "game", "Server")
	if err := os.MkdirAll(gameDir, 0755); err != nil {
		t.Fatal(err)
	}
	gameConfig := []byte(`{"Version":4,"MOTD":"Keep my world","Password":"keep-secret","MaxPlayers":64,"RequireJoinPermission":true,"Defaults":{"World":"default","GameMode":"Creative"}}`)
	if err := os.WriteFile(filepath.Join(gameDir, "config.json"), gameConfig, 0644); err != nil {
		t.Fatal(err)
	}
	a := &App{dataDir: dir}
	if err := a.loadConfig(); err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	got := a.cfg.Servers[0]
	if got.GameMode != "Creative" || got.MaxPlayers != 64 || got.MOTD != "Keep my world" || got.Password != "keep-secret" || !got.WhitelistEnabled {
		t.Fatalf("existing game settings were not migrated: %#v", got)
	}
}

func TestLoadConfigMigratesLegacySettings(t *testing.T) {
	dir := t.TempDir()
	cfg := ControllerConfig{Settings: &legacySettings{ServerName: "Old World", Port: 5522, MemoryMin: "3G", MemoryMax: "5G"}}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "controller.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	a := &App{dataDir: dir}
	if err := a.loadConfig(); err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if len(a.cfg.Servers) != 1 {
		t.Fatalf("legacy config migrated to %d servers, want one", len(a.cfg.Servers))
	}
	if got := a.cfg.Servers[0]; got.ServerName != "Old World" || got.Port != 5522 || got.MemoryMin != "3G" || got.MemoryMax != "5G" {
		t.Fatalf("legacy settings not preserved: %#v", got)
	}
	if a.cfg.Settings != nil {
		t.Fatal("legacy settings were not cleared after migration")
	}
}

func TestApplyHytaleConfigUpdatesWizardSettings(t *testing.T) {
	dir := t.TempDir()
	server := defaultServerSettings("friends", "Friends World", 5521, "servers/friends")
	server.GameMode = "Creative"
	server.MaxPlayers = 42
	server.MOTD = "Build together"
	server.Password = "secret"
	server.WhitelistEnabled = true
	a := &App{dataDir: dir}
	configDir := filepath.Join(a.serverRoot(server), "game", "Server")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configDir, "config.json")
	initial := []byte(`{"Version":4,"ServerName":"Old","Defaults":{"World":"default","GameMode":"Adventure"},"Unrelated":true}`)
	if err := os.WriteFile(path, initial, 0644); err != nil {
		t.Fatal(err)
	}
	if err := a.applyHytaleConfig(server); err != nil {
		t.Fatalf("applyHytaleConfig() error = %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["ServerName"] != "Friends World" || got["MOTD"] != "Build together" || got["Password"] != "secret" || got["MaxPlayers"] != float64(42) || got["Port"] != float64(5521) || got["RequireJoinPermission"] != true || got["Unrelated"] != true {
		t.Fatalf("global config not applied or unrelated field lost: %#v", got)
	}
	defaults, ok := got["Defaults"].(map[string]any)
	if !ok || defaults["GameMode"] != "Creative" || defaults["World"] != "default" {
		t.Fatalf("world defaults not correctly updated: %#v", got["Defaults"])
	}
}

func TestApplyHytaleConfigCreatesConfigForInstalledServer(t *testing.T) {
	dir := t.TempDir()
	server := defaultServerSettings("first", "First World", 5520, "servers/first")
	server.GameMode = "Creative"
	server.MaxPlayers = 32
	a := &App{dataDir: dir}
	serverDir := filepath.Join(a.serverRoot(server), "game", "Server")
	if err := os.MkdirAll(serverDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serverDir, "HytaleServer.jar"), []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := a.applyHytaleConfig(server); err != nil {
		t.Fatalf("applyHytaleConfig() error = %v", err)
	}
	b, err := os.ReadFile(filepath.Join(serverDir, "config.json"))
	if err != nil {
		t.Fatalf("first-start config was not created: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	defaults := got["Defaults"].(map[string]any)
	if got["ServerName"] != "First World" || got["Port"] != float64(5520) || got["MaxPlayers"] != float64(32) || defaults["GameMode"] != "Creative" {
		t.Fatalf("wizard settings missing from generated config: %#v", got)
	}
}

func TestApplyHytaleConfigAllowsFirstInstallWithoutConfig(t *testing.T) {
	a := &App{dataDir: t.TempDir()}
	server := defaultServerSettings("first", "First World", 5520, "servers/first")
	if err := a.applyHytaleConfig(server); err != nil {
		t.Fatalf("missing config should be allowed before game files are installed: %v", err)
	}
}
