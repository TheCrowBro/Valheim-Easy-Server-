package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"valheim-easy-server/internal/a2s"
)

// The enshrouded personality stands in for the Enshrouded dedicated server
// (internal/games/enshrouded), as the real 1.0 server behaves as far as the
// app can see it (that package's FACTS.md and testdata hold real logs). The
// app starts it as the server's own program, enshrouded_server.exe, with no
// arguments; Match knows it by that name, which no other game's server has.
//
// Like the real server it:
//   - reads enshrouded_server.json beside its program at start, and refuses
//     user groups the real one refuses, printing why on standard output
//     (the real one does so before its log is open) and exiting;
//   - writes its log to logDirectory/enshrouded_server.log, after moving
//     the last one into logDirectory/backup, every line led by its level
//     and the time since it started ("[I 00:01:38,732] ");
//   - keeps the world in saveDirectory, written when it autosaves (every 5
//     minutes of game time, silently, as 1.0 does) and when it stops;
//   - answers Steam's A2S_INFO query on its one UDP port, queryPort, with
//     the number of players on and slotCount;
//   - says "[Session] 'HostOnline' (up)!" once friends can join;
//   - logs each scripted player's connection, login (with the rights their
//     group gives) and leave in the real server's words; "deny" is a
//     player turned away because the server is full;
//   - on Ctrl+C (SIGINT; on Windows the app's Ctrl+C, since it is a
//     console program like the real one) shuts down with the real one's
//     lines, saves the world, writes its settings file back with the keys
//     the real one adds (tags, gameSettings, bannedAccounts) and exits.
//
// It has no console commands, as the real server has none.

func init() {
	register(&Personality{
		ID:      "enshrouded",
		Version: "1267041",
		MemMB:   64,
		Match:   func(c Call) bool { return c.Program == "enshrouded_server" },
		Run:     runEnshrouded,
	})
}

// enGroup is one of the settings file's user groups.
type enGroup struct {
	Name                 string `json:"name"`
	Password             string `json:"password"`
	CanKickBan           bool   `json:"canKickBan"`
	CanAccessInventories bool   `json:"canAccessInventories"`
	CanEditWorld         bool   `json:"canEditWorld"`
	CanEditBase          bool   `json:"canEditBase"`
	CanExtendBase        bool   `json:"canExtendBase"`
	ReservedSlots        int    `json:"reservedSlots"`
}

// enConfig is what the stand-in reads from enshrouded_server.json.
type enConfig struct {
	Name          string    `json:"name"`
	Password      string    `json:"password"`
	SaveDirectory string    `json:"saveDirectory"`
	LogDirectory  string    `json:"logDirectory"`
	IP            string    `json:"ip"`
	QueryPort     int       `json:"queryPort"`
	SlotCount     int       `json:"slotCount"`
	Preset        string    `json:"gameSettingsPreset"`
	UserGroups    []enGroup `json:"userGroups"`
}

// enshrouded is one run of the stand-in.
type enshrouded struct {
	s        *Server
	start    time.Time
	cfg      enConfig
	cfgPath  string
	raw      map[string]json.RawMessage // the whole settings file, to write back
	world    string                     // the world's file
	saves    int
	stopping bool
	peers    map[string]string // player → peer handle
	nextPeer int
}

