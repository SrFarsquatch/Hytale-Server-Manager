package main

import (
	"archive/zip"
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

func terrariaSettings(id string) ServerSettings {
	s := defaultServerSettings(id, "Terra", 5522, filepath.Join("servers", id))
	s.GameType, s.GameMode, s.MaxPlayers, s.MOTD = "terraria", "Expert", 8, "Welcome to Terraria!"
	s.TerrariaVersion, s.TerrariaWorldName, s.TerrariaWorldSize = "latest", "Sky Island", "large"
	return s
}

func TestCreateTerrariaAppliesDefaultsAndRejectsBadWorlds(t *testing.T) {
	app := &App{dataDir: t.TempDir(), managers: map[string]*ServerManager{}}
	code, out := createServer(t, app, `{"game":"terraria","name":"Terra Firma"}`)
	if code != 201 {
		t.Fatalf("create = %d %v", code, out)
	}
	s := app.cfg.Servers[0]
	if s.GameMode != "Classic" || s.MaxPlayers != 8 || s.MOTD != "Welcome to Terraria!" || s.Port != 5520 {
		t.Errorf("defaults = mode %q max %d motd %q port %d", s.GameMode, s.MaxPlayers, s.MOTD, s.Port)
	}
	if s.TerrariaVersion != "latest" || s.TerrariaWorldName != "Terra Firma" || s.TerrariaWorldSize != "medium" {
		t.Errorf("world = %q %q %q", s.TerrariaVersion, s.TerrariaWorldName, s.TerrariaWorldSize)
	}

	for body, want := range map[string]string{
		`{"game":"terraria","name":"Terra","terrariaWorldSize":"huge"}`:  "small, medium or large",
		`{"game":"terraria","name":"Terra","terrariaVersion":"1.4; rm"}`: "release number",
		`{"game":"terraria","name":"Terra","terrariaWorldName":"!!!"}`:   "letter or digit",
		`{"game":"terraria","name":"Terra","gameMode":"Hardcore"}`:       "Classic, Expert, Master, or Journey",
		`{"game":"terraria","name":"Terra","maxPlayers":256}`:            "up to 255 players",
		`{"game":"terraria","name":"Terra","port":7777}`:                 "TCP range",
	} {
		root := t.TempDir()
		app := &App{dataDir: root, managers: map[string]*ServerManager{}}
		code, out := createServer(t, app, body)
		if code != 400 || !strings.Contains(out["error"].(string), want) {
			t.Errorf("%s: %d %v, want 400 mentioning %q", body, code, out, want)
		}
		assertServerCreateDidNotMutateState(t, app, root)
	}
}

func TestTerrariaSettingsKeepTheWorldFixed(t *testing.T) {
	app := newCapabilityTestApp(t, terrariaSettings("tr"))
	code, out := putSettings(t, app, "tr", func(s map[string]any) {
		s["terrariaWorldName"], s["terrariaWorldSize"], s["terrariaVersion"] = "Other", "small", "1.0"
		s["gameMode"], s["memoryMax"] = "Journey", "16G"
		s["maxPlayers"], s["motd"] = float64(16), "Hello"
	})
	if code != 200 {
		t.Fatalf("PUT = %d %v", code, out)
	}
	s := storedServer(app, "tr")
	if s.TerrariaWorldName != "Sky Island" || s.TerrariaWorldSize != "large" || s.TerrariaVersion != "latest" || s.GameMode != "Expert" || s.MemoryMax != "4G" {
		t.Errorf("fixed fields changed: %#v", s)
	}
	if s.MaxPlayers != 16 || s.MOTD != "Hello" {
		t.Errorf("editable fields = max %d motd %q", s.MaxPlayers, s.MOTD)
	}

	// Other games' settings saves must not pick up Terraria fields.
	app = newCapabilityTestApp(t, defaultServerSettings("hy", "Alpha", 5520, filepath.Join("servers", "hy")))
	putSettings(t, app, "hy", func(s map[string]any) { s["terrariaWorldName"] = "intruder" })
	if got := storedServer(app, "hy").TerrariaWorldName; got != "" {
		t.Errorf("Hytale server accepted a Terraria world name %q", got)
	}
}

func TestWriteTerrariaConfigManagesItsKeysAndKeepsOthers(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "game", "serverconfig.txt")
	writeFile(t, cfg, "#comment\nsecure=1\nmaxplayers=2\n")
	s := terrariaSettings("tr")
	s.MOTD = "line one\nline two"
	path, err := writeTerrariaConfig(root, s)
	if err != nil || path != cfg {
		t.Fatalf("path %q, err %v", path, err)
	}
	b, _ := os.ReadFile(cfg)
	got := string(b)
	worlds := filepath.Join(root, "game", "Worlds")
	for _, want := range []string{
		"#comment\n", "secure=1\n", "maxplayers=8\n",
		"world=" + filepath.Join(worlds, "sky-island.wld") + "\n", "worldpath=" + worlds + "\n",
		"worldname=Sky Island\n", "autocreate=3\n", "difficulty=1\n", "port=5522\n",
		"motd=line one line two\n", "upnp=0\n", "language=en-US\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("serverconfig.txt lacks %q:\n%s", want, got)
		}
	}
	if fi, err := os.Stat(worlds); err != nil || !fi.IsDir() {
		t.Error("Worlds folder was not created")
	}
}

