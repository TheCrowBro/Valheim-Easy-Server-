package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// The terraria personality stands in for Terraria's vanilla dedicated
// server (internal/games/terraria), as far as the app can see it. Every
// line it prints is the real 1.4.6.1 server's, copied from the logs in
// internal/games/terraria/testdata/real (FACTS.md there says how they were
// made), including the byte order marks before the first line and the
// ": " prompt that starts the line after the server waits for a command.
//
// Its command line is the one the adapter gives the real server:
//
//	TerrariaServer -config <serverconfig.txt>
//
// and it reads from that file what the real server does: world (the .wld
// to load), autocreate/worldname/difficulty/seed (to make it when it's
// missing), port, maxplayers and password. It listens on the port with TCP
// and UDP; when the TCP port is taken it says "Listening on port N" and
// quits with code 0 without a word, as the real one does.
//
// Its console takes "save" (saves, with the real progress lines), "exit"
// ("Saving before exit...", saves silently, exits 0), "exit-nosave",
// "playing", "version", "port" and "help". Ctrl+C ends it at once without
// saving, as the real one does. While players are on it autosaves every
// 10 minutes of its clock, and it saves when the last player leaves.
//
// Script verbs: join and leave (with the "is connecting..." line before a
// join), deny (a wrong password), badversion (an older client), save, and
// count (the "playing" answer). The real server prints nothing when a
// player dies, so neither does this one.

func init() {
	register(&Personality{
		ID:      "terraria",
		Version: "1.4.6.1",
		MemMB:   24,
		Match: func(c Call) bool {
			return strings.HasPrefix(c.Program, "terrariaserver") && c.Args.Has("config")
		},
		Run: runTerraria,
	})
}

type terraria struct {
	s        *Server
	world    string // the .wld file
	name     string // the world's own name, kept inside the file
	port     int
	max      int
	saves    int  // how many times the world was saved, so every save changes the file
	prompt   bool // the server is waiting for a command: the next line starts with ": "
	conf     map[string]string
	addrs    map[string]string // each player's made-up address
	stopping bool
}

func runTerraria(s *Server) {
	t := &terraria{s: s, addrs: map[string]string{}}
	t.conf = readTerrariaConfig(s.Args().String("config", ""))
	t.world = t.conf["world"]
	t.port = atoiDefault(t.conf["port"], 7777)
	t.max = atoiDefault(t.conf["maxplayers"], 8)

	t.say("\uFEFF\uFEFF\uFEFF\uFEFFError Logging Enabled.")
	t.say("Terraria Server v" + s.Version())
	t.say("")
	if t.world == "" {
		// The real server would ask which world to run; the app always
		// says.
		t.say("Load failed!  No backup found.")
		t.say("")
		t.say("System.ArgumentException: no world was given")
		s.Exit(0)
	}
	if _, err := os.Stat(t.world); err == nil {
		t.load()
	} else if size := t.conf["autocreate"]; size != "" {
		t.create(size)
	} else {
		t.say("Load failed!  No backup found.")
		t.say("")
		t.say(fmt.Sprintf("System.IO.FileNotFoundException: Could not find file %q", t.world))
		s.Exit(0)
	}

	t.say("Terraria Server v" + s.Version())
	t.say("")
	t.say(fmt.Sprintf("Listening on port %d", t.port))
	t.say("Type 'help' for a list of commands.")
	t.say("")
	if err := s.ListenTCP(t.port); err != nil {
		// What the real server does when another program has its port.
		t.s.Log(": ")
		s.Exit(0)
	}
	// The UDP side of the same port is crossplay's; the real server goes
	// on without it when it is taken.
	s.ListenUDP(t.port)

	s.OnCommand(t.command)
	s.OnStop(func() {
		// Ctrl+C: the real server ends at once, saving nothing.
		code := 130
		if runtime.GOOS == "windows" {
			code = 0xC000013A // STATUS_CONTROL_C_EXIT
		}
		s.Exit(code)
	})
	s.OnStep(t.step)
	s.OnCrash(func() {
		t.say("")
		t.say("System.NullReferenceException: Object reference not set to an instance of an object")
	})
	s.Every(10*time.Minute, func() {
		if len(s.Players()) > 0 && !t.stopping {
			t.save(true)
		}
	})
	// The console waits for a command (": ") while the server finishes
	// starting, so its ready line follows the prompt.
	s.Ready(": Server started")
}

