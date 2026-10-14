package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// crashTestApp creates a Hytale server whose start.sh is the given script,
// wired to the app's crash handling with very short restart delays.
func crashTestApp(t *testing.T, autoRestart bool, script string) (*App, *ServerManager, ServerSettings) {
	t.Helper()
	root := t.TempDir()
	s := defaultServerSettings("hy", "Hytale", 5520, filepath.Join("servers", "hy"))
	s.AutoRestart = autoRestart
	app := &App{dataDir: root, managers: map[string]*ServerManager{}, crashBackoff: []time.Duration{20 * time.Millisecond}}
	app.cfg.Servers = []ServerSettings{s}
	game := filepath.Join(app.serverRoot(s), "game")
	writeFile(t, filepath.Join(game, "Server", "HytaleServer.jar"), "")
	writeFile(t, filepath.Join(game, "Assets.zip"), "")
	writeFile(t, filepath.Join(game, "start.sh"), script)
	if err := os.Chmod(filepath.Join(game, "start.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := app.newServerManager(s)
	app.managers[s.ID] = m
	t.Cleanup(func() {
		app.cfgMu.Lock()
		app.cfg.Servers[0].AutoRestart = false
		app.cfgMu.Unlock()
		if pid := currentPID(m); pid > 0 {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	})
	return app, m, s
}

func countLog(m *ServerManager, text string) int {
	n := 0
	for _, line := range m.logs.Snapshot() {
		if strings.Contains(line.Line, text) {
			n++
		}
	}
	return n
}

func TestCrashedServerIsRestartedThenGivenUpOn(t *testing.T) {
	app, m, s := crashTestApp(t, true, "#!/bin/bash\necho booting\nexit 1\n")
	if err := app.startServerByID(s.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "auto-restart to give up", func() bool { return logContains(m, "Auto-restart stopped", time.Time{}) })
	time.Sleep(100 * time.Millisecond)
	if got := countLog(m, "Starting hytale server"); got != 1+maxCrashRestarts {
		t.Errorf("server started %d times, want 1 + %d restarts", got, maxCrashRestarts)
	}
	if m.IsRunning() {
		t.Error("server still running after auto-restart gave up")
	}
	// A manual start clears the history, so the next crash is restarted again.
	app.resetCrashes(s.ID)
	if err := app.startServerByID(s.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a fresh restart after reset", func() bool { return countLog(m, "auto-restart 1 of") >= 2 })
}

func TestCrashIsNotRestartedWhenAutoRestartIsOff(t *testing.T) {
	app, m, s := crashTestApp(t, false, "#!/bin/bash\necho booting\nexit 1\n")
	if err := app.startServerByID(s.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "exit", func() bool { return logContains(m, "exited with code 1", time.Time{}) })
	time.Sleep(200 * time.Millisecond)
	if got := countLog(m, "Starting hytale server"); got != 1 {
		t.Errorf("server started %d times with auto-restart off", got)
	}
}

func TestRequestedStopAndCleanExitAreNotCrashes(t *testing.T) {
	// Exits with an error code, but only because it was asked to stop.
	app, m, s := crashTestApp(t, true, "#!/bin/bash\necho ready\nwhile read -r line; do if [ \"$line\" = \"/stop\" ]; then exit 3; fi; done\n")
	if err := app.startServerByID(s.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "ready", func() bool { return logContains(m, "ready", time.Time{}) })
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stopped", func() bool { return !m.IsRunning() })
	time.Sleep(200 * time.Millisecond)
	if got := countLog(m, "Starting hytale server"); got != 1 {
		t.Errorf("requested stop was treated as a crash: %d starts", got)
	}

	app2, m2, s2 := crashTestApp(t, true, "#!/bin/bash\necho done\nexit 0\n")
	if err := app2.startServerByID(s2.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "clean exit", func() bool { return logContains(m2, "exited with code 0", time.Time{}) })
	time.Sleep(200 * time.Millisecond)
	if got := countLog(m2, "Starting hytale server"); got != 1 {
		t.Errorf("clean exit was treated as a crash: %d starts", got)
	}
}
