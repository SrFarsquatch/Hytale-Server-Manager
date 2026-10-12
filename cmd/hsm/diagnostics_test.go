package main

import "testing"

func TestDiagnosticState(t *testing.T) {
	tests := []struct {
		name string
		checks []DiagnosticCheck
		want string
	}{
		{name: "empty is healthy", want: "ok"},
		{name: "healthy checks", checks: []DiagnosticCheck{{State: "ok"}, {State: "info"}}, want: "ok"},
		{name: "warning", checks: []DiagnosticCheck{{State: "ok"}, {State: "warning"}}, want: "warning"},
		{name: "error takes precedence", checks: []DiagnosticCheck{{State: "warning"}, {State: "error"}}, want: "error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := diagnosticState(tt.checks); got != tt.want {
				t.Fatalf("diagnosticState() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHumanBytes(t *testing.T) {
	if got := humanBytes(0); got != "0 B" {
		t.Fatalf("humanBytes(0) = %q", got)
	}
	if got := humanBytes(1024); got != "1.0 KB" {
		t.Fatalf("humanBytes(1024) = %q", got)
	}
}
