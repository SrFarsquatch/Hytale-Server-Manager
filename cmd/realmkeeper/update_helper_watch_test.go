package main

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// dockerFrame is one framed chunk of a non-TTY container's log stream.
func dockerFrame(stream byte, text string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(text)))
	return append(h, text...)
}

// fakeDockerAPI serves handler as the Docker Engine API.
func fakeDockerAPI(t *testing.T, handler http.HandlerFunc) *dockerAPI {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	return &dockerAPI{client: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}}}}
}

func TestDemuxDockerLogs(t *testing.T) {
	framed := append(dockerFrame(1, "Downloading image…\n"), dockerFrame(2, "Update failed: boom\n")...)
	if got := demuxDockerLogs(framed); got != "Downloading image…\nUpdate failed: boom\n" {
		t.Errorf("framed = %q", got)
	}
	if got := demuxDockerLogs([]byte("plain tty output\n")); got != "plain tty output\n" {
		t.Errorf("unframed = %q", got)
	}
}

func helperFake(t *testing.T, running func() bool, exitCode int, logs func() []byte) *dockerAPI {
	return fakeDockerAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/json"):
			code := 0
			if !running() {
				code = exitCode
			}
			w.Write([]byte(`{"State":{"Running":` + map[bool]string{true: "true", false: "false"}[running()] + `,"ExitCode":` + string(rune('0'+code)) + `}}`))
		case strings.HasSuffix(r.URL.Path, "/logs"):
			w.Write(logs())
		default:
			http.NotFound(w, r)
		}
	})
}

func TestWatchUpdateHelperReportsProgressThenFailure(t *testing.T) {
	var mu sync.Mutex
	phase := 0
	at := func() int { mu.Lock(); defer mu.Unlock(); return phase }
	d := helperFake(t, func() bool { return at() == 0 }, 1, func() []byte {
		if at() == 0 {
			return dockerFrame(1, "Downloading ghcr.io/srfarsquatch/realmkeeper:latest…\nStatus: Downloaded newer image\n")
		}
		return dockerFrame(1, "Downloading ghcr.io/srfarsquatch/realmkeeper:latest…\nUpdate failed: pull release image failed: denied\n")
	})
	setUpdateState("updating", "Starting the updater helper.", "0.6.86")
	t.Cleanup(func() { setUpdateState("idle", "", "") })
	done := make(chan struct{})
	go func() { watchUpdateHelper(d, "helper1", 20*time.Millisecond); close(done) }()

	waitFor(t, "progress message", func() bool {
		_, msg, _ := updateState.snapshot(time.Now())
		return msg == "Updating: Status: Downloaded newer image"
	})
	mu.Lock()
	phase = 1
	mu.Unlock()
	<-done
	status, msg, _ := updateState.snapshot(time.Now())
	if status != "failed" || !strings.Contains(msg, "exit code 1") || !strings.Contains(msg, "denied") || !strings.Contains(msg, "still running") {
		t.Errorf("after the helper failed: %s %q", status, msg)
	}
}

func TestWatchUpdateHelperLeavesASuccessfulUpdateAlone(t *testing.T) {
	d := helperFake(t, func() bool { return false }, 0, func() []byte { return dockerFrame(1, "Update complete\n") })
	setUpdateState("updating", "Starting the updater helper.", "0.6.86")
	t.Cleanup(func() { setUpdateState("idle", "", "") })
	watchUpdateHelper(d, "helper1", 10*time.Millisecond)
	if status, _, target := updateState.snapshot(time.Now()); status != "updating" || target != "0.6.86" {
		t.Errorf("a successful helper changed the state to %s %s", status, target)
	}
}

func TestReadPullProgressFindsErrorsAnywhereInTheStream(t *testing.T) {
	long := strings.Repeat(`{"status":"Downloading","id":"abc","progressDetail":{"current":1}}`+"\n", 300000)
	for name, c := range map[string]struct {
		body    string
		wantErr string
	}{
		"ok":           {long + `{"status":"Status: Downloaded newer image for x"}` + "\n", ""},
		"error at end": {long + `{"error":"unauthorized"}` + "\n", "unauthorized"},
		"http error":   {"denied", "HTTP 500"},
	} {
		code := 200
		if name == "http error" {
			code = 500
		}
		// About 20 MB of progress, more than the old 16 MB read limit.
		resp := &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(c.body))}
		err := readPullProgress(resp)
		if (c.wantErr == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("%s: err = %v, want %q", name, err, c.wantErr)
		}
	}
}
