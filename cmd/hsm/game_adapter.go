package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
