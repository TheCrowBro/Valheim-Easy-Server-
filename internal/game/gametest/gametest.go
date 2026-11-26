// Package gametest runs one game adapter without the manager, against the
// stand-in server (cmd/fakeserver) or, with Options.Real, against the real
// server (spec 7.1). Every adapter's conformance test and real-server check
// go through it, so a game can be built and checked before the manager
// knows about it.
//
// A Session does what the manager does for one instance, in the same
// order (spec 2.4): it installs the server through the adapter's
// Installer, asks for a Plan, starts the program the way the app does
// (output to the run's log file, a console pipe for console games), writes
// the run record, follows the log through the adapter's Monitor, runs the
// probe, and stops the server with the adapter's stop chain. Takeover acts
// out an app restart. Conformance strings these together into the checks
// every adapter must pass.
package gametest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/rcon"
)

// Options say how to run the adapter.
type Options struct {
	Fake     string // the built fakeserver; "" with Real (or to have Start build it)
	Real     bool   // use the adapter's real Installer and Plan (the realserver-tagged tests)
	Script   string // the stand-in's players: "join:Alex@1,death:Alex@5,leave:Alex@9"
	World    string // the world to make and run; "" means "Conformance"
	NewWorld game.Values
	Change   func(*game.Saved) // adjust the settings before the install (Saved starts from the schema's defaults)
}

// fakeSpeed runs the stand-in's own clocks (loading, autosaves) this much
// faster, so a session takes seconds; script times stay real seconds.
const fakeSpeed = 60

// Session is one instance of the adapter, installed and started.
type Session struct {
	t     testing.TB
	g     game.Game
	info  game.Info
	o     Options
	env   game.Env
	store *memStore
	world string
	tlog  *testLog

	mu      sync.Mutex
	srv     game.Server
	run     *attachment
	known   map[string]string // the account each name last played from; read by the Monitor under mu
	req     game.PlanRequest  // the last start's
	plan    game.Plan
	stopLog []stepResult // what the last Stop did, step by step

	// Ports, under mu: picked holds the roles whose ports gametest chose
	// (not the ones Options.Change set) and claimed what pickPorts claimed
	// for this session; retries counts the starts tried again on new ports.
	picked    map[string]int
	claimed   map[string]int
	retries   int
	afterPick func(ports map[string]int) // tests: sees each new choice of ports before the server starts on it
}

// stepResult is one stop step as Stop ran it.
type stepResult struct {
	Kind string
	Err  error // what sending it gave (game.ErrNoConsole after a takeover)
}

// Start installs the game (the stand-in unless o.Real), plans a start and
// launches it, as the manager would.
func Start(t *testing.T, g game.Game, o Options) *Session {
	t.Helper()
	s := newSession(t, g, o)
	s.install()
	s.start()
	return s
}

func newSession(t testing.TB, g game.Game, o Options) *Session {
	t.Helper()
	info := g.Info()
	if o.World == "" {
		o.World = "Conformance"
	}
	if !o.Real && o.Fake == "" {
		o.Fake = BuildFakeserver(t)
	}
	data := t.TempDir()
	root := filepath.Join(data, "games", info.ID)
	dirs := game.Dirs{
		Root:     root,
		Server:   filepath.Join(root, "server"),
		Saves:    filepath.Join(root, "saves"),
		Backups:  filepath.Join(root, "backups"),
		Logs:     filepath.Join(root, "logs"),
		Runtime:  filepath.Join(root, "runtime"),
		Staging:  filepath.Join(root, "uploads"),
		SteamCMD: filepath.Join(data, "steamcmd"),
	}
	for _, d := range []string{dirs.Server, dirs.Saves, dirs.Backups, dirs.Logs, dirs.Runtime, dirs.Staging} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ports, err := pickPorts(info.Ports, o.Real)
	if err != nil {
		t.Fatalf("gametest: choosing ports for %s: %v", info.ID, err)
	}
	tl := &testLog{t: t}
	s := &Session{t: t, g: g, info: info, o: o, world: o.World, tlog: tl, known: map[string]string{}, claimed: ports, picked: map[string]int{}}
	s.store = &memStore{}
	s.env = game.Env{
		Instance: info.ID,
		Data:     data,
		Dirs:     dirs,
		Log:      log.New(tl, info.ID+": ", 0),
		HTTP:     &http.Client{Timeout: 5 * time.Minute},
		Store:    s.store,
	}
	if !o.Real {
		s.env.Fake = &game.Fake{Program: o.Fake, Script: o.Script, Speed: fakeSpeed}
	}
	s.srv = g.New(s.env)
	// What Add a game would save: each field's default, the chosen world
	// selected with its world-scope defaults, and the ports picked.
	saved := game.Saved{Server: game.Values{}, Worlds: map[string]game.Values{o.World: {}}, World: o.World, Ports: maps.Clone(ports), State: game.Values{}}
	for _, f := range s.srv.Schema() {
		if f.Default == "" {
			continue
		}
		switch f.Scope {
		case game.ServerScope:
			saved.Server[f.Key] = f.Default
		case game.WorldScope:
			saved.Worlds[o.World][f.Key] = f.Default
		}
	}
	if o.Change != nil {
		o.Change(&saved)
	}
	for role, p := range ports {
		if saved.Ports[role] == p {
			s.picked[role] = p
		}
	}
	s.store.Save(func(sv *game.Saved) error { *sv = saved; return nil })
	t.Cleanup(s.cleanup)
	return s
}

