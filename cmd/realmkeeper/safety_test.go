package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeHytaleInstall creates the files HytaleAdapter needs, with a start script
// that ignores "/stop" so tests can exercise the stop escalation path.
func fakeHytaleInstall(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	game := filepath.Join(root, "game")
	if err := os.MkdirAll(filepath.Join(game, "Server"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{filepath.Join(game, "Server", "HytaleServer.jar"), filepath.Join(game, "Assets.zip")} {
		if err := os.WriteFile(name, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/bash\necho ready\nwhile read -r line; do echo \"got $line\"; done\n"
	if err := os.WriteFile(filepath.Join(game, "start.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func logContains(m *ServerManager, text string, since time.Time) bool {
	for _, line := range m.logs.Snapshot() {
		if !line.Time.Before(since) && strings.Contains(line.Line, text) {
			return true
		}
	}
	return false
}

func currentPID(m *ServerManager) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd == nil || m.cmd.Process == nil {
		return 0
	}
	return m.cmd.Process.Pid
}

func TestStopEscalatesWhenServerIgnoresGracefulStop(t *testing.T) {
	m := NewServerManager(fakeHytaleInstall(t), t.TempDir(), "hytale")
	m.stopGrace, m.stopInterrupt = 200*time.Millisecond, 200*time.Millisecond
	if err := m.Start(nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "server ready", func() bool { return logContains(m, "ready", time.Time{}) })
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "forced stop", func() bool { return !m.IsRunning() })
	if !logContains(m, "Graceful shutdown timed out", time.Time{}) {
		t.Fatal("expected the timeout escalation to stop a server that ignores /stop")
	}
}

func TestStopEscalationDoesNotTouchARestartedServer(t *testing.T) {
	m := NewServerManager(fakeHytaleInstall(t), t.TempDir(), "hytale")
	m.stopGrace, m.stopInterrupt = 300*time.Millisecond, 300*time.Millisecond
	if err := m.Start(nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first server ready", func() bool { return logContains(m, "ready", time.Time{}) })
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	// The first process exits some other way and a new one starts before the
	// stop timeout fires, as with a quick manual restart.
	_ = syscall.Kill(-currentPID(m), syscall.SIGKILL)
	waitFor(t, "first server exit", func() bool { return !m.IsRunning() })
	restartedAt := time.Now()
	if err := m.Start(nil); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Kill(-currentPID(m), syscall.SIGKILL) }()
	time.Sleep(900 * time.Millisecond) // past both escalation delays
	if !m.IsRunning() {
		t.Fatal("restarted server was stopped by the previous stop's timeout")
	}
	if logContains(m, "Graceful shutdown timed out", restartedAt) || logContains(m, "forcing process group", restartedAt) {
		t.Fatal("previous stop's timeout escalated against the restarted server")
	}
}

func TestSettingsNeverReturnPasswordsAndBlankKeepsThem(t *testing.T) {
	s := defaultServerSettings("alpha", "Alpha", 5520, filepath.Join("servers", "alpha"))
	s.Password = "join-secret"
	app := newCapabilityTestApp(t, s)
	mux := newTestMux(app)
	call := func(method, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, "/api/settings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-HSM-Server", "alpha")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if strings.Contains(rec.Body.String(), "join-secret") || strings.Contains(rec.Body.String(), "new-secret") {
			t.Fatalf("%s /api/settings leaked a password: %s", method, rec.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	stored := func() string {
		app.cfgMu.RLock()
		defer app.cfgMu.RUnlock()
		return app.cfg.Servers[0].Password
	}

	code, got := call("GET", "")
	if code != 200 || got["passwordSet"] != true || got["password"] != "" {
		t.Fatalf("GET = %d %v, want passwordSet and an empty password", code, got)
	}

	// Sending the GET response straight back must work and keep the password.
	body, _ := json.Marshal(got)
	if code, out := call("PUT", string(body)); code != 200 || stored() != "join-secret" {
		t.Fatalf("round-trip PUT = %d %v, stored password %q", code, out, stored())
	}

	got["password"] = "new-secret"
	body, _ = json.Marshal(got)
	if code, _ := call("PUT", string(body)); code != 200 || stored() != "new-secret" {
		t.Fatalf("setting a new password: status %d, stored %q", code, stored())
	}

	got["password"] = ""
	got["clearPassword"] = true
	body, _ = json.Marshal(got)
	if code, out := call("PUT", string(body)); code != 200 || stored() != "" || out["passwordSet"] != false {
		t.Fatalf("clearing the password: status %d, stored %q, response %v", code, stored(), out)
	}
}
