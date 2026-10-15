package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// testJar is a minimal jar: a zip with a manifest.
func testJar(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("META-INF/MANIFEST.MF")
	_, _ = w.Write([]byte("Manifest-Version: 1.0\n"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeMinecraftServices stands in for PaperMC Fill, Fabric Meta and Mojang's
// manifest. Paper 1.21.8 has only an ALPHA build; 1.21.7 has a stable one.
func fakeMinecraftServices(t *testing.T) (jar []byte, agents *[]string) {
	t.Helper()
	jar = testJar(t)
	sum := sha256.Sum256(jar)
	seen := []string{}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("User-Agent"))
		build := func(id int, channel string) map[string]any {
			return map[string]any{"id": id, "channel": channel, "downloads": map[string]any{"server:default": map[string]any{
				"name": "paper.jar", "url": srv.URL + "/paper.jar", "checksums": map[string]string{"sha256": hex.EncodeToString(sum[:])},
			}}}
		}
		switch r.URL.Path {
		case "/paper":
			_ = json.NewEncoder(w).Encode(map[string]any{"versions": map[string][]string{"1.21": {"1.21.9-pre1", "1.21.8", "1.21.7"}, "1.20": {"1.20.6"}}})
		case "/paper/versions/1.21.8/builds":
			_ = json.NewEncoder(w).Encode([]any{build(3, "ALPHA")})
		case "/paper/versions/1.21.7/builds":
			_ = json.NewEncoder(w).Encode([]any{build(10, "STABLE"), build(12, "STABLE"), build(13, "BETA")})
		case "/paper.jar", "/fabric/versions/loader/1.21.8/0.17.2/1.1.0/server/jar":
			_, _ = w.Write(jar)
		case "/fabric/versions/game":
			_, _ = w.Write([]byte(`[{"version":"26.1-snapshot-2","stable":false},{"version":"1.21.8","stable":true},{"version":"1.21.7","stable":true}]`))
		case "/fabric/versions/loader":
			_, _ = w.Write([]byte(`[{"version":"0.18.0-beta","stable":false},{"version":"0.17.2","stable":true}]`))
		case "/fabric/versions/installer":
			_, _ = w.Write([]byte(`[{"version":"1.1.0","stable":true}]`))
		case "/mojang":
			// No releases: the Java requirement lookup fails and is skipped.
			_, _ = w.Write([]byte(`{"latest":{"release":"1.21.8"},"versions":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	oldPaper, oldFabric, oldMojang, oldTrust := paperProjectURL, fabricMetaURL, minecraftVersionManifestURL, trustedPaperDownload
	paperProjectURL, fabricMetaURL, minecraftVersionManifestURL = srv.URL+"/paper", srv.URL+"/fabric", srv.URL+"/mojang"
	trustedPaperDownload = func(raw string) bool { return strings.HasPrefix(raw, srv.URL+"/") }
	t.Cleanup(func() {
		paperProjectURL, fabricMetaURL, minecraftVersionManifestURL, trustedPaperDownload = oldPaper, oldFabric, oldMojang, oldTrust
	})
	return jar, &seen
}

func TestResolvePaperPicksTheNewestStableBuild(t *testing.T) {
	_, agents := fakeMinecraftServices(t)
	ctx, client := context.Background(), http.DefaultClient
	v, u, sha, err := resolvePaper(ctx, client, "latest")
	if err != nil || v != "1.21.7" || !strings.HasSuffix(u, "/paper.jar") || len(sha) != 64 {
		t.Fatalf("latest = %q %q %q %v; want 1.21.7, skipping the pre-release and alpha-only 1.21.8", v, u, sha, err)
	}
	if _, _, _, err := resolvePaper(ctx, client, "1.21.8"); err == nil || !strings.Contains(err.Error(), "no stable build") {
		t.Errorf("1.21.8 = %v, want no stable build", err)
	}
	if _, _, _, err := resolvePaper(ctx, client, "1.8.8"); err == nil || !strings.Contains(err.Error(), "does not publish") {
		t.Errorf("1.8.8 = %v, want unknown version", err)
	}
	for _, ua := range *agents {
		if !strings.HasPrefix(ua, "Realmkeeper/") {
			t.Errorf("request sent User-Agent %q", ua)
		}
	}
	trustedPaperDownload = func(string) bool { return false }
	if _, _, _, err := resolvePaper(ctx, client, "latest"); err == nil || !strings.Contains(err.Error(), "official") {
		t.Errorf("untrusted download URL = %v, want rejection", err)
	}
}

func TestResolveFabricUsesStableLoaderAndInstaller(t *testing.T) {
	fakeMinecraftServices(t)
	ctx, client := context.Background(), http.DefaultClient
	v, u, err := resolveFabric(ctx, client, "latest")
	if err != nil || v != "1.21.8" || !strings.HasSuffix(u, "/fabric/versions/loader/1.21.8/0.17.2/1.1.0/server/jar") {
		t.Fatalf("latest = %q %q %v", v, u, err)
	}
	if v, _, err := resolveFabric(ctx, client, "1.21.7"); err != nil || v != "1.21.7" {
		t.Errorf("1.21.7 = %q %v", v, err)
	}
	if _, _, err := resolveFabric(ctx, client, "1.2.5"); err == nil {
		t.Error("an unsupported version resolved")
	}
}

func TestDownloadVerifiedRejectsBadChecksumsAndOversizedFiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("hello")) }))
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "f")
	ctx, client := context.Background(), http.DefaultClient
	sum := sha256.Sum256([]byte("hello"))
	if err := downloadVerified(ctx, client, srv.URL, dst, 1<<20, hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("good download: %v", err)
	}
	if err := downloadVerified(ctx, client, srv.URL, dst, 1<<20, strings.Repeat("0", 64)); err == nil || fileExists(dst) {
		t.Errorf("bad checksum = %v, file kept = %v", err, fileExists(dst))
	}
	if err := downloadVerified(ctx, client, srv.URL, dst, 3, ""); err == nil || fileExists(dst) {
		t.Errorf("oversized = %v, file kept = %v", err, fileExists(dst))
	}
}

func TestMinecraftInstallerInstallsEachVariant(t *testing.T) {
	fakeMinecraftServices(t)
	for variant, jar := range map[string]string{"paper": "server.jar", "fabric": fabricLauncherJar} {
		t.Run(variant, func(t *testing.T) {
			s := minecraftSettings("mc")
			s.MinecraftVariant, s.MinecraftVersion = variant, "latest"
			app := newCapabilityTestApp(t, s)
			m := app.managers["mc"]
			if err := m.RunMinecraftInstaller("latest", variant); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "install to finish", func() bool { return !m.InstallBusy() })
			game := filepath.Join(m.rootDir, "game")
			if !fileExists(filepath.Join(game, jar)) || !m.Installed() {
				for _, l := range m.logs.Snapshot() {
					t.Log(l.Line)
				}
				t.Fatalf("%s missing or server not installed", jar)
			}
			if info, ok := readMinecraftInstallInfo(game); !ok || info.Variant != variant {
				t.Errorf("install info = %+v", info)
			}
			args, err := (MinecraftAdapter{}).PrepareLaunch(app, s)
			if err != nil || strings.Join(args[2:], " ") != "-jar "+jar+" nogui" {
				t.Errorf("launch args = %v, %v", args, err)
			}
			if _, err := (MinecraftAdapter{}).BuildProcess(m.rootDir, args); err != nil {
				t.Errorf("BuildProcess: %v", err)
			}
		})
	}
	if err := (&ServerManager{gameType: "minecraft-java"}).RunMinecraftInstaller("latest", "spigot"); err == nil {
		t.Error("an unknown variant was accepted")
	}
}

func uploadMod(t *testing.T, mux http.Handler, id, name string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", name)
	_, _ = fw.Write([]byte("jar"))
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/api/mods/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-HSM-Server", id)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestModsFollowTheMinecraftVariant(t *testing.T) {
	paper, fabric, vanilla := minecraftSettings("pp"), minecraftSettings("fb"), minecraftSettings("vn")
	paper.MinecraftVariant, fabric.MinecraftVariant = "paper", "fabric"
	app := newCapabilityTestApp(t, paper, fabric, vanilla)
	mux := newTestMux(app)

	rec := httptest.NewRecorder()
	app.handleServersList(rec, httptest.NewRequest("GET", "/api/servers", nil))
	var list []ServerSummary
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	kinds := map[string]string{}
	for _, s := range list {
		kinds[s.ID] = map[bool]string{true: s.Capabilities.ModsKind, false: "none"}[s.Capabilities.Mods]
	}
	if kinds["pp"] != "plugins" || kinds["fb"] != "mods" || kinds["vn"] != "none" {
		t.Fatalf("mods by server = %v", kinds)
	}

	if rec := uploadMod(t, mux, "pp", "EssentialsX.jar"); rec.Code != 201 || !fileExists(filepath.Join(app.serverRoot(paper), "game", "plugins", "EssentialsX.jar")) {
		t.Errorf("Paper plugin upload = %d %s", rec.Code, rec.Body.String())
	}
	if rec := uploadMod(t, mux, "fb", "sodium.jar"); rec.Code != 201 || !fileExists(filepath.Join(app.serverRoot(fabric), "game", "mods", "sodium.jar")) {
		t.Errorf("Fabric mod upload = %d %s", rec.Code, rec.Body.String())
	}
	if rec := uploadMod(t, mux, "pp", "pack.zip"); rec.Code != 400 {
		t.Errorf("zip on Paper = %d, want 400", rec.Code)
	}
	if rec := uploadMod(t, mux, "vn", "sodium.jar"); rec.Code != 409 {
		t.Errorf("vanilla mod upload = %d, want 409", rec.Code)
	}
	// Hytale keeps its own folder and file types.
	hy := newCapabilityTestApp(t, testServer("hy", "hytale"))
	if rec := uploadMod(t, newTestMux(hy), "hy", "mod.zip"); rec.Code != 201 || !fileExists(filepath.Join(hy.serverRoot(testServer("hy", "hytale")), "game", "Server", "mods", "mod.zip")) {
		t.Errorf("Hytale mod upload = %d %s", rec.Code, rec.Body.String())
	}
}
