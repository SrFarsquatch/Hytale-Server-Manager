package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneSnapshotsKeepsNewestAndPreservesSafetyCopies(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	names := []string{
		"snapshot-older.tar.gz",
		"snapshot-middle.tar.gz",
		"snapshot-newest.tar.gz",
		"pre-restore-safety.tar.gz",
	}
	for i, name := range names {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("archive"), 0600); err != nil {
			t.Fatal(err)
		}
		stamp := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}

	pruneSnapshots(dir, 2)

	for _, name := range []string{"snapshot-newest.tar.gz", "snapshot-middle.tar.gz", "pre-restore-safety.tar.gz"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected %s to be retained: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "snapshot-older.tar.gz")); !os.IsNotExist(err) {
		t.Errorf("expected older snapshot to be pruned, stat error = %v", err)
	}
}

func TestPruneSnapshotsDoesNothingWhenRetentionIsDisabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snapshot-keep.tar.gz")
	if err := os.WriteFile(path, []byte("archive"), 0600); err != nil {
		t.Fatal(err)
	}
	pruneSnapshots(dir, 0)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("retention less than one should not delete snapshots: %v", err)
	}
}
