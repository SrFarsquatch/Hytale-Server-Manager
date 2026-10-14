package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const minecraftVersionManifestURL = "https://piston-meta.mojang.com/mc/game/version_manifest_v2.json"

type MinecraftAdapter struct{}

func (MinecraftAdapter) GameType() string { return "minecraft-java" }

func (MinecraftAdapter) Installed(rootDir string) bool {
	b, err := os.ReadFile(filepath.Join(rootDir, "game", "eula.txt"))
	if err != nil || !fileExists(filepath.Join(rootDir, "game", "server.jar")) {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.EqualFold(strings.TrimSpace(line), "eula=true") {
			return true
		}
	}
	return false
}

func (MinecraftAdapter) PrepareLaunch(app *App, settings ServerSettings) ([]string, error) {
	if !settings.MinecraftEulaAccepted {
		return nil, errors.New("Minecraft EULA must be accepted before starting this server")
	}
	if !validMemory(settings.MemoryMin) || !validMemory(settings.MemoryMax) {
		return nil, errors.New("invalid Minecraft JVM memory settings")
	}
	if err := checkMinecraftJava(filepath.Join(app.serverRoot(settings), "game"), detectJavaMajor); err != nil {
		return nil, err
	}
	if err := writeMinecraftProperties(app.serverRoot(settings), settings); err != nil {
		return nil, err
	}
	return []string{"-Xms" + settings.MemoryMin, "-Xmx" + settings.MemoryMax, "-jar", "server.jar", "nogui"}, nil
}

func (MinecraftAdapter) BuildProcess(rootDir string, args []string) (ProcessLaunch, error) {
	gameDir := filepath.Join(rootDir, "game")
	if !fileExists(filepath.Join(gameDir, "server.jar")) {
		return ProcessLaunch{}, errors.New("Minecraft server.jar is missing; install the server files first")
	}
	return ProcessLaunch{Path: "java", Args: append([]string(nil), args...), Dir: gameDir}, nil
}

func (MinecraftAdapter) RequestStop(_ int, stdin io.WriteCloser) error {
	if stdin == nil {
		return errors.New("server stdin is unavailable")
	}
	_, err := io.WriteString(stdin, "stop\n")
	return err
}

type minecraftManifest struct {
	Latest struct {
		Release string `json:"release"`
	} `json:"latest"`
	Versions []struct {
		ID   string `json:"id"`
		URL  string `json:"url"`
		Type string `json:"type"`
	} `json:"versions"`
}

type minecraftVersionMeta struct {
	Downloads struct {
		Server struct {
			URL string `json:"url"`
		} `json:"server"`
	} `json:"downloads"`
	// JavaVersion is Mojang's declared minimum Java for this release.
	JavaVersion struct {
		MajorVersion int `json:"majorVersion"`
	} `json:"javaVersion"`
}

func isOfficialMojangURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "mojang.com" || strings.HasSuffix(host, ".mojang.com")
}

func resolveMinecraftServerURL(ctx context.Context, client *http.Client, version string) (string, string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, minecraftVersionManifestURL, nil)
	if err != nil {
		return "", "", 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", 0, fmt.Errorf("fetch Minecraft version manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", 0, fmt.Errorf("Minecraft version manifest returned HTTP %d", resp.StatusCode)
	}
	var manifest minecraftManifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&manifest); err != nil {
		return "", "", 0, fmt.Errorf("parse Minecraft version manifest: %w", err)
	}
	version = strings.TrimSpace(version)
	if version == "" || strings.EqualFold(version, "latest") {
		version = manifest.Latest.Release
	}
	var metadataURL string
	for _, entry := range manifest.Versions {
		if entry.ID == version {
			if !isOfficialMojangURL(entry.URL) {
				return "", "", 0, errors.New("Minecraft version metadata URL is not an official HTTPS Mojang URL")
			}
			metadataURL = entry.URL
			break
		}
	}
	if metadataURL == "" {
		return "", "", 0, fmt.Errorf("Minecraft version %q was not found in the official version manifest", version)
	}
	metaReq, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return "", "", 0, err
	}
	metaResp, err := client.Do(metaReq)
	if err != nil {
		return "", "", 0, fmt.Errorf("fetch Minecraft %s metadata: %w", version, err)
	}
	defer metaResp.Body.Close()
	if metaResp.StatusCode != http.StatusOK {
		return "", "", 0, fmt.Errorf("Minecraft version metadata returned HTTP %d", metaResp.StatusCode)
	}
	var meta minecraftVersionMeta
	if err := json.NewDecoder(io.LimitReader(metaResp.Body, 8<<20)).Decode(&meta); err != nil {
		return "", "", 0, fmt.Errorf("parse Minecraft version metadata: %w", err)
	}
	if meta.Downloads.Server.URL == "" {
		return "", "", 0, fmt.Errorf("official Minecraft version %s does not publish a server jar", version)
	}
	if !isOfficialMojangURL(meta.Downloads.Server.URL) {
		return "", "", 0, errors.New("Minecraft server download URL is not an official HTTPS Mojang URL")
	}
	return version, meta.Downloads.Server.URL, meta.JavaVersion.MajorVersion, nil
}

