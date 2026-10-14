package main

import (
	"strings"
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.5.1", "0.5.0", 1},
		{"1.0.0", "0.99.99", 1},
		{"0.5.0", "0.5.0", 0},
		{"0.4.9", "0.5.0", -1},
		{"v0.5.1", "0.5.0", 1},
		{"1.0.0", "1.0.0-rc.1", 1},
		{"1.0.0-rc.2", "1.0.0", -1},
	}
	for _, tc := range cases {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestValidReleaseVersion(t *testing.T) {
	for _, version := range []string{"0.5.1", "1.2.3", "1.2.3-rc.1"} {
		if !validReleaseVersion(version) {
			t.Errorf("validReleaseVersion(%q) = false, want true", version)
		}
	}
	for _, version := range []string{"latest", "main", "../0.5.1", "0.5", "v0.5.1", ""} {
		if validReleaseVersion(version) {
			t.Errorf("validReleaseVersion(%q) = true, want false", version)
		}
	}
}

func TestIsDockerContainerID(t *testing.T) {
	for _, value := range []string{
		"0123456789ab",
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	} {
		if !isDockerContainerID(value) {
			t.Errorf("isDockerContainerID(%q) = false, want true", value)
		}
	}
	for _, value := range []string{
		"",
		"hytale-server-manager",
		"0123456789",
		"0123456789az",
		"container-0123456789ab",
	} {
		if isDockerContainerID(value) {
			t.Errorf("isDockerContainerID(%q) = true, want false", value)
		}
	}
}

func TestAppUpdateStateExpiresStaleUpdate(t *testing.T) {
	started := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	state := appUpdateState{
		status: "updating",
		message: "Starting the updater helper",
		target: "0.7.0",
		startedAt: started,
	}
	status, message, target := state.snapshot(started.Add(13 * time.Minute))
	if status != "failed" {
		t.Fatalf("status = %q, want failed", status)
	}
	if !strings.Contains(message, "12 minutes") {
		t.Fatalf("message = %q, want stale-update explanation", message)
	}
	if target != "" {
		t.Fatalf("target = %q, want empty after expiration", target)
	}
}

func TestAppUpdateStateDoesNotExpireRecentUpdate(t *testing.T) {
	started := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	state := appUpdateState{status: "updating", message: "Pulling image", target: "0.7.0", startedAt: started}
	status, message, target := state.snapshot(started.Add(2 * time.Minute))
	if status != "updating" || message != "Pulling image" || target != "0.7.0" {
		t.Fatalf("snapshot = (%q, %q, %q), want active update", status, message, target)
	}
}
