package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"strconv"
	"time"
)

const dragonwildsSteamAppID = "4019830"
const steamCMDArchiveURL = "https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz"

func portRangeForGame(gameType string) (int, int) {
	if strings.EqualFold(strings.TrimSpace(gameType), "runescape-dragonwilds") { return 7777, 7782 }
	minPort, maxPort, _ := deploymentPortRange()
	return minPort, maxPort
}

func nameSafeDefault(s string) string {
	s = strings.TrimSpace(s)
	if s == "" { return "Dragonwilds World" }
	var b strings.Builder
	for _, r := range s {
		if r == '\r' || r == '\n' || r == 0 { continue }
		b.WriteRune(r)
		if b.Len() >= 32 { break }
	}
	out := strings.TrimSpace(b.String())
	if out == "" { return "Dragonwilds World" }
	return out
}

func (a *App) nextAvailablePortForRange(minPort, maxPort int) int {
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()
	for port := minPort; port <= maxPort; port++ {
		used := false
		for _, server := range a.cfg.Servers {
			if server.Port == port { used = true; break }
		}
		if !used { return port }
	}
	return minPort
}

type DragonwildsAdapter struct{}

func (DragonwildsAdapter) GameType() string { return "runescape-dragonwilds" }

func (DragonwildsAdapter) PrepareLaunch(app *App, settings ServerSettings) ([]string, error) {
	if strings.TrimSpace(settings.DragonwildsOwnerID) == "" {
		return nil, errors.New("Dragonwilds Owner ID is required; enter your in-game Player ID in server settings")
	}
	if strings.TrimSpace(settings.DragonwildsAdminPassword) == "" {
		return nil, errors.New("Dragonwilds admin password is required")
	}
	if settings.Port < 1 || settings.Port > 65535 {
		return nil, errors.New("Dragonwilds UDP port is invalid")
	}
	if err := writeDragonwildsConfig(app.serverRoot(settings), settings); err != nil {
		return nil, err
	}
	return []string{"-log", "-NewConsole", "-port=" + strconv.Itoa(settings.Port)}, nil
}

func writeDragonwildsConfig(root string, settings ServerSettings) error {
	configDirs := []string{
		filepath.Join(root, "game", "RSDragonwilds", "Saved", "Config", "Linux"),
		filepath.Join(root, "game", "RSDragonwilds", "Saved", "Config", "LinuxServer"),
	}
	worldName := strings.TrimSpace(settings.DragonwildsWorldName)
	if worldName == "" { worldName = nameSafeDefault(settings.ServerName) }
	values := []struct{ key, value string }{
		{"AdminPassword", settings.DragonwildsAdminPassword},
		{"OwnerId", settings.DragonwildsOwnerID},
		{"Public", boolDigit(settings.DragonwildsPublic)},
		{"WorldPassword", settings.DragonwildsWorldPassword},
		{"ServerName", nameSafeDefault(settings.ServerName)},
		{"DefaultWorldName", worldName},
	}
	for _, item := range values {
		if strings.ContainsAny(item.value, "\r\n\x00") {
			return fmt.Errorf("invalid newline in Dragonwilds setting %s", item.key)
		}
	}
	for _, configDir := range configDirs {
		if err := os.MkdirAll(configDir, 0755); err != nil {
			return fmt.Errorf("create Dragonwilds config directory: %w", err)
		}
		path := filepath.Join(configDir, "DedicatedServer.ini")
		if err := mergeDragonwildsINI(path, values); err != nil {
			return fmt.Errorf("update Dragonwilds config: %w", err)
		}
	}
	return nil
}

// mergeDragonwildsINI updates only the keys managed by HSM, preserving other
// settings and game-generated admin-account entries in the INI file.
func mergeDragonwildsINI(path string, values []struct{ key, value string }) error {
	const section = "[/script/dominion.dedicatedserversettings]"
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) { return err }
	lines := []string{}
	if err == nil && len(existing) > 0 { lines = strings.Split(strings.TrimRight(string(existing), "\r\n"), "\n") }
	valueByKey := make(map[string]string, len(values))
	for _, item := range values { valueByKey[item.key] = item.value }
	seen := make(map[string]bool, len(values))
	output := make([]string, 0, len(lines)+len(values)+2)
	inSection, foundSection := false, false
	appendMissing := func() {
		for _, item := range values {
			if !seen[item.key] { output = append(output, item.key+"="+item.value); seen[item.key] = true }
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			if inSection { appendMissing() }
			inSection = strings.EqualFold(trimmed, section)
			if inSection { foundSection = true }
			output = append(output, line)
			continue
		}
		if inSection {
			key, _, ok := strings.Cut(line, "=")
			if ok {
				if value, managed := valueByKey[strings.TrimSpace(key)]; managed {
					output = append(output, key+"="+value)
					seen[strings.TrimSpace(key)] = true
					continue
				}
			}
		}
		output = append(output, line)
	}
	if inSection { appendMissing() }
	if !foundSection {
		if len(output) > 0 && output[len(output)-1] != "" { output = append(output, "") }
		output = append(output, section)
		appendMissing()
	}
	content := strings.Join(output, "\n") + "\n"
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0600); err != nil { return err }
	if err := os.Rename(tmp, path); err != nil { _ = os.Remove(tmp); return err }
	return nil
}

func boolDigit(v bool) string { if v { return "1" }; return "0" }

