package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const version = "0.5.0"

//go:embed web/*
var webFS embed.FS

type AdminConfig struct {
	Username     string `json:"username"`
	PasswordHash string `json:"passwordHash"`
}

type ServerSettings struct {
	ID              string `json:"id"`
	ServerName      string `json:"serverName"`
	Port            int    `json:"port"`
	MemoryMin       string `json:"memoryMin"`
	MemoryMax       string `json:"memoryMax"`
	AutoStart       bool   `json:"autoStart"`
	ExtraArgs       string `json:"extraArgs"`
	PublicHost      string `json:"publicHost"`
	BackupFrequency int    `json:"backupFrequency"`
	StorageDir      string `json:"storageDir,omitempty"`
	PortMin         int    `json:"portMin,omitempty"`
	PortMax         int    `json:"portMax,omitempty"`
}

type legacySettings struct {
	ServerName      string `json:"serverName"`
	Port            int    `json:"port"`
	MemoryMin       string `json:"memoryMin"`
	MemoryMax       string `json:"memoryMax"`
	AutoStart       bool   `json:"autoStart"`
	ExtraArgs       string `json:"extraArgs"`
	PublicHost      string `json:"publicHost"`
	BackupFrequency int    `json:"backupFrequency"`
}

type ControllerConfig struct {
	Admin    *AdminConfig     `json:"admin,omitempty"`
	Servers  []ServerSettings `json:"servers"`
	Settings *legacySettings  `json:"settings,omitempty"`
}

type Session struct {
	Username string
	Expires  time.Time
}

type App struct {
	dataDir    string
	cfgMu      sync.RWMutex
	cfg        ControllerConfig
	sessionsMu sync.Mutex
	sessions   map[string]Session
	managersMu sync.RWMutex
	managers   map[string]*ServerManager
}

type ServerManager struct {
	mu          sync.Mutex
	rootDir     string
	toolsDir    string
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	running     bool
	startedAt   time.Time
	exitCode    int
	lastExitAt  time.Time
	installBusy bool
	logs        *LogRing
	hub         *LogHub
}

type LogLine struct {
	Time   time.Time `json:"time"`
	Source string    `json:"source"`
	Line   string    `json:"line"`
}
type LogRing struct {
	mu    sync.RWMutex
	max   int
	lines []LogLine
}
type LogHub struct {
	mu      sync.Mutex
	clients map[chan LogLine]struct{}
}

type Status struct {
	Running          bool   `json:"running"`
	Installed        bool   `json:"installed"`
	InstallBusy      bool   `json:"installBusy"`
	PID              int    `json:"pid"`
	UptimeSeconds    int64  `json:"uptimeSeconds"`
	ExitCode         int    `json:"exitCode"`
	LastExitAt       string `json:"lastExitAt,omitempty"`
	MemoryBytes      int64  `json:"memoryBytes"`
	MemoryLimitBytes int64  `json:"memoryLimitBytes"`
	MeetsMinMemory   bool   `json:"meetsMinimumMemory"`
	ControllerVers   string `json:"controllerVersion"`
}

type ServerSummary struct {
	ID            string `json:"id"`
	ServerName    string `json:"serverName"`
	Port          int    `json:"port"`
	PublicHost    string `json:"publicHost"`
	Running       bool   `json:"running"`
	Installed     bool   `json:"installed"`
	InstallBusy   bool   `json:"installBusy"`
	UptimeSeconds int64  `json:"uptimeSeconds"`
	StorageDir    string `json:"storageDir"`
	Primary       bool   `json:"primary"`
}

func main() {
	dataDir := envFirst([]string{"HSM_DATA_DIR", "ORBIS_DATA_DIR"}, "/data")
	if err := ensureRootDirs(dataDir); err != nil {
		log.Fatal(err)
	}
	app := &App{dataDir: dataDir, sessions: map[string]Session{}, managers: map[string]*ServerManager{}}
	if err := app.loadConfig(); err != nil {
		log.Printf("config: %v", err)
	}
	app.ensureManagers()

	mux := http.NewServeMux()
	app.routes(mux)
	addr := envFirst([]string{"HSM_LISTEN", "ORBIS_LISTEN"}, ":8080")
	srv := &http.Server{Addr: addr, Handler: securityHeaders(mux), ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}

	app.cfgMu.RLock()
	servers := append([]ServerSettings(nil), app.cfg.Servers...)
	app.cfgMu.RUnlock()
	for _, s := range servers {
		if s.AutoStart {
			if m, _, err := app.serverByID(s.ID); err == nil && m.Installed() {
				sid := s.ID
				go func() {
					time.Sleep(1200 * time.Millisecond)
					if err := app.startServerByID(sid); err != nil {
						m.appendLog("controller", "Auto-start failed: "+err.Error())
					}
				}()
			}
		}
	}

	log.Printf("Hytale Server Manager %s listening on %s", version, addr)
	log.Fatal(srv.ListenAndServe())
}

