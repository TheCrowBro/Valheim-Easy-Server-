package terraria

import (
	"fmt"
	"net"
	"strconv"

	"valheim-easy-server/internal/game"
)

// Join tells friends how to join. Terraria asks for the address and the
// port separately (its own invite text: "The server address is …. The
// server port is …."), so Address is the bare IP and the port is its own
// step. Friends need exactly the server's version.
func (s *server) Join(in game.JoinInput) game.Join {
	p := port(in.Saved)
	pw := in.Saved.Server["password"]
	if in.Rec != nil {
		if in.Rec.Port > 0 {
			p = in.Rec.Port
		}
		pw = in.Rec.Password
	}
	j := game.Join{How: "address", Port: p, Password: pw, Public: true, Router: true, LAN: []string{}}
	for _, ip := range in.LAN {
		j.LAN = append(j.LAN, net.JoinHostPort(ip, strconv.Itoa(p)))
	}
	j.Address = in.PublicIP

	version := ""
	if in.State != nil {
		version = in.State.Version
	}
	if version == "" {
		version = s.knownVersion()
	}

	where := "this computer's address"
	if j.Address != "" {
		where = j.Address
	}
	j.Steps = []string{
		fmt.Sprintf("In Terraria, open Multiplayer and add a server: the address is %s and the port is %d.", where, p),
	}
	if pw != "" {
		j.Steps = append(j.Steps, "Type the server's password when Terraria asks for it.")
	}
	if version != "" {
		j.Steps = append(j.Steps, fmt.Sprintf("Use Terraria %s: it must be the same version as the server.", version))
	} else {
		j.Steps = append(j.Steps, "Use the newest Terraria: it must be the same version as the server.")
	}
	j.Hint = fmt.Sprintf("Add the server %s, port %d, in Terraria's Multiplayer menu.", where, p)
	if in.Router["game"] == "failed" {
		j.Problem = fmt.Sprintf("Friends outside your home can't reach the server until your router forwards port %d (TCP and UDP) to this computer.", p)
	}
	return j
}
