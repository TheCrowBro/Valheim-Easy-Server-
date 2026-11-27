package main

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The zomboid personality stands in for the Project Zomboid Build 42
// dedicated server (internal/games/zomboid), as the real server behaves
// (that package's FACTS.md and testdata/real hold real console output).
// The app starts it as the server's own Java, the way StartServer64.bat
// does:
//
//	java -Djava.awt.headless=true … -Xmx6144m -Drconlo=true … -cp java/;java/projectzomboid.jar
//	     zombie.network.GameServer -statistic 0 -cachedir=DIR -servername NAME
//	     -adminusername admin -adminpassword PW -port 16261 -udpport 16262
//
// Match knows it by the main class, zombie.network.GameServer, which
// neither Minecraft ("-jar server.jar --nogui") nor Necesse ("-jar
// Server.jar -nogui -datadir …") has; it runs no jar.
//
// Like the real server it writes its log lines with their header
// ("LOG  : Network      f:0 st:34,907,676> *** SERVER STARTED ****") on
// standard output; keeps everything under -cachedir: the world's settings
// in Server/<name>.ini (completed with defaults and written back at every
// start, as the real one does) and Server/<name>_SandboxVars.lua, its map
// in Saves/Multiplayer/<name>/ and its accounts in db/<name>.db; asks for
// an admin password on its console when a new world gets none; listens on
// the UDP game and direct ports; once started, answers RCON (RCONPort and
// RCONPassword from the .ini: "players", "save", "quit"); and saves on
// "quit" and on Ctrl+C (Java's shutdown handling saves). Joins and leaves
// go to its own Logs/<time>_user.txt, never to the console.
//
// Script verbs: join, leave, save. A scripted join always gets in.

func init() {
	register(&Personality{
		ID:      "zomboid",
		Version: "42.22.2",
		MemMB:   48,
		Match:   matchZomboid,
		Run:     runZomboid,
	})
}

const zomboidMain = "zombie.network.GameServer"

func matchZomboid(c Call) bool {
	return c.Program == "java" && c.Args.Contains(zomboidMain)
}

// zomboid is one run of the stand-in.
type zomboid struct {
	s        *Server
	started  time.Time
	frame    int
	cache    string // -cachedir
	name     string // -servername: the world
	ini      map[string]string
	saveDir  string // Saves/Multiplayer/<name>
	saves    int
	quitting bool
	userLog  string
}

// zomboidRevision is the build revision a 42.22.2 server prints.
const zomboidRevision = "3d39148846"

// The keys the real server writes into a new world's .ini that the app
// and this stand-in care about, with the real defaults (FACTS.md).
var zomboidIniDefaults = []struct{ key, value string }{
	{"PVP", "true"}, {"PauseEmpty", "true"}, {"Open", "true"},
	{"DefaultPort", "16261"}, {"UDPPort", "16262"}, {"Public", "false"},
	{"PublicName", "My PZ Server"}, {"MaxPlayers", "32"},
	{"RCONPort", "27015"}, {"RCONPassword", ""}, {"Password", ""}, {"UPnP", "true"},
}

// log writes one line with the real server's header: its level, the part
// of the game that wrote it padded to 12, the frame and the server time.
func (z *zomboid) log(level, part, msg string) { z.s.Log(z.header(level, part, msg)) }

func (z *zomboid) info(msg string) { z.log("LOG", "General", msg) }
func (z *zomboid) net(msg string)  { z.log("LOG", "Network", msg) }

