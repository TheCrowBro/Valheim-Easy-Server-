package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"valheim-easy-server/internal/a2s"
)

// The demo personality is a made-up game, "Demo Game", whose server is
// "demoserver". It exists so the framework, gametest and the manager can be
// tested with a game no real server has to match, and it is the example a
// new game's personality copies. internal/game/gametest/demo is its
// adapter.
//
// Its command line:
//
//	demoserver -port 7100 -world NAME -saves DIR [-name TEXT] [-password TEXT]
//	           [-maxplayers N] [-autosave MINUTES] [-difficulty easy|normal|hard]
//	           [-seed TEXT] [-log FILE]
//	           [-api PORT -apipassword TEXT]    an HTTP JSON API (user "admin")
//	           [-rcon PORT -rconpassword TEXT]  RCON: list, save, stop
//	           [-query PORT]                    Steam's A2S_INFO
//
// It writes its log on standard output (or to -log), each line starting
// with "[2006-01-02 15:04:05] ". A world is a folder in -saves holding
// world.json and region.dat; every save rewrites both, so a save changes
// the world's files. Its console (standard input) takes "save", "stop",
// "list" and "say TEXT"; Ctrl+C saves and stops too.

func init() {
	register(&Personality{
		ID:      "demo",
		Version: "1.4.2",
		MemMB:   16,
		Match:   func(c Call) bool { return c.Program == "demoserver" },
		Run:     runDemo,
	})
}

// demoWorld is world.json.
type demoWorld struct {
	Name       string    `json:"name"`
	Seed       string    `json:"seed"`
	Difficulty string    `json:"difficulty"`
	Created    time.Time `json:"created"`
	Saved      time.Time `json:"saved"`
	Saves      int       `json:"saves"`
}

type demo struct {
	s        *Server
	dir      string // the world's folder
	world    demoWorld
	port     int
	max      int
	name     string
	stopping bool
}

func runDemo(s *Server) {
	a := s.Args()
	d := &demo{s: s, port: a.Int("port", 7100), max: a.Int("maxplayers", 8), name: a.String("name", "Demo server")}
	s.LogTo(a.String("log", ""))
	s.OnCrash(func() { d.log("Fatal error: the demo server crashed") })
	d.log("Demo Server version " + s.Version())

	saves := a.String("saves", ".")
	worldName := a.String("world", "Demo")
	d.dir = filepath.Join(saves, worldName)
	if b, err := os.ReadFile(filepath.Join(d.dir, "world.json")); err == nil && json.Unmarshal(b, &d.world) == nil {
		d.log(fmt.Sprintf("Loading world %q", worldName))
	} else {
		seed := a.String("seed", "")
		if seed == "" {
			seed = fmt.Sprintf("%08d", rand.IntN(100000000))
		}
		d.world = demoWorld{Name: worldName, Seed: seed, Difficulty: a.String("difficulty", "normal"), Created: time.Now().UTC()}
		d.log(fmt.Sprintf("Creating world %q (seed %s, difficulty %s)", worldName, seed, d.world.Difficulty))
		d.write()
	}
	s.Sleep(2 * time.Second) // loading

	d.listen("port", d.port, s.ListenTCP)
	d.listen("port", d.port, s.ListenUDP)
	if p := a.Int("api", 0); p > 0 {
		d.listen("API port", p, func(port int) error { return s.HTTP(port, false, d.api(a.String("apipassword", ""))) })
	}
	if p := a.Int("rcon", 0); p > 0 {
		d.listen("RCON port", p, func(port int) error { return s.RCON(port, a.String("rconpassword", ""), d.rcon) })
	}
	if p := a.Int("query", 0); p > 0 {
		d.listen("query port", p, func(port int) error { return s.A2S(port, true, d.info) })
	}

	s.OnCommand(d.command)
	s.OnStop(func() {
		d.log("Ctrl+C received: stopping")
		d.stop()
	})
	s.OnStep(d.step)
	s.Every(time.Duration(a.Int("autosave", 10))*time.Minute, d.save)
	s.Ready(fmt.Sprintf("Server ready: players can join %q on port %d", d.name, d.port))
}

func (d *demo) log(msg string) {
	d.s.Log("[" + time.Now().Format("2006-01-02 15:04:05") + "] " + msg)
}

