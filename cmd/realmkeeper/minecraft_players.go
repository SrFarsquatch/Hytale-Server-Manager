package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Minecraft player management: the whitelist, operators and bans.
//
// A running server keeps these lists in memory and rewrites its JSON files
// when they change, so changes to a running server go through its console
// commands. A stopped server's files are edited directly; the server reads
// them on its next start. Entries need the account's UUID, which is looked up
// from Mojang the same way the server does it (servers run in online mode).

const (
	minecraftWhitelistFile = "whitelist.json"
	minecraftOpsFile       = "ops.json"
	minecraftBansFile      = "banned-players.json"
	minecraftIPBansFile    = "banned-ips.json"
)

var (
	minecraftNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,16}$`)
	// minecraftProfileURL looks up an account by name; tests point it at a fake.
	minecraftProfileURL = "https://api.mojang.com/users/profiles/minecraft/"
	// minecraftListUpdateWait is how long a console change is given to reach
	// the server's files before the response is sent.
	minecraftListUpdateWait = 3 * time.Second
	minecraftPlayerFilesMu  sync.Mutex
)

type minecraftListedPlayer struct {
	Name string `json:"name"`
	UUID string `json:"uuid"`
}

type minecraftOp struct {
	minecraftListedPlayer
	Level int `json:"level"`
}

type minecraftBan struct {
	Name    string `json:"name,omitempty"`
	IP      string `json:"ip,omitempty"`
	Reason  string `json:"reason"`
	Created string `json:"created"`
	Source  string `json:"source"`
	Expires string `json:"expires"`
}

type minecraftPlayerLists struct {
	Running          bool                    `json:"running"`
	WhitelistEnabled bool                    `json:"whitelistEnabled"`
	Whitelist        []minecraftListedPlayer `json:"whitelist"`
	Ops              []minecraftOp           `json:"ops"`
	Bans             []minecraftBan          `json:"bans"`
	IPBans           []minecraftBan          `json:"ipBans"`
	Message          string                  `json:"message,omitempty"`
}

// readMinecraftList reads one of the server's JSON lists. Entries are kept as
// maps so fields the app does not know about survive a rewrite.
func readMinecraftList(gameDir, file string) ([]map[string]any, error) {
	b, err := os.ReadFile(filepath.Join(gameDir, file))
	if errors.Is(err, os.ErrNotExist) {
		return []map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(b)) == "" {
		return []map[string]any{}, nil
	}
	var entries []map[string]any
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", file, err)
	}
	return entries, nil
}

func writeMinecraftList(gameDir, file string, entries []map[string]any) error {
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(gameDir, file), append(b, '\n'), 0o644)
}

func entryString(e map[string]any, key string) string {
	s, _ := e[key].(string)
	return s
}

func readMinecraftPlayerLists(gameDir string) (minecraftPlayerLists, error) {
	out := minecraftPlayerLists{Whitelist: []minecraftListedPlayer{}, Ops: []minecraftOp{}, Bans: []minecraftBan{}, IPBans: []minecraftBan{}}
	whitelist, err := readMinecraftList(gameDir, minecraftWhitelistFile)
	if err != nil {
		return out, err
	}
	for _, e := range whitelist {
		out.Whitelist = append(out.Whitelist, minecraftListedPlayer{Name: entryString(e, "name"), UUID: entryString(e, "uuid")})
	}
	ops, err := readMinecraftList(gameDir, minecraftOpsFile)
	if err != nil {
		return out, err
	}
	for _, e := range ops {
		level, _ := e["level"].(float64)
		out.Ops = append(out.Ops, minecraftOp{minecraftListedPlayer{Name: entryString(e, "name"), UUID: entryString(e, "uuid")}, int(level)})
	}
	for _, list := range []struct {
		file string
		into *[]minecraftBan
	}{{minecraftBansFile, &out.Bans}, {minecraftIPBansFile, &out.IPBans}} {
		entries, err := readMinecraftList(gameDir, list.file)
		if err != nil {
			return out, err
		}
		for _, e := range entries {
			*list.into = append(*list.into, minecraftBan{
				Name: entryString(e, "name"), IP: entryString(e, "ip"), Reason: entryString(e, "reason"),
				Created: entryString(e, "created"), Source: entryString(e, "source"), Expires: entryString(e, "expires"),
			})
		}
	}
	return out, nil
}

// minecraftEntryIndex finds an entry by player name (case-insensitively, as
// the server does) or, for IP bans, by address.
func minecraftEntryIndex(entries []map[string]any, key, value string) int {
	for i, e := range entries {
		if strings.EqualFold(entryString(e, key), value) {
			return i
		}
	}
	return -1
}

// lookupMinecraftProfile returns an account's UUID (with dashes) and the
// name's canonical capitalisation.
func lookupMinecraftProfile(ctx context.Context, name string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, minecraftProfileURL+name, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", appUserAgent())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("could not reach Mojang to look up %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNoContent {
		return "", "", fmt.Errorf("there is no Minecraft account named %s", name)
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("Mojang account lookup returned HTTP %d", resp.StatusCode)
	}
	var profile struct{ ID, Name string }
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&profile); err != nil {
		return "", "", fmt.Errorf("unexpected Mojang response: %w", err)
	}
	id := strings.ToLower(strings.ReplaceAll(profile.ID, "-", ""))
	if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
		return "", "", errors.New("unexpected Mojang response: bad account ID")
	}
	if profile.Name == "" {
		profile.Name = name
	}
	return id[0:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:], profile.Name, nil
}

// minecraftOpLevel is the level the server gives new operators.
func minecraftOpLevel(gameDir string) int {
	f, err := os.Open(filepath.Join(gameDir, "server.properties"))
	if err != nil {
		return 4
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(scanner.Text()), "=")
		if ok && strings.TrimSpace(key) == "op-permission-level" {
			if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && n >= 1 && n <= 4 {
				return n
			}
		}
	}
	return 4
}

type minecraftPlayerAction struct {
	Action string `json:"action"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// cleanReason makes a ban or kick reason safe to send as part of a command.
