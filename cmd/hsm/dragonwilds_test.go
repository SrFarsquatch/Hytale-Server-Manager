package main

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPortRangeForGame(t *testing.T) {
	if min, max := portRangeForGame("runescape-dragonwilds"); min != 7777 || max != 7782 {
		t.Fatalf("Dragonwilds port range = %d-%d, want 7777-7782", min, max)
	}
	min, max, _ := deploymentPortRange()
	if gotMin, gotMax := portRangeForGame("hytale"); gotMin != min || gotMax != max {
		t.Fatalf("Hytale port range = %d-%d, want %d-%d", gotMin, gotMax, min, max)
	}
}

func TestWriteDragonwildsConfig(t *testing.T) {
	root := t.TempDir()
	settings := ServerSettings{
		GameType: "runescape-dragonwilds", ServerName: "Friends World", Port: 7777,
		DragonwildsOwnerID: "player-id-123", DragonwildsWorldName: "Misty Valley",
		DragonwildsAdminPassword: "admin-secret", DragonwildsWorldPassword: "world-secret",
		DragonwildsPublic: true,
	}
	if err := writeDragonwildsConfig(root, settings); err != nil { t.Fatalf("writeDragonwildsConfig() error = %v", err) }
	path := filepath.Join(root, "game", "RSDragonwilds", "Saved", "Config", "LinuxServer", "DedicatedServer.ini")
	data, err := os.ReadFile(path)
	if err != nil { t.Fatalf("read config: %v", err) }
	documentedPath := filepath.Join(root, "game", "RSDragonwilds", "Saved", "Config", "Linux", "DedicatedServer.ini")
	if _, err := os.Stat(documentedPath); err != nil { t.Fatalf("officially documented Linux config path missing: %v", err) }
	text := string(data)
	for _, expected := range []string{
		"[/script/dominion.dedicatedserversettings]",
		"OwnerId=player-id-123",
		"AdminPassword=admin-secret",
		"WorldPassword=world-secret",
		"ServerName=Friends World",
		"DefaultWorldName=Misty Valley",
		"Public=1",
	} {
		if !strings.Contains(text, expected) { t.Errorf("config missing %q:\n%s", expected, text) }
	}
	info, err := os.Stat(path)
	if err != nil { t.Fatal(err) }
	if info.Mode().Perm() != 0600 { t.Errorf("config permissions = %o, want 600", info.Mode().Perm()) }
}

func TestWriteDragonwildsConfigRejectsNewlineInjection(t *testing.T) {
	err := writeDragonwildsConfig(t.TempDir(), ServerSettings{
		ServerName: "Safe", DragonwildsOwnerID: "player\nPublic=1",
		DragonwildsAdminPassword: "password", DragonwildsWorldName: "World",
	})
	if err == nil { t.Fatal("expected newline injection to be rejected") }
}

func TestExtractSteamCMDArchiveRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "bad.tar.gz")
	out, err := os.Create(src)
	if err != nil { t.Fatal(err) }
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0644, Size: 1, Typeflag: tar.TypeReg}); err != nil { t.Fatal(err) }
	if _, err := tw.Write([]byte("x")); err != nil { t.Fatal(err) }
	if err := tw.Close(); err != nil { t.Fatal(err) }
	if err := gz.Close(); err != nil { t.Fatal(err) }
	if err := out.Close(); err != nil { t.Fatal(err) }
	if err := extractSteamCMDArchive(src, filepath.Join(root, "out")); err == nil { t.Fatal("expected traversal path to be rejected") }
}
