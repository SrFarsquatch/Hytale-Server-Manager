package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests pin the per-game rules applied by the generic server handlers
// (create, settings, install status, players), so moving those rules into
// each game's adapter cannot change what users see.

func createServer(t *testing.T, app *App, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/servers", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.handleServersCreate(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestCreateAppliesEachGamesDefaults(t *testing.T) {
	cases := []struct {
		name, body                  string
		gameMode, motd              string
		maxPlayers, port            int
		jvmOptions                  bool
		minecraftVersion, worldName string
	}{
		{
			name: "hytale", body: `{"name":"Alpha"}`,
			gameMode: "Adventure", motd: "Welcome to your Hytale server!", maxPlayers: 20, port: 5520, jvmOptions: true,
		},
		{
			name: "minecraft", body: `{"game":"minecraft-java","name":"Blocks","minecraftEulaAccepted":true}`,
			gameMode: "Survival", motd: "A Minecraft Server", maxPlayers: 20, port: 5520, minecraftVersion: "latest",
		},
		{
			name: "dragonwilds", body: `{"game":"runescape-dragonwilds","name":"Vale","dragonwildsOwnerId":"owner-1","dragonwildsAdminPassword":"admin"}`,
			gameMode: "Adventure", motd: "Welcome to your Hytale server!", maxPlayers: 6, port: 7777, worldName: "Vale",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			app := &App{dataDir: root, managers: map[string]*ServerManager{}}
			code, out := createServer(t, app, tc.body)
			if code != 201 {
				t.Fatalf("create = %d %v", code, out)
			}
			s := app.cfg.Servers[0]
			if s.GameMode != tc.gameMode || s.MOTD != tc.motd || s.MaxPlayers != tc.maxPlayers || s.Port != tc.port {
				t.Errorf("defaults = mode %q motd %q max %d port %d", s.GameMode, s.MOTD, s.MaxPlayers, s.Port)
			}
			if s.MinecraftVersion != tc.minecraftVersion || s.DragonwildsWorldName != tc.worldName {
				t.Errorf("game fields = version %q world %q", s.MinecraftVersion, s.DragonwildsWorldName)
			}
			if tc.name == "minecraft" && s.MinecraftVariant != "vanilla" {
				t.Errorf("variant = %q, want vanilla", s.MinecraftVariant)
			}
			_, err := os.Stat(filepath.Join(app.serverRoot(s), "game", "jvm.options"))
			if (err == nil) != tc.jvmOptions {
				t.Errorf("jvm.options present = %v, want %v", err == nil, tc.jvmOptions)
			}
		})
	}
}

func TestCreateRejectsValuesOutsideEachGamesRules(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"hytale mode", `{"name":"Alpha","gameMode":"Survival"}`, "Adventure or Creative"},
		{"hytale port", `{"name":"Alpha","port":7777}`, "UDP range"},
		{"minecraft mode", `{"game":"minecraft-java","name":"Blocks","minecraftEulaAccepted":true,"gameMode":"Hardcore"}`, "Survival, Adventure, Creative, or Spectator"},
		{"minecraft variant", `{"game":"minecraft-java","name":"Blocks","minecraftEulaAccepted":true,"minecraftVariant":"forge"}`, "vanilla, paper or fabric"},
		{"minecraft version", `{"game":"minecraft-java","name":"Blocks","minecraftEulaAccepted":true,"minecraftVersion":"1.21\n"}`, "invalid Minecraft version"},
		{"minecraft port", `{"game":"minecraft-java","name":"Blocks","minecraftEulaAccepted":true,"port":7777}`, "TCP range"},
		{"dragonwilds players", `{"game":"runescape-dragonwilds","name":"Vale","dragonwildsOwnerId":"o","dragonwildsAdminPassword":"a","maxPlayers":7}`, "up to 6 players"},
		{"dragonwilds port", `{"game":"runescape-dragonwilds","name":"Vale","dragonwildsOwnerId":"o","dragonwildsAdminPassword":"a","port":5520}`, "UDP range 7777-7782"},
		{"dragonwilds world", `{"game":"runescape-dragonwilds","name":"Vale","dragonwildsOwnerId":"o","dragonwildsAdminPassword":"a","dragonwildsWorldName":"` + strings.Repeat("w", 33) + `"}`, "world name"},
		{"dragonwilds owner", `{"game":"runescape-dragonwilds","name":"Vale","dragonwildsOwnerId":"` + strings.Repeat("o", 129) + `","dragonwildsAdminPassword":"a"}`, "Owner ID"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			app := &App{dataDir: root, managers: map[string]*ServerManager{}}
			code, out := createServer(t, app, tc.body)
			if code != 400 || !strings.Contains(out["error"].(string), tc.want) {
				t.Fatalf("create = %d %v, want 400 mentioning %q", code, out, tc.want)
			}
			assertServerCreateDidNotMutateState(t, app, root)
		})
	}
}

