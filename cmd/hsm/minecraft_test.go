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

func TestMinecraftInstallerRejectsOtherGameManager(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), "hytale")
	if err := m.RunMinecraftInstaller("latest"); err == nil || !strings.Contains(err.Error(), "Minecraft Java") {
		t.Fatalf("RunMinecraftInstaller error = %v, want wrong-game rejection", err)
	}
}
