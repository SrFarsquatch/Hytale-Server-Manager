package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeLoaderServices stands in for Forge's promotions and Maven, NeoForge's
// Maven, Quilt's installer download and Mojang's manifest. badChecksum makes
// the published installer checksum wrong.
func fakeLoaderServices(t *testing.T, badChecksum bool) {
	t.Helper()
	jar := testJar(t)
	sum := sha256.Sum256(jar)
	checksum := hex.EncodeToString(sum[:])
	if badChecksum {
		checksum = strings.Repeat("0", 64)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/forge/promotions":
			_, _ = w.Write([]byte(`{"homepage":"x","promos":{"1.16.5-latest":"36.2.39","1.20.1-recommended":"47.3.0","1.20.1-latest":"47.4.0","1.21.1-latest":"52.0.1"}}`))
		case r.URL.Path == "/neoforge/maven-metadata.xml":
			_, _ = w.Write([]byte(`<metadata><versioning><release>21.4.0-beta</release><versions>
				<version>20.4.237</version><version>21.0.167</version><version>21.1.9</version><version>21.1.228</version><version>21.4.0-beta</version>
			</versions></versioning></metadata>`))
		case strings.HasSuffix(r.URL.Path, ".jar.sha256"):
			_, _ = w.Write([]byte(checksum + "  installer.jar\n"))
		case strings.HasSuffix(r.URL.Path, "-installer.jar"), r.URL.Path == "/quilt-installer":
			_, _ = w.Write(jar)
		case r.URL.Path == "/mojang":
			_, _ = w.Write([]byte(`{"latest":{"release":"1.21.8"},"versions":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := []string{forgePromotionsURL, forgeMavenURL, neoforgeMavenURL, quiltInstallerURL, minecraftVersionManifestURL}
	forgePromotionsURL, forgeMavenURL, neoforgeMavenURL = srv.URL+"/forge/promotions", srv.URL+"/forge/maven", srv.URL+"/neoforge"
	quiltInstallerURL, minecraftVersionManifestURL = srv.URL+"/quilt-installer", srv.URL+"/mojang"
	t.Cleanup(func() {
		forgePromotionsURL, forgeMavenURL, neoforgeMavenURL, quiltInstallerURL, minecraftVersionManifestURL = old[0], old[1], old[2], old[3], old[4]
	})
}

// fakeJava stands in for java: it reports Java 21 and acts like the loader
// installers, writing the files each one produces.
func fakeJava(t *testing.T) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "java")
	writeFile(t, script, `#!/bin/sh
if [ "$1" = "-version" ]; then echo 'openjdk version "21.0.4"' >&2; exit 0; fi
case "$3" in
  --installServer)
    echo "Installing server"
    mkdir -p "$(dirname "$FAKE_ARGS_FILE")" && echo "-cp libs" > "$FAKE_ARGS_FILE"
    touch run.sh run.bat installer.jar.log ;;
  install)
    mkdir -p server && touch server/quilt-server-launch.jar server/server.jar && echo "$5" > server/installed-version ;;
  *) echo "unexpected arguments: $*" >&2; exit 3 ;;
esac
`)
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	old := javaCommand
	javaCommand = script
	t.Cleanup(func() { javaCommand = old })
}

func TestResolveForgePrefersRecommendedBuilds(t *testing.T) {
	fakeLoaderServices(t, false)
	ctx, client := context.Background(), http.DefaultClient
	li, err := resolveForge(ctx, client, "latest")
	if err != nil || li.MinecraftVersion != "1.21.1" || li.LoaderVersion != "52.0.1" ||
		!strings.HasSuffix(li.InstallerURL, "/forge/maven/1.21.1-52.0.1/forge-1.21.1-52.0.1-installer.jar") ||
		li.ArgsFile != "libraries/net/minecraftforge/forge/1.21.1-52.0.1/unix_args.txt" {
		t.Fatalf("latest = %+v %v", li, err)
	}
	if li, err := resolveForge(ctx, client, "1.20.1"); err != nil || li.LoaderVersion != "47.3.0" {
		t.Errorf("1.20.1 = %+v %v, want the recommended 47.3.0", li, err)
	}
	if _, err := resolveForge(ctx, client, "1.16.5"); err == nil || !strings.Contains(err.Error(), "Java 8") {
		t.Errorf("1.16.5 = %v, want a Java 8 refusal", err)
	}
	if _, err := resolveForge(ctx, client, "1.21.4"); err == nil || !strings.Contains(err.Error(), "no build") {
		t.Errorf("1.21.4 = %v, want no build", err)
	}
}

func TestResolveNeoForgeMapsMinecraftVersions(t *testing.T) {
	fakeLoaderServices(t, false)
	ctx, client := context.Background(), http.DefaultClient
	li, err := resolveNeoForge(ctx, client, "latest")
	if err != nil || li.LoaderVersion != "21.1.228" || li.MinecraftVersion != "1.21.1" ||
		li.ArgsFile != "libraries/net/neoforged/neoforge/21.1.228/unix_args.txt" || !strings.HasSuffix(li.InstallerURL, "/neoforge/21.1.228/neoforge-21.1.228-installer.jar") {
		t.Fatalf("latest = %+v %v, want the newest stable build, not the beta", li, err)
	}
	if li, err := resolveNeoForge(ctx, client, "1.21"); err != nil || li.LoaderVersion != "21.0.167" || li.MinecraftVersion != "1.21" {
		t.Errorf("1.21 = %+v %v", li, err)
	}
	if li, err := resolveNeoForge(ctx, client, "1.21.1"); err != nil || li.LoaderVersion != "21.1.228" {
		t.Errorf("1.21.1 = %+v %v, want 21.1.228 over 21.1.9", li, err)
	}
	for want, msg := range map[string]string{"1.20.1": "create a Forge server", "1.21.4": "no stable build", "1.19.2": "no stable build", "26.1": "does not support"} {
		if _, err := resolveNeoForge(ctx, client, want); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%s = %v, want %q", want, err, msg)
		}
	}
}

func TestLoaderInstallersProduceALaunchableServer(t *testing.T) {
	fakeLoaderServices(t, false)
	fakeJava(t)
	cases := map[string]struct{ version, argsFile, launch string }{
		"forge":    {"1.20.1", "libraries/net/minecraftforge/forge/1.20.1-47.3.0/unix_args.txt", "@libraries/net/minecraftforge/forge/1.20.1-47.3.0/unix_args.txt nogui"},
		"neoforge": {"latest", "libraries/net/neoforged/neoforge/21.1.228/unix_args.txt", "@libraries/net/neoforged/neoforge/21.1.228/unix_args.txt nogui"},
		"quilt":    {"latest", "", "-jar " + quiltLauncherJar + " nogui"},
	}
	for variant, c := range cases {
		t.Run(variant, func(t *testing.T) {
			s := minecraftSettings("mc")
			s.MinecraftVariant, s.MinecraftVersion = variant, c.version
			app := newCapabilityTestApp(t, s)
			m := app.managers["mc"]
			game := filepath.Join(m.rootDir, "game")
			t.Setenv("FAKE_ARGS_FILE", filepath.Join(game, filepath.FromSlash(c.argsFile)))
			if err := m.RunMinecraftInstaller(c.version, variant); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "install to finish", func() bool { return !m.InstallBusy() })
			if !m.Installed() {
				for _, l := range m.logs.Snapshot() {
					t.Log(l.Line)
				}
				t.Fatal("server not installed")
			}
			info, ok := readMinecraftInstallInfo(game)
			if !ok || info.Variant != variant || info.ArgsFile != c.argsFile {
				t.Errorf("install info = %+v", info)
			}
			args, err := (MinecraftAdapter{}).PrepareLaunch(app, s)
			if err != nil || strings.Join(args[2:], " ") != c.launch {
				t.Errorf("launch args = %v, %v; want %q", args, err, c.launch)
			}
			if _, err := (MinecraftAdapter{}).BuildProcess(m.rootDir, args); err != nil {
				t.Errorf("BuildProcess: %v", err)
			}
			if fileExists(filepath.Join(game, "run.sh")) || fileExists(filepath.Join(game, "installer.jar.log")) {
				t.Error("the installer's start scripts were left behind")
			}
			if entries, _ := os.ReadDir(pathsFor(m.rootDir).Tmp()); len(entries) != 0 {
				t.Errorf("tmp/ not cleaned: %v", entries)
			}
			if f, ok := modFolderFor(s); !ok || f.Dir != filepath.Join("game", "mods") || f.Kind != "mods" {
				t.Errorf("mod folder = %+v %v", f, ok)
			}
			if variant == "quilt" {
				if b, _ := os.ReadFile(filepath.Join(game, "installed-version")); strings.TrimSpace(string(b)) != "1.21.8" {
					t.Errorf("Quilt was installed for %q, want Mojang's latest release 1.21.8", b)
				}
			}
		})
	}
}

func TestLoaderInstallStopsOnABadChecksum(t *testing.T) {
	fakeLoaderServices(t, true)
	fakeJava(t)
	s := minecraftSettings("mc")
	s.MinecraftVariant = "neoforge"
	app := newCapabilityTestApp(t, s)
	m := app.managers["mc"]
	if err := m.RunMinecraftInstaller("latest", "neoforge"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "install to finish", func() bool { return !m.InstallBusy() })
	if m.Installed() || fileExists(filepath.Join(m.rootDir, "game", "eula.txt")) {
		t.Error("an installer with a wrong checksum was run")
	}
	found := false
	for _, l := range m.logs.Snapshot() {
		found = found || strings.Contains(l.Line, "checksum")
	}
	if !found {
		t.Error("the console does not explain the checksum failure")
	}
}

func TestLoaderClientRefusesRedirectsToOtherHosts(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("x")) }))
	defer other.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, http.StatusFound) }))
	defer redirect.Close()
	resp, err := loaderClient(http.DefaultClient, "neoforged.net").Get(redirect.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("a redirect off the loader's domain was followed")
	}
}
