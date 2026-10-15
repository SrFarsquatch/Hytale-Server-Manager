package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// tModLoader is Terraria with mod support. Its GitHub release zip carries the
// server (tModLoader.dll) and launch scripts. On first start the scripts
// download the .NET runtime into the server folder, so unlike vanilla
// Terraria it is not limited to x86-64.
var (
	tmodloaderLatestReleaseURL = "https://api.github.com/repos/tModLoader/tModLoader/releases/latest"
	tmodloaderDownloadBaseURL  = "https://github.com/tModLoader/tModLoader/releases/download/"
	tmodloaderVersionText      = regexp.MustCompile(`^(?:latest|v\d{4}\.\d{1,2}\.\d{1,3}\.\d{1,4})$`)
)

const (
	tmodloaderStartScript = "start-tModLoaderServer.sh"
	tmodloaderDLL         = "tModLoader.dll"
)

// terrariaVariant treats servers created before variants existed as vanilla.
func terrariaVariant(s ServerSettings) string {
	if s.TerrariaVariant == "" {
		return "vanilla"
	}
	return s.TerrariaVariant
}

func tmodloaderInstalled(gameDir string) bool {
	return fileExists(filepath.Join(gameDir, tmodloaderDLL)) && fileExists(filepath.Join(gameDir, tmodloaderStartScript))
}

// resolveTModLoaderTag returns the release tag to install.
func resolveTModLoaderTag(ctx context.Context, client *http.Client, version string) (string, error) {
	if version != "" && !strings.EqualFold(version, "latest") {
		if !tmodloaderVersionText.MatchString(version) {
			return "", fmt.Errorf("invalid tModLoader version %q", version)
		}
		return version, nil
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := getJSON(ctx, client, tmodloaderLatestReleaseURL, &release); err != nil {
		return "", fmt.Errorf("find the latest tModLoader release: %w", err)
	}
	if !tmodloaderVersionText.MatchString(release.TagName) || release.TagName == "latest" {
		return "", fmt.Errorf("unexpected tModLoader release tag %q", release.TagName)
	}
	return release.TagName, nil
}

func (m *ServerManager) installTModLoader(version string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 12 * time.Minute}
	tag, err := resolveTModLoaderTag(ctx, client, version)
	if err != nil {
		return err
	}
	m.appendLog("installer", "Downloading tModLoader "+tag+"…")
	gameDir, err := m.installServerZip(ctx, client, tmodloaderDownloadBaseURL+url.PathEscape(tag)+"/tModLoader.zip", tmodloaderDLL, "the tModLoader server ("+tmodloaderDLL+")")
	if err != nil {
		return err
	}
	if !fileExists(filepath.Join(gameDir, tmodloaderStartScript)) {
		return errors.New("the tModLoader download has no " + tmodloaderStartScript)
	}
	// The launch scripts must be executable; zip extraction may drop the bit.
	_ = filepath.WalkDir(gameDir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), ".sh") {
			_ = os.Chmod(path, 0755)
		}
		return nil
	})
	if err := os.MkdirAll(filepath.Join(gameDir, "Mods"), 0755); err != nil {
		return err
	}
	info, _ := json.Marshal(terrariaInstall{Version: tag})
	if err := atomicWrite(filepath.Join(gameDir, terrariaInstallInfo), info, 0644); err != nil {
		return err
	}
	m.appendLog("installer", "Installed tModLoader "+tag+". The first start downloads the .NET runtime, which can take a few minutes.")
	return nil
}

// setModEnabled adds or removes a mod in tModLoader's Mods/enabled.json, a
// JSON array of mod names. A mod's name is its .tmod file name without the
// extension.
func setModEnabled(modsDir, fileName string, enabled bool) error {
	name := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	path := filepath.Join(modsDir, "enabled.json")
	var list []string
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &list); err != nil {
			return fmt.Errorf("Mods/enabled.json is not a JSON list: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	out := []string{}
	for _, n := range list {
		if n != name {
			out = append(out, n)
		}
	}
	if enabled {
		out = append(out, name)
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(b, '\n'), 0644)
}
