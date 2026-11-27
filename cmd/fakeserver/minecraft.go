package main

import (
	"bytes"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The minecraft personality stands in for Minecraft: Java Edition's
// vanilla server, run as the app runs it (internal/games/minecraft):
//
//	java -Xms512M -Xmx2048M -jar <server>/server.jar --nogui --universe DIR --world NAME --port N
//
// in the server folder, which holds server.properties, eula.txt and the
// list files (whitelist.json, ops.json, banned-players.json). It writes
// the lines the real 26.3 server writes (copied from a real run; the
// adapter's testdata has them), on standard output, which the app
// captures:
//
//	[19:40:16] [Server thread/INFO]: System chat: Alex joined the game
//
// It refuses to start without eula=true, listens on the game port and on
// RCON (rcon.port, rcon.password) once ready, and answers RCON's list,
// stop, save-all, whitelist, op/deop and ban/pardon. A world is a folder
// <universe>/<world> holding level.dat and a region file that grows with
// every save. "stop" saves with the real server's lines; Ctrl+C saves
// without any (the real server's log has closed by then) and exits with
// Java's 130.
//
// Script verbs: join, leave, death, deny (turned away: not on the allow
// list), save, count. A scripted join always gets in, so a test needn't
// fill the allow list first.

func init() {
	register(&Personality{
		ID:      "minecraft",
		Version: "26.3",
		MemMB:   40,
		Match:   matchMinecraft,
		Run:     runMinecraft,
	})
}

// matchMinecraft: Java running a jar called server.jar with --nogui, which
// is how Minecraft's server is started (other Java games name their own
// jar or main class).
func matchMinecraft(c Call) bool {
	// --universe is Minecraft's own: Necesse's Server.jar also runs with
	// -nogui, and a case-blind jar name can't tell them apart.
	return c.Program == "java" && strings.EqualFold(filepath.Base(c.Args.String("jar", "")), "server.jar") && c.Args.Has("nogui") && c.Args.Has("universe")
}

type minecraft struct {
	s        *Server
	dir      string // the server folder (the working directory)
	props    map[string]string
	universe string
	world    string // the world's folder name
	level    string // its name inside (level-name)
	worldDir string
	port     int
	max      int
	saves    int
	entity   int
	stopping bool
}

func runMinecraft(s *Server) {
	a := s.Args()
	dir, _ := os.Getwd()
	m := &minecraft{s: s, dir: dir, props: readProps(filepath.Join(dir, "server.properties")), entity: 4}
	s.Log("Starting net.minecraft.server.Main")
	if eula := readProps(filepath.Join(dir, "eula.txt")); !strings.EqualFold(eula["eula"], "true") {
		m.log("ServerMain", "INFO", "You need to agree to the EULA in order to run the server. Go to eula.txt for more info.")
		s.Exit(0)
	}
	m.universe = a.String("universe", ".")
	m.level = m.prop("level-name", "world")
	m.world = a.String("world", m.level)
	m.worldDir = filepath.Join(m.universe, m.world)
	m.port = a.Int("port", atoiDef(m.prop("server-port", ""), 25565))
	m.max = atoiDef(m.prop("max-players", ""), 20)
	s.OnCrash(func() {
		m.log("Server thread", "ERROR", "Encountered an unexpected exception")
		s.Log("java.lang.IllegalStateException: the stand-in crashed, as its fakeserver.json asked")
	})

	if _, err := os.Stat(filepath.Join(m.worldDir, "level.dat")); err != nil {
		m.log("Worker-Main-1", "INFO", "No existing world data, creating new world")
	}
	m.say("Starting minecraft server version " + s.Version())
	m.say("Loading properties")
	m.say("Default game type: " + strings.ToUpper(m.prop("gamemode", "survival")))
	m.say("Generating keypair")
	m.say(fmt.Sprintf("Starting Minecraft server on *:%d", m.port))
	if err := s.ListenTCP(m.port); err != nil {
		m.log("Server thread", "WARN", "**** FAILED TO BIND TO PORT!")
		m.log("Server thread", "WARN", "The exception was: io.netty.channel.unix.Errors$NativeIoException: bind(..) failed: "+err.Error())
		m.log("Server thread", "WARN", "Perhaps a server is already running on that port?")
		m.log("Server thread", "ERROR", "Encountered an unexpected exception")
		s.Log("java.lang.IllegalStateException: Failed to initialize server")
		m.say("Stopping server")
		m.say("Saving worlds")
		s.Exit(0)
	}
	m.say(fmt.Sprintf("Preparing level %q", m.world))
	start := time.Now()
	s.Sleep(5 * time.Second) // loading or making the world
	m.write()
	m.say("Preparing spawn area: 100%")
	m.say(fmt.Sprintf("Time elapsed: %d ms", time.Since(start).Milliseconds()))

	s.OnStop(func() {
		// Java's shutdown hook saves and stops the server, but its log has
		// already closed, so nothing is written.
		m.write()
		s.Exit(130)
	})
	s.OnCommand(func(line string) {
		if reply := m.command(line, false); reply != "" {
			m.say(systemChatPrefix + reply)
		}
	})
	s.OnStep(m.step)
	// Autosaves, every 5 minutes of game time; the real ones are silent.
	s.Every(5*time.Minute, m.write)

	s.Ready(m.stamp("Server thread", "INFO", fmt.Sprintf("Done (%.3fs)! For help, type \"help\"", time.Since(start).Seconds())))
	if m.prop("enable-rcon", "false") == "true" {
		rport := atoiDef(m.prop("rcon.port", ""), 25575)
		m.say("Starting remote control listener")
		if err := s.RCON(rport, m.prop("rcon.password", ""), m.rcon); err != nil {
			m.log("Server thread", "WARN", fmt.Sprintf("Unable to initialise RCON on 0.0.0.0:%d", rport))
			s.Log("java.net.BindException: Address already in use")
		} else {
			m.say("Thread RCON Listener started")
			m.say(fmt.Sprintf("RCON running on 0.0.0.0:%d", rport))
		}
	}
	m.saveChunks() // the save after loading, as the real server does
}

const systemChatPrefix = "System chat: "

// stamp is a line in Minecraft's format.
func (m *minecraft) stamp(thread, level, msg string) string {
	return fmt.Sprintf("[%s] [%s/%s]: %s", time.Now().Format("15:04:05"), thread, level, msg)
}

func (m *minecraft) log(thread, level, msg string) { m.s.Log(m.stamp(thread, level, msg)) }

// say is a line from the server's main thread.
func (m *minecraft) say(msg string) { m.log("Server thread", "INFO", msg) }

func (m *minecraft) prop(key, def string) string {
	if v, ok := m.props[key]; ok && v != "" {
		return v
	}
	return def
}

// write saves the world: level.dat, and a region file that grows a little
// with every save, as a played world does.
func (m *minecraft) write() {
	m.saves++
	region := filepath.Join(m.worldDir, "dimensions", "minecraft", "overworld", "region")
	os.MkdirAll(region, 0o755)
	os.MkdirAll(filepath.Join(m.worldDir, "players", "data"), 0o755)
	b, _ := json.Marshal(map[string]any{"LevelName": m.level, "saves": m.saves, "saved": time.Now().UTC()})
	os.WriteFile(filepath.Join(m.worldDir, "level.dat"), b, 0o644)
	os.WriteFile(filepath.Join(region, "r.0.0.mca"), bytes.Repeat([]byte{byte(m.saves)}, 4096+512*m.saves), 0o644)
}

// saveChunks is a save with the lines the real server writes for it.
func (m *minecraft) saveChunks() {
	m.write()
	for _, dim := range []string{"overworld", "the_nether", "the_end"} {
		m.say(fmt.Sprintf("Saving chunks for level 'ServerLevel[%s]'/minecraft:%s", m.level, dim))
	}
}

// stop is "stop" (console or RCON): save everything, then exit.
func (m *minecraft) stop() {
	if m.stopping {
		return
	}
	m.stopping = true
	m.say("Stopping server")
	m.say("Saving players")
	m.say("Saving worlds")
	m.saveChunks()
	m.say("Thread RCON Listener stopped")
	m.s.Exit(0)
}

// offlineUUID is the account id Minecraft gives a name without Mojang
// (a name-based UUID of "OfflinePlayer:<name>"): stable, which is all the
// stand-in needs.
func offlineUUID(name string) string {
	h := md5.Sum([]byte("OfflinePlayer:" + name))
	h[6] = h[6]&0x0f | 0x30
	h[8] = h[8]&0x3f | 0x80
	x := fmt.Sprintf("%x", h[:])
	return x[0:8] + "-" + x[8:12] + "-" + x[12:16] + "-" + x[16:20] + "-" + x[20:]
}

var deathsSaid = []string{"was slain by Zombie", "fell from a high place", "drowned", "hit the ground too hard", "was shot by Skeleton"}

func (m *minecraft) step(st Step) bool {
	switch st.Verb {
	case "join":
		if !m.s.Join(st.Name) {
			return true
		}
		m.entity += 17
		m.log("User Authenticator #1", "INFO", fmt.Sprintf("UUID of player %s is %s", st.Name, offlineUUID(st.Name)))
		m.say(fmt.Sprintf("%s[/127.0.0.1:%d] logged in with entity id %d at (102.5, 144.0, -33.5)", st.Name, 40000+rand.IntN(20000), m.entity))
		m.say(systemChatPrefix + st.Name + " joined the game")
		m.remember(st.Name)
	case "leave":
		if m.s.Leave(st.Name) {
			m.say(st.Name + " lost connection: Disconnected")
			m.say(systemChatPrefix + st.Name + " left the game")
		}
	case "death":
		m.say(systemChatPrefix + st.Name + " " + deathsSaid[rand.IntN(len(deathsSaid))])
	case "deny":
		addr := fmt.Sprintf("/127.0.0.1:%d", 40000+rand.IntN(20000))
		m.say(fmt.Sprintf("Disconnecting %s (%s): You are not white-listed on this server!", st.Name, addr))
		m.say(fmt.Sprintf("%s (%s) lost connection: You are not white-listed on this server!", st.Name, addr))
		m.log("Server thread", "WARN", "handleDisconnection() called twice")
	case "save":
		m.say(systemChatPrefix + "Saving the game (this may take a moment!)")
		m.write()
		m.say(systemChatPrefix + "Saved the game")
	case "count":
		m.say(systemChatPrefix + m.listReply())
	default:
		return false
	}
	return true
}

// remember adds a player to Minecraft's user cache, in the universe folder
// as 26.3 keeps it.
func (m *minecraft) remember(name string) {
	path := filepath.Join(m.universe, "usercache.json")
	var cache []map[string]string
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &cache)
	}
	cache = slices.DeleteFunc(cache, func(e map[string]string) bool { return strings.EqualFold(e["name"], name) })
	cache = append(cache, map[string]string{"uuid": offlineUUID(name), "name": name,
		"expiresOn": time.Now().AddDate(0, 1, 0).UTC().Format("2006-01-02 15:04:05 -0700")})
	b, _ := json.Marshal(cache)
	os.WriteFile(path, b, 0o644)
}

