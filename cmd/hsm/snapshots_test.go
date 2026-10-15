package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func tarEntries(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
	sort.Strings(names)
	return names
}

func TestMinecraftLevelNameFallsBackToSafeDefault(t *testing.T) {
	dir := t.TempDir()
	if got := minecraftLevelName(dir); got != "world" {
		t.Errorf("missing properties: %q", got)
	}
	writeFile(t, filepath.Join(dir, "server.properties"), "motd=hi\nlevel-name=survival\n")
	if got := minecraftLevelName(dir); got != "survival" {
		t.Errorf("custom level: %q", got)
	}
	for _, bad := range []string{"../escape", "a/b", "..", ""} {
		writeFile(t, filepath.Join(dir, "server.properties"), "level-name="+bad+"\n")
		if got := minecraftLevelName(dir); got != "world" {
			t.Errorf("level-name %q gave %q, want world", bad, got)
		}
	}
}

func TestRestoreSnapshotReplacesEntriesInsteadOfMerging(t *testing.T) {
	base := t.TempDir()
	spec := snapshotSpec{Base: base, Include: []string{"world", "server.properties"}}
	writeFile(t, filepath.Join(base, "world", "region", "r.0.0.mca"), "old region")
	writeFile(t, filepath.Join(base, "server.properties"), "difficulty=easy\n")
	snap := filepath.Join(t.TempDir(), "snap.tar.gz")
	if err := createTarGz(snap, base, spec.Include); err != nil {
		t.Fatal(err)
	}
	// After the snapshot the world grows and settings change.
	writeFile(t, filepath.Join(base, "world", "region", "r.0.0.mca"), "new region")
	writeFile(t, filepath.Join(base, "world", "region", "r.5.5.mca"), "explored later")
	writeFile(t, filepath.Join(base, "server.properties"), "difficulty=hard\n")
	writeFile(t, filepath.Join(base, "server.jar"), "not part of the snapshot")

	if err := restoreSnapshot(snap, spec); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(base, "world", "region", "r.0.0.mca")); string(b) != "old region" {
		t.Errorf("region file = %q, want the snapshot's", b)
	}
	if _, err := os.Stat(filepath.Join(base, "world", "region", "r.5.5.mca")); !os.IsNotExist(err) {
		t.Error("a region file created after the snapshot survived the restore")
	}
	if b, _ := os.ReadFile(filepath.Join(base, "server.properties")); string(b) != "difficulty=easy\n" {
		t.Errorf("server.properties = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(base, "server.jar")); string(b) != "not part of the snapshot" {
		t.Error("restore touched a file outside the snapshot")
	}
	entries, _ := os.ReadDir(base)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".hsm-restore-") {
			t.Errorf("staging directory %s left behind", e.Name())
		}
	}
}

func TestRestoreSnapshotLeavesDataUntouchedWhenArchiveIsCorrupt(t *testing.T) {
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "world", "level.dat"), "current")
	bad := filepath.Join(t.TempDir(), "bad.tar.gz")
	writeFile(t, bad, "not a gzip archive")
	if err := restoreSnapshot(bad, snapshotSpec{Base: base, Include: []string{"world"}}); err == nil {
		t.Fatal("corrupt snapshot restored without error")
	}
	if b, _ := os.ReadFile(filepath.Join(base, "world", "level.dat")); string(b) != "current" {
		t.Error("corrupt snapshot modified live data")
	}
}

