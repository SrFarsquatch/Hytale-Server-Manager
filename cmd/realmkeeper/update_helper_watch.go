package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// watchUpdateHelper follows the update helper container. While it runs, its
// latest progress line becomes the update message. If it stops with an
// error, the manager is still the old version, so the update is marked
// failed with the helper's last output instead of waiting for a restart
// that will never come.
func watchUpdateHelper(d *dockerAPI, helperID string, every time.Duration) {
	deadline := time.Now().Add(13 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(every)
		if status, _, _ := updateState.snapshot(time.Now()); status != "updating" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		running, exitCode, output, err := updateHelperStatus(ctx, d, helperID)
		cancel()
		if err != nil {
			continue
		}
		lines := nonEmptyLines(output)
		if running {
			if len(lines) > 0 {
				setUpdateMessage("Updating: " + lines[len(lines)-1])
			}
			continue
		}
		if exitCode == 0 {
			return // the helper finished; this manager is being replaced
		}
		detail := "no output"
		if len(lines) > 0 {
			detail = strings.Join(lines[max(0, len(lines)-3):], " · ")
		}
		if len(detail) > 600 {
			detail = detail[len(detail)-600:]
		}
		setUpdateState("failed", fmt.Sprintf("The update helper stopped with exit code %d: %s. This version is still running, so you can try again or update the app from CasaOS.", exitCode, detail), "")
		return
	}
}

// setUpdateMessage changes the message of an update that is still running.
func setUpdateMessage(message string) {
	updateState.mu.Lock()
	if updateState.status == "updating" {
		updateState.message = message
	}
	updateState.mu.Unlock()
}

// updateHelperStatus reads the helper container's state and recent output.
func updateHelperStatus(ctx context.Context, d *dockerAPI, id string) (running bool, exitCode int, output string, err error) {
	b, err := d.call(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/json", nil, http.StatusOK)
	if err != nil {
		return false, 0, "", err
	}
	var inspect struct {
		State struct {
			Running  bool `json:"Running"`
			ExitCode int  `json:"ExitCode"`
		} `json:"State"`
	}
	if err := json.Unmarshal(b, &inspect); err != nil {
		return false, 0, "", err
	}
	logs, err := d.call(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/logs?stdout=1&stderr=1&tail=30", nil, http.StatusOK)
	if err != nil {
		return false, 0, "", err
	}
	return inspect.State.Running, inspect.State.ExitCode, demuxDockerLogs(logs), nil
}

// demuxDockerLogs strips the 8-byte frame headers Docker puts on the logs of
// a container without a terminal. Output that is not framed is returned as is.
func demuxDockerLogs(b []byte) string {
	var out strings.Builder
	rest := b
	for len(rest) > 0 {
		if len(rest) < 8 || rest[0] > 2 || rest[1] != 0 || rest[2] != 0 || rest[3] != 0 {
			if out.Len() == 0 {
				return string(b)
			}
			out.Write(rest)
			break
		}
		size := int(binary.BigEndian.Uint32(rest[4:8]))
		if 8+size > len(rest) {
			size = len(rest) - 8
		}
		out.Write(rest[8 : 8+size])
		rest = rest[8+size:]
	}
	return out.String()
}

func nonEmptyLines(s string) []string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}
