package main

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"
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
