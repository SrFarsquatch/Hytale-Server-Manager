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
