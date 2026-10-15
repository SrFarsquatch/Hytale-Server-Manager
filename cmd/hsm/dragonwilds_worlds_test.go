package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type worldsResponse struct {
	SaveDir          string               `json:"saveDir"`
	DefaultWorldName string               `json:"defaultWorldName"`
	Worlds           []dragonwildsWorld   `json:"worlds"`
	Archives         []dragonwildsArchive `json:"archives"`
}

// dragonwildsWorldsApp is an installed Dragonwilds server with two saves:
// Old Vale (older) and New Vale (newer, so it loads next).
func dragonwildsWorldsApp(t *testing.T) (*App, http.Handler, string) {
	t.Helper()
	s := dragonwildsSettings("dw")
	app := newCapabilityTestApp(t, s)
	root := app.serverRoot(s)
	writeFile(t, filepath.Join(root, "game", "RSDragonwildsServer.sh"), "#!/bin/sh\n")
	saves := filepath.Join(dragonwildsSavedDir(root), "SaveGames")
	writeFile(t, filepath.Join(saves, "Old Vale.sav"), "old")
	writeFile(t, filepath.Join(saves, "Old Vale.backup"), "old-backup")
	writeFile(t, filepath.Join(saves, "New Vale.sav"), "new")
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(saves, "Old Vale.sav"), past, past)
	return app, newTestMux(app), root
}

func worldsCall(t *testing.T, mux http.Handler, method, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-HSM-Server", "dw")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func listWorlds(t *testing.T, mux http.Handler) worldsResponse {
	t.Helper()
	code, body := worldsCall(t, mux, "GET", "/api/worlds", "")
	var out worldsResponse
	if code != 200 || json.Unmarshal([]byte(body), &out) != nil {
		t.Fatalf("GET /api/worlds = %d %s", code, body)
	}
	return out
}

func loadsNext(r worldsResponse) string {
	for _, w := range r.Worlds {
		if w.LoadsNext {
			return w.Name
		}
	}
	return ""
}

func TestWorldsListShowsWhichSaveLoadsNext(t *testing.T) {
	_, mux, _ := dragonwildsWorldsApp(t)
	got := listWorlds(t, mux)
	if got.SaveDir != "game/RSDragonwilds/Saved/SaveGames" || len(got.Worlds) != 2 {
		t.Fatalf("worlds = %+v", got)
	}
	if loadsNext(got) != "New Vale" {
		t.Errorf("loads next = %q, want the newest save", loadsNext(got))
	}
	if w := got.Worlds[1]; w.Name != "Old Vale" || !w.HasBackup || w.Path != "game/RSDragonwilds/Saved/SaveGames/Old Vale.sav" {
		t.Errorf("Old Vale = %+v", w)
	}
}

func TestWorldsSaveDirFollowsTheExistingFolder(t *testing.T) {
	root := t.TempDir()
	if got := dragonwildsSaveDir(root); filepath.Base(got) != "SaveGames" {
		t.Errorf("default = %s", got)
	}
	writeFile(t, filepath.Join(dragonwildsSavedDir(root), "Savegames", "World.sav"), "x")
	if got := dragonwildsSaveDir(root); filepath.Base(got) != "Savegames" {
		t.Errorf("with saves in Savegames = %s", got)
	}
}

func TestUseWorldMakesItLoadNextAndSetsTheWorldName(t *testing.T) {
	app, mux, root := dragonwildsWorldsApp(t)
	if code, body := worldsCall(t, mux, "POST", "/api/worlds/use", `{"name":"Old Vale"}`); code != 200 {
		t.Fatalf("use = %d %s", code, body)
	}
	if got := listWorlds(t, mux); loadsNext(got) != "Old Vale" || got.DefaultWorldName != "Old Vale" {
		t.Errorf("after use: loads next %q, default %q", loadsNext(got), got.DefaultWorldName)
	}
	if storedServer(app, "dw").DragonwildsWorldName != "Old Vale" {
		t.Error("world name was not saved in the server settings")
	}
	ini, _ := os.ReadFile(filepath.Join(dragonwildsSavedDir(root), "Config", "LinuxServer", "DedicatedServer.ini"))
	if !strings.Contains(string(ini), "DefaultWorldName=Old Vale") {
		t.Errorf("DedicatedServer.ini = %s", ini)
	}
	if code, _ := worldsCall(t, mux, "POST", "/api/worlds/use", `{"name":"Missing"}`); code != 404 {
		t.Errorf("unknown world = %d, want 404", code)
	}
	if code, _ := worldsCall(t, mux, "POST", "/api/worlds/use", `{"name":"../../etc/passwd"}`); code != 400 {
		t.Errorf("path in name = %d, want 400", code)
	}
}

