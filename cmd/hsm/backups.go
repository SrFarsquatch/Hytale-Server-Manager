package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type FileInfoDTO struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Mod  string `json:"modified"`
}

func (a *App) handleBackupsList(w http.ResponseWriter, r *http.Request) {
	_, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if !requireCapability(w, s, "Backups", func(c GameCapability) bool { return c.Backups }) {
		return
	}
	a.pruneBackupRetention(s)
	root := a.serverRoot(s)
	native := []FileInfoDTO{}
	if gameCapabilitiesFor(s.GameType).NativeBackups {
		native = listAllFiles(filepath.Join(pathsFor(root).Game(), "Server", "backups"))
	}
	snapshots := listFiles(pathsFor(root).Snapshots(), ".tar.gz")
	if snapshots == nil {
		snapshots = []FileInfoDTO{} // a server with no snapshots yet has no folder
	}
	if native == nil {
		native = []FileInfoDTO{}
	}
	jsonOut(w, 200, map[string]any{"snapshots": snapshots, "native": native})
}

func (a *App) backupMaintenanceLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		a.cfgMu.RLock()
		servers := append([]ServerSettings(nil), a.cfg.Servers...)
		a.cfgMu.RUnlock()
		for _, s := range servers {
			a.pruneBackupRetention(s)
		}
	}
}

func (a *App) pruneBackupRetention(s ServerSettings) {
	root := a.serverRoot(s)
	pruneBackupFiles(filepath.Join(pathsFor(root).Game(), "Server", "backups"), s.BackupRetention, func(name string) bool { return true })
	pruneBackupFiles(pathsFor(root).Snapshots(), s.SnapshotRetention, func(name string) bool {
		return strings.HasPrefix(name, "snapshot-") && strings.HasSuffix(name, ".tar.gz")
	})
}

func pruneBackupFiles(dir string, keep int, include func(string) bool) {
	if keep < 1 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type backupEntry struct {
		path string
		mod  time.Time
	}
	var files []backupEntry
	for _, entry := range entries {
		if entry.IsDir() || !include(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		files = append(files, backupEntry{path: filepath.Join(dir, entry.Name()), mod: info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	for _, old := range files[minInt(keep, len(files)):] {
		if err := os.Remove(old.path); err != nil {
			log.Printf("backup retention: could not remove %s: %v", old.path, err)
		}
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (a *App) handleNativeBackup(w http.ResponseWriter, r *http.Request) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if !requireCapability(w, s, "Native backups", func(c GameCapability) bool { return c.NativeBackups }) {
		return
	}
	if !m.IsRunning() {
		jsonError(w, 409, "server must be running for a native Hytale backup")
		return
	}
	if err := m.SendCommand("/backup"); err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	go func(server ServerSettings) { time.Sleep(15 * time.Second); a.pruneBackupRetention(server) }(s)
	jsonOut(w, 202, map[string]any{"ok": true})
}

func (a *App) handleSnapshotCreate(w http.ResponseWriter, r *http.Request) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if !requireCapability(w, s, "Snapshots", func(c GameCapability) bool { return c.Backups }) {
		return
	}
	name, status, err := a.createSnapshot(m, s)
	if err != nil {
		jsonError(w, status, err.Error())
		return
	}
	jsonOut(w, 201, map[string]any{"ok": true, "name": name})
}

// createSnapshot archives a server according to its game's snapshot spec. On
// failure it returns the HTTP status that describes the error.
func (a *App) createSnapshot(m *ServerManager, s ServerSettings) (string, int, error) {
	root := a.serverRoot(s)
	spec, ok := snapshotSpecFor(root, s)
	if !ok {
		return "", 409, errors.New("snapshots are not supported for this game")
	}
	live := gameCapabilitiesFor(s.GameType).LiveSnapshots
	if m.IsRunning() && !live {
		return "", 409, errors.New("stop the server before making a full restorable snapshot")
	}
	if !m.backupMu.TryLock() {
		return "", 409, errors.New("a snapshot or restore is already in progress for this server")
	}
	defer m.backupMu.Unlock()
	name := "snapshot-" + time.Now().Format("20060102-150405") + ".tar.gz"
	dst := filepath.Join(pathsFor(root).Snapshots(), name)
	archive := func() error { return createTarGz(dst, spec.Base, spec.Include) }
	var err error
	if m.IsRunning() {
		err = m.withMinecraftSaveHold(minecraftSaveTimeout, archive)
	} else {
		err = archive()
	}
	if err != nil {
		_ = os.Remove(dst)
		return "", 500, err
	}
	a.pruneBackupRetention(s)
	return name, 0, nil
}

func (a *App) handleSnapshotRestore(w http.ResponseWriter, r *http.Request) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if !requireCapability(w, s, "Snapshots", func(c GameCapability) bool { return c.Backups }) {
		return
	}
	if m.IsRunning() {
		jsonError(w, 409, "stop the server before restoring a snapshot")
		return
	}
	if !m.backupMu.TryLock() {
		jsonError(w, 409, "a snapshot or restore is already in progress for this server")
		return
	}
	defer m.backupMu.Unlock()
	name := filepath.Base(r.PathValue("name"))
	if !strings.HasSuffix(name, ".tar.gz") {
		jsonError(w, 400, "invalid snapshot")
		return
	}
	root := a.serverRoot(s)
	src := filepath.Join(pathsFor(root).Snapshots(), name)
	if _, err := os.Stat(src); err != nil {
		jsonError(w, 404, "snapshot not found")
		return
	}
	spec, ok := snapshotSpecFor(root, s)
	if !ok {
		jsonError(w, 409, "snapshots are not supported for this game")
		return
	}
	// Never restore without a copy of what is about to be replaced.
	safety := filepath.Join(pathsFor(root).Snapshots(), "pre-restore-"+time.Now().Format("20060102-150405")+".tar.gz")
	if err := createTarGz(safety, spec.Base, spec.Include); err != nil {
		_ = os.Remove(safety)
		jsonError(w, 500, "could not create the safety snapshot, so nothing was restored: "+err.Error())
		return
	}
	if err := restoreSnapshot(src, spec); err != nil {
		jsonError(w, 500, err.Error()+" (safety snapshot: "+filepath.Base(safety)+")")
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true, "safetySnapshot": filepath.Base(safety)})
}

func (a *App) handleSnapshotDelete(w http.ResponseWriter, r *http.Request) {
	_, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if !requireCapability(w, s, "Snapshots", func(c GameCapability) bool { return c.Backups }) {
		return
	}
	name := filepath.Base(r.PathValue("name"))
	if !strings.HasSuffix(name, ".tar.gz") {
		jsonError(w, 400, "invalid snapshot")
		return
	}
	if err := os.Remove(filepath.Join(pathsFor(a.serverRoot(s)).Snapshots(), name)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			jsonError(w, 404, "snapshot not found")
		} else {
			jsonError(w, 500, err.Error())
		}
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true})
}