func (m *minecraft) listReply() string {
	p := m.s.Players()
	return fmt.Sprintf("There are %d of a max of %d players online: %s", len(p), m.max, strings.Join(p, ", "))
}

// rcon answers an RCON command, echoing it to the log as the real server
// does for admins ("[Rcon: …]").
func (m *minecraft) rcon(cmd string) string {
	return m.command(cmd, true)
}

// command runs a console or RCON command and returns its answer.
func (m *minecraft) command(line string, viaRCON bool) string {
	echo := func(msg string) {
		if viaRCON && m.prop("broadcast-rcon-to-ops", "true") == "true" {
			m.say(systemChatPrefix + "[Rcon: " + msg + "]")
		}
	}
	f := strings.Fields(line)
	if len(f) == 0 {
		return ""
	}
	arg := func(i int) string {
		if i < len(f) {
			return f[i]
		}
		return ""
	}
	switch strings.ToLower(f[0]) {
	case "list":
		return m.listReply()
	case "stop":
		echo("Stopping the server")
		m.s.post(m.stop) // after the answer has gone out
		return "Stopping the server"
	case "save-all":
		m.write()
		echo("Saved the game")
		return "Saving the game (this may take a moment!)Saved the game"
	case "whitelist":
		return m.whitelist(strings.ToLower(arg(1)), arg(2), echo)
	case "op":
		return m.listChange("ops.json", arg(1), true, "Made %s a server operator", "Nothing changed. The player is already an operator", echo)
	case "deop":
		return m.listChange("ops.json", arg(1), false, "Made %s no longer a server operator", "Nothing changed. The player is not an operator", echo)
	case "ban":
		return m.listChange("banned-players.json", arg(1), true, "Banned %s: Banned by an operator.", "Nothing changed. The player is already banned", echo)
	case "pardon":
		return m.listChange("banned-players.json", arg(1), false, "Unbanned %s", "Nothing changed. The player isn't banned", echo)
	}
	return "Unknown or incomplete command. See below for error" + line + "<--[HERE]"
}