func (a *App) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, 200, map[string]any{"ok": true, "version": version})
	})
	mux.HandleFunc("GET /api/bootstrap", a.handleBootstrapStatus)
	mux.HandleFunc("POST /api/bootstrap", a.handleBootstrapCreate)
	mux.HandleFunc("POST /api/login", a.handleLogin)
	mux.HandleFunc("POST /api/logout", a.handleLogout)

	mux.Handle("GET /api/servers", a.auth(http.HandlerFunc(a.handleServersList)))
	mux.Handle("POST /api/servers", a.auth(http.HandlerFunc(a.handleServersCreate)))
	mux.Handle("DELETE /api/servers/{id}", a.auth(http.HandlerFunc(a.handleServersDelete)))
	mux.Handle("POST /api/servers/{id}/start", a.auth(http.HandlerFunc(a.handleServerStartByPath)))
	mux.Handle("POST /api/servers/{id}/stop", a.auth(http.HandlerFunc(a.handleServerStopByPath)))
	mux.Handle("POST /api/servers/{id}/restart", a.auth(http.HandlerFunc(a.handleServerRestartByPath)))

	mux.Handle("GET /api/status", a.auth(http.HandlerFunc(a.handleStatus)))
	mux.Handle("GET /api/settings", a.auth(http.HandlerFunc(a.handleSettingsGet)))
	mux.Handle("PUT /api/settings", a.auth(http.HandlerFunc(a.handleSettingsPut)))
	mux.Handle("POST /api/server/start", a.auth(http.HandlerFunc(a.handleStart)))
	mux.Handle("POST /api/server/stop", a.auth(http.HandlerFunc(a.handleStop)))
	mux.Handle("POST /api/server/restart", a.auth(http.HandlerFunc(a.handleRestart)))
	mux.Handle("POST /api/console", a.auth(http.HandlerFunc(a.handleConsole)))
	mux.Handle("GET /api/logs", a.auth(http.HandlerFunc(a.handleLogs)))
	mux.Handle("GET /api/events", a.auth(http.HandlerFunc(a.handleEvents)))

	mux.Handle("GET /api/server/config", a.auth(http.HandlerFunc(a.handleServerConfigGet)))
	mux.Handle("PUT /api/server/config", a.auth(http.HandlerFunc(a.handleServerConfigPut)))
	mux.Handle("GET /api/server/file/{name}", a.auth(http.HandlerFunc(a.handleServerFileGet)))
	mux.Handle("PUT /api/server/file/{name}", a.auth(http.HandlerFunc(a.handleServerFilePut)))
	mux.Handle("GET /api/mods", a.auth(http.HandlerFunc(a.handleModsList)))
	mux.Handle("POST /api/mods/upload", a.auth(http.HandlerFunc(a.handleModUpload)))
	mux.Handle("DELETE /api/mods/{name}", a.auth(http.HandlerFunc(a.handleModDelete)))
	mux.Handle("GET /api/backups", a.auth(http.HandlerFunc(a.handleBackupsList)))
	mux.Handle("POST /api/backups/native", a.auth(http.HandlerFunc(a.handleNativeBackup)))
	mux.Handle("POST /api/backups/snapshot", a.auth(http.HandlerFunc(a.handleSnapshotCreate)))
	mux.Handle("POST /api/backups/{name}/restore", a.auth(http.HandlerFunc(a.handleSnapshotRestore)))
	mux.Handle("DELETE /api/backups/{name}", a.auth(http.HandlerFunc(a.handleSnapshotDelete)))
	mux.Handle("GET /api/install/status", a.auth(http.HandlerFunc(a.handleInstallStatus)))
	mux.Handle("POST /api/install/downloader", a.auth(http.HandlerFunc(a.handleDownloaderUpload)))
	mux.Handle("POST /api/install/run-downloader", a.auth(http.HandlerFunc(a.handleDownloaderRun)))
	mux.Handle("POST /api/install/import-local", a.auth(http.HandlerFunc(a.handleImportLocal)))

	for _, x := range []struct{ path, cmd string }{
		{"/api/update/check", "/update check"}, {"/api/update/download", "/update download"}, {"/api/update/apply", "/update apply --confirm"},
		{"/api/update/status", "/update status"}, {"/api/update/cancel", "/update cancel"}, {"/api/update/setup", "/update setup"},
		{"/api/update/force-download", "/update download --force"}, {"/api/auth/device", "/auth login device"},
	} {
		cmd := x.cmd
		mux.Handle("POST "+x.path, a.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.commandShortcut(w, r, cmd) })))
	}

	sub, _ := fs.Sub(webFS, "web")
	fileServer := http.FileServer(http.FS(sub))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(sub, p); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}
		rr := r.Clone(r.Context())
		rr.URL.Path = "/"
		fileServer.ServeHTTP(w, rr)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func (a *App) handleBootstrapStatus(w http.ResponseWriter, r *http.Request) {
	a.cfgMu.RLock()
	configured := a.cfg.Admin != nil
	a.cfgMu.RUnlock()
	jsonOut(w, 200, map[string]any{"configured": configured})
}
func (a *App) handleBootstrapCreate(w http.ResponseWriter, r *http.Request) {
	var body struct{ Username, Password string }
	if err := decodeJSON(r, &body); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	if len(strings.TrimSpace(body.Username)) < 2 || len(body.Password) < 8 {
		jsonError(w, 400, "username must be at least 2 characters and password at least 8 characters")
		return
	}
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	if a.cfg.Admin != nil {
		jsonError(w, 409, "admin account already exists")
		return
	}
	h, err := hashPassword(body.Password)
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	a.cfg.Admin = &AdminConfig{Username: strings.TrimSpace(body.Username), PasswordHash: h}
	if err := a.saveConfigLocked(); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	token := a.newSession(body.Username)
	a.setSessionCookie(w, token)
	jsonOut(w, 201, map[string]any{"ok": true})
}
func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct{ Username, Password string }
	if err := decodeJSON(r, &body); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	a.cfgMu.RLock()
	admin := a.cfg.Admin
	a.cfgMu.RUnlock()
	if admin == nil || admin.Username != body.Username || !verifyPassword(admin.PasswordHash, body.Password) {
		jsonError(w, 401, "invalid username or password")
		return
	}
	token := a.newSession(body.Username)
	a.setSessionCookie(w, token)
	jsonOut(w, 200, map[string]any{"ok": true})
}
func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := sessionCookie(r); err == nil {
		a.sessionsMu.Lock()
		delete(a.sessions, c.Value)
		a.sessionsMu.Unlock()
	}
	clearSessionCookies(w)
	jsonOut(w, 200, map[string]any{"ok": true})
}
func (a *App) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := sessionCookie(r)
		if err != nil || !a.validSession(c.Value) {
			jsonError(w, 401, "authentication required")
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (a *App) newSession(username string) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := base64.RawURLEncoding.EncodeToString(b)
	a.sessionsMu.Lock()
	a.sessions[token] = Session{Username: username, Expires: time.Now().Add(7 * 24 * time.Hour)}
	a.sessionsMu.Unlock()
	return token
}
func (a *App) validSession(token string) bool {
	a.sessionsMu.Lock()
	defer a.sessionsMu.Unlock()
	s, ok := a.sessions[token]
	if !ok || time.Now().After(s.Expires) {
		delete(a.sessions, token)
		return false
	}
	return true
}
func (a *App) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: "hsm_session", Value: token, Path: "/", MaxAge: 7 * 24 * 3600, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: false})
}

