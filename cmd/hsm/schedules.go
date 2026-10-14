package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Schedule is one automated task for a server. Schedules are stored per
// server in ControllerConfig.Schedules rather than in ServerSettings, so a
// settings form that does not know about them can never erase them.
type Schedule struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// Action is restart, start, stop, backup or command.
	Action  string `json:"action"`
	Command string `json:"command,omitempty"`
	// Kind is "daily" (Time, optionally only on Days) or "interval".
	Kind         string `json:"kind"`
	Time         string `json:"time,omitempty"`
	Days         []int  `json:"days,omitempty"` // 0 = Sunday … 6 = Saturday; empty = every day
	EveryMinutes int    `json:"everyMinutes,omitempty"`
	// WarnMinutes announces a restart or stop in game this many minutes
	// before it happens, for games with a broadcast command.
	WarnMinutes int `json:"warnMinutes,omitempty"`
}

const maxSchedulesPerServer = 20

var (
	scheduleIDPattern   = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	scheduleTimePattern = regexp.MustCompile(`^([01]\d|2[0-3]):([0-5]\d)$`)
	scheduleActions     = []string{"restart", "start", "stop", "backup", "command"}
)

// broadcastTemplate is the console command that messages every player, with
// {message} as the placeholder. Games without one get no warnings.
func broadcastTemplate(gameType string) string {
	switch normalizeGameType(gameType) {
	case "minecraft-java":
		return "say {message}"
	case "hytale":
		return "/say {message}"
	}
	return ""
}

// scheduleActionsFor lists the actions a server's game supports.
func scheduleActionsFor(gameType string) []string {
	caps := gameCapabilitiesFor(gameType)
	actions := []string{"restart", "start", "stop"}
	if caps.Backups || caps.NativeBackups {
		actions = append(actions, "backup")
	}
	if caps.ConsoleCommands {
		actions = append(actions, "command")
	}
	return actions
}

func newScheduleID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// normalizeSchedules validates a full schedule list for one server and fills
// in defaults. It returns an error a person can act on.
func normalizeSchedules(in []Schedule, gameType string) ([]Schedule, error) {
	if len(in) > maxSchedulesPerServer {
		return nil, fmt.Errorf("a server can have at most %d schedules", maxSchedulesPerServer)
	}
	allowed := scheduleActionsFor(gameType)
	canWarn := broadcastTemplate(gameType) != ""
	seen := map[string]bool{}
	out := make([]Schedule, 0, len(in))
	for i, sch := range in {
		label := fmt.Sprintf("schedule %d", i+1)
		if sch.ID == "" {
			sch.ID = newScheduleID()
		}
		if !scheduleIDPattern.MatchString(sch.ID) || seen[sch.ID] {
			return nil, fmt.Errorf("%s has an invalid or duplicate id", label)
		}
		seen[sch.ID] = true
		sch.Name = strings.TrimSpace(sch.Name)
		if len(sch.Name) > 60 || strings.ContainsAny(sch.Name, "\r\n\x00") {
			return nil, fmt.Errorf("%s: name must be at most 60 characters on one line", label)
		}
		if !containsString(allowed, sch.Action) {
			return nil, fmt.Errorf("%s: action %q is not available for this game", label, sch.Action)
		}
		if sch.Name == "" {
			sch.Name = strings.ToUpper(sch.Action[:1]) + sch.Action[1:]
		}
		sch.Command = strings.TrimSpace(sch.Command)
		if sch.Action == "command" {
			if sch.Command == "" || len(sch.Command) > 200 || strings.ContainsAny(sch.Command, "\r\n\x00") {
				return nil, fmt.Errorf("%s: enter a single-line console command of at most 200 characters", label)
			}
		} else {
			sch.Command = ""
		}
		switch sch.Kind {
		case "daily":
			if !scheduleTimePattern.MatchString(sch.Time) {
				return nil, fmt.Errorf("%s: time must be HH:MM in 24-hour format", label)
			}
			days := map[int]bool{}
			for _, d := range sch.Days {
				if d < 0 || d > 6 || days[d] {
					return nil, fmt.Errorf("%s: days must be distinct values from 0 (Sunday) to 6 (Saturday)", label)
				}
				days[d] = true
			}
			sort.Ints(sch.Days)
			if len(sch.Days) == 7 {
				sch.Days = nil
			}
			sch.EveryMinutes = 0
		case "interval":
			if sch.EveryMinutes < 5 || sch.EveryMinutes > 10080 {
				return nil, fmt.Errorf("%s: repeat every 5 minutes to 7 days", label)
			}
			sch.Time, sch.Days = "", nil
		default:
			return nil, fmt.Errorf("%s: choose daily or repeating timing", label)
		}
		if sch.Action != "restart" && sch.Action != "stop" || !canWarn {
			sch.WarnMinutes = 0
		}
		if sch.WarnMinutes < 0 || sch.WarnMinutes > 60 {
			return nil, fmt.Errorf("%s: warning time must be 0–60 minutes", label)
		}
		if sch.Kind == "interval" && sch.WarnMinutes >= sch.EveryMinutes {
			return nil, fmt.Errorf("%s: the warning must be shorter than the repeat interval", label)
		}
		out = append(out, sch)
	}
	return out, nil
}

