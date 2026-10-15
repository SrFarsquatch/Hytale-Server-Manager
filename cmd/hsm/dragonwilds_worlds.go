package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Dragonwilds keeps each world as a .sav file (with a .backup copy) in
// RSDragonwilds/Saved/SaveGames. On start the server loads the newest .sav;
// with none, it creates a new Standard world named DefaultWorldName. World
// rules (Standard, Custom, Creative and their settings) live inside the save,
// so a world with custom rules is made in the game and uploaded here, or
// changed in game from Settings > Server Management with the admin password.
//
// HSM never deletes a world: replaced or removed saves are moved to
// world-backups/<time>/ under the server folder.

const dragonwildsWorldUploadLimit = 512 << 20

func dragonwildsSavedDir(root string) string {
	return filepath.Join(root, "game", "RSDragonwilds", "Saved")
}

// dragonwildsSaveDir returns the folder the game uses for saves. Guides spell
// it SaveGames or Savegames, and Linux paths are case-sensitive, so prefer the
// one that already holds saves.
func dragonwildsSaveDir(root string) string {
	saved := dragonwildsSavedDir(root)
	var existing []string
	for _, name := range []string{"SaveGames", "Savegames"} {
		dir := filepath.Join(saved, name)
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			if matches, _ := filepath.Glob(filepath.Join(dir, "*.sav")); len(matches) > 0 {
				return dir
			}
			existing = append(existing, dir)
		}
	}
	if len(existing) > 0 {
		return existing[0]
	}
	return filepath.Join(saved, "SaveGames")
}

type dragonwildsWorld struct {
	Name      string `json:"name"`
	File      string `json:"file"`
	Path      string `json:"path"` // relative to the server folder, for downloads
	Size      int64  `json:"size"`
	Modified  string `json:"modified"`
	HasBackup bool   `json:"hasBackup"`
	// LoadsNext marks the newest save, which the server loads on start.
	LoadsNext bool `json:"loadsNext"`
}

type dragonwildsArchive struct {
	ID    string   `json:"id"`
	Files []string `json:"files"`
}

func listDragonwildsWorlds(root string) ([]dragonwildsWorld, error) {
	dir := dragonwildsSaveDir(root)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []dragonwildsWorld{}, nil
	}
	if err != nil {
		return nil, err
	}
	worlds := []dragonwildsWorld{}
	newest := -1
	var newestTime time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".sav") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		rel, _ := filepath.Rel(root, filepath.Join(dir, e.Name()))
		worlds = append(worlds, dragonwildsWorld{
			Name: name, File: e.Name(), Path: filepath.ToSlash(rel), Size: info.Size(),
			Modified:  info.ModTime().UTC().Format(time.RFC3339),
			HasBackup: fileExists(filepath.Join(dir, name+".backup")),
		})
		if newest < 0 || info.ModTime().After(newestTime) {
			newest, newestTime = len(worlds)-1, info.ModTime()
		}
	}
	if newest >= 0 {
		worlds[newest].LoadsNext = true
	}
	sort.Slice(worlds, func(i, j int) bool { return strings.ToLower(worlds[i].Name) < strings.ToLower(worlds[j].Name) })
	return worlds, nil
}

func dragonwildsArchiveDir(root string) string { return filepath.Join(root, "world-backups") }

func listDragonwildsArchives(root string) []dragonwildsArchive {
	entries, _ := os.ReadDir(dragonwildsArchiveDir(root))
	out := []dragonwildsArchive{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(dragonwildsArchiveDir(root), e.Name()))
		a := dragonwildsArchive{ID: e.Name(), Files: []string{}}
		for _, f := range files {
			if !f.IsDir() {
				a.Files = append(a.Files, f.Name())
			}
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// archiveDragonwildsFiles moves the named save files (when present) into a
// new world-backups folder and returns its ID.
func archiveDragonwildsFiles(root string, files []string) (string, error) {
	dir := dragonwildsSaveDir(root)
	var present []string
	for _, f := range files {
		if fileExists(filepath.Join(dir, f)) {
			present = append(present, f)
		}
	}
	if len(present) == 0 {
		return "", nil
	}
	id := time.Now().UTC().Format("20060102-150405.000")
	dst := filepath.Join(dragonwildsArchiveDir(root), id)
	if err := os.MkdirAll(dst, 0755); err != nil {
		return "", err
	}
	for _, f := range present {
		if err := os.Rename(filepath.Join(dir, f), filepath.Join(dst, f)); err != nil {
			return "", fmt.Errorf("archive %s: %w", f, err)
		}
	}
	return id, nil
}

// worldFiles is a save and its backup copy.
func worldFiles(name string) []string { return []string{name + ".sav", name + ".backup"} }

// validWorldFileName accepts a plain file name with the given extension.
func validWorldFileName(name, ext string) bool {
	return name != "" && name == filepath.Base(name) && !strings.HasPrefix(name, ".") &&
		strings.EqualFold(filepath.Ext(name), ext) && !strings.ContainsAny(name, "/\\\x00\r\n") && len(name) <= 128
}

// dragonwildsWorldRequest loads a Dragonwilds server that is stopped, which
// world changes need: the game rewrites saves while it runs.
func (a *App) dragonwildsWorldRequest(w http.ResponseWriter, r *http.Request) (*ServerManager, ServerSettings, bool) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return nil, s, false
	}
	if !requireCapability(w, s, "World management", func(c GameCapability) bool { return c.Worlds }) {
		return nil, s, false
	}
	if r.Method != http.MethodGet && m.IsRunning() {
		jsonError(w, 409, "stop the server before changing its worlds; the game rewrites saves while it runs")
		return nil, s, false
	}
	return m, s, true
}