func (a *App) handleServersList(w http.ResponseWriter, r *http.Request) {
	a.cfgMu.RLock()
	servers := append([]ServerSettings(nil), a.cfg.Servers...)
	a.cfgMu.RUnlock()
	out := make([]ServerSummary, 0, len(servers))
	for i, s := range servers {
		m, _, err := a.serverByID(s.ID)
		if err != nil {
			continue
		}
		st := m.Status()
		out = append(out, ServerSummary{ID: s.ID, ServerName: s.ServerName, Port: s.Port, PublicHost: s.PublicHost, Running: st.Running, Installed: st.Installed, InstallBusy: st.InstallBusy, UptimeSeconds: st.UptimeSeconds, StorageDir: s.StorageDir, Primary: i == 0})
	}
	jsonOut(w, 200, out)
}
func (a *App) handleServersCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name           string `json:"name"`
		Port           int    `json:"port"`
		SourceServerID string `json:"sourceServerId"`
	}
	if err := decodeJSON(r, &body); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "New Hytale Server"
	}
	id := a.uniqueServerID(slugify(name))
	if id == "" {
		jsonError(w, 500, "could not generate server id")
		return
	}
	port := body.Port
	if port == 0 {
		port = a.nextAvailablePort()
	}
	min, max, _ := deploymentPortRange()
	if port < min || port > max {
		jsonError(w, 400, fmt.Sprintf("port must be within the published UDP range %d-%d", min, max))
		return
	}
	if a.portInUse(port, "") {
		jsonError(w, 409, "that UDP port is already assigned to another server")
		return
	}
	s := defaultServerSettings(id, name, port, filepath.Join("servers", id))
	if err := ensureServerDirs(a.serverRoot(s)); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	if body.SourceServerID != "" {
		srcM, srcS, err := a.serverByID(body.SourceServerID)
		if err != nil {
			jsonError(w, 404, "source server not found")
			return
		}
		if srcM.IsRunning() {
			jsonError(w, 409, "stop the source server before cloning it")
			return
		}
		if !srcM.Installed() {
			jsonError(w, 409, "source server is not installed")
			return
		}
		if err := cloneGameInstall(a.serverRoot(srcS), a.serverRoot(s)); err != nil {
			jsonError(w, 500, "clone failed: "+err.Error())
			return
		}
	}
	a.cfgMu.Lock()
	a.cfg.Servers = append(a.cfg.Servers, s)
	err := a.saveConfigLocked()
	a.cfgMu.Unlock()
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	a.managersMu.Lock()
	a.managers[id] = NewServerManager(a.serverRoot(s), filepath.Join(a.dataDir, "tools"))
	a.managersMu.Unlock()
	_ = a.writeJVMOptionsFor(s)
	jsonOut(w, 201, s)
}
func (a *App) handleServersDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, s, err := a.serverByID(id)
	if err != nil {
		jsonError(w, 404, "server not found")
		return
	}
	if m.IsRunning() {
		jsonError(w, 409, "stop the server before deleting it")
		return
	}
	a.cfgMu.Lock()
	idx := -1
	for i, x := range a.cfg.Servers {
		if x.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		a.cfgMu.Unlock()
		jsonError(w, 404, "server not found")
		return
	}
	if idx == 0 || s.StorageDir == "." {
		a.cfgMu.Unlock()
		jsonError(w, 409, "the primary server cannot be deleted; rename or repurpose it instead")
		return
	}
	a.cfg.Servers = append(a.cfg.Servers[:idx], a.cfg.Servers[idx+1:]...)
	err = a.saveConfigLocked()
	a.cfgMu.Unlock()
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	a.managersMu.Lock()
	delete(a.managers, id)
	a.managersMu.Unlock()
	if err := os.RemoveAll(a.serverRoot(s)); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true})
}
func (a *App) handleServerStartByPath(w http.ResponseWriter, r *http.Request) {
	if err := a.startServerByID(r.PathValue("id")); err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	jsonOut(w, 202, map[string]any{"ok": true})
}
func (a *App) handleServerStopByPath(w http.ResponseWriter, r *http.Request) {
	m, _, err := a.serverByID(r.PathValue("id"))
	if err != nil {
		jsonError(w, 404, "server not found")
		return
	}
	if err := m.Stop(); err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	jsonOut(w, 202, map[string]any{"ok": true})
}
func (a *App) handleServerRestartByPath(w http.ResponseWriter, r *http.Request) {
	a.restartByID(w, r.PathValue("id"))
}

