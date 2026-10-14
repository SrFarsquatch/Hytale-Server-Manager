package main

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestHandleServersCreateMissingCloneSourceLeavesNoOrphanDirectory(t *testing.T) {
	root := t.TempDir()
	app := &App{dataDir: root, managers: map[string]*ServerManager{}}
	req := httptest.NewRequest("POST", "/api/servers", strings.NewReader(`{"game":"hytale","name":"Orphan Prevention Test","sourceServerId":"missing-source"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	app.handleServersCreate(rec, req)

	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "source server not found") {
		t.Fatalf("response = %d %s, want 404 for missing clone source", rec.Code, rec.Body.String())
	}
	if len(app.cfg.Servers) != 0 {
		t.Fatalf("invalid clone request created %d server settings", len(app.cfg.Servers))
	}
	if len(app.managers) != 0 {
		t.Fatalf("invalid clone request created %d server managers", len(app.managers))
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed clone request left filesystem entries behind: %v", entries)
	}
}