func runEnshrouded(s *Server) {
	e := &enshrouded{s: s, start: time.Now(), peers: map[string]string{}}
	e.cfgPath = filepath.Join(s.call.Dir, "enshrouded_server.json")
	if err := e.readConfig(); err != "" {
		// The real server says what is wrong with its settings on
		// standard output only, before its log is open.
		fmt.Println("Internal Error: " + err)
		s.Exit(1)
	}
	logDir := e.path(e.cfg.LogDirectory, "./logs")
	logPath := filepath.Join(logDir, "enshrouded_server.log")
	backedUp := ""
	if st, err := os.Stat(logPath); err == nil {
		os.MkdirAll(filepath.Join(logDir, "backup"), 0o755)
		backedUp = filepath.Join(logDir, "backup", "enshrouded_server_"+st.ModTime().Format("2006-01-02_15-04-05")+".log")
		os.Rename(logPath, backedUp)
	}
	s.LogTo(logPath)
	e.log("[enshrouded] Create logfile")
	if backedUp != "" {
		e.log(fmt.Sprintf("[holistic] Backed up log to '%s'.", backedUp))
	}
	e.log("enshrouded_server(detached HEAD) - version 3f6b2a91c04d5e7f8a9b0c1d2e3f4a5b6c7d8e9f (master)")
	e.log("CPUs:")
	e.log(" Idx   Id  Group  LogIdx  CoreIdx  LLCIdx  NumaIdx  EffClass")
	e.log("  0   256    0       0        0       3       0        0")
	e.log("[app] start creation step Init")
	e.log("[app] start creation step Init_Steamworks")
	e.log("[app] start creation step Init_Version")
	e.log("Game Version (SVN): " + s.Version())
	e.log("[os] Started thread 0x00000000117708f8 (P2pReceiveThread) with stack range (0x000000001d380000-0x000000001d480000 size:1,048,576)")
	e.log("[Session] started transition from 'Lobby' to 'Host_Online' (current='<invalid>')!")
	e.log("[Session] 'PrepareGame' (up)!")

	// The one port: Steam's server queries, and the game.
	err := s.A2S(e.cfg.QueryPort, true, func() a2s.Info {
		return a2s.Info{Name: e.cfg.Name, Map: "Embervale", Folder: "enshrouded", Game: "Enshrouded",
			AppID: 1203620, Players: len(s.Players()), MaxPlayers: e.cfg.SlotCount, Version: s.Version()}
	})
	if err != nil {
		e.log(fmt.Sprintf("[online] Couldn't bind dedicated server socket. error='%v'", err))
		e.log("[enshrouded] Close Log file")
		s.Exit(1)
	}
	s.Sleep(2 * time.Second)
	e.log("[online] Server connected to Steam successfully")
	e.log("[online] Server is not VAC Secure!")
	e.log("[online] Public ipv4: 203.0.113.7")
	e.log("[OnlineProviderSteam] 'Initialize' (up)!")
	e.log("[OnlineProviderSteam] finished transition from 'Uninitialized' to 'InGame' (current='InGame')!")

	e.world = filepath.Join(e.path(e.cfg.SaveDirectory, "./savegame"), "world0.ksd")
	if b, err := os.ReadFile(e.world); err == nil {
		e.saves = bytes.Count(b, []byte("\n"))
	} else {
		e.saves = -1 // a new world: made in memory now, written at its first save
	}

	s.OnStep(e.step)
	s.OnStop(e.stop)
	s.Every(5*time.Minute, func() {
		if !e.stopping {
			e.save()
		}
	})
	// The session dump the real server writes every 30 seconds (real
	// seconds: the clock it counts by isn't the game's).
	s.Every(time.Duration(30*s.cfg.Speed)*time.Second, func() {
		e.log("-------------- Session ----------------")
		e.log("Machines:")
		e.log("  m#0(128): up 0 (0), down 0 (0), remote 0 (0), limit 256, lost 0, ping 0 ms, EstablishingBaseline")
		e.log("---------------------------------------")
	})
	s.Ready(e.stamp("[Session] 'HostOnline' (up)!"),
		e.stamp("[Session] finished transition from 'Lobby' to 'Host_Online' (current='Host_Online')!"))
	if e.saves >= 0 {
		e.log(fmt.Sprintf("[server] Savegame deserialization took %.2f ms", 120+float64(e.saves)))
	}
}

// path is a folder from the settings file: absolute, or relative to the
// program's folder.
func (e *enshrouded) path(p, def string) string {
	if p == "" {
		p = def
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(e.s.call.Dir, p)
	}
	return p
}

// readConfig reads and checks the settings file the way the real server
// does, returning what is wrong ("" when nothing is).
func (e *enshrouded) readConfig() string {
	b, err := os.ReadFile(e.cfgPath)
	if os.IsNotExist(err) {
		// The real server writes a new file with four groups and random
		// passwords; the app always writes one first.
		e.cfg = enConfig{Name: "Enshrouded Server", QueryPort: 15637, SlotCount: 16,
			UserGroups: []enGroup{{Name: "Friend", Password: "Friend" + enRandom(8), CanAccessInventories: true, CanEditWorld: true, CanEditBase: true}}}
		e.raw = map[string]json.RawMessage{}
		return ""
	}
	if err != nil {
		return "Failed to parse Config"
	}
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	if json.Unmarshal(b, &e.raw) != nil || json.Unmarshal(b, &e.cfg) != nil {
		return "Failed to parse Config"
	}
	c := &e.cfg
	if c.QueryPort == 0 {
		c.QueryPort = 15637
	}
	c.SlotCount = min(max(c.SlotCount, 1), 16)
	names, passwords := map[string]bool{}, map[string]bool{}
	open, reserved := 0, 0
	for _, g := range c.UserGroups {
		switch {
		case g.Name == "":
			return "user groups require a name"
		case names[g.Name]:
			return "user groups names must be unique"
		case g.Password != "" && passwords[g.Password]:
			return "user groups passwords must be unique"
		case g.CanEditBase && !g.CanEditWorld:
			return "Users that are allowed to Edit the Base But not the World ?"
		}
		names[g.Name], passwords[g.Password] = true, true
		if g.Password == "" {
			open++
		}
		reserved += g.ReservedSlots
	}
	switch {
	case open > 1:
		return "Only one user group can be without password"
	case reserved > c.SlotCount:
		return "There are more reserved slots than overall slots"
	}
	return ""
}

// stamp is a line as the real log writes it: the level, then the time
// since the server started.
func (e *enshrouded) stamp(line string) string {
	d := time.Since(e.start)
	return fmt.Sprintf("[I %02d:%02d:%02d,%03d] %s", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60, d.Milliseconds()%1000, line)
}