// listen opens a port or stops the server the way a real one does when
// another program has it.
func (d *demo) listen(what string, port int, open func(int) error) {
	if err := open(port); err != nil {
		d.log(fmt.Sprintf("Error: can't open %s %d: %v", what, port, err))
		d.s.Exit(1)
	}
}

// write saves the world's files.
func (d *demo) write() {
	os.MkdirAll(d.dir, 0o755)
	b, _ := json.MarshalIndent(d.world, "", "  ")
	os.WriteFile(filepath.Join(d.dir, "world.json"), b, 0o644)
	// The world grows a little with every save, as a played world does.
	os.WriteFile(filepath.Join(d.dir, "region.dat"), bytes.Repeat([]byte{byte(d.world.Saves)}, 1000+100*d.world.Saves), 0o644)
}

func (d *demo) save() {
	d.log("Saving the world...")
	d.s.Sleep(500 * time.Millisecond)
	d.world.Saves++
	d.world.Saved = time.Now().UTC()
	d.write()
	d.log("World saved")
}

// stop saves and exits; a second request while stopping changes nothing.
func (d *demo) stop() {
	if d.stopping {
		return
	}
	d.stopping = true
	d.log("Stopping the server")
	d.save()
	d.log("Server stopped")
	d.s.Exit(0)
}

func (d *demo) countLine() string {
	p := d.s.Players()
	line := fmt.Sprintf("Players online: %d", len(p))
	if len(p) > 0 {
		line += " (" + strings.Join(p, ", ") + ")"
	}
	return line
}

func (d *demo) step(st Step) bool {
	switch st.Verb {
	case "join":
		if len(d.s.Players()) >= d.max {
			d.log("Turned away " + st.Name + ": the server is full")
		} else if d.s.Join(st.Name) {
			d.log("Player joined: " + st.Name)
		}
	case "leave":
		if d.s.Leave(st.Name) {
			d.log("Player left: " + st.Name)
		}
	case "death":
		d.log("Player died: " + st.Name)
	case "deny":
		d.log("Turned away " + st.Name + ": wrong password")
	case "save":
		d.save()
	case "count":
		d.log(d.countLine())
	default:
		return false
	}
	return true
}

func (d *demo) command(line string) {
	cmd, rest, _ := strings.Cut(line, " ")
	switch strings.ToLower(cmd) {
	case "":
	case "save":
		d.save()
	case "stop", "quit", "exit":
		d.stop()
	case "list", "players":
		d.log(d.countLine())
	case "say":
		d.log("[Server] " + rest)
	default:
		d.log("Unknown command: " + cmd)
	}
}

// api answers the HTTP JSON API, for the adapter's probe and stop.
func (d *demo) api(password string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if user, pw, ok := r.BasicAuth(); !ok || user != "admin" || pw != password {
			w.Header().Set("WWW-Authenticate", `Basic realm="demo"`)
			WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "wrong password"})
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/status":
			p := d.s.Players()
			WriteJSON(w, http.StatusOK, map[string]any{"online": true, "version": d.s.Version(), "world": d.world.Name,
				"players": append([]string{}, p...), "count": len(p), "maxPlayers": d.max})
		case r.Method == http.MethodPost && r.URL.Path == "/api/save":
			d.save()
			WriteJSON(w, http.StatusOK, map[string]bool{"saved": true})
		case r.Method == http.MethodPost && r.URL.Path == "/api/stop":
			WriteJSON(w, http.StatusOK, map[string]bool{"stopping": true})
			d.s.post(d.stop) // after the answer has gone out
		default:
			WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		}
	}
}

// rcon answers RCON commands.
func (d *demo) rcon(cmd string) string {
	switch strings.ToLower(strings.TrimSpace(cmd)) {
	case "list":
		p := d.s.Players()
		return fmt.Sprintf("There are %d of a max of %d players online: %s", len(p), d.max, strings.Join(p, ", "))
	case "save":
		d.save()
		return "Saved the world"
	case "stop":
		d.s.post(d.stop)
		return "Stopping the server"
	}
	return "Unknown command: " + cmd
}

// info answers A2S_INFO.
func (d *demo) info() a2s.Info {
	return a2s.Info{Name: d.name, Map: d.world.Name, Folder: "demo", Game: "Demo Game",
		Players: len(d.s.Players()), MaxPlayers: d.max, Version: d.s.Version()}
}
