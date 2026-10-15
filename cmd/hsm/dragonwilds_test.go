package main

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPortRangeForGame(t *testing.T) {
	if min, max := portRangeForGame("runescape-dragonwilds"); min != 7777 || max != 7782 {
		t.Fatalf("Dragonwilds port range = %d-%d, want 7777-7782", min, max)
	}
	min, max, _ := deploymentPortRange()
	if gotMin, gotMax := portRangeForGame("hytale"); gotMin != min || gotMax != max {
		t.Fatalf("Hytale port range = %d-%d, want %d-%d", gotMin, gotMax, min, max)
	}
}

func TestWriteDragonwildsConfig(t *testing.T) {
	root := t.TempDir()
	settings := ServerSettings{
		GameType: "runescape-dragonwilds", ServerName: "Friends World", Port: 7777,
		DragonwildsOwnerID: "player-id-123", DragonwildsWorldName: "Misty Valley",
		DragonwildsAdminPassword: "admin-secret", DragonwildsWorldPassword: "world-secret",
		DragonwildsPublic: true,
	}
	if err := writeDragonwildsConfig(root, settings); err != nil {
		t.Fatalf("writeDragonwildsConfig() error = %v", err)
	}
	path := filepath.Join(root, "game", "RSDragonwilds", "Saved", "Config", "LinuxServer", "DedicatedServer.ini")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	documentedPath := filepath.Join(root, "game", "RSDragonwilds", "Saved", "Config", "Linux", "DedicatedServer.ini")
	if _, err := os.Stat(documentedPath); err != nil {
		t.Fatalf("officially documented Linux config path missing: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "\nPublic=") {
		t.Errorf("wrote undocumented Public setting to DedicatedServer.ini:\n%s", text)
	}
	for _, expected := range []string{
		"[/script/dominion.dedicatedserversettings]",
		"OwnerId=player-id-123",
		"AdminPassword=admin-secret",
		"WorldPassword=world-secret",
		"ServerName=Friends World",
		"DefaultWorldName=Misty Valley",
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("config missing %q:\n%s", expected, text)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("config permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestWriteDragonwildsConfigAllowsEmptyWorldPassword(t *testing.T) {
	root := t.TempDir()
	settings := ServerSettings{
		ServerName:               "Public World",
		DragonwildsOwnerID:       "owner-id",
		DragonwildsWorldName:     "World",
		DragonwildsAdminPassword: "admin-secret",
	}
	if err := writeDragonwildsConfig(root, settings); err != nil {
		t.Fatalf("writeDragonwildsConfig() error = %v", err)
	}
	path := filepath.Join(root, "game", "RSDragonwilds", "Saved", "Config", "LinuxServer", "DedicatedServer.ini")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "WorldPassword=\n") {
		t.Fatalf("empty optional WorldPassword was not written:\n%s", data)
	}
	if strings.Contains(string(data), "\nPublic=") {
		t.Fatalf("wrote undocumented Public setting:\n%s", data)
	}
}

func TestWriteDragonwildsConfigRejectsNewlineInjection(t *testing.T) {
	err := writeDragonwildsConfig(t.TempDir(), ServerSettings{
		ServerName: "Safe", DragonwildsOwnerID: "player\nPublic=1",
		DragonwildsAdminPassword: "password", DragonwildsWorldName: "World",
	})
	if err == nil {
		t.Fatal("expected newline injection to be rejected")
	}
}

func TestExtractSteamCMDArchiveRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "bad.tar.gz")
	out, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractSteamCMDArchive(src, filepath.Join(root, "out")); err == nil {
		t.Fatal("expected traversal path to be rejected")
	}
}

func TestWriteDragonwildsConfigPreservesUnmanagedEntries(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "game", "RSDragonwilds", "Saved", "Config", "LinuxServer", "DedicatedServer.ini")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	existing := "; game metadata\n[/script/dominion.dedicatedserversettings]\nOwnerId=old-owner\nGeneratedAdminAccount=keep-me\n[OtherSection]\nKeep=also-me\n"
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}
	settings := ServerSettings{ServerName: "New Name", DragonwildsOwnerID: "new-owner", DragonwildsAdminPassword: "new-admin", DragonwildsWorldName: "World"}
	if err := writeDragonwildsConfig(root, settings); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{"OwnerId=new-owner", "GeneratedAdminAccount=keep-me", "[OtherSection]", "Keep=also-me", "AdminPassword=new-admin"} {
		if !strings.Contains(got, want) {
			t.Errorf("config lost or missed %q:\n%s", want, got)
		}
	}
}

// fakeSteamCMD installs a steamcmd.sh that fails its first failures runs
// with SteamCMD's "Missing configuration" error, then installs the server.
func fakeSteamCMD(t *testing.T, m *ServerManager, failures int) string {
	t.Helper()
	dir := filepath.Join(m.toolsDir, "steamcmd")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/bash\n" +
		"echo \"$*\" >> \"$(dirname \"$0\")/calls.log\"\n" +
		"n=$(wc -l < \"$(dirname \"$0\")/calls.log\")\n" +
		"if [ \"$n\" -le " + strconv.Itoa(failures) + " ]; then echo \"ERROR! Failed to install app '4019830' (Missing configuration)\"; exit 8; fi\n" +
		"mkdir -p \"$2\" && touch \"$2/RSDragonwildsServer.sh\"\n"
	if err := os.WriteFile(filepath.Join(dir, "steamcmd.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	oldDelay := steamCMDRetryDelay
	steamCMDRetryDelay = 0
	t.Cleanup(func() { steamCMDRetryDelay = oldDelay })
	return filepath.Join(dir, "calls.log")
}

func TestDragonwildsInstallRetriesMissingConfiguration(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("the Dragonwilds installer runs only on amd64")
	}
	app := newCapabilityTestApp(t, dragonwildsSettings("dw"))
	m := app.managers["dw"]
	calls := fakeSteamCMD(t, m, 1)
	if err := m.installDragonwilds(); err != nil {
		t.Fatalf("install after one transient failure: %v", err)
	}
	b, _ := os.ReadFile(calls)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		t.Fatalf("SteamCMD ran %d times, want 2", len(lines))
	}
	if !strings.Contains(lines[0], "+login anonymous +app_info_update 1 +app_update 4019830 validate +quit") {
		t.Errorf("SteamCMD args = %q", lines[0])
	}
	if !logContains(m, "retrying, attempt 2 of 3", time.Time{}) || !m.Installed() {
		t.Error("retry was not logged or the server is not installed")
	}
}

func TestDragonwildsInstallGivesUpAfterThreeFailures(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("the Dragonwilds installer runs only on amd64")
	}
	app := newCapabilityTestApp(t, dragonwildsSettings("dw"))
	m := app.managers["dw"]
	calls := fakeSteamCMD(t, m, 10)
	err := m.installDragonwilds()
	if err == nil || !strings.Contains(err.Error(), "failed 3 times") {
		t.Fatalf("err = %v, want failure after 3 attempts", err)
	}
	b, _ := os.ReadFile(calls)
	if n := len(strings.Split(strings.TrimSpace(string(b)), "\n")); n != 3 {
		t.Errorf("SteamCMD ran %d times, want 3", n)
	}
}