// groupDigits writes 34907676 as "34,907,676", as the server's st: does.
func groupDigits(n int64) string {
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func runZomboid(s *Server) {
	a := s.Args()
	z := &zomboid{s: s, started: time.Now(), cache: a.String("cachedir", ""), name: a.String("servername", "servertest")}
	if z.cache == "" {
		// The real server would use the Zomboid folder in the user's home,
		// where the owner's own game keeps its saves; a stand-in must never
		// write there, so it uses one in its working folder instead.
		wd, _ := os.Getwd()
		z.cache = filepath.Join(wd, "Zomboid")
	}
	z.saveDir = filepath.Join(z.cache, "Saves", "Multiplayer", z.name)
	s.OnCrash(func() {
		z.log("ERROR", "General", "GameServer.main> Exception thrown")
		s.Log("java.lang.IllegalStateException: the stand-in server crashed, as its fakeserver.json asked")
		z.net("Server exited")
	})

	z.info(fmt.Sprintf("version=%s %s integrity=pending demo=false", s.Version(), zomboidRevision))
	z.info("revision=" + zomboidRevision)
	z.info(fmt.Sprintf("server name is %q", z.name))
	newWorld := !fileExists(filepath.Join(z.saveDir, "map_t.bin"))
	if newWorld {
		z.info("map_t.bin does not exist, cannot determine the server's WorldVersion.  This is ok the first time a server is started.")
	}
	z.info("Loading networking libraries...")
	z.info("Loading RakNet64...")
	z.info("Loading ZNet64...")
	z.info("Waiting for response from Steam servers")

	z.loadIni(a.Int("port", -1), a.Int("udpport", -1))
	z.ensureSandbox()
	if !z.admin(a.String("adminusername", "admin"), a.String("adminpassword", "")) {
		return // waiting on the console for an admin password, for good
	}
	z.net("Initialising Server Systems...")
	z.net("Loading world...")
	s.Sleep(60 * time.Second) // a real server takes about a minute
	if newWorld {
		z.writeWorld()
	}

	game, direct := atoiOr(z.ini["DefaultPort"], 16261), atoiOr(z.ini["UDPPort"], 16262)
	for _, p := range []int{game, direct} {
		if err := s.ListenUDP(p); err != nil {
			z.cantStart(p, err)
			return
		}
	}

	s.OnCommand(z.console)
	s.OnStop(func() { z.shutdown(130) }) // Java's shutdown handling saves, then exits 130
	s.OnStep(z.step)
	z.frame = 1
	s.Ready(z.header("LOG", "Network", "*** SERVER STARTED ****"), z.header("LOG", "Network", "*** Steam is enabled"))
	z.openRCON()
}

// header is a log line with the real server's header (log writes it;
// Ready takes it ready-made).
func (z *zomboid) header(level, part, msg string) string {
	prefix := map[string]string{"LOG": "LOG  : ", "WARN": "WARN : ", "ERROR": "ERROR: "}[level]
	ms := 30_000_000 + time.Since(z.started).Milliseconds()
	return fmt.Sprintf("%s%-12s f:%d st:%s> %s", prefix, part, z.frame, groupDigits(ms), msg)
}

func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

var reIniLine = regexp.MustCompile(`^([^#=\s][^=]*)=(.*)$`)

// loadIni reads Server/<name>.ini (each line's value up to its next "=",
// trimmed, as the real server reads it), takes the command line's ports
// over the file's, adds the defaults the file lacks and writes it back.
func (z *zomboid) loadIni(port, udpPort int) {
	path := filepath.Join(z.cache, "Server", z.name+".ini")
	b, _ := os.ReadFile(path)
	z.ini = map[string]string{}
	var order []string
	nl := "\n"
	if bytes.Contains(b, []byte("\r\n")) {
		nl = "\r\n"
	}
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(strings.TrimSuffix(l, "\r"))
		m := reIniLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		v, _, _ := strings.Cut(m[2], "=")
		if _, seen := z.ini[m[1]]; !seen {
			order = append(order, m[1])
		}
		z.ini[m[1]] = strings.TrimSpace(v)
	}
	for _, d := range zomboidIniDefaults {
		if _, ok := z.ini[d.key]; !ok {
			z.ini[d.key] = d.value
			order = append(order, d.key)
		}
	}
	if port > 0 {
		z.ini["DefaultPort"] = strconv.Itoa(port)
	}
	if udpPort > 0 {
		z.ini["UDPPort"] = strconv.Itoa(udpPort)
	}
	var out []string
	for _, k := range order {
		out = append(out, k+"="+z.ini[k])
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(strings.Join(out, nl+nl)), 0o644)
}

// ensureSandbox makes Server/<name>_SandboxVars.lua when the world has
// none (the real server writes its whole default file then), and the two
// spawn files.
func (z *zomboid) ensureSandbox() {
	dir := filepath.Join(z.cache, "Server")
	path := filepath.Join(dir, z.name+"_SandboxVars.lua")
	if !fileExists(path) {
		os.WriteFile(path, []byte("SandboxVars = {\n    VERSION = 6,\n    Zombies = 4,\n    ZombieConfig = {\n        PopulationMultiplier = 0.65,\n    },\n}\n"), 0o644)
	}
	z.info("writing " + path)
	for name, text := range map[string]string{
		"_spawnregions.lua": "function SpawnRegions()\n\treturn {\n\t\t{ name = \"Muldraugh, KY\", file = \"media/maps/Muldraugh, KY/spawnpoints.lua\" },\n\t}\nend\n",
		"_spawnpoints.lua":  "function SpawnPoints()\n\treturn {\n\t\tunemployed = {\n\t\t\t{ worldX = 40, worldY = 22, posX = 67, posY = 201 }\n\t\t}\n\t}\nend\n",
	} {
		if p := filepath.Join(dir, z.name+name); !fileExists(p) {
			os.WriteFile(p, []byte(text), 0o644)
		}
	}
}