func putSettings(t *testing.T, app *App, id string, edit func(map[string]any)) (int, map[string]any) {
	t.Helper()
	mux := newTestMux(app)
	req := httptest.NewRequest("GET", "/api/settings", nil)
	req.Header.Set("X-HSM-Server", id)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("GET settings: %v %s", err, rec.Body.String())
	}
	edit(got)
	body, _ := json.Marshal(got)
	req = httptest.NewRequest("PUT", "/api/settings", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-HSM-Server", id)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func storedServer(app *App, id string) ServerSettings {
	app.cfgMu.RLock()
	defer app.cfgMu.RUnlock()
	for _, s := range app.cfg.Servers {
		if s.ID == id {
			return s
		}
	}
	return ServerSettings{}
}

func minecraftSettings(id string) ServerSettings {
	s := defaultServerSettings(id, id, 5521, filepath.Join("servers", id))
	s.GameType, s.GameMode, s.MOTD = "minecraft-java", "Survival", "A Minecraft Server"
	s.MinecraftVersion, s.MinecraftVariant, s.MinecraftEulaAccepted = "1.21.1", "vanilla", true
	return s
}

func dragonwildsSettings(id string) ServerSettings {
	s := defaultServerSettings(id, id, 7777, filepath.Join("servers", id))
	s.GameType, s.MaxPlayers = "runescape-dragonwilds", 6
	s.DragonwildsOwnerID, s.DragonwildsWorldName, s.DragonwildsAdminPassword = "owner-1", "Vale", "admin-secret"
	return s
}

func TestSettingsKeepMinecraftIdentityFixed(t *testing.T) {
	app := newCapabilityTestApp(t, minecraftSettings("mc"))
	code, out := putSettings(t, app, "mc", func(s map[string]any) {
		s["minecraftVersion"], s["minecraftVariant"], s["minecraftEulaAccepted"] = "1.8", "paper", false
		s["gameMode"] = "Spectator"
		s["dragonwildsOwnerId"] = "intruder"
	})
	if code != 200 {
		t.Fatalf("PUT = %d %v", code, out)
	}
	s := storedServer(app, "mc")
	if s.MinecraftVersion != "1.21.1" || s.MinecraftVariant != "vanilla" || !s.MinecraftEulaAccepted {
		t.Errorf("Minecraft identity changed: %#v", s)
	}
	if s.GameMode != "Spectator" || s.DragonwildsOwnerID != "" {
		t.Errorf("mode %q, Dragonwilds owner %q", s.GameMode, s.DragonwildsOwnerID)
	}
	if _, err := os.Stat(filepath.Join(app.serverRoot(s), "game", "jvm.options")); err == nil {
		t.Error("a Minecraft settings save wrote Hytale's jvm.options")
	}

	code, out = putSettings(t, app, "mc", func(s map[string]any) { s["port"] = float64(7777) })
	if code != 400 || !strings.Contains(out["error"].(string), "TCP range") {
		t.Errorf("out-of-range port = %d %v", code, out)
	}
}

func TestSettingsLockDragonwildsFieldsAndValidateItsOwn(t *testing.T) {
	app := newCapabilityTestApp(t, dragonwildsSettings("dw"))
	code, out := putSettings(t, app, "dw", func(s map[string]any) {
		s["maxPlayers"], s["motd"], s["memoryMax"] = float64(50), "changed", "16G"
		s["dragonwildsWorldName"] = "  New Vale  "
	})
	if code != 200 {
		t.Fatalf("PUT = %d %v", code, out)
	}
	s := storedServer(app, "dw")
	if s.MaxPlayers != 6 || s.MOTD != "Welcome to your Hytale server!" || s.MemoryMax != "4G" {
		t.Errorf("locked fields changed: max %d motd %q memory %q", s.MaxPlayers, s.MOTD, s.MemoryMax)
	}
	if s.DragonwildsWorldName != "New Vale" || s.DragonwildsAdminPassword != "admin-secret" {
		t.Errorf("world %q, admin password kept = %v", s.DragonwildsWorldName, s.DragonwildsAdminPassword == "admin-secret")
	}

	for name, edit := range map[string]func(map[string]any){
		"owner": func(s map[string]any) { s["dragonwildsOwnerId"] = " " },
		"world": func(s map[string]any) { s["dragonwildsWorldName"] = strings.Repeat("w", 33) },
		"port":  func(s map[string]any) { s["port"] = float64(5520) },
	} {
		if code, out := putSettings(t, app, "dw", edit); code != 400 {
			t.Errorf("%s: PUT = %d %v, want 400", name, code, out)
		}
	}
}