// install runs the adapter's installer and checks it left a program and a
// build behind.
func (s *Session) install() {
	s.t.Helper()
	ctx := s.t.Context()
	err := s.srv.Installer().Install(ctx, false, func(p game.Progress) {})
	if err != nil {
		s.t.Fatalf("gametest: installing %s: %v", s.info.ID, err)
	}
	if _, err := os.Stat(s.srv.Program()); err != nil {
		s.t.Fatalf("gametest: after the install, the server's program %s isn't there: %v", s.srv.Program(), err)
	}
	if _, ok := s.srv.Build(); !ok {
		s.t.Fatalf("gametest: after the install, Build() says nothing is installed")
	}
}

// start plans a start of the selected world and launches it: a new world
// when the adapter's Worlds says it doesn't exist yet.
func (s *Session) start() {
	s.t.Helper()
	s.startTry(1)
}

// startTry is start's try'th try (retryOnNewPorts makes the later ones).
func (s *Session) startTry(try int) {
	s.t.Helper()
	s.mu.Lock()
	if s.run != nil && !s.run.isExited() {
		s.mu.Unlock()
		s.t.Fatalf("gametest: %s is already running", s.info.ID)
	}
	srv := s.srv
	s.mu.Unlock()

	saved := s.store.Get()
	newWorld := game.Values{}
	for _, f := range srv.Schema() {
		if f.Scope == game.NewWorldScope && f.Default != "" {
			newWorld[f.Key] = f.Default
		}
	}
	for k, v := range s.o.NewWorld {
		newWorld[k] = v
	}
	req := game.PlanRequest{
		World:       s.world,
		New:         !srv.Worlds().Exists(s.world),
		NewWorld:    newWorld,
		Saved:       saved,
		LogPath:     s.logPath(),
		Step:        func(msg string, pct float64) { s.tlog.Printf("plan: %s", msg) },
		Cancellable: func(bool) {},
	}
	plan, err := srv.Plan(s.t.Context(), req)
	if err != nil {
		s.t.Fatalf("gametest: Plan for %s: %v", s.info.ID, err)
	}
	if plan.Exe == "" {
		s.t.Fatalf("gametest: Plan for %s gave no program to run", s.info.ID)
	}
	if plan.LogPath == "" {
		plan.LogPath = req.LogPath
	}

	// As the manager does (spec 2.4): Capture sends the program's output
	// to the run's log file; a console game also gets a pipe on standard
	// input whose write end the app keeps, and a copy of which the game
	// holds so it never sees its console end.
	var out, stdinRead, stdinWrite *os.File
	if plan.Output == game.Capture {
		os.MkdirAll(filepath.Dir(plan.LogPath), 0o755)
		if out, err = os.OpenFile(plan.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err != nil {
			s.t.Fatal(err)
		}
	}
	opts := platform.StartOptions{Exe: plan.Exe, Args: plan.Args, Dir: plan.Dir, Env: append(os.Environ(), plan.Env...), Stdout: out, Stderr: out}
	if plan.Console {
		if stdinRead, stdinWrite, err = os.Pipe(); err != nil {
			s.t.Fatal(err)
		}
		opts.Stdin, opts.Inherit = stdinRead, []*os.File{stdinWrite}
	}
	proc, err := launch(opts)
	// The program has its own copies of these now.
	if out != nil {
		out.Close()
	}
	if stdinRead != nil {
		stdinRead.Close()
	}
	if err != nil {
		if stdinWrite != nil {
			stdinWrite.Close()
		}
		s.t.Fatalf("gametest: starting %s (%s): %v", s.info.ID, plan.Exe, err)
	}

	rec := plan.Record
	rec.PID = proc.PID()
	rec.Started = proc.StartTime()
	rec.Launched = time.Now()
	rec.LogPath = plan.LogPath
	rec.World = s.world
	rec.ArgsKey = plan.Fingerprint
	rec.Exe = plan.Exe
	rec.Game, rec.Instance, rec.Name = s.info.ID, s.info.ID, s.info.Short
	if rec.Ports == nil {
		rec.Ports = saved.Ports
	}
	if rec.Played.Mods == nil {
		rec.Played.Mods = map[string]string{} // as non-legacy instances write it (spec 2.2)
	}
	if err := writeRecord(s.recordPath(), rec); err != nil {
		s.t.Fatal(err)
	}

	s.mu.Lock()
	s.req, s.plan = req, plan
	mon := srv.Watch(rec, s.knownLocked)
	s.mu.Unlock()
	s.attach(proc, rec, stdinWrite, mon, try)
}

// retryOnNewPorts starts the server again on new ports when its run ended
// before it ever came online because another program took a port
// gametest had chosen for it (stolenPort says when that is so), at most
// maxPortRetries times for one start, and says so in the test's log. It
// says whether it started it again.
//
// Choosing a free port and the server opening it can't be one step, so on
// a busy computer (CI runs many test programs at once) another program
// can take the port in between. Trying the whole start again on new
// ports, as the owner would after picking other ports, keeps that from
// failing a test, while a port the test chose itself, a port that stays
// taken, or a server that fails in any other way still fails it.
func (s *Session) retryOnNewPorts(a *attachment) bool {
	s.t.Helper()
	if a.try == 0 || a.try > maxPortRetries { // a takeover's run, or out of tries
		return false
	}
	why, ok := s.stolenPort(a)
	if !ok {
		return false
	}
	s.tlog.Printf("%s; starting it again on new ports (retry %d of %d)", why, a.try, maxPortRetries)
	a.detach() // closes the failed run's console pipe
	s.mu.Lock()
	req, srv := s.req, s.srv
	s.retries++
	s.mu.Unlock()
	// The failed run may have begun making the new world before it tried
	// the port; remove that, so the retry makes it again as asked.
	if wl := srv.Worlds(); req.New && wl.Exists(s.world) {
		if err := wl.Delete(s.world); err != nil {
			s.tlog.Printf("removing the world the failed start began: %v", err)
		}
	}
	if err := s.newPorts(); err != nil {
		s.t.Fatalf("gametest: choosing new ports for %s: %v", s.info.ID, err)
	}
	s.startTry(a.try + 1)
	return true
}

// logPath is a fresh log file for one run, named as the manager names them.
func (s *Session) logPath() string {
	base := filepath.Join(s.env.Dirs.Logs, s.info.ID+"-"+time.Now().Format("2006-01-02_15-04-05"))
	path := base + ".log"
	for i := 2; ; i++ {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return path
		}
		path = fmt.Sprintf("%s_%d.log", base, i)
	}
}

