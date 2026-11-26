package corekeeper

import (
	"valheim-easy-server/internal/game"
)

// Join tells friends how to reach the server. Core Keeper players on Steam
// join by Game ID (the game's Join Game menu has a box for it; FACTS.md):
// Steam's relay carries the connection, so nobody sets up the router and
// the server opens no port for friends.
//
// The Game ID is the one the running server said (RunState.Code, from its
// log), or else the one the app gives it at every start, kept in the
// instance's settings. Join never reads a file: the manager calls it with
// its lock held (spec 4.8).
func (s *server) Join(in game.JoinInput) game.Join {
	code := in.Saved.State[gameIDKey]
	if in.State != nil && in.State.Code != "" {
		code = in.State.Code
	}
	j := game.Join{How: "id", CodeLabel: "Game ID", Code: code, LAN: []string{}, Router: false}
	if code == "" {
		j.Steps = []string{
			"Start the server once: its Game ID shows here, and stays the same after that.",
			"Then, in Core Keeper, choose Join Game and type the Game ID.",
		}
		j.Hint = "Core Keeper: Join Game with the server's Game ID (start the server to get it)"
	} else {
		j.Steps = []string{
			"In Core Keeper, choose Join Game.",
			"Type (or paste) the Game ID " + code + " in the box, then click Join.",
		}
		j.Hint = "Core Keeper: Join Game → Game ID " + code
	}
	if in.State != nil && in.State.Version != "" {
		j.Steps = append(j.Steps, "Use Core Keeper "+in.State.Version+": it must be the same version as the server.")
	}
	if in.State != nil && in.State.Ready && in.State.Code == "" && code == "" {
		j.Problem = "The server is running but hasn't said its Game ID. Its log on the Log page may say why."
	}
	return j
}
