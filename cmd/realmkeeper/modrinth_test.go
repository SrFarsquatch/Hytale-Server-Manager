package main

import (
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type fakeModrinth struct {
	srv      *httptest.Server
	mu       sync.Mutex
	searches []url.Values
	files    map[string][]byte
}

func sha512Hex(b []byte) string {
	sum := sha512.Sum512(b)
	return hex.EncodeToString(sum[:])
}

// newFakeModrinth serves three projects for Fabric 1.21.1:
//   - sodium: a 0.6.0 release and a newer 0.7.0 beta; needs fabric-api
//   - fabric-api: a release; its 1.20.1 build is what lithium names exactly
//   - lithium: requires fabric-api version "fapi-old" (for 1.20.1)
func newFakeModrinth(t *testing.T) *fakeModrinth {
	t.Helper()
	f := &fakeModrinth{files: map[string][]byte{
		"sodium-0.6.0.jar":     []byte("sodium 0.6.0"),
		"sodium-0.5.0.jar":     []byte("sodium 0.5.0"),
		"sodium-0.7.0.jar":     []byte("sodium 0.7.0 beta"),
		"fabric-api-0.100.jar": []byte("fabric api"),
		"fabric-api-old.jar":   []byte("fabric api old"),
		"lithium-1.0.jar":      []byte("lithium"),
		"broken.jar":           []byte("not what the hash says"),
	}}
	var versions map[string][]map[string]any
	var byID map[string]map[string]any
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case path == "/v2/search":
			f.mu.Lock()
			f.searches = append(f.searches, r.URL.Query())
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"total_hits": 2, "hits": []any{
				map[string]any{"project_id": "sodium", "slug": "sodium", "title": "Sodium", "description": "Fast", "author": "jelly", "downloads": 100, "icon_url": "https://cdn.modrinth.com/icon.png"},
				map[string]any{"project_id": "lithium", "slug": "lithium", "title": "Lithium", "icon_url": "https://evil.example/icon.png"},
			}})
		case strings.HasPrefix(path, "/v2/project/") && strings.HasSuffix(path, "/version"):
			project := strings.TrimSuffix(strings.TrimPrefix(path, "/v2/project/"), "/version")
			if !strings.Contains(r.URL.Query().Get("loaders"), "fabric") || r.URL.Query().Get("game_versions") != `["1.21.1"]` {
				t.Errorf("version filter = %v", r.URL.Query())
			}
			_ = json.NewEncoder(w).Encode(versions[project])
		case strings.HasPrefix(path, "/v2/project/"):
			_ = json.NewEncoder(w).Encode(map[string]string{"title": strings.ToUpper(strings.TrimPrefix(path, "/v2/project/"))})
		case strings.HasPrefix(path, "/v2/version/"):
			v, ok := byID[strings.TrimPrefix(path, "/v2/version/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(v)
		case path == "/v2/version_files/update":
			var body struct{ Hashes []string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			out := map[string]any{}
			for _, h := range body.Hashes {
				if h == sha512Hex(f.files["sodium-0.5.0.jar"]) {
					out[h] = versions["sodium"][1]
				}
				if h == sha512Hex(f.files["fabric-api-0.100.jar"]) {
					out[h] = versions["fabric-api"][0] // already the newest
				}
			}
			_ = json.NewEncoder(w).Encode(out)
		case strings.HasPrefix(path, "/cdn/"):
			_, _ = w.Write(f.files[strings.TrimPrefix(path, "/cdn/")])
		default:
			http.NotFound(w, r)
		}
	}))
	version := func(id, project, number, kind, date, file string, game string, deps ...map[string]any) map[string]any {
		hash := sha512Hex(f.files[file])
		if file == "broken.jar" {
			hash = strings.Repeat("a", 128)
		}
		if deps == nil {
			deps = []map[string]any{}
		}
		return map[string]any{
			"id": id, "project_id": project, "name": project + " " + number, "version_number": number, "version_type": kind,
			"date_published": date, "game_versions": []string{game}, "loaders": []string{"fabric"},
			"files":        []any{map[string]any{"url": f.srv.URL + "/cdn/" + file, "filename": file, "primary": true, "size": len(f.files[file]), "hashes": map[string]string{"sha512": hash}}},
			"dependencies": deps,
		}
	}
	dep := func(project, version, kind string) map[string]any {
		return map[string]any{"project_id": project, "version_id": version, "dependency_type": kind}
	}
	versions = map[string][]map[string]any{
		"sodium": {
			version("sod7", "sodium", "0.7.0", "beta", "2026-09-02", "sodium-0.7.0.jar", "1.21.1", dep("fabric-api", "", "required")),
			version("sod6", "sodium", "0.6.0", "release", "2026-08-01", "sodium-0.6.0.jar", "1.21.1", dep("fabric-api", "", "required"), dep("modmenu", "", "optional")),
		},
		"fabric-api": {version("fapi", "fabric-api", "0.100", "release", "2026-08-01", "fabric-api-0.100.jar", "1.21.1")},
		"lithium":    {version("lith", "lithium", "1.0", "release", "2026-08-01", "lithium-1.0.jar", "1.21.1", dep("fabric-api", "fapi-old", "required"))},
		"broken":     {version("brk", "broken", "1.0", "release", "2026-08-01", "broken.jar", "1.21.1")},
	}
	byID = map[string]map[string]any{"fapi-old": version("fapi-old", "fabric-api", "0.90", "release", "2024-01-01", "fabric-api-old.jar", "1.20.1")}
	for _, list := range versions {
		for _, v := range list {
			byID[v["id"].(string)] = v
		}
	}
	t.Cleanup(f.srv.Close)
	oldAPI, oldTrust := modrinthAPI, trustedModrinthDownload
	modrinthAPI = f.srv.URL + "/v2"
	trustedModrinthDownload = func(raw string) bool { return strings.HasPrefix(raw, f.srv.URL+"/cdn/") }
	t.Cleanup(func() { modrinthAPI, trustedModrinthDownload = oldAPI, oldTrust })
	return f
}

