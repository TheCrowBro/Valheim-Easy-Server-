package zomboid

import (
	"fmt"
	"net"
	"strconv"

	"valheim-easy-server/internal/game"
)

// Join tells friends how to reach the server. Project Zomboid has no join
// codes: friends add the server in the game's Join screen with this
// computer's address, the port and the server's password, and a username
// and password of their own, which make their account on the server the
// first time (the app sets Open=true, FACTS.md). Friends outside the home
// need the router to forward both UDP ports: the game port, and the direct
// connection port beside it (without it the game falls back to Steam's
// slower relay, FACTS.md).
//
// The manager calls Join with its lock held, also for every Status, so it
// reads nothing from disk: the ports are the running server's (a port
// changed in Settings only counts after a restart), else the saved ones.
func (s *server) Join(in game.JoinInput) game.Join {
	p, direct := gamePort(in.Saved), directPort(in.Saved)
	pw := in.Saved.Server["password"]
	if in.Rec != nil {
		if in.Rec.Port > 0 {
			p = in.Rec.Port
		}
		if d := in.Rec.Ports[roleDirect]; d > 0 {
			direct = d
		}
		pw = in.Rec.Password
	}
	// The game asks for the IP and the port in separate boxes, so the
	// address is the IP alone. Public is false: the app keeps the server
	// out of the game's public list (Public=false).
	j := game.Join{How: "address", Port: p, Password: pw, Router: true, Public: false, LAN: []string{}}
	for _, ip := range in.LAN {
		j.LAN = append(j.LAN, net.JoinHostPort(ip, strconv.Itoa(p)))
	}
	ip := in.PublicIP
	if ip != "" {
		j.Address = ip
	} else {
		ip = "this computer's internet address"
	}
	j.Steps = []string{
		"In Project Zomboid, choose Join and add a server.",
		fmt.Sprintf("Type %s as its IP and %d as its port.", ip, p),
	}
	if pw != "" {
		// The password itself is in Join.Password, which the panel and
		// Discord show or leave out as the owner chose.
		j.Steps = append(j.Steps, "Type the server's password too.")
	}
	j.Steps = append(j.Steps, "Make up a username and a password of your own: the server makes your account the first time you join, so use the same ones every time after that.")
	version := "the same version as the server (Build 42)"
	if in.State != nil && in.State.Version != "" {
		version = in.State.Version + ": it must be the same version as the server"
	}
	j.Steps = append(j.Steps, "Use Project Zomboid "+version+".")
	j.Hint = fmt.Sprintf("Project Zomboid: Join → add a server, IP %s, port %d", ip, p)
	if in.Router[roleGame] == "failed" || in.Router[roleDirect] == "failed" {
		j.Problem = fmt.Sprintf("Friends outside your home can't reach the server until the router forwards UDP ports %d and %d to this computer.", p, direct)
	}
	return j
}