func uploadWorld(t *testing.T, mux http.Handler, saveName, backupName string) (int, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", saveName)
	_, _ = fw.Write([]byte("uploaded"))
	if backupName != "" {
		bw, _ := mw.CreateFormFile("backup", backupName)
		_, _ = bw.Write([]byte("uploaded-backup"))
	}
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/api/worlds/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-HSM-Server", "dw")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestUploadWorldKeepsThePreviousCopyAndLoadsTheUpload(t *testing.T) {
	_, mux, root := dragonwildsWorldsApp(t)
	saves := filepath.Join(dragonwildsSavedDir(root), "SaveGames")
	if code, body := uploadWorld(t, mux, "Old Vale.sav", "Old Vale.backup"); code != 201 {
		t.Fatalf("upload = %d %s", code, body)
	}
	if b, _ := os.ReadFile(filepath.Join(saves, "Old Vale.sav")); string(b) != "uploaded" {
		t.Errorf("save = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(saves, "Old Vale.backup")); string(b) != "uploaded-backup" {
		t.Errorf("backup = %q", b)
	}
	got := listWorlds(t, mux)
	if loadsNext(got) != "Old Vale" || got.DefaultWorldName != "Old Vale" {
		t.Errorf("after upload: loads next %q, default %q", loadsNext(got), got.DefaultWorldName)
	}
	if len(got.Archives) != 1 || !reflect.DeepEqual(got.Archives[0].Files, []string{"Old Vale.backup", "Old Vale.sav"}) {
		t.Fatalf("archives = %+v", got.Archives)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "world-backups", got.Archives[0].ID, "Old Vale.sav")); string(b) != "old" {
		t.Errorf("archived copy = %q", b)
	}

	for name, want := range map[string]int{"world.txt": 400, ".sav": 400} {
		if code, _ := uploadWorld(t, mux, name, ""); code != want {
			t.Errorf("upload %q = %d, want %d", name, code, want)
		}
	}
}

func TestArchiveRestoreAndNewWorld(t *testing.T) {
	app, mux, root := dragonwildsWorldsApp(t)
	saves := filepath.Join(dragonwildsSavedDir(root), "SaveGames")
	if code, body := worldsCall(t, mux, "POST", "/api/worlds/archive", `{"name":"Old Vale"}`); code != 200 {
		t.Fatalf("archive = %d %s", code, body)
	}
	if fileExists(filepath.Join(saves, "Old Vale.sav")) || fileExists(filepath.Join(saves, "Old Vale.backup")) {
		t.Error("archived world is still in the save folder")
	}
	archive := listWorlds(t, mux).Archives[0].ID
	if code, body := worldsCall(t, mux, "POST", "/api/worlds/restore", `{"name":"Old Vale","archive":"`+archive+`"}`); code != 200 {
		t.Fatalf("restore = %d %s", code, body)
	}
	if got := listWorlds(t, mux); loadsNext(got) != "Old Vale" || len(got.Worlds) != 2 {
		t.Errorf("after restore: %+v", got)
	}
	if code, _ := worldsCall(t, mux, "POST", "/api/worlds/restore", `{"name":"Old Vale","archive":"../x"}`); code != 400 {
		t.Errorf("bad archive id = %d, want 400", code)
	}

	if code, body := worldsCall(t, mux, "POST", "/api/worlds/new", `{"name":"Fresh Start"}`); code != 200 {
		t.Fatalf("new = %d %s", code, body)
	}
	got := listWorlds(t, mux)
	if len(got.Worlds) != 0 || got.DefaultWorldName != "Fresh Start" || storedServer(app, "dw").DragonwildsWorldName != "Fresh Start" {
		t.Errorf("after new world: %+v", got)
	}
	if code, _ := worldsCall(t, mux, "POST", "/api/worlds/new", `{"name":"`+strings.Repeat("x", 33)+`"}`); code != 400 {
		t.Errorf("33-character name = %d, want 400", code)
	}
}

func TestWorldChangesWaitForTheServerToStop(t *testing.T) {
	app, mux, _ := dragonwildsWorldsApp(t)
	m := app.managers["dw"]
	m.mu.Lock()
	m.running = true
	m.mu.Unlock()
	t.Cleanup(func() { m.mu.Lock(); m.running = false; m.mu.Unlock() })
	if code, body := worldsCall(t, mux, "POST", "/api/worlds/use", `{"name":"Old Vale"}`); code != 409 || !strings.Contains(body, "stop the server") {
		t.Errorf("use while running = %d %s", code, body)
	}
	if code, _ := uploadWorld(t, mux, "Old Vale.sav", ""); code != 409 {
		t.Errorf("upload while running = %d, want 409", code)
	}
	listWorlds(t, mux) // listing still works while running
}

func TestDragonwildsSnapshotsCoverSavesAndConfig(t *testing.T) {
	spec, ok := snapshotSpecFor("/r", dragonwildsSettings("dw"))
	if !ok || spec.Base != filepath.Join("/r", "game", "RSDragonwilds", "Saved") || !reflect.DeepEqual(spec.Include, []string{"SaveGames", "Savegames", "Config"}) {
		t.Fatalf("spec = %+v, %v", spec, ok)
	}
}
