package main

import (
	"errors"
	"strings"
	"testing"
)

func TestParseJavaMajor(t *testing.T) {
	cases := map[string]int{
		`openjdk version "25.0.1" 2025-10-21`:     25,
		`openjdk version "21.0.4" 2024-07-16 LTS`: 21,
		`java version "1.8.0_392"`:                8,
		`openjdk version "17" 2021-09-14`:         17,
	}
	for out, want := range cases {
		if got, ok := parseJavaMajor(out); !ok || got != want {
			t.Errorf("parseJavaMajor(%q) = %d, %v; want %d", out, got, ok, want)
		}
	}
	if _, ok := parseJavaMajor("bash: java: command not found"); ok {
		t.Error("parsed a version from unrelated output")
	}
}

func TestCheckMinecraftJava(t *testing.T) {
	dir := t.TempDir()
	java := func(v int) func() (int, error) { return func() (int, error) { return v, nil } }

	// No recorded requirement (installed before this check existed): allowed.
	if err := checkMinecraftJava(dir, java(8)); err != nil {
		t.Fatalf("unknown requirement blocked start: %v", err)
	}
	if err := writeMinecraftInstallInfo(dir, minecraftInstallInfo{Version: "1.21.4", JavaMajor: 21}); err != nil {
		t.Fatal(err)
	}
	if err := checkMinecraftJava(dir, java(25)); err != nil {
		t.Errorf("newer Java rejected: %v", err)
	}
	if err := checkMinecraftJava(dir, java(21)); err != nil {
		t.Errorf("exact Java rejected: %v", err)
	}
	err := checkMinecraftJava(dir, java(17))
	if err == nil || !strings.Contains(err.Error(), "needs Java 21") || !strings.Contains(err.Error(), "Java 17") {
		t.Errorf("old Java error = %v", err)
	}
	if err := checkMinecraftJava(dir, func() (int, error) { return 0, errors.New("no java") }); err == nil {
		t.Error("missing Java was not reported")
	}
}