func (a *App) requestServer(r *http.Request) (*ServerManager, ServerSettings, error) {
	id := strings.TrimSpace(r.Header.Get("X-HSM-Server"))
	if id == "" {
		id = strings.TrimSpace(r.Header.Get("X-Orbis-Server")) // legacy client compatibility
	}
	if id == "" {
		id = strings.TrimSpace(r.URL.Query().Get("server"))
	}
	if id == "" {
		a.cfgMu.RLock()
		if len(a.cfg.Servers) > 0 {
			id = a.cfg.Servers[0].ID
		}
		a.cfgMu.RUnlock()
	}
	return a.serverByID(id)
}
func (a *App) serverByID(id string) (*ServerManager, ServerSettings, error) {
	a.cfgMu.RLock()
	var s ServerSettings
	found := false
	for _, x := range a.cfg.Servers {
		if x.ID == id {
			s = x
			found = true
			break
		}
	}
	a.cfgMu.RUnlock()
	if !found {
		return nil, s, errors.New("server not found")
	}
	a.managersMu.RLock()
	m := a.managers[id]
	a.managersMu.RUnlock()
	if m == nil {
		return nil, s, errors.New("server manager missing")
	}
	return m, s, nil
}
func (a *App) serverRoot(s ServerSettings) string {
	if s.StorageDir == "" || s.StorageDir == "." {
		return a.dataDir
	}
	return filepath.Join(a.dataDir, filepath.Clean(s.StorageDir))
}
func (a *App) ensureManagers() {
	a.cfgMu.RLock()
	servers := append([]ServerSettings(nil), a.cfg.Servers...)
	a.cfgMu.RUnlock()
	a.managersMu.Lock()
	defer a.managersMu.Unlock()
	for _, s := range servers {
		_ = ensureServerDirs(a.serverRoot(s))
		if a.managers[s.ID] == nil {
			a.managers[s.ID] = NewServerManager(a.serverRoot(s), filepath.Join(a.dataDir, "tools"))
		}
	}
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	m, _, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	jsonOut(w, 200, m.Status())
}
func (a *App) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	_, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	min, max, _ := deploymentPortRange()
	s.PortMin = min
	s.PortMax = max
	jsonOut(w, 200, s)
}
func (a *App) handleSettingsPut(w http.ResponseWriter, r *http.Request) {
	m, current, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	var in ServerSettings
	if err := decodeJSON(r, &in); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	in.ID = current.ID
	in.StorageDir = current.StorageDir
	min, max, _ := deploymentPortRange()
	in.PortMin = min
	in.PortMax = max
	if in.ServerName == "" {
		jsonError(w, 400, "server name is required")
		return
	}
	if in.Port < min || in.Port > max {
		jsonError(w, 400, fmt.Sprintf("port must be within the Docker/CasaOS UDP range %d-%d", min, max))
		return
	}
	if a.portInUse(in.Port, current.ID) {
		jsonError(w, 409, "that UDP port is already assigned to another server")
		return
	}
	if !validMemory(in.MemoryMin) || !validMemory(in.MemoryMax) {
		jsonError(w, 400, "memory values must look like 2G, 4096M, or 1024K")
		return
	}
	if in.BackupFrequency < 5 || in.BackupFrequency > 10080 {
		jsonError(w, 400, "backup frequency must be between 5 and 10080 minutes")
		return
	}
	if m.IsRunning() && in.Port != current.Port {
		jsonError(w, 409, "stop the server before changing its port")
		return
	}
	a.cfgMu.Lock()
	for i := range a.cfg.Servers {
		if a.cfg.Servers[i].ID == current.ID {
			a.cfg.Servers[i] = in
			break
		}
	}
	err = a.saveConfigLocked()
	a.cfgMu.Unlock()
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	if err := a.writeJVMOptionsFor(in); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonOut(w, 200, in)
}
func (a *App) handleStart(w http.ResponseWriter, r *http.Request) {
	_, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if err := a.startServerByID(s.ID); err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	jsonOut(w, 202, map[string]any{"ok": true})
}
func (a *App) startServerByID(id string) error {
	m, s, err := a.serverByID(id)
	if err != nil {
		return err
	}
	if err := a.writeJVMOptionsFor(s); err != nil {
		return err
	}
	args := []string{"--bind", fmt.Sprintf("0.0.0.0:%d", s.Port), "--backup-frequency", strconv.Itoa(s.BackupFrequency)}
	args = append(args, splitArgs(s.ExtraArgs)...)
	return m.Start(args)
}
func (a *App) handleStop(w http.ResponseWriter, r *http.Request) {
	m, _, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if err := m.Stop(); err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	jsonOut(w, 202, map[string]any{"ok": true})
}
func (a *App) restartByID(w http.ResponseWriter, id string) {
	m, _, err := a.serverByID(id)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if !m.IsRunning() {
		if err := a.startServerByID(id); err != nil {
			jsonError(w, 409, err.Error())
			return
		}
		jsonOut(w, 202, map[string]any{"ok": true})
		return
	}
	go func() {
		_ = m.Stop()
		deadline := time.Now().Add(100 * time.Second)
		for m.IsRunning() && time.Now().Before(deadline) {
			time.Sleep(500 * time.Millisecond)
		}
		if !m.IsRunning() {
			if err := a.startServerByID(id); err != nil {
				m.appendLog("controller", "Restart failed: "+err.Error())
			}
		}
	}()
	jsonOut(w, 202, map[string]any{"ok": true})
}
func (a *App) handleRestart(w http.ResponseWriter, r *http.Request) {
	_, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	a.restartByID(w, s.ID)
}
func (a *App) handleConsole(w http.ResponseWriter, r *http.Request) {
	m, _, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	var body struct {
		Command string `json:"command"`
	}
	if err := decodeJSON(r, &body); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	if err := m.SendCommand(body.Command); err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true})
}
func (a *App) commandShortcut(w http.ResponseWriter, r *http.Request, cmd string) {
	m, _, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if err := m.SendCommand(cmd); err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true})
}
func (a *App) handleLogs(w http.ResponseWriter, r *http.Request) {
	m, _, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	jsonOut(w, 200, m.logs.Snapshot())
}
func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	m, _, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, 500, "streaming is not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "retry: 2000\n\n")
	flusher.Flush()
	ch := m.hub.Subscribe()
	defer m.hub.Unsubscribe(ch)
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case line := <-ch:
			b, _ := json.Marshal(line)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = io.WriteString(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func (a *App) handleServerConfigGet(w http.ResponseWriter, r *http.Request) {
	a.serveServerJSONGet(w, r, "config.json")
}
func (a *App) handleServerConfigPut(w http.ResponseWriter, r *http.Request) {
	a.serveServerJSONPut(w, r, "config.json")
}
func (a *App) handleServerFileGet(w http.ResponseWriter, r *http.Request) {
	name, ok := allowedServerJSONName(r.PathValue("name"))
	if !ok {
		jsonError(w, 404, "unsupported server file")
		return
	}
	a.serveServerJSONGet(w, r, name)
}
func (a *App) handleServerFilePut(w http.ResponseWriter, r *http.Request) {
	name, ok := allowedServerJSONName(r.PathValue("name"))
	if !ok {
		jsonError(w, 404, "unsupported server file")
		return
	}
	a.serveServerJSONPut(w, r, name)
}
func (a *App) serveServerJSONGet(w http.ResponseWriter, r *http.Request, name string) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	path := filepath.Join(a.serverRoot(s), "game", "Server", name)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		jsonOut(w, 200, map[string]any{"name": name, "exists": false, "content": "{}", "running": m.IsRunning(), "editable": !m.IsRunning()})
		return
	}
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonOut(w, 200, map[string]any{"name": name, "exists": true, "content": string(b), "running": m.IsRunning(), "editable": !m.IsRunning()})
}
func (a *App) serveServerJSONPut(w http.ResponseWriter, r *http.Request, name string) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if m.IsRunning() {
		jsonError(w, 409, "stop the Hytale server before editing configuration files")
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := decodeJSON(r, &body); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	var tmp any
	if err := json.Unmarshal([]byte(body.Content), &tmp); err != nil {
		jsonError(w, 400, "file must contain valid JSON: "+err.Error())
		return
	}
	path := filepath.Join(a.serverRoot(s), "game", "Server", name)
	if err := atomicWrite(path, []byte(body.Content), 0644); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true, "name": name})
}
func allowedServerJSONName(name string) (string, bool) {
	switch name {
	case "config.json", "permissions.json", "whitelist.json", "bans.json":
		return name, true
	}
	return "", false
}

type FileInfoDTO struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Mod  string `json:"modified"`
}

