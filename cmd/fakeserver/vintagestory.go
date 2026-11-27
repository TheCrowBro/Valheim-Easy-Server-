package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// The vintagestory personality stands in for the Vintage Story dedicated
// server (internal/games/vintagestory), as the real 1.22.7 server behaves
// (that package's FACTS.md and testdata/real hold the real console). The app
// starts it as the server's .NET program:
//
//	dotnet <server>/<version>/VintagestoryServer.dll --dataPath DIR [--genconfig]
//
// Match knows it by VintagestoryServer.dll on a dotnet command line, which no
// other game's stand-in uses.
//
// Like the real server it:
//   - with --genconfig, writes a default serverconfig.json into --dataPath
//     and exits;
//   - otherwise reads serverconfig.json there (and makes one when there is
//     none), and stops at once, with exit code 0, when the player roles are
//     missing (a file written from scratch), as the real one does;
//   - opens the world at WorldConfig.SaveFileLocation, or makes it there,
//     and keeps it as <name>.vcdbs with a -wal and -shm beside it while it
//     runs, folded into the main file when it stops (the real one is
//     SQLite);
//   - listens on Port with TCP and UDP, and with the port taken prints the
//     real crash report and exits with code 0;
//   - writes time-stamped "[Server Event]" lines on standard output, the
//     ready, join, leave, saved and stop lines among them;
//   - saves on "/autosavenow", every 5 minutes (on its clock), and on its
//     way out; stops on "/stop" and on Ctrl+C, saving in both cases;
//   - reads its console as the real one does, so on Windows a line typed
//     with "\n" alone is lost and the console goes deaf (readConsole).

func init() {
	register(&Personality{
		ID:      "vintagestory",
		Version: "1.22.7",
		MemMB:   48,
		Match:   matchVintageStory,
		Run:     runVintageStory,
	})
}

func matchVintageStory(c Call) bool {
	if c.Program == "vintagestoryserver" {
		return true
	}
	if c.Program != "dotnet" {
		return false
	}
	for _, a := range c.Args.List() {
		if strings.EqualFold(filepath.Base(a), "VintagestoryServer.dll") {
			return true
		}
	}
	return false
}

// vsConfig is the part of serverconfig.json the stand-in reads.
type vsConfig struct {
	ServerName      string
	Port            int
	MaxClients      int
	Password        *string
	WhitelistMode   int
	DefaultRoleCode string
	Roles           []struct{ Code string }
	WorldConfig     struct {
		Seed             *string
		SaveFileLocation string
		WorldName        string
		PlayStyle        string
	}
}

// vsWorld is the stand-in's world file: a header the real server doesn't
// have (its world is SQLite), and padding that grows with every save, as
// a played world does.
type vsWorld struct {
	Name      string    `json:"name"`
	PlayStyle string    `json:"playStyle"`
	Seed      string    `json:"seed"`
	Created   time.Time `json:"created"`
	Saves     int       `json:"saves"`
}

type vintageStory struct {
	s        *Server
	data     string
	cfg      vsConfig
	path     string // the world's .vcdbs
	world    vsWorld
	clientID int
	stopping bool
}

func (v *vintageStory) line(kind, msg string) {
	v.s.Log(time.Now().Format("2.1.2006 15:04:05") + " [Server " + kind + "] " + msg)
}
func (v *vintageStory) note(msg string)  { v.line("Notification", msg) }
func (v *vintageStory) event(msg string) { v.line("Event", msg) }