func (m *ServerManager) RunMinecraftInstaller(version string) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return errors.New("server is running")
	}
	if m.installBusy {
		m.mu.Unlock()
		return errors.New("an install job is already running")
	}
	if m.gameType != "minecraft-java" {
		m.mu.Unlock()
		return errors.New("select a Minecraft Java server before installing Minecraft files")
	}
	if (MinecraftAdapter{}).Installed(m.rootDir) {
		m.mu.Unlock()
		return errors.New("Minecraft server files are already installed; updating them is not supported by this installer yet")
	}
	m.installBusy = true
	m.mu.Unlock()
	go func() {
		defer func() { m.mu.Lock(); m.installBusy = false; m.mu.Unlock() }()
		if err := m.installMinecraft(version); err != nil {
			m.appendLog("installer", "Minecraft installation failed: "+err.Error())
		} else {
			m.appendLog("installer", "Minecraft Java server files installed successfully")
		}
	}()
	return nil
}

func (m *ServerManager) installMinecraft(version string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 8 * time.Minute}
	resolvedVersion, downloadURL, javaMajor, err := resolveMinecraftServerURL(ctx, client, version)
	if err != nil {
		return err
	}
	gameDir := filepath.Join(m.rootDir, "game")
	if err := os.MkdirAll(gameDir, 0755); err != nil {
		return err
	}
	finalPath := filepath.Join(gameDir, "server.jar")
	tmpPath := filepath.Join(gameDir, ".server.jar.download")
	_ = os.Remove(tmpPath)
	m.appendLog("installer", "Downloading official Minecraft Java server "+resolvedVersion+"…")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download server jar: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Minecraft server download returned HTTP %d", resp.StatusCode)
	}
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(out, io.LimitReader(resp.Body, (512<<20)+1))
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return copyErr
	}
	if written > 512<<20 {
		_ = os.Remove(tmpPath)
		return errors.New("Minecraft server download exceeded the 512 MiB safety limit")
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return closeErr
	}
	if err := validateMinecraftJar(tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.WriteFile(filepath.Join(gameDir, "eula.txt"), []byte("# Accepted by the server owner in HSM. See https://aka.ms/MinecraftEULA.\neula=true\n"), 0600); err != nil {
		return err
	}
	if err := writeMinecraftInstallInfo(gameDir, minecraftInstallInfo{Version: resolvedVersion, JavaMajor: javaMajor}); err != nil {
		return err
	}
	m.appendLog("installer", "Downloaded Minecraft "+resolvedVersion+" server jar")
	return nil
}

func (a *App) handleMinecraftInstall(w http.ResponseWriter, r *http.Request) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if s.GameType != "minecraft-java" {
		jsonError(w, 400, "select a Minecraft Java server before installing Minecraft files")
		return
	}
	if s.MinecraftVariant != "" && s.MinecraftVariant != "vanilla" {
		jsonError(w, 400, "only vanilla Minecraft Java servers are currently supported")
		return
	}
	if !s.MinecraftEulaAccepted {
		jsonError(w, 400, "accept the Minecraft EULA in the creation wizard before installing server files")
		return
	}
	if m.IsRunning() {
		jsonError(w, 409, "stop the server before installing or updating files")
		return
	}
	if err := m.RunMinecraftInstaller(s.MinecraftVersion); err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	jsonOut(w, 202, map[string]any{"ok": true, "message": "Minecraft Java server download started; follow progress in Console"})
}

func validateMinecraftJar(path string) error {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("downloaded Minecraft server file is not a valid JAR: %w", err)
	}
	defer archive.Close()
	for _, file := range archive.File {
		if strings.EqualFold(file.Name, "META-INF/MANIFEST.MF") {
			return nil
		}
	}
	return errors.New("downloaded Minecraft server JAR is missing its manifest")
}

