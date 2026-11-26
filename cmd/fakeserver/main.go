// Command fakeserver stands in for the dedicated servers of every game
// except Valheim (which keeps cmd/fakevalheim), for tests and for trying the
// app without downloading the real servers (spec 7.1).
//
// It is one program with a personality per game. The personality is chosen
// from the command line it is started with, the way the real server would
// be started (for example "java -jar server.jar nogui" is Minecraft's), or
// by "game" in its fakeserver.json. Everything else about one copy (its
// script of players joining and leaving, how fast its clocks run, the
// version it reports, the memory it holds, whether it crashes) comes from
// fakeserver.json, found beside the program or in one of the three folders
// above it. Nothing comes from the environment, so tests can run several
// copies at once, each with its own script, and an app under test can't
// leak its settings into a game it starts.
//
// install.Fake puts a copy of this program where the game's real server
// would be and writes its fakeserver.json:
//
//	{"game": "minecraft", "script": "join:Alex@1,death:Alex@5,deny:Bob@6,leave:Alex@9",
//	 "speed": 60, "version": "26.3", "memMB": 40, "crashAfter": 0, "noReady": false}
//
// The framework (this file and fw_*.go) gives every personality what a real
// server does: its command line (fw_args.go), a log on standard output or in
// a file (fw_log.go), the script runner (fw_script.go), memory ballast,
// listeners on its ports so port checks see them in use, RCON, A2S and
// HTTP(S) JSON responders (fw_net.go), a console read from standard input,
// and Ctrl+C handling (fw_server.go). Each game's personality is a file of
// its own, <id>.go, that registers itself from init(); demo.go is the
// example to copy.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Personality is one game's stand-in.
type Personality struct {
	// ID is the game's id ("minecraft"), which fakeserver.json's "game"
	// names to choose this personality.
	ID string
	// Version is what the server says it is unless fakeserver.json says
	// otherwise.
	Version string
	// MemMB is the memory it holds while it runs unless fakeserver.json
	// says otherwise, so the app's memory figures have something to show.
	MemMB int
	// Match says whether a command line starts this game's server. It is
	// asked only when fakeserver.json doesn't name the game, and must be
	// specific enough that no other personality's command line matches
	// too (several Java games all run "java"; their jar or main class
	// tells them apart).
	Match func(c Call) bool
	// Run is the server starting up: read the arguments, open the log and
	// the ports, load or make the world, set the handlers, and call
	// s.Ready once players could join. It runs on the server's own
	// goroutine (see Server) and must return once the server is up; the
	// program then keeps running until a handler calls s.Exit.
	Run func(s *Server)
}

// Call is how the program was started, for Personality.Match.
type Call struct {
	Program string // the program's file name in lower case, without ".exe": "java", "terrariaserver"
	Args    Args   // its arguments
	Dir     string // the folder the program is in
}

// personalities holds every registered personality by id.
var personalities = map[string]*Personality{}

// register adds a personality. It is called from init() in each
// personality's file, so a mistake stops every run at once rather than
// surfacing as the wrong game answering.
func register(p *Personality) {
	switch {
	case p == nil || p.ID == "" || p.Run == nil:
		panic("fakeserver: a personality needs an ID and a Run function")
	case personalities[p.ID] != nil:
		panic("fakeserver: personality " + p.ID + " registered twice")
	}
	personalities[p.ID] = p
}

// choose finds the personality for this run: the one fakeserver.json
// names, or else the only one that recognises the command line.
func choose(cfg Config, c Call) (*Personality, error) {
	ids := slices.Sorted(func(yield func(string) bool) {
		for id := range personalities {
			if !yield(id) {
				return
			}
		}
	})
	if cfg.Game != "" {
		if p := personalities[cfg.Game]; p != nil {
			return p, nil
		}
		return nil, fmt.Errorf("fakeserver.json names the game %q, which this stand-in doesn't know (it knows %s)", cfg.Game, strings.Join(ids, ", "))
	}
	var found []*Personality
	for _, id := range ids {
		if p := personalities[id]; p.Match != nil && p.Match(c) {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return nil, fmt.Errorf("no game's stand-in recognises the command line %q %q; name the game in fakeserver.json (\"game\": \"<id>\")", c.Program, c.Args.List())
	default:
		names := make([]string, len(found))
		for i, p := range found {
			names[i] = p.ID
		}
		return nil, fmt.Errorf("the command line %q %q could start %s; name the game in fakeserver.json (\"game\": \"<id>\")", c.Program, c.Args.List(), strings.Join(names, " or "))
	}
}

// programName is the program's own file name as Match sees it.
func programName(exe string) string {
	name := strings.ToLower(filepath.Base(exe))
	return strings.TrimSuffix(name, ".exe")
}

func main() {
	exe, err := os.Executable()
	if err != nil {
		fail("can't find this program's own path: %v", err)
	}
	cfg, err := loadConfig(filepath.Dir(exe))
	if err != nil {
		fail("%v", err)
	}
	c := Call{Program: programName(exe), Args: Args{list: os.Args[1:]}, Dir: filepath.Dir(exe)}
	p, err := choose(cfg, c)
	if err != nil {
		fail("%v", err)
	}
	newServer(p, cfg, c).run()
}

// fail stops a stand-in that can't start, saying why on standard error
// (which the app captures in the game's log). Exit code 2, like a real
// server given a command line it can't use.
func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "fakeserver: "+format+"\n", args...)
	os.Exit(2)
}
