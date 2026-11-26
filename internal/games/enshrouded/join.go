package enshrouded

import (
	"fmt"
	"net"
	"strconv"

	"valheim-easy-server/internal/game"
)

// Join tells friends how to reach the server (FACTS.md, "Joining").
//
// Enshrouded lists dedicated servers in its own Join menu once "Show
// dedicated servers" is ticked in its filter, so friends find the server
// by its name, or search the list by address (press T there and type
// address:port). Steam's own server favourites are Keen's way for a server
// the list doesn't show. The address always has the port: the game asks
// for the query port, whatever it is.
//
// Join never reads a file: the manager calls it with its lock held (spec
// 4.8). A running server's name, port and password are the ones it
// started with (its run record), not ones changed since.
func (s *server) Join(in game.JoinInput) game.Join {
	sv := in.Saved
	name, port, password := s.value(sv, "serverName"), s.port(sv), s.value(sv, "password")
	if in.Rec != nil {
		if in.Rec.Server != "" {
			name = in.Rec.Server
		}
		if in.Rec.Port != 0 {
			port = in.Rec.Port
		}
		if in.Rec.Password != "" {
			password = in.Rec.Password
		}
	}
	withPort := func(host string) string { return net.JoinHostPort(host, strconv.Itoa(port)) }
	// Public: Enshrouded's own list shows every dedicated server, so
	// anyone can see this one (the password keeps them out).
	j := game.Join{How: "address", Port: port, Password: password, Public: true, Router: true, LAN: []string{}}
	for _, ip := range in.LAN {
		j.LAN = append(j.LAN, withPort(ip))
	}
	where := "this computer's internet address and port " + strconv.Itoa(port)
	if in.PublicIP != "" {
		j.Address = withPort(in.PublicIP)
		where = j.Address
	}
	j.Steps = []string{
		"In Enshrouded, choose Play, then Join, and tick Show dedicated servers in the filter.",
		fmt.Sprintf("Find %q in the list, or press T and search for %s. Join it and type the password.", name, where),
		"If it isn't listed, add " + where + " in Steam (View → Game Servers → Favorites → +), then look in Enshrouded's list again.",
	}
	j.Hint = fmt.Sprintf("Enshrouded: Join → Show dedicated servers → %q (%s)", name, where)
	if in.Router[roleQuery] == "failed" {
		j.Problem = fmt.Sprintf("Friends outside your home can't reach the server until the router forwards port %d (UDP) to this computer.", port)
	}
	return j
}
