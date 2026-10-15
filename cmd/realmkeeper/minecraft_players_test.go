package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeMojang answers profile lookups for Notch and Jeb_ only.
func fakeMojang(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.ToLower(strings.TrimPrefix(r.URL.Path, "/")) {
		case "notch":
			_, _ = w.Write([]byte(`{"id":"069a79f444e94726a5befca90e38aaf5","name":"Notch"}`))
		case "jeb_":
			_, _ = w.Write([]byte(`{"id":"853c80ef3c3749fdaa49938b674adae6","name":"jeb_"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	old := minecraftProfileURL
	minecraftProfileURL = srv.URL + "/"
	t.Cleanup(func() { minecraftProfileURL = old })
}

func minecraftPlayersApp(t *testing.T) (*App, http.Handler, string) {
	t.Helper()
	s := minecraftSettings("mc")
	app := newCapabilityTestApp(t, s)
	return app, newTestMux(app), pathsFor(app.serverRoot(s)).Game()
}

func playerAction(t *testing.T, mux http.Handler, server, body string) (int, minecraftPlayerLists, string) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/minecraft/players", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-HSM-Server", server)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out minecraftPlayerLists
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

func listEntries(t *testing.T, gameDir, file string) []map[string]any {
	t.Helper()
	entries, err := readMinecraftList(gameDir, file)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestStoppedServerListsAreEditedWithMojangUUIDs(t *testing.T) {
	fakeMojang(t)
	_, mux, gameDir := minecraftPlayersApp(t)
	writeFile(t, filepath.Join(gameDir, "server.properties"), "op-permission-level=3\n")

	code, out, body := playerAction(t, mux, "mc", `{"action":"whitelist-add","name":"notch"}`)
	if code != 200 || len(out.Whitelist) != 1 || out.Whitelist[0].Name != "Notch" || out.Whitelist[0].UUID != "069a79f4-44e9-4726-a5be-fca90e38aaf5" {
		t.Fatalf("whitelist add = %d %s", code, body)
	}
	if !strings.Contains(out.Message, "next start") && !strings.Contains(out.Message, "starts") {
		t.Errorf("message = %q, want a note that it applies on start", out.Message)
	}
	// Adding again is a no-op.
	playerAction(t, mux, "mc", `{"action":"whitelist-add","name":"Notch"}`)
	if n := len(listEntries(t, gameDir, minecraftWhitelistFile)); n != 1 {
		t.Errorf("whitelist has %d entries after a repeat add", n)
	}

	if _, out, body := playerAction(t, mux, "mc", `{"action":"op","name":"jeb_"}`); len(out.Ops) != 1 || out.Ops[0].Level != 3 {
		t.Fatalf("op = %s", body)
	}
	if _, out, body := playerAction(t, mux, "mc", `{"action":"ban","name":"jeb_","reason":"griefing\nop me"}`); len(out.Bans) != 1 || out.Bans[0].Reason != "griefing op me" || out.Bans[0].Expires != "forever" || out.Bans[0].Source != "Realmkeeper" {
		t.Fatalf("ban = %s", body)
	}
	if _, out, body := playerAction(t, mux, "mc", `{"action":"ban-ip","name":"203.0.113.7"}`); len(out.IPBans) != 1 || out.IPBans[0].IP != "203.0.113.7" {
		t.Fatalf("ban-ip = %s", body)
	}
	for _, action := range []string{`{"action":"pardon","name":"JEB_"}`, `{"action":"deop","name":"jeb_"}`, `{"action":"pardon-ip","name":"203.0.113.7"}`} {
		if code, _, body := playerAction(t, mux, "mc", action); code != 200 {
			t.Fatalf("%s = %d %s", action, code, body)
		}
	}
	if _, out, _ := playerAction(t, mux, "mc", `{"action":"whitelist-remove","name":"NOTCH"}`); len(out.Whitelist)+len(out.Ops)+len(out.Bans)+len(out.IPBans) != 0 {
		t.Errorf("lists not empty after removing everything: %+v", out)
	}
}

func TestPlayerListRewritesKeepUnknownFields(t *testing.T) {
	fakeMojang(t)
	_, mux, gameDir := minecraftPlayersApp(t)
	writeFile(t, filepath.Join(gameDir, minecraftOpsFile), `[{"uuid":"u1","name":"Alex","level":4,"bypassesPlayerLimit":true,"extra":"kept"}]`)
	playerAction(t, mux, "mc", `{"action":"op","name":"Notch"}`)
	entries := listEntries(t, gameDir, minecraftOpsFile)
	if len(entries) != 2 || entries[0]["extra"] != "kept" || entries[0]["bypassesPlayerLimit"] != true {
		t.Errorf("ops.json = %+v", entries)
	}
}

func TestPlayerActionsRejectBadInput(t *testing.T) {
	fakeMojang(t)
	_, mux, gameDir := minecraftPlayersApp(t)
	cases := map[string]string{
		"unknown account": `{"action":"whitelist-add","name":"NoSuchPlayer"}`,
		"command in name": `{"action":"op","name":"Alex\nstop"}`,
		"space in name":   `{"action":"ban","name":"Alex stop"}`,
		"long name":       `{"action":"op","name":"abcdefghijklmnopq"}`,
		"bad address":     `{"action":"ban-ip","name":"example.com"}`,
		"kick stopped":    `{"action":"kick","name":"Notch"}`,
		"unknown action":  `{"action":"stop","name":"Notch"}`,
	}
	for name, body := range cases {
		if code, _, out := playerAction(t, mux, "mc", body); code != 400 {
			t.Errorf("%s: %d %s, want 400", name, code, out)
		}
	}
	if fileExists(filepath.Join(gameDir, minecraftOpsFile)) || fileExists(filepath.Join(gameDir, minecraftWhitelistFile)) {
		t.Error("a refused action wrote a file")
	}
}

type nopWriteCloser struct{ *bytes.Buffer }

func (nopWriteCloser) Close() error { return nil }

func TestRunningServerGetsConsoleCommands(t *testing.T) {
	old := minecraftListUpdateWait
	minecraftListUpdateWait = 50 * time.Millisecond
	t.Cleanup(func() { minecraftListUpdateWait = old })
	app, mux, gameDir := minecraftPlayersApp(t)
	stdin := nopWriteCloser{&bytes.Buffer{}}
	m := app.managers["mc"]
	m.mu.Lock()
	m.running, m.stdin = true, stdin
	m.mu.Unlock()
	t.Cleanup(func() { m.mu.Lock(); m.running, m.stdin = false, nil; m.mu.Unlock() })

	// The server writes ops.json itself; simulate that for the op command.
	writeFile(t, filepath.Join(gameDir, minecraftOpsFile), `[{"uuid":"u","name":"Notch","level":4}]`)
	if _, out, body := playerAction(t, mux, "mc", `{"action":"op","name":"Notch"}`); out.Message != "Done" || !out.Running {
		t.Errorf("op on a running server = %s", body)
	}
	if _, out, _ := playerAction(t, mux, "mc", `{"action":"ban","name":"Notch","reason":"bad\r\nstop"}`); !strings.Contains(out.Message, "console") {
		t.Errorf("ban message = %q, want a note to check the console", out.Message)
	}
	playerAction(t, mux, "mc", `{"action":"kick","name":"Notch","reason":"afk"}`)
	if code, out, body := playerAction(t, mux, "mc", `{"action":"whitelist-on"}`); code != 200 || !out.WhitelistEnabled {
		t.Fatalf("whitelist-on = %d %s", code, body)
	}
	if !storedServer(app, "mc").WhitelistEnabled {
		t.Error("whitelist setting was not saved")
	}
	want := "op Notch\nban Notch bad stop\nkick Notch afk\nwhitelist on\n"
	if got := stdin.String(); got != want {
		t.Errorf("commands = %q, want %q", got, want)
	}
	if fileExists(filepath.Join(gameDir, minecraftBansFile)) {
		t.Error("the app wrote banned-players.json while the server was running")
	}
}

func TestPlayerManagementIsMinecraftOnly(t *testing.T) {
	app := newCapabilityTestApp(t, testServer("hy", "hytale"))
	mux := newTestMux(app)
	if code, _, _ := playerAction(t, mux, "hy", `{"action":"op","name":"Notch"}`); code != 409 {
		t.Errorf("player action on Hytale = %d, want 409", code)
	}
	req := httptest.NewRequest("GET", "/api/minecraft/players", nil)
	req.Header.Set("X-HSM-Server", "hy")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 409 {
		t.Errorf("GET on Hytale = %d, want 409", rec.Code)
	}
}

func TestPlayerListsReportBadFiles(t *testing.T) {
	_, mux, gameDir := minecraftPlayersApp(t)
	writeFile(t, filepath.Join(gameDir, minecraftBansFile), "not json")
	req := httptest.NewRequest("GET", "/api/minecraft/players", nil)
	req.Header.Set("X-HSM-Server", "mc")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), "banned-players.json") {
		t.Errorf("bad file = %d %s", rec.Code, rec.Body.String())
	}
	if err := os.Remove(filepath.Join(gameDir, minecraftBansFile)); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req.Clone(req.Context()))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"bans":[]`) {
		t.Errorf("empty lists = %d %s", rec.Code, rec.Body.String())
	}
}
