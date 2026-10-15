package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// Storage layout. Everything the app keeps lives under its data folder:
//
//	<data>/
//	  controller.json          app settings: servers, schedules, admin login
//	  tools/                   shared downloads (SteamCMD, Hytale downloader)
//	  servers/<id>/            one folder per server:
//	    game/                  the game's own files: install, worlds, configs, mods
//	    backups/snapshots/     snapshots made by the app
//	    backups/worlds/        Dragonwilds saves moved aside by the Worlds tab
//	    imports/               files waiting to be installed (Hytale game.zip)
//	    home/                  HOME for the game process and SteamCMD
//	    tmp/                   downloads and unpacking; emptied at startup
//
// Only game/ belongs to the game; the rest is the app's. Paths are built
// here and nowhere else, so the layout has one definition.

type serverPaths struct{ Root string }

func pathsFor(root string) serverPaths { return serverPaths{Root: root} }

func (p serverPaths) Game() string         { return filepath.Join(p.Root, "game") }
func (p serverPaths) Backups() string      { return filepath.Join(p.Root, "backups") }
func (p serverPaths) Snapshots() string    { return filepath.Join(p.Root, "backups", "snapshots") }
func (p serverPaths) WorldBackups() string { return filepath.Join(p.Root, "backups", "worlds") }
func (p serverPaths) Imports() string      { return filepath.Join(p.Root, "imports") }
func (p serverPaths) Home() string         { return filepath.Join(p.Root, "home") }
func (p serverPaths) Tmp() string          { return filepath.Join(p.Root, "tmp") }

// Ensure creates the server's folders.
func (p serverPaths) Ensure() error {
	for _, d := range []string{p.Root, p.Game(), p.Snapshots(), p.WorldBackups(), p.Imports(), p.Home(), p.Tmp()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) toolsDir() string   { return filepath.Join(a.dataDir, "tools") }
func (a *App) serversDir() string { return filepath.Join(a.dataDir, "servers") }

// serverEntries are the per-server folders, including names used before the
// current layout, as they appear in a legacy server kept in the data root.
var legacyRootEntries = []string{"game", "imports", "snapshots", "home", "backups", "world-backups"}

// migrateStorage brings existing installs to the current layout. It runs at
// startup, before any server is started, and is a no-op on current installs.
// It returns true when server settings changed and must be saved.
func migrateStorage(dataDir string, servers []ServerSettings) bool {
	changed := false
	for i := range servers {
		s := &servers[i]
		if s.StorageDir == "" || s.StorageDir == "." {
			if moveLegacyRootServer(dataDir, s) {
				changed = true
			}
		}
		root := filepath.Join(dataDir, filepath.Clean(s.StorageDir))
		if s.StorageDir == "" || s.StorageDir == "." {
			root = dataDir
		}
		migrateServerFolders(root)
	}
	cleanTemporaryFiles(dataDir, servers)
	return changed
}

// moveLegacyRootServer moves a server kept directly in the data folder (the
// layout of the first, single-server releases) into servers/<id>. Left in
// place, its file manager would reach the app's settings and other servers.
func moveLegacyRootServer(dataDir string, s *ServerSettings) bool {
	target := filepath.Join(dataDir, "servers", s.ID)
	if entries, err := os.ReadDir(target); err == nil && len(entries) > 0 {
		log.Printf("storage: %s is in the data folder but %s is not empty; leaving it in place", s.ID, target)
		return false
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		log.Printf("storage: move %s: %v", s.ID, err)
		return false
	}
	var moved []string
	for _, name := range legacyRootEntries {
		src := filepath.Join(dataDir, name)
		if _, err := os.Lstat(src); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := os.Rename(src, filepath.Join(target, name)); err != nil {
			log.Printf("storage: move %s/%s: %v; undoing", s.ID, name, err)
			for _, back := range moved {
				_ = os.Rename(filepath.Join(target, back), filepath.Join(dataDir, back))
			}
			return false
		}
		moved = append(moved, name)
	}
	s.StorageDir = filepath.Join("servers", s.ID)
	log.Printf("storage: moved server %s from the data folder to servers/%s", s.ID, s.ID)
	return true
}

// migrateServerFolders renames folders from earlier layouts inside a server:
// snapshots/ to backups/snapshots/ and world-backups/ to backups/worlds/.
func migrateServerFolders(root string) {
	p := pathsFor(root)
	for _, m := range []struct{ old, new string }{
		{filepath.Join(root, "snapshots"), p.Snapshots()},
		{filepath.Join(root, "world-backups"), p.WorldBackups()},
	} {
		if err := mergeFolder(m.old, m.new); err != nil {
			log.Printf("storage: %v", err)
		}
	}
}

// mergeFolder moves every entry of src into dst (created if needed) and
// removes src. An entry whose name already exists in dst is kept in src.
func mergeFolder(src, dst string) error {
	entries, err := os.ReadDir(src)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	kept := 0
	for _, e := range entries {
		to := filepath.Join(dst, e.Name())
		if _, err := os.Lstat(to); err == nil {
			kept++
			continue
		}
		if err := os.Rename(filepath.Join(src, e.Name()), to); err != nil {
			return fmt.Errorf("move %s: %w", filepath.Join(src, e.Name()), err)
		}
	}
	if kept == 0 {
		return os.Remove(src)
	}
	return nil
}

// cleanTemporaryFiles removes downloads and staging folders left by an
// interrupted install or import. Nothing is running at startup, so none of
// them can be in use.
func cleanTemporaryFiles(dataDir string, servers []ServerSettings) {
	remove := func(path string) {
		if _, err := os.Lstat(path); err == nil {
			if err := os.RemoveAll(path); err != nil {
				log.Printf("storage: clean %s: %v", path, err)
			}
		}
	}
	tools := filepath.Join(dataDir, "tools")
	for _, name := range []string{".steamcmd.tar.gz", ".hytale-downloader-upload", ".downloader-unpack"} {
		remove(filepath.Join(tools, name))
	}
	if entries, err := os.ReadDir(filepath.Join(dataDir, "servers")); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".importing") {
				remove(filepath.Join(dataDir, "servers", e.Name()))
			}
		}
	}
	for _, s := range servers {
		root := filepath.Join(dataDir, filepath.Clean(s.StorageDir))
		if s.StorageDir == "" || s.StorageDir == "." {
			root = dataDir
		}
		// Staging names used before tmp/ existed.
		for _, name := range []string{".import-tmp", ".server-download.zip", ".server-extract", ".terraria-download.zip", ".terraria-extract"} {
			remove(filepath.Join(root, name))
		}
		tmp := pathsFor(root).Tmp()
		if entries, err := os.ReadDir(tmp); err == nil {
			for _, e := range entries {
				remove(filepath.Join(tmp, e.Name()))
			}
		}
	}
}
