package main

import (
	"fmt"
	"strconv"
	"strings"
)

// GameAdapter owns game-specific launch preparation while ServerManager remains
// responsible for the shared host-process lifecycle (start, stop, logs and stdin).
// Add an adapter only when its install and launch behavior has been verified.
type GameAdapter interface {
	GameType() string
	PrepareLaunch(app *App, settings ServerSettings) ([]string, error)
}

type HytaleAdapter struct{}

func (HytaleAdapter) GameType() string { return "hytale" }

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
	case "minecraft-java", "terraria", "runescape-dragonwilds":
		return nil, fmt.Errorf("%s server support is not implemented yet", gameType)
	default:
		return nil, fmt.Errorf("unknown game type %q", gameType)
	}
}