func (a *App) handleModsList(w http.ResponseWriter, r *http.Request) {
	_, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	dir := filepath.Join(a.serverRoot(s), "game", "Server", "mods")
	_ = os.MkdirAll(dir, 0755)
	entries, err := os.ReadDir(dir)
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	var out []FileInfoDTO
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, FileInfoDTO{Name: e.Name(), Size: info.Size(), Mod: info.ModTime().Format(time.RFC3339)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	jsonOut(w, 200, out)
}
func (a *App) handleModUpload(w http.ResponseWriter, r *http.Request) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		jsonError(w, 400, "invalid upload: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		jsonError(w, 400, "file is required")
		return
	}
	defer file.Close()
	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext != ".jar" && ext != ".zip" {
		jsonError(w, 400, "Hytale mods must be .jar or .zip files")
		return
	}
	name := filepath.Base(header.Filename)
	dir := filepath.Join(a.serverRoot(s), "game", "Server", "mods")
	_ = os.MkdirAll(dir, 0755)
	if err := streamToFile(file, filepath.Join(dir, name), 0644); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	m.appendLog("controller", "Installed mod: "+name)
	jsonOut(w, 201, map[string]any{"ok": true, "name": name})
}
func (a *App) handleModDelete(w http.ResponseWriter, r *http.Request) {
	_, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	name := filepath.Base(r.PathValue("name"))
	if name == "." || name == "" {
		jsonError(w, 400, "invalid mod name")
		return
	}
	if err := os.Remove(filepath.Join(a.serverRoot(s), "game", "Server", "mods", name)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			jsonError(w, 404, "mod not found")
		} else {
			jsonError(w, 500, err.Error())
		}
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true})
}
func (a *App) handleBackupsList(w http.ResponseWriter, r *http.Request) {
	_, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	root := a.serverRoot(s)
	jsonOut(w, 200, map[string]any{"snapshots": listFiles(filepath.Join(root, "snapshots"), ".tar.gz"), "native": listAllFiles(filepath.Join(root, "game", "Server", "backups"))})
}
func (a *App) handleNativeBackup(w http.ResponseWriter, r *http.Request) {
	m, _, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
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
	jsonOut(w, 202, map[string]any{"ok": true})
}
func (a *App) handleSnapshotCreate(w http.ResponseWriter, r *http.Request) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if m.IsRunning() {
		jsonError(w, 409, "stop the server before making a full restorable snapshot")
		return
	}
	root := a.serverRoot(s)
	name := "snapshot-" + time.Now().Format("20060102-150405") + ".tar.gz"
	dst := filepath.Join(root, "snapshots", name)
	gameServer := filepath.Join(root, "game", "Server")
	include := []string{"universe", "mods", "config.json", "permissions.json", "whitelist.json", "bans.json"}
	if err := createTarGz(dst, gameServer, include); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonOut(w, 201, map[string]any{"ok": true, "name": name})
}
func (a *App) handleSnapshotRestore(w http.ResponseWriter, r *http.Request) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if m.IsRunning() {
		jsonError(w, 409, "stop the server before restoring a snapshot")
		return
	}
	name := filepath.Base(r.PathValue("name"))
	if !strings.HasSuffix(name, ".tar.gz") {
		jsonError(w, 400, "invalid snapshot")
		return
	}
	root := a.serverRoot(s)
	src := filepath.Join(root, "snapshots", name)
	if _, err := os.Stat(src); err != nil {
		jsonError(w, 404, "snapshot not found")
		return
	}
	serverDir := filepath.Join(root, "game", "Server")
	safety := filepath.Join(root, "snapshots", "pre-restore-"+time.Now().Format("20060102-150405")+".tar.gz")
	_ = createTarGz(safety, serverDir, []string{"universe", "mods", "config.json", "permissions.json", "whitelist.json", "bans.json"})
	if err := extractTarGz(src, serverDir); err != nil {
		jsonError(w, 500, err.Error())
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
	name := filepath.Base(r.PathValue("name"))
	if !strings.HasSuffix(name, ".tar.gz") {
		jsonError(w, 400, "invalid snapshot")
		return
	}
	if err := os.Remove(filepath.Join(a.serverRoot(s), "snapshots", name)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			jsonError(w, 404, "snapshot not found")
		} else {
			jsonError(w, 500, err.Error())
		}
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true})
}
func (a *App) handleInstallStatus(w http.ResponseWriter, r *http.Request) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	root := a.serverRoot(s)
	downloader := filepath.Join(a.dataDir, "tools", "hytale-downloader")
	bundle := filepath.Join(root, "imports", "game.zip")
	jsonOut(w, 200, map[string]any{"installed": m.Installed(), "downloaderPresent": fileExists(downloader), "bundlePresent": fileExists(bundle), "busy": m.InstallBusy(), "paths": map[string]string{"downloader": downloader, "bundle": bundle, "game": filepath.Join(root, "game")}})
}
func (a *App) handleDownloaderUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(128 << 20); err != nil {
		jsonError(w, 400, "invalid upload: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		jsonError(w, 400, "file is required")
		return
	}
	defer file.Close()
	if strings.HasSuffix(strings.ToLower(header.Filename), ".exe") {
		jsonError(w, 400, "upload the Linux hytale-downloader or official hytale-downloader.zip")
		return
	}
	toolsDir := filepath.Join(a.dataDir, "tools")
	_ = os.MkdirAll(toolsDir, 0755)
	uploadPath := filepath.Join(toolsDir, ".hytale-downloader-upload")
	_ = os.Remove(uploadPath)
	if err := streamToFile(file, uploadPath, 0644); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	defer os.Remove(uploadPath)
	dst := filepath.Join(toolsDir, "hytale-downloader")
	_ = os.Remove(dst)
	if strings.HasSuffix(strings.ToLower(header.Filename), ".zip") || isZipFile(uploadPath) {
		tmp := filepath.Join(toolsDir, ".downloader-unpack")
		_ = os.RemoveAll(tmp)
		defer os.RemoveAll(tmp)
		if err := extractZip(uploadPath, tmp); err != nil {
			jsonError(w, 400, "could not unpack downloader archive: "+err.Error())
			return
		}
		candidate, err := locateDownloaderBinary(tmp)
		if err != nil {
			jsonError(w, 400, err.Error())
			return
		}
		if err := copyFile(candidate, dst, 0755); err != nil {
			jsonError(w, 500, err.Error())
			return
		}
	} else if err := copyFile(uploadPath, dst, 0755); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	_ = os.Chmod(dst, 0755)
	jsonOut(w, 201, map[string]any{"ok": true, "filename": header.Filename})
}
func (a *App) handleDownloaderRun(w http.ResponseWriter, r *http.Request) {
	m, _, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if m.IsRunning() {
		jsonError(w, 409, "stop the server before installing server files")
		return
	}
	if err := m.RunDownloader(); err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	jsonOut(w, 202, map[string]any{"ok": true})
}
func (a *App) handleImportLocal(w http.ResponseWriter, r *http.Request) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if m.IsRunning() {
		jsonError(w, 409, "stop the server before importing server files")
		return
	}
	if err := m.ImportBundle(filepath.Join(a.serverRoot(s), "imports", "game.zip")); err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	jsonOut(w, 202, map[string]any{"ok": true})
}

