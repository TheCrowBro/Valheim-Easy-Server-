// Package demo is the adapter of "Demo Game", the made-up game whose server
// is cmd/fakeserver's demo personality. It is complete enough to pass
// gametest.Conformance, so the framework, gametest and the manager can be
// tested with a game that needs nothing downloaded, and it shows a new
// game's adapter what each part of the contract asks for.
//
// It comes in four kinds, set by Game's fields, which between them use
// every way a stop chain can go: plain (Ctrl+C), Console (a "stop" line on
// its console, then Ctrl+C), API (an HTTP API that the probe asks who is
// on and the stop asks to save and stop, then Ctrl+C) and RCON (a "stop"
// command over RCON, then Ctrl+C).
//
// It doesn't register itself: a test or the manager's tests pass a Game
// where they need one.
package demo

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/install"
)

// Game is Demo Game.
type Game struct {
	// ID is the game's id; "demo" when empty. Tests running several kinds
	// at once give each its own.
	ID string
	// Console stops it with a "stop" line typed on its console, then
	// Ctrl+C (after a takeover only Ctrl+C is left).
	Console bool
	// API gives it an HTTP API on a "rest" port: the probe asks it who is
	// on, and the stop asks it to save and stop before trying Ctrl+C.
	API bool
	// RCON gives it RCON on an "rcon" port, with a new password every run
	// kept in the run record (secret "rcon"); the stop sends "stop" over
	// it before trying Ctrl+C. It needs internal/rcon (stream RC).
	RCON bool
	// Real is a built cmd/fakeserver that the "real" installer copies
	// alone, without fakeserver.json, for testing gametest's Options.Real.
	Real string
}

// Info describes the game.
func (g Game) Info() game.Info {
	id := g.ID
	if id == "" {
		id = "demo"
	}
	in := game.Info{
		ID:          id,
		Name:        "Demo Game",
		Short:       "Demo",
		Letter:      "D",
		Color:       "#6b7280",
		Order:       900,
		Stage:       game.Hidden,
		OS:          map[string]game.Level{"windows": game.Runs, "darwin": game.Runs, "linux": game.Runs},
		DownloadMB:  10,
		DiskMB:      20,
		MemoryMB:    64,
		MaxPlayers:  8,
		FriendsNeed: "Nothing: Demo Game is made up, for testing the app",
		Presence:    game.ByName,
		Deaths:      true,
		Ports: []game.PortRole{
			{Role: "game", Label: "Game port", Protos: []string{"udp", "tcp"}, Default: 7100, Public: true},
		},
		Password: game.PasswordRule{Use: "optional", Min: 4, Max: 32, Help: "Friends type it when they join. Leave it empty for no password."},
	}
	if g.API {
		in.Ports = append(in.Ports, game.PortRole{Role: "rest", Label: "API port", Protos: []string{"tcp"}, Default: 7101, Tie: "game+1"})
	}
	if g.RCON {
		in.Ports = append(in.Ports, game.PortRole{Role: "rcon", Label: "RCON port", Protos: []string{"tcp"}, Default: 7102, Tie: "game+2"})
	}
	return in
}

// New makes the adapter for one instance.
func (g Game) New(env game.Env) game.Server {
	s := &server{g: g, env: env, store: env.Store}
	if s.store == nil {
		s.store = &memStore{}
	}
	s.worlds = &worldList{dir: env.Dirs.Saves, backups: env.Dirs.Backups}
	if g.API {
		return apiServer{s} // only this kind has a probe (game.Prober)
	}
	return s
}

// apiServer is the API kind, which adds the probe.
type apiServer struct{ *server }

type server struct {
	g      Game
	env    game.Env
	store  game.Store
	worlds *worldList
}

// Program is the server's program, laid out the same on every system.
func (s *server) Program() string {
	name := "demoserver"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(s.env.Dirs.Server, name)
}

// dataFile is the game's data file, which Plan checks is there (the fake
// install makes it as a placeholder).
func (s *server) dataFile() string { return filepath.Join(s.env.Dirs.Server, "demo.pak") }

func (s *server) Store() game.Store { return s.store }

func (s *server) Installer() game.Installer {
	if s.env.Fake != nil {
		return install.Fake(s.env.Fake, s.Program(), []string{s.dataFile()})
	}
	return realInstaller{s}
}

// realInstaller stands in for a real download: it copies Game.Real alone.
type realInstaller struct{ s *server }