func listFiles(dir, suffix string) []FileInfoDTO {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []FileInfoDTO{}
	}
	var out []FileInfoDTO
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), suffix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, FileInfoDTO{Name: e.Name(), Size: info.Size(), Mod: info.ModTime().Format(time.RFC3339)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mod > out[j].Mod })
	return out
}

func listAllFiles(root string) []FileInfoDTO {
	var out []FileInfoDTO
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		out = append(out, FileInfoDTO{Name: rel, Size: info.Size(), Mod: info.ModTime().Format(time.RFC3339)})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Mod > out[j].Mod })
	return out
}

func createTarGz(dst, base string, include []string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	closeAll := func() error {
		e1 := tw.Close()
		e2 := gz.Close()
		e3 := f.Close()
		if e1 != nil {
			return e1
		}
		if e2 != nil {
			return e2
		}
		return e3
	}
	for _, name := range include {
		path := filepath.Join(base, name)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		err := filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(base, p)
			if err != nil {
				return err
			}
			h, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			h.Name = filepath.ToSlash(rel)
			if err := tw.WriteHeader(h); err != nil {
				return err
			}
			if info.Mode().IsRegular() {
				in, err := os.Open(p)
				if err != nil {
					return err
				}
				_, cpErr := io.Copy(tw, in)
				clErr := in.Close()
				if cpErr != nil {
					return cpErr
				}
				return clErr
			}
			return nil
		})
		if err != nil {
			_ = closeAll()
			return err
		}
	}
	return closeAll()
}

func extractTarGz(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		clean := filepath.Clean(h.Name)
		if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("unsafe snapshot path: %s", h.Name)
		}
		target := filepath.Join(dst, clean)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(h.Mode))
			if err != nil {
				return err
			}
			_, e1 := io.Copy(out, tr)
			e2 := out.Close()
			if e1 != nil {
				return e1
			}
			if e2 != nil {
				return e2
			}
		}
	}
	return nil
}
