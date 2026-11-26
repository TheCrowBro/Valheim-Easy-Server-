package minecraft

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/rcon"
)

// reList is RCON "list"'s answer: "There are 1 of a max of 10 players
// online: Alex" (the names are ", "-separated; none leaves it empty).
var reList = regexp.MustCompile(`^There are (\d+) of a max of (\d+) players online:(.*)$`)

// Probe asks the server over RCON who is on. RCON answers once the server
// is ready, so an answer also means it is up.
func (s *server) Probe(ctx context.Context, rec game.Record) (game.ProbeResult, error) {
	addr, pw, err := rconAddr(&rec)
	if err != nil {
		return game.ProbeResult{Count: -1}, err
	}
	reply, err := rcon.Exec(ctx, addr, pw, "list")
	if err != nil {
		return game.ProbeResult{Count: -1}, err
	}
	return parseList(reply)
}

func parseList(reply string) (game.ProbeResult, error) {
	m := reList.FindStringSubmatch(strings.TrimSpace(reply))
	if m == nil {
		return game.ProbeResult{Up: true, Count: -1}, fmt.Errorf("Minecraft's player list wasn't understood: %q", reply)
	}
	var count int
	fmt.Sscan(m[1], &count)
	names := []string{}
	for n := range strings.SplitSeq(m[3], ",") {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	return game.ProbeResult{Up: true, Count: count, Names: names}, nil
}

// Stop asks the server to stop over RCON ("stop" saves every world, then
// exits), then, if that can't be sent or doesn't end it, presses Ctrl+C:
// Java's shutdown hook makes Minecraft save and stop too, though by then
// its log has closed, so nothing in the log says it saved (FACTS.md).
// The RCON port and password come from the run record, so this works for
// a copy of the app that took the server over.
func (s *server) Stop(rec game.Record) []game.StopStep {
	return []game.StopStep{
		{Kind: "rcon", Line: "stop", Wait: 3 * time.Minute},
		{Kind: "interrupt", Wait: 2 * time.Minute, Say: "Asking Minecraft to save and shut down…"},
	}
}

// ExitHint reads the last lines of a run that ended by itself and says
// what to do, in the owner's words. Minecraft exits with code 0 even when
// it couldn't start, so the lines are all there is to go on.
func (s *server) ExitHint(tail []string) string {
	has := func(sub string) bool {
		return slices.ContainsFunc(tail, func(l string) bool { return strings.Contains(strings.ToLower(l), strings.ToLower(sub)) })
	}
	switch {
	case has("FAILED TO BIND TO PORT"):
		return "Another program is using Minecraft's game port. Close it, or pick new ports on Minecraft's page."
	case has("You need to agree to the EULA"):
		return "Minecraft needs you to agree to its EULA first: tick \"I agree\" on Minecraft's Settings tab."
	case has("UnsupportedClassVersionError"):
		return "This Minecraft needs a newer Java than the one installed. Click Repair to install the right one."
	case has("Failed to load world data. World files may be corrupted"):
		return "The world's files are damaged (level.dat). Restore a backup of it on the Worlds tab."
	case has("java.lang.OutOfMemoryError"):
		return "Minecraft ran out of memory. Give it more under Settings → More settings, or lower the view distance."
	}
	return ""
}

// Join says how friends join: by address, in Minecraft's server list. The
// port is the running server's (a port changed in Settings only counts
// after a restart), else the saved one. The address friends outside type
// leaves the port out when it's Minecraft's own (25565), as they needn't
// type it then (spec 5.2); the home addresses always have it.
//
// The manager calls Join with its lock held, also for every Status, so it
// reads nothing from disk: the version is the running server's, or the
// one the last Build() found.
func (s *server) Join(in game.JoinInput) game.Join {
	port := in.Saved.Ports[roleGame]
	if in.Rec != nil && in.Rec.Port != 0 {
		port = in.Rec.Port
	}
	withPort := func(host string) string {
		if port == 0 {
			return host
		}
		return net.JoinHostPort(host, strconv.Itoa(port))
	}
	// Public is false: Minecraft has no public server list to be shown in
	// (in Valheim, Public means listed in the game's own list).
	j := game.Join{How: "address", Port: port, Public: false, Router: true, LAN: []string{}}
	for _, ip := range in.LAN {
		j.LAN = append(j.LAN, withPort(ip))
	}
	if in.PublicIP != "" {
		j.Address = in.PublicIP
		if port != defaultPort {
			j.Address = withPort(in.PublicIP)
		}
	}
	version := ""
	if in.State != nil && in.State.Version != "" {
		version = in.State.Version
	} else {
		version = s.installedVersion()
	}
	where := j.Address
	if where == "" {
		where = "this computer's internet address"
	}
	j.Steps = []string{"In Minecraft: Multiplayer → Add Server, type the address " + where + "."}
	if version != "" && version != "fake" {
		j.Steps = append(j.Steps, "Use Minecraft "+version+", the server's version.")
		j.Hint = fmt.Sprintf("Minecraft %s: Multiplayer → Add Server → %s", version, where)
	} else {
		j.Steps = append(j.Steps, "Use the same Minecraft version as the server.")
		j.Hint = "Minecraft: Multiplayer → Add Server → " + where
	}
	on := allowListWanted(in.Saved)
	if in.State != nil && in.State.Extra[extraAllowList] != "" {
		on = in.State.Extra[extraAllowList] == allowOn
	}
	if on {
		j.Steps = append(j.Steps, "If it says you're not white-listed, ask the server's owner to add you; they'll see an Allow button.")
	}
	if in.Router[roleGame] == "failed" {
		j.Problem = fmt.Sprintf("Friends outside your home can't reach the server until the router forwards port %d (TCP) to this computer.", port)
	}
	return j
}

// Recover makes a run record for a server found running without its run
// file. The RCON port and password the server is using are in its
// server.properties (the plan wrote them there), so the app can still count
// players and stop it gently.
func (s *server) Recover(pid int, started time.Time) game.Record {
	sv := s.store.Get()
	rec := game.Record{PID: pid, Started: started, Launched: started, World: sv.World, Exe: s.Program(),
		LogPath: newestLog(s.env.Dirs.Logs, s.env.Instance), Ports: map[string]int{}, Secrets: map[string]string{}}
	if port := sv.Ports[roleGame]; port != 0 {
		rec.Port, rec.Ports[roleGame] = port, port
	}
	b, err := os.ReadFile(filepath.Join(s.env.Dirs.Server, "server.properties"))
	if err != nil {
		return rec
	}
	p := parseProperties(string(b))
	num := func(key string) int {
		v, _ := p.Get(key)
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	}
	if rec.Port == 0 {
		if port := num("server-port"); port != 0 {
			rec.Port, rec.Ports[roleGame] = port, port
		}
	}
	if on, _ := p.Get("enable-rcon"); on == "true" && num("rcon.port") != 0 {
		rec.Ports[roleRCON] = num("rcon.port")
		rec.Secrets[roleRCON], _ = p.Get("rcon.password")
	}
	rec.Server, _ = p.Get("motd")
	onOff := allowOff
	if v, _ := p.Get("white-list"); v == "true" {
		onOff = allowOn
	}
	a, _ := json.Marshal(onOff)
	m, _ := json.Marshal(value(sv.Server, "allowList", allowAuto))
	rec.Extra = map[string]json.RawMessage{extraAllowList: a, extraAllowMode: m}
	return rec
}

// newestLog is the newest <instance>-*.log in dir, "" when there's none.
func newestLog(dir, instance string) string {
	matches, _ := filepath.Glob(filepath.Join(dir, instance+"-*.log"))
	var best string
	var bestTime time.Time
	for _, m := range matches {
		if st, err := os.Stat(m); err == nil && (best == "" || st.ModTime().After(bestTime)) {
			best, bestTime = m, st.ModTime()
		}
	}
	return best
}

// memStore keeps the settings in memory when the instance has no Store
// (never in the app, which always gives one; it keeps the adapter usable
// on its own).
type memStore struct {
	mu sync.Mutex
	s  game.Saved
}

func (m *memStore) Get() game.Saved {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.s.Clone()
}

func (m *memStore) Save(change func(*game.Saved) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.s.Clone()
	if err := change(&c); err != nil {
		return err
	}
	m.s = c
	return nil
}
