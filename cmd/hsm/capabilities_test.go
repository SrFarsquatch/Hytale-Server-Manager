package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// newCapabilityTestApp builds an App with one stopped server per game type.
func newCapabilityTestApp(t *testing.T, servers ...ServerSettings) *App {
	t.Helper()
	root := t.TempDir()
	app := &App{dataDir: root, managers: map[string]*ServerManager{}}
	for _, s := range servers {
		app.cfg.Servers = append(app.cfg.Servers, s)
		app.managers[s.ID] = NewServerManager(filepath.Join(root, "servers", s.ID), filepath.Join(root, "tools"), s.GameType)
	}
	return app
}

func testServer(id, game string) ServerSettings {
	return ServerSettings{ID: id, GameType: game, ServerName: id, Port: 5520, StorageDir: filepath.Join("servers", id)}
}

func TestEveryAvailableGameHasAnAdapterAndProtocol(t *testing.T) {
	for _, game := range supportedGameCatalog() {
		if !game.Available {
			continue
		}
		if _, err := gameAdapterFor(game.ID); err != nil {
			t.Errorf("available game %q has no adapter: %v", game.ID, err)
		}
		if game.Protocol != "tcp" && game.Protocol != "udp" {
			t.Errorf("available game %q protocol = %q", game.ID, game.Protocol)
		}
		if game.Capabilities.ConfigFiles == nil {
			t.Errorf("available game %q must serialize configFiles as a list", game.ID)
		}
	}
}

func TestGameCapabilitiesFailClosedForPlannedAndUnknownGames(t *testing.T) {
	for _, game := range []string{"terraria-tmodloader", "not-a-game"} {
		if _, ok := gameCatalogEntryFor(game); ok {
			t.Errorf("%q resolved as an available game", game)
		}
		c := gameCapabilitiesFor(game)
		if c.Console || c.ConsoleCommands || c.FileManagement || c.Backups || c.Mods || c.PlayerList || c.DeviceAuth || c.GameUpdates || len(c.ConfigFiles) != 0 {
			t.Errorf("%q capabilities = %#v, want none", game, c)
		}
	}
	if !gameCapabilitiesFor("").DeviceAuth {
		t.Error("legacy servers without a game type must keep Hytale capabilities")
	}
}

func TestRequestServerRequiresExplicitIDWithMultipleServers(t *testing.T) {
	app := newCapabilityTestApp(t, testServer("alpha", "hytale"), testServer("bravo", "minecraft-java"))

	req := httptest.NewRequest("GET", "/api/status", nil)
	if _, _, err := app.requestServer(req); err == nil {
		t.Fatal("request without a server id resolved to a server while two are configured")
	}

	req = httptest.NewRequest("GET", "/api/status", nil)
	req.Header.Set("X-HSM-Server", "bravo")
	if _, s, err := app.requestServer(req); err != nil || s.ID != "bravo" {
		t.Fatalf("header selection = %q, %v; want bravo", s.ID, err)
	}

	req = httptest.NewRequest("GET", "/api/events?server=alpha", nil)
	if _, s, err := app.requestServer(req); err != nil || s.ID != "alpha" {
		t.Fatalf("query selection = %q, %v; want alpha", s.ID, err)
	}

	req = httptest.NewRequest("GET", "/api/status", nil)
	req.Header.Set("X-HSM-Server", "deleted")
	if _, _, err := app.requestServer(req); err == nil {
		t.Fatal("unknown server id must not fall back to another server")
	}
}

func TestRequestServerKeepsLegacySingleServerFallback(t *testing.T) {
	app := newCapabilityTestApp(t, testServer("only", "hytale"))
	req := httptest.NewRequest("GET", "/api/status", nil)
	if _, s, err := app.requestServer(req); err != nil || s.ID != "only" {
		t.Fatalf("single-server fallback = %q, %v; want only", s.ID, err)
	}
}