// fabricServer is an installed Fabric 1.21.1 server.
func fabricServer(t *testing.T) (*App, http.Handler, string, string) {
	t.Helper()
	s := minecraftSettings("fab")
	s.MinecraftVariant, s.MinecraftVersion = "fabric", "latest"
	app := newCapabilityTestApp(t, s)
	game := pathsFor(app.serverRoot(s)).Game()
	if err := os.MkdirAll(game, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeMinecraftInstallInfo(game, minecraftInstallInfo{Version: "1.21.1", Variant: "fabric"}); err != nil {
		t.Fatal(err)
	}
	return app, newTestMux(app), game, filepath.Join(game, "mods")
}

func modrinthCall(t *testing.T, mux http.Handler, server, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-HSM-Server", server)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestModrinthSearchIsLimitedToTheServer(t *testing.T) {
	f := newFakeModrinth(t)
	_, mux, game, mods := fabricServer(t)
	writeFile(t, filepath.Join(mods, "lithium-1.0.jar"), "x")
	if err := writeModrinthInstalls(game, map[string]modrinthInstall{"lithium": {ProjectID: "lithium", Filename: "lithium-1.0.jar"}}); err != nil {
		t.Fatal(err)
	}
	code, out := modrinthCall(t, mux, "fab", "GET", "/api/modrinth/search?q=fast", "")
	if code != 200 || out["gameVersion"] != "1.21.1" {
		t.Fatalf("search = %d %v", code, out)
	}
	facets := f.searches[0].Get("facets")
	for _, want := range []string{`"categories:fabric"`, `"versions:1.21.1"`, `"server_side:required"`, `"project_type:mod"`} {
		if !strings.Contains(facets, want) {
			t.Errorf("facets %s lack %s", facets, want)
		}
	}
	if f.searches[0].Get("query") != "fast" {
		t.Errorf("query = %v", f.searches[0])
	}
	hits := out["hits"].([]any)
	sodium, lithium := hits[0].(map[string]any), hits[1].(map[string]any)
	if sodium["installed"] != false || lithium["installed"] != true {
		t.Errorf("installed flags = %v / %v", sodium["installed"], lithium["installed"])
	}
	if sodium["icon_url"] != "https://cdn.modrinth.com/icon.png" || lithium["icon_url"] != "" {
		t.Errorf("icons = %v / %v; icons off Modrinth must be dropped", sodium["icon_url"], lithium["icon_url"])
	}
}

func TestModrinthInstallPicksAReleaseAndItsRequiredDependencies(t *testing.T) {
	newFakeModrinth(t)
	_, mux, game, mods := fabricServer(t)
	code, out := modrinthCall(t, mux, "fab", "POST", "/api/modrinth/install", `{"projectId":"sodium"}`)
	if code != 200 {
		t.Fatalf("install = %d %v", code, out)
	}
	if msg := out["message"].(string); !strings.Contains(msg, "SODIUM 0.6.0") || !strings.Contains(msg, "1 required dependency") {
		t.Errorf("message = %q", msg)
	}
	if b, _ := os.ReadFile(filepath.Join(mods, "sodium-0.6.0.jar")); string(b) != "sodium 0.6.0" {
		t.Errorf("sodium = %q; want the release, not the newer beta", b)
	}
	if !fileExists(filepath.Join(mods, "fabric-api-0.100.jar")) {
		t.Error("the required dependency was not installed")
	}
	installs := readModrinthInstalls(game)
	if installs["sodium"].VersionID != "sod6" || installs["fabric-api"].Filename != "fabric-api-0.100.jar" || len(installs) != 2 {
		t.Errorf("tracked installs = %+v", installs)
	}
	entries, _ := os.ReadDir(mods)
	if len(entries) != 2 {
		t.Errorf("mods folder = %v; the optional dependency must not be installed and no temp files left", entries)
	}

	// Lithium names a fabric-api build for 1.20.1, which is already covered.
	if code, out := modrinthCall(t, mux, "fab", "POST", "/api/modrinth/install", `{"projectId":"lithium"}`); code != 200 || strings.Contains(out["message"].(string), "dependenc") {
		t.Errorf("lithium = %d %v; fabric-api is already installed", code, out)
	}
}

func TestModrinthDependencyFallsBackToACompatibleVersion(t *testing.T) {
	newFakeModrinth(t)
	_, mux, _, mods := fabricServer(t)
	if code, out := modrinthCall(t, mux, "fab", "POST", "/api/modrinth/install", `{"projectId":"lithium"}`); code != 200 {
		t.Fatalf("install = %d %v", code, out)
	}
	if fileExists(filepath.Join(mods, "fabric-api-old.jar")) || !fileExists(filepath.Join(mods, "fabric-api-0.100.jar")) {
		t.Error("the dependency's 1.20.1 build was installed instead of a 1.21.1 one")
	}
}

func TestModrinthRefusesBadDownloads(t *testing.T) {
	newFakeModrinth(t)
	_, mux, _, mods := fabricServer(t)
	if code, out := modrinthCall(t, mux, "fab", "POST", "/api/modrinth/install", `{"projectId":"broken"}`); code != 502 || !strings.Contains(out["error"].(string), "SHA-512") {
		t.Errorf("bad checksum = %d %v", code, out)
	}
	if code, _ := modrinthCall(t, mux, "fab", "POST", "/api/modrinth/install", `{"projectId":"sodium","versionId":"fapi-old"}`); code != 502 {
		t.Errorf("an incompatible version = %d, want 502", code)
	}
	trustedModrinthDownload = func(string) bool { return false }
	if code, _ := modrinthCall(t, mux, "fab", "POST", "/api/modrinth/install", `{"projectId":"sodium"}`); code != 502 {
		t.Errorf("an untrusted download = %d, want 502", code)
	}
	for _, body := range []string{`{"projectId":"../x"}`, `{"projectId":"sodium","replace":"../server.jar"}`} {
		if code, _ := modrinthCall(t, mux, "fab", "POST", "/api/modrinth/install", body); code != 400 {
			t.Errorf("%s = %d, want 400", body, code)
		}
	}
	if entries, _ := os.ReadDir(mods); len(entries) != 0 {
		t.Errorf("files left after refused installs: %v", entries)
	}
}

func TestModrinthUpdatesFindUploadedJarsAndReplaceThem(t *testing.T) {
	f := newFakeModrinth(t)
	_, mux, _, mods := fabricServer(t)
	writeFile(t, filepath.Join(mods, "sodium-0.5.0.jar"), string(f.files["sodium-0.5.0.jar"]))
	writeFile(t, filepath.Join(mods, "fabric-api-0.100.jar"), string(f.files["fabric-api-0.100.jar"]))
	writeFile(t, filepath.Join(mods, "private.jar"), "not on modrinth")
	code, out := modrinthCall(t, mux, "fab", "GET", "/api/modrinth/updates", "")
	if code != 200 || out["checked"].(float64) != 3 {
		t.Fatalf("updates = %d %v", code, out)
	}
	updates := out["updates"].([]any)
	if len(updates) != 1 {
		t.Fatalf("updates = %v; only sodium is out of date", updates)
	}
	u := updates[0].(map[string]any)
	if u["file"] != "sodium-0.5.0.jar" || u["versionId"] != "sod6" || u["versionNumber"] != "0.6.0" {
		t.Errorf("update = %v", u)
	}
	if code, out := modrinthCall(t, mux, "fab", "POST", "/api/modrinth/install", `{"projectId":"sodium","versionId":"sod6","replace":"sodium-0.5.0.jar"}`); code != 200 {
		t.Fatalf("update install = %d %v", code, out)
	}
	if fileExists(filepath.Join(mods, "sodium-0.5.0.jar")) || !fileExists(filepath.Join(mods, "sodium-0.6.0.jar")) || !fileExists(filepath.Join(mods, "private.jar")) {
		t.Error("the update did not replace exactly the old file")
	}
}

func TestModBrowserNeedsALoaderServer(t *testing.T) {
	vanilla := minecraftSettings("van")
	paper := minecraftSettings("pap")
	paper.MinecraftVariant = "paper"
	app := newCapabilityTestApp(t, vanilla, paper, testServer("hy", "hytale"))
	mux := newTestMux(app)
	for _, id := range []string{"van", "hy"} {
		if code, _ := modrinthCall(t, mux, id, "GET", "/api/modrinth/search", ""); code != 409 {
			t.Errorf("%s search = %d, want 409", id, code)
		}
	}
	if !serverCapabilities(paper).ModBrowser || serverCapabilities(vanilla).ModBrowser {
		t.Error("ModBrowser should be on for Paper and off for vanilla")
	}
	if tgt, ok := app.modrinthTargetFor(paper); !ok || strings.Join(tgt.Loaders, ",") != "paper,spigot,bukkit" || filepath.Base(tgt.Dir) != "plugins" {
		t.Errorf("Paper target = %+v", tgt)
	}
}