func (a *App) handleWorldsList(w http.ResponseWriter, r *http.Request) {
	_, s, ok := a.dragonwildsWorldRequest(w, r)
	if !ok {
		return
	}
	root := a.serverRoot(s)
	worlds, err := listDragonwildsWorlds(root)
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	rel, _ := filepath.Rel(root, dragonwildsSaveDir(root))
	jsonOut(w, 200, map[string]any{
		"saveDir": filepath.ToSlash(rel), "defaultWorldName": s.DragonwildsWorldName,
		"worlds": worlds, "archives": listDragonwildsArchives(root),
	})
}

// useDragonwildsWorld makes a save the one the server loads next: it becomes
// the newest file, and DefaultWorldName is set to its name.
func (a *App) useDragonwildsWorld(m *ServerManager, s ServerSettings, name string) error {
	root := a.serverRoot(s)
	path := filepath.Join(dragonwildsSaveDir(root), name+".sav")
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		return err
	}
	if n := len([]rune(name)); n >= 1 && n <= 32 {
		if err := a.setDragonwildsWorldName(s.ID, name); err != nil {
			return err
		}
		s.DragonwildsWorldName = name
	}
	if m.Installed() {
		if err := writeDragonwildsConfig(root, s); err != nil {
			return err
		}
	}
	m.appendLog("controller", "World "+name+" will load on the next start")
	return nil
}

func (a *App) setDragonwildsWorldName(id, name string) error {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	for i := range a.cfg.Servers {
		if a.cfg.Servers[i].ID == id {
			a.cfg.Servers[i].DragonwildsWorldName = name
			return a.saveConfigLocked()
		}
	}
	return errors.New("server not found")
}

func (a *App) handleWorldUpload(w http.ResponseWriter, r *http.Request) {
	m, s, ok := a.dragonwildsWorldRequest(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2*dragonwildsWorldUploadLimit+(1<<20))
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		jsonError(w, 400, "invalid upload: "+err.Error())
		return
	}
	defer r.MultipartForm.RemoveAll()
	save, saveHeader, err := r.FormFile("file")
	if err != nil {
		jsonError(w, 400, "choose a world .sav file")
		return
	}
	defer save.Close()
	saveName := filepath.Base(saveHeader.Filename)
	if !validWorldFileName(saveName, ".sav") {
		jsonError(w, 400, "the world file must be a .sav file with a plain name")
		return
	}
	name := strings.TrimSuffix(saveName, filepath.Ext(saveName))
	saveName = name + ".sav"
	backup, backupHeader, backupErr := r.FormFile("backup")
	if backupErr == nil {
		defer backup.Close()
		if !validWorldFileName(filepath.Base(backupHeader.Filename), ".backup") {
			jsonError(w, 400, "the backup file must be a .backup file")
			return
		}
	}
	root := a.serverRoot(s)
	dir := dragonwildsSaveDir(root)
	if err := os.MkdirAll(dir, 0755); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	archived, err := archiveDragonwildsFiles(root, worldFiles(name))
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	if err := streamToFile(save, filepath.Join(dir, saveName), 0644); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	if backupErr == nil {
		if err := streamToFile(backup, filepath.Join(dir, name+".backup"), 0644); err != nil {
			jsonError(w, 500, err.Error())
			return
		}
	}
	msg := "Uploaded world " + name
	if archived != "" {
		msg += "; the previous copy was moved to world-backups/" + archived
	}
	m.appendLog("controller", msg)
	if r.FormValue("activate") != "false" {
		if err := a.useDragonwildsWorld(m, s, name); err != nil {
			jsonError(w, 500, "world uploaded, but it could not be selected: "+err.Error())
			return
		}
	}
	jsonOut(w, 201, map[string]any{"ok": true, "name": name, "archived": archived})
}

