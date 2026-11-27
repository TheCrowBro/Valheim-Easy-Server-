package windrose

import (
	"encoding/json"

	"valheim-easy-server/internal/game"
)

// Join tells friends how to reach the server. Windrose players connect by
// the server's invite code (Play, Connect to Server, FACTS.md): Windrose's
// online service finds the server and connects them through NAT
// punch-through, so nobody sets up the router and the server opens no
// port of its own for friends. That needs a router that allows UPnP,
// which the manual router steps mention.
//
// Public is false: Windrose has no public list of servers (in Valheim it
// means the server is in the game's own list), and friends find this one
// only by its invite code.
//
// The invite code is the one the running server printed (RunState.Code,
// from its log), or else the one its settings file had when the app last
// started it, kept in the run record and in the instance's settings. Join
// never reads a file: the manager calls it with its lock held (spec 4.8).
func (s *server) Join(in game.JoinInput) game.Join {
	code := in.Saved.State[stateInvite]
	if in.Rec != nil {
		if c := extraString(*in.Rec, "inviteCode"); c != "" {
			code = c
		}
	}
	if in.State != nil && in.State.Code != "" {
		code = in.State.Code
	}
	password := in.Saved.Server["password"]
	if in.Rec != nil {
		password = in.Rec.Password
	}
	j := game.Join{How: "code", CodeLabel: "Invite code", Code: code, LAN: []string{}, Password: password,
		Public: false, Router: false, Manual: "Your router must allow UPnP for NAT punch-through."}
	if code == "" {
		j.Steps = []string{
			"Start the server once: its invite code shows here, and stays the same after that.",
			"Then, in Windrose, choose Play, then Connect to Server, and paste the invite code.",
		}
		j.Hint = "Windrose: Play → Connect to Server with the server's invite code (start the server to get it)"
	} else {
		j.Steps = []string{
			"In Windrose, choose Play, then Connect to Server.",
			"Paste the invite code " + code + " and connect.",
		}
		j.Hint = "Windrose: Play → Connect to Server → invite code " + code
	}
	if password != "" {
		j.Steps = append(j.Steps, "Type the server's password when Windrose asks for it.")
	}
	if in.State != nil && in.State.Version != "" {
		j.Steps = append(j.Steps, "Use Windrose "+in.State.Version+": it must be the same version as the server.")
	}
	switch {
	case in.Saved.State[stateDirect] == "true":
		j.Problem = "Direct connection is turned on in the server's ServerDescription.json (UseDirectConnection), so the invite code doesn't work. Turn it off there while the server is stopped."
	case in.State != nil && in.State.Extra["backend"] == "failed":
		j.Problem = "The server can't reach Windrose's online service, so friends can't find it by its invite code. Check this computer's internet connection."
	case in.State != nil && in.State.Ready && code == "":
		j.Problem = "The server is running but hasn't said its invite code. Its log on the Log page may say why."
	}
	return j
}

// extraString is one of the adapter's own text fields in a run record.
func extraString(rec game.Record, key string) string {
	var v string
	if raw, ok := rec.Extra[key]; ok {
		json.Unmarshal(raw, &v)
	}
	return v
}
