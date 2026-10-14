package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// publishedPorts records which game ports the manager's own container
// publishes to the host. The app can only hand out ports; Docker or CasaOS
// decides which ones players can reach, so the UI warns about the gap.
//
// A running container's port bindings cannot change, so one successful read
// holds for the life of this process.
type publishedPorts struct {
	mu    sync.RWMutex
	known bool
	// all is true when every port is reachable (host networking).
	all   bool
	ports map[string]bool // "5520/udp"
}

// published reports whether port/protocol is published. ok is false when the
// container's ports could not be read (no Docker socket, not in Docker), in
// which case no warning should be shown.
func (p *publishedPorts) published(port int, protocol string) (published, ok bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !p.known {
		return false, false
	}
	if p.all {
		return true, true
	}
	return p.ports[strconv.Itoa(port)+"/"+strings.ToLower(protocol)], true
}

func (p *publishedPorts) set(all bool, ports map[string]bool) {
	p.mu.Lock()
	p.known, p.all, p.ports = true, all, ports
	p.mu.Unlock()
}

// parseContainerPorts reads port bindings from a Docker container inspection.
// A port that is exposed but not published has a null binding list.
func parseContainerPorts(inspect []byte) (all bool, ports map[string]bool, err error) {
	var c struct {
		HostConfig struct {
			NetworkMode string `json:"NetworkMode"`
		} `json:"HostConfig"`
		NetworkSettings struct {
			Ports map[string][]struct {
				HostIP   string `json:"HostIp"`
				HostPort string `json:"HostPort"`
			} `json:"Ports"`
		} `json:"NetworkSettings"`
	}
	if err := json.Unmarshal(inspect, &c); err != nil {
		return false, nil, err
	}
	if c.HostConfig.NetworkMode == "host" {
		return true, nil, nil
	}
	ports = map[string]bool{}
	for key, bindings := range c.NetworkSettings.Ports {
		if len(bindings) > 0 {
			ports[strings.ToLower(key)] = true
		}
	}
	return false, ports, nil
}

// watchPublishedPorts reads this container's port bindings, retrying while
// Docker is unreachable. Outside Docker, or without the socket, it gives up
// silently and the UI shows no port warnings.
func (a *App) watchPublishedPorts() {
	if !dockerSocketAvailable() {
		return
	}
	for attempt := 0; attempt < 10; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Minute)
		}
		id, err := currentContainerID()
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		b, err := newDockerAPI().call(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/json", nil, http.StatusOK)
		cancel()
		if err != nil {
			continue
		}
		all, ports, err := parseContainerPorts(b)
		if err != nil {
			log.Printf("published ports: %v", err)
			return
		}
		a.ports.set(all, ports)
		return
	}
}
