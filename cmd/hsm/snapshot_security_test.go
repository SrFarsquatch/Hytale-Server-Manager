package main

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func writeTestSnapshot(t *testing.T, path, name string, kind byte, body string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil { t.Fatal(err) }
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	h := &tar.Header{Name: name, Typeflag: kind, Mode: 0644, Size: int64(len(body))}
	if kind == tar.TypeSymlink { h.Linkname = body; h.Size = 0 }
	if err := tw.WriteHeader(h); err != nil { t.Fatal(err) }
	if kind == tar.TypeReg {
		if _, err := tw.Write([]byte(body)); err != nil { t.Fatal(err) }
	}
	if err := tw.Close(); err != nil { t.Fatal(err) }
	if err := gz.Close(); err != nil { t.Fatal(err) }
	if err := f.Close(); err != nil { t.Fatal(err) }
}

func TestExtractTarGzRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(root, "bad.tar.gz")
	outside := filepath.Join(root, "outside.txt")
	writeTestSnapshot(t, archive, "../outside.txt", tar.TypeReg, "should not escape")
	if err := extractTarGz(archive, filepath.Join(root, "server")); err == nil {
		t.Fatal("expected traversal archive to be rejected")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("archive wrote outside target directory: stat err=%v", err)
	}
}

func TestExtractTarGzRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(root, "link.tar.gz")
	writeTestSnapshot(t, archive, "link", tar.TypeSymlink, "../../outside")
	if err := extractTarGz(archive, filepath.Join(root, "server")); err == nil {
		t.Fatal("expected symlink archive entry to be rejected")
	}
}
