package main

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// minecraftProperty is one server.properties key offered in the structured
// form. Keys HSM already manages from server settings (port, MOTD, players,
// game mode, whitelist, online mode) are deliberately not listed here.
type minecraftProperty struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Kind     string   `json:"kind"` // "bool", "int", "choice" or "text"
	Default  string   `json:"default"`
	Choices  []string `json:"choices,omitempty"`
	Min      int      `json:"min,omitempty"`
	Max      int      `json:"max,omitempty"`
	Help     string   `json:"help,omitempty"`
	MaxBytes int      `json:"-"`
}

var minecraftPropertyForm = []minecraftProperty{
	{Key: "difficulty", Label: "Difficulty", Kind: "choice", Default: "easy", Choices: []string{"peaceful", "easy", "normal", "hard"}},
	{Key: "hardcore", Label: "Hardcore", Kind: "bool", Default: "false", Help: "Players are banned on death. Applies to newly created worlds."},
	{Key: "pvp", Label: "Player versus player", Kind: "bool", Default: "true"},
	{Key: "allow-flight", Label: "Allow flight", Kind: "bool", Default: "false", Help: "Stops survival players being kicked for flying with mods or elytra glitches."},
	{Key: "allow-nether", Label: "Allow the Nether", Kind: "bool", Default: "true"},
	{Key: "force-gamemode", Label: "Force game mode on join", Kind: "bool", Default: "false"},
	{Key: "view-distance", Label: "View distance (chunks)", Kind: "int", Default: "10", Min: 3, Max: 32},
	{Key: "simulation-distance", Label: "Simulation distance (chunks)", Kind: "int", Default: "10", Min: 3, Max: 32},
	{Key: "spawn-protection", Label: "Spawn protection radius", Kind: "int", Default: "16", Min: 0, Max: 1000, Help: "Blocks around spawn only operators can change. 0 disables it."},
	{Key: "level-seed", Label: "World seed", Kind: "text", Default: "", MaxBytes: 100, Help: "Only used when a new world is generated."},
	{Key: "level-name", Label: "World folder", Kind: "text", Default: "world", MaxBytes: 64, Help: "Changing this loads or creates a different world folder."},
}

func minecraftPropertyByKey(key string) (minecraftProperty, bool) {
	for _, p := range minecraftPropertyForm {
		if p.Key == key {
			return p, true
		}
	}
	return minecraftProperty{}, false
}

// validate returns the normalised value or an error a person can act on.
func (p minecraftProperty) validate(value string) (string, error) {
	value = strings.TrimSpace(value)
	switch p.Kind {
	case "bool":
		if value != "true" && value != "false" {
			return "", fmt.Errorf("%s must be true or false", p.Label)
		}
	case "int":
		n, err := strconv.Atoi(value)
		if err != nil || n < p.Min || n > p.Max {
			return "", fmt.Errorf("%s must be a whole number from %d to %d", p.Label, p.Min, p.Max)
		}
		value = strconv.Itoa(n)
	case "choice":
		for _, c := range p.Choices {
			if value == c {
				return value, nil
			}
		}
		return "", fmt.Errorf("%s must be one of %s", p.Label, strings.Join(p.Choices, ", "))
	case "text":
		// Backslashes, separators and line breaks would change how the
		// properties file is parsed.
		if len(value) > p.MaxBytes || strings.ContainsAny(value, "\\\r\n\x00=:") {
			return "", fmt.Errorf("%s must be at most %d characters without \\, =, : or line breaks", p.Label, p.MaxBytes)
		}
		if p.Key == "level-name" && (value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/")) {
			return "", fmt.Errorf("%s must be a single folder name", p.Label)
		}
	}
	return value, nil
}

// readMinecraftProperties returns the form keys' current values, using the
// vanilla default for keys the file does not set yet.
func readMinecraftProperties(gameDir string) (map[string]string, error) {
	values := map[string]string{}
	for _, p := range minecraftPropertyForm {
		values[p.Key] = p.Default
	}
	f, err := os.Open(filepath.Join(gameDir, "server.properties"))
	if os.IsNotExist(err) {
		return values, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if _, known := minecraftPropertyByKey(strings.TrimSpace(key)); known {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return values, scanner.Err()
}

func (a *App) handleMinecraftPropertiesGet(w http.ResponseWriter, r *http.Request) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if !requireCapability(w, s, "Editing server.properties", func(c GameCapability) bool { return c.ServerProperties }) {
		return
	}
	values, err := readMinecraftProperties(pathsFor(a.serverRoot(s)).Game())
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonOut(w, 200, map[string]any{"fields": minecraftPropertyForm, "values": values, "running": m.IsRunning()})
}

func (a *App) handleMinecraftPropertiesPut(w http.ResponseWriter, r *http.Request) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if !requireCapability(w, s, "Editing server.properties", func(c GameCapability) bool { return c.ServerProperties }) {
		return
	}
	var body struct {
		Values map[string]string `json:"values"`
	}
	if err := decodeJSON(r, &body); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	var updates []struct{ key, value string }
	for key, raw := range body.Values {
		p, ok := minecraftPropertyByKey(key)
		if !ok {
			jsonError(w, 400, "server.properties key "+strconv.Quote(key)+" cannot be edited here")
			return
		}
		value, err := p.validate(raw)
		if err != nil {
			jsonError(w, 400, err.Error())
			return
		}
		updates = append(updates, struct{ key, value string }{key, value})
	}
	gameDir := pathsFor(a.serverRoot(s)).Game()
	if err := os.MkdirAll(gameDir, 0o755); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	if err := mergeMinecraftProperties(filepath.Join(gameDir, "server.properties"), updates); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	values, err := readMinecraftProperties(gameDir)
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonOut(w, 200, map[string]any{"fields": minecraftPropertyForm, "values": values, "running": m.IsRunning()})
}
