package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func propertiesRequest(t *testing.T, h http.Handler, server, method, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, "/api/minecraft/properties", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-HSM-Server", server)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out == nil {
		out = map[string]any{"raw": rec.Body.String()}
	}
	return rec.Code, out
}

func TestMinecraftPropertiesFormReadsDefaultsAndMergesEdits(t *testing.T) {
	app := newCapabilityTestApp(t, testServer("mc", "minecraft-java"), testServer("hy", "hytale"))
	h := newTestMux(app)
	gameDir := filepath.Join(app.managers["mc"].rootDir, "game")
	writeFile(t, filepath.Join(gameDir, "server.properties"), "#Minecraft server properties\nmotd=Hello\ndifficulty=hard\ncustom-key=keep-me\n")

	code, out := propertiesRequest(t, h, "mc", "GET", "")
	values, _ := out["values"].(map[string]any)
	if code != 200 || values["difficulty"] != "hard" || values["view-distance"] != "10" {
		t.Fatalf("GET = %d %v", code, out)
	}

	code, out = propertiesRequest(t, h, "mc", "PUT", `{"values":{"difficulty":"peaceful","view-distance":"16","pvp":"false"}}`)
	if code != 200 {
		t.Fatalf("PUT = %d %v", code, out)
	}
	b, _ := os.ReadFile(filepath.Join(gameDir, "server.properties"))
	text := string(b)
	for _, want := range []string{"difficulty=peaceful", "view-distance=16", "pvp=false", "motd=Hello", "custom-key=keep-me", "#Minecraft server properties"} {
		if !strings.Contains(text, want) {
			t.Errorf("server.properties missing %q:\n%s", want, text)
		}
	}
}

func TestMinecraftPropertiesRejectsInvalidAndManagedKeys(t *testing.T) {
	app := newCapabilityTestApp(t, testServer("mc", "minecraft-java"), testServer("hy", "hytale"))
	h := newTestMux(app)
	for _, body := range []string{
		`{"values":{"view-distance":"99"}}`,
		`{"values":{"difficulty":"impossible"}}`,
		`{"values":{"pvp":"yes"}}`,
		`{"values":{"level-seed":"abc\ninjected=true"}}`,
		`{"values":{"level-name":"../other"}}`,
		`{"values":{"server-port":"25565"}}`,
		`{"values":{"online-mode":"false"}}`,
	} {
		if code, out := propertiesRequest(t, h, "mc", "PUT", body); code != 400 {
			t.Errorf("PUT %s = %d %v, want 400", body, code, out)
		}
	}
	if _, err := os.Stat(filepath.Join(app.managers["mc"].rootDir, "game", "server.properties")); !os.IsNotExist(err) {
		t.Error("a rejected update still wrote server.properties")
	}
	if code, _ := propertiesRequest(t, h, "hy", "GET", ""); code != 409 {
		t.Errorf("Hytale server.properties GET = %d, want 409", code)
	}
}