func (m *minecraft) whitelist(sub, name string, echo func(string)) string {
	switch sub {
	case "add":
		return m.listChange("whitelist.json", name, true, "Added %s to the whitelist", "Player is already whitelisted", echo)
	case "remove":
		return m.listChange("whitelist.json", name, false, "Removed %s from the whitelist", "Player is not whitelisted", echo)
	case "on", "off":
		want := sub == "on"
		if (m.prop("white-list", "false") == "true") == want {
			return "Whitelist is already turned " + sub
		}
		m.props["white-list"] = fmt.Sprint(want)
		setProp(filepath.Join(m.dir, "server.properties"), "white-list", fmt.Sprint(want))
		echo("Whitelist is now turned " + sub)
		return "Whitelist is now turned " + sub
	case "list":
		names := m.listNames("whitelist.json")
		if len(names) == 0 {
			return "There are no whitelisted players"
		}
		return fmt.Sprintf("There are %d whitelisted player(s): %s", len(names), strings.Join(names, ", "))
	}
	return "Unknown or incomplete command. See below for errorwhitelist " + sub + "<--[HERE]"
}

func (m *minecraft) readList(file string) []map[string]any {
	var list []map[string]any
	if b, err := os.ReadFile(filepath.Join(m.dir, file)); err == nil {
		json.Unmarshal(b, &list)
	}
	return list
}