func defaultServerSettings(id, name string, port int, storage string) ServerSettings {
	min, max, _ := deploymentPortRange()
	return ServerSettings{ID: id, ServerName: name, Port: port, MemoryMin: "2G", MemoryMax: "4G", AutoStart: false, ExtraArgs: "", PublicHost: "", BackupFrequency: 30, StorageDir: storage, PortMin: min, PortMax: max}
}
func (a *App) loadConfig() error {
	path := filepath.Join(a.dataDir, "controller.json")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		min, _, _ := deploymentPortRange()
		a.cfg = ControllerConfig{Servers: []ServerSettings{defaultServerSettings("main", "My Hytale Server", min, ".")}}
		return a.saveConfigUnlocked()
	}
	if err != nil {
		return err
	}
	var cfg ControllerConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return err
	}
	if len(cfg.Servers) == 0 {
		port := 5520
		name := "My Hytale Server"
		ls := cfg.Settings
		if ls != nil {
			if ls.Port > 0 {
				port = ls.Port
			}
			if ls.ServerName != "" {
				name = ls.ServerName
			}
		}
		s := defaultServerSettings("main", name, port, ".")
		if ls != nil {
			s.MemoryMin = defaultIf(ls.MemoryMin, "2G")
			s.MemoryMax = defaultIf(ls.MemoryMax, "4G")
			s.AutoStart = ls.AutoStart
			s.ExtraArgs = ls.ExtraArgs
			s.PublicHost = ls.PublicHost
			if ls.BackupFrequency > 0 {
				s.BackupFrequency = ls.BackupFrequency
			}
		}
		cfg.Servers = []ServerSettings{s}
		cfg.Settings = nil
	}
	min, max, _ := deploymentPortRange()
	for i := range cfg.Servers {
		if cfg.Servers[i].ID == "" {
			cfg.Servers[i].ID = fmt.Sprintf("server-%d", i+1)
		}
		if cfg.Servers[i].StorageDir == "" {
			if i == 0 {
				cfg.Servers[i].StorageDir = "."
			} else {
				cfg.Servers[i].StorageDir = filepath.Join("servers", cfg.Servers[i].ID)
			}
		}
		if cfg.Servers[i].Port == 0 {
			cfg.Servers[i].Port = min + i
		}
		cfg.Servers[i].PortMin = min
		cfg.Servers[i].PortMax = max
		if cfg.Servers[i].MemoryMin == "" {
			cfg.Servers[i].MemoryMin = "2G"
		}
		if cfg.Servers[i].MemoryMax == "" {
			cfg.Servers[i].MemoryMax = "4G"
		}
		if cfg.Servers[i].BackupFrequency == 0 {
			cfg.Servers[i].BackupFrequency = 30
		}
	}
	a.cfg = cfg
	return a.saveConfigUnlocked()
}
func (a *App) saveConfigUnlocked() error {
	b, err := json.MarshalIndent(a.cfg, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(a.dataDir, "controller.json"), b, 0600)
}
func (a *App) saveConfigLocked() error { return a.saveConfigUnlocked() }
func (a *App) writeJVMOptionsFor(s ServerSettings) error {
	if !validMemory(s.MemoryMin) || !validMemory(s.MemoryMax) {
		return errors.New("invalid JVM memory settings")
	}
	lines := []string{"# Managed by Hytale Server Manager - " + s.ServerName, "-Xms" + s.MemoryMin, "-Xmx" + s.MemoryMax, "-XX:+UseG1GC"}
	return atomicWrite(filepath.Join(a.serverRoot(s), "game", "jvm.options"), []byte(strings.Join(lines, "\n")+"\n"), 0644)
}
func (a *App) uniqueServerID(base string) string {
	if base == "" {
		base = "server"
	}
	id := base
	for n := 2; n < 1000; n++ {
		if !a.serverIDExists(id) {
			return id
		}
		id = fmt.Sprintf("%s-%d", base, n)
	}
	return ""
}
func (a *App) serverIDExists(id string) bool {
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()
	for _, s := range a.cfg.Servers {
		if s.ID == id {
			return true
		}
	}
	return false
}
func (a *App) nextAvailablePort() int {
	min, max, _ := deploymentPortRange()
	for p := min; p <= max; p++ {
		if !a.portInUse(p, "") {
			return p
		}
	}
	return 0
}
func (a *App) portInUse(port int, except string) bool {
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()
	for _, s := range a.cfg.Servers {
		if s.ID != except && s.Port == port {
			return true
		}
	}
	return false
}

