package main

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestLoginRateLimitBlocksAfterTenFailures(t *testing.T) {
	a := &App{}
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	for i := 0; i < loginAttemptLimit; i++ {
		if blocked, _ := a.loginBlocked("192.0.2.1", now); blocked {
			t.Fatalf("attempt %d was blocked before the limit", i+1)
		}
		a.recordLoginFailure("192.0.2.1", now)
	}
	blocked, wait := a.loginBlocked("192.0.2.1", now)
	if !blocked {
		t.Fatal("expected address to be blocked after ten failures")
	}
	if wait <= 0 || wait > loginBlockDuration {
		t.Fatalf("unexpected retry duration: %v", wait)
	}
	if blocked, _ := a.loginBlocked("192.0.2.2", now); blocked {
		t.Fatal("rate limit should be isolated by client address")
	}
}

func TestLoginRateLimitResetsAfterWindow(t *testing.T) {
	a := &App{}
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	for i := 0; i < loginAttemptLimit; i++ {
		a.recordLoginFailure("192.0.2.1", now)
	}
	blocked, _ := a.loginBlocked("192.0.2.1", now.Add(loginWindow+time.Second))
	if blocked {
		t.Fatal("expected rate limit to reset after the failure window")
	}
}

func TestClientAddressUsesRemoteAddrNotForwardedHeader(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/login", nil)
	r.RemoteAddr = "192.0.2.9:12345"
	r.Header.Set("X-Forwarded-For", "198.51.100.7")
	if got := clientAddress(r); got != "192.0.2.9" {
		t.Fatalf("clientAddress() = %q, want actual peer IP", got)
	}
}