func (m *minecraft) listNames(file string) []string {
	var names []string
	for _, e := range m.readList(file) {
		if n, _ := e["name"].(string); n != "" {
			names = append(names, n)
		}
	}
	return names
}

// listChange adds a player to a list file or takes one off, as the real
// commands do.
func (m *minecraft) listChange(file, name string, add bool, done, nothing string, echo func(string)) string {
	if name == "" {
		return "Unknown or incomplete command. See below for error<--[HERE]"
	}
	list := m.readList(file)
	i := slices.IndexFunc(list, func(e map[string]any) bool { n, _ := e["name"].(string); return strings.EqualFold(n, name) })
	switch {
	case add && i >= 0, !add && i < 0:
		return nothing
	case add:
		e := map[string]any{"uuid": offlineUUID(name), "name": name}
		switch file {
		case "ops.json":
			e["level"], e["bypassesPlayerLimit"] = 4, false
		case "banned-players.json":
			e["created"], e["source"], e["expires"], e["reason"] = time.Now().UTC().Format("2006-01-02 15:04:05 -0700"), "Rcon", "forever", "Banned by an operator."
		}
		list = append(list, e)
	default:
		list = slices.Delete(list, i, i+1)
	}
	if list == nil {
		list = []map[string]any{}
	}
	b, _ := json.MarshalIndent(list, "", "  ")
	os.WriteFile(filepath.Join(m.dir, file), b, 0o644)
	msg := fmt.Sprintf(done, name)
	echo(msg)
	return msg
}

// readProps reads a .properties file's key=value lines (enough for what the
// app writes: it escapes ":" and "=" with a backslash).
func readProps(path string) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || line[0] == '#' || line[0] == '!' {
			continue
		}
		k, v, _ := strings.Cut(line, "=")
		r := strings.NewReplacer(`\:`, ":", `\=`, "=", `\#`, "#", `\!`, "!", `\\`, `\`)
		out[strings.TrimSpace(r.Replace(k))] = strings.TrimSpace(r.Replace(v))
	}
	return out
}

// setProp changes one key's line in a .properties file, as the real server
// does when "whitelist on" changes white-list.
func setProp(path, key, value string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(string(b), "\n")
	found := false
	for i, l := range lines {
		if k, _, ok := strings.Cut(l, "="); ok && strings.TrimSpace(k) == key {
			lines[i], found = key+"="+value, true
		}
	}
	if !found {
		lines = append(lines, key+"="+value)
	}
	os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

func atoiDef(s string, def int) int {
	var n int
	if _, err := fmt.Sscan(strings.TrimSpace(s), &n); err != nil {
		return def
	}
	return n
}
