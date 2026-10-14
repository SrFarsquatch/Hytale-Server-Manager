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
	// settle is how long to keep collecting after the first recognised line,
	// for games that answer with one line per world.
	settle time.Duration
	// parse returns the player names, the server-reported maximum (0 when the
	// game does not say), and whether a complete answer was recognised.
	parse func(lines []LogLine) (players []string, max int, known bool)
}

func playerQueryFor(gameType string) (playerQuery, bool) {
	switch normalizeGameType(gameType) {
	case "hytale":
		return playerQuery{command: "/who", settle: 400 * time.Millisecond, parse: parseHytaleWho}, true
	case "minecraft-java":
		return playerQuery{command: "list", parse: parseMinecraftList}, true
	}
	return playerQuery{}, false
}

// Hytale answers /who with one line per world:
//   GrandMine (1): : Clarkonalark (Clarkonalark)
// that is "<world> (<count>):", a separator, then "Name (username)" entries.
var hytaleWhoWorld = regexp.MustCompile(`^(?:\[[^\]]*\]\s*)*(\S.*?) \((\d+)\):(.*)$`)
var hytaleWhoEntry = regexp.MustCompile(`^(.*?)\s*\(([^()]*)\)$`)

func parseHytaleWho(lines []LogLine) ([]string, int, bool) {
	var players []string
	seen := map[string]bool{}
	worlds, total := 0, 0
	for _, l := range lines {
		line := strings.TrimSpace(stripANSI(l.Line))
		m := hytaleWhoWorld.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		count, err := strconv.Atoi(m[2])
		if err != nil || count < 0 || count > 100000 {
			continue
		}
		worlds++
		total += count
		rest := strings.TrimSpace(m[3])
		for strings.HasPrefix(rest, ":") {
			rest = strings.TrimSpace(strings.TrimPrefix(rest, ":"))
		}
		if rest == "" {
			continue
		}
		for _, entry := range strings.Split(rest, ",") {
			entry = strings.TrimSpace(entry)
			name := entry
			if e := hytaleWhoEntry.FindStringSubmatch(entry); e != nil {
				name = strings.TrimSpace(e[1])
				if name == "" {
					name = strings.TrimSpace(e[2])
				}
			}
			if name != "" && !seen[name] {
				seen[name] = true
				players = append(players, name)
			}
		}
	}
	if worlds == 0 {
		// Not the per-world format; fall back to the generic patterns.
		legacy, known := parseWhoOutput(lines)
		return legacy, 0, known
	}
	if total == 0 {
		return []string{}, 0, true
	}
	if len(players) == 0 {
		return nil, 0, false
	}
	return players, 0, true
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
		if known && q.settle > 0 {
			// More worlds may still be printing; collect them before answering.
			time.Sleep(q.settle)
			q.settle = 0
			continue
		}
		if known || time.Now().After(deadline) {
			return players, max, known, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
}
