package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMinecraftAdapterRequiresJarAndAcceptedEULA(t *testing.T) {
	root := t.TempDir()
	gameDir := filepath.Join(root, "game")
	if err := os.MkdirAll(gameDir, 0755); err != nil { t.Fatal(err) }
	if err := os.WriteFile(filepath.Join(gameDir, "server.jar"), []byte("jar"), 0644); err != nil { t.Fatal(err) }
	if (MinecraftAdapter{}).Installed(root) { t.Fatal("server with no EULA file must not be considered installed") }
	if err := os.WriteFile(filepath.Join(gameDir, "eula.txt"), []byte("eula=false\n"), 0600); err != nil { t.Fatal(err) }
	if (MinecraftAdapter{}).Installed(root) { t.Fatal("server with eula=false must not be considered installed") }
	if err := os.WriteFile(filepath.Join(gameDir, "eula.txt"), []byte("eula=true\n"), 0600); err != nil { t.Fatal(err) }
	if !(MinecraftAdapter{}).Installed(root) { t.Fatal("server with jar and eula=true should be installed") }
}

func TestMinecraftAdapterBuildProcessUsesJavaAndInstanceDirectory(t *testing.T) {
	root := t.TempDir()
	gameDir := filepath.Join(root, "game")
	if err := os.MkdirAll(gameDir, 0755); err != nil { t.Fatal(err) }
	if err := os.WriteFile(filepath.Join(gameDir, "server.jar"), []byte("jar"), 0644); err != nil { t.Fatal(err) }
	spec, err := (MinecraftAdapter{}).BuildProcess(root, []string{"-Xms2G", "-Xmx4G", "-jar", "server.jar", "nogui"})
	if err != nil { t.Fatal(err) }
	if spec.Path != "java" || spec.Dir != gameDir { t.Fatalf("unexpected process spec: %#v", spec) }
	if !reflect.DeepEqual(spec.Args, []string{"-Xms2G", "-Xmx4G", "-jar", "server.jar", "nogui"}) { t.Fatalf("args = %#v", spec.Args) }
}

func TestWriteMinecraftPropertiesRequiresEULAAcceptance(t *testing.T) {
	root := t.TempDir()
	s := ServerSettings{Port: 5521, MaxPlayers: 12, ServerName: "Example", MOTD: "Hello", GameMode: "Creative"}
	if err := writeMinecraftProperties(root, s); err == nil || !strings.Contains(err.Error(), "EULA") {
		t.Fatalf("writeMinecraftProperties error = %v, want EULA acceptance error", err)
	}
	s.MinecraftEulaAccepted = true
	if err := writeMinecraftProperties(root, s); err != nil { t.Fatal(err) }
	props, err := os.ReadFile(filepath.Join(root, "game", "server.properties"))
	if err != nil { t.Fatal(err) }
	for _, expected := range []string{"server-port=5521", "max-players=12", "gamemode=creative", "motd=Hello", "white-list=false"} {
		if !strings.Contains(string(props), expected) { t.Errorf("server.properties missing %q: %s", expected, props) }
	}
	eula, err := os.ReadFile(filepath.Join(root, "game", "eula.txt"))
	if err != nil { t.Fatal(err) }
	if !strings.Contains(string(eula), "eula=true") { t.Fatalf("eula.txt = %q, want accepted EULA", eula) }
}

func TestMinecraftPropertiesPreserveGameManagedAndCustomSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.properties")
	initial := "motd=old\nserver-port=25565\nview-distance=12\nonline-mode=false\n"
	if err := os.WriteFile(path, []byte(initial), 0644); err != nil { t.Fatal(err) }
	values := []struct{ key, value string }{{"motd", "new"}, {"server-port", "5520"}, {"online-mode", "true"}}
	if err := mergeMinecraftProperties(path, values); err != nil { t.Fatal(err) }
	got, err := os.ReadFile(path)
	if err != nil { t.Fatal(err) }
	for _, expected := range []string{"motd=new", "server-port=5520", "online-mode=true", "view-distance=12"} {
		if !strings.Contains(string(got), expected) { t.Errorf("merged properties missing %q: %s", expected, got) }
	}
	if strings.Contains(string(got), "motd=old") || strings.Contains(string(got), "online-mode=false") {
		t.Fatalf("managed property was not replaced: %s", got)
	}
}

func TestMojangURLValidationRejectsNonOfficialOrInsecureURLs(t *testing.T) {
	for _, raw := range []string{"http://piston-data.mojang.com/server.jar", "https://example.com/server.jar", "https://mojang.com.evil.test/server.jar", "https://user@piston-data.mojang.com/server.jar"} {
		if isOfficialMojangURL(raw) { t.Errorf("isOfficialMojangURL(%q) = true, want false", raw) }
	}
	for _, raw := range []string{"https://piston-meta.mojang.com/manifest.json", "https://piston-data.mojang.com/server.jar"} {
		if !isOfficialMojangURL(raw) { t.Errorf("isOfficialMojangURL(%q) = false, want true", raw) }
	}
}

func TestMinecraftInstallerRejectsOtherGameManager(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), "hytale")
	if err := m.RunMinecraftInstaller("latest"); err == nil || !strings.Contains(err.Error(), "Minecraft Java") {
		t.Fatalf("RunMinecraftInstaller error = %v, want wrong-game rejection", err)
	}
}
