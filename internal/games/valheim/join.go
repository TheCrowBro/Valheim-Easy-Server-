package valheim

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/mods"
)

const (
	hintCode    = "In Valheim, go to Join Game and join by code."
	hintAddress = "In Valheim, go to Join Game and add the server by its address."
)

// Join says how friends join: by the crossplay join code, or without
// crossplay by the server's address. While the server runs, friends need
// the name, port, password and crossplay it started with, not changes
// waiting for a restart (a record from an older build, without a port,
// means the settings' ones).
func (s *Server) Join(in game.JoinInput) game.Join {
	v := in.Saved.Server
	crossplay, port, password := v.Bool("crossplay"), in.Saved.Ports["game"], v["password"]
	if port == 0 {
		port = v.Int("port", 0)
	}
	if in.Rec != nil {
		crossplay = !NoCrossplay(*in.Rec)
		if in.Rec.Port != 0 {
			port, password = in.Rec.Port, in.Rec.Password
		}
	}
	j := game.Join{How: "address", Port: port, Password: password, Public: v.Bool("public"), Crossplay: crossplay,
		Steps: []string{hintAddress}, Hint: hintAddress, Router: !crossplay}
	if crossplay {
		j.How, j.CodeLabel, j.Steps, j.Hint = "code", "Join code", []string{hintCode}, hintCode
	}
	for _, ip := range in.LAN {
		j.LAN = append(j.LAN, net.JoinHostPort(ip, strconv.Itoa(port)))
	}
	if st := in.State; st != nil {
		j.Code = st.Code
		j.Address = st.Extra["publicAddr"]
		if st.Ready && crossplay && st.Code == "" {
			switch {
			case st.Extra["crossplayFailed"] == "true":
				j.Problem = "crossplay"
			case st.Extra["codeLate"] == "true":
				j.Problem = "late"
			}
		}
	}
	if j.Address == "" && in.PublicIP != "" {
		j.Address = net.JoinHostPort(in.PublicIP, strconv.Itoa(port))
	}
	return j
}

// Online is the Discord post when the server is ready: today's text, with
// the mods players need and the mods code.
func (s *Server) Online(in game.OnlineInput) string {
	msg := OnlineMessage{Server: in.Server, World: in.World, Code: in.Join.Code, Password: in.Password, Reminder: in.Reminder}
	if msg.Code == "" {
		msg.Address = in.Join.Address
	}
	for _, md := range s.PlayerMods(in.World, in.Rec) {
		if md.Players == mods.PlayersOnly {
			msg.Optional = append(msg.Optional, mods.Info{Mod: md.Mod})
		} else {
			msg.Mods = append(msg.Mods, md)
		}
	}
	if in.Rec != nil {
		msg.ModsCode = s.ModsCode(in.Rec)
	}
	return msg.Text()
}

// OnlineMessage is what the channel sees when the server is ready.
type OnlineMessage struct {
	Server, World string
	Code          string // crossplay join code
	Address       string // internet address, when there's no join code
	Password      string // empty to leave it out
	Mods          []mods.Info
	Optional      []mods.Info // just for players' own games
	ModsCode      string      // imports all the mods in r2modman or Gale
	Reminder      []string    // what players need to do first, such as "update Valheim to 1.0.21"
}

// Text is the post.
func (o OnlineMessage) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "🟢 **%s** is online\n", escape(o.Server))
	fmt.Fprintf(&b, "World: %s\n", escape(o.World))
	switch {
	case o.Code != "":
		fmt.Fprintf(&b, "Join code: **%s**\n", o.Code)
	case o.Address != "":
		fmt.Fprintf(&b, "Address: **%s**\n", o.Address)
	}
	if o.Password != "" {
		fmt.Fprintf(&b, "Password: ||%s||\n", escape(o.Password))
	}
	hint := hintCode
	if o.Code == "" {
		hint = hintAddress
	}
	if len(o.Mods) > 0 {
		list := make([]string, 0, len(o.Mods))
		for _, md := range o.Mods {
			item := fmt.Sprintf("[%s](%s)", md.Name, md.URL)
			if md.Version != "" {
				item += " " + md.Version
			}
			list = append(list, item)
		}
		fmt.Fprintf(&b, "Mods: %s\n", strings.Join(list, ", "))
		them := "the mod"
		if len(o.Mods) > 1 {
			them = "the mods"
		}
		if o.ModsCode != "" {
			hint += fmt.Sprintf(" Install %s first: in the r2modman or Gale mod manager, import the mods code as a new profile.", them)
		} else {
			hint += fmt.Sprintf(" Install %s first with the Gale or r2modman mod manager.", them)
		}
	} else if o.ModsCode != "" {
		hint += " For the optional mods, import the mods code as a new profile in r2modman or Gale."
	}
	if len(o.Optional) > 0 {
		list := make([]string, 0, len(o.Optional))
		for _, md := range o.Optional {
			list = append(list, fmt.Sprintf("[%s](%s)", md.Name, md.URL))
		}
		fmt.Fprintf(&b, "Optional, just for your own game: %s\n", strings.Join(list, ", "))
	}
	if o.ModsCode != "" {
		fmt.Fprintf(&b, "📦 Mods code: `%s`\n", o.ModsCode)
	}
	if len(o.Reminder) > 0 {
		fmt.Fprintf(&b, "📢 **Before you join:** %s.\n", strings.Join(o.Reminder, ", "))
	}
	b.WriteString("-# " + hint)
	return b.String()
}

// escape stops Discord from reading s as formatting, so a name with
// underscores or stars shows as typed. It is internal/discord's Escape,
// which a game's adapter may not import (spec 2.1); a test keeps the two
// the same.
func escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune("\\*_~`|[<", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