func (s *Session) recordPath() string { return filepath.Join(s.env.Dirs.Root, "run.json") }

// knownLocked answers the Monitor's "which account did this name last play
// from"; the Monitor calls it with s.mu held, so it must not lock.
func (s *Session) knownLocked(name string) string { return s.known[name] }

// writeRecord writes the run record the way the manager does (games/<id>/run.json, 0600).
func writeRecord(path string, rec game.Record) error {
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func readRecord(path string) (game.Record, error) {
	var rec game.Record
	b, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(b, &rec)
	}
	return rec, err
}

// WaitEvent waits for the Monitor to report an event of kind (for name,
// when name isn't "") and fails the test if none comes within the time.
// Each event is returned once: waiting twice for "join" "Alex" finds the
// second join. Events count from the latest start or takeover.
//
// When the server exits before it ever came online because another
// program took a port gametest chose for it, the start is tried again on
// new ports (retryOnNewPorts, which says so in the test's log), and the
// wait goes on with the new run, which gets the whole time again.
func (s *Session) WaitEvent(kind, name string, within time.Duration) game.Event {
	s.t.Helper()
	deadline := time.Now().Add(within)
	for {
		s.mu.Lock()
		a := s.run
		if a == nil {
			s.mu.Unlock()
			s.t.Fatalf("gametest: WaitEvent(%q, %q): nothing has been started", kind, name)
		}
		for i, e := range a.events {
			if !a.used[i] && e.Kind == kind && (name == "" || e.Name == name) {
				a.used[i] = true
				s.mu.Unlock()
				return e
			}
		}
		changed, exited, code := a.changed, a.isExited(), a.exitCode
		s.mu.Unlock()
		if exited {
			if s.retryOnNewPorts(a) {
				deadline = time.Now().Add(within) // the new run gets the whole wait
				continue
			}
			s.t.Fatalf("gametest: the %s server exited (code %d) without a %q event%s\n%s", s.info.ID, code, kind, forName(name), a.tail())
		}
		left := time.Until(deadline)
		if left <= 0 {
			s.t.Fatalf("gametest: no %q event%s from the %s server within %v\n%s", kind, forName(name), s.info.ID, within, a.tail())
		}
		select {
		case <-changed:
		case <-time.After(left):
		}
	}
}

