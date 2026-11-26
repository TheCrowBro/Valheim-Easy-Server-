package zomboid

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/rcon"
)

// rePlayers is the first line of RCON "players"'s answer:
// "Players connected (2): ", then one "-name" line per player, each line
// ending in a newline (FACTS.md).
var rePlayers = regexp.MustCompile(`Players connected \((\d+)\):`)

// Probe asks the server over RCON who is on. RCON opens only once the
// server has started, so an answer also means it is up. The RCON port and
// password are the run's own (its record), so this works for a copy of
// the app that took the server over.
func (s *server) Probe(ctx context.Context, rec game.Record) (game.ProbeResult, error) {
	addr, pw, err := rconAddr(rec)
	if err != nil {
		return game.ProbeResult{Count: -1}, err
	}
	reply, err := rcon.Exec(ctx, addr, pw, "players")
	if err != nil {
		return game.ProbeResult{Count: -1}, err
	}
	return parsePlayers(reply)
}

// rconAddr is where the run's RCON listens: on this computer only
// (-Drconlo, FACTS.md), on the port and with the password of its record.
func rconAddr(rec game.Record) (addr, password string, err error) {
	p := rec.Ports[roleRCON]
	if p == 0 {
		return "", "", errors.New(`the run record has no "rcon" port`)
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(p)), rec.Secrets[roleRCON], nil
}

// parsePlayers reads "players"'s answer: the count, and the names on the
// "-name" lines after it. A name may have spaces in it; only the dash
// before it is taken off.
func parsePlayers(reply string) (game.ProbeResult, error) {
	m := rePlayers.FindStringSubmatchIndex(reply)
	if m == nil {
		return game.ProbeResult{Up: true, Count: -1}, fmt.Errorf("Project Zomboid's player list wasn't understood: %q", reply)
	}
	count, _ := strconv.Atoi(reply[m[2]:m[3]])
	names := []string{}
	for line := range strings.SplitSeq(reply[m[1]:], "\n") {
		line = strings.TrimRight(line, "\r")
		if name, ok := strings.CutPrefix(strings.TrimLeft(line, " \t"), "-"); ok && strings.TrimSpace(name) != "" {
			names = append(names, strings.TrimSpace(name))
		}
	}
	return game.ProbeResult{Up: true, Count: count, Names: names}, nil
}