func (e *enshrouded) log(line string) { e.s.Log(e.stamp(line)) }

// enAccount is a made-up Steam account for a scripted player, the same
// every time.
func enAccount(name string) string {
	h := fnv.New32a()
	h.Write([]byte(name))
	return fmt.Sprintf("765611980%08d", h.Sum32()%100000000)
}

func enRandom(n int) string {
	const chars = "0123456789AabBcCdDeEfFgGhHiIjJkKlLmMnNoOpPqQrRsStTuUvVwWxXyYzZ"
	b := make([]byte, n)
	for i := range b {
		b[i] = chars[rand.IntN(len(chars))]
	}
	return string(b)
}

func (e *enshrouded) step(st Step) bool {
	if e.stopping {
		return true
	}
	switch st.Verb {
	case "join":
		if len(e.s.Players()) >= e.cfg.SlotCount {
			e.log(fmt.Sprintf("[online] Session rejected with peer because it is full (%s)", enAccount(st.Name)))
			return true
		}
		if !e.s.Join(st.Name) {
			return true
		}
		e.nextPeer++
		handle := fmt.Sprintf("0(%d)", e.nextPeer)
		e.peers[st.Name] = handle
		e.log(fmt.Sprintf("[online] Added peer %s (steamid:%s)", handle, enAccount(st.Name)))
		e.log(fmt.Sprintf("[server] Machine '%d': Player '0(0)' logged in", e.nextPeer))
		e.log(fmt.Sprintf("[server] Player '%s' logged in with Permissions:", st.Name))
		if len(e.cfg.UserGroups) > 0 {
			g := e.cfg.UserGroups[0]
			for _, p := range []struct {
				on   bool
				name string
			}{{g.CanKickBan, "CanKickBan"}, {g.CanAccessInventories, "CanAccessInventories"}, {g.CanEditWorld, "CanEditWorld"},
				{g.CanEditBase, "CanEditBase"}, {g.CanExtendBase, "CanExtendBase"}, {true, "CanReceiveEXP"}} {
				if p.on {
					e.log("\t - " + p.name)
				}
			}
		}
	case "leave":
		if !e.s.Leave(st.Name) {
			return true
		}
		e.log(fmt.Sprintf("[server] Remove Entity for Player '%s'", st.Name))
		e.log(fmt.Sprintf("[server] Remove Player '%s'", st.Name))
		e.log(fmt.Sprintf("[online] Removed peer %s", e.peers[st.Name]))
		delete(e.peers, st.Name)
	case "deny":
		e.log(fmt.Sprintf("[online] Session rejected with peer because it is full (%s)", enAccount(st.Name)))
	case "save":
		e.save()
	case "count":
		// The count is in every A2S answer.
	default:
		return false
	}
	return true
}

// save writes the world: one line more each time, so a save always
// changes the file.
func (e *enshrouded) save() {
	e.saves = max(e.saves, 0) + 1
	os.MkdirAll(filepath.Dir(e.world), 0o755)
	var b strings.Builder
	b.WriteString("KSD world0 (stand-in) " + e.cfg.Name + "\n")
	for i := 1; i < e.saves; i++ {
		fmt.Fprintf(&b, "save %d\n", i)
	}
	if err := os.WriteFile(e.world, []byte(b.String()), 0o644); err != nil {
		e.log(fmt.Sprintf("[server] Failed to save %v", err))
	}
}

// stop is Ctrl+C: the real server's shutdown, which saves the world.
func (e *enshrouded) stop() {
	if e.stopping {
		return
	}
	e.stopping = true
	e.log("[app] Trigger gameflow shutdown, exit: Ctrl_C")
	e.log("[app] start destruction step StartGame")
	e.log("[app] start destruction step CreateServer")
	e.log(fmt.Sprintf("[game_server] Switching state from Run to Shutdown after %.2f s", time.Since(e.start).Seconds()))
	for _, p := range e.s.Players() {
		e.log(fmt.Sprintf("[server] Send Shutdown Message to Player '%s'", p))
		e.log(fmt.Sprintf("[server] Remove Entity for Player '%s'", p))
		e.s.Leave(p)
	}
	e.s.Sleep(3 * time.Second)
	e.save()
	e.log("[Session] 'HostOnline' (down)!")
	e.writeBack()
	e.log("[enshrouded] Close Log file")
	e.s.Exit(0)
}

// writeBack writes the settings file as the real server does when it
// stops: the same keys, plus the ones it adds when they are missing.
func (e *enshrouded) writeBack() {
	if e.raw == nil {
		return
	}
	for k, v := range map[string]string{"tags": "[]", "gameSettings": `{"playerHealthFactor": 1}`, "bannedAccounts": "[]"} {
		if _, ok := e.raw[k]; !ok {
			e.raw[k] = json.RawMessage(v)
		}
	}
	b, err := json.MarshalIndent(e.raw, "", "\t")
	if err == nil {
		os.WriteFile(e.cfgPath, append(b, '\n'), 0o644)
	}
}