// admin makes the admin account when the world has none, as the real
// server does: with -adminpassword at once, without it by asking on the
// console for a password and its confirmation, and waiting for them. When
// the console ends first, the real server's Java ends with an exception
// (its line reader finds no line), and so does the stand-in. It says false
// when the server ended.
func (z *zomboid) admin(user, password string) bool {
	db := filepath.Join(z.cache, "db", z.name+".db")
	if !fileExists(db) {
		if password == "" {
			z.info("User '" + user + "' not found, creating it ")
			z.info("Command line admin password: null")
			z.info("Enter new administrator password: ")
			first, ok := readConsoleLine()
			if ok {
				z.info("Confirm the password: ")
				_, ok = readConsoleLine()
			}
			if !ok || first == "" {
				z.s.Log("Exception in thread \"main\" java.util.NoSuchElementException: No line found")
				z.s.Log("\tat java.base/java.util.Scanner.nextLine(Scanner.java:1660)")
				z.s.Log("\tat zombie.network.ServerWorldDatabase.create(ServerWorldDatabase.java:709)")
				z.info("Shutdown handling started")
				z.net("Server exited")
				z.info("Shutdown handling finished")
				z.s.Exit(1)
				return false
			}
		}
		os.MkdirAll(filepath.Dir(db), 0o755)
		os.WriteFile(db, []byte("SQLite format 3\x00 stand-in accounts: "+user+"\n"), 0o644)
		z.info("Administrator account '" + user + "' created.")
	}
	if password != "" {
		z.info("admin password changed via -adminpassword option")
	}
	return true
}

// readConsoleLine reads one line typed on the console, a byte at a time so
// nothing after it is taken from the console loop that reads on later. It
// says false when the console ended first.
func readConsoleLine() (string, bool) {
	var line []byte
	b := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(b)
		if n == 1 {
			if b[0] == '\n' {
				return strings.TrimSpace(string(line)), true
			}
			line = append(line, b[0])
			continue
		}
		if err != nil {
			return "", false
		}
	}
}

// writeWorld makes a new world's map files.
func (z *zomboid) writeWorld() {
	os.MkdirAll(z.saveDir, 0o755)
	for name, size := range map[string]int{"map_t.bin": 2048, "map_ver.bin": 8, "WorldDictionary.bin": 512, "players.db": 4096, "vehicles.db": 4096} {
		os.WriteFile(filepath.Join(z.saveDir, name), bytes.Repeat([]byte{1}, size), 0o644)
	}
}

// cantStart ends a start whose UDP port another program has: the real
// server's RakNet start fails, the server says so in an exception and its
// Java ends. The stand-in first says which port in the system's own words,
// on standard error.
func (z *zomboid) cantStart(port int, err error) {
	z.s.Warnf("UDP port %d: %v", port, err)
	z.net("Initialising RakNet...")
	z.log("ERROR", "General", "GameServer.main> Exception thrown")
	z.s.Log("java.net.ConnectException: Connection Startup Failed. Code: 5")
	z.s.Log("\tat zombie.core.raknet.UdpEngine.<init>(UdpEngine.java:76)")
	z.s.Log("\tat zombie.network.GameServer.startServer(GameServer.java:1493)")
	z.info("Shutdown handling started")
	z.net("Server exited")
	z.info("Shutdown handling finished")
	z.s.Exit(0)
}

// openRCON listens for RCON once the server has started, when the .ini
// gives a port and a password (an empty password turns RCON off).
func (z *zomboid) openRCON() {
	port, pw := atoiOr(z.ini["RCONPort"], 27015), z.ini["RCONPassword"]
	if port == 0 || pw == "" {
		return
	}
	if err := z.s.RCON(port, pw, z.command); err != nil {
		z.info(fmt.Sprintf("RCON: error creating socket on port %d", port))
		z.log("ERROR", "General", "RCONServer.<init>> Exception thrown")
		z.s.Log("java.net.BindException: Address already in use: bind")
		return
	}
	z.info(fmt.Sprintf("RCON: listening on port %d", port))
}