func cleanReason(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200])
	}
	return s
}

// validate checks the target and returns the console command for a running
// server ("" for actions that are not commands).
func (p *minecraftPlayerAction) validate() (string, error) {
	p.Name = strings.TrimSpace(p.Name)
	p.Reason = cleanReason(p.Reason)
	switch p.Action {
	case "whitelist-on", "whitelist-off":
		return "", nil
	case "ban-ip", "pardon-ip":
		if net.ParseIP(p.Name) == nil {
			return "", errors.New("enter an IP address, for example 203.0.113.7")
		}
	case "whitelist-add", "whitelist-remove", "op", "deop", "ban", "pardon", "kick":
		if !minecraftNamePattern.MatchString(p.Name) {
			return "", errors.New("enter a Minecraft player name: 1–16 letters, numbers or underscores")
		}
	default:
		return "", errors.New("unknown action")
	}
	command := map[string]string{
		"whitelist-add": "whitelist add", "whitelist-remove": "whitelist remove", "op": "op", "deop": "deop",
		"ban": "ban", "pardon": "pardon", "ban-ip": "ban-ip", "pardon-ip": "pardon-ip", "kick": "kick",
	}[p.Action] + " " + p.Name
	if p.Reason != "" && (p.Action == "ban" || p.Action == "ban-ip" || p.Action == "kick") {
		command += " " + p.Reason
	}
	return command, nil
}

// listFor names the file an action changes, the key that identifies an
// entry, and whether the entry is present afterwards.
func (p minecraftPlayerAction) listFor() (file, key string, present bool) {
	switch p.Action {
	case "whitelist-add", "whitelist-remove":
		return minecraftWhitelistFile, "name", p.Action == "whitelist-add"
	case "op", "deop":
		return minecraftOpsFile, "name", p.Action == "op"
	case "ban", "pardon":
		return minecraftBansFile, "name", p.Action == "ban"
	case "ban-ip", "pardon-ip":
		return minecraftIPBansFile, "ip", p.Action == "ban-ip"
	}
	return "", "", false
}

