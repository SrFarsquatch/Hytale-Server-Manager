package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func tmodloaderSettings(id string) ServerSettings {
	s := terrariaSettings(id)
	s.TerrariaVariant, s.TerrariaVersion = "tmodloader", "latest"
	return s
}

func TestCreateTModLoaderValidatesItsVersion(t *testing.T) {
	for body, want := range map[string]int{
		`{"game":"terraria","name":"Modded","terrariaVariant":"tmodloader"}`:                                  201,
		`{"game":"terraria","name":"Modded","terrariaVariant":"tmodloader","terrariaVersion":"v2025.08.3.0"}`: 201,
		`{"game":"terraria","name":"Modded","terrariaVariant":"tmodloader","terrariaVersion":"1.4.4.9"}`:      400,
		`{"game":"terraria","name":"Modded","terrariaVariant":"tshock"}`:                                      400,
	} {
		app := &App{dataDir: t.TempDir(), managers: map[string]*ServerManager{}}
		if code, out := createServer(t, app, body); code != want {
			t.Errorf("%s: %d %v, want %d", body, code, out, want)
		} else if code == 201 && app.cfg.Servers[0].TerrariaVariant != "tmodloader" {
			t.Errorf("%s: variant not stored", body)
		}
	}
}

// fakeTModLoaderRelease serves GitHub's latest-release API and a release zip.
func fakeTModLoaderRelease(t *testing.T, withDLL bool) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string) {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	if withDLL {
		add(tmodloaderDLL, "dll")
	}
	add(tmodloaderStartScript, "#!/bin/bash\n")
	add("LaunchUtils/ScriptCaller.sh", "#!/bin/bash\n")
	_ = zw.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			_, _ = w.Write([]byte(`{"tag_name":"v2025.08.3.0"}`))
		case "/dl/v2025.08.3.0/tModLoader.zip":
			_, _ = w.Write(buf.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	oldLatest, oldBase := tmodloaderLatestReleaseURL, tmodloaderDownloadBaseURL
	tmodloaderLatestReleaseURL, tmodloaderDownloadBaseURL = srv.URL+"/latest", srv.URL+"/dl/"
	t.Cleanup(func() { tmodloaderLatestReleaseURL, tmodloaderDownloadBaseURL = oldLatest, oldBase })
}

func TestResolveTModLoaderTag(t *testing.T) {
	fakeTModLoaderRelease(t, true)
	ctx, client := context.Background(), http.DefaultClient
	if tag, err := resolveTModLoaderTag(ctx, client, "latest"); err != nil || tag != "v2025.08.3.0" {
		t.Errorf("latest = %q, %v", tag, err)
	}
	if tag, err := resolveTModLoaderTag(ctx, client, "v2024.05.3.0"); err != nil || tag != "v2024.05.3.0" {
		t.Errorf("explicit = %q, %v", tag, err)
	}
	if _, err := resolveTModLoaderTag(ctx, client, "../../evil"); err == nil {
		t.Error("an invalid tag was accepted")
	}
}

func TestTModLoaderInstallerExtractsTheRelease(t *testing.T) {
	fakeTModLoaderRelease(t, true)
	oldPlatform := terrariaPlatformSupported
	terrariaPlatformSupported = false // tModLoader must not depend on x86-64
	t.Cleanup(func() { terrariaPlatformSupported = oldPlatform })
	app := newCapabilityTestApp(t, tmodloaderSettings("tm"))
	m := app.managers["tm"]
	if err := m.RunTerrariaInstaller("latest", "tmodloader"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "install to finish", func() bool { return !m.InstallBusy() })
	game := filepath.Join(m.rootDir, "game")
	if !m.Installed() {
		for _, l := range m.logs.Snapshot() {
			t.Log(l.Line)
		}
		t.Fatal("tModLoader not installed")
	}
	for _, script := range []string{tmodloaderStartScript, "LaunchUtils/ScriptCaller.sh"} {
		if fi, err := os.Stat(filepath.Join(game, script)); err != nil || fi.Mode()&0o111 == 0 {
			t.Errorf("%s missing or not executable", script)
		}
	}
	if fi, err := os.Stat(filepath.Join(game, "Mods")); err != nil || !fi.IsDir() {
		t.Error("Mods folder was not created")
	}
	if info, err := readTerrariaInstallInfo(game); err != nil || info.Version != "v2025.08.3.0" {
		t.Errorf("install info = %+v, %v", info, err)
	}
	if err := m.RunTerrariaInstaller("latest", "vanilla"); err == nil {
		t.Error("vanilla install was allowed off x86-64")
	}
}

func TestTModLoaderInstallerRejectsAZipWithoutTheServer(t *testing.T) {
	fakeTModLoaderRelease(t, false)
	app := newCapabilityTestApp(t, tmodloaderSettings("tm"))
	m := app.managers["tm"]
	if err := m.RunTerrariaInstaller("latest", "tmodloader"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "install to finish", func() bool { return !m.InstallBusy() })
	if m.Installed() || !logContains(m, "does not contain the tModLoader server", time.Time{}) {
		t.Error("a release without tModLoader.dll was accepted")
	}
}

// fakeTModLoaderServer is a start script that echoes its arguments and then
// behaves like the Terraria console.
func fakeTModLoaderServer(t *testing.T, root string) {
	t.Helper()
	game := filepath.Join(root, "game")
	writeFile(t, filepath.Join(game, tmodloaderDLL), "dll")
	fakeTerrariaServer(t, root)
	b, _ := os.ReadFile(filepath.Join(game, terrariaBinary))
	_ = os.Remove(filepath.Join(game, terrariaBinary))
	writeFile(t, filepath.Join(game, tmodloaderStartScript), string(b))
}

func TestTModLoaderStartsWithoutSteamAndKeepsSavesInTheServerFolder(t *testing.T) {
	app := newCapabilityTestApp(t, tmodloaderSettings("tm"))
	m := app.managers["tm"]
	fakeTModLoaderServer(t, m.rootDir)
	mux := newTestMux(app)
	call := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("X-HSM-Server", "tm")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := call("POST", "/api/server/start"); rec.Code >= 300 {
		t.Fatalf("start = %d %s", rec.Code, rec.Body.String())
	}
	t.Cleanup(func() {
		if pid := currentPID(m); pid > 0 {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	})
	waitFor(t, "fake tModLoader ready", func() bool { return logContains(m, "Server started", time.Time{}) })
	game := filepath.Join(m.rootDir, "game")
	want := "args: -nosteam -config " + filepath.Join(game, "serverconfig.txt") + " -tmlsavedirectory " + game
	if !logContains(m, want, time.Time{}) {
		t.Errorf("launch arguments did not include %q", want)
	}
	var players struct {
		Known   bool     `json:"known"`
		Players []string `json:"players"`
	}
	_ = json.Unmarshal(call("GET", "/api/players").Body.Bytes(), &players)
	if !players.Known || !reflect.DeepEqual(players.Players, []string{"Bob", "Ann"}) {
		t.Errorf("players = %+v", players)
	}
	call("POST", "/api/server/stop")
	waitFor(t, "server to exit", func() bool { return !m.IsRunning() })
}

func TestTModLoaderModsKeepEnabledJSONInSync(t *testing.T) {
	s := tmodloaderSettings("tm")
	app := newCapabilityTestApp(t, s)
	mux := newTestMux(app)
	dir := filepath.Join(app.serverRoot(s), "game", "Mods")
	enabled := func() []string {
		var list []string
		b, _ := os.ReadFile(filepath.Join(dir, "enabled.json"))
		_ = json.Unmarshal(b, &list)
		return list
	}
	for _, name := range []string{"CalamityMod.tmod", "MagicStorage.tmod"} {
		if rec := uploadMod(t, mux, "tm", name); rec.Code != 201 {
			t.Fatalf("upload %s = %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if got := enabled(); !reflect.DeepEqual(got, []string{"CalamityMod", "MagicStorage"}) {
		t.Errorf("enabled.json = %v", got)
	}
	if rec := uploadMod(t, mux, "tm", "Calamity.jar"); rec.Code != 400 {
		t.Errorf(".jar on tModLoader = %d, want 400", rec.Code)
	}

	req := httptest.NewRequest("GET", "/api/mods", nil)
	req.Header.Set("X-HSM-Server", "tm")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "enabled.json") || !strings.Contains(rec.Body.String(), "MagicStorage.tmod") {
		t.Errorf("mods list = %s", rec.Body.String())
	}

	del := func(name string) int {
		req := httptest.NewRequest("DELETE", "/api/mods/"+name, nil)
		req.Header.Set("X-HSM-Server", "tm")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := del("CalamityMod.tmod"); code != 200 || !reflect.DeepEqual(enabled(), []string{"MagicStorage"}) {
		t.Errorf("delete = %d, enabled.json = %v", code, enabled())
	}
	if code := del("enabled.json"); code != 400 {
		t.Errorf("deleting enabled.json = %d, want 400", code)
	}

	// Vanilla Terraria has no Mods tab.
	van := newCapabilityTestApp(t, terrariaSettings("tr"))
	if rec := uploadMod(t, newTestMux(van), "tr", "CalamityMod.tmod"); rec.Code != 409 {
		t.Errorf("vanilla Terraria mod upload = %d, want 409", rec.Code)
	}
}

func TestTModLoaderSnapshotsIncludeModsAndTheyStayFixed(t *testing.T) {
	spec, ok := snapshotSpecFor("/r", tmodloaderSettings("tm"))
	if !ok || !reflect.DeepEqual(spec.Include, []string{"Worlds", "serverconfig.txt", "banlist.txt", "Mods", "ModConfigs"}) {
		t.Fatalf("spec = %+v", spec)
	}
	app := newCapabilityTestApp(t, tmodloaderSettings("tm"))
	putSettings(t, app, "tm", func(s map[string]any) { s["terrariaVariant"] = "vanilla" })
	if got := storedServer(app, "tm").TerrariaVariant; got != "tmodloader" {
		t.Errorf("variant changed to %q", got)
	}
}
