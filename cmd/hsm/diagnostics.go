package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type DiagnosticCheck struct {
	ID             string `json:"id"`
	Label          string `json:"label"`
	State          string `json:"state"`
	Detail         string `json:"detail"`
	Recommendation string `json:"recommendation,omitempty"`
}

func diagnosticState(checks []DiagnosticCheck) string {
	state := "ok"
	for _, check := range checks {
		switch check.State {
		case "error":
			return "error"
		case "warning":
			if state != "error" {
				state = "warning"
			}
		}
	}
	return state
}

func (a *App) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}

	root := a.serverRoot(s)
	gameServer := filepath.Join(root, "game", "Server")
	status := m.Status()
	checks := make([]DiagnosticCheck, 0, 8)
	add := func(id, label, state, detail, recommendation string) {
		checks = append(checks, DiagnosticCheck{ID: id, Label: label, State: state, Detail: detail, Recommendation: recommendation})
	}

	if status.Installed {
		add("game-files", "Game files", "ok", "HytaleServer.jar and Assets.zip are present.", "")
	} else {
		add("game-files", "Game files", "error", "Required Hytale server files are missing.", "Open Install and import an official server bundle or run the Hytale downloader.")
	}

	startScript := filepath.Join(root, "game", "start.sh")
	if fileExists(startScript) {
		add("start-script", "Startup script", "ok", "game/start.sh is present.", "")
	} else if status.Installed {
		add("start-script", "Startup script", "error", "The game files exist, but game/start.sh is missing.", "Re-import the official server bundle or repair the installation before starting.")
	} else {
		add("start-script", "Startup script", "warning", "The startup script will be checked after installation.", "Finish installing the Hytale server files.")
	}

	configPath := filepath.Join(gameServer, "config.json")
	configBytes, configErr := os.ReadFile(configPath)
	switch {
	case configErr == nil:
		var value any
		if json.Unmarshal(configBytes, &value) == nil {
			add("config", "Server configuration", "ok", "config.json contains valid JSON.", "")
		} else {
			add("config", "Server configuration", "error", "config.json is not valid JSON.", "Stop the server and repair config.json, or restore a known-good snapshot.")
		}
	case os.IsNotExist(configErr):
		if status.Installed {
			add("config", "Server configuration", "warning", "config.json has not been created yet.", "Start the server once to let Hytale generate its configuration, if supported by this server build.")
		} else {
			add("config", "Server configuration", "info", "Configuration will be checked after installation.", "")
		}
	default:
		add("config", "Server configuration", "warning", "Could not read config.json: "+configErr.Error(), "Check the server data directory permissions.")
	}

	minPort, maxPort, _ := deploymentPortRange()
	if s.Port < minPort || s.Port > maxPort {
		add("port-range", "UDP port range", "error", fmt.Sprintf("Configured port %d is outside the published range %d–%d.", s.Port, minPort, maxPort), "Choose a port published by the Docker/CasaOS deployment and recreate the container if the published range must change.")
	} else if a.portInUse(s.Port, s.ID) {
		add("port-range", "UDP port assignment", "error", fmt.Sprintf("UDP port %d is assigned to more than one managed server.", s.Port), "Assign a unique UDP port to each server.")
	} else {
		add("port-range", "UDP port assignment", fmt.Sprintf("ok"), fmt.Sprintf("UDP port %d is within the configured range %d–%d and is unique in this manager.", s.Port, minPort, maxPort), "")
	}

	var fs syscall.Statfs_t
	if err := syscall.Statfs(root, &fs); err != nil {
		add("storage", "Data storage", "warning", "Could not inspect the server data filesystem: "+err.Error(), "Check that the persistent data volume is mounted and accessible.")
	} else {
		free := int64(fs.Bavail) * int64(fs.Bsize)
		switch {
		case free < 1<<30:
			add("storage", "Data storage", "error", fmt.Sprintf("Only %s of free space remains.", humanBytes(free)), "Free disk space before installing, updating, or backing up a world.")
		case free < 5<<30:
			add("storage", "Data storage", "warning", fmt.Sprintf("%s of free space remains.", humanBytes(free)), "Consider freeing space before creating large snapshots.")
		default:
			add("storage", "Data storage", "ok", fmt.Sprintf("%s of free space is available.", humanBytes(free)), "")
		}
	}

	if status.State == "crashed" {
		detail := fmt.Sprintf("The last server process exited with code %d.", status.ExitCode)
		if status.LastExitAt != "" {
			detail += " Last exit: " + status.LastExitAt + "."
		}
		add("process", "Server process", "warning", detail, "Open Console and inspect the latest output for the underlying error.")
	} else if status.Running {
		add("process", "Server process", "ok", "The server process is running.", "")
	} else if status.InstallBusy {
		add("process", "Server process", "info", "An installation task is running.", "")
	} else {
		add("process", "Server process", "info", "The server is stopped.", "Start the server after resolving any installation or configuration issues.")
	}

	if status.MemoryLimitBytes > 0 && !status.MeetsMinMemory {
		add("memory", "Memory allocation", "warning", "The container memory limit is below the recommended 4 GiB minimum.", "Increase the CasaOS/Docker memory limit if the host has enough RAM.")
	} else {
		add("memory", "Memory allocation", "ok", "No low-memory warning is currently reported.", "")
	}

	jsonOut(w, http.StatusOK, map[string]any{
		"serverId": s.ID,
		"serverName": s.ServerName,
		"state": diagnosticState(checks),
		"checks": checks,
	})
}

func humanBytes(n int64) string {
	if n < 0 {
		n = 0
	}
	const unit = int64(1024)
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := unit, 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %s", float64(n)/float64(div), []string{"KB", "MB", "GB", "TB", "PB"}[exp])
}

func diagnosticSummaryText(state string) string {
	switch strings.ToLower(state) {
	case "error":
		return "One or more checks need attention."
	case "warning":
		return "Some checks recommend attention."
	default:
		return "No blocking issues were detected."
	}
}
