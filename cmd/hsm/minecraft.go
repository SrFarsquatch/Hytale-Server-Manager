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
	"strconv"
	"strings"
	"time"
)

const minecraftVersionManifestURL = "https://piston-meta.mojang.com/mc/game/version_manifest_v2.json"

type MinecraftAdapter struct{}

func (MinecraftAdapter) GameType() string { return "minecraft-java" }

func (MinecraftAdapter) Installed(rootDir string) bool {
	b, err := os.ReadFile(filepath.Join(rootDir, "game", "eula.txt"))
	return fileExists(filepath.Join(rootDir, "game", "server.jar")) && err == nil && strings.Contains(strings.ToLower(string(b)), "eula=true")
}

func (MinecraftAdapter) PrepareLaunch(app *App, settings ServerSettings) ([]string, error) {
	if !settings.MinecraftEulaAccepted {
		return nil, errors.New("Minecraft EULA must be accepted before starting this server")
	}
	if !validMemory(settings.MemoryMin) || !validMemory(settings.MemoryMax) {
		return nil, errors.New("invalid Minecraft JVM memory settings")
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
	if stdin == nil { return errors.New("server stdin is unavailable") }
	_, err := io.WriteString(stdin, "stop\n")
	return err
}

type minecraftManifest struct {
	Latest struct { Release string `json:"release"` } `json:"latest"`
	Versions []struct {
		ID string `json:"id"`
		URL string `json:"url"`
		Type string `json:"type"`
	} `json:"versions"`
}

type minecraftVersionMeta struct {
	Downloads struct {
		Server struct { URL string `json:"url"` } `json:"server"`
	} `json:"downloads"`
}

func resolveMinecraftServerURL(ctx context.Context, client *http.Client, version string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, minecraftVersionManifestURL, nil)
	if err != nil { return "", "", err }
	resp, err := client.Do(req)
	if err != nil { return "", "", fmt.Errorf("fetch Minecraft version manifest: %w", err) }
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK { return "", "", fmt.Errorf("Minecraft version manifest returned HTTP %d", resp.StatusCode) }
	var manifest minecraftManifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&manifest); err != nil { return "", "", fmt.Errorf("parse Minecraft version manifest: %w", err) }
	version = strings.TrimSpace(version)
	if version == "" || strings.EqualFold(version, "latest") { version = manifest.Latest.Release }
	var metadataURL string
	for _, entry := range manifest.Versions {
		if entry.ID == version { metadataURL = entry.URL; break }
	}
	if metadataURL == "" { return "", "", fmt.Errorf("Minecraft version %q was not found in the official version manifest", version) }
	metaReq, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil { return "", "", err }
	metaResp, err := client.Do(metaReq)
	if err != nil { return "", "", fmt.Errorf("fetch Minecraft %s metadata: %w", version, err) }
	defer metaResp.Body.Close()
	if metaResp.StatusCode != http.StatusOK { return "", "", fmt.Errorf("Minecraft version metadata returned HTTP %d", metaResp.StatusCode) }
	var meta minecraftVersionMeta
	if err := json.NewDecoder(io.LimitReader(metaResp.Body, 8<<20)).Decode(&meta); err != nil { return "", "", fmt.Errorf("parse Minecraft version metadata: %w", err) }
	if meta.Downloads.Server.URL == "" { return "", "", fmt.Errorf("official Minecraft version %s does not publish a server jar", version) }
	return version, meta.Downloads.Server.URL, nil
}

func (m *ServerManager) RunMinecraftInstaller(version string) error {
	m.mu.Lock()
	if m.running { m.mu.Unlock(); return errors.New("server is running") }
	if m.installBusy { m.mu.Unlock(); return errors.New("an install job is already running") }
	if m.gameType != "minecraft-java" { m.mu.Unlock(); return errors.New("select a Minecraft Java server before installing Minecraft files") }
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
	client := &http.Client{Timeout: 2*time.Minute}
	resolvedVersion, downloadURL, err := resolveMinecraftServerURL(ctx, client, version)
	if err != nil { return err }
	gameDir := filepath.Join(m.rootDir, "game")
	if err := os.MkdirAll(gameDir, 0755); err != nil { return err }
	finalPath := filepath.Join(gameDir, "server.jar")
	tmpPath := filepath.Join(gameDir, ".server.jar.download")
	_ = os.Remove(tmpPath)
	m.appendLog("installer", "Downloading official Minecraft Java server "+resolvedVersion+"…")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil { return err }
	resp, err := client.Do(req)
	if err != nil { return fmt.Errorf("download server jar: %w", err) }
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK { return fmt.Errorf("Minecraft server download returned HTTP %d", resp.StatusCode) }
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil { return err }
	_, copyErr := io.Copy(out, io.LimitReader(resp.Body, 512<<20))
	closeErr := out.Close()
	if copyErr != nil { _ = os.Remove(tmpPath); return copyErr }
	if closeErr != nil { _ = os.Remove(tmpPath); return closeErr }
	if err := os.Rename(tmpPath, finalPath); err != nil { _ = os.Remove(tmpPath); return err }
	m.appendLog("installer", "Downloaded Minecraft "+resolvedVersion+" server jar")
	return nil
}

func writeMinecraftProperties(root string, s ServerSettings) error {
	gameDir := filepath.Join(root, "game")
	if err := os.MkdirAll(gameDir, 0755); err != nil { return err }
	if !s.MinecraftEulaAccepted { return errors.New("Minecraft EULA must be accepted before installing or starting this server") }
	eula := filepath.Join(gameDir, "eula.txt")
	if err := os.WriteFile(eula, []byte("# Accepted by the server owner in HSM. See https://aka.ms/MinecraftEULA.\neula=true\n"), 0600); err != nil { return err }
	mode := "survival"
	if strings.EqualFold(s.GameMode, "creative") { mode = "creative" } else if strings.EqualFold(s.GameMode, "adventure") { mode = "adventure" }
	props := []string{
		"motd=" + strings.ReplaceAll(strings.ReplaceAll(s.MOTD, "\r", " "), "\n", " "),
		"server-port=" + strconv.Itoa(s.Port),
		"max-players=" + strconv.Itoa(s.MaxPlayers),
		"gamemode=" + mode,
		"white-list=" + strconv.FormatBool(s.WhitelistEnabled),
		"online-mode=true",
		"enable-command-block=false",
		"server-ip=",
	}
	return os.WriteFile(filepath.Join(gameDir, "server.properties"), []byte(strings.Join(props, "\n")+"\n"), 0644)
}

