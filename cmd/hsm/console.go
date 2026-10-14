package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

func (a *App) handleConsole(w http.ResponseWriter, r *http.Request) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if !requireCapability(w, s, "Console commands", func(c GameCapability) bool { return c.ConsoleCommands }) {
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

func (a *App) commandShortcut(w http.ResponseWriter, r *http.Request, cmd, feature string, supported func(GameCapability) bool) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if !requireCapability(w, s, feature, supported) {
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
