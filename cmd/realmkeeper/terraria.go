package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Terraria's official dedicated server is published as one zip per release.
// The names endpoint lists the available zips, newest first.
var (
	terrariaServerListURL   = "https://terraria.org/api/get/dedicated-servers-names"
	terrariaDownloadBaseURL = "https://terraria.org/api/download/pc-dedicated-server/"
	// terrariaPlatformSupported is false off x86-64: the official Linux
	// server is an x86-64 build with a bundled Mono runtime.
	terrariaPlatformSupported = runtime.GOARCH == "amd64"
)

const (
	terrariaBinary      = "TerrariaServer.bin.x86_64"
	terrariaInstallInfo = ".hsm-terraria.json"
)

var (
	terrariaZipName     = regexp.MustCompile(`^terraria-server-(\d{3,6})\.zip$`)
	terrariaVersionText = regexp.MustCompile(`^(?:latest|\d+(?:\.\d+){0,4})$`)
	terrariaWorldSizes  = map[string]int{"small": 1, "medium": 2, "large": 3}
	terrariaDifficulty  = map[string]int{"Classic": 0, "Expert": 1, "Master": 2, "Journey": 3}
)

type TerrariaAdapter struct{}

func (TerrariaAdapter) GameType() string { return "terraria" }

// Rules use the world difficulty as the game mode. Terraria only applies it
// when the world is first generated, so it is fixed after creation.
func (TerrariaAdapter) Rules() GameRules {
	return GameRules{GameModes: []string{"Classic", "Expert", "Master", "Journey"}, ModeLabel: "Difficulty", ModeFixed: true, DefaultMOTD: "Welcome to Terraria!", DefaultMaxPlayers: 8, PlayerLimit: 255}
}

func (TerrariaAdapter) ValidateNew(req *newServerRequest) error {
	if req.TerrariaVariant == "" {
		req.TerrariaVariant = "vanilla"
	}
	version := strings.TrimSpace(req.TerrariaVersion)
	if version == "" {
		version = "latest"
	}
	switch req.TerrariaVariant {
	case "vanilla":
		if !terrariaVersionText.MatchString(version) {
			return errors.New("Terraria version must be latest or a release number such as 1.4.4.9")
		}
	case "tmodloader":
		if !tmodloaderVersionText.MatchString(version) {
			return errors.New("tModLoader version must be latest or a release tag such as v2025.08.3.0")
		}
	default:
		return errors.New("Terraria server type must be vanilla or tmodloader")
	}
	req.TerrariaVersion = version
	if req.TerrariaWorldSize == "" {
		req.TerrariaWorldSize = "medium"
	}
	if _, ok := terrariaWorldSizes[req.TerrariaWorldSize]; !ok {
		return errors.New("Terraria world size must be small, medium or large")
	}
	req.TerrariaWorldName = strings.TrimSpace(req.TerrariaWorldName)
	if req.TerrariaWorldName == "" {
		req.TerrariaWorldName = strings.TrimSpace(req.Name)
	}
	if n := len([]rune(req.TerrariaWorldName)); n < 1 || n > 32 || strings.ContainsAny(req.TerrariaWorldName, "\r\n\x00") || terrariaWorldFile(req.TerrariaWorldName) == "" {
		return errors.New("Terraria world name must be 1–32 characters and include a letter or digit")
	}
	return nil
}

// MergeSettings keeps the world (name, size, difficulty) and installed version
// fixed: they describe files on disk. Memory settings do not apply.
func (TerrariaAdapter) MergeSettings(in *ServerSettings, current ServerSettings) error {
	keepMinecraftFields(in, current)
	keepDragonwildsFields(in, current)
	keepTerrariaFields(in, current)
	in.GameMode = current.GameMode
	in.MemoryMin = current.MemoryMin
	in.MemoryMax = current.MemoryMax
	return nil
}

// ApplySettings has nothing to write: PrepareLaunch writes serverconfig.txt
// before each start.
func (TerrariaAdapter) ApplySettings(*App, *ServerManager, ServerSettings) error { return nil }

