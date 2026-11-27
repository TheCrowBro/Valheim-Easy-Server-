package palworld

import (
	"net"
	"strconv"

	"valheim-easy-server/internal/game"
)

// Join tells friends how to reach the server. Palworld has no join codes
// for a dedicated server: friends type its address and port in the box at
// the bottom of the server list, under Join Multiplayer Game (the docs,
// FACTS.md). Friends outside the home need the router to forward the UDP
// game port. The server isn't put on the game's community list (that needs
// -publiclobby), so Public is false, and only PC and Mac players can join
// by address.
//
// Join reads nothing from disk: the manager calls it with its lock held.
func (s *server) Join(in game.JoinInput) game.Join {
	p, _ := ports(in.Saved)
	j := game.Join{How: "address", Password: in.Saved.Server["password"], Router: true, LAN: []string{}}
	if in.Rec != nil {
		// What the running server really uses, not a change waiting for
		// the next start.
		if in.Rec.Port > 0 {
			p = in.Rec.Port
		}
		j.Password = in.Rec.Password
	}
	j.Port = p
	port := strconv.Itoa(p)
	for _, ip := range in.LAN {
		j.LAN = append(j.LAN, net.JoinHostPort(ip, port))
	}
	addr := "this computer's address and port " + port
	if in.PublicIP != "" {
		j.Address = net.JoinHostPort(in.PublicIP, port)
		addr = j.Address
	}
	j.Steps = []string{
		"In Palworld, choose Join Multiplayer Game.",
		"Type " + addr + " in the box at the bottom of the server list, then Connect.",
	}
	if j.Password != "" {
		j.Steps = append(j.Steps, "Type the password when it asks.")
	}
	if in.State != nil && in.State.Version != "" {
		j.Steps = append(j.Steps, "Use Palworld "+in.State.Version+": it must be exactly the same version as the server.")
	} else {
		j.Steps = append(j.Steps, "Update Palworld first: it must be exactly the same version as the server.")
	}
	j.Hint = "Palworld: Join Multiplayer Game → type " + addr + " at the bottom"
	if in.Router["game"] == "failed" {
		j.Problem = "Friends outside your home can't reach the server until the router forwards UDP port " + port + " to this computer."
	}
	return j
}
