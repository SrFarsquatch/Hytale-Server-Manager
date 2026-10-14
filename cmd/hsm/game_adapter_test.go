package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGameAdapterForDefaultsLegacyTypeToHytale(t *testing.T) {
	adapter, err := gameAdapterFor("")
	if err != nil {
		t.Fatalf("gameAdapterFor(empty) error = %v", err)
	}
	if adapter.GameType() != "hytale" {
		t.Fatalf("adapter type = %q, want hytale", adapter.GameType())
	}
}

func TestGameAdapterForResolvesHytaleCaseInsensitively(t *testing.T) {
	adapter, err := gameAdapterFor(" Hytale ")
	if err != nil {
		t.Fatalf("gameAdapterFor(Hytale) error = %v", err)
	}
	if adapter.GameType() != "hytale" {
		t.Fatalf("adapter type = %q, want hytale", adapter.GameType())
	}
}

func TestGameAdapterForRejectsPlannedGamesUntilImplemented(t *testing.T) {
	for _, gameType := range []string{"terraria"} {
		t.Run(gameType, func(t *testing.T) {
			adapter, err := gameAdapterFor(gameType)
			if err == nil {
				t.Fatalf("gameAdapterFor(%q) error = nil, want unsupported error", gameType)
			}
			if adapter != nil {
				t.Fatalf("gameAdapterFor(%q) returned an adapter before support exists", gameType)
			}
		})
	}
}

func TestGameAdapterForResolvesMinecraftJava(t *testing.T) {
	adapter, err := gameAdapterFor(" Minecraft-Java ")
	if err != nil { t.Fatalf("gameAdapterFor(Minecraft Java) error = %v", err) }
	if adapter.GameType() != "minecraft-java" { t.Fatalf("adapter type = %q, want minecraft-java", adapter.GameType()) }
}

func TestGameAdapterForResolvesDragonwilds(t *testing.T) {
	adapter, err := gameAdapterFor(" RuneScape-Dragonwilds ")
	if err != nil { t.Fatalf("gameAdapterFor(Dragonwilds) error = %v", err) }
	if adapter.GameType() != "runescape-dragonwilds" { t.Fatalf("adapter type = %q, want runescape-dragonwilds", adapter.GameType()) }
}

func TestGameAdapterForRejectsUnknownGame(t *testing.T) {
	if adapter, err := gameAdapterFor("not-a-game"); err == nil || adapter != nil {
		t.Fatalf("gameAdapterFor(unknown) = (%v, %v), want (nil, error)", adapter, err)
	}
}

func TestHytaleAdapterBuildProcessUsesSharedSupervisorEntry(t *testing.T) {
	root := t.TempDir()
	gameDir := filepath.Join(root, "game")
	if err := os.MkdirAll(gameDir, 0755); err != nil { t.Fatal(err) }
	startScript := filepath.Join(gameDir, "start.sh")
	if err := os.WriteFile(startScript, []byte("#!/bin/sh\n"), 0644); err != nil { t.Fatal(err) }
	serverDir := filepath.Join(gameDir, "Server")
	if err := os.MkdirAll(serverDir, 0755); err != nil { t.Fatal(err) }
	for _, name := range []string{"HytaleServer.jar", "Assets.zip"} {
		if err := os.WriteFile(filepath.Join(gameDir, name), []byte("test"), 0644); err != nil { t.Fatal(err) }
	}
	if err := os.WriteFile(filepath.Join(serverDir, "HytaleServer.jar"), []byte("test"), 0644); err != nil { t.Fatal(err) }

	spec, err := (HytaleAdapter{}).BuildProcess(root, []string{"--bind", "0.0.0.0:5520"})
	if err != nil { t.Fatalf("BuildProcess() error = %v", err) }
	if spec.Path != "bash" { t.Fatalf("Path = %q, want bash", spec.Path) }
	if spec.Dir != gameDir { t.Fatalf("Dir = %q, want %q", spec.Dir, gameDir) }
	if !reflect.DeepEqual(spec.Args, []string{startScript, "--bind", "0.0.0.0:5520"}) {
		t.Fatalf("Args = %#v", spec.Args)
	}
	if ! (HytaleAdapter{}).Installed(root) { t.Fatal("Installed() = false after adding required Hytale files") }
}

func TestDragonwildsAdapterBuildProcessPrefersLauncherScript(t *testing.T) {
	root := t.TempDir()
	gameDir := filepath.Join(root, "game")
	if err := os.MkdirAll(gameDir, 0755); err != nil { t.Fatal(err) }
	launcher := filepath.Join(gameDir, "RSDragonwildsServer.sh")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\n"), 0644); err != nil { t.Fatal(err) }

	spec, err := (DragonwildsAdapter{}).BuildProcess(root, []string{"-log"})
	if err != nil { t.Fatalf("BuildProcess() error = %v", err) }
	if spec.Path != launcher { t.Fatalf("Path = %q, want %q", spec.Path, launcher) }
	if spec.Dir != gameDir { t.Fatalf("Dir = %q, want %q", spec.Dir, gameDir) }
	if !reflect.DeepEqual(spec.Args, []string{"-log"}) { t.Fatalf("Args = %#v", spec.Args) }
	if !(DragonwildsAdapter{}).Installed(root) { t.Fatal("Installed() = false after adding launcher") }
}

func TestAdaptersRejectMissingLaunchFiles(t *testing.T) {
	root := t.TempDir()
	if _, err := (HytaleAdapter{}).BuildProcess(root, nil); err == nil {
		t.Fatal("Hytale BuildProcess() error = nil, want missing start script error")
	}
	if _, err := (DragonwildsAdapter{}).BuildProcess(root, nil); err == nil {
		t.Fatal("Dragonwilds BuildProcess() error = nil, want missing launcher error")
	}
	if (HytaleAdapter{}).Installed(root) || (DragonwildsAdapter{}).Installed(root) {
		t.Fatal("new empty root unexpectedly reports an installed server")
	}
}
