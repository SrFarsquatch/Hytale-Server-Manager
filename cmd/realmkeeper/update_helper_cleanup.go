package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const updateHelperPrefix = "hsm-update-helper-"

// updateHelperContainer is the part of Docker's container list we need.
type updateHelperContainer struct {
	ID      string   `json:"Id"`
	Names   []string `json:"Names"`
	State   string   `json:"State"`
	Status  string   `json:"Status"`
	Created int64    `json:"Created"`
}

var exitedStatus = regexp.MustCompile(`^Exited \((-?\d+)\)`)

// updateHelpersToRemove picks finished update-helper containers to delete.
// Successful helpers are always removed. Of the failed ones only the newest
// is kept, so its logs remain available for diagnosing a failed update.
// Running helpers and unrelated containers are never selected.
func updateHelpersToRemove(containers []updateHelperContainer) []string {
	var remove []string
	var failed []updateHelperContainer
	for _, c := range containers {
		isHelper := false
		for _, name := range c.Names {
			if strings.HasPrefix(strings.TrimPrefix(name, "/"), updateHelperPrefix) {
				isHelper = true
				break
			}
		}
		if !isHelper || c.ID == "" || (c.State != "exited" && c.State != "dead" && c.State != "created") {
			continue
		}
		code := -1
		if m := exitedStatus.FindStringSubmatch(c.Status); m != nil {
			code, _ = strconv.Atoi(m[1])
		}
		if c.State == "exited" && code == 0 {
			remove = append(remove, c.ID)
			continue
		}
		failed = append(failed, c)
	}
	sort.Slice(failed, func(i, j int) bool { return failed[i].Created > failed[j].Created })
	for i, c := range failed {
		if i > 0 {
			remove = append(remove, c.ID)
		}
	}
	return remove
}

// cleanupUpdateHelpers removes leftover update-helper containers. CasaOS lists
// every container it did not install as a "Legacy app", so finished helpers
// would otherwise accumulate on the dashboard after each update.
func cleanupUpdateHelpers() {
	if _, err := os.Stat(dockerSocketPath); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	d := newDockerAPI()
	listBytes, err := d.call(ctx, http.MethodGet, "/containers/json?all=1&filters="+url.QueryEscape(`{"name":["`+updateHelperPrefix+`"]}`), nil, http.StatusOK)
	if err != nil {
		log.Printf("update helper cleanup: %v", err)
		return
	}
	var containers []updateHelperContainer
	if err := json.Unmarshal(listBytes, &containers); err != nil {
		log.Printf("update helper cleanup: decode container list: %v", err)
		return
	}
	for _, id := range updateHelpersToRemove(containers) {
		if _, err := d.call(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id)+"?v=true", nil, http.StatusNoContent, http.StatusNotFound); err != nil {
			log.Printf("update helper cleanup: remove %s: %v", id, err)
		}
	}
}