func TestParseTerrariaPlaying(t *testing.T) {
	lines := func(text ...string) []LogLine {
		out := make([]LogLine, len(text))
		for i, l := range text {
			out[i] = LogLine{Line: l}
		}
		return out
	}
	cases := []struct {
		name  string
		in    []LogLine
		want  []string
		known bool
	}{
		{"none", lines(": No players connected."), []string{}, true},
		{"one after prompt", lines(": Bob (192.168.1.20:52114)", "1 player connected."), []string{"Bob"}, true},
		{"several", lines("Bob has joined.", ": Bob (10.0.0.2:1)", "Ann Lee ([::1]:2)", "Zed (10.0.0.3:3)", "3 players connected."), []string{"Bob", "Ann Lee", "Zed"}, true},
		{"chat is not a player", lines("<Bob> hi (lol)", "No players connected."), []string{}, true},
		{"no answer yet", lines(": Bob (10.0.0.2:1)"), nil, false},
	}
	for _, tc := range cases {
		got, max, known := parseTerrariaPlaying(tc.in)
		if known != tc.known || max != 0 || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v %d %v, want %v %v", tc.name, got, max, known, tc.want, tc.known)
		}
	}
}

// fakeTerrariaDownloads serves an official-looking server list and zip.
func fakeTerrariaDownloads(t *testing.T, withLinux bool) {
	t.Helper()
	buf := &strings.Builder{}
	zw := zip.NewWriter(writerFunc(func(p []byte) (int, error) { return buf.WriteString(string(p)) }))
	add := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	add("1458/Windows/TerrariaServer.exe", "win")
	if withLinux {
		add("1458/Linux/"+terrariaBinary, "#!/bin/sh\n")
		add("1458/Linux/TerrariaServer", "#!/bin/sh\n")
		add("1458/Linux/Terraria.png", "png")
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/names":
			_, _ = w.Write([]byte(`["terraria-server-1458.zip","terraria-server-1449.zip","notes.txt"]`))
		case "/dl/terraria-server-1458.zip":
			_, _ = w.Write([]byte(buf.String()))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	oldList, oldBase := terrariaServerListURL, terrariaDownloadBaseURL
	terrariaServerListURL, terrariaDownloadBaseURL = srv.URL+"/names", srv.URL+"/dl/"
	t.Cleanup(func() { terrariaServerListURL, terrariaDownloadBaseURL = oldList, oldBase })
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestResolveTerrariaZip(t *testing.T) {
	fakeTerrariaDownloads(t, true)
	ctx, client := context.Background(), http.DefaultClient
	for version, want := range map[string]string{"latest": "terraria-server-1458.zip", "1.4.4.9": "terraria-server-1449.zip", "1449": "terraria-server-1449.zip"} {
		if got, err := resolveTerrariaZip(ctx, client, version); err != nil || got != want {
			t.Errorf("%s: %q, %v; want %q", version, got, err, want)
		}
	}
	if _, err := resolveTerrariaZip(ctx, client, "1.3.0.1"); err == nil {
		t.Error("an unlisted version resolved")
	}
}

func TestTerrariaInstallerExtractsTheLinuxServer(t *testing.T) {
	if !terrariaPlatformSupported {
		t.Skip("the Terraria installer runs only on amd64")
	}
	fakeTerrariaDownloads(t, true)
	app := newCapabilityTestApp(t, terrariaSettings("tr"))
	m := app.managers["tr"]
	if err := m.RunTerrariaInstaller("latest", "vanilla"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "install to finish", func() bool { return !m.InstallBusy() })
	game := filepath.Join(m.rootDir, "game")
	fi, err := os.Stat(filepath.Join(game, terrariaBinary))
	if err != nil || fi.Mode()&0o111 == 0 {
		for _, l := range m.logs.Snapshot() {
			t.Log(l.Line)
		}
		t.Fatalf("server binary missing or not executable: %v", err)
	}
	if fileExists(filepath.Join(game, "TerrariaServer.exe")) || fileExists(filepath.Join(m.rootDir, ".terraria-extract")) || fileExists(filepath.Join(m.rootDir, ".terraria-download.zip")) {
		t.Error("Windows files or download leftovers remain")
	}
	if info, err := readTerrariaInstallInfo(game); err != nil || info.Version != "1458" {
		t.Errorf("install info = %+v, %v", info, err)
	}
	if !m.Installed() {
		t.Error("server does not report installed")
	}
	if err := m.RunTerrariaInstaller("latest", "vanilla"); err == nil || !strings.Contains(err.Error(), "already installed") {
		t.Errorf("second install = %v, want already installed", err)
	}
}

func TestTerrariaInstallerFailsWithoutLinuxServer(t *testing.T) {
	if !terrariaPlatformSupported {
		t.Skip("the Terraria installer runs only on amd64")
	}
	fakeTerrariaDownloads(t, false)
	app := newCapabilityTestApp(t, terrariaSettings("tr"))
	m := app.managers["tr"]
	if err := m.RunTerrariaInstaller("latest", "vanilla"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "install to finish", func() bool { return !m.InstallBusy() })
	if m.Installed() || !logContains(m, "does not contain the Linux server", time.Time{}) {
		t.Error("an archive without the Linux server was accepted")
	}
}

func TestTerrariaInstallRejectsOtherGames(t *testing.T) {
	app := newCapabilityTestApp(t, testServer("hy", "hytale"))
	req := httptest.NewRequest("POST", "/api/install/terraria", nil)
	req.Header.Set("X-HSM-Server", "hy")
	rec := httptest.NewRecorder()
	newTestMux(app).ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
}

// fakeTerrariaServer behaves like the real console: it prints a ": " prompt
// without a newline, so answers start on the prompt's line.
func fakeTerrariaServer(t *testing.T, root string) {
	t.Helper()
	script := "#!/bin/bash\n" +
		"echo \"args: $*\"\n" +
		"echo 'Server started'\n" +
		"while true; do\n" +
		"  printf ': '\n" +
		"  read -r line || exit 0\n" +
		"  case \"$line\" in\n" +
		"    playing) echo 'Bob (10.0.0.2:51234)'; echo 'Ann (10.0.0.3:51235)'; echo '2 players connected.' ;;\n" +
		"    exit) echo 'Saving world...'; exit 0 ;;\n" +
		"    *) echo \"got $line\" ;;\n" +
		"  esac\n" +
		"done\n"
	writeFile(t, filepath.Join(root, "game", terrariaBinary), script)
}

func TestTerrariaServerStartsListsPlayersAndStops(t *testing.T) {
	if !terrariaPlatformSupported {
		t.Skip("Terraria runs only on amd64")
	}
	app := newCapabilityTestApp(t, terrariaSettings("tr"))
	m := app.managers["tr"]
	fakeTerrariaServer(t, m.rootDir)
	mux := newTestMux(app)
	call := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("X-HSM-Server", "tr")
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
	waitFor(t, "fake Terraria ready", func() bool { return logContains(m, "Server started", time.Time{}) })
	cfg := filepath.Join(m.rootDir, "game", "serverconfig.txt")
	if !logContains(m, "args: -config "+cfg, time.Time{}) {
		t.Error("server was not started with -config serverconfig.txt")
	}

	var players struct {
		Known      bool     `json:"known"`
		Count      int      `json:"count"`
		MaxPlayers int      `json:"maxPlayers"`
		Players    []string `json:"players"`
	}
	if err := json.Unmarshal(call("GET", "/api/players").Body.Bytes(), &players); err != nil {
		t.Fatal(err)
	}
	if !players.Known || players.Count != 2 || players.MaxPlayers != 8 || !reflect.DeepEqual(players.Players, []string{"Bob", "Ann"}) {
		t.Fatalf("players = %+v", players)
	}

	if rec := call("POST", "/api/server/stop"); rec.Code >= 300 {
		t.Fatalf("stop = %d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, "server to exit after exit", func() bool { return !m.IsRunning() })
	if !logContains(m, "Saving world", time.Time{}) {
		t.Error("stop did not send exit")
	}
}

func TestTerrariaSnapshotSpecCoversWorldsAndConfig(t *testing.T) {
	spec, ok := snapshotSpecFor("/r", terrariaSettings("tr"))
	if !ok || spec.Base != filepath.Join("/r", "game") || !reflect.DeepEqual(spec.Include, []string{"Worlds", "serverconfig.txt", "banlist.txt"}) {
		t.Fatalf("spec = %+v, %v", spec, ok)
	}
	if got := broadcastTemplate("terraria"); got != "say {message}" {
		t.Errorf("broadcast = %q", got)
	}
}
