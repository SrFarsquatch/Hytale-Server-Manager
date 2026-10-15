package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// exportServer downloads an export of server id from app.
func exportServer(t *testing.T, mux http.Handler, id, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/servers/"+id+"/export"+query, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func importServer(t *testing.T, mux http.Handler, archive []byte) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/servers/import", bytes.NewReader(archive))
	req.Header.Set("Content-Type", "application/gzip")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// hytaleExportSource is an installed Hytale server with a world, a native
// backup, a snapshot, a password and a schedule.
func hytaleExportSource(t *testing.T) (*App, http.Handler, ServerSettings) {
	t.Helper()
	s := defaultServerSettings("alpha", "Alpha", 5520, filepath.Join("servers", "alpha"))
	s.Password = "join-secret"
	app := newCapabilityTestApp(t, s)
	app.cfg.Schedules = map[string][]Schedule{"alpha": {{ID: "nightly", Name: "Nightly", Action: "restart", Kind: "daily", Time: "04:00", Enabled: true}}}
	root := app.serverRoot(s)
	writeFile(t, filepath.Join(root, "game", "Server", "HytaleServer.jar"), "jar")
	writeFile(t, filepath.Join(root, "game", "Assets.zip"), "assets")
	writeFile(t, filepath.Join(root, "game", "Server", "universe", "worlds", "default", "chunk.bin"), "world-data")
	writeFile(t, filepath.Join(root, "game", "Server", "backups", "auto.zip"), "native-backup")
	writeFile(t, filepath.Join(pathsFor(root).Snapshots(), "snapshot-1.tar.gz"), "snap")
	if err := os.Symlink("universe", filepath.Join(root, "game", "Server", "world-link")); err != nil {
		t.Fatal(err)
	}
	return app, newTestMux(app), s
}

func TestExportAndImportMoveAServerToAFreshInstall(t *testing.T) {
	_, srcMux, _ := hytaleExportSource(t)
	rec := exportServer(t, srcMux, "alpha", "")
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Disposition"), "realmkeeper-alpha-") {
		t.Fatalf("export = %d %s", rec.Code, rec.Header())
	}

	// A fresh install with nothing configured.
	fresh := &App{dataDir: t.TempDir(), managers: map[string]*ServerManager{}}
	mux := newTestMux(fresh)
	code, out := importServer(t, mux, rec.Body.Bytes())
	if code != 201 {
		t.Fatalf("import = %d %v", code, out)
	}
	if len(fresh.cfg.Servers) != 1 {
		t.Fatalf("servers after import = %d", len(fresh.cfg.Servers))
	}
	s := fresh.cfg.Servers[0]
	if s.ID != "alpha" || s.ServerName != "Alpha" || s.Port != 5520 || s.Password != "join-secret" || s.StorageDir != filepath.Join("servers", "alpha") {
		t.Errorf("imported settings = %#v", s)
	}
	root := fresh.serverRoot(s)
	if b, _ := os.ReadFile(filepath.Join(root, "game", "Server", "universe", "worlds", "default", "chunk.bin")); string(b) != "world-data" {
		t.Errorf("world data = %q", b)
	}
	if link, err := os.Readlink(filepath.Join(root, "game", "Server", "world-link")); err != nil || link != "universe" {
		t.Errorf("symlink = %q, %v", link, err)
	}
	if fileExists(filepath.Join(root, "game", "Server", "backups", "auto.zip")) || fileExists(filepath.Join(pathsFor(root).Snapshots(), "snapshot-1.tar.gz")) {
		t.Error("backups were exported without being asked for")
	}
	if got := fresh.cfg.Schedules["alpha"]; len(got) != 1 || got[0].ID != "nightly" {
		t.Errorf("schedules = %+v", got)
	}
	if fresh.managers["alpha"] == nil || !fresh.managers["alpha"].Installed() {
		t.Error("imported server has no manager or is not installed")
	}
}