// nextScheduleRun returns when sch is next due strictly after `after`, in the
// time zone of `after` (the container's local time in production).
func nextScheduleRun(sch Schedule, after time.Time) time.Time {
	if sch.Kind == "interval" {
		return after.Add(time.Duration(sch.EveryMinutes) * time.Minute)
	}
	m := scheduleTimePattern.FindStringSubmatch(sch.Time)
	if m == nil {
		return time.Time{}
	}
	hour, minute := int(m[1][0]-'0')*10+int(m[1][1]-'0'), int(m[2][0]-'0')*10+int(m[2][1]-'0')
	day := time.Date(after.Year(), after.Month(), after.Day(), hour, minute, 0, 0, after.Location())
	for i := 0; i < 8; i++ {
		candidate := day.AddDate(0, 0, i)
		// Rebuild from the date so daylight-saving changes keep the wall time.
		candidate = time.Date(candidate.Year(), candidate.Month(), candidate.Day(), hour, minute, 0, 0, after.Location())
		if !candidate.After(after) {
			continue
		}
		if len(sch.Days) == 0 || containsInt(sch.Days, int(candidate.Weekday())) {
			return candidate
		}
	}
	return time.Time{}
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// scheduleState is runtime bookkeeping for one schedule; it is not persisted,
// so runs missed while the manager was down are skipped, not replayed.
type scheduleState struct {
	next       time.Time
	lastRun    time.Time
	lastResult string
	running    bool
}

type scheduler struct {
	mu    sync.Mutex
	state map[string]*scheduleState // key: serverID + "/" + scheduleID
}

func scheduleKey(serverID, scheduleID string) string { return serverID + "/" + scheduleID }

// fireTime is when a run starts: early enough to send its warnings.
func fireTime(sch Schedule, next time.Time) time.Time {
	return next.Add(-time.Duration(sch.WarnMinutes) * time.Minute)
}

// schedulerTick starts every schedule whose fire time has passed. It is
// driven by a ticker in production and called directly by tests.
func (a *App) schedulerTick(now time.Time) {
	a.cfgMu.RLock()
	type job struct {
		server ServerSettings
		sch    Schedule
	}
	var jobs []job
	for _, s := range a.cfg.Servers {
		for _, sch := range a.cfg.Schedules[s.ID] {
			if sch.Enabled {
				jobs = append(jobs, job{s, sch})
			}
		}
	}
	a.cfgMu.RUnlock()

	a.sched.mu.Lock()
	if a.sched.state == nil {
		a.sched.state = map[string]*scheduleState{}
	}
	var due []job
	for _, j := range jobs {
		key := scheduleKey(j.server.ID, j.sch.ID)
		st := a.sched.state[key]
		if st == nil {
			st = &scheduleState{}
			a.sched.state[key] = st
		}
		if st.next.IsZero() {
			st.next = nextScheduleRun(j.sch, now)
			continue
		}
		if !st.running && !now.Before(fireTime(j.sch, st.next)) {
			st.running = true
			due = append(due, j)
		}
	}
	a.sched.mu.Unlock()

	for _, j := range due {
		go a.runSchedule(j.server.ID, j.sch)
	}
}

func (a *App) runSchedule(serverID string, sch Schedule) {
	key := scheduleKey(serverID, sch.ID)
	a.sched.mu.Lock()
	st := a.sched.state[key]
	at := st.next
	a.sched.mu.Unlock()

	result := "Done"
	if err := a.executeSchedule(serverID, sch, at); err != nil {
		result = err.Error()
	}
	if m, _, err := a.serverByID(serverID); err == nil {
		m.appendLog("controller", fmt.Sprintf("Schedule %q (%s): %s", sch.Name, sch.Action, result))
	}

	a.sched.mu.Lock()
	if st := a.sched.state[key]; st != nil {
		st.running = false
		st.lastRun = time.Now()
		st.lastResult = result
		// The next run follows the one just handled; anything missed while a
		// long action ran is skipped rather than run back to back.
		next := nextScheduleRun(sch, at)
		for !next.IsZero() && !next.After(time.Now()) {
			next = nextScheduleRun(sch, next)
		}
		st.next = next
	}
	a.sched.mu.Unlock()
}

// errSkipped marks a run with nothing to do, such as stopping a stopped server.
type errSkipped struct{ reason string }

func (e errSkipped) Error() string { return "Skipped: " + e.reason }

func (a *App) executeSchedule(serverID string, sch Schedule, at time.Time) error {
	m, s, err := a.serverByID(serverID)
	if err != nil {
		return err
	}
	caps := gameCapabilitiesFor(s.GameType)
	switch sch.Action {
	case "start":
		if m.IsRunning() {
			return errSkipped{"the server is already running"}
		}
		a.resetCrashes(serverID)
		return a.startServerByID(serverID)
	case "stop", "restart":
		if !m.IsRunning() {
			if sch.Action == "restart" {
				return errSkipped{"the server is not running"}
			}
			return errSkipped{"the server is already stopped"}
		}
		a.sendScheduleWarnings(m, s, sch, at)
		if err := a.stopAndWait(m, 100*time.Second); err != nil {
			return err
		}
		if sch.Action == "restart" {
			return a.startServerByID(serverID)
		}
		return nil
	case "backup":
		if caps.NativeBackups && m.IsRunning() {
			return m.SendCommand("/backup")
		}
		if !caps.Backups {
			return errSkipped{"backups are not supported for this game"}
		}
		_, _, err := a.createSnapshot(m, s)
		return err
	case "command":
		if !caps.ConsoleCommands {
			return errSkipped{"console commands are not supported for this game"}
		}
		if !m.IsRunning() {
			return errSkipped{"the server is not running"}
		}
		return m.SendCommand(sch.Command)
	}
	return fmt.Errorf("unknown action %q", sch.Action)
}

// sendScheduleWarnings announces a scheduled restart or stop, then waits until
// the scheduled time: once WarnMinutes before, and again one minute before.
func (a *App) sendScheduleWarnings(m *ServerManager, s ServerSettings, sch Schedule, at time.Time) {
	template := broadcastTemplate(s.GameType)
	if sch.WarnMinutes <= 0 || template == "" {
		return
	}
	what := "restart"
	if sch.Action == "stop" {
		what = "shut down"
	}
	say := func(minutes int) {
		unit := "minutes"
		if minutes == 1 {
			unit = "minute"
		}
		msg := fmt.Sprintf("The server will %s in %d %s.", what, minutes, unit)
		_ = m.SendCommand(strings.ReplaceAll(template, "{message}", msg))
	}
	say(sch.WarnMinutes)
	if sch.WarnMinutes > 1 {
		time.Sleep(time.Until(at.Add(-time.Minute)))
		if !m.IsRunning() {
			return
		}
		say(1)
	}
	time.Sleep(time.Until(at))
}

func (a *App) stopAndWait(m *ServerManager, timeout time.Duration) error {
	if err := m.Stop(); err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	for m.IsRunning() {
		if time.Now().After(deadline) {
			return errors.New("the server did not stop in time")
		}
		time.Sleep(250 * time.Millisecond)
	}
	return nil
}

func (a *App) schedulerLoop() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for now := range ticker.C {
		a.schedulerTick(now)
	}
}

