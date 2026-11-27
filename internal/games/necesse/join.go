package necesse

import (
	"fmt"
	"strconv"

	"valheim-easy-server/internal/game"
)

// Join tells friends how to reach the server. Necesse has no join codes:
// friends type this computer's address and the port, in the game's
// Multiplayer menu (Join a server, then Direct join, whose boxes are "IP
// address" and "Port number"; FACTS.md). Friends outside the home need the
// router to forward the UDP port.
func (s *server) Join(in game.JoinInput) game.Join {
	p := port(in.Saved)
	if in.Rec != nil && in.Rec.Port > 0 {
		p = in.Rec.Port // what the running server really uses
	}
	j := game.Join{How: "address", Port: p, Password: in.Saved.Server["password"], Router: true, Public: true, LAN: []string{}}
	if in.Rec != nil {
		j.Password = in.Rec.Password
	}
	for _, ip := range in.LAN {
		j.LAN = append(j.LAN, fmt.Sprintf("%s:%d", ip, p))
	}
	ip := in.PublicIP
	if ip != "" {
		j.Address = fmt.Sprintf("%s:%d", ip, p)
	} else {
		ip = "this computer's address"
	}
	j.Steps = []string{
		"In Necesse, choose Multiplayer, then Join a server.",
		fmt.Sprintf("Choose Direct join, type %s as the IP address and %d as the port number.", ip, p),
	}
	if j.Password != "" {
		j.Steps = append(j.Steps, "Type the password when it asks.")
	}
	if in.State != nil && in.State.Version != "" {
		j.Steps = append(j.Steps, "Use Necesse "+in.State.Version+": it must be the same version as the server.")
	}
	j.Hint = "Necesse: Multiplayer → Join a server → Direct join, IP " + ip + ", port " + strconv.Itoa(p)
	if in.Router["game"] == "failed" {
		j.Problem = fmt.Sprintf("Friends outside your home can't reach the server until the router forwards UDP port %d to this computer.", p)
	}
	return j
}
