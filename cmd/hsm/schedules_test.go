package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNextScheduleRun(t *testing.T) {
	loc := time.FixedZone("test", -7*3600)
	// Wednesday 14 October 2026, 10:00 local.
	now := time.Date(2026, 10, 14, 10, 0, 0, 0, loc)
	cases := []struct {
		name string
		sch  Schedule
		want time.Time
	}{
		{"later today", Schedule{Kind: "daily", Time: "18:30"}, time.Date(2026, 10, 14, 18, 30, 0, 0, loc)},
		{"already passed today", Schedule{Kind: "daily", Time: "04:00"}, time.Date(2026, 10, 15, 4, 0, 0, 0, loc)},
		{"exactly now is not due again", Schedule{Kind: "daily", Time: "10:00"}, time.Date(2026, 10, 15, 10, 0, 0, 0, loc)},
		{"mondays only", Schedule{Kind: "daily", Time: "04:00", Days: []int{1}}, time.Date(2026, 10, 19, 4, 0, 0, 0, loc)},
		{"weekend", Schedule{Kind: "daily", Time: "12:00", Days: []int{0, 6}}, time.Date(2026, 10, 17, 12, 0, 0, 0, loc)},
		{"interval", Schedule{Kind: "interval", EveryMinutes: 90}, now.Add(90 * time.Minute)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nextScheduleRun(tc.sch, now); !got.Equal(tc.want) {
				t.Errorf("next = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestNormalizeSchedules(t *testing.T) {
	good, err := normalizeSchedules([]Schedule{{Action: "restart", Kind: "daily", Time: "04:00", Days: []int{0, 1, 2, 3, 4, 5, 6}, WarnMinutes: 5}}, "minecraft-java")
	if err != nil {
		t.Fatal(err)
	}
	if good[0].ID == "" || good[0].Name != "Restart" || good[0].Days != nil || good[0].WarnMinutes != 5 {
		t.Errorf("defaults not applied: %#v", good[0])
	}
	// Dragonwilds has no broadcast command, so warnings are dropped.
	dw, err := normalizeSchedules([]Schedule{{Action: "restart", Kind: "daily", Time: "04:00", WarnMinutes: 5}}, "runescape-dragonwilds")
	if err != nil || dw[0].WarnMinutes != 0 {
		t.Errorf("Dragonwilds warnings = %#v, %v", dw, err)
	}
	bad := map[string][]Schedule{
		"bad time":            {{Action: "restart", Kind: "daily", Time: "25:00"}},
		"bad day":             {{Action: "restart", Kind: "daily", Time: "04:00", Days: []int{7}}},
		"interval too short":  {{Action: "backup", Kind: "interval", EveryMinutes: 2}},
		"empty command":       {{Action: "command", Kind: "interval", EveryMinutes: 30}},
		"multi-line command":  {{Action: "command", Command: "say hi\nop me", Kind: "interval", EveryMinutes: 30}},
		"unknown action":      {{Action: "format-disk", Kind: "interval", EveryMinutes: 30}},
		"warning >= interval": {{Action: "restart", Kind: "interval", EveryMinutes: 10, WarnMinutes: 10}},
		"duplicate ids":       {{ID: "a", Action: "start", Kind: "interval", EveryMinutes: 30}, {ID: "a", Action: "stop", Kind: "interval", EveryMinutes: 30}},
	}
	for name, list := range bad {
		if _, err := normalizeSchedules(list, "minecraft-java"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Dragonwilds cannot take console commands, but can make snapshots.
	if _, err := normalizeSchedules([]Schedule{{Action: "command", Command: "x", Kind: "interval", EveryMinutes: 30}}, "runescape-dragonwilds"); err == nil {
		t.Error("Dragonwilds accepted a console command")
	}
	if _, err := normalizeSchedules([]Schedule{{Action: "backup", Kind: "interval", EveryMinutes: 30}}, "runescape-dragonwilds"); err != nil {
		t.Errorf("Dragonwilds refused a backup: %v", err)
	}
}

func TestScheduledCommandRunsWhenDue(t *testing.T) {
	app, m, s := crashTestApp(t, false, "#!/bin/bash\necho ready\nwhile read -r line; do echo \"got $line\"; done\n")
	if err := app.startServerByID(s.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "ready", func() bool { return logContains(m, "ready", time.Time{}) })
	app.cfg.Schedules = map[string][]Schedule{s.ID: {{ID: "hello", Name: "Greeting", Enabled: true, Action: "command", Command: "/say hello", Kind: "interval", EveryMinutes: 30}}}

	now := time.Now()
	app.schedulerTick(now) // first tick only plans the next run
	time.Sleep(100 * time.Millisecond)
	if logContains(m, "got /say hello", time.Time{}) {
		t.Fatal("schedule ran before it was due")
	}
	app.schedulerTick(now.Add(31 * time.Minute))
	waitFor(t, "scheduled command", func() bool { return logContains(m, "got /say hello", time.Time{}) })
	waitFor(t, "result recorded", func() bool {
		views := app.scheduleViews(s)
		return len(views) == 1 && views[0].LastResult == "Done" && !views[0].Running
	})
	if next := app.scheduleViews(s)[0].NextRun; next == "" {
		t.Error("no next run after the schedule ran")
	}
}

func TestScheduledRestartWarnsPlayersThenRestarts(t *testing.T) {
	app, m := minecraftTestServer(t, true)
	_, settings, _ := app.serverByID("mc")
	sch := Schedule{ID: "nightly", Name: "Nightly restart", Enabled: true, Action: "restart", Kind: "daily", Time: "04:00", WarnMinutes: 5}
	// A due time in the past makes the warning waits return immediately.
	if err := app.executeSchedule(settings.ID, sch, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"got say The server will restart in 5 minutes.", "got say The server will restart in 1 minute.", "got stop"} {
		if !logContains(m, want, time.Time{}) {
			t.Errorf("missing %q in console", want)
		}
	}
	waitFor(t, "restart", func() bool { return countLog(m, "Starting minecraft-java server") == 2 && m.IsRunning() })
}

func TestScheduleAPIRoundTripAndDeleteCleanup(t *testing.T) {
	app := newCapabilityTestApp(t, testServer("mc", "minecraft-java"))
	h := newTestMux(app)
	call := func(method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-HSM-Server", "mc")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	code, out := call("PUT", "/api/schedules", `{"schedules":[{"name":"Nightly","enabled":true,"action":"restart","kind":"daily","time":"04:00","warnMinutes":5}]}`)
	if code != 200 {
		t.Fatalf("PUT = %d %v", code, out)
	}
	code, out = call("GET", "/api/schedules", "")
	list, _ := out["schedules"].([]any)
	if code != 200 || len(list) != 1 || out["warnings"] != true {
		t.Fatalf("GET = %d %v", code, out)
	}
	first, _ := list[0].(map[string]any)
	if first["nextRun"] == "" || first["id"] == "" {
		t.Errorf("schedule view missing id or next run: %v", first)
	}
	// The GET response, read-only fields included, can be sent back as-is.
	b, _ := json.Marshal(map[string]any{"schedules": list})
	if code, out := call("PUT", "/api/schedules", string(b)); code != 200 {
		t.Fatalf("round-trip PUT = %d %v", code, out)
	}
	if code, out := call("PUT", "/api/schedules", `{"schedules":[{"action":"restart","kind":"daily","time":"4pm"}]}`); code != 400 {
		t.Errorf("invalid schedule PUT = %d %v", code, out)
	}
	if code, _ := call("DELETE", "/api/servers/mc", ""); code != 200 {
		t.Fatalf("delete failed: %d", code)
	}
	if _, ok := app.cfg.Schedules["mc"]; ok {
		t.Error("deleted server's schedules were kept")
	}
}