func writeMinecraftProperties(root string, s ServerSettings) error {
	gameDir := filepath.Join(root, "game")
	if err := os.MkdirAll(gameDir, 0755); err != nil {
		return err
	}
	if !s.MinecraftEulaAccepted {
		return errors.New("Minecraft EULA must be accepted before installing or starting this server")
	}
	eula := filepath.Join(gameDir, "eula.txt")
	if err := os.WriteFile(eula, []byte("# Accepted by the server owner in HSM. See https://aka.ms/MinecraftEULA.\neula=true\n"), 0600); err != nil {
		return err
	}
	mode := "survival"
	if strings.EqualFold(s.GameMode, "creative") {
		mode = "creative"
	} else if strings.EqualFold(s.GameMode, "adventure") {
		mode = "adventure"
	} else if strings.EqualFold(s.GameMode, "spectator") {
		mode = "spectator"
	}
	props := []struct{ key, value string }{
		{"motd", strings.ReplaceAll(strings.ReplaceAll(s.MOTD, "\r", " "), "\n", " ")},
		{"server-port", strconv.Itoa(s.Port)},
		{"max-players", strconv.Itoa(s.MaxPlayers)},
		{"gamemode", mode},
		{"white-list", strconv.FormatBool(s.WhitelistEnabled)},
		{"online-mode", "true"},
		{"enable-command-block", "false"},
		{"server-ip", ""},
	}
	return mergeMinecraftProperties(filepath.Join(gameDir, "server.properties"), props)
}

func mergeMinecraftProperties(path string, values []struct{ key, value string }) error {
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	managed := make(map[string]string, len(values))
	for _, item := range values {
		managed[item.key] = item.value
	}
	seen := make(map[string]bool, len(values))
	lines := []string{}
	if err == nil {
		lines = strings.Split(strings.TrimRight(string(existing), "\r\n"), "\n")
	}
	output := make([]string, 0, len(lines)+len(values))
	for _, line := range lines {
		key, _, ok := strings.Cut(line, "=")
		if ok {
			if value, isManaged := managed[strings.TrimSpace(key)]; isManaged {
				output = append(output, key+"="+value)
				seen[strings.TrimSpace(key)] = true
				continue
			}
		}
		output = append(output, line)
	}
	for _, item := range values {
		if !seen[item.key] {
			output = append(output, item.key+"="+item.value)
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(output, "\n")+"\n"), 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// minecraftInstallInfo is written next to server.jar at install time so the
// Java requirement is known without contacting Mojang again before each start.
type minecraftInstallInfo struct {
	Version   string `json:"version"`
	JavaMajor int    `json:"javaMajor"`
}

const minecraftInstallInfoFile = ".hsm-minecraft.json"

func writeMinecraftInstallInfo(gameDir string, info minecraftInstallInfo) error {
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(gameDir, minecraftInstallInfoFile), append(b, '\n'), 0644)
}

func readMinecraftInstallInfo(gameDir string) (minecraftInstallInfo, bool) {
	var info minecraftInstallInfo
	b, err := os.ReadFile(filepath.Join(gameDir, minecraftInstallInfoFile))
	if err != nil || json.Unmarshal(b, &info) != nil {
		return minecraftInstallInfo{}, false
	}
	return info, true
}

// checkMinecraftJava refuses to start a server whose release needs a newer
// Java than the one installed. Servers installed before the requirement was
// recorded are not checked, because the requirement is unknown.
func checkMinecraftJava(gameDir string, detect func() (int, error)) error {
	info, ok := readMinecraftInstallInfo(gameDir)
	if !ok || info.JavaMajor <= 0 {
		return nil
	}
	have, err := detect()
	if err != nil {
		return fmt.Errorf("could not check the installed Java version: %w", err)
	}
	if have < info.JavaMajor {
		return fmt.Errorf("Minecraft %s needs Java %d or newer, but this container has Java %d; update the HSM image to one with a newer Java runtime", info.Version, info.JavaMajor, have)
	}
	return nil
}

var javaVersionPattern = regexp.MustCompile(`version "(\d+)(?:\.(\d+))?`)

// parseJavaMajor reads the major version from `java -version` output:
// "21.0.4" is 21, and the legacy "1.8.0_392" scheme is 8.
func parseJavaMajor(output string) (int, bool) {
	m := javaVersionPattern.FindStringSubmatch(output)
	if m == nil {
		return 0, false
	}
	major, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	if major == 1 && m[2] != "" {
		minor, err := strconv.Atoi(m[2])
		if err != nil {
			return 0, false
		}
		return minor, true
	}
	return major, true
}

func detectJavaMajor() (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "java", "-version").CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("java -version failed: %w", err)
	}
	major, ok := parseJavaMajor(string(out))
	if !ok {
		return 0, fmt.Errorf("unrecognised java -version output: %q", strings.TrimSpace(string(out)))
	}
	return major, nil
}
