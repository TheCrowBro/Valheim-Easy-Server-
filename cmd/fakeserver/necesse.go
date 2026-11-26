package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The necesse personality stands in for the Necesse dedicated server
// (internal/games/necesse), as the real 1.3.3 server behaves (that
// package's FACTS.md and testdata/real hold the real console). The app
// starts it as the server's own Java:
//
//	java -Xmx2048M -XX:… -jar Server.jar -nogui -datadir DIR -world NAME
//	     -port 14159 -slots 10 -password PW -motd TEXT -logging 0 -zipsaves 1 -language en
//
// Match knows it by "-jar Server.jar" (with that capital S, as Steam's
// download names it) together with "-datadir", which neither Minecraft
// ("-jar server.jar --nogui --universe …") nor Project Zomboid (a main
// class, no -jar) uses.
//
// Like the real server it writes coloured, time-stamped lines on standard
// output; keeps each world as <datadir>/saves/worlds/<name>.zip (an empty
// zip until the first save); listens on the UDP game port; saves on
// "save", and saves and exits on "stop", "exit" or "quit"; autosaves once a
// minute while players are on (or always with -pausewhenempty 0), leaving
// LATEST_BACKUP1.zip beside the worlds the first time; and on Ctrl+C exits
// at once without saving. With its port taken it says it started, then
// stops (code NETWORK_ERROR), as the real one does.

func init() {
	register(&Personality{
		ID:      "necesse",
		Version: "1.3.3",
		MemMB:   48,
		Match:   matchNecesse,
		Run:     runNecesse,
	})
}

func matchNecesse(c Call) bool {
	return c.Program == "java" && jarArg(c.Args) == "Server.jar" && c.Args.Has("datadir")
}

// jarArg is the argument after "-jar", exactly as given.
func jarArg(a Args) string {
	list := a.List()
	for i, arg := range list {
		if arg == "-jar" && i+1 < len(list) {
			return list[i+1]
		}
	}
	return ""
}

// necesse is one run of the stand-in.
type necesse struct {
	s         *Server
	worldName string
	worldPath string // <datadir>/saves/worlds/<name>.zip
	saves     int    // how often this world was saved, kept in its world.dat
	port      int
	slots     int
	pause     bool // pauses (and so doesn't autosave) while nobody is on
	backedUp  bool // the first autosave makes LATEST_BACKUP1.zip
	stopping  bool
	ticks     int
	accounts  map[string]int64
}

// Colour codes the real server starts each line with.
const (
	necColour = "\x1b[39m"
	necDebug  = "\x1b[34m"
	necError  = "\x1b[31m"
)

func (n *necesse) stamp() string { return "[" + time.Now().Format("2006-01-02 15:04:05") + "] " }

func (n *necesse) log(msg string)   { n.s.Log(necColour + n.stamp() + msg) }
func (n *necesse) debug(msg string) { n.s.Log(necDebug + n.stamp() + "(DEBUG) " + msg) }
func (n *necesse) err(msg string)   { n.s.Log(necError + n.stamp() + "(ERR) " + msg) }

// necValue is an option's value the way the server reads it: a value that
// starts with "-" is taken for the next option, so the option has none.
func necValue(a Args, name, def string) string {
	v := a.String(name, def)
	if strings.HasPrefix(v, "-") {
		return ""
	}
	return v
}