func (TerrariaAdapter) InstallStatus(app *App, s ServerSettings) map[string]any {
	gameDir := pathsFor(app.serverRoot(s)).Game()
	info, _ := readTerrariaInstallInfo(gameDir)
	return map[string]any{
		"game": s.GameType, "version": s.TerrariaVersion, "installedVersion": info.Version,
		"worldName": s.TerrariaWorldName, "worldSize": s.TerrariaWorldSize,
		"variant": terrariaVariant(s), "platformSupported": terrariaPlatformSupported || terrariaVariant(s) == "tmodloader",
		"paths": map[string]string{"game": gameDir, "world": filepath.Join(gameDir, "Worlds", terrariaWorldFile(s.TerrariaWorldName))},
	}
}

func (TerrariaAdapter) Installed(rootDir string) bool {
	return fileExists(filepath.Join(pathsFor(rootDir).Game(), terrariaBinary)) || tmodloaderInstalled(pathsFor(rootDir).Game())
}

func (TerrariaAdapter) PrepareLaunch(app *App, s ServerSettings) ([]string, error) {
	tml := terrariaVariant(s) == "tmodloader"
	if !tml && !terrariaPlatformSupported {
		return nil, errors.New("the official Terraria server runs only on x86-64 (amd64) hosts")
	}
	path, err := writeTerrariaConfig(app.serverRoot(s), s)
	if err != nil {
		return nil, err
	}
	args := []string{"-config", path}
	if tml {
		// -nosteam skips the start script's interactive Steam prompt. The save
		// directory keeps worlds, mods and mod configs inside the server folder.
		gameDir := pathsFor(app.serverRoot(s)).Game()
		args = append([]string{"-nosteam"}, append(args, "-tmlsavedirectory", gameDir)...)
	}
	return append(args, splitArgs(s.ExtraArgs)...), nil
}

func (TerrariaAdapter) BuildProcess(rootDir string, args []string) (ProcessLaunch, error) {
	gameDir := pathsFor(rootDir).Game()
	if tmodloaderInstalled(gameDir) {
		script := filepath.Join(gameDir, tmodloaderStartScript)
		return ProcessLaunch{Path: "bash", Args: append([]string{script}, args...), Dir: gameDir}, nil
	}
	bin := filepath.Join(gameDir, terrariaBinary)
	if !fileExists(bin) {
		return ProcessLaunch{}, errors.New("the Terraria server is not installed; install it from the Install tab first")
	}
	if err := os.Chmod(bin, 0755); err != nil {
		return ProcessLaunch{}, fmt.Errorf("make Terraria server executable: %w", err)
	}
	return ProcessLaunch{Path: bin, Args: append([]string(nil), args...), Dir: gameDir}, nil
}

// RequestStop sends exit, which saves the world before the server quits.
func (TerrariaAdapter) RequestStop(_ int, stdin io.WriteCloser) error {
	if stdin == nil {
		return errors.New("server stdin is unavailable")
	}
	_, err := io.WriteString(stdin, "exit\n")
	return err
}

func keepTerrariaFields(in *ServerSettings, current ServerSettings) {
	in.TerrariaVariant = current.TerrariaVariant
	in.TerrariaVersion = current.TerrariaVersion
	in.TerrariaWorldName = current.TerrariaWorldName
	in.TerrariaWorldSize = current.TerrariaWorldSize
}

// terrariaWorldFile is the world's file name under game/Worlds, derived from
// the world name, which cannot change after creation.
func terrariaWorldFile(worldName string) string {
	slug := slugify(worldName)
	if slug == "" {
		return ""
	}
	return slug + ".wld"
}

func oneLine(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ", "\x00", "").Replace(s)
}

