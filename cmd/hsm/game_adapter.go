package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ProcessLaunch is the game-specific part of process creation. ServerManager
// owns the shared lifecycle, log pipes, process group, and exit-state tracking.
type ProcessLaunch struct {
	Path string
	Args []string
	Dir  string
}

// GameAdapter owns game-specific install detection, launch preparation, process
// selection, and graceful shutdown. Shared process lifecycle stays in
// ServerManager so every game gets the same logging and crash-state behavior.
type GameAdapter interface {
	GameType() string
	Installed(rootDir string) bool
	PrepareLaunch(app *App, settings ServerSettings) ([]string, error)
	BuildProcess(rootDir string, args []string) (ProcessLaunch, error)
	RequestStop(pid int, stdin io.WriteCloser) error
}

type HytaleAdapter struct{}

func (HytaleAdapter) GameType() string { return "hytale" }

func (HytaleAdapter) Installed(rootDir string) bool {
	return fileExists(filepath.Join(rootDir, "game", "Server", "HytaleServer.jar")) &&
		fileExists(filepath.Join(rootDir, "game", "Assets.zip"))
}

func (HytaleAdapter) PrepareLaunch(app *App, settings ServerSettings) ([]string, error) {
	if err := app.writeJVMOptionsFor(settings); err != nil {
		return nil, err
	}
	if err := app.applyHytaleConfig(settings); err != nil {
		return nil, fmt.Errorf("could not apply Hytale settings: %w", err)
	}
	args := []string{
		"--bind", fmt.Sprintf("0.0.0.0:%d", settings.Port),
		"--backup-frequency", strconv.Itoa(settings.BackupFrequency),
	}
	args = append(args, splitArgs(settings.ExtraArgs)...)
	return args, nil
}

func (HytaleAdapter) BuildProcess(rootDir string, args []string) (ProcessLaunch, error) {
	gameDir := filepath.Join(rootDir, "game")
	startScript := filepath.Join(gameDir, "start.sh")
	if !fileExists(startScript) {
		return ProcessLaunch{}, errors.New("game/start.sh is missing; import an official server bundle or run /update setup first")
	}
	if err := os.Chmod(startScript, 0755); err != nil {
		return ProcessLaunch{}, fmt.Errorf("make Hytale start script executable: %w", err)
	}
	cmdArgs := append([]string{startScript}, args...)
	return ProcessLaunch{Path: "bash", Args: cmdArgs, Dir: gameDir}, nil
}

func (HytaleAdapter) RequestStop(_ int, stdin io.WriteCloser) error {
	if stdin == nil {
		return errors.New("server stdin is unavailable")
	}
	return writeStopCommand(stdin)
}

type DragonwildsAdapter struct{}

func (DragonwildsAdapter) GameType() string { return "runescape-dragonwilds" }

func (DragonwildsAdapter) Installed(rootDir string) bool {
	return fileExists(filepath.Join(rootDir, "game", "RSDragonwildsServer.sh")) ||
		fileExists(filepath.Join(rootDir, "game", "RSDragonwilds", "Binaries", "Linux", "RSDragonwildsServer-Linux-Shipping"))
}

func (DragonwildsAdapter) PrepareLaunch(app *App, settings ServerSettings) ([]string, error) {
	if strings.TrimSpace(settings.DragonwildsOwnerID) == "" {
		return nil, errors.New("Dragonwilds Owner ID is required; enter your in-game Player ID in server settings")
	}
	if strings.TrimSpace(settings.DragonwildsAdminPassword) == "" {
		return nil, errors.New("Dragonwilds admin password is required")
	}
	if settings.Port < 1 || settings.Port > 65535 {
		return nil, errors.New("Dragonwilds UDP port is invalid")
	}
	if err := writeDragonwildsConfig(app.serverRoot(settings), settings); err != nil {
		return nil, err
	}
	return []string{"-log", "-NewConsole", "-port=" + strconv.Itoa(settings.Port)}, nil
}

func (DragonwildsAdapter) BuildProcess(rootDir string, args []string) (ProcessLaunch, error) {
	gameDir := filepath.Join(rootDir, "game")
	launcher := filepath.Join(gameDir, "RSDragonwildsServer.sh")
	if fileExists(launcher) {
		if err := os.Chmod(launcher, 0755); err != nil {
			return ProcessLaunch{}, fmt.Errorf("make Dragonwilds launcher executable: %w", err)
		}
		return ProcessLaunch{Path: launcher, Args: append([]string(nil), args...), Dir: gameDir}, nil
	}
	binary := filepath.Join(gameDir, "RSDragonwilds", "Binaries", "Linux", "RSDragonwildsServer-Linux-Shipping")
	if !fileExists(binary) {
		return ProcessLaunch{}, errors.New("Dragonwilds launcher or Linux server binary is missing")
	}
	if err := os.Chmod(binary, 0755); err != nil {
		return ProcessLaunch{}, fmt.Errorf("make Dragonwilds server binary executable: %w", err)
	}
	return ProcessLaunch{Path: binary, Args: append([]string(nil), args...), Dir: gameDir}, nil
}

func (DragonwildsAdapter) RequestStop(pid int, _ io.WriteCloser) error {
	return syscall.Kill(-pid, syscall.SIGINT)
}

func writeStopCommand(stdin io.WriteCloser) error {
	_, err := io.WriteString(stdin, "/stop\n")
	return err
}

// gameAdapterFor intentionally returns only implemented adapters. Catalog entries
// for planned games must not be mistaken for working installers or launchers.
func gameAdapterFor(gameType string) (GameAdapter, error) {
	gameType = strings.TrimSpace(strings.ToLower(gameType))
	if gameType == "" {
		gameType = "hytale" // compatibility for old settings and test fixtures
	}
	switch gameType {
	case "hytale":
		return HytaleAdapter{}, nil
	case "runescape-dragonwilds":
		return DragonwildsAdapter{}, nil
	case "minecraft-java", "terraria":
		return nil, fmt.Errorf("%s server support is not implemented yet", gameType)
	default:
		return nil, fmt.Errorf("unknown game type %q", gameType)
	}
}