// say writes one line, after the prompt when the server was waiting for a
// command, as the real console does.
func (t *terraria) say(line string) {
	if t.prompt {
		line = ": " + line
		t.prompt = false
	}
	t.s.Log(line)
}

// create makes the world as autocreate does, with a few of the real
// progress lines (the real server prints thousands).
func (t *terraria) create(size string) {
	t.name = t.conf["worldname"]
	if t.name == "" {
		t.name = strings.TrimSuffix(filepath.Base(t.world), ".wld")
	}
	seed := t.conf["seed"]
	if seed == "" {
		seed = strconv.Itoa(rand.IntN(2147483647))
	}
	if len(seed) > 40 {
		seed = seed[:40] // as the real server does
	}
	width, height := 4200, 1200
	switch size {
	case "2":
		width, height = 6400, 1800
	case "3":
		width, height = 8400, 2400
	}
	t.say(fmt.Sprintf("Creating world - Seed: %s, Width: %d, Height: %d, Evil: -1, Difficulty: %s", seed, width, height, cmpDefault(t.conf["difficulty"], "0")))
	for _, p := range []int{1, 50, 100} {
		t.say(fmt.Sprintf("Resetting game objects %d%%", p))
	}
	for _, l := range []string{"0.0% - Generating world terrain - 9.0%", "0.4% - Adding sand - 0.0%",
		"50.0% - Settling liquids - 47.5%", "99.9% - Final clean up - 97.0%", "100.0% - Finalizing world - 100.0%"} {
		t.s.Sleep(2 * time.Second)
		t.say(l)
	}
	t.write(false)
}

// load reads the world back.
func (t *terraria) load() {
	t.name, t.saves = readTerrariaWorld(t.world)
	for _, p := range []int{1, 50, 99} {
		t.say(fmt.Sprintf("Resetting game objects %d%%", p))
	}
	for _, p := range []int{1, 50, 99} {
		t.s.Sleep(time.Second)
		t.say(fmt.Sprintf("Loading world data: %d%%", p))
	}
	for _, p := range []int{1, 99} {
		t.say(fmt.Sprintf("Settling liquids %d%%", p))
	}
}

// save writes the world. Out loud it prints the progress lines "save" and
// autosaves print; on the way out ("exit") the real server prints nothing.
func (t *terraria) save(outLoud bool) {
	if outLoud {
		for _, p := range []int{11, 45, 80, 100} {
			t.say(fmt.Sprintf("Saving world data: %d%%", p))
		}
		t.s.Sleep(time.Second)
		for _, p := range []int{26, 87} {
			t.say(fmt.Sprintf("Validating world save: %d%%", p))
		}
	}
	t.write(true)
	if outLoud {
		t.say("Backing up world file")
	}
}

// write saves the world file: a header like the real one (the release,
// "relogic", the world's name) and a body that grows with each save, as a
// played world does. The copy it replaces becomes .wld.bak, and that one
// .wld.bak2, as the real server keeps them.
func (t *terraria) write(backup bool) {
	if backup {
		if old, err := os.ReadFile(t.world); err == nil {
			if prev, err := os.ReadFile(t.world + ".bak"); err == nil {
				os.WriteFile(t.world+".bak2", prev, 0o644)
			}
			os.WriteFile(t.world+".bak", old, 0o644)
		}
		t.saves++
	}
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, int32(329))
	b.WriteString("relogic")
	b.WriteByte(2)
	binary.Write(&b, binary.LittleEndian, uint32(t.saves))
	b.WriteByte(byte(len(t.name)))
	b.WriteString(t.name)
	b.Write(bytes.Repeat([]byte{byte(t.saves)}, 1000+100*t.saves))
	os.MkdirAll(filepath.Dir(t.world), 0o755)
	if err := os.WriteFile(t.world, b.Bytes(), 0o644); err != nil {
		t.s.Warnf("can't write the world: %v", err)
	}
}