// writeTerrariaConfig writes the settings Realmkeeper manages into serverconfig.txt
// and keeps any other lines the owner added. The world is generated on first
// start (autocreate) and loaded on later starts.
func writeTerrariaConfig(root string, s ServerSettings) (string, error) {
	gameDir := pathsFor(root).Game()
	worlds := filepath.Join(gameDir, "Worlds")
	if err := os.MkdirAll(worlds, 0755); err != nil {
		return "", err
	}
	file := terrariaWorldFile(s.TerrariaWorldName)
	size, ok := terrariaWorldSizes[s.TerrariaWorldSize]
	if file == "" || !ok {
		return "", errors.New("this Terraria server has no valid world settings")
	}
	difficulty, ok := terrariaDifficulty[s.GameMode]
	if !ok {
		difficulty = 0
	}
	path := filepath.Join(gameDir, "serverconfig.txt")
	err := mergeMinecraftProperties(path, []struct{ key, value string }{
		{"world", filepath.Join(worlds, file)},
		{"worldpath", worlds},
		{"worldname", oneLine(s.TerrariaWorldName)},
		{"autocreate", strconv.Itoa(size)},
		{"difficulty", strconv.Itoa(difficulty)},
		{"maxplayers", strconv.Itoa(s.MaxPlayers)},
		{"port", strconv.Itoa(s.Port)},
		{"password", oneLine(s.Password)},
		{"motd", oneLine(s.MOTD)},
		// The container cannot open router ports, and the player-list parser
		// reads English console output.
		{"upnp", "0"},
		{"language", "en-US"},
	})
	return path, err
}

// Terraria's "playing" command prints one "Name (address)" line per player,
// then a total. Output can follow the ": " command prompt on the same line.
var (
	terrariaPlayingTotal = regexp.MustCompile(`^(?:No players connected\.|1 player connected\.|(\d+) players connected\.)$`)
	terrariaPlayingEntry = regexp.MustCompile(`^(.+?) \(([0-9A-Fa-f.:\[\]%]+)\)$`)
)

func trimTerrariaPrompt(line string) string {
	line = strings.TrimSpace(stripANSI(line))
	for strings.HasPrefix(line, ":") {
		line = strings.TrimSpace(strings.TrimPrefix(line, ":"))
	}
	return line
}

func parseTerrariaPlaying(lines []LogLine) ([]string, int, bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		m := terrariaPlayingTotal.FindStringSubmatch(trimTerrariaPrompt(lines[i].Line))
		if m == nil {
			continue
		}
		count := 0
		if strings.HasPrefix(m[0], "1 ") {
			count = 1
		} else if m[1] != "" {
			count, _ = strconv.Atoi(m[1])
		}
		players := []string{}
		if count == 0 {
			return players, 0, true
		}
		// The entries are the lines just above the total.
		for j := i - 1; j >= 0 && len(players) < count; j-- {
			e := terrariaPlayingEntry.FindStringSubmatch(trimTerrariaPrompt(lines[j].Line))
			if e == nil {
				break
			}
			players = append([]string{strings.TrimSpace(e[1])}, players...)
		}
		return players, 0, true
	}
	return nil, 0, false
}

type terrariaInstall struct {
	Version string `json:"version"`
}

func readTerrariaInstallInfo(gameDir string) (terrariaInstall, error) {
	var info terrariaInstall
	b, err := os.ReadFile(filepath.Join(gameDir, terrariaInstallInfo))
	if err != nil {
		return info, err
	}
	return info, json.Unmarshal(b, &info)
}

// resolveTerrariaZip returns the official zip name for a version ("latest",
// "1.4.4.9" or "1449").
func resolveTerrariaZip(ctx context.Context, client *http.Client, version string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, terrariaServerListURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch the Terraria server list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the Terraria server list returned HTTP %d", resp.StatusCode)
	}
	var names []string
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&names); err != nil {
		return "", fmt.Errorf("parse the Terraria server list: %w", err)
	}
	want := strings.ReplaceAll(strings.TrimSpace(version), ".", "")
	for _, name := range names {
		m := terrariaZipName.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		if want == "" || strings.EqualFold(version, "latest") || m[1] == want {
			return name, nil
		}
	}
	return "", fmt.Errorf("Terraria server version %q is not in the official server list", version)
}