// command runs one console or RCON command and returns its answer, as the
// server's command handler does (on the server's goroutine).
func (z *zomboid) command(line string) string {
	cmd, _, _ := strings.Cut(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "/")), " ")
	switch strings.ToLower(cmd) {
	case "players":
		var b strings.Builder
		players := z.s.Players()
		fmt.Fprintf(&b, "Players connected (%d): \n", len(players))
		for _, p := range players {
			b.WriteString("-" + p + "\n")
		}
		return b.String()
	case "save":
		z.s.post(z.save) // the real one saves on its next tick
		return "World saved"
	case "quit":
		z.s.post(func() { z.shutdown(0) })
		return "Quit"
	case "":
		return ""
	}
	return "Unknown command " + cmd
}

// console is a line typed on the server's console: echoed, then answered
// in the log.
func (z *zomboid) console(line string) {
	if line == "" {
		return
	}
	z.info(fmt.Sprintf("command entered via server console (System.in): %q", line))
	reply := z.command(line)
	first, rest, _ := strings.Cut(reply, "\n")
	z.info(first)
	for _, l := range strings.Split(strings.TrimSuffix(rest, "\n"), "\n") {
		if l != "" {
			z.s.Log(l)
		}
	}
}

// save writes the world as the real server's save does, with its lines.
func (z *zomboid) save() {
	start := time.Now()
	z.saves++
	os.MkdirAll(z.saveDir, 0o755)
	// The map grows a little with every save, run after run, as a played
	// world does.
	mapFile := filepath.Join(z.saveDir, "map_t.bin")
	size := 2048
	if st, err := os.Stat(mapFile); err == nil {
		size = int(st.Size())
	}
	os.WriteFile(mapFile, bytes.Repeat([]byte{byte(z.saves)}, size+64), 0o644)
	os.WriteFile(filepath.Join(z.saveDir, "players.db"), bytes.Repeat([]byte{2}, 4096+16*len(z.s.Players())), 0o644)
	z.info(fmt.Sprintf("SaveAll took %.4f ms", float64(time.Since(start).Microseconds())/1000))
	z.info("Saving players")
	z.info("Saving worldgen params")
	z.info("Saving GlobalModData")
	z.info("Saving finish")
	z.info(fmt.Sprintf("Saving took %.4f ms", float64(time.Since(start).Microseconds())/1000+20))
}

// shutdown is Java's shutdown handling, which runs on "quit" (exit 0) and
// on Ctrl+C (exit 130): it saves, waits five seconds for the clients to
// go, closes the databases and ends.
func (z *zomboid) shutdown(code int) {
	if z.quitting {
		return
	}
	z.quitting = true
	z.info("Shutdown handling started")
	z.net("Server exited")
	z.save()
	z.s.Sleep(5 * time.Second)
	z.info("The id_manager_data.bin file is saved")
	z.info("waiting for UdpEngine thread termination")
	z.info("Shutdown handling finished")
	z.s.Exit(code)
}

// zomboidAccount is a made-up Steam account number for a name, the same
// every time.
func zomboidAccount(name string) int64 {
	h := fnv.New32a()
	h.Write([]byte(name))
	return 76561198000000000 + int64(h.Sum32()%100000000)
}

// user writes one line of the server's own user log, where joins and
// leaves go (Logs/<start time>_user.txt).
func (z *zomboid) user(line string) {
	if z.userLog == "" {
		z.userLog = filepath.Join(z.cache, "Logs", z.started.Format("02-01-06_15-04-05")+"_user.txt")
		os.MkdirAll(filepath.Dir(z.userLog), 0o755)
	}
	f, err := os.OpenFile(z.userLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "[%s] %s.\n", time.Now().Format("02-01-06 15:04:05.000"), line)
}

func (z *zomboid) step(st Step) bool {
	switch st.Verb {
	case "join":
		if z.s.Join(st.Name) {
			z.user(fmt.Sprintf("%d %q fully connected (10583,9409,0)", zomboidAccount(st.Name), st.Name))
		}
	case "leave":
		if z.s.Leave(st.Name) {
			z.user(fmt.Sprintf("%d %q disconnected player (10583,9409,0)", zomboidAccount(st.Name), st.Name))
		}
	case "save":
		z.save()
	case "death", "deny", "count":
		// Nothing on the console: deaths, turned-away players and counts
		// show only in the server's own files and in RCON's "players".
	default:
		return false
	}
	return true
}