// readTerrariaWorld reads the name and save count back from write's header.
func readTerrariaWorld(path string) (name string, saves int) {
	b, err := os.ReadFile(path)
	if err != nil || len(b) < 17 || string(b[4:11]) != "relogic" {
		return strings.TrimSuffix(filepath.Base(path), ".wld"), 0
	}
	saves = int(binary.LittleEndian.Uint32(b[12:16]))
	n := int(b[16])
	if 17+n <= len(b) {
		name = string(b[17 : 17+n])
	}
	return name, saves
}

func (t *terraria) command(line string) {
	cmd, _, _ := strings.Cut(line, " ")
	switch strings.ToLower(cmd) {
	case "":
	case "save":
		t.save(true)
	case "exit":
		t.stop(true)
		return
	case "exit-nosave":
		t.stop(false)
		return
	case "playing":
		t.playing()
	case "version":
		t.say("Terraria Server v" + t.s.Version())
	case "port":
		t.say(fmt.Sprintf("Port: %d", t.port))
	case "help":
		t.say("Available commands:")
		t.say("")
		t.say("exit\t\tShutdown the server and save.")
		t.say("save\t\tSave the game world.")
	default:
		t.say("Invalid command.")
	}
	t.prompt = true
}

// stop ends the server as "exit" does: it says so, saves without a word
// and exits 0.
func (t *terraria) stop(save bool) {
	if t.stopping {
		return
	}
	t.stopping = true
	if save {
		t.say("Saving before exit...")
		t.s.Sleep(time.Second)
		t.write(true)
	}
	t.s.Exit(0)
}

func (t *terraria) playing() {
	p := t.s.Players()
	for _, name := range p {
		t.say(fmt.Sprintf("%s (%s)", name, t.addrs[name]))
	}
	switch len(p) {
	case 0:
		t.say("No players connected.")
	case 1:
		t.say("1 player connected.")
	default:
		t.say(fmt.Sprintf("%d players connected.", len(p)))
	}
}

// addr is a made-up address for a friend's connection, as the real server
// shows them.
func (t *terraria) addr() string {
	return fmt.Sprintf("127.0.0.1:%d", 40000+rand.IntN(20000))
}

func (t *terraria) step(st Step) bool {
	switch st.Verb {
	case "join":
		a := t.addr()
		t.say(a + " is connecting...")
		if len(t.s.Players()) >= t.max {
			t.say(a + " was booted: This server is full right now, please try again later.")
		} else if t.s.Join(st.Name) {
			t.addrs[st.Name] = a
			t.say(st.Name + " has joined.")
		}
	case "leave":
		if t.s.Leave(st.Name) {
			delete(t.addrs, st.Name)
			t.say(st.Name + " has left.")
			if len(t.s.Players()) == 0 {
				t.save(true) // the server saves when the last player leaves
			}
		}
	case "death":
		// The real server prints nothing.
	case "deny":
		a := t.addr()
		t.say(a + " is connecting...")
		t.say(a + " was booted: Incorrect password")
	case "badversion":
		a := t.addr()
		t.say(a + " is connecting...")
		t.say(a + " was booted: You are not using the same version as this server.")
	case "save":
		t.save(true)
	case "count":
		t.playing()
	default:
		return false
	}
	return true
}

// readTerrariaConfig reads serverconfig.txt the way the server does: one
// key=value a line, the value being everything after the first "=", lines
// starting with "#" ignored, keys in any case.
func readTerrariaConfig(path string) map[string]string {
	conf := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return conf
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			conf[strings.ToLower(strings.TrimSpace(k))] = v
		}
	}
	return conf
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}

func cmpDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
