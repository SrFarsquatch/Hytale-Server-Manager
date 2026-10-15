package main

import (
	"archive/tar"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStartupMovesALegacyRootServerAndRenamesOldFolders(t *testing.T) {
	data := t.TempDir()
	cfg := ControllerConfig{Servers: []ServerSettings{
		{ID: "main", GameType: "hytale", ServerName: "Main", Port: 5520, StorageDir: "."},
		{ID: "other", GameType: "runescape-dragonwilds", ServerName: "Other", Port: 7777, StorageDir: filepath.Join("servers", "other")},
	}}
	b, _ := json.Marshal(cfg)
	writeFile(t, filepath.Join(data, "controller.json"), string(b))
	writeFile(t, filepath.Join(data, "game", "Server", "universe", "chunk.bin"), "world")
	writeFile(t, filepath.Join(data, "snapshots", "snapshot-1.tar.gz"), "snap")
	writeFile(t, filepath.Join(data, "imports", "game.zip"), "zip")
	writeFile(t, filepath.Join(data, "home", ".config", "x"), "home")
	writeFile(t, filepath.Join(data, "tools", "steamcmd", "steamcmd.sh"), "tool")
	other := filepath.Join(data, "servers", "other")
	writeFile(t, filepath.Join(other, "snapshots", "snapshot-2.tar.gz"), "snap2")
	writeFile(t, filepath.Join(other, "world-backups", "20261015-000000.000", "Vale.sav"), "vale")

	app := &App{dataDir: data, managers: map[string]*ServerManager{}}
	if err := app.loadConfig(); err != nil {
		t.Fatal(err)
	}
	if got := app.cfg.Servers[0].StorageDir; got != filepath.Join("servers", "main") {
		t.Fatalf("main storage = %q", got)
	}
	main := pathsFor(filepath.Join(data, "servers", "main"))
	for path, want := range map[string]string{
		filepath.Join(main.Game(), "Server", "universe", "chunk.bin"):                    "world",
		filepath.Join(main.Snapshots(), "snapshot-1.tar.gz"):                             "snap",
		filepath.Join(main.Imports(), "game.zip"):                                        "zip",
		filepath.Join(main.Home(), ".config", "x"):                                       "home",
		filepath.Join(pathsFor(other).Snapshots(), "snapshot-2.tar.gz"):                  "snap2",
		filepath.Join(pathsFor(other).WorldBackups(), "20261015-000000.000", "Vale.sav"): "vale",
		filepath.Join(data, "tools", "steamcmd", "steamcmd.sh"):                          "tool",
	} {
		if b, err := os.ReadFile(path); err != nil || string(b) != want {
			t.Errorf("%s = %q, %v; want %q", path, b, err, want)
		}
	}
	for _, gone := range []string{filepath.Join(data, "game"), filepath.Join(data, "snapshots"), filepath.Join(main.Root, "snapshots"), filepath.Join(other, "snapshots"), filepath.Join(other, "world-backups")} {
		if _, err := os.Stat(gone); err == nil {
			t.Errorf("%s still exists", gone)
		}
	}
	if !fileExists(filepath.Join(data, "controller.json")) {
		t.Error("the app settings were moved")
	}

	// Saved and loaded again: nothing moves a second time.
	app2 := &App{dataDir: data, managers: map[string]*ServerManager{}}
	if err := app2.loadConfig(); err != nil || app2.cfg.Servers[0].StorageDir != filepath.Join("servers", "main") {
		t.Fatalf("second load: %v %q", err, app2.cfg.Servers[0].StorageDir)
	}
}

func TestLegacyRootServerStaysWhenItsTargetIsTaken(t *testing.T) {
	data := t.TempDir()
	writeFile(t, filepath.Join(data, "game", "keep.txt"), "x")
	writeFile(t, filepath.Join(data, "servers", "main", "something"), "y")
	servers := []ServerSettings{{ID: "main", StorageDir: "."}}
	if migrateStorage(data, servers) || servers[0].StorageDir != "." || !fileExists(filepath.Join(data, "game", "keep.txt")) {
		t.Errorf("server was moved onto an existing folder: %+v", servers[0])
	}
}

func TestMergeFolderKeepsConflictingEntries(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "old", "a"), "old-a")
	writeFile(t, filepath.Join(root, "old", "b"), "old-b")
	writeFile(t, filepath.Join(root, "new", "a"), "new-a")
	if err := mergeFolder(filepath.Join(root, "old"), filepath.Join(root, "new")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "new", "a")); string(b) != "new-a" {
		t.Errorf("existing entry overwritten: %q", b)
	}
	if !fileExists(filepath.Join(root, "new", "b")) || !fileExists(filepath.Join(root, "old", "a")) {
		t.Error("b was not moved, or the conflicting a was not kept in the old folder")
	}
}

func TestStartupRemovesInterruptedDownloadsAndStaging(t *testing.T) {
	data := t.TempDir()
	root := filepath.Join(data, "servers", "s1")
	leftovers := []string{
		filepath.Join(root, ".import-tmp", "x"),
		filepath.Join(root, ".server-download.zip"),
		filepath.Join(root, "tmp", "server-extract", "y"),
		filepath.Join(data, "servers", "s2.importing", "game", "z"),
		filepath.Join(data, "tools", ".steamcmd.tar.gz"),
	}
	for _, p := range leftovers {
		writeFile(t, p, "x")
	}
	writeFile(t, filepath.Join(root, "game", "keep"), "keep")
	migrateStorage(data, []ServerSettings{{ID: "s1", StorageDir: filepath.Join("servers", "s1")}})
	for _, p := range leftovers {
		if fileExists(p) {
			t.Errorf("%s was not cleaned", p)
		}
	}
	if !fileExists(filepath.Join(root, "game", "keep")) || !fileExists(filepath.Join(root, "tmp")) {
		t.Error("server files or the tmp folder itself were removed")
	}
}

func TestImportMapsOldExportFoldersToTheCurrentLayout(t *testing.T) {
	archive := craftedArchive(t, "hytale",
		&tar.Header{Name: "files/snapshots/snapshot-1.tar.gz", Mode: 0o644, Size: 3, Typeflag: tar.TypeReg},
		&tar.Header{Name: "files/world-backups/a/Vale.sav", Mode: 0o644, Size: 3, Typeflag: tar.TypeReg},
	)
	app := &App{dataDir: t.TempDir(), managers: map[string]*ServerManager{}}
	if code, out := importServer(t, newTestMux(app), archive); code != 201 {
		t.Fatalf("import = %d %v", code, out)
	}
	p := pathsFor(app.serverRoot(app.cfg.Servers[0]))
	if !fileExists(filepath.Join(p.Snapshots(), "snapshot-1.tar.gz")) || !fileExists(filepath.Join(p.WorldBackups(), "a", "Vale.sav")) {
		t.Error("old export folders did not land in backups/")
	}
}
