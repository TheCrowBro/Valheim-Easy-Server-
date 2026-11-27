package satisfactory

import (
	"net"
	"strconv"

	"valheim-easy-server/internal/game"
)

// Join tells friends how to reach the server. A dedicated server has no
// join code: friends add it by address and port in the game's Server
// Manager (the wiki, FACTS.md), confirm its certificate the first time
// (the game asks, because the server made the certificate itself), and
// join. Friends outside the home need the router to forward the game port
// (UDP and TCP) and the reliable messaging port (TCP).
//
// Join reads nothing from disk: the manager calls it with its lock held.
func (s *server) Join(in game.JoinInput) game.Join {
	gamePort, reliablePort := ports(in.Saved)
	j := game.Join{How: "address", Password: in.Saved.Server["password"], Router: true, LAN: []string{}}
	if in.Rec != nil {
		// What the running server really uses, not a change waiting for
		// the next start.
		if p := in.Rec.Ports["game"]; p > 0 {
			gamePort = p
		} else if in.Rec.Port > 0 {
			gamePort = in.Rec.Port
		}
		if p := in.Rec.Ports["reliable"]; p > 0 {
			reliablePort = p
		}
		j.Password = in.Rec.Password
	}
	j.Port = gamePort
	port := strconv.Itoa(gamePort)
	for _, ip := range in.LAN {
		j.LAN = append(j.LAN, net.JoinHostPort(ip, port))
	}
	host := "this computer's address"
	if in.PublicIP != "" {
		j.Address = net.JoinHostPort(in.PublicIP, port)
		host = in.PublicIP
	}
	j.Steps = []string{
		"In Satisfactory, open the Server Manager from the main menu and choose Add Server.",
		"Type " + host + " as the address and " + port + " as the port, then confirm.",
	}
	if fp := fingerprintShown(in.State); fp != "" {
		j.Steps = append(j.Steps, "The game asks whether to trust the server's certificate: check it shows "+fp+", then accept it.")
	} else {
		j.Steps = append(j.Steps, "The game asks whether to trust the server's certificate the first time: accept it.")
	}
	if j.Password != "" {
		j.Steps = append(j.Steps, "Choose the server, then Join Game, and type the password when it asks.")
	} else {
		j.Steps = append(j.Steps, "Choose the server, then Join Game.")
	}
	if in.State != nil && in.State.Version != "" {
		j.Steps = append(j.Steps, "Use Satisfactory "+in.State.Version+": it must be the same version as the server.")
	} else {
		j.Steps = append(j.Steps, "Update Satisfactory first: it must be the same version as the server.")
	}
	j.Hint = "Satisfactory: Server Manager → Add Server → " + host + ", port " + port
	if in.Router["game"] == "failed" || in.Router["reliable"] == "failed" {
		j.Problem = "Friends outside your home can't reach the server until the router forwards port " + port +
			" (UDP and TCP) and port " + strconv.Itoa(reliablePort) + " (TCP) to this computer."
	}
	return j
}

// fingerprintShown is the certificate fingerprint the server wrote in its
// log for this run, which is what the game shows friends to check.
func fingerprintShown(st *game.RunState) string {
	if st == nil {
		return ""
	}
	return st.Extra[extraFingerprint]
}
