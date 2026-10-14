package main

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// playerQuery is how a game is asked for its online players: a console
// command and a parser for the lines the server prints in response.
type playerQuery struct {
	command string
	// parse returns the player names, the server-reported maximum (0 when the
	// game does not say), and whether a complete answer was recognised.
	parse func(lines []LogLine) (players []string, max int, known bool)
}

func playerQueryFor(gameType string) (playerQuery, bool) {
	switch normalizeGameType(gameType) {
	case "hytale":
		return playerQuery{command: "/who", parse: func(lines []LogLine) ([]string, int, bool) {
			players, known := parseWhoOutput(lines)
			return players, 0, known
		}}, true
	case "minecraft-java":
		return playerQuery{command: "list", parse: parseMinecraftList}, true
	}
	return playerQuery{}, false
}

var (
	// 1.13+: "There are 2 of a max of 20 players online: Alex, Steve"
	minecraftListModern = regexp.MustCompile(`There are (\d+) of a max(?: of)? (\d+) players online:?\s*(.*)$`)
	// Older releases: "There are 2/20 players online:" with names on the next line.
	minecraftListLegacy = regexp.MustCompile(`There are (\d+)/(\d+) players online:?\s*(.*)$`)
	// Strips the "[12:00:00] [Server thread/INFO]: " prefix from a log line.
	minecraftLogPrefix = regexp.MustCompile(`^(?:\[[^\]]*\]\s*)*:?\s*`)
)

func parseMinecraftList(lines []LogLine) ([]string, int, bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(stripANSI(lines[i].Line))
		match := minecraftListModern.FindStringSubmatch(line)
		if match == nil {
			match = minecraftListLegacy.FindStringSubmatch(line)
		}
		if match == nil {
			continue
		}
		count, err1 := strconv.Atoi(match[1])
		max, err2 := strconv.Atoi(match[2])
		if err1 != nil || err2 != nil || count < 0 || count > 100000 {
			continue
		}
		if count == 0 {
			return []string{}, max, true
		}
		names := parsePlayerNames(match[3])
		if len(names) == 0 && i+1 < len(lines) {
			// Legacy format: the names follow on the next line.
			names = parsePlayerNames(minecraftLogPrefix.ReplaceAllString(strings.TrimSpace(stripANSI(lines[i+1].Line)), ""))
		}
		if len(names) == 0 {
			return nil, max, false
		}
		return names, max, true
	}
	return nil, 0, false
}

// queryPlayers sends the game's player command and waits up to timeout for a
// recognisable answer, returning as soon as one arrives.
func (m *ServerManager) queryPlayers(q playerQuery, timeout time.Duration) ([]string, int, bool, error) {
	sentAt := time.Now()
	if err := m.SendCommand(q.command); err != nil {
		return nil, 0, false, err
	}
	deadline := sentAt.Add(timeout)
	for {
		var lines []LogLine
		for _, line := range m.logs.Snapshot() {
			if !line.Time.Before(sentAt) && line.Source != "console" {
				lines = append(lines, line)
			}
		}
		players, max, known := q.parse(lines)
		if known || time.Now().After(deadline) {
			return players, max, known, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
}
