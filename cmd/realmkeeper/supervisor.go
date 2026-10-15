package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type ServerManager struct {
	mu         sync.Mutex
	rootDir    string
	toolsDir   string
	gameType   string
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	running    bool
	phase      string
	startedAt  time.Time
	exitCode   int
	lastExitAt time.Time
	// lastExitCrashed is false when the last exit followed a stop request,
	// even with a non-zero code: Dragonwilds is stopped with SIGINT and
	// always reports one.
	lastExitCrashed bool
	installBusy     bool
	logs            *LogRing
	hub             *LogHub
	// Stop escalation delays; zero uses the production defaults.
	stopGrace     time.Duration
	stopInterrupt time.Duration
	// backupMu serialises snapshot creation and restore for this server.
	backupMu sync.Mutex
	// stopRequested is set by Stop so an exit it caused is not a crash.
	stopRequested bool
	// onCrash runs after the process exits non-zero without a stop request.
	onCrash func()
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
	State            string `json:"state"`
	PID              int    `json:"pid"`
	UptimeSeconds    int64  `json:"uptimeSeconds"`
	ExitCode         int    `json:"exitCode"`
	LastExitAt       string `json:"lastExitAt,omitempty"`
	MemoryBytes      int64  `json:"memoryBytes"`
	MemoryLimitBytes int64  `json:"memoryLimitBytes"`
	MeetsMinMemory   bool   `json:"meetsMinimumMemory"`
	ControllerVers   string `json:"controllerVersion"`
}

func NewServerManager(rootDir, toolsDir string, gameTypes ...string) *ServerManager {
	gameType := "hytale"
	if len(gameTypes) > 0 && strings.TrimSpace(gameTypes[0]) != "" {
		gameType = strings.ToLower(strings.TrimSpace(gameTypes[0]))
	}
	return &ServerManager{rootDir: rootDir, toolsDir: toolsDir, gameType: gameType, logs: &LogRing{max: 2500}, hub: &LogHub{clients: map[chan LogLine]struct{}{}}}
}

func (m *ServerManager) Installed() bool {
	adapter, err := gameAdapterFor(m.gameType)
	if err != nil {
		return false
	}
	return adapter.Installed(m.rootDir)
}

func (m *ServerManager) IsRunning() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.running }

func (m *ServerManager) InstallBusy() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.installBusy }

func serverDisplayState(running, installBusy, installed bool, exitCode int, lastExitAt time.Time) string {
	return serverDisplayStateWithPhase(running, "", installBusy, installed, exitCode, lastExitAt)
}

func serverDisplayStateWithPhase(running bool, phase string, installBusy, installed bool, exitCode int, lastExitAt time.Time) string {
	if running && (phase == "starting" || phase == "stopping") {
		return phase
	}
	if running {
		return "online"
	}
	if installBusy {
		return "installing"
	}
	if !installed {
		return "not-installed"
	}
	if !lastExitAt.IsZero() && exitCode != 0 {
		return "crashed"
	}
	return "offline"
}

func (m *ServerManager) Status() Status {
	m.mu.Lock()
	running := m.running
	started := m.startedAt
	exitCode := m.exitCode
	lastExit := m.lastExitAt
	stateCode := exitCode
	if !m.lastExitCrashed {
		stateCode = 0 // a requested stop is shown as offline, not crashed
	}
	busy := m.installBusy
	phase := m.phase
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
	st.State = serverDisplayStateWithPhase(running, phase, busy, st.Installed, stateCode, lastExit)
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
		return errors.New("server files are not installed yet")
	}
	adapter, err := gameAdapterFor(m.gameType)
	if err != nil {
		return err
	}
	spec, err := adapter.BuildProcess(m.rootDir, args)
	if err != nil {
		return err
	}
	cmd := exec.Command(spec.Path, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = append(os.Environ(), "HOME="+pathsFor(m.rootDir).Home())
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
	m.stopRequested = false
	m.running = true
	m.startedAt = time.Now()
	m.phase = "starting"
	m.exitCode = 0
	m.lastExitCrashed = false
	m.appendLogLocked("controller", fmt.Sprintf("Starting %s server on PID %d", m.gameType, cmd.Process.Pid))
	go m.scanPipe(stdout, "server")
	go m.scanPipe(stderr, "stderr")
	go m.wait(cmd)
	go func() {
		time.Sleep(5 * time.Second)
		m.mu.Lock()
		if m.cmd == cmd && m.running && m.phase == "starting" {
			m.phase = "running"
		}
		m.mu.Unlock()
	}()
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
	crashed := false
	if m.cmd == cmd {
		m.running = false
		m.phase = ""
		m.stdin = nil
		m.exitCode = code
		m.lastExitAt = time.Now()
		crashed = code != 0 && !m.stopRequested
		m.lastExitCrashed = crashed
	}
	if code != 0 && !crashed && m.cmd == cmd {
		m.appendLogLocked("controller", fmt.Sprintf("Server stopped as requested (exit code %d)", code))
	} else {
		m.appendLogLocked("controller", fmt.Sprintf("Server process exited with code %d", code))
	}
	onCrash := m.onCrash
	m.mu.Unlock()
	if crashed && onCrash != nil {
		go onCrash()
	}
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
	cmd := m.cmd
	pid := cmd.Process.Pid
	stdin := m.stdin
	graceTimeout, interruptTimeout := m.stopTimeouts()
	adapter, adapterErr := gameAdapterFor(m.gameType)
	if adapterErr != nil {
		m.mu.Unlock()
		return adapterErr
	}
	m.appendLogLocked("controller", "Requesting graceful shutdown")
	err := adapter.RequestStop(pid, stdin)
	if err == nil {
		m.phase = "stopping"
		m.stopRequested = true
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	// Escalate only against the process this call asked to stop. After a quick
	// restart a different process is running, and it must not be signalled.
	stillRunning := func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.cmd == cmd && m.running
	}
	go func() {
		time.Sleep(graceTimeout)
		if stillRunning() {
			m.appendLog("controller", "Graceful shutdown timed out; sending SIGINT")
			_ = syscall.Kill(-pid, syscall.SIGINT)
			time.Sleep(interruptTimeout)
			if stillRunning() {
				m.appendLog("controller", "Server still running; forcing process group to stop")
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		}
	}()
	return nil
}

// stopTimeouts returns how long Stop waits for a graceful exit before SIGINT,
// then before SIGKILL. Tests shorten them; zero means the production default.
func (m *ServerManager) stopTimeouts() (time.Duration, time.Duration) {
	grace, interrupt := m.stopGrace, m.stopInterrupt
	if grace <= 0 {
		grace = 75 * time.Second
	}
	if interrupt <= 0 {
		interrupt = 10 * time.Second
	}
	return grace, interrupt
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