type worldNameRequest struct {
	Name    string `json:"name"`
	Archive string `json:"archive"`
}

func decodeWorldName(w http.ResponseWriter, r *http.Request) (worldNameRequest, bool) {
	var req worldNameRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		jsonError(w, 400, "invalid request")
		return req, false
	}
	req.Name = strings.TrimSpace(req.Name)
	if !validWorldFileName(req.Name+".sav", ".sav") {
		jsonError(w, 400, "invalid world name")
		return req, false
	}
	return req, true
}

func (a *App) handleWorldUse(w http.ResponseWriter, r *http.Request) {
	m, s, ok := a.dragonwildsWorldRequest(w, r)
	if !ok {
		return
	}
	req, ok := decodeWorldName(w, r)
	if !ok {
		return
	}
	if !fileExists(filepath.Join(dragonwildsSaveDir(a.serverRoot(s)), req.Name+".sav")) {
		jsonError(w, 404, "world not found")
		return
	}
	if err := a.useDragonwildsWorld(m, s, req.Name); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true})
}

func (a *App) handleWorldArchive(w http.ResponseWriter, r *http.Request) {
	m, s, ok := a.dragonwildsWorldRequest(w, r)
	if !ok {
		return
	}
	req, ok := decodeWorldName(w, r)
	if !ok {
		return
	}
	id, err := archiveDragonwildsFiles(a.serverRoot(s), worldFiles(req.Name))
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	if id == "" {
		jsonError(w, 404, "world not found")
		return
	}
	m.appendLog("controller", "Moved world "+req.Name+" to world-backups/"+id)
	jsonOut(w, 200, map[string]any{"ok": true, "archive": id})
}

// handleWorldRestore copies a world back from world-backups, archiving any
// current save with the same name first.
func (a *App) handleWorldRestore(w http.ResponseWriter, r *http.Request) {
	m, s, ok := a.dragonwildsWorldRequest(w, r)
	if !ok {
		return
	}
	req, ok := decodeWorldName(w, r)
	if !ok {
		return
	}
	root := a.serverRoot(s)
	if req.Archive == "" || req.Archive != filepath.Base(req.Archive) || strings.HasPrefix(req.Archive, ".") {
		jsonError(w, 400, "invalid archive")
		return
	}
	src := filepath.Join(dragonwildsArchiveDir(root), req.Archive)
	if !fileExists(filepath.Join(src, req.Name+".sav")) {
		jsonError(w, 404, "that world is not in this archive")
		return
	}
	if _, err := archiveDragonwildsFiles(root, worldFiles(req.Name)); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	dir := dragonwildsSaveDir(root)
	if err := os.MkdirAll(dir, 0755); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	for _, f := range worldFiles(req.Name) {
		if fileExists(filepath.Join(src, f)) {
			if err := copyFile(filepath.Join(src, f), filepath.Join(dir, f), 0644); err != nil {
				jsonError(w, 500, err.Error())
				return
			}
		}
	}
	m.appendLog("controller", "Restored world "+req.Name+" from world-backups/"+req.Archive)
	if err := a.useDragonwildsWorld(m, s, req.Name); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true})
}

// handleWorldNew moves every save to world-backups so the server creates a
// new Standard world called name on its next start.
func (a *App) handleWorldNew(w http.ResponseWriter, r *http.Request) {
	m, s, ok := a.dragonwildsWorldRequest(w, r)
	if !ok {
		return
	}
	req, ok := decodeWorldName(w, r)
	if !ok {
		return
	}
	if n := len([]rune(req.Name)); n > 32 {
		jsonError(w, 400, "Dragonwilds world names are 1–32 characters")
		return
	}
	root := a.serverRoot(s)
	entries, _ := os.ReadDir(dragonwildsSaveDir(root))
	var files []string
	for _, e := range entries {
		if ext := strings.ToLower(filepath.Ext(e.Name())); !e.IsDir() && (ext == ".sav" || ext == ".backup") {
			files = append(files, e.Name())
		}
	}
	id, err := archiveDragonwildsFiles(root, files)
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	if err := a.setDragonwildsWorldName(s.ID, req.Name); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	s.DragonwildsWorldName = req.Name
	if m.Installed() {
		if err := writeDragonwildsConfig(root, s); err != nil {
			jsonError(w, 500, err.Error())
			return
		}
	}
	msg := "The next start creates a new world, " + req.Name
	if id != "" {
		msg += "; previous saves were moved to world-backups/" + id
	}
	m.appendLog("controller", msg)
	jsonOut(w, 200, map[string]any{"ok": true, "archive": id})
}
