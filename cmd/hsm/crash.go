package main

import (
	"fmt"
	"path/filepath"
	"time"
)

// Crash recovery: when AutoRestart is on, a server that exits non-zero without
// being asked to stop is started again after a growing delay. After
// maxCrashRestarts crashes within crashWindow it is left stopped, so a server
// that cannot start does not loop forever.
const (
	maxCrashRestarts = 3
	crashWindow      = 10 * time.Minute
)

var defaultCrashBackoff = []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second}

// newServerManager creates a manager wired to this app's crash handling.
func (a *App) newServerManager(s ServerSettings) *ServerManager {
	m := NewServerManager(a.serverRoot(s), filepath.Join(a.dataDir, "tools"), s.GameType)
	id := s.ID
	m.onCrash = func() { a.handleCrash(id) }
	return m
}

func (a *App) resetCrashes(id string) {
	a.crashMu.Lock()
	delete(a.crashHistory, id)
	a.crashMu.Unlock()
}

func (a *App) handleCrash(id string) {
	m, s, err := a.serverByID(id)
	if err != nil || !s.AutoRestart {
		return
	}
	now := time.Now()
	a.crashMu.Lock()
	if a.crashHistory == nil {
		a.crashHistory = map[string][]time.Time{}
	}
	recent := a.crashHistory[id][:0]
	for _, t := range a.crashHistory[id] {
		if now.Sub(t) < crashWindow {
			recent = append(recent, t)
		}
	}
	recent = append(recent, now)
	a.crashHistory[id] = recent
	attempt := len(recent)
	backoff := a.crashBackoff
	if backoff == nil {
		backoff = defaultCrashBackoff
	}
	a.crashMu.Unlock()

	if attempt > maxCrashRestarts {
		m.appendLog("controller", fmt.Sprintf("Auto-restart stopped: the server crashed %d times in %d minutes. Check the console for the cause, then start it manually.", attempt, int(crashWindow.Minutes())))
		return
	}
	delay := backoff[min(attempt, len(backoff))-1]
	m.appendLog("controller", fmt.Sprintf("Server crashed; auto-restart %d of %d in %s", attempt, maxCrashRestarts, delay))
	time.Sleep(delay)

	// Re-check: the user may have started it, turned auto-restart off or
	// deleted the server while we waited.
	m2, s2, err := a.serverByID(id)
	if err != nil || m2 != m || !s2.AutoRestart || m.IsRunning() {
		return
	}
	if err := a.startServerByID(id); err != nil {
		m.appendLog("controller", "Auto-restart failed: "+err.Error())
	}
}