func runNecesse(s *Server) {
	a := s.Args()
	n := &necesse{s: s, port: a.Int("port", 14159), slots: a.Int("slots", 10),
		pause: a.String("pausewhenempty", "1") != "0", accounts: map[string]int64{}}
	data := a.String("datadir", ".")
	n.worldName = necValue(a, "world", "")
	s.OnCrash(func() { n.err("Stuck in crash loop, stopping server.") })
	if n.worldName == "" {
		// The real server asks on its console and waits; the app always
		// names the world, so the stand-in only says what went wrong.
		n.log("Type a name for the world to create")
		s.Warnf("no -world given")
		s.Exit(2)
	}
	n.debug("Started logging to: " + filepath.Join(data, "latest-server-log.txt"))
	n.log("Launched game with arguments: " + strings.Join(a.List(), " "))
	n.debug("Initializing DesktopPlatform")
	n.log("Loading dedicated server on version " + s.Version() + ".")
	s.Sleep(2 * time.Second)

	dir := filepath.Join(data, "saves", "worlds")
	n.worldPath = filepath.Join(dir, n.worldName+".zip")
	if saves, ok := readNecWorld(n.worldPath); ok {
		n.saves = saves
		n.log("Loading existing world at " + n.worldPath)
	} else {
		n.log("Creating new world at " + n.worldPath)
		n.log("Creating save with name: " + n.worldName + ".zip")
		n.log("Could not find world file, creating new one: " + n.worldName + ".zip")
		n.log("Could not find Level: " + n.worldName + "/levels/surface.dat file for " + n.worldName + ".zip")
		n.log("Finding spawn position...")
		n.log("This may take a moment")
		s.Sleep(5 * time.Second)
		n.log("Found spawn tile at: 2712, -136")
		// Until its first save a new world is an empty zip.
		os.MkdirAll(dir, 0o755)
		writeEmptyZip(n.worldPath)
	}

	// The real server finds a free port of its own for LAN discovery; the
	// stand-in only says so, so that tests running at once never clash.
	n.log("Started lan socket at port 55169")
	listenErr := s.ListenUDP(n.port)
	if listenErr != nil {
		n.err("java.net.BindException: Address already in use")
		n.err("\tat java.base/sun.nio.ch.Net.bind0(Native Method)")
		n.err("\tat necesse.engine.network.server.Server.start(Server.java:149)")
		n.log("Starting world save")
	}
	pw := necValue(a, "password", "")
	with := ""
	if pw != "" {
		with = fmt.Sprintf(" with password %q", pw)
	}
	n.log(fmt.Sprintf("Started server using port %d with %d slots on world %q%s, game version %s.", n.port, n.slots, n.worldName+".zip", with, s.Version()))
	n.log("Found 0 saved players.")
	n.log(fmt.Sprintf("Local address: 127.0.0.1:%d", n.port))
	if listenErr != nil {
		n.log("Type help for list of commands.")
		n.saveWorld()
		n.log("Completed world save before stopping server")
		n.stopped("NETWORK_ERROR")
		return
	}

	s.OnCommand(n.command)
	// Ctrl+C: Java ends at once, with nothing said and nothing saved.
	s.OnStop(func() { s.Exit(130) })
	s.OnStep(n.step)
	s.Every(time.Minute, n.autosave)
	s.Ready("Type help for list of commands.")
}

// readNecWorld reads how often a world was saved; false when there is no
// saved world (no file, or the empty zip of one never saved).
func readNecWorld(path string) (int, bool) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return 0, false
	}
	defer zr.Close()
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/world.dat") {
			rc, err := f.Open()
			if err != nil {
				return 0, false
			}
			b, _ := io.ReadAll(rc)
			rc.Close()
			first, _, _ := strings.Cut(string(b), "\n")
			saves, _ := strconv.Atoi(strings.TrimPrefix(first, "saves="))
			return saves, true
		}
	}
	return 0, false
}

func writeEmptyZip(path string) {
	var buf bytes.Buffer
	zip.NewWriter(&buf).Close()
	os.WriteFile(path, buf.Bytes(), 0o644)
}

// saveWorld writes the world's zip, a little bigger each time, as a
// played world grows. It writes a new file and renames it into place.
func (n *necesse) saveWorld() {
	n.saves++
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	put := func(name string, data []byte) {
		w, _ := zw.Create(n.worldName + "/" + name)
		w.Write(data)
	}
	put("world.dat", append([]byte(fmt.Sprintf("saves=%d\n", n.saves)), bytes.Repeat([]byte{byte(n.saves)}, 900+50*n.saves)...))
	put("worldSettings.cfg", []byte("WORLDSETTINGS = {\n\tdifficulty = CLASSIC,\n\tgameVersion = "+n.s.Version()+"\n}\n"))
	zw.Close()
	tmp := n.worldPath + ".tmp"
	os.MkdirAll(filepath.Dir(n.worldPath), 0o755)
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err == nil {
		os.Rename(tmp, n.worldPath)
	}
}

func (n *necesse) save() {
	n.log("Starting world save")
	n.log("Starting save...")
	n.s.Sleep(2 * time.Second)
	n.saveWorld()
	n.log("Completed world save")
}