func (a *App) handleDragonwildsInstall(w http.ResponseWriter, r *http.Request) {
	m, s, err := a.requestServer(r)
	if err != nil { jsonError(w, 404, err.Error()); return }
	if s.GameType != "runescape-dragonwilds" {
		jsonError(w, 400, "select a RuneScape: Dragonwilds server before installing Dragonwilds files")
		return
	}
	if m.IsRunning() { jsonError(w, 409, "stop the server before installing or updating files"); return }
	if err := m.RunDragonwildsInstaller(); err != nil { jsonError(w, 409, err.Error()); return }
	jsonOut(w, 202, map[string]any{"ok": true, "message": "Dragonwilds SteamCMD install started; follow progress in the Console"})
}

func (m *ServerManager) RunDragonwildsInstaller() error {
	m.mu.Lock()
	if m.running { m.mu.Unlock(); return errors.New("server is running") }
	if m.installBusy { m.mu.Unlock(); return errors.New("an install job is already running") }
	m.installBusy = true
	m.mu.Unlock()

	go func() {
		defer func() { m.mu.Lock(); m.installBusy = false; m.mu.Unlock() }()
		if err := m.installDragonwilds(); err != nil {
			m.appendLog("installer", "Dragonwilds installation failed: "+err.Error())
		} else {
			m.appendLog("installer", "Dragonwilds dedicated server files installed successfully")
		}
	}()
	return nil
}

func (m *ServerManager) installDragonwilds() error {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return fmt.Errorf("the official Dragonwilds Linux dedicated server currently requires a Linux x86_64 host; this manager runs on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	steamDir := filepath.Join(m.toolsDir, "steamcmd")
	steamCMD := filepath.Join(steamDir, "steamcmd.sh")
	if !fileExists(steamCMD) {
		m.appendLog("installer", "Downloading official Valve SteamCMD for Linux…")
		if err := os.MkdirAll(steamDir, 0755); err != nil { return err }
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, steamCMDArchiveURL, nil)
		if err != nil { return err }
		resp, err := http.DefaultClient.Do(req)
		if err != nil { return fmt.Errorf("download SteamCMD: %w", err) }
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK { return fmt.Errorf("download SteamCMD returned HTTP %d", resp.StatusCode) }
		tmpArchive := filepath.Join(m.toolsDir, ".steamcmd.tar.gz")
		out, err := os.OpenFile(tmpArchive, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil { return err }
		_, copyErr := io.Copy(out, io.LimitReader(resp.Body, 512<<20))
		closeErr := out.Close()
		if copyErr != nil { _ = os.Remove(tmpArchive); return copyErr }
		if closeErr != nil { _ = os.Remove(tmpArchive); return closeErr }
		if err := extractSteamCMDArchive(tmpArchive, steamDir); err != nil { _ = os.Remove(tmpArchive); return err }
		_ = os.Remove(tmpArchive)
	}
	_ = os.Chmod(steamCMD, 0755)
	gameDir := filepath.Join(m.rootDir, "game")
	if err := os.MkdirAll(gameDir, 0755); err != nil { return err }
	m.appendLog("installer", "Installing RuneScape: Dragonwilds dedicated server via SteamCMD (App ID "+dragonwildsSteamAppID+")…")
	cmd := exec.Command(steamCMD, "+force_install_dir", gameDir, "+login", "anonymous", "+app_update", dragonwildsSteamAppID, "validate", "+quit")
	cmd.Dir = steamDir
	cmd.Env = append(os.Environ(), "HOME="+filepath.Join(m.rootDir, "home"))
	stdout, err := cmd.StdoutPipe()
	if err != nil { return err }
	stderr, err := cmd.StderrPipe()
	if err != nil { return err }
	if err := cmd.Start(); err != nil { return fmt.Errorf("start SteamCMD: %w", err) }
	done := make(chan struct{}, 2)
	go func() { m.scanPipe(stdout, "installer"); done <- struct{}{} }()
	go func() { m.scanPipe(stderr, "installer"); done <- struct{}{} }()
	err = cmd.Wait()
	<-done
	<-done
	if err != nil {
		return fmt.Errorf("SteamCMD exited with an error; check Console output. Some Steam tools may require a Steam account that has claimed the free dedicated-server tool: %w", err)
	}
	if !m.Installed() { return errors.New("SteamCMD finished but the Dragonwilds launcher was not found in the install directory") }
	return nil
}

func extractSteamCMDArchive(src, dst string) error {
	f, err := os.Open(src)
	if err != nil { return err }
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil { return fmt.Errorf("open SteamCMD archive: %w", err) }
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) { break }
		if err != nil { return fmt.Errorf("read SteamCMD archive: %w", err) }
		name := filepath.Clean(filepath.FromSlash(h.Name))
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe path in SteamCMD archive: %q", h.Name)
		}
		target := filepath.Join(dst, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil { return err }
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil { return err }
			mode := os.FileMode(h.Mode) & 0755
			if mode == 0 { mode = 0644 }
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil { return err }
			_, copyErr := io.Copy(out, io.LimitReader(tr, 256<<20))
			closeErr := out.Close()
			if copyErr != nil { return copyErr }
			if closeErr != nil { return closeErr }
		default:
			// Do not extract links, devices, or other special entries from a remote archive.
			continue
		}
	}
	return nil
}