// scheduleView is a schedule with its runtime status, as sent to the browser.
type scheduleView struct {
	Schedule
	NextRun    string `json:"nextRun,omitempty"`
	LastRun    string `json:"lastRun,omitempty"`
	LastResult string `json:"lastResult,omitempty"`
	Running    bool   `json:"running"`
}

func (a *App) scheduleViews(s ServerSettings) []scheduleView {
	a.cfgMu.RLock()
	list := append([]Schedule(nil), a.cfg.Schedules[s.ID]...)
	a.cfgMu.RUnlock()
	now := time.Now()
	a.sched.mu.Lock()
	defer a.sched.mu.Unlock()
	out := make([]scheduleView, 0, len(list))
	for _, sch := range list {
		v := scheduleView{Schedule: sch}
		if st := a.sched.state[scheduleKey(s.ID, sch.ID)]; st != nil {
			if !st.lastRun.IsZero() {
				v.LastRun = st.lastRun.Format(time.RFC3339)
			}
			v.LastResult, v.Running = st.lastResult, st.running
			if sch.Enabled && !st.next.IsZero() {
				v.NextRun = st.next.Format(time.RFC3339)
			}
		}
		if sch.Enabled && v.NextRun == "" {
			if next := nextScheduleRun(sch, now); !next.IsZero() {
				v.NextRun = next.Format(time.RFC3339)
			}
		}
		out = append(out, v)
	}
	return out
}

