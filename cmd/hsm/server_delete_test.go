package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveServerDataPreservesManagerFilesForPrimary(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"game", "imports", "snapshots", "home", "backups", "servers/other", "tools"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name, "keep-or-remove.txt"), []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(root, "config.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := removeServerData(root, true); err != nil {
		t.Fatalf("removeServerData() error = %v", err)
	}

	for _, name := range []string{"game", "imports", "snapshots", "home", "backups"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Errorf("server-owned path %q still exists or stat failed: %v", name, err)
		}
	}
	for _, name := range []string{"config.json", "servers/other/keep-or-remove.txt", "tools/keep-or-remove.txt"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Errorf("manager/shared path %q was not preserved: %v", name, err)
		}
	}
}

func TestRemoveServerDataRemovesDedicatedServerRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "servers", "server-2")
	if err := os.MkdirAll(filepath.Join(root, "game"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := removeServerData(root, false); err != nil {
		t.Fatalf("removeServerData() error = %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("dedicated server root still exists or stat failed: %v", err)
	}
}
