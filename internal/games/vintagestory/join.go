package vintagestory

import (
	"net"
	"slices"
	"strconv"
	"strings"

	"valheim-easy-server/internal/game"
)

// Join tells friends how to reach the server. Vintage Story has no join
// codes: friends add the server by its address in the game's Multiplayer
// menu ("Add New Server", whose boxes are "Host / IP Address" and "Server
// Password (if any)"; FACTS.md). Friends outside the home need the router to
// forward the port.
//
// Join reads no files: the manager calls it with its lock held (spec 4.8).
func (s *server) Join(in game.JoinInput) game.Join {
	p := port(in.Saved)
	pw := in.Saved.Server["password"]
	if in.Rec != nil {
		// What the running server really uses, not a change waiting for a
		// restart.
		if in.Rec.Port > 0 {
			p = in.Rec.Port
		}
		pw = in.Rec.Password
	}
	j := game.Join{How: "address", Port: p, Password: pw, Router: true, LAN: []string{}}
	for _, ip := range in.LAN {
		j.LAN = append(j.LAN, net.JoinHostPort(ip, strconv.Itoa(p)))
	}
	host := "this computer's address"
	if in.PublicIP != "" {
		// The game adds the default port by itself, so the address is
		// shortest without it.
		j.Address = in.PublicIP
		if p != defaultPort {
			j.Address = net.JoinHostPort(in.PublicIP, strconv.Itoa(p))
		} else if strings.Contains(in.PublicIP, ":") {
			j.Address = "[" + in.PublicIP + "]"
		}
		host = j.Address
	}
	j.Steps = []string{
		"In Vintage Story, choose Multiplayer, then Add New Server.",
		"Type " + host + " as the Host / IP Address, and the server's password as the Server Password.",
	}
	version := ""
	if in.State != nil {
		version = in.State.Version
	}
	if version == "" {
		version = s.installedVersion()
	}
	if version != "" {
		j.Steps = append(j.Steps, "Use Vintage Story "+version+": it must be the same version as the server.")
	} else {
		j.Steps = append(j.Steps, "Use the same version of Vintage Story as the server.")
	}
	j.Hint = "Vintage Story: Multiplayer → Add New Server, host " + host
	if in.Router["game"] == "failed" {
		j.Problem = "Friends outside your home can't reach the server until the router forwards port " + strconv.Itoa(p) + " (TCP and UDP) to this computer."
	}
	return j
}

// ExitHint explains a server that ended by itself, from the end of its log.
// The server exits with code 0 even when it couldn't start (FACTS.md), so
// the lines are all there is to go on.
func (s *server) ExitHint(tail []string) string {
	lines := make([]string, 0, len(tail))
	for _, l := range tail {
		t, _, _ := splitLine(l)
		lines = append(lines, t)
	}
	has := func(subs ...string) bool {
		return slices.ContainsFunc(lines, func(l string) bool {
			for _, sub := range subs {
				if strings.Contains(strings.ToLower(l), strings.ToLower(sub)) {
					return true
				}
			}
			return false
		})
	}
	switch {
	case has("Address already in use", "Only one usage of each socket address"):
		return "Another program is using Vintage Story's port, so the server couldn't open it. Close that program, or choose new ports for Vintage Story."
	case has("Failed opening savegame"):
		return "The world couldn't be opened: its file may be damaged. Restore one of its backups on the Worlds page."
	case has("Failed to read serverconfig.json", "no such group exists"):
		return "The server couldn't use its settings file. Start it again: the app sets a broken settings file aside and makes a new one."
	case has("still contains old files from a previous game version"):
		return "Some of the server's files are left over from an older version. Click Repair to install it again."
	case has("A full harddisk can heavily corrupt a savegame", "Out of disk space"):
		return "The disk is nearly full, so the server stopped to keep the world safe. Free some space, then start it again."
	case has("OutOfMemoryException"):
		return "The Vintage Story server ran out of memory. Close other programs, or stop another game, then start it again."
	case has("You must install or update .NET", "hostfxr", "libhostpolicy"):
		return "The server's .NET runtime is missing or damaged. Click Repair to install it again."
	case has("does not exist or is not a managed .dll", "Could not load file or assembly", "FileNotFoundException"):
		return "Some of the Vintage Story server's files are missing or damaged. Click Repair to download them again."
	case has("Caught unhandled exception", "Critical error occurred"):
		return "The Vintage Story server crashed. Start it again; if it keeps crashing, restore a backup of the world on the Worlds page. Its crash report is server-crash.log in the Logs folder of its saves."
	}
	return ""
}
