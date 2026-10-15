package main

import (
	"math"
	"path/filepath"
	"testing"
)

func TestParseCPUStatUsage(t *testing.T) {
	got, ok := parseCPUStatUsage("usage_usec 250000\nuser_usec 120000\nsystem_usec 130000\n")
	if !ok || got != 250000000 {
		t.Fatalf("got (%d, %v), want (250000000, true)", got, ok)
	}
	if _, ok := parseCPUStatUsage("user_usec 10\n"); ok {
		t.Fatal("accepted missing usage_usec")
	}
}

func TestParseCPUQuota(t *testing.T) {
	got, ok := parseCPUQuota("150000 100000")
	if !ok || got != 1.5 {
		t.Fatalf("got (%v, %v), want (1.5, true)", got, ok)
	}
	if _, ok := parseCPUQuota("0 100000"); ok {
		t.Fatal("accepted zero quota")
	}
	if _, ok := parseCPUQuota("invalid"); ok {
		t.Fatal("accepted malformed quota")
	}
}

func TestCPUPercent(t *testing.T) {
	if got := cpuPercent(500000000, 1000000000, 1); math.Abs(got-50) > .01 {
		t.Fatalf("got %v, want 50", got)
	}
	if got := cpuPercent(1000000000, 1000000000, 2); math.Abs(got-50) > .01 {
		t.Fatalf("two-core result = %v, want 50", got)
	}
	if got := cpuPercent(2000000000, 1000000000, 1); got != 100 {
		t.Fatalf("result should cap at 100, got %v", got)
	}
	if got := cpuPercent(1, 0, 1); got != 0 {
		t.Fatalf("zero interval result = %v, want 0", got)
	}
}

func TestDiskUsage(t *testing.T) {
	dir := t.TempDir()
	used, total, free, err := diskUsage(dir)
	if err != nil {
		t.Fatal(err)
	}
	if total <= 0 || used < 0 || free < 0 || used > total {
		t.Fatalf("invalid disk usage: %d/%d, free %d", used, total, free)
	}
	if _, _, _, err := diskUsage(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("expected error for nonexistent path")
	}
}

func TestDeploymentPortRangeDefaults(t *testing.T) {
	for _, prefix := range []string{"REALMKEEPER_", "HSM_", "ORBIS_"} {
		for _, name := range []string{"GAME_PORT_START", "GAME_PORT_END", "GAME_PORT"} {
			t.Setenv(prefix+name, "")
		}
	}
	start, end, locked := deploymentPortRange()
	if start != 5520 || end != 5525 || locked {
		t.Fatalf("deploymentPortRange() = (%d, %d, %v), want (5520, 5525, false)", start, end, locked)
	}
}