func TestSettingsWriteHytaleFilesAndKeepOtherGamesFields(t *testing.T) {
	s := defaultServerSettings("hy", "Alpha", 5520, filepath.Join("servers", "hy"))
	app := newCapabilityTestApp(t, s)
	code, out := putSettings(t, app, "hy", func(s map[string]any) {
		s["memoryMax"] = "6G"
		s["dragonwildsOwnerId"] = "intruder"
	})
	if code != 200 {
		t.Fatalf("PUT = %d %v", code, out)
	}
	b, err := os.ReadFile(filepath.Join(app.serverRoot(s), "game", "jvm.options"))
	if err != nil || !strings.Contains(string(b), "-Xmx6G") {
		t.Errorf("jvm.options = %q, %v", b, err)
	}
	if got := storedServer(app, "hy").DragonwildsOwnerID; got != "" {
		t.Errorf("Hytale server accepted a Dragonwilds owner %q", got)
	}
	if code, out := putSettings(t, app, "hy", func(s map[string]any) { s["gameMode"] = "Spectator" }); code != 400 {
		t.Errorf("Spectator on Hytale = %d %v, want 400", code, out)
	}
}

func TestInstallStatusDescribesEachGamesInstall(t *testing.T) {
	app := newCapabilityTestApp(t, testServer("hy", "hytale"), minecraftSettings("mc"), dragonwildsSettings("dw"))
	mux := newTestMux(app)
	want := map[string][]string{
		"hy": {"downloaderPresent", "bundlePresent", "paths.downloader", "paths.bundle", "paths.game"},
		"mc": {"game", "version", "variant", "eulaAccepted", "paths.game"},
		"dw": {"game", "paths.game", "paths.steamcmd"},
	}
	for id, keys := range want {
		req := httptest.NewRequest("GET", "/api/install/status", nil)
		req.Header.Set("X-HSM-Server", id)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != 200 {
			t.Fatalf("%s: %d %s", id, rec.Code, rec.Body.String())
		}
		if out["installed"] != false || out["busy"] != false {
			t.Errorf("%s: installed/busy = %v/%v", id, out["installed"], out["busy"])
		}
		for _, key := range keys {
			v, ok := out[key]
			if p, isPath := strings.CutPrefix(key, "paths."); isPath {
				paths, _ := out["paths"].(map[string]any)
				v, ok = paths[p]
			}
			if !ok || v == nil {
				t.Errorf("%s: missing %s in %v", id, key, out)
			}
		}
	}
}

func TestPlayersForDragonwildsReportsUnknownWithItsLimit(t *testing.T) {
	app := newCapabilityTestApp(t, dragonwildsSettings("dw"))
	req := httptest.NewRequest("GET", "/api/players", nil)
	req.Header.Set("X-HSM-Server", "dw")
	rec := httptest.NewRecorder()
	newTestMux(app).ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["known"] != false || out["maxPlayers"] != float64(6) || !strings.Contains(out["message"].(string), "Dragonwilds") {
		t.Fatalf("players = %v", out)
	}
}

func TestEveryAvailableGameDeclaresUsableRules(t *testing.T) {
	for _, entry := range supportedGameCatalog() {
		if !entry.Available {
			continue
		}
		adapter, err := gameAdapterFor(entry.ID)
		if err != nil {
			t.Fatalf("%s: %v", entry.ID, err)
		}
		r := adapter.Rules()
		min, max := r.portRange()
		if len(r.GameModes) == 0 || r.DefaultMOTD == "" || r.DefaultMaxPlayers < 1 || r.DefaultMaxPlayers > r.PlayerLimit || min < 1 || max < min {
			t.Errorf("%s rules = %#v, ports %d-%d", entry.ID, r, min, max)
		}
		if err := r.checkGameMode(r.GameModes[0]); err != nil {
			t.Errorf("%s rejects its own default mode: %v", entry.ID, err)
		}
	}
}

func TestCatalogServesResolvedRulesForAvailableGames(t *testing.T) {
	min, max, _ := deploymentPortRange()
	for _, g := range catalogWithArt() {
		if !g.Available {
			if g.Rules != nil {
				t.Errorf("%s: unavailable game has rules", g.ID)
			}
			continue
		}
		if g.Rules == nil || len(g.Rules.GameModes) == 0 || g.Rules.ModeLabel == "" || g.Rules.PortMin == 0 {
			t.Fatalf("%s: rules = %+v", g.ID, g.Rules)
		}
		wantMin, wantMax := min, max
		if g.ID == "runescape-dragonwilds" {
			wantMin, wantMax = 7777, 7782
		}
		if g.Rules.PortMin != wantMin || g.Rules.PortMax != wantMax {
			t.Errorf("%s: ports %d-%d, want %d-%d", g.ID, g.Rules.PortMin, g.Rules.PortMax, wantMin, wantMax)
		}
	}
}
