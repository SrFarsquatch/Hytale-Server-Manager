package main

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"
)

func logLines(lines ...string) []LogLine {
	out := make([]LogLine, len(lines))
	for i, line := range lines {
		out[i] = LogLine{Source: "server", Line: line}
	}
	return out
}

func TestParseMinecraftList(t *testing.T) {
	cases := []struct {
		name    string
		lines   []LogLine
		players []string
		max     int
		known   bool
	}{
		{"modern", logLines("[12:00:01] [Server thread/INFO]: There are 2 of a max of 20 players online: Alex, Steve"), []string{"Alex", "Steve"}, 20, true},
		{"empty", logLines("[Server thread/INFO]: There are 0 of a max of 10 players online:"), []string{}, 10, true},
		{"legacy names on next line", logLines("[12:00:01 INFO]: There are 1/20 players online:", "[12:00:01 INFO]: Notch"), []string{"Notch"}, 20, true},
		{"count without names yet", logLines("There are 3 of a max of 20 players online:"), nil, 20, false},
		{"unrelated output", logLines("[Server thread/INFO]: Saving the game"), nil, 0, false},
		{"ansi colours", logLines("\x1b[32mThere are 1 of a max of 5 players online: Herobrine\x1b[0m"), []string{"Herobrine"}, 5, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			players, max, known := parseMinecraftList(tc.lines)
			if known != tc.known || max != tc.max || !reflect.DeepEqual(players, tc.players) {
				t.Errorf("got %#v max %d known %v; want %#v max %d known %v", players, max, known, tc.players, tc.max, tc.known)
			}
		})
	}
}

func TestMinecraftPlayersEndpointUsesListCommand(t *testing.T) {
	app, m := minecraftTestServer(t, true)
	req := httptest.NewRequest("GET", "/api/players", nil)
	req.Header.Set("X-HSM-Server", "mc")
	rec := httptest.NewRecorder()
	newTestMux(app).ServeHTTP(rec, req)
	var body struct {
		Known      bool     `json:"known"`
		Count      int      `json:"count"`
		MaxPlayers int      `json:"maxPlayers"`
		Players    []string `json:"players"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Known || body.Count != 2 || body.MaxPlayers != 20 || !reflect.DeepEqual(body.Players, []string{"Alex", "Steve"}) {
		t.Fatalf("players = %s", rec.Body.String())
	}
	if logContains(m, "got /who", m.startedAt.AddDate(-1, 0, 0)) {
		t.Error("Hytale's /who was sent to a Minecraft server")
	}
}

func TestParseHytaleWho(t *testing.T) {
	cases := []struct {
		name    string
		lines   []LogLine
		players []string
		known   bool
	}{
		// Captured from a live Hytale server.
		{"live sample", logLines("GrandMine (1): : Clarkonalark (Clarkonalark)"), []string{"Clarkonalark"}, true},
		{"with log prefix", logLines("[2026/10/14 15:03:34   INFO]          [CommandManager] Console executed command: who", "[2026/10/14 15:03:34   INFO] [Universe] GrandMine (1): : Clarkonalark (Clarkonalark)"), []string{"Clarkonalark"}, true},
		{"several players", logLines("GrandMine (2): : Alice (alice), Bob (bob_b)"), []string{"Alice", "Bob"}, true},
		{"several worlds", logLines("GrandMine (1): : Alice (alice)", "Nether (2): : Bob (bob), Cara (cara)"), []string{"Alice", "Bob", "Cara"}, true},
		{"empty world", logLines("GrandMine (0): :"), []string{}, true},
		{"empty and populated worlds", logLines("Lobby (0): :", "GrandMine (1): : Alice (alice)"), []string{"Alice"}, true},
		{"command echo only", logLines("[CommandManager] Console executed command: who"), nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			players, _, known := parseHytaleWho(tc.lines)
			if known != tc.known || !reflect.DeepEqual(players, tc.players) {
				t.Errorf("got %#v known %v; want %#v known %v", players, known, tc.players, tc.known)
			}
		})
	}
}

func TestHytalePlayersEndpointCountsEveryWorld(t *testing.T) {
	root := fakeHytaleInstall(t)
	script := "#!/bin/bash\necho ready\nwhile read -r line; do\n" +
		"  if [ \"$line\" = \"/who\" ]; then echo 'GrandMine (1): : Clarkonalark (Clarkonalark)'; sleep 0.1; echo 'Arena (2): : Alice (alice), Bob (bob)'; fi\n" +
		"done\n"
	writeFile(t, filepath.Join(root, "game", "start.sh"), script)
	s := defaultServerSettings("hy", "Hytale", 5520, "")
	app := newCapabilityTestApp(t, s)
	m := NewServerManager(root, t.TempDir(), "hytale")
	app.managers["hy"] = m
	if err := m.Start(nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-currentPID(m), syscall.SIGKILL) })
	waitFor(t, "fake Hytale ready", func() bool { return logContains(m, "ready", time.Time{}) })

	req := httptest.NewRequest("GET", "/api/players", nil)
	req.Header.Set("X-HSM-Server", "hy")
	rec := httptest.NewRecorder()
	newTestMux(app).ServeHTTP(rec, req)
	var body struct {
		Known      bool     `json:"known"`
		Count      int      `json:"count"`
		MaxPlayers int      `json:"maxPlayers"`
		Players    []string `json:"players"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Known || body.Count != 3 || body.MaxPlayers != s.MaxPlayers || !reflect.DeepEqual(body.Players, []string{"Clarkonalark", "Alice", "Bob"}) {
		t.Fatalf("players = %s", rec.Body.String())
	}
}