func NewServerManager(rootDir, toolsDir string) *ServerManager {
	return &ServerManager{rootDir: rootDir, toolsDir: toolsDir, logs: &LogRing{max: 2500}, hub: &LogHub{clients: map[chan LogLine]struct{}{}}}
}
func (m *ServerManager) Installed() bool {
	return fileExists(filepath.Join(m.rootDir, "game", "Server", "HytaleServer.jar")) && fileExists(filepath.Join(m.rootDir, "game", "Assets.zip"))
}
func (m *ServerManager) IsRunning() bool   { m.mu.Lock(); defer m.mu.Unlock(); return m.running }
func (m *ServerManager) InstallBusy() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.installBusy }
func (m *ServerManager) Status() Status {
	m.mu.Lock()
	running := m.running
	started := m.startedAt
	exitCode := m.exitCode
	lastExit := m.lastExitAt
	busy := m.installBusy
	pid := 0
	if m.cmd != nil && m.cmd.Process != nil && running {
		pid = m.cmd.Process.Pid
	}
	m.mu.Unlock()
	st := Status{Running: running, Installed: m.Installed(), InstallBusy: busy, PID: pid, ExitCode: exitCode, ControllerVers: version}
	if running {
		st.UptimeSeconds = int64(time.Since(started).Seconds())
	}
	if !lastExit.IsZero() {
		st.LastExitAt = lastExit.Format(time.RFC3339)
	}
	if running && pid > 0 {
		st.MemoryBytes = readProcessGroupMemory(pid)
	} else {
		st.MemoryBytes = 0
	}
	st.MemoryLimitBytes = readEffectiveMemoryLimit()
	st.MeetsMinMemory = st.MemoryLimitBytes == 0 || st.MemoryLimitBytes >= 4*1024*1024*1024
	return st
}
func (m *ServerManager) Start(args []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return errors.New("server is already running")
	}
	if m.installBusy {
		return errors.New("an install job is running")
	}
	if !m.Installed() {
		return errors.New("Hytale server files are not installed yet")
	}
	startSh := filepath.Join(m.rootDir, "game", "start.sh")
	if !fileExists(startSh) {
		return errors.New("game/start.sh is missing; import an official server bundle or run /update setup first")
	}
	_ = os.Chmod(startSh, 0755)
	cmdArgs := append([]string{startSh}, args...)
	cmd := exec.Command("bash", cmdArgs...)
	cmd.Dir = filepath.Join(m.rootDir, "game")
	cmd.Env = append(os.Environ(), "HOME="+filepath.Join(m.rootDir, "home"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	m.cmd = cmd
	m.stdin = stdin
	m.running = true
	m.startedAt = time.Now()
	m.exitCode = 0
	m.appendLogLocked("controller", fmt.Sprintf("Starting Hytale server on PID %d", cmd.Process.Pid))
	go m.scanPipe(stdout, "server")
	go m.scanPipe(stderr, "stderr")
	go m.wait(cmd)
	return nil
}
func (m *ServerManager) wait(cmd *exec.Cmd) {
	err := cmd.Wait()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	m.mu.Lock()
	if m.cmd == cmd {
		m.running = false
		m.stdin = nil
		m.exitCode = code
		m.lastExitAt = time.Now()
	}
	m.appendLogLocked("controller", fmt.Sprintf("Server process exited with code %d", code))
	m.mu.Unlock()
}
func (m *ServerManager) scanPipe(r io.Reader, source string) {
	s := bufio.NewScanner(r)
	buf := make([]byte, 64*1024)
	s.Buffer(buf, 2*1024*1024)
	for s.Scan() {
		m.appendLog(source, s.Text())
	}
	if err := s.Err(); err != nil && !errors.Is(err, os.ErrClosed) && !strings.Contains(strings.ToLower(err.Error()), "file already closed") {
		m.appendLog("controller", source+" stream error: "+err.Error())
	}
}
func (m *ServerManager) SendCommand(command string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return errors.New("command is empty")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running || m.stdin == nil {
		return errors.New("server is not running")
	}
	m.appendLogLocked("console", "> "+command)
	_, err := io.WriteString(m.stdin, command+"\n")
	return err
}
func (m *ServerManager) Stop() error {
	m.mu.Lock()
	if !m.running || m.stdin == nil || m.cmd == nil || m.cmd.Process == nil {
		m.mu.Unlock()
		return errors.New("server is not running")
	}
	pid := m.cmd.Process.Pid
	stdin := m.stdin
	m.appendLogLocked("controller", "Requesting graceful shutdown with /stop")
	_, err := io.WriteString(stdin, "/stop\n")
	m.mu.Unlock()
	if err != nil {
		return err
	}
	go func() {
		time.Sleep(75 * time.Second)
		if m.IsRunning() {
			m.appendLog("controller", "Graceful shutdown timed out; sending SIGINT")
			_ = syscall.Kill(-pid, syscall.SIGINT)
			time.Sleep(10 * time.Second)
			if m.IsRunning() {
				m.appendLog("controller", "Server still running; forcing process group to stop")
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		}
	}()
	return nil
}
func (m *ServerManager) RunDownloader() error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return errors.New("server is running")
	}
	if m.installBusy {
		m.mu.Unlock()
		return errors.New("an install job is already running")
	}
	downloader := filepath.Join(m.toolsDir, "hytale-downloader")
	if !fileExists(downloader) {
		m.mu.Unlock()
		return errors.New("official Hytale downloader is not uploaded yet")
	}
	_ = os.Chmod(downloader, 0755)
	m.installBusy = true
	m.mu.Unlock()
	go func() {
		defer func() { m.mu.Lock(); m.installBusy = false; m.mu.Unlock() }()
		bundle := filepath.Join(m.rootDir, "imports", "game.zip")
		_ = os.Remove(bundle)
		m.appendLog("installer", "Launching official Hytale Downloader. Complete the device authorization shown below.")
		cmd := exec.Command(downloader, "-download-path", bundle)
		cmd.Dir = m.toolsDir
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			m.appendLog("installer", err.Error())
			return
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			m.appendLog("installer", err.Error())
			return
		}
		if err := cmd.Start(); err != nil {
			m.appendLog("installer", "Downloader failed to start: "+err.Error())
			return
		}
		go m.scanPipe(stdout, "installer")
		go m.scanPipe(stderr, "installer")
		if err := cmd.Wait(); err != nil {
			m.appendLog("installer", "Downloader exited with an error: "+err.Error())
			return
		}
		m.appendLog("installer", "Download complete; importing game.zip")
		if err := m.importBundleSync(bundle); err != nil {
			m.appendLog("installer", "Import failed: "+err.Error())
			return
		}
		m.appendLog("installer", "Hytale server files installed successfully")
	}()
	return nil
}
func (m *ServerManager) ImportBundle(path string) error {
	m.mu.Lock()
	if m.installBusy {
		m.mu.Unlock()
		return errors.New("an install job is already running")
	}
	if !fileExists(path) {
		m.mu.Unlock()
		return errors.New("game.zip was not found in this server's imports folder")
	}
	m.installBusy = true
	m.mu.Unlock()
	go func() {
		defer func() { m.mu.Lock(); m.installBusy = false; m.mu.Unlock() }()
		m.appendLog("installer", "Importing mounted game.zip")
		if err := m.importBundleSync(path); err != nil {
			m.appendLog("installer", "Import failed: "+err.Error())
			return
		}
		m.appendLog("installer", "Hytale server files installed successfully")
	}()
	return nil
}
func (m *ServerManager) importBundleSync(path string) error {
	tmp := filepath.Join(m.rootDir, ".import-tmp")
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0755); err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := extractZip(path, tmp); err != nil {
		return err
	}
	root, err := locateGameRoot(tmp)
	if err != nil {
		return err
	}
	dest := filepath.Join(m.rootDir, "game")
	if fileExists(filepath.Join(dest, "Server", "HytaleServer.jar")) {
		return errors.New("a server is already installed; use Hytale's built-in updater instead of overwriting it")
	}
	_ = os.RemoveAll(dest)
	if err := copyDir(root, dest); err != nil {
		return err
	}
	if fileExists(filepath.Join(dest, "start.sh")) {
		_ = os.Chmod(filepath.Join(dest, "start.sh"), 0755)
	}
	return nil
}
func (m *ServerManager) appendLog(source, line string) {
	m.mu.Lock()
	m.appendLogLocked(source, line)
	m.mu.Unlock()
}
func (m *ServerManager) appendLogLocked(source, line string) {
	ll := LogLine{Time: time.Now(), Source: source, Line: stripANSI(line)}
	m.logs.Add(ll)
	m.hub.Broadcast(ll)
}
func (l *LogRing) Add(line LogLine) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
	if len(l.lines) > l.max {
		l.lines = append([]LogLine(nil), l.lines[len(l.lines)-l.max:]...)
	}
}
func (l *LogRing) Snapshot() []LogLine {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return append([]LogLine(nil), l.lines...)
}
func (h *LogHub) Subscribe() chan LogLine {
	ch := make(chan LogLine, 64)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}
func (h *LogHub) Unsubscribe(ch chan LogLine) {
	h.mu.Lock()
	delete(h.clients, ch)
	close(ch)
	h.mu.Unlock()
}
func (h *LogHub) Broadcast(line LogLine) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- line:
		default:
		}
	}
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	iter := 180000
	key := pbkdf2SHA256([]byte(password), salt, iter, 32)
	return fmt.Sprintf("pbkdf2_sha256$%d$%s$%s", iter, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}
