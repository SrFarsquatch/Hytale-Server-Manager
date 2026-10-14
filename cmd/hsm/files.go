package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxManagedUpload = 64 << 20
const maxEditableFile = 2 << 20

type ManagedFileDTO struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Directory bool   `json:"directory"`
	Size      int64  `json:"size"`
	Modified  string `json:"modified"`
}

func (a *App) handleFilesList(w http.ResponseWriter, r *http.Request) {
	_, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	rel := r.URL.Query().Get("path")
	dir, clean, err := managedPath(a.serverRoot(s), rel, true)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	info, err := os.Stat(dir)
	if err != nil {
		jsonError(w, 404, "directory not found")
		return
	}
	if !info.IsDir() {
		jsonError(w, 400, "path is not a directory")
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	out := make([]ManagedFileDTO, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".hsm-tmp-") || strings.HasPrefix(entry.Name(), ".hsm-restore-") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		out = append(out, ManagedFileDTO{Name: entry.Name(), Path: filepath.ToSlash(filepath.Join(clean, entry.Name())), Directory: entry.IsDir(), Size: info.Size(), Modified: info.ModTime().Format(time.RFC3339)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Directory != out[j].Directory {
			return out[i].Directory
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	jsonOut(w, 200, map[string]any{"path": filepath.ToSlash(clean), "parent": filepath.ToSlash(filepath.Dir(clean)), "entries": out})
}

func (a *App) handleFilesRead(w http.ResponseWriter, r *http.Request) {
	_, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	path, clean, err := managedPath(a.serverRoot(s), r.URL.Query().Get("path"), false)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			jsonError(w, 404, "file not found")
		} else {
			jsonError(w, 500, err.Error())
		}
		return
	}
	if !info.Mode().IsRegular() {
		jsonError(w, 400, "only regular files can be opened")
		return
	}
	if info.Size() > maxEditableFile {
		jsonError(w, 413, "file is larger than the 2 MiB in-app editor limit; download it to edit externally")
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonOut(w, 200, map[string]any{"path": filepath.ToSlash(clean), "name": filepath.Base(path), "content": string(b), "size": info.Size(), "modified": info.ModTime().Format(time.RFC3339)})
}

func (a *App) handleFilesWrite(w http.ResponseWriter, r *http.Request) {
	_, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	var body struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decodeJSON(r, &body); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	if len(body.Content) > maxEditableFile {
		jsonError(w, 413, "file exceeds the 2 MiB in-app editor limit")
		return
	}
	path, clean, err := managedPath(a.serverRoot(s), body.Path, false)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		jsonError(w, 400, "cannot overwrite a directory")
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	if err := atomicWrite(path, []byte(body.Content), 0644); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true, "path": filepath.ToSlash(clean)})
}

func (a *App) handleFilesMkdir(w http.ResponseWriter, r *http.Request) {
	_, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	var body struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &body); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	path, clean, err := managedPath(a.serverRoot(s), body.Path, false)
	if err != nil || clean == "." {
		jsonError(w, 400, "choose a valid new folder path")
		return
	}
	if err := os.Mkdir(path, 0755); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	jsonOut(w, 201, map[string]any{"ok": true, "path": filepath.ToSlash(clean)})
}

