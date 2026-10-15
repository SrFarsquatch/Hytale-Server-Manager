package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A server export is a .tar.gz holding hsm-export.json (the server's
// settings, including its passwords, and schedules) followed by the server's
// files under files/. Importing it creates a new server, so a server can move
// to a fresh install of HSM or another machine.

const exportManifestName = "hsm-export.json"

type serverExport struct {
	Format     int            `json:"format"`
	App        string         `json:"app"`
	AppVersion string         `json:"appVersion"`
	ExportedAt string         `json:"exportedAt"`
	Server     ServerSettings `json:"server"`
	Schedules  []Schedule     `json:"schedules,omitempty"`
}

// exportEntries are the folders an export copies from the server folder.
// The game folder holds the installed server, its worlds, settings and mods.
// Snapshots and backups are optional because they can be large.
func exportEntries(withBackups bool) []string {
	if withBackups {
		return []string{"game", "snapshots", "world-backups"}
	}
	return []string{"game"}
}

// skipWithoutBackups lists folders inside game/ that hold backups.
var skipWithoutBackups = []string{filepath.Join("game", "Server", "backups")}

func (a *App) handleServerExport(w http.ResponseWriter, r *http.Request) {
	m, s, err := a.serverByID(r.PathValue("id"))
	if err != nil {
		jsonError(w, 404, "server not found")
		return
	}
	if m.IsRunning() || m.InstallBusy() {
		jsonError(w, 409, "stop the server before exporting it, so its world is not changing while it is copied")
		return
	}
	withBackups := r.URL.Query().Get("backups") == "1"
	a.cfgMu.RLock()
	schedules := append([]Schedule(nil), a.cfg.Schedules[s.ID]...)
	a.cfgMu.RUnlock()
	manifest, err := json.MarshalIndent(serverExport{
		Format: 1, App: "hytale-server-manager", AppVersion: version,
		ExportedAt: time.Now().UTC().Format(time.RFC3339), Server: s, Schedules: schedules,
	}, "", "  ")
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}

	name := fmt.Sprintf("hsm-%s-%s.tar.gz", s.ID, time.Now().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	m.appendLog("controller", "Exporting server to "+name)
	if err := writeServerExport(w, a.serverRoot(s), manifest, withBackups); err != nil {
		// Headers are sent, so the client sees a truncated download.
		log.Printf("export %s: %v", s.ID, err)
		m.appendLog("controller", "Export failed: "+err.Error())
		return
	}
	m.appendLog("controller", "Export finished")
}

func writeServerExport(w io.Writer, root string, manifest []byte, withBackups bool) error {
	gz, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: exportManifestName, Mode: 0o600, Size: int64(len(manifest)), ModTime: time.Now(), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err := tw.Write(manifest); err != nil {
		return err
	}
	for _, entry := range exportEntries(withBackups) {
		base := filepath.Join(root, entry)
		if _, err := os.Lstat(base); errors.Is(err, os.ErrNotExist) {
			continue
		}
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if !withBackups {
				for _, skip := range skipWithoutBackups {
					if rel == skip {
						return filepath.SkipDir
					}
				}
			}
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			link := ""
			if info.Mode()&os.ModeSymlink != 0 {
				if link, err = os.Readlink(path); err != nil {
					return err
				}
			} else if !info.IsDir() && !info.Mode().IsRegular() {
				return nil // sockets, devices and pipes are not part of a server
			}
			hdr, err := tar.FileInfoHeader(info, link)
			if err != nil {
				return err
			}
			hdr.Name = "files/" + filepath.ToSlash(rel)
			if info.IsDir() {
				hdr.Name += "/"
			}
			hdr.Uname, hdr.Gname = "", ""
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.CopyN(tw, f, hdr.Size)
			return err
		})
		if err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// handleServerImport reads an export from the request body and creates a new
// server from it. The body is the .tar.gz itself, streamed rather than
// buffered, since a server with its game files can be several gigabytes.
func (a *App) handleServerImport(w http.ResponseWriter, r *http.Request) {
	gz, err := gzip.NewReader(r.Body)
	if err != nil {
		jsonError(w, 400, "this is not an HSM server export (.tar.gz)")
		return
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	hdr, err := tr.Next()
	if err != nil || hdr.Name != exportManifestName || hdr.Size > 4<<20 {
		jsonError(w, 400, "this is not an HSM server export: "+exportManifestName+" must come first")
		return
	}
	var manifest serverExport
	if err := json.NewDecoder(io.LimitReader(tr, 4<<20)).Decode(&manifest); err != nil || manifest.Format != 1 {
		jsonError(w, 400, "unsupported or damaged export manifest")
		return
	}
	s := manifest.Server
	game := normalizeGameType(s.GameType)
	adapter, adapterErr := gameAdapterFor(game)
	entry, available := gameCatalogEntryFor(game)
	if adapterErr != nil || !available {
		jsonError(w, 400, fmt.Sprintf("this release cannot run %q servers", game))
		return
	}
	s.GameType = game
	if name := strings.TrimSpace(s.ServerName); len(name) < 2 || len(name) > 64 {
		s.ServerName = "Imported server"
	}

	id, root, err := a.reserveImportFolder(s.ServerName)
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	staging := root + ".importing"
	cleanup := func() { _ = os.RemoveAll(staging) }
	if err := os.MkdirAll(staging, 0o755); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	if err := extractServerFiles(tr, staging); err != nil {
		cleanup()
		jsonError(w, 400, "import failed: "+err.Error())
		return
	}
	if err := ensureServerDirs(staging); err != nil {
		cleanup()
		jsonError(w, 500, err.Error())
		return
	}

	rules := adapter.Rules()
	min, max := rules.portRange()
	s.ID, s.StorageDir, s.PortMin, s.PortMax = id, filepath.Join("servers", id), min, max
	if s.Port < min || s.Port > max || a.portInUse(s.Port, "") {
		if s.Port = a.firstFreePort(min, max); s.Port == 0 {
			cleanup()
			jsonError(w, 409, fmt.Sprintf("every %s port in %d-%d is in use; free one and import again", strings.ToUpper(entry.Protocol), min, max))
			return
		}
	}
	schedules, schedErr := normalizeSchedules(manifest.Schedules, game)
	if err := os.Rename(staging, root); err != nil {
		cleanup()
		jsonError(w, 500, err.Error())
		return
	}

	a.cfgMu.Lock()
	a.cfg.Servers = append(a.cfg.Servers, s)
	if schedErr == nil && len(schedules) > 0 {
		if a.cfg.Schedules == nil {
			a.cfg.Schedules = map[string][]Schedule{}
		}
		a.cfg.Schedules[id] = schedules
	}
	err = a.saveConfigLocked()
	if err != nil {
		a.cfg.Servers = a.cfg.Servers[:len(a.cfg.Servers)-1]
		delete(a.cfg.Schedules, id)
	}
	a.cfgMu.Unlock()
	if err != nil {
		_ = os.RemoveAll(root)
		jsonError(w, 500, err.Error())
		return
	}
	m := a.newServerManager(s)
	a.managersMu.Lock()
	a.managers[id] = m
	a.managersMu.Unlock()
	if game == "runescape-dragonwilds" {
		a.rememberDragonwildsOwner(s.DragonwildsOwnerID)
	}
	m.appendLog("controller", fmt.Sprintf("Imported from an export made %s by HSM %s", manifest.ExportedAt, manifest.AppVersion))
	if schedErr != nil {
		m.appendLog("controller", "Schedules were not imported: "+schedErr.Error())
	}
	jsonOut(w, 201, settingsView(s))
}

// reserveImportFolder picks a server ID whose folder does not exist yet.
func (a *App) reserveImportFolder(name string) (string, string, error) {
	base := slugify(name)
	if base == "" {
		base = "imported"
	}
	for i := 0; i < 50; i++ {
		id := a.uniqueServerID(base)
		if i > 0 {
			b := make([]byte, 3)
			_, _ = rand.Read(b)
			id = a.uniqueServerID(base + "-" + hex.EncodeToString(b))
		}
		root := filepath.Join(a.dataDir, "servers", id)
		if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
			if _, err := os.Lstat(root + ".importing"); errors.Is(err, os.ErrNotExist) {
				return id, root, os.MkdirAll(filepath.Dir(root), 0o755)
			}
		}
	}
	return "", "", errors.New("could not choose a folder for the imported server")
}

// extractServerFiles writes the files/ entries of an export into dst. Paths
// that would leave dst, and links that point outside it, are refused.
func extractServerFiles(tr *tar.Reader, dst string) error {
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("damaged archive: %w", err)
		}
		rel, ok := strings.CutPrefix(hdr.Name, "files/")
		if !ok {
			return fmt.Errorf("unexpected entry %q", hdr.Name)
		}
		rel = strings.TrimSuffix(rel, "/")
		if rel == "" {
			continue
		}
		clean := filepath.Clean(filepath.FromSlash(rel))
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe path %q", hdr.Name)
		}
		target := filepath.Join(dst, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(hdr.Mode).Perm() & 0o755
			if mode == 0 {
				mode = 0o644
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode|0o600)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(f, tr)
			if err := f.Close(); copyErr == nil {
				copyErr = err
			}
			if copyErr != nil {
				return copyErr
			}
		case tar.TypeSymlink:
			link := hdr.Linkname
			resolved := filepath.Clean(filepath.Join(filepath.Dir(target), link))
			if filepath.IsAbs(link) || (resolved != dst && !strings.HasPrefix(resolved, dst+string(filepath.Separator))) {
				return fmt.Errorf("link %q points outside the server folder", hdr.Name)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(link, target); err != nil {
				return err
			}
		default:
			// Other entry types are not part of a server export; skip them.
		}
	}
}