func runVintageStory(s *Server) {
	a := s.Args()
	v := &vintageStory{s: s, data: a.String("dataPath", ".")}
	v.note("Server logger started.")
	v.note("Game Version: v" + s.Version() + " (Stable)")
	if a.Has("genconfig") {
		if err := v.writeDefaultConfig(); err != nil {
			s.Warnf("can't write serverconfig.json: %v", err)
			s.Exit(1)
		}
		v.note("Config generated.")
		s.Exit(0)
	}
	s.OnCrash(func() {
		v.line("Error", "Caught unhandled exception in thread 'Server'. Shutting down server.")
	})
	v.event("Launching server...")
	v.event("Loading configuration...")
	cfgPath := filepath.Join(v.data, "serverconfig.json")
	b, err := os.ReadFile(cfgPath)
	if os.IsNotExist(err) {
		v.note("serverconfig.json not found, creating new one")
		if err := v.writeDefaultConfig(); err != nil {
			s.Warnf("can't write serverconfig.json: %v", err)
			s.Exit(1)
		}
		b, err = os.ReadFile(cfgPath)
	}
	if err == nil {
		// A file without DefaultRoleCode gets the real server's own
		// default (its minimal-config.log names suplayer).
		v.cfg.DefaultRoleCode = "suplayer"
		err = json.Unmarshal(bytes.TrimPrefix(b, []byte("\ufeff")), &v.cfg)
	}
	if err != nil {
		v.line("Error", "Failed to read serverconfig.json")
		v.line("Error", "Exception: "+err.Error())
		v.stopSequence("serverconfig.json read error", false)
		return
	}
	if !v.hasRole(v.cfg.DefaultRoleCode) {
		v.line("Fatal", "You have configured a default group code "+v.cfg.DefaultRoleCode+" but no such group exists! Killing server")
		s.Exit(0)
	}
	if v.cfg.Port == 0 {
		v.cfg.Port = 42420
	}
	v.openWorld()
	s.Sleep(20 * time.Second) // loading assets and the spawn area

	// The real server opens TCP and then UDP; with either taken it stops
	// with its crash report, and exit code 0.
	for _, open := range []func(int) error{s.ListenTCP, s.ListenUDP} {
		if err := open(v.cfg.Port); err != nil {
			v.s.Log("Game Version: v" + s.Version() + " (Stable)")
			v.s.Log(time.Now().Format("1/2/2006 15:04:05") + ": Critical error occurred")
			v.s.Log("Loaded Mods: ")
			v.s.Log("System.Net.Sockets.SocketException (98): Address already in use")
			v.s.Log("   at System.Net.Sockets.Socket.DoBind(EndPoint endPointSnapshot, SocketAddress socketAddress)")
			v.s.Log("Crash written to file at \"" + filepath.Join(v.data, "Logs", "server-crash.log") + "\"")
			s.Exit(0)
		}
	}
	v.startWAL()
	v.readConsole()
	s.OnStop(func() {
		v.note("Server termination event SIGINT received. Shutting down server")
		v.note("Server: Exit() called")
		v.stopSequence("External close event SIGINT received", true)
	})
	s.OnStep(v.step)
	s.Every(5*time.Minute, func() { v.save(false) })
	v.event("Begin game ticking...")
	v.note("Starting server threads")
	lines := []string{v.stamp("Event", fmt.Sprintf("Dedicated Server now running on Port %d and all ips!", v.cfg.Port))}
	if v.cfg.WhitelistMode == 0 {
		lines = append(lines, v.stamp("Notification", "Please be aware that as of 1.20, servers default configurations have changed - servers no longer register themselves to the public servers list and are invite-only (whitelisted) out of the box. If you desire so, you can enable server advertising with '/serverconfig advertise on' and disable the whitelist mode with '/serverconfig whitelistmode off'"))
	}
	s.Ready(lines...)
	// Like the real server, it writes its settings back once it runs.
	v.rewriteConfig()
}

func (v *vintageStory) stamp(kind, msg string) string {
	return time.Now().Format("2.1.2006 15:04:05") + " [Server " + kind + "] " + msg
}

func (v *vintageStory) hasRole(code string) bool {
	for _, r := range v.cfg.Roles {
		if r.Code == code {
			return true
		}
	}
	return false
}

func (v *vintageStory) openWorld() {
	wc := v.cfg.WorldConfig
	v.path = wc.SaveFileLocation
	if v.path == "" {
		v.path = filepath.Join(v.data, "Saves", "default.vcdbs")
	}
	if !filepath.IsAbs(v.path) {
		v.path = filepath.Join(v.data, "Saves", v.path)
	}
	if wc.Seed != nil && *wc.Seed != "" {
		v.note("Using world seed: " + *wc.Seed)
	}
	v.note("Loading savegame")
	if b, err := os.ReadFile(v.path); err == nil {
		if i := bytes.IndexByte(b, '\n'); i < 0 || json.Unmarshal(b[:i], &v.world) != nil {
			v.line("Error", "Failed opening savegame.")
			v.line("Error", "Exception: SQLite Error 26: 'file is not a database'.")
			v.line("Fatal", "Failed opening savegame, possibly corrupted. Error Message: Failed opening savegame.. Will exit server now.")
			v.stopSequence("Failed opening savegame", false)
			return
		}
		v.note("Loaded existing save game data. Playstyle: " + v.world.PlayStyle + ", Playstyle Lang code: " + v.world.PlayStyle + ", WorldType: standard")
	} else {
		style := wc.PlayStyle
		if style == "" {
			style = "surviveandbuild"
		}
		seed := ""
		if wc.Seed != nil {
			seed = *wc.Seed
		}
		if seed == "" {
			seed = fmt.Sprint(rand.IntN(1 << 30))
		}
		v.world = vsWorld{Name: wc.WorldName, PlayStyle: style, Seed: seed, Created: time.Now().UTC()}
		v.note("Create new save game data. Playstyle: " + style)
		v.writeWorld()
	}
	v.note("Savegame " + v.path + " loaded")
}