func (m *ServerManager) RunTerrariaInstaller(version, variant string) error {
	if variant == "" {
		variant = "vanilla"
	}
	if variant != "vanilla" && variant != "tmodloader" {
		return fmt.Errorf("unknown Terraria server type %q", variant)
	}
	if variant == "vanilla" && !terrariaPlatformSupported {
		return errors.New("the official Terraria server runs only on x86-64 (amd64) hosts")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case m.running:
		return errors.New("server is running")
	case m.installBusy:
		return errors.New("an install job is already running")
	case m.gameType != "terraria":
		return errors.New("select a Terraria server before installing Terraria files")
	case (TerrariaAdapter{}).Installed(m.rootDir):
		return errors.New("Terraria server files are already installed; updating them is not supported by this installer yet")
	}
	m.installBusy = true
	go func() {
		defer func() { m.mu.Lock(); m.installBusy = false; m.mu.Unlock() }()
		install := m.installTerraria
		if variant == "tmodloader" {
			install = m.installTModLoader
		}
		if err := install(version); err != nil {
			m.appendLog("installer", "Terraria installation failed: "+err.Error())
		} else {
			m.appendLog("installer", "Terraria server files installed successfully")
		}
	}()
	return nil
}

func (m *ServerManager) installTerraria(version string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 12 * time.Minute}
	name, err := resolveTerrariaZip(ctx, client, version)
	if err != nil {
		return err
	}
	m.appendLog("installer", "Downloading the official Terraria server ("+name+")…")
	gameDir, err := m.installServerZip(ctx, client, terrariaDownloadBaseURL+name, terrariaBinary, "the Linux server ("+terrariaBinary+")")
	if err != nil {
		return err
	}
	entries, _ := os.ReadDir(gameDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "TerrariaServer") && !e.IsDir() {
			_ = os.Chmod(filepath.Join(gameDir, e.Name()), 0755)
		}
	}
	digits := terrariaZipName.FindStringSubmatch(name)[1]
	info, _ := json.Marshal(terrariaInstall{Version: digits})
	return atomicWrite(filepath.Join(gameDir, terrariaInstallInfo), info, 0644)
}

// installServerZip downloads a server zip, extracts it to a staging folder and
// copies the folder holding marker into game/. It returns the game folder.
func (m *ServerManager) installServerZip(ctx context.Context, client *http.Client, zipURL, marker, what string) (string, error) {
	tmp := pathsFor(m.rootDir).Tmp()
	if err := os.MkdirAll(tmp, 0755); err != nil {
		return "", err
	}
	zipPath := filepath.Join(tmp, "server-download.zip")
	staging := filepath.Join(tmp, "server-extract")
	defer os.Remove(zipPath)
	defer os.RemoveAll(staging)
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(m.rootDir, 0755); err != nil {
		return "", err
	}
	if err := downloadVerified(ctx, client, zipURL, zipPath, 512<<20, ""); err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	m.appendLog("installer", "Extracting the server files…")
	if err := extractZip(zipPath, staging); err != nil {
		return "", fmt.Errorf("extract: %w", err)
	}
	dir, err := locateFileDir(staging, marker)
	if err != nil {
		return "", err
	}
	if dir == "" {
		return "", errors.New("the download does not contain " + what)
	}
	gameDir := pathsFor(m.rootDir).Game()
	if err := os.MkdirAll(filepath.Join(gameDir, "Worlds"), 0755); err != nil {
		return "", err
	}
	return gameDir, copyDir(dir, gameDir)
}

// locateFileDir returns the folder under root that holds a file called name,
// or "" when there is none.
func locateFileDir(root, name string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == name {
			found = filepath.Dir(path)
			return filepath.SkipAll
		}
		return nil
	})
	return found, err
}

func (a *App) handleTerrariaInstall(w http.ResponseWriter, r *http.Request) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if s.GameType != "terraria" {
		jsonError(w, 400, "select a Terraria server before installing Terraria files")
		return
	}
	if err := m.RunTerrariaInstaller(s.TerrariaVersion, s.TerrariaVariant); err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	jsonOut(w, 202, map[string]any{"ok": true, "message": "Terraria server download started; follow progress in Console"})
}
