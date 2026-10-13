package main

import "testing"

func TestGameAdapterForDefaultsLegacyTypeToHytale(t *testing.T) {
	adapter, err := gameAdapterFor("")
	if err != nil {
		t.Fatalf("gameAdapterFor(empty) error = %v", err)
	}
	if adapter.GameType() != "hytale" {
		t.Fatalf("adapter type = %q, want hytale", adapter.GameType())
	}
}

func TestGameAdapterForResolvesHytaleCaseInsensitively(t *testing.T) {
	adapter, err := gameAdapterFor(" Hytale ")
	if err != nil {
		t.Fatalf("gameAdapterFor(Hytale) error = %v", err)
	}
	if adapter.GameType() != "hytale" {
		t.Fatalf("adapter type = %q, want hytale", adapter.GameType())
	}
}

func TestGameAdapterForRejectsPlannedGamesUntilImplemented(t *testing.T) {
	for _, gameType := range []string{"minecraft-java", "terraria", "runescape-dragonwilds"} {
		t.Run(gameType, func(t *testing.T) {
			adapter, err := gameAdapterFor(gameType)
			if err == nil {
				t.Fatalf("gameAdapterFor(%q) error = nil, want unsupported error", gameType)
			}
			if adapter != nil {
				t.Fatalf("gameAdapterFor(%q) returned an adapter before support exists", gameType)
			}
		})
	}
}

func TestGameAdapterForRejectsUnknownGame(t *testing.T) {
	if adapter, err := gameAdapterFor("not-a-game"); err == nil || adapter != nil {
		t.Fatalf("gameAdapterFor(unknown) = (%v, %v), want (nil, error)", adapter, err)
	}
}
