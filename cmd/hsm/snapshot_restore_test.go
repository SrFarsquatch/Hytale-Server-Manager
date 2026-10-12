package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceSnapshotEntriesReplacesIncludedData(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, "stage")
	dst := filepath.Join(root, "server")
	for _, p := range []string{
		filepath.Join(stage, "universe", "world.dat"),
		filepath.Join(dst, "universe", "stale.dat"),
		filepath.Join(dst, "mods", "old.jar"),
		filepath.Join(dst, "config.json"),
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := replaceSnapshotEntries(stage, dst, []string{"universe", "mods", "config.json"}); err != nil {
		t.Fatalf("replaceSnapshotEntries() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "universe", "world.dat")); err != nil {
		t.Fatalf("restored world file missing: %v", err)
	}
	for _, p := range []string{filepath.Join(dst, "universe", "stale.dat"), filepath.Join(dst, "mods"), filepath.Join(dst, "config.json")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Fatalf("stale included path %q remains (err=%v)", p, err)
		}
	}
}

func TestReplaceSnapshotEntriesLeavesUnlistedDataAlone(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, "stage")
	dst := filepath.Join(root, "server")
	if err := os.MkdirAll(filepath.Join(dst, "logs"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "logs", "server.log"), []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := replaceSnapshotEntries(stage, dst, []string{"universe", "mods"}); err != nil {
		t.Fatalf("replaceSnapshotEntries() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "logs", "server.log")); err != nil {
		t.Fatalf("unlisted data should remain: %v", err)
	}
}