// applyToStoppedServer edits the list files of a stopped server.
func (p minecraftPlayerAction) applyToStoppedServer(ctx context.Context, gameDir string) error {
	file, key, add := p.listFor()
	if file == "" {
		return errors.New("start the server to kick a player")
	}
	minecraftPlayerFilesMu.Lock()
	defer minecraftPlayerFilesMu.Unlock()
	entries, err := readMinecraftList(gameDir, file)
	if err != nil {
		return err
	}
	i := minecraftEntryIndex(entries, key, p.Name)
	if !add {
		if i < 0 {
			return nil
		}
		return writeMinecraftList(gameDir, file, append(entries[:i], entries[i+1:]...))
	}
	if i >= 0 {
		return nil
	}
	entry := map[string]any{}
	if key == "ip" {
		entry["ip"] = p.Name
	} else {
		uuid, name, err := lookupMinecraftProfile(ctx, p.Name)
		if err != nil {
			return err
		}
		entry["uuid"], entry["name"] = uuid, name
	}
	switch p.Action {
	case "op":
		entry["level"] = minecraftOpLevel(gameDir)
		entry["bypassesPlayerLimit"] = false
	case "ban", "ban-ip":
		reason := p.Reason
		if reason == "" {
			reason = "Banned by an operator."
		}
		entry["created"] = time.Now().Format("2006-01-02 15:04:05 -0700")
		entry["source"] = "Realmkeeper"
		entry["expires"] = "forever"
		entry["reason"] = reason
	}
	return writeMinecraftList(gameDir, file, append(entries, entry))
}

// waitForMinecraftList waits for the server to write a console change to its
// file and reports whether it did.
func (p minecraftPlayerAction) waitForMinecraftList(gameDir string) bool {
	file, key, present := p.listFor()
	if file == "" {
		return true
	}
	deadline := time.Now().Add(minecraftListUpdateWait)
	for {
		entries, err := readMinecraftList(gameDir, file)
		if err == nil && (minecraftEntryIndex(entries, key, p.Name) >= 0) == present {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (a *App) minecraftPlayersRequest(w http.ResponseWriter, r *http.Request) (*ServerManager, ServerSettings, bool) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return nil, s, false
	}
	if !requireCapability(w, s, "Player management", func(c GameCapability) bool { return c.PlayerManagement }) {
		return nil, s, false
	}
	return m, s, true
}

func (a *App) writeMinecraftPlayerLists(w http.ResponseWriter, m *ServerManager, s ServerSettings, message string) {
	lists, err := readMinecraftPlayerLists(pathsFor(a.serverRoot(s)).Game())
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	lists.Running, lists.WhitelistEnabled, lists.Message = m.IsRunning(), s.WhitelistEnabled, message
	jsonOut(w, 200, lists)
}

func (a *App) handleMinecraftPlayers(w http.ResponseWriter, r *http.Request) {
	m, s, ok := a.minecraftPlayersRequest(w, r)
	if !ok {
		return
	}
	a.writeMinecraftPlayerLists(w, m, s, "")
}

func (a *App) setWhitelistEnabled(id string, enabled bool) (ServerSettings, error) {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	for i := range a.cfg.Servers {
		if a.cfg.Servers[i].ID == id {
			a.cfg.Servers[i].WhitelistEnabled = enabled
			return a.cfg.Servers[i], a.saveConfigLocked()
		}
	}
	return ServerSettings{}, errors.New("server not found")
}

func (a *App) handleMinecraftPlayerAction(w http.ResponseWriter, r *http.Request) {
	m, s, ok := a.minecraftPlayersRequest(w, r)
	if !ok {
		return
	}
	var p minecraftPlayerAction
	if err := decodeJSON(r, &p); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	command, err := p.validate()
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	gameDir := pathsFor(a.serverRoot(s)).Game()
	running := m.IsRunning()

	if p.Action == "whitelist-on" || p.Action == "whitelist-off" {
		enabled := p.Action == "whitelist-on"
		if s, err = a.setWhitelistEnabled(s.ID, enabled); err != nil {
			jsonError(w, 500, err.Error())
			return
		}
		message := "Whitelist turned off"
		if enabled {
			message = "Whitelist turned on: only listed players can join"
		}
		if running {
			if err := m.SendCommand(map[bool]string{true: "whitelist on", false: "whitelist off"}[enabled]); err != nil {
				jsonError(w, 409, err.Error())
				return
			}
		} else {
			message += ". Applies when the server starts"
		}
		a.writeMinecraftPlayerLists(w, m, s, message)
		return
	}

	if !running {
		if err := p.applyToStoppedServer(r.Context(), gameDir); err != nil {
			jsonError(w, 400, err.Error())
			return
		}
		m.appendLog("controller", "Player lists: "+command)
		a.writeMinecraftPlayerLists(w, m, s, "Saved. Applies when the server starts")
		return
	}
	if err := m.SendCommand(command); err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	message := "Done"
	if !p.waitForMinecraftList(gameDir) {
		message = "Sent to the server. Check the console if the list does not update"
	}
	a.writeMinecraftPlayerLists(w, m, s, message)
}