func TestExportWithBackupsAndImportBesideTheOriginal(t *testing.T) {
	app, mux, _ := hytaleExportSource(t)
	rec := exportServer(t, mux, "alpha", "?backups=1")
	if rec.Code != 200 {
		t.Fatalf("export = %d", rec.Code)
	}
	// Importing into the same install: new ID, folder and port.
	code, out := importServer(t, mux, rec.Body.Bytes())
	if code != 201 {
		t.Fatalf("import = %d %v", code, out)
	}
	s := app.cfg.Servers[1]
	if s.ID == "alpha" || s.Port == 5520 || s.StorageDir == filepath.Join("servers", "alpha") {
		t.Errorf("copy reused the original's identity: %#v", s)
	}
	root := app.serverRoot(s)
	if !fileExists(filepath.Join(root, "game", "Server", "backups", "auto.zip")) || !fileExists(filepath.Join(pathsFor(root).Snapshots(), "snapshot-1.tar.gz")) {
		t.Error("backups were not included when asked for")
	}
}

func TestExportRefusesARunningServer(t *testing.T) {
	app, mux, _ := hytaleExportSource(t)
	m := app.managers["alpha"]
	m.mu.Lock()
	m.running = true
	m.mu.Unlock()
	t.Cleanup(func() { m.mu.Lock(); m.running = false; m.mu.Unlock() })
	if rec := exportServer(t, mux, "alpha", ""); rec.Code != 409 {
		t.Errorf("export of a running server = %d, want 409", rec.Code)
	}
	if rec := exportServer(t, mux, "missing", ""); rec.Code != 404 {
		t.Errorf("export of an unknown server = %d, want 404", rec.Code)
	}
}

// craftedArchive builds an export with a valid manifest and the given entries.
func craftedArchive(t *testing.T, game string, entries ...*tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	manifest, _ := json.Marshal(serverExport{Format: 1, Server: ServerSettings{ServerName: "Crafted", GameType: game, Port: 5520, GameMode: "Adventure", MaxPlayers: 10}})
	_ = tw.WriteHeader(&tar.Header{Name: exportManifestName, Mode: 0o600, Size: int64(len(manifest)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(manifest)
	for _, h := range entries {
		_ = tw.WriteHeader(h)
		if h.Typeflag == tar.TypeReg {
			_, _ = tw.Write(bytes.Repeat([]byte("x"), int(h.Size)))
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func TestImportRefusesUnsafeOrForeignArchives(t *testing.T) {
	cases := map[string][]byte{
		"path escape": craftedArchive(t, "hytale", &tar.Header{Name: "files/../../evil.txt", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}),
		"link escape": craftedArchive(t, "hytale", &tar.Header{Name: "files/game/out", Linkname: "../../../etc", Typeflag: tar.TypeSymlink}),
		"absolute":    craftedArchive(t, "hytale", &tar.Header{Name: "files//etc/passwd", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}),
		"stray entry": craftedArchive(t, "hytale", &tar.Header{Name: "other.txt", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}),
		"planned":     craftedArchive(t, "not-a-game"),
		"not gzip":    []byte("hello"),
	}
	for name, archive := range cases {
		t.Run(name, func(t *testing.T) {
			app := &App{dataDir: t.TempDir(), managers: map[string]*ServerManager{}}
			code, out := importServer(t, newTestMux(app), archive)
			if code != 400 {
				t.Fatalf("import = %d %v, want 400", code, out)
			}
			if len(app.cfg.Servers) != 0 {
				t.Error("a server was added")
			}
			entries, _ := os.ReadDir(filepath.Join(app.dataDir, "servers"))
			if len(entries) != 0 {
				t.Errorf("files left behind: %v", entries)
			}
			if fileExists(filepath.Join(app.dataDir, "evil.txt")) {
				t.Error("a file was written outside the server folder")
			}
		})
	}
	// A well-formed archive with an ordinary file is accepted.
	app := &App{dataDir: t.TempDir(), managers: map[string]*ServerManager{}}
	if code, _ := importServer(t, newTestMux(app), craftedArchive(t, "hytale", &tar.Header{Name: "files/game/ok.txt", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg})); code != 201 {
		t.Errorf("a plain archive was refused: %d", code)
	}
}
