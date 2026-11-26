package minecraft

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/rcon"
)

// The owner's decision: the allow list is on "once the server is reachable
// from the internet". In practice the app can't prove reachability before
// friends try, so "auto" means on, from the first start, before any router
// mapping opens the port, except when the app has found this computer
// can't be reached at all: carrier-grade NAT, and nobody has ever come in
// from a public address. If someone then does log in from the internet,
// the Monitor turns the list on at once over RCON. Once on, it stays on.
const (
	// stateAllowListOn is when the app first turned the list on (RFC
	// 3339). From then on "auto" means on, and the app no longer adds
	// players to the list by itself: the list is the owner's.
	stateAllowListOn = "allowListOn"
	// stateCGNAT is "true" when the app's router check (spec 6.5) found
	// carrier-grade NAT, so the internet can't reach this computer. The
	// manager's router code keeps it up to date.
	stateCGNAT = "cgnat"
)

// allowListWanted says whether the list is on for this start.
func allowListWanted(sv game.Saved) bool {
	switch value(sv.Server, "allowList", allowAuto) {
	case allowOn:
		return true
	case allowOff:
		return false
	}
	return sv.State[stateAllowListOn] != "" || sv.State[stateCGNAT] != "true"
}

// prepareAllowList decides the allow list before the server starts. The
// first time it turns on, everyone who has played here before goes on the
// list (from Minecraft's user cache), so turning it on never locks out a
// friend who already played.
func (s *server) prepareAllowList(sv game.Saved) (bool, error) {
	on := allowListWanted(sv)
	if !on || sv.State[stateAllowListOn] != "" {
		return on, nil
	}
	if err := s.seedAllowList(); err != nil {
		return on, fmt.Errorf("Couldn't put the players who already played on Minecraft's allow list: %v", err)
	}
	s.saveState(stateAllowListOn, now().UTC().Format(time.RFC3339))
	return on, nil
}

// seedAllowList adds everyone in Minecraft's user cache to whitelist.json,
// except banned players.
func (s *server) seedAllowList() error {
	path := s.listPath("whitelist.json")
	entries, err := readEntries(path)
	if err != nil {
		return err
	}
	banned, _ := s.ReadList("banned")
	has := func(list []entry, name string) bool {
		for _, e := range list {
			if strings.EqualFold(e.str("name"), name) {
				return true
			}
		}
		return false
	}
	changed := false
	for _, p := range s.cachedPlayers() {
		if has(entries, p.name) || containsFold(banned, p.name) {
			continue
		}
		entries = append(entries, entry{"uuid": p.uuid, "name": p.name})
		changed = true
	}
	if !changed && fileExists(path) {
		return nil
	}
	return writeEntries(path, entries)
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// publicAddr says whether a login came from the internet rather than from
// this computer or the home network. The CGNAT range (100.64.0.0/10) isn't
// the internet either: it is the carrier's own network.
func publicAddr(ip string) bool {
	ip = strings.Trim(ip, "[]")
	if i := strings.IndexByte(ip, '%'); i >= 0 {
		ip = ip[:i] // an IPv6 zone
	}
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	a = a.Unmap()
	cgnat := netip.MustParsePrefix("100.64.0.0/10")
	return a.IsGlobalUnicast() && !a.IsPrivate() && !cgnat.Contains(a)
}

// turnOnAllowList turns the list on while the server runs, after someone
// came in from the internet with it off: everyone on now goes on the list
// first (nobody playing is thrown out), then "whitelist on". It runs on its
// own goroutine, because the Monitor that notices is called with the
// manager's lock held and must not wait for the network.
func (s *server) turnOnAllowList(rec game.Record, playing []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	addr, pw, err := rconAddr(&rec)
	if err == nil {
		var c *rcon.Client
		if c, err = rcon.Dial(ctx, addr, pw); err == nil {
			for _, n := range playing {
				c.Exec(ctx, "whitelist add "+n)
			}
			_, err = c.Exec(ctx, "whitelist on")
			c.Close()
		}
	}
	if err != nil {
		if s.env.Log != nil {
			s.env.Log.Printf("minecraft: couldn't turn the allow list on: %v", err)
		}
		return
	}
	s.saveState(stateAllowListOn, now().UTC().Format(time.RFC3339))
}