func (r realInstaller) Install(ctx context.Context, repair bool, progress func(game.Progress)) error {
	if r.s.g.Real == "" {
		return errors.New("Demo Game has no real server to download (set demo.Game.Real)")
	}
	progress(game.Progress{Step: "Downloading the Demo Game server", Percent: -1})
	b, err := os.ReadFile(r.s.g.Real)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(r.s.env.Dirs.Server, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(r.s.Program(), b, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(r.s.dataFile(), nil, 0o644); err != nil {
		return err
	}
	bj, _ := json.Marshal(game.Build{Version: "1.4.2", Date: time.Now().UTC().Truncate(time.Second)})
	return os.WriteFile(filepath.Join(r.s.env.Dirs.Server, ".egs-build.json"), bj, 0o644)
}

// Build reads .egs-build.json, the file install.WriteBuild writes and
// install.Fake fills in. It's read here directly so the demo needs
// nothing of the install package but Fake.
func (s *server) Build() (game.Build, bool) {
	var b game.Build
	data, err := os.ReadFile(filepath.Join(s.env.Dirs.Server, ".egs-build.json"))
	if err != nil || json.Unmarshal(data, &b) != nil || (b.Version == "" && b.Build == "") {
		return game.Build{}, false
	}
	return b, true
}

func (s *server) Schema() game.Schema {
	return game.Schema{
		{Key: "serverName", Scope: game.ServerScope, Kind: "text", Label: "Server name", Default: "Demo server", Max: 64, Restart: true, Add: true},
		{Key: "password", Scope: game.ServerScope, Kind: "password", Label: "Password", Help: "Friends type it when they join.", Max: 32, Restart: true, Add: true},
		{Key: "maxPlayers", Scope: game.ServerScope, Kind: "number", Label: "Most players at once", Default: "8", Min: 1, Max: 64, Restart: true},
		{Key: "autosave", Scope: game.ServerScope, Kind: "number", Label: "Save every (minutes)", Default: "10", Min: 1, Max: 60, Restart: true, Advanced: true},
		{Key: "motd", Scope: game.ServerScope, Kind: "text", Label: "Welcome message", Max: 100},
		{Key: "difficulty", Scope: game.WorldScope, Kind: "choice", Label: "Difficulty", Default: "normal", Restart: true,
			Choices: []game.Choice{{Value: "easy", Label: "Easy"}, {Value: "normal", Label: "Normal"}, {Value: "hard", Label: "Hard"}}},
		{Key: "seed", Scope: game.NewWorldScope, Kind: "text", Label: "Seed", Help: "Leave it empty for a random world.", Max: 20, Advanced: true},
	}
}

func (s *server) Validate(sv game.Saved, world string) game.FieldErrors {
	errs := game.FieldErrors{}
	name := strings.TrimSpace(sv.Server["serverName"])
	switch {
	case name == "":
		errs["serverName"] = "Give the server a name."
	case len([]rune(name)) > 64:
		errs["serverName"] = "The server name can be at most 64 characters."
	}
	if pw := sv.Server["password"]; pw != "" && (len(pw) < 4 || len(pw) > 32) {
		errs["password"] = "The password must be 4 to 32 characters."
	}
	for _, f := range []struct {
		key      string
		min, max int
		msg      string
	}{{"maxPlayers", 1, 64, "Most players must be 1 to 64."}, {"autosave", 1, 60, "Save every 1 to 60 minutes."}} {
		if v, ok := sv.Server[f.key]; ok {
			if n, err := strconv.Atoi(v); err != nil || n < f.min || n > f.max {
				errs[f.key] = f.msg
			}
		}
	}
	if d := sv.Worlds[world]["difficulty"]; d != "" && !slices.Contains([]string{"easy", "normal", "hard"}, d) {
		errs["difficulty"] = "Choose easy, normal or hard."
	}
	if p := s.worlds.NameProblem(world); p != "" {
		errs["world"] = p
	}
	return errs
}

// args is the command line for a world, and what it depends on.
func (s *server) args(sv game.Saved, world string) []string {
	srv, w := sv.Server, sv.Worlds[world]
	a := []string{"-port", strconv.Itoa(sv.Ports["game"]), "-world", world, "-saves", s.env.Dirs.Saves,
		"-name", srv["serverName"], "-maxplayers", strconv.Itoa(srv.Int("maxPlayers", 8)),
		"-autosave", strconv.Itoa(srv.Int("autosave", 10)), "-difficulty", cmp(w["difficulty"], "normal")}
	if pw := srv["password"]; pw != "" {
		a = append(a, "-password", pw)
	}
	if s.g.API {
		a = append(a, "-api", strconv.Itoa(sv.Ports["rest"]))
	}
	if s.g.RCON {
		a = append(a, "-rcon", strconv.Itoa(sv.Ports["rcon"]))
	}
	return a
}

func cmp(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// Fingerprint covers the command line (without the per-run passwords),
// so changing any setting the server reads at start says "restart to
// apply"; the welcome message is read live, so it's left out.
func (s *server) Fingerprint(sv game.Saved, world string) string {
	h := sha256.New()
	for _, a := range s.args(sv, world) {
		io.WriteString(h, a+"\x00")
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func (s *server) Plan(ctx context.Context, req game.PlanRequest) (game.Plan, error) {
	if _, err := os.Stat(s.dataFile()); err != nil {
		return game.Plan{}, errors.New("the Demo Game server's files are incomplete; repair the install")
	}
	if errs := s.Validate(req.Saved, req.World); len(errs) > 0 {
		return game.Plan{}, errs
	}
	args := s.args(req.Saved, req.World)
	if req.New {
		if seed := req.NewWorld["seed"]; seed != "" {
			args = append(args, "-seed", seed)
		}
	}
	rec := game.Record{Server: req.Saved.Server["serverName"], Port: req.Saved.Ports["game"], Password: req.Saved.Server["password"],
		Secrets: map[string]string{}}
	// A new API or RCON password every run, kept in the run record so a
	// copy of the app that takes over can still probe and stop it (spec 2.5).
	if s.g.API {
		rec.Secrets["rest"] = rand.Text()
		args = append(args, "-apipassword", rec.Secrets["rest"])
	}
	if s.g.RCON {
		rec.Secrets["rcon"] = rand.Text()
		args = append(args, "-rconpassword", rec.Secrets["rcon"])
	}
	return game.Plan{
		Exe:         s.Program(),
		Args:        args,
		Dir:         s.env.Dirs.Server,
		Output:      game.Capture,
		LogPath:     req.LogPath,
		Console:     s.g.Console,
		SaveLine:    consoleSave(s.g.Console),
		ReadyWait:   2 * time.Minute,
		Fingerprint: s.Fingerprint(req.Saved, req.World),
		Record:      rec,
	}, nil
}

func consoleSave(console bool) string {
	if console {
		return "save"
	}
	return ""
}

func (s *server) Watch(rec game.Record, known func(name string) string) game.Monitor {
	return &monitor{known: known, state: game.RunState{Count: 0}}
}

func (s *server) Stop(rec game.Record) []game.StopStep {
	ctrlC := game.StopStep{Kind: "interrupt", Wait: 30 * time.Second, Resend: 10 * time.Second}
	switch {
	case s.g.Console:
		ctrlC.Say = "Saving the world and shutting down… (Demo Game also saves on Ctrl+C, so nothing is lost after a takeover.)"
		return []game.StopStep{{Kind: "console", Line: "stop", Wait: 30 * time.Second}, ctrlC}
	case s.g.API:
		return []game.StopStep{{Kind: "call", Call: func(ctx context.Context) error {
			_, err := s.call(ctx, rec, http.MethodPost, "/api/stop")
			return err
		}, Wait: 30 * time.Second}, ctrlC}
	case s.g.RCON:
		// The RCON port and password come from the run record (role and
		// secret "rcon"), which the app reads when it sends this step.
		return []game.StopStep{{Kind: "rcon", Line: "stop", Wait: 30 * time.Second}, ctrlC}
	}
	return []game.StopStep{ctrlC}
}

// call asks the server's API, with the password the run started with.
func (s *server) call(ctx context.Context, rec game.Record, method, path string) ([]byte, error) {
	port := rec.Ports["rest"]
	if port == 0 {
		return nil, errors.New("the run has no API port")
	}
	req, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth("admin", rec.Secrets["rest"])
	client := s.env.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err == nil && resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("the Demo Game API answered %s", resp.Status)
	}
	return b, err
}

// Probe asks the API who is on.
func (s apiServer) Probe(ctx context.Context, rec game.Record) (game.ProbeResult, error) {
	b, err := s.call(ctx, rec, http.MethodGet, "/api/status")
	if err != nil {
		return game.ProbeResult{Count: -1}, err
	}
	var st struct {
		Online  bool     `json:"online"`
		Version string   `json:"version"`
		Players []string `json:"players"`
		Count   int      `json:"count"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return game.ProbeResult{Count: -1}, err
	}
	return game.ProbeResult{Up: st.Online, Count: st.Count, Names: st.Players, Version: st.Version}, nil
}

func (s *server) Join(in game.JoinInput) game.Join {
	port := in.Saved.Ports["game"]
	j := game.Join{How: "address", Port: port, Password: in.Saved.Server["password"], Router: true, Public: true}
	for _, ip := range in.LAN {
		j.LAN = append(j.LAN, fmt.Sprintf("%s:%d", ip, port))
	}
	if in.PublicIP != "" {
		j.Address = fmt.Sprintf("%s:%d", in.PublicIP, port)
	}
	j.Steps = []string{"Open Demo Game and choose Join.", "Type the address " + cmp(j.Address, "of this computer") + "."}
	if j.Password != "" {
		j.Steps = append(j.Steps, "Type the password when it asks.")
	}
	j.Hint = "Join Demo Game at " + cmp(j.Address, "this computer's address")
	if in.Router["game"] == "failed" {
		j.Problem = "Friends outside your home can't reach the server until the router forwards its port."
	}
	return j
}

func (s *server) Worlds() game.Worlds { return s.worlds }

func (s *server) Texts() game.Texts { return game.Texts{Players: "players", Player: "player"} }

func (s *server) ExitHint(tail []string) string {
	for _, l := range slices.Backward(tail) {
		if strings.Contains(l, "Error: can't open") {
			return "Another program is using the server's port."
		}
	}
	return ""
}

// memStore keeps the settings in memory when the instance has no Store.
type memStore struct {
	mu sync.Mutex
	s  game.Saved
}

func (m *memStore) Get() game.Saved {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.s.Clone()
}

func (m *memStore) Save(change func(*game.Saved) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.s.Clone()
	if err := change(&c); err != nil {
		return err
	}
	m.s = c
	return nil
}
