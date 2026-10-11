package main

import (
	"testing"
	"time"
)

func TestServerDisplayState(t *testing.T) {
	exitedAt := time.Date(2026, time.October, 11, 21, 15, 0, 0, time.UTC)

	tests := []struct {
		name        string
		running     bool
		installBusy bool
		installed   bool
		exitCode    int
		lastExitAt  time.Time
		want        string
	}{
		{name: "running takes precedence", running: true, installBusy: true, installed: true, want: "online"},
		{name: "installing", installBusy: true, installed: true, want: "installing"},
		{name: "not installed", want: "not-installed"},
		{name: "crashed process", installed: true, exitCode: 9, lastExitAt: exitedAt, want: "crashed"},
		{name: "cleanly stopped", installed: true, exitCode: 0, lastExitAt: exitedAt, want: "offline"},
		{name: "never started", installed: true, want: "offline"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := serverDisplayState(tt.running, tt.installBusy, tt.installed, tt.exitCode, tt.lastExitAt)
			if got != tt.want {
				t.Fatalf("serverDisplayState() = %q, want %q", got, tt.want)
			}
		})
	}
}