func forName(name string) string {
	if name == "" {
		return ""
	}
	return " for " + name
}

// State is the Monitor's view of the running server.
func (s *Session) State() game.RunState {
	s.t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run == nil {
		s.t.Fatalf("gametest: State: nothing has been started")
	}
	return s.run.mon.State()
}

// Takeover acts as after an app restart: a new Monitor from the run record, no stdin pipe.
//
// The app that started the server is gone, and its end of the console pipe
// with it. A new copy of the app reads the run record from disk, opens the
// process by its PID, makes a new adapter from the game (g.New), asks it
// for a Monitor and replays the log from the start, as the manager's
// adoption does.
func (s *Session) Takeover() {
	s.t.Helper()
	s.mu.Lock()
	a := s.run
	s.mu.Unlock()
	if a == nil || a.isExited() {
		s.t.Fatalf("gametest: Takeover: the %s server isn't running", s.info.ID)
	}
	a.detach()
	rec, err := readRecord(s.recordPath())
	if err != nil {
		s.t.Fatalf("gametest: Takeover: reading the run record: %v", err)
	}
	proc, err := platform.OpenProc(rec.PID)
	if err != nil {
		s.t.Fatalf("gametest: Takeover: opening process %d from the run record: %v", rec.PID, err)
	}
	srv := s.g.New(s.env)
	s.mu.Lock()
	s.srv = srv
	mon := srv.Watch(rec, s.knownLocked)
	s.mu.Unlock()
	s.attach(proc, rec, nil, mon, 0)
}

// Stop runs the stop chain and returns when the world was last saved (the
// Monitor's LastSave; zero when the game never says).
//
// As the manager does (spec 2.7): each step is sent, then Stop waits up to
// the step's Wait for the program to exit, sending it again every Resend.
// A step that can't be sent (ErrNoConsole after a takeover, a refused
// connection) moves on to the next at once. When the last step's wait ends
// with the server still running, the test fails and the server is killed:
// the app never kills a server by itself.
func (s *Session) Stop() (saved time.Time) {
	s.t.Helper()
	s.mu.Lock()
	a, srv := s.run, s.srv
	s.stopLog = nil
	s.mu.Unlock()
	if a == nil || a.isExited() {
		s.t.Fatalf("gametest: Stop: the %s server isn't running", s.info.ID)
	}
	steps := srv.Stop(a.rec)
	if len(steps) == 0 {
		s.t.Fatalf("gametest: %s's Stop gave no steps", s.info.ID)
	}
	stopped := false
	for _, st := range steps {
		err := s.send(a, st)
		s.mu.Lock()
		s.stopLog = append(s.stopLog, stepResult{Kind: st.Kind, Err: err})
		s.mu.Unlock()
		if err != nil {
			s.tlog.Printf("stop step %q: %v; the next step runs at once", st.Kind, err)
			continue
		}
		if stopped = a.waitExit(st.Wait, st.Resend, func() { s.send(a, st) }); stopped {
			break
		}
	}
	if !stopped && !a.proc.Exited() {
		a.proc.Kill()
		<-a.exited
		s.t.Fatalf("gametest: %s's stop chain (%s) ended with the server still running; it was killed\n%s", s.info.ID, kinds(steps), a.tail())
	}
	<-a.exited
	s.mu.Lock()
	defer s.mu.Unlock()
	return a.mon.State().LastSave
}

