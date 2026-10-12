package main

import "testing"

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