// autosave runs every minute on the game's clock, unless the world is
// paused because nobody is on.
func (n *necesse) autosave() {
	n.ticks++
	if n.stopping || (n.pause && len(n.s.Players()) == 0) {
		return
	}
	if !n.backedUp {
		n.backedUp = true
		n.log("Running an auto save with backup...")
		n.log("Starting world save")
		n.saveWorld()
		n.log("Reloaded file system in 438.70 us")
		backup := filepath.Join(filepath.Dir(n.worldPath), "LATEST_BACKUP1.zip")
		n.log("Copying backup to path: " + backup)
		if b, err := os.ReadFile(n.worldPath); err == nil {
			os.WriteFile(backup, b, 0o644)
		}
		n.log("Finished backup")
		return
	}
	n.saveWorld()
	n.log("Auto save: Reloaded file system in 2.90 ms")
}

// stop saves and exits, as "stop", "exit" and "quit" do.
func (n *necesse) stop() {
	if n.stopping {
		return
	}
	n.stopping = true
	n.log("Starting world save")
	n.s.Sleep(2 * time.Second)
	n.saveWorld()
	n.log("Completed world save before stopping server")
	n.stopped("SERVER_STOPPED")
}

// stopped prints the server's last lines and exits.
func (n *necesse) stopped(code string) {
	n.log("Stopped server on " + time.Now().Format("2006/01/02 15:04:05") + " with code: " + code)
	n.log(fmt.Sprintf("World time: %d, day 0", 9+10*n.ticks))
	n.log(fmt.Sprintf("Game time: %d", 9992+10000*n.ticks))
	n.log(fmt.Sprintf("Total ticks: %d", 667+600*n.ticks))
	n.log("Received: 0 B (0 packets)")
	n.log("Sent: 0 B (0 packets)")
	n.log("Server has stopped")
	n.log("Exiting in 2 seconds...")
	n.s.Sleep(2 * time.Second)
	n.s.Exit(0)
}

// account is a made-up Steam account number for a name, the same every
// time.
func (n *necesse) account(name string) int64 {
	if a, ok := n.accounts[name]; ok {
		return a
	}
	h := fnv.New32a()
	h.Write([]byte(name))
	a := 76561198000000000 + int64(h.Sum32()%100000000)
	n.accounts[name] = a
	return a
}

func (n *necesse) step(st Step) bool {
	switch st.Verb {
	case "join":
		acc := n.account(st.Name)
		n.log(fmt.Sprintf("Client %q with address 127.0.0.1:%d is connecting with version %s.", st.Name, 50000+len(n.s.Players()), n.s.Version()))
		if len(n.s.Players()) >= n.slots || !n.s.Join(st.Name) {
			return true
		}
		n.log(fmt.Sprintf("Client %q connected on slot %d/%d.", st.Name, len(n.s.Players()), n.slots))
		n.log(fmt.Sprintf("Loaded player: %d", acc))
	case "leave":
		if n.s.Leave(st.Name) {
			n.log(fmt.Sprintf("Player %d (%q) disconnected with message: Quit", n.account(st.Name), st.Name))
		}
	case "death":
		// As a 2021 server printed it (FACTS.md).
		n.log(st.Name + " got humiliated by Zombie")
	case "deny":
		// Turned away (a wrong password): the server says the client is
		// connecting, then never gives it a slot.
		n.log(fmt.Sprintf("Client %q with address 127.0.0.1:50999 is connecting with version %s.", st.Name, n.s.Version()))
	case "save":
		n.save()
	case "count":
		n.log(fmt.Sprintf("Players online: %d/%d", len(n.s.Players()), n.slots))
	default:
		return false
	}
	return true
}

func (n *necesse) command(line string) {
	if line == "" {
		return
	}
	n.log("> " + line)
	cmd, _, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	switch strings.ToLower(cmd) {
	case "save":
		n.save()
	case "stop", "exit", "quit":
		n.stop()
	case "players":
		n.log(fmt.Sprintf("Players online: %d/%d", len(n.s.Players()), n.slots))
	case "help":
		n.log("Commands page 1 of 17 (Server):")
		n.log("allowcheats ")
		n.log("ban <authentication/name>")
	}
}