func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got := pbkdf2SHA256([]byte(password), salt, iter, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}
func pbkdf2SHA256(password, salt []byte, iterations, keyLen int) []byte {
	hLen := 32
	blocks := (keyLen + hLen - 1) / hLen
	out := make([]byte, 0, blocks*hLen)
	for i := 1; i <= blocks; i++ {
		mac := hmac.New(sha256.New, password)
		_, _ = mac.Write(salt)
		var ib [4]byte
		binary.BigEndian.PutUint32(ib[:], uint32(i))
		_, _ = mac.Write(ib[:])
		u := mac.Sum(nil)
		t := append([]byte(nil), u...)
		for j := 1; j < iterations; j++ {
			mac = hmac.New(sha256.New, password)
			_, _ = mac.Write(u)
			u = mac.Sum(nil)
			for k := range t {
				t[k] ^= u[k]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}
func deploymentPortRange() (int, int, bool) {
	start := 5520
	end := 5539
	locked := false
	if v := strings.TrimSpace(envFirst([]string{"HSM_GAME_PORT_START", "ORBIS_GAME_PORT_START"}, "")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 65535 {
			start = n
			locked = true
		}
	}
	if v := strings.TrimSpace(envFirst([]string{"HSM_GAME_PORT_END", "ORBIS_GAME_PORT_END"}, "")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= start && n <= 65535 {
			end = n
			locked = true
		}
	}
	if v := strings.TrimSpace(envFirst([]string{"HSM_GAME_PORT", "ORBIS_GAME_PORT"}, "")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 65535 {
			start = n
			if end < start {
				end = start + 19
			}
			locked = true
		}
	}
	if end < start {
		end = start
	}
	return start, end, locked
}
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
func defaultIf(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}
func isZipFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, 4)
	if _, err := io.ReadFull(f, b); err != nil {
		return false
	}
	return string(b) == "PK\x03\x04" || string(b) == "PK\x05\x06" || string(b) == "PK\x07\x08"
}
func locateDownloaderBinary(root string) (string, error) {
	var matches []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := strings.ToLower(d.Name())
		if name == "hytale-downloader" || strings.HasPrefix(name, "hytale-downloader-linux") {
			matches = append(matches, path)
		}
		return nil
	})
	if len(matches) == 0 {
		return "", errors.New("archive does not contain a Linux hytale-downloader binary")
	}
	sort.Strings(matches)
	return matches[0], nil
}
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
func readEffectiveMemoryLimit() int64 {
	paths := []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		v := strings.TrimSpace(string(b))
		if v == "max" || v == "" {
			return 0
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err == nil && n > 0 && n < (1<<60) {
			return n
		}
	}
	return 0
}
func ensureRootDirs(dataDir string) error {
	for _, d := range []string{dataDir, filepath.Join(dataDir, "tools"), filepath.Join(dataDir, "servers")} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return err
		}
	}
	return nil
}
func ensureServerDirs(root string) error {
	for _, d := range []string{root, filepath.Join(root, "game"), filepath.Join(root, "imports"), filepath.Join(root, "snapshots"), filepath.Join(root, "home")} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return err
		}
	}
	return nil
}
func jsonOut(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func jsonError(w http.ResponseWriter, code int, msg string) {
	jsonOut(w, code, map[string]any{"error": msg})
}
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
func sessionCookie(r *http.Request) (*http.Cookie, error) {
	if c, err := r.Cookie("hsm_session"); err == nil {
		return c, nil
	}
	return r.Cookie("orbis_session")
}
func clearSessionCookies(w http.ResponseWriter) {
	for _, name := range []string{"hsm_session", "orbis_session"} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	}
}
func envFirst(keys []string, fallback string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return fallback
}
func envOr(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
func validMemory(v string) bool {
	v = strings.TrimSpace(strings.ToUpper(v))
	if len(v) < 2 {
		return false
	}
	unit := v[len(v)-1]
	if unit != 'G' && unit != 'M' && unit != 'K' {
		return false
	}
	n, err := strconv.Atoi(v[:len(v)-1])
	return err == nil && n > 0
}
func splitArgs(s string) []string {
	var out []string
	var b strings.Builder
	quote := rune(0)
	esc := false
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	for _, r := range s {
		if esc {
			b.WriteRune(r)
			esc = false
			continue
		}
		if r == '\\' {
			esc = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if r == ' ' || r == '\t' || r == '\n' {
			flush()
			continue
		}
		b.WriteRune(r)
	}
	flush()
	return out
}
func atomicWrite(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }
func streamToFile(src multipart.File, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, io.LimitReader(src, 8<<30))
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
func extractZip(src, dst string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		clean := filepath.Clean(f.Name)
		if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("unsafe archive path: %s", f.Name)
		}
		target := filepath.Join(dst, clean)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		mode := f.Mode()
		if mode == 0 {
			mode = 0644
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			rc.Close()
			return err
		}
		_, e1 := io.Copy(out, rc)
		e2 := out.Close()
		e3 := rc.Close()
		if e1 != nil {
			return e1
		}
		if e2 != nil {
			return e2
		}
		if e3 != nil {
			return e3
		}
	}
	return nil
}
func locateGameRoot(root string) (string, error) {
	candidates := []string{root, filepath.Join(root, "game"), filepath.Join(root, "HytaleServer")}
	for _, c := range candidates {
		if fileExists(filepath.Join(c, "Server", "HytaleServer.jar")) && fileExists(filepath.Join(c, "Assets.zip")) {
			return c, nil
		}
	}
	var found string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if fileExists(filepath.Join(path, "Server", "HytaleServer.jar")) && fileExists(filepath.Join(path, "Assets.zip")) {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	if found == "" {
		return "", errors.New("game.zip did not contain Server/HytaleServer.jar and Assets.zip")
	}
	return found, nil
}
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, info.Mode())
	})
}
func cloneGameInstall(srcRoot, dstRoot string) error {
	src := filepath.Join(srcRoot, "game")
	dst := filepath.Join(dstRoot, "game")
	if !fileExists(filepath.Join(src, "Server", "HytaleServer.jar")) {
		return errors.New("source server files are missing")
	}
	_ = os.RemoveAll(dst)
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == filepath.Join("Server", "universe") || strings.HasPrefix(rel, filepath.Join("Server", "universe")+string(os.PathSeparator)) || rel == filepath.Join("Server", "backups") || strings.HasPrefix(rel, filepath.Join("Server", "backups")+string(os.PathSeparator)) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, info.Mode())
	})
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

func readProcessGroupMemory(pgid int) int64 {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	var total int64
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		g, err := syscall.Getpgid(pid)
		if err != nil || g != pgid {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "status"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "VmRSS:") {
				f := strings.Fields(line)
				if len(f) >= 2 {
					if kb, err := strconv.ParseInt(f[1], 10, 64); err == nil {
						total += kb * 1024
					}
				}
				break
			}
		}
	}
	return total
}

func readContainerMemory() int64 {
	paths := []string{"/sys/fs/cgroup/memory.current", "/sys/fs/cgroup/memory/memory.usage_in_bytes"}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err == nil {
			if n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil {
				return n
			}
		}
	}
	return 0
}
func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !inEsc && c == 0x1b {
			inEsc = true
			continue
		}
		if inEsc {
			if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
				inEsc = false
			}
			continue
		}
		if c != '\r' {
			b.WriteByte(c)
		}
	}
	return b.String()
}