// writeWorld writes the main world file, as SQLite folds its log back in.
func (v *vintageStory) writeWorld() {
	os.MkdirAll(filepath.Dir(v.path), 0o755)
	head, _ := json.Marshal(v.world)
	body := bytes.Repeat([]byte{byte('a' + v.world.Saves%26)}, 4096+512*v.world.Saves)
	if err := os.WriteFile(v.path, append(append(head, '\n'), body...), 0o644); err != nil {
		v.s.Warnf("can't write the world: %v", err)
	}
}

// startWAL opens the world's log and index, as SQLite does while it runs.
func (v *vintageStory) startWAL() {
	os.WriteFile(v.path+"-shm", make([]byte, 32768), 0o644)
	os.WriteFile(v.path+"-wal", nil, 0o644)
}

// save is an autosave (or one asked for): the changes go to the -wal.
func (v *vintageStory) save(asked bool) {
	if v.stopping {
		return
	}
	if asked {
		v.note("Server ticking has been suspended")
	}
	v.note("Autosaving game world. Notifying mods, then systems of save...")
	v.event("Mods and systems notified, now saving everything...")
	v.event("Saved player world data...")
	v.event("Saved map regions...")
	v.event("Saved map chunks...")
	f, err := os.OpenFile(v.path+"-wal", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err == nil {
		f.Write(bytes.Repeat([]byte{'w'}, 4096))
		f.Close()
	}
	v.event(fmt.Sprintf("World saved! Saved %d chunks, 0 mapchunks, 16 mapregions.", 8+rand.IntN(8)))
	if asked {
		v.note("Server ticking has been resumed")
		v.note("Autosave completed")
	}
	v.event("Offthread save of savegame done.")
}

// stopSequence saves (when the world was loaded) and exits with code 0, as
// every stop of the real server does, whatever the reason.
func (v *vintageStory) stopSequence(reason string, save bool) {
	if v.stopping {
		return
	}
	v.stopping = true
	v.note("Server stop requested, begin shutdown sequence. Stop reason: " + reason)
	v.note("Entering runphase Shutdown")
	if save {
		v.event("Mods and systems notified, now saving everything...")
		v.s.Sleep(time.Second)
		v.world.Saves++
		v.writeWorld()
		os.Remove(v.path + "-wal")
		os.Remove(v.path + "-shm")
		v.event("Saved savegamedata...2")
		v.event(fmt.Sprintf("World saved! Saved %d chunks, 1 mapchunks, 16 mapregions.", 8+rand.IntN(8)))
	}
	v.event("Shutting down 10 server threads... ")
	v.event("All threads gracefully shut down")
	v.event("Stopped the server!")
	v.s.Exit(0)
}

// vsNewline is the system's line ending as .NET's Environment.NewLine
// gives it, which the real server's console reader looks for.
func vsNewline() string {
	if runtime.GOOS == "windows" {
		return "\r\n"
	}
	return "\n"
}

// readConsole reads the console the way the real server does (its
// ServerConsole.readAsync, in VintagestoryLib.dll; the adapter's FACTS.md,
// "The console"), not with the framework's line reader, so that the app's
// console lines are tested against the real rule on every system: it reads
// up to 1024 bytes at a time as UTF-8, and runs what it has once that ends
// with the system's line ending ("\r\n" on Windows, "\n" elsewhere),
// trimmed, as one command. After a read that ends any other way (a line
// typed with "\n" alone on Windows, or half a line) the real server stops
// reading its console for good, without a word, and so does this one; so
// does the end of the console.
func (v *vintageStory) readConsole() {
	newline := vsNewline()
	go func() {
		buf := make([]byte, 1024)
		input := ""
		for {
			n, err := os.Stdin.Read(buf)
			if n == 0 {
				return // the end of the console, or an error
			}
			input += string(buf[:n])
			if !strings.HasSuffix(input, newline) {
				return
			}
			line := strings.TrimSpace(input)
			input = ""
			v.s.post(func() { v.command(line) })
			if err != nil {
				return
			}
		}
	}()
}

func (v *vintageStory) command(line string) {
	if line == "" {
		return
	}
	cmd, rest, _ := strings.Cut(line, " ")
	v.note("Handling Console Command " + cmd + " " + rest)
	switch strings.ToLower(cmd) {
	case "/autosavenow":
		v.save(true)
	case "/stop":
		v.note("Message to all in group 0: Console Admin commenced a server shutdown")
		v.event("Console Admin shuts down server.")
		v.stopSequence("Shutdown via server command", true)
	case "/list":
		v.note("List of online Players")
		for _, p := range v.s.Players() {
			v.s.Log("[" + p + "]")
		}
	default:
		v.note("Command not found: " + strings.TrimPrefix(cmd, "/"))
	}
}

func (v *vintageStory) step(st Step) bool {
	switch st.Verb {
	case "join":
		if len(v.s.Players()) >= v.cfg.MaxClients && v.cfg.MaxClients > 0 {
			v.note("Server and Connection queue is full")
			return true
		}
		v.clientID++
		port := 50000 + rand.IntN(10000)
		v.note(fmt.Sprintf("A Client attempts connecting via TCP on [::ffff:127.0.0.1]:%d, assigning client id %d", port, v.clientID))
		v.note(fmt.Sprintf("Client %d uid %s attempting identification. Name: %s", v.clientID, vsUID(st.Name), st.Name))
		if v.s.Join(st.Name) {
			v.event(fmt.Sprintf("%s [::ffff:127.0.0.1]:%d joins.", st.Name, port))
		}
	case "leave":
		if v.s.Leave(st.Name) {
			v.note(fmt.Sprintf("Client %d disconnected:", v.clientID))
			v.event("Player " + st.Name + " left.")
			v.event(fmt.Sprintf("Client %d disconnected.", v.clientID))
		}
	case "deny":
		v.clientID++
		v.note(fmt.Sprintf("Client %d uid %s attempting identification. Name: %s", v.clientID, vsUID(st.Name), st.Name))
		v.note(st.Name + " fails to join (invalid server password).")
	case "save":
		v.save(false)
	default:
		return false
	}
	return true
}

// vsUID is a made-up player uid in the real ones' form: 24 base64 letters.
func vsUID(name string) string {
	const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var b strings.Builder
	h := uint32(2166136261)
	for i := 0; b.Len() < 24; i++ {
		h ^= uint32(name[i%len(name)]) + uint32(i)
		h *= 16777619
		b.WriteByte(letters[h%64])
	}
	return b.String()
}

// writeDefaultConfig writes a default serverconfig.json, the shape --genconfig
// gives (shortened: two of the real file's nine roles).
func (v *vintageStory) writeDefaultConfig() error {
	if err := os.MkdirAll(v.data, 0o755); err != nil {
		return err
	}
	type role struct {
		Code            string
		PrivilegeLevel  int
		Name            string
		Privileges      []string
		DefaultGameMode int
		AutoGrant       bool
	}
	type worldConfig struct {
		Seed                *string
		SaveFileLocation    string
		WorldName           string
		AllowCreativeMode   bool
		PlayStyle           string
		PlayStyleLangCode   string
		WorldType           string
		WorldConfiguration  any
		MapSizeY            any
		CreatedByPlayerName any
		DisabledMods        any
		RepairMode          bool
	}
	cfg := struct {
		FileEditWarning     string
		ConfigVersion       string
		ServerName          string
		ServerUrl           any
		ServerDescription   any
		WelcomeMessage      string
		Ip                  any
		Port                int
		Upnp                bool
		AdvertiseServer     bool
		MaxClients          int
		Password            any
		ServerLanguage      string
		Roles               []role
		DefaultRoleCode     string
		ModPaths            []string
		WorldConfig         worldConfig
		OnlyWhitelisted     bool
		WhitelistMode       int
		VerifyPlayerAuth    bool
		AllowPvP            bool
		DieBelowDiskSpaceMb int
	}{
		FileEditWarning: "", ConfigVersion: "1.7", ServerName: "Vintage Story Server",
		WelcomeMessage: "Welcome {0}, may you survive well and prosper", Port: 42420, MaxClients: 16,
		ServerLanguage: "en",
		Roles: []role{
			{Code: "suplayer", Name: "Survival Player", Privileges: []string{"chat", "build", "useblock"}, DefaultGameMode: 1},
			{Code: "admin", PrivilegeLevel: 99999, Name: "Admin", Privileges: []string{"root", "controlserver"}, DefaultGameMode: 1, AutoGrant: true},
		},
		DefaultRoleCode: "suplayer",
		ModPaths:        []string{"Mods", filepath.Join(v.data, "Mods")},
		WorldConfig: worldConfig{SaveFileLocation: filepath.Join(v.data, "Saves", "default.vcdbs"), WorldName: "A new world",
			AllowCreativeMode: true, PlayStyle: "surviveandbuild", PlayStyleLangCode: "surviveandbuild-bands", WorldType: "standard"},
		VerifyPlayerAuth: true, AllowPvP: true, DieBelowDiskSpaceMb: 400,
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(v.data, "serverconfig.json"), b, 0o644)
}

// rewriteConfig writes the settings back, every key kept (in Go's order,
// not the server's: the app must not depend on the order), as the real
// server does once it has started.
func (v *vintageStory) rewriteConfig() {
	path := filepath.Join(v.data, "serverconfig.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var all map[string]any
	if json.Unmarshal(bytes.TrimPrefix(b, []byte("\ufeff")), &all) != nil {
		return
	}
	all["ConfigVersion"] = "1.10"
	if out, err := json.MarshalIndent(all, "", "  "); err == nil {
		os.WriteFile(path, out, 0o644)
	}
}