func kinds(steps []game.StopStep) string {
	var k []string
	for _, st := range steps {
		k = append(k, st.Kind)
	}
	return strings.Join(k, " → ")
}

// send sends one stop step.
func (s *Session) send(a *attachment, st game.StopStep) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	switch st.Kind {
	case "console":
		return a.typeLine(st.Line)
	case "rcon":
		// The RCON port and password the run started with are in its
		// record (role "rcon", secret "rcon"), so this works after a
		// takeover too.
		port := a.rec.Ports["rcon"]
		if port == 0 {
			return errors.New(`the run record has no "rcon" port`)
		}
		_, err := rcon.Exec(ctx, fmt.Sprintf("127.0.0.1:%d", port), a.rec.Secrets["rcon"], st.Line)
		return err
	case "call":
		if st.Call == nil {
			return errors.New(`a "call" step without a Call`)
		}
		return st.Call(ctx)
	case "interrupt":
		quietCtrlC()
		return a.proc.Interrupt()
	}
	s.t.Errorf("gametest: %s's Stop has a step of unknown kind %q", s.info.ID, st.Kind)
	return fmt.Errorf("unknown kind %q", st.Kind)
}

var quietOnce sync.Once

// quietCtrlC keeps the Ctrl+C that Windows sends through the game's
// console from also stopping this test, as the app's own signal.Notify
// does for it. Only Windows needs it: elsewhere Interrupt signals just the
// game, and a developer's Ctrl+C must still stop the tests.
func quietCtrlC() {
	if runtime.GOOS == "windows" {
		quietOnce.Do(func() { signal.Notify(make(chan os.Signal, 1), os.Interrupt) })
	}
}

// Console types a line on the server's console, as the app types a console
// game's save line before it exits. It gives game.ErrNoConsole when the
// server has none (it isn't a console game, or after a takeover).
func (s *Session) Console(line string) error {
	s.mu.Lock()
	a := s.run
	s.mu.Unlock()
	if a == nil {
		return game.ErrNoConsole
	}
	return a.typeLine(line)
}

// skip marks every event of kind seen so far as already returned, so the
// next WaitEvent for it waits for a new one.
func (s *Session) skip(kind string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run == nil {
		return
	}
	for i, e := range s.run.events {
		if e.Kind == kind {
			s.run.used[i] = true
		}
	}
}

// StartAgain starts the stopped server again, on the same install and the
// selected world (a new world only if the adapter's Worlds says it isn't
// there).
func (s *Session) StartAgain() {
	s.t.Helper()
	s.start()
}

// Server is the adapter's Server for this instance (a new one after
// Takeover, as after an app restart).
func (s *Session) Server() game.Server {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.srv
}

// Env is the instance's environment: its folders and its Store.
func (s *Session) Env() game.Env { return s.env }

// Record is the current run's record, as the run file holds it.
func (s *Session) Record() game.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run == nil {
		return game.Record{}
	}
	return s.run.rec
}

// Plan is the latest start's plan.
func (s *Session) Plan() game.Plan {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.plan
}

// cleanup kills a server the test left running (before t.TempDir removes
// its folder, which Windows refuses while the program runs) and, when the
// test failed, shows the end of the server's log.
func (s *Session) cleanup() {
	s.mu.Lock()
	a := s.run
	s.mu.Unlock()
	if a != nil {
		if !a.isExited() {
			a.proc.Kill()
			select {
			case <-a.exited:
			case <-time.After(15 * time.Second):
			}
		}
		a.detach()
		if s.t.Failed() {
			s.t.Logf("gametest: the end of %s's last log:\n%s", s.info.ID, a.tail())
		}
	}
	s.mu.Lock()
	claimed := s.claimed
	s.claimed = nil
	s.mu.Unlock()
	releasePorts(claimed)
	s.tlog.close()
}

// testLog sends the adapter's log (Env.Log) and gametest's own notes to
// the test's log, and drops what comes after the test ends (an adapter's
// goroutine may still be writing), which testing would otherwise panic on.
type testLog struct {
	mu     sync.Mutex
	t      testing.TB
	closed bool
}

func (l *testLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed {
		l.t.Log(strings.TrimRight(string(p), "\n"))
	}
	return len(p), nil
}

func (l *testLog) Printf(format string, args ...any) {
	fmt.Fprintf(l, "gametest: "+format, args...)
}

func (l *testLog) close() {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
}