// fakeMinecraft installs a server.jar/eula.txt and puts a fake `java` first
// on PATH. It confirms "save-all flush" unless saveConfirm is false.
func fakeMinecraft(t *testing.T, root string, saveConfirm bool) {
	t.Helper()
	game := filepath.Join(root, "game")
	writeFile(t, filepath.Join(game, "server.jar"), "jar")
	writeFile(t, filepath.Join(game, "eula.txt"), "eula=true\n")
	writeFile(t, filepath.Join(game, "world", "level.dat"), "level")
	writeFile(t, filepath.Join(game, "server.properties"), "level-name=world\n")
	confirm := `echo "[Server thread/INFO]: Saved the game"`
	if !saveConfirm {
		confirm = `:`
	}
	bin := t.TempDir()
	script := "#!/bin/bash\n" +
		"if [ \"$1\" = \"-version\" ]; then echo 'openjdk version \"21.0.4\" 2024-07-16' >&2; exit 0; fi\n" +
		"echo '[Server thread/INFO]: Done (1.0s)! For help, type \"help\"'\n" +
		"while read -r line; do\n" +
		"  echo \"[Server thread/INFO]: got $line\"\n" +
		"  if [ \"$line\" = \"save-all flush\" ]; then " + confirm + "; fi\n" +
		"  if [ \"$line\" = \"list\" ]; then echo '[Server thread/INFO]: There are 2 of a max of 20 players online: Alex, Steve'; fi\n" +
		"  if [ \"$line\" = \"stop\" ]; then exit 0; fi\n" +
		"done\n"
	writeFile(t, filepath.Join(bin, "java"), script)
	if err := os.Chmod(filepath.Join(bin, "java"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func minecraftTestServer(t *testing.T, saveConfirm bool) (*App, *ServerManager) {
	t.Helper()
	s := defaultServerSettings("mc", "Minecraft", 5521, filepath.Join("servers", "mc"))
	s.GameType = "minecraft-java"
	s.MinecraftEulaAccepted = true
	s.MinecraftVariant = "vanilla"
	s.GameMode = "Survival"
	app := newCapabilityTestApp(t, s)
	m := app.managers["mc"]
	fakeMinecraft(t, m.rootDir, saveConfirm)
	if err := m.Start([]string{"-jar", "server.jar", "nogui"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-currentPID(m), syscall.SIGKILL) })
	waitFor(t, "fake Minecraft ready", func() bool { return logContains(m, "Done (", time.Time{}) })
	return app, m
}

func TestLiveMinecraftSnapshotHoldsSavesAndCapturesWorld(t *testing.T) {
	app, m := minecraftTestServer(t, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/backups/snapshot", nil)
	req.Header.Set("X-HSM-Server", "mc")
	newTestMux(app).ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct{ Name string }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	entries := tarEntries(t, filepath.Join(pathsFor(m.rootDir).Snapshots(), out.Name))
	if !contains(entries, "world/level.dat") || !contains(entries, "server.properties") {
		t.Errorf("snapshot entries = %v", entries)
	}
	if contains(entries, "server.jar") || contains(entries, "eula.txt") {
		t.Errorf("snapshot captured files outside the Minecraft spec: %v", entries)
	}
	waitFor(t, "autosave re-enabled", func() bool { return logContains(m, "got save-on", time.Time{}) })
	order := commandOrder(m, "got save-off", "got save-all flush", "got save-on")
	if order != "ok" {
		t.Errorf("save commands out of order: %s", order)
	}
}

func TestLiveMinecraftSnapshotFailsSafelyWithoutSaveConfirmation(t *testing.T) {
	app, m := minecraftTestServer(t, false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/backups/snapshot", nil)
	req.Header.Set("X-HSM-Server", "mc")
	orig := minecraftSaveTimeoutForTest(500)
	defer orig()
	newTestMux(app).ServeHTTP(rec, req)
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), "did not confirm") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if files, _ := os.ReadDir(pathsFor(m.rootDir).Snapshots()); len(files) != 0 {
		t.Errorf("an unconfirmed snapshot was kept: %v", files)
	}
	waitFor(t, "autosave re-enabled after failure", func() bool { return logContains(m, "got save-on", time.Time{}) })
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// commandOrder checks that the given log fragments appear in order.
func commandOrder(m *ServerManager, fragments ...string) string {
	next := 0
	for _, line := range m.logs.Snapshot() {
		if next < len(fragments) && strings.Contains(line.Line, fragments[next]) {
			next++
		}
	}
	if next == len(fragments) {
		return "ok"
	}
	return "missing or out of order from " + fragments[next]
}

func TestBackupsListReturnsEmptyListsBeforeFirstSnapshot(t *testing.T) {
	app := newCapabilityTestApp(t, testServer("mc", "minecraft-java"))
	req := httptest.NewRequest("GET", "/api/backups", nil)
	req.Header.Set("X-HSM-Server", "mc")
	rec := httptest.NewRecorder()
	newTestMux(app).ServeHTTP(rec, req)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "null") {
		t.Fatalf("GET /api/backups = %d %s, want empty arrays rather than null", rec.Code, rec.Body.String())
	}
}