func (a *App) schedulesResponse(s ServerSettings) map[string]any {
	zone, offset := time.Now().Zone()
	return map[string]any{
		"schedules": a.scheduleViews(s),
		"actions":   scheduleActionsFor(s.GameType),
		"warnings":  broadcastTemplate(s.GameType) != "",
		"timezone":  fmt.Sprintf("%s (UTC%+03d:%02d)", zone, offset/3600, abs(offset%3600)/60),
		"now":       time.Now().Format(time.RFC3339),
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func (a *App) handleSchedulesGet(w http.ResponseWriter, r *http.Request) {
	_, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	jsonOut(w, 200, a.schedulesResponse(s))
}

func (a *App) handleSchedulesPut(w http.ResponseWriter, r *http.Request) {
	_, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	var body struct {
		Schedules []Schedule `json:"schedules"`
	}
	dec := decodeJSONLenient(r, &body)
	if dec != nil {
		jsonError(w, 400, dec.Error())
		return
	}
	list, err := normalizeSchedules(body.Schedules, s.GameType)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	a.cfgMu.Lock()
	if a.cfg.Schedules == nil {
		a.cfg.Schedules = map[string][]Schedule{}
	}
	previous := a.cfg.Schedules[s.ID]
	if len(list) == 0 {
		delete(a.cfg.Schedules, s.ID)
	} else {
		a.cfg.Schedules[s.ID] = list
	}
	err = a.saveConfigLocked()
	if err != nil {
		if previous == nil {
			delete(a.cfg.Schedules, s.ID)
		} else {
			a.cfg.Schedules[s.ID] = previous
		}
	}
	a.cfgMu.Unlock()
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	a.resetScheduleState(s.ID, list)
	jsonOut(w, 200, a.schedulesResponse(s))
}

// resetScheduleState recomputes next runs after an edit, keeping last-run
// history for schedules that still exist and are not currently running.
func (a *App) resetScheduleState(serverID string, list []Schedule) {
	now := time.Now()
	a.sched.mu.Lock()
	defer a.sched.mu.Unlock()
	if a.sched.state == nil {
		a.sched.state = map[string]*scheduleState{}
	}
	keep := map[string]bool{}
	for _, sch := range list {
		key := scheduleKey(serverID, sch.ID)
		keep[key] = true
		st := a.sched.state[key]
		if st == nil {
			st = &scheduleState{}
			a.sched.state[key] = st
		}
		if !st.running {
			st.next = nextScheduleRun(sch, now)
		}
	}
	prefix := serverID + "/"
	for key, st := range a.sched.state {
		if strings.HasPrefix(key, prefix) && !keep[key] && !st.running {
			delete(a.sched.state, key)
		}
	}
}

// removeServerSchedules drops a deleted server's schedules. Callers hold cfgMu.
func (a *App) removeServerSchedulesLocked(serverID string) {
	delete(a.cfg.Schedules, serverID)
	a.sched.mu.Lock()
	for key := range a.sched.state {
		if strings.HasPrefix(key, serverID+"/") {
			delete(a.sched.state, key)
		}
	}
	a.sched.mu.Unlock()
}

// decodeJSONLenient decodes a body that may carry read-only fields from a
// previous GET (nextRun, lastResult …), which the strict decoder rejects.
func decodeJSONLenient(r *http.Request, v any) error {
	if err := decodeJSONAllowUnknown(r, v); err != nil {
		log.Printf("schedules: %v", err)
		return errors.New("invalid schedule list")
	}
	return nil
}