func (a *App) handleFilesUpload(w http.ResponseWriter, r *http.Request) {
	_, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxManagedUpload+(1<<20))
	if err := r.ParseMultipartForm(maxManagedUpload); err != nil {
		jsonError(w, 400, "upload too large or invalid multipart request (64 MiB maximum)")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		jsonError(w, 400, "choose a file to upload")
		return
	}
	defer file.Close()
	name := filepath.Base(strings.ReplaceAll(header.Filename, "\\", "/"))
	if name == "." || name == "/" || name == "" || strings.ContainsRune(name, 0) {
		jsonError(w, 400, "invalid filename")
		return
	}
	relDir := r.FormValue("path")
	targetRel := filepath.ToSlash(filepath.Join(relDir, name))
	target, clean, err := managedPath(a.serverRoot(s), targetRel, false)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	dir, _, err := managedPath(a.serverRoot(s), relDir, true)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		jsonError(w, 400, "upload destination is not a directory")
		return
	}
	tmp, err := os.CreateTemp(dir, ".hsm-tmp-*")
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	n, copyErr := io.Copy(tmp, io.LimitReader(file, maxManagedUpload+1))
	closeErr := tmp.Close()
	if copyErr != nil {
		jsonError(w, 500, copyErr.Error())
		return
	}
	if n > maxManagedUpload {
		jsonError(w, 413, "upload exceeds the 64 MiB limit")
		return
	}
	if closeErr != nil {
		jsonError(w, 500, closeErr.Error())
		return
	}
	if err := os.Rename(tmpName, target); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonOut(w, 201, map[string]any{"ok": true, "path": filepath.ToSlash(clean), "size": n})
}

func (a *App) handleFilesDownload(w http.ResponseWriter, r *http.Request) {
	_, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	path, _, err := managedPath(a.serverRoot(s), r.URL.Query().Get("path"), false)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		jsonError(w, 404, "file not found")
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(path)))
	http.ServeFile(w, r, path)
}

func (a *App) handleFilesDelete(w http.ResponseWriter, r *http.Request) {
	_, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	path, clean, err := managedPath(a.serverRoot(s), r.URL.Query().Get("path"), false)
	if err != nil || clean == "." {
		jsonError(w, 400, "choose a file or folder inside the server directory")
		return
	}
	if _, err := os.Lstat(path); err != nil {
		jsonError(w, 404, "file or folder not found")
		return
	}
	if err := os.RemoveAll(path); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true, "path": filepath.ToSlash(clean)})
}

func (a *App) handleFilesRename(w http.ResponseWriter, r *http.Request) {
	_, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	var body struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	if body.Name == "" || body.Name == "." || body.Name == ".." || strings.ContainsAny(body.Name, "/\\") || strings.ContainsRune(body.Name, 0) {
		jsonError(w, 400, "enter a valid filename")
		return
	}
	oldPath, oldClean, err := managedPath(a.serverRoot(s), body.Path, false)
	if err != nil || oldClean == "." {
		jsonError(w, 400, "choose an existing file or folder")
		return
	}
	newRel := filepath.Join(filepath.Dir(oldClean), body.Name)
	newPath, newClean, err := managedPath(a.serverRoot(s), newRel, false)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	if _, err := os.Lstat(oldPath); err != nil {
		jsonError(w, 404, "file or folder not found")
		return
	}
	if _, err := os.Lstat(newPath); err == nil {
		jsonError(w, 409, "a file or folder with that name already exists")
		return
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true, "path": filepath.ToSlash(newClean)})
}

// managedPath confines all browser file operations to one server's data root and rejects symlinks.
func managedPath(root, rel string, allowRoot bool) (string, string, error) {
	if strings.ContainsRune(rel, 0) || strings.Contains(rel, "\\") || filepath.IsAbs(rel) {
		return "", "", fmt.Errorf("invalid relative path")
	}
	clean := filepath.Clean(rel)
	if clean == "" {
		clean = "."
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path must stay inside this server's directory")
	}
	if clean == "." && !allowRoot {
		return "", "", fmt.Errorf("choose a file path")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	current := rootAbs
	if info, err := os.Lstat(current); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", "", fmt.Errorf("server data root cannot be a symbolic link")
	}
	if clean != "." {
		for _, part := range strings.Split(clean, string(filepath.Separator)) {
			if part == "" || part == "." || part == ".." {
				return "", "", fmt.Errorf("invalid path segment")
			}
			current = filepath.Join(current, part)
			info, err := os.Lstat(current)
			if err == nil && info.Mode()&os.ModeSymlink != 0 {
				return "", "", fmt.Errorf("symbolic links are not supported in the in-app file manager")
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return "", "", err
			}
		}
	}
	return current, clean, nil
}

var _ = json.Valid