func TestHytaleOnlyEndpointsRejectOtherGames(t *testing.T) {
	app := newCapabilityTestApp(t, testServer("mc", "minecraft-java"), testServer("dw", "runescape-dragonwilds"))
	mux := newTestMux(app)
	cases := []struct {
		method, path, server, body string
	}{
		{"GET", "/api/mods", "mc", ""},
		{"DELETE", "/api/mods/example.jar", "mc", ""},
		{"GET", "/api/backups", "dw", ""},
		{"POST", "/api/backups/native", "mc", ""},
		{"POST", "/api/backups/snapshot", "dw", ""},
		{"POST", "/api/backups/snapshot-1.tar.gz/restore", "dw", ""},
		{"POST", "/api/auth/device", "mc", ""},
		{"POST", "/api/update/apply", "dw", ""},
		{"GET", "/api/server/file/config.json", "mc", ""},
		{"PUT", "/api/server/file/config.json", "mc", `{"content":"{}"}`},
		{"GET", "/api/server/config", "dw", ""},
		{"POST", "/api/console", "dw", `{"command":"save"}`},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path+" "+tc.server, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-HSM-Server", tc.server)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != 409 {
				t.Fatalf("status = %d, want 409; body: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "not supported") {
				t.Fatalf("error does not explain the unsupported capability: %s", rec.Body.String())
			}
		})
	}
}

func TestHytaleInstallersRejectOtherGames(t *testing.T) {
	app := newCapabilityTestApp(t, testServer("mc", "minecraft-java"))
	mux := newTestMux(app)
	for _, path := range []string{"/api/install/run-downloader", "/api/install/import-local"} {
		req := httptest.NewRequest("POST", path, nil)
		req.Header.Set("X-HSM-Server", "mc")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 400 {
			t.Errorf("%s status = %d, want 400; body: %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestPlayersIsUnknownForGamesWithoutPlayerList(t *testing.T) {
	app := newCapabilityTestApp(t, testServer("dw", "runescape-dragonwilds"))
	mux := newTestMux(app)
	req := httptest.NewRequest("GET", "/api/players", nil)
	req.Header.Set("X-HSM-Server", "dw")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Known bool `json:"known"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Known {
		t.Fatalf("players response = %s (err %v), want known=false", rec.Body.String(), err)
	}
}

func TestServersListIncludesCatalogCapabilities(t *testing.T) {
	app := newCapabilityTestApp(t, testServer("hy", "hytale"), testServer("mc", "minecraft-java"))
	rec := httptest.NewRecorder()
	app.handleServersList(rec, httptest.NewRequest("GET", "/api/servers", nil))
	var out []ServerSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d servers", len(out))
	}
	byID := map[string]ServerSummary{}
	for _, s := range out {
		byID[s.ID] = s
	}
	if hy := byID["hy"]; !hy.Capabilities.Mods || hy.Protocol != "udp" || len(hy.Capabilities.ConfigFiles) == 0 {
		t.Errorf("Hytale summary = %#v", hy)
	}
	if mc := byID["mc"]; mc.Capabilities.Mods || mc.Capabilities.NativeBackups || !mc.Capabilities.Backups || !mc.Capabilities.ConsoleCommands || mc.Protocol != "tcp" {
		t.Errorf("Minecraft summary = %#v", mc)
	}
}

// newTestMux serves the real routes, including session authentication, with
// a valid session cookie attached to every request.
func newTestMux(app *App) http.Handler {
	if app.sessions == nil {
		app.sessions = map[string]Session{}
	}
	token := app.newSession("admin")
	mux := http.NewServeMux()
	app.routes(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "hsm_session", Value: token})
		mux.ServeHTTP(w, r)
	})
}

func TestGameArtOnlyReportsFilesThatExist(t *testing.T) {
	fsys := fstest.MapFS{
		"assets/games/art/hytale-icon.png":                {Data: []byte("png")},
		"assets/games/art/hytale-banner.webp":             {Data: []byte("webp")},
		"assets/games/art/minecraft-java-icon.svg":        {Data: []byte("<svg/>")},
		"assets/games/art/minecraft-java-logo.webp":       {Data: []byte("webp")},
		"assets/games/art/runescape-dragonwilds-icon.gif": {Data: []byte("unsupported")},
	}
	if got := gameArtFrom(fsys, "hytale"); got.Icon != "/assets/games/art/hytale-icon.png" || got.Banner != "/assets/games/art/hytale-banner.webp" {
		t.Errorf("hytale art = %#v", got)
	}
	if got := gameArtFrom(fsys, "minecraft-java"); got.Icon == "" || got.Logo != "/assets/games/art/minecraft-java-logo.webp" || got.Banner != "" {
		t.Errorf("minecraft art = %#v, want icon and logo only", got)
	}
	if got := gameArtFrom(fsys, "runescape-dragonwilds"); got != (GameArt{}) {
		t.Errorf("unsupported extension produced art %#v", got)
	}
}
