package manager

// Tests of stream A's lifecycle (spec 9.3 row A): ports, the memory check,
// auto-stop, idle sleep, going to sleep and the resume after waking, with
// the legacy game's stand-in (cmd/fakevalheim), Demo Game's stand-ins
// (cmd/fakeserver) and a fake clock, so an hour passes in a moment.

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/game/gametest"
	"valheim-easy-server/internal/game/gametest/demo"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/records"
	"valheim-easy-server/internal/servers"
	"valheim-easy-server/internal/worlds"
)

// --- a fake clock -----------------------------------------------------------

// lifeFakeClock is the real clock moved on by Advance. Its timers fire only
// when Advance passes them, so a test decides when a wait ends.
type lifeFakeClock struct {
	mu      sync.Mutex
	offset  time.Duration
	waiters []lifeTimer
}

type lifeTimer struct {
	at time.Time
	ch chan time.Time
}

func (c *lifeFakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.offset)
}

func (c *lifeFakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.waiters = append(c.waiters, lifeTimer{at: time.Now().Add(c.offset + d), ch: ch})
	return ch
}

// Advance moves the clock on by d, firing every timer whose time has come.
func (c *lifeFakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offset += d
	now := time.Now().Add(c.offset)
	keep := c.waiters[:0]
	for _, w := range c.waiters {
		if now.Before(w.at) {
			keep = append(keep, w)
		} else {
			w.ch <- now
		}
	}
	c.waiters = keep
}

func (c *lifeFakeClock) waiting() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}

// --- Demo Game as a second, third… game ----------------------------------------

// lifeGame is Demo Game (internal/game/gametest/demo) with the presence
// and memory a test needs. Its stand-in writes its log to the file the
// plan names (-log), which every build of the manager follows.
type lifeGame struct {
	demo.Game
	presence game.Presence
	memMB    int
}

func (g lifeGame) Info() game.Info {
	in := g.Game.Info()
	in.Stage = game.Preview
	in.Presence = g.presence
	in.MemoryMB = g.memMB
	return in
}

func (g lifeGame) New(env game.Env) game.Server { return lifeLogArg{g.Game.New(env)} }

// lifeLogArg runs Demo Game with its log in the run's log file. It also
// reads world "" as the selected world, as the contract says (the demo
// adapter doesn't yet: stream F's, noted for the lead).
type lifeLogArg struct{ game.Server }

func (s lifeLogArg) Validate(sv game.Saved, world string) game.FieldErrors {
	if world == "" {
		world = sv.World
	}
	if errs := s.Server.Validate(sv, world); len(errs) > 0 {
		return errs
	}
	return nil // the demo answers an empty map, which the start reads as an error
}

func (s lifeLogArg) Fingerprint(sv game.Saved, world string) string {
	if world == "" {
		world = sv.World
	}
	return s.Server.Fingerprint(sv, world)
}

func (s lifeLogArg) Plan(ctx context.Context, req game.PlanRequest) (game.Plan, error) {
	p, err := s.Server.Plan(ctx, req)
	p.Args = append(p.Args, "-log", req.LogPath)
	p.Output = game.LogArg
	return p, err
}

type lifeDemo struct {
	id       string
	presence game.Presence // "" = names
	memMB    int           // 0 = 64
	script   string
}

// demoPort is a port for a test's Demo Game: away from the legacy game's
// (20000-39999 in these tests) and the ranges gametest and the stand-in's
// own tests use.
func lifeDemoPort(m *Manager) int {
	for {
		p := 40000 + rand.Intn(5000)
		m.mu.Lock()
		taken := m.reservedPortsLocked()[p] != ""
		m.mu.Unlock()
		if busy, _ := platform.PortInUse("tcp", p); !taken && !busy {
			return p
		}
	}
}

// lifeAddDemo puts a Demo Game instance in m, as Add a game does: its
// folders under games/<id>, its settings, the fake installer. It has no
// servers.json entry (a test that needs one sets the list itself), and it
// must be called before m.Start.
func lifeAddDemo(t *testing.T, m *Manager, o lifeDemo) *instance {
	t.Helper()
	if o.presence == "" {
		o.presence = game.ByName
	}
	if o.memMB == 0 {
		o.memMB = 64
	}
	g := lifeGame{Game: demo.Game{ID: o.id}, presence: o.presence, memMB: o.memMB}
	dirs := m.gameDirs(o.id)
	store := &testStore{saved: game.Saved{Server: game.Values{"serverName": "Demo " + o.id}, World: "Demo",
		Worlds: map[string]game.Values{}, Ports: map[string]int{"game": lifeDemoPort(m)}}}
	inst := &instance{id: o.id, g: g, info: g.Info(), dirs: dirs, known: map[string]*KnownPlayer{},
		uploads: map[string]*worlds.Upload{}, logs: newRing[LogLine](3000), recs: recordsData{Runs: map[string][]records.Event{}}}
	inst.srv = g.New(game.Env{Instance: o.id, Data: m.opt.DataDir, Dirs: dirs, Log: m.log, Store: store,
		Fake: &game.Fake{Program: gametest.BuildFakeserver(t), Script: o.script, Speed: 4},
		Note: func(kind, text string) { m.addGameActivity(inst, kind, "", text) }})
	putInstance(t, m, inst, nil)
	return inst
}

type lifeOpts struct {
	clock  *lifeFakeClock
	sample time.Duration // the sample loop's tick; 0 = the tests' 200 ms
	idle   int           // idle sleep after these minutes; 0 = off
	demos  []lifeDemo
}

// setupLife starts a manager with the legacy game and Demo Games. The
// legacy game needs no memory here: only the memory tests check it, with
// readings they set themselves.
func setupLife(t *testing.T, o lifeOpts) (*testEnv, map[string]*instance) {
	t.Helper()
	env := &testEnv{data: t.TempDir()}
	var change []func(*Options)
	if o.sample > 0 {
		change = append(change, func(op *Options) { op.SampleInterval = o.sample })
	}
	m := newTestManager(t, env.data, env, change...)
	env.m = m
	s := m.Settings()
	s.Port = 20000 + rand.Intn(20000)
	s.SaveIntervalMinutes = 1
	s.UpdateOnStart = false
	if o.idle > 0 {
		s.IdleHibernate, s.IdleMinutes = true, o.idle
	}
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	if o.clock != nil {
		m.life.clock = o.clock
	}
	m.legacy().info.MemoryMB = 0
	insts := map[string]*instance{}
	for _, d := range o.demos {
		insts[d.id] = lifeAddDemo(t, m, d)
	}
	m.Start()
	t.Cleanup(func() {
		m.eachInstance(func(inst *instance) {
			if m.forceStopGame(inst) == nil {
				waitFor(t, "forced stop of "+inst.id, 10*time.Second, func() bool { return !lifeRuns(m, inst) })
			}
		})
		m.Close()
	})
	return env, insts
}

func lifeRuns(m *Manager, inst *instance) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return inst.run != nil
}

func lifeOnline(m *Manager, inst *instance) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return inst.run != nil && inst.run.ready && !inst.run.stopping
}

// installGame installs an instance's server and waits for it.
func lifeInstall(t *testing.T, m *Manager, inst *instance) {
	t.Helper()
	if err := m.InstallGame(inst.id, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, inst.id+" installed", 30*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return inst.job != nil && inst.job.Finished != nil
	})
	m.mu.Lock()
	job := *inst.job
	m.mu.Unlock()
	if job.Error != "" {
		t.Fatalf("installing %s: %s", inst.id, job.Error)
	}
}

// startOnline starts an instance's server (with no memory check) and
// waits until it is ready.
func lifeStartOnline(t *testing.T, m *Manager, inst *instance) {
	t.Helper()
	if err := m.StartGame(inst.id, StartOptions{Memory: memoryAnyway, Source: SourcePanel}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, inst.id+" online", 30*time.Second, func() bool { return lifeOnline(m, inst) })
}

// lastExit is the instance's last exit, once it has stopped.
func lifeExit(m *Manager, inst *instance) *ExitInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inst.run != nil {
		return nil
	}
	return inst.lastExit
}

func lifeHasText(m *Manager, text string) bool {
	acts, _ := m.Activity(0, 0)
	return slices.ContainsFunc(acts, func(a Activity) bool { return strings.Contains(a.Text, text) })
}

// --- ports ------------------------------------------------------------------

// Ports are picked from each role's default, past every port another
// instance reserves (the legacy game's settings.json port and port+1, and
// servers.json's) and every port another program holds, keeping a tied
// role's port free too, and keeping what the owner chose (spec 2.5).
func TestPortsAvoidValheimAndOtherInstances(t *testing.T) {
	env := &testEnv{data: t.TempDir()}
	m := newTestManager(t, env.data, env)
	s := m.Settings()
	s.Port = 7100 // Valheim: 7100 and 7101
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	busy := map[string]bool{"udp/7103": true} // another program
	set(t, &portInUse, func(proto string, port int) (bool, int) { return busy[fmt.Sprintf("%s/%d", proto, port)], 4242 })
	m.mu.Lock()
	m.publishServersLocked(servers.File{Schema: servers.Schema, Instances: []servers.Instance{
		{ID: m.legacyID, Game: m.legacyID, Legacy: true},
		{ID: "othergame", Game: "othergame", Ports: map[string]int{"game": 7102}}, // a game this build doesn't know keeps its port
	}})
	info := lifeGame{Game: demo.Game{ID: "demoapi", API: true}}.Info() // game 7100, rest = game+1
	ports, err := m.pickPortsLocked(info, nil)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// 7100-7102 are reserved, 7103 is held by a program: game 7104, its
	// tied API port 7105.
	if ports["game"] != 7104 || ports["rest"] != 7105 {
		t.Fatalf("picked %v, want game 7104 and rest 7105", ports)
	}

	// What the owner chose is kept, and its tied port follows it.
	m.mu.Lock()
	ports, err = m.pickPortsLocked(info, map[string]int{"game": 7200})
	m.mu.Unlock()
	if err != nil || ports["game"] != 7200 || ports["rest"] != 7201 {
		t.Fatalf("with the game port chosen: %v %v", ports, err)
	}

	// Picking again for an instance gives it its own ports back.
	got, err := pickPortsWith(info, nil, map[int]string{7100: "valheim", 7104: "demoapi", 7105: "demoapi"}, "demoapi",
		func(string, int) (bool, int) { return false, 0 })
	if err != nil || got["game"] != 7101 {
		t.Fatalf("picked again: %v %v (7101 is free, 7104 is its own)", got, err)
	}

	// Nothing free in the whole range: a clear error, not a port that clashes.
	if _, err := pickPortsWith(info, nil, nil, "", func(string, int) (bool, int) { return true, 0 }); err == nil ||
		!strings.Contains(err.Error(), "No free port") {
		t.Fatalf("with every port taken: %v", err)
	}
}

// A start finds a port another program took since it was picked: it
// fails with the program's name and the button to pick new ports, or, for
// a port the owner chose, sends them to Settings (spec 2.5).
func TestStartPortClash(t *testing.T) {
	env := &testEnv{data: t.TempDir()}
	m := newTestManager(t, env.data, env)
	inst := lifeAddDemo(t, m, lifeDemo{id: "demo"})
	port := inst.saved().Ports["game"]
	set(t, &portInUse, func(proto string, p int) (bool, int) { return proto == "tcp" && p == port, 4242 })
	set(t, &procName, func(pid int) string { return map[int]string{4242: "java"}[pid] })

	var ae *ActionError
	err := m.checkPorts(inst)
	want := fmt.Sprintf("Port %d (TCP) is being used by another program: java.", port)
	if !errors.As(err, &ae) || ae.Action != "pick_ports" || ae.Msg != want {
		t.Fatalf("clash = %#v, want %q with pick_ports", err, want)
	}

	m.mu.Lock()
	m.publishServersLocked(servers.File{Schema: servers.Schema, Instances: []servers.Instance{{ID: "demo", Game: "demo", PortsChosen: []string{"game"}}}})
	m.mu.Unlock()
	if err := m.checkPorts(inst); !errors.As(err, &ae) || ae.Action != "settings" || !strings.Contains(ae.Msg, "change it in Settings") {
		t.Fatalf("clash of a chosen port = %#v", err)
	}

	set(t, &portInUse, func(string, int) (bool, int) { return false, 0 })
	if err := m.checkPorts(inst); err != nil {
		t.Fatalf("no clash: %v", err)
	}
}

// Ports the owner types are checked against the other instances and the
// game's own roles (spec 2.5): a tied role follows its base.
func TestChosenPortsChecked(t *testing.T) {
	env := &testEnv{data: t.TempDir()}
	m := newTestManager(t, env.data, env)
	s := m.Settings()
	s.Port = 7300
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	g := lifeGame{Game: demo.Game{ID: "demoapi", API: true}}
	inst := lifeAddDemo(t, m, lifeDemo{id: "demoapi"})
	inst.g, inst.info = g, g.Info()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range []struct {
		ports map[string]int
		key   string
		has   string
	}{
		{map[string]int{"game": 7300}, "game", "Valheim's"},
		{map[string]int{"game": 7299}, "game", "takes with it"}, // its API port would be 7300
		{map[string]int{"rest": 7400}, "rest", "always the game port plus 1"},
		{map[string]int{"game": 80}, "game", "from 1024 to 65535"},
		{map[string]int{"query": 7500}, "query", "no port called"},
	} {
		errs := m.checkChosenPortsLocked(inst, c.ports)
		if !strings.Contains(errs[c.key], c.has) {
			t.Errorf("%v: errors %v, want %q under %q", c.ports, errs, c.has, c.key)
		}
	}
	if errs := m.checkChosenPortsLocked(inst, map[string]int{"game": 7600}); errs != nil {
		t.Errorf("a free port: %v", errs)
	}
}

// --- the memory check -------------------------------------------------------------

// A game that needs more memory than is free isn't started from the panel
// without the owner's word, and the refusal carries what the dialog needs;
// from Discord it isn't started either (spec 4.3, decided). "Start anyway"
// starts it.
func TestMemoryShort(t *testing.T) {
	env, insts := setupLife(t, lifeOpts{sample: time.Hour, demos: []lifeDemo{{id: "demo", memMB: 4096}}})
	m := env.m
	d := insts["demo"]
	install(t, m)
	lifeInstall(t, m, d)
	lifeStartOnline(t, m, m.legacy())
	m.mu.Lock()
	m.live.MemTotal, m.live.MemSys = 8<<30, 6<<30 // 2 GB free
	m.legacy().run.memNow = 3170 << 20
	m.mu.Unlock()

	before := currentJobID(m)
	var ae *ActionError
	err := m.StartGame("demo", StartOptions{Source: SourcePanel})
	if !errors.As(err, &ae) || ae.Action != "memory" {
		t.Fatalf("start = %v, want the memory refusal", err)
	}
	if want := "There may not be enough memory to start Demo: it needs about 4 GB and 2 GB is free."; ae.Msg != want {
		t.Errorf("message %q, want %q", ae.Msg, want)
	}
	ms, ok := ae.Data.(MemoryShort)
	if !ok || ms.NeedMB != 4096 || ms.FreeMB != 2048 || len(ms.Running) != 1 ||
		ms.Running[0] != (RunningGame{ID: m.legacyID, Name: "Valheim", MemMB: 3170}) {
		t.Fatalf("data = %#v", ae.Data)
	}
	if currentJobID(m) != before {
		t.Fatal("a refused start made a job")
	}

	// From Discord: the same refusal, which the waker replies with.
	if err := m.startFromWake("demo", "request1"); !errors.As(err, &ae) || ae.Action != "memory" {
		t.Fatalf("Discord start = %v, want the memory refusal", err)
	}
	if lifeRuns(m, d) {
		t.Fatal("Discord started a game that doesn't fit")
	}
	// Its repeats don't post the refusal again.
	if err := m.startFromWake("demo", "request1"); err != nil {
		t.Fatalf("a repeat of the same request = %v, want nil", err)
	}

	// The owner's "Start anyway", and a restart after a crash, don't check.
	m.mu.Lock()
	for _, o := range []StartOptions{{Memory: memoryAnyway, Source: SourcePanel}, {Source: SourceRestart}} {
		if err := m.memoryForStartLocked(d, o); err != nil {
			t.Errorf("%+v: %v", o, err)
		}
	}
	m.mu.Unlock()
	lifeStartOnline(t, m, d)
}

// "Stop Valheim and start" stops that game inside the start job, then
// starts this one (spec 4.3).
func TestStopToMakeRoom(t *testing.T) {
	env, insts := setupLife(t, lifeOpts{sample: time.Hour, demos: []lifeDemo{{id: "demo"}}})
	m := env.m
	d, v := insts["demo"], m.legacy()
	install(t, m)
	lifeInstall(t, m, d)

	// Stopping a game that isn't running, or the game itself, is refused
	// before any job is made.
	for _, o := range []StartOptions{{Memory: memoryStop + v.id}, {Memory: memoryStop + d.id}, {Memory: memoryStop + "nosuchgame"}} {
		o.Source = SourcePanel
		if err := m.StartGame(d.id, o); err == nil {
			t.Fatalf("%q was accepted", o.Memory)
		}
	}

	lifeStartOnline(t, m, v)
	rec := listen(m)
	if err := m.StartGame(d.id, StartOptions{Memory: memoryStop + v.id, Source: SourcePanel}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "Demo online", 30*time.Second, func() bool { return lifeOnline(m, d) })
	if lifeRuns(m, v) {
		t.Fatal("Valheim is still running")
	}
	calls := rec.String()
	if i, j := strings.Index(calls, "exit "+v.id), strings.Index(calls, "ready "+d.id); i < 0 || j < 0 || i > j {
		t.Fatalf("Valheim didn't stop before Demo started:\n%s", calls)
	}
	if !lifeHasText(m, "Valheim was stopped to make room for Demo.") {
		t.Fatal("no activity about the stop")
	}
	if e := lifeExit(m, v); e == nil || !e.Expected {
		t.Fatalf("Valheim's exit = %+v", e)
	}
}

// --- auto-stop ------------------------------------------------------------------

// A game with nobody on stops after its minutes (here 30), through its
// stop chain; a game whose players can't be seen (presence none, or log
// presence not proven on this install) never does (spec 4.5, 2.6).
func TestAutoStopAfterMinutes(t *testing.T) {
	fakeUser(t)
	t.Setenv("FAKE_VALHEIM_SCRIPT", "join:Astrid@0.3,leave:Astrid@1.0")
	clk := &lifeFakeClock{}
	env, insts := setupLife(t, lifeOpts{clock: clk, demos: []lifeDemo{{id: "nobody", presence: game.Unknown}, {id: "unproven"}}})
	m := env.m
	v := m.legacy()
	thirty := 30
	m.mu.Lock()
	m.publishServersLocked(servers.File{Schema: servers.Schema, Instances: []servers.Instance{{ID: v.id, Game: v.id, Legacy: true, AutoStopMinutes: &thirty}}})
	m.mu.Unlock()
	install(t, m)
	for _, d := range insts {
		lifeInstall(t, m, d)
		lifeStartOnline(t, m, d)
	}
	if err := m.StartServer(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "Astrid to come and go", 30*time.Second, func() bool { return hasActivity(m, "leave", "Astrid") })
	left := clk.Now()

	clk.Advance(29 * time.Minute)
	time.Sleep(time.Second) // a few sample ticks
	if !lifeOnline(m, v) {
		t.Fatal("Valheim stopped before its 30 minutes were up")
	}
	st := m.Status()
	for _, gs := range st.Games {
		switch gs.ID {
		case v.id:
			if gs.AutoStop == nil || gs.AutoStop.Minutes != 30 || gs.AutoStop.At == nil ||
				gs.AutoStop.At.Sub(left.Add(30*time.Minute)).Abs() > 5*time.Second {
				t.Errorf("Valheim's autoStop = %+v, want 30 minutes after %s", gs.AutoStop, left)
			}
		default:
			if gs.AutoStop != nil {
				t.Errorf("%s shows an auto-stop (%+v) though its players can't be seen", gs.ID, gs.AutoStop)
			}
		}
	}

	clk.Advance(time.Minute + time.Second)
	waitFor(t, "auto-stop", 30*time.Second, func() bool { return lifeExit(m, v) != nil })
	if e := lifeExit(m, v); !e.Expected {
		t.Fatalf("exit = %+v", e)
	}
	waitFor(t, "the activity", 5*time.Second, func() bool {
		return lifeHasText(m, "Valheim stopped: nobody played for 30 minutes.")
	})

	clk.Advance(3 * time.Hour)
	time.Sleep(time.Second)
	for id, d := range insts {
		if !lifeOnline(m, d) {
			t.Errorf("%s was stopped, though nobody can see whether anyone plays it", id)
		}
	}
}

// lifeTick is a sample tick's part for auto-stop and idle sleep, exactly
// as sampleLoop runs it, at the fake clock's time. The tests that need a
// tick at a given moment run with a sample loop that never ticks by
// itself (sample: time.Hour) and tick by hand.
func lifeTick(m *Manager, clk *lifeFakeClock) {
	at := clk.Now()
	m.mu.Lock()
	m.observeLocked(func(o observer) { o.onTick(at) })
	m.mu.Unlock()
	m.checkIdle(at)
}

// The last player leaves at T: the game stops at T+60 and the computer
// sleeps right after, not an hour later, with nothing to start again when
// it wakes (spec 4.5). The same for a game nobody joins after its start.
// The clock goes across T+60 in steps much shorter than a sample tick, so
// a tick lands wherever the two clocks could disagree: when the auto-stop
// clock restarted after the idle clock (she left a moment after the last
// tick; the game became ready, and its start job ended, after the idle
// clock started with the start), going to sleep came first, stopped the
// game itself and kept it to start again on the next wake.
func TestLastPlayerLeavesSleepRightAfter(t *testing.T) {
	for _, tc := range []struct{ name, script string }{
		{"the last player leaves", "join:Astrid@0.3,leave:Astrid@1.5"},
		{"nobody joins", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeUser(t)
			noNetwork(t)
			t.Setenv("FAKE_VALHEIM_SCRIPT", tc.script)
			clk := &lifeFakeClock{}
			env, _ := setupLife(t, lifeOpts{clock: clk, sample: time.Hour, idle: 60})
			m := env.m
			v := m.legacy()
			install(t, m)
			if err := m.StartServer(); err != nil {
				t.Fatal(err)
			}
			if job := waitJob(t, m, currentJobID(m)); job.Error != "" {
				t.Fatalf("start job: %+v", job)
			}
			if tc.script != "" {
				// A tick while she plays, as the sample loop's every 2 s,
				// and she leaves a second later.
				waitFor(t, "Astrid to join", 30*time.Second, func() bool { return hasActivity(m, "join", "Astrid") })
				lifeTick(m, clk)
				waitFor(t, "Astrid to leave", 30*time.Second, func() bool { return hasActivity(m, "leave", "Astrid") })
			}
			m.mu.Lock()
			from := v.emptySince
			if m.machineIdleSince.Before(from) {
				from = m.machineIdleSince
			}
			m.mu.Unlock()
			if from.IsZero() {
				t.Fatal("the auto-stop clock never started")
			}

			clk.Advance(from.Add(59 * time.Minute).Sub(clk.Now()))
			lifeTick(m, clk)
			if !lifeOnline(m, v) || env.slept.Load()+env.hibernated.Load() != 0 {
				t.Fatal("stopped or slept before the hour was up")
			}
			clk.Advance(time.Minute - 3*time.Second)
			for {
				lifeTick(m, clk)
				m.mu.Lock()
				stopping := v.run == nil || v.run.autoStopped
				resting := m.appJob != nil && m.appJob.Kind == "hibernate"
				m.mu.Unlock()
				if resting {
					t.Fatalf("going to sleep came before auto-stop, %s after the clock started", clk.Now().Sub(from))
				}
				if stopping {
					break
				}
				if clk.Now().Sub(from) > 61*time.Minute {
					t.Fatal("no auto-stop a minute after the hour")
				}
				clk.Advance(10 * time.Millisecond)
			}
			waitFor(t, "auto-stop", 30*time.Second, func() bool { return lifeExit(m, v) != nil })
			waitFor(t, "the stop job", 10*time.Second, func() bool {
				m.mu.Lock()
				defer m.mu.Unlock()
				return !m.anyJobLocked()
			})
			if env.slept.Load()+env.hibernated.Load() != 0 {
				t.Fatal("slept while the game was being stopped")
			}

			// The next tick: nothing holds the computer awake any more.
			clk.Advance(2 * time.Second)
			lifeTick(m, clk)
			waitFor(t, "the computer to sleep", 30*time.Second, func() bool { return env.slept.Load()+env.hibernated.Load() == 1 })
			m.mu.Lock()
			owed := append([]string(nil), m.resumeSet...)
			m.mu.Unlock()
			if len(owed) != 0 {
				t.Fatalf("resume set = %v, want empty: auto-stop stopped the game", owed)
			}
			if !lifeHasText(m, "Valheim stopped: nobody played for an hour.") || !lifeHasText(m, "Nobody has played for 60 minutes.") {
				t.Fatal("missing the auto-stop or the sleep activity")
			}
		})
	}
}

// An auto-stop whose server doesn't stop restarts only the auto-stop
// clock: neither it nor the end of its job is someone doing something, so
// a server that won't stop doesn't keep the computer awake by failing
// every hour (going to sleep tries once more, and sleeps anyway). Any
// other job of the game is it seen in use, and keeps the idle clock from
// being behind the auto-stop one.
func TestFailedAutoStopLeavesTheIdleClock(t *testing.T) {
	fakeUser(t)
	noNetwork(t)
	clk := &lifeFakeClock{}
	env, _ := setupLife(t, lifeOpts{clock: clk, sample: time.Hour, idle: 60})
	m := env.m
	v := m.legacy()
	install(t, m)
	if err := m.StartServer(); err != nil {
		t.Fatal(err)
	}
	if job := waitJob(t, m, currentJobID(m)); job.Error != "" {
		t.Fatalf("start job: %+v", job)
	}

	// The observer's calls, as jobs.go makes them, around a stop job that
	// autoStopGame's undo marked as failed while it ran.
	o := lifecycleObserver{m}
	m.mu.Lock()
	defer m.mu.Unlock()
	if v.run == nil || !m.presenceKnownLocked(v) {
		t.Fatal("Valheim isn't running with its players seen")
	}
	past := clk.Now().Add(-time.Hour)
	m.machineIdleSince = past
	failed := &Job{ID: 1000, Kind: "stop", Title: "Stopping the server"}
	m.life.autoFailed = map[string]*Job{v.id: failed}
	clk.Advance(time.Second)
	ended := time.Now()
	failed.Finished = &ended
	o.onJob(v, failed)
	if !m.machineIdleSince.Equal(past) {
		t.Fatalf("the end of a failed auto-stop moved the idle clock to %s", m.machineIdleSince)
	}
	if v.emptySince.Before(clk.Now().Add(-time.Second)) {
		t.Fatalf("the auto-stop clock didn't start again: %s", v.emptySince)
	}
	if m.life.autoFailed[v.id] != nil {
		t.Fatal("the failed job is still noted")
	}

	other := &Job{ID: 1001, Kind: "backup", Title: "Backing up the world", Finished: &ended}
	o.onJob(v, other)
	if m.machineIdleSince.Before(v.emptySince) {
		t.Fatalf("after a backup job: idle clock %s is behind the auto-stop clock %s", m.machineIdleSince, v.emptySince)
	}
}

// --- idle sleep -------------------------------------------------------------------

// The computer doesn't sleep by itself while a game runs whose players
// can't be seen, nor while another version's server runs (spec 4.7).
func TestIdleSleepBlockedByUnseenGames(t *testing.T) {
	fakeUser(t)
	env, insts := setupLife(t, lifeOpts{sample: time.Hour, idle: 10, demos: []lifeDemo{{id: "nobody", presence: game.Unknown}}})
	m := env.m
	d := insts["nobody"]
	lifeInstall(t, m, d)
	lifeStartOnline(t, m, d)
	blocked := func(what string) {
		t.Helper()
		m.mu.Lock()
		m.machineIdleSince, m.keepAwakeUntil = time.Now().Add(-time.Hour), time.Time{}
		deadline := m.idleDeadlineLocked()
		m.mu.Unlock()
		m.checkIdle(time.Now())
		if !deadline.IsZero() || env.slept.Load()+env.hibernated.Load() != 0 {
			t.Fatalf("%s: sleeps at %v, slept %d", what, deadline, env.slept.Load()+env.hibernated.Load())
		}
	}
	blocked("a presence-none game")

	if err := m.StopGame(d.id); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stop", 30*time.Second, func() bool { return lifeExit(m, d) != nil })
	waitFor(t, "the stop job", 10*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return !m.anyJobLocked()
	})
	pid, started := thisProcess(t)
	writeRun(t, env.data, "terraria", game.Record{PID: pid, Started: started, Launched: started, Game: "terraria", Name: "Terraria"})
	m.scanForeign()
	blocked("a foreign game")

	set(t, &openProc, func(int) (*platform.Proc, error) { return nil, os.ErrNotExist })
	m.scanForeign()
	m.mu.Lock()
	m.machineIdleSince, m.keepAwakeUntil = time.Now().Add(-time.Hour), time.Time{}
	deadline := m.idleDeadlineLocked()
	m.mu.Unlock()
	if deadline.IsZero() {
		t.Fatal("nothing blocks it any more, yet it never sleeps")
	}
}

// --- going to sleep and waking -----------------------------------------------------

// Going to sleep stops every running game and remembers them for the
// wake, leaving out one whose auto-stop time had come (spec 4.7).
func TestSleepStopsAllAndRecordsResume(t *testing.T) {
	env, insts := setupLife(t, lifeOpts{sample: time.Hour, demos: []lifeDemo{{id: "demo"}}})
	m := env.m
	d, v := insts["demo"], m.legacy()
	install(t, m)
	lifeInstall(t, m, d)
	lifeStartOnline(t, m, v)
	lifeStartOnline(t, m, d)
	rec := listen(m)
	if err := m.Hibernate(""); err != nil {
		t.Fatal(err)
	}
	if job := waitJob(t, m, currentJobID(m)); job.Error != "" {
		t.Fatalf("sleep job: %+v", job)
	}
	if lifeRuns(m, v) || lifeRuns(m, d) || env.slept.Load()+env.hibernated.Load() != 1 {
		t.Fatalf("after Sleep: Valheim runs %v, Demo runs %v, rested %d", lifeRuns(m, v), lifeRuns(m, d), env.slept.Load()+env.hibernated.Load())
	}
	for _, inst := range []*instance{v, d} {
		if e := lifeExit(m, inst); e == nil || !e.Expected {
			t.Errorf("%s's exit = %+v", inst.id, e)
		}
		if !rec.has("exit " + inst.id + " expected=true auto=false") {
			t.Errorf("no exit of %s in:\n%s", inst.id, rec)
		}
	}
	m.mu.Lock()
	got := append([]string(nil), m.resumeSet...)
	m.mu.Unlock()
	if !slices.Equal(got, []string{v.id, d.id}) {
		t.Fatalf("resume set = %v, want %v", got, []string{v.id, d.id})
	}

	// Valheim's hour with nobody on was up: it stays off after the wake.
	lifeStartOnline(t, m, v)
	lifeStartOnline(t, m, d)
	m.mu.Lock()
	m.resumeSet = nil
	v.emptySince = time.Now().Add(-2 * time.Hour)
	m.mu.Unlock()
	if err := m.Hibernate(""); err != nil {
		t.Fatal(err)
	}
	waitJob(t, m, currentJobID(m))
	m.mu.Lock()
	got = append([]string(nil), m.resumeSet...)
	m.mu.Unlock()
	if !slices.Equal(got, []string{d.id}) {
		t.Fatalf("resume set = %v, want only %s", got, d.id)
	}
}

// lifeStarts notes, by the fake clock, when each instance's start job
// began.
type lifeStarts struct {
	mu  sync.Mutex
	clk *lifeFakeClock
	t0  time.Time
	at  map[string]time.Duration
}

func lifeWatchStarts(m *Manager, clk *lifeFakeClock) *lifeStarts {
	s := &lifeStarts{clk: clk, at: map[string]time.Duration{}}
	m.mu.Lock()
	m.observers = append(m.observers, s)
	m.mu.Unlock()
	return s
}

func (s *lifeStarts) begin() {
	s.mu.Lock()
	s.t0 = s.clk.Now()
	s.mu.Unlock()
}

func (s *lifeStarts) elapsed() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clk.Now().Sub(s.t0)
}

func (s *lifeStarts) get(id string) (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.at[id]
	return d, ok
}

func (s *lifeStarts) onJob(inst *instance, job *Job) {
	if inst == nil || job.Kind != "start" || job.Finished != nil {
		return
	}
	s.mu.Lock()
	if _, ok := s.at[inst.id]; !ok {
		s.at[inst.id] = s.clk.Now().Sub(s.t0)
	}
	s.mu.Unlock()
}
func (s *lifeStarts) onReady(*instance)                       {}
func (s *lifeStarts) onEvent(*instance, game.Event)           {}
func (s *lifeStarts) onExit(*instance, *serverRun, *ExitInfo) {}
func (s *lifeStarts) onTick(time.Time)                        {}
func (s *lifeStarts) onTakeover(*instance)                    {}
func (s *lifeStarts) onWake(string)                           {}

// stepClock moves the fake clock on by a second at a time up to the given
// time since the wake, letting the resume take each step (it is waiting on
// the clock again, or has begun starting games).
func lifeStep(t *testing.T, m *Manager, clk *lifeFakeClock, st *lifeStarts, until time.Duration) {
	t.Helper()
	for st.elapsed() < until {
		clk.Advance(time.Second)
		waitFor(t, "the resume to take the step", 5*time.Second, func() bool {
			m.mu.Lock()
			r := m.life.resume
			idle := r == nil || r.starting
			m.mu.Unlock()
			return idle || clk.waiting() > 0
		})
	}
}

// The resume waits for the network (spec 4.7's two cases): with the
// network back 5 s after the wake and no Discord request, the games that
// ran start at 35 s; with it back at 25 s and a request heard at 40 s,
// only the request's game starts.
func TestResumeWaitsForNetwork(t *testing.T) {
	t.Run("network at 5 s", func(t *testing.T) {
		clk := &lifeFakeClock{}
		var st *lifeStarts
		set(t, &gatewayAnswers, func(context.Context) bool { return st.elapsed() >= 5*time.Second })
		env, insts := setupLife(t, lifeOpts{clock: clk, sample: time.Hour, demos: []lifeDemo{{id: "demo"}}})
		m := env.m
		d, v := insts["demo"], m.legacy()
		install(t, m)
		lifeInstall(t, m, d)
		st = lifeWatchStarts(m, clk)
		m.mu.Lock()
		m.resumeSet = []string{v.id, d.id}
		m.mu.Unlock()
		m.onWake(time.Hour)
		st.begin()
		m.wokenOnPurpose()
		if !lifeHasText(m, "Starting Valheim and Demo again once the network is back (they were running before the computer went to sleep).") {
			t.Fatal("no activity about the resume")
		}
		lifeStep(t, m, clk, st, 34*time.Second)
		if _, ok := st.get(v.id); ok {
			t.Fatal("a game started before the network was back and 30 s had passed")
		}
		lifeStep(t, m, clk, st, 38*time.Second)
		at, ok := st.get(v.id)
		if !ok || at < 35*time.Second || at > 38*time.Second {
			t.Fatalf("Valheim started at %v (started %v), want at about 35 s", at, ok)
		}
		// Demo starts once Valheim's start is done, so its memory check
		// sees what Valheim took.
		waitFor(t, "Demo to start", 30*time.Second, func() bool { _, ok := st.get(d.id); return ok })
		waitFor(t, "both online", 30*time.Second, func() bool { return lifeOnline(m, v) && lifeOnline(m, d) })
		m.mu.Lock()
		left := len(m.resumeSet)
		m.mu.Unlock()
		if left != 0 {
			t.Fatalf("the resume set wasn't cleared: %d left", left)
		}
	})

	t.Run("network at 25 s, request at 40 s", func(t *testing.T) {
		clk := &lifeFakeClock{}
		var st *lifeStarts
		set(t, &gatewayAnswers, func(context.Context) bool { return st.elapsed() >= 25*time.Second })
		env, insts := setupLife(t, lifeOpts{clock: clk, sample: time.Hour, demos: []lifeDemo{{id: "demo"}}})
		m := env.m
		d, v := insts["demo"], m.legacy()
		install(t, m)
		lifeInstall(t, m, d)
		st = lifeWatchStarts(m, clk)
		m.mu.Lock()
		m.resumeSet = []string{v.id}
		m.mu.Unlock()
		m.onWake(time.Hour)
		st.begin()
		m.wokenOnPurpose()
		lifeStep(t, m, clk, st, 40*time.Second)
		if _, ok := st.get(v.id); ok {
			t.Fatal("the resume started before the network was back and 30 s had passed")
		}
		// The waker's packet for Demo, as wakesignal.go hands it over.
		m.mu.Lock()
		m.observeLocked(func(o observer) { o.onWake(wakeTagged) })
		m.mu.Unlock()
		if err := m.startFromWake(d.id, "request1"); err != nil {
			t.Fatal(err)
		}
		lifeStep(t, m, clk, st, 70*time.Second)
		waitFor(t, "Demo online", 30*time.Second, func() bool { return lifeOnline(m, d) })
		if _, ok := st.get(v.id); ok || lifeRuns(m, v) {
			t.Fatal("the resume started Valheim, though a Discord request asked for Demo only")
		}
		m.mu.Lock()
		pending, owed := m.life.resume != nil, len(m.resumeSet)
		m.mu.Unlock()
		if pending || owed != 0 {
			t.Fatalf("after the request: resume pending %v, %d owed", pending, owed)
		}
		// The waker repeats its packet: the request was met, so nothing more.
		before := currentJobID(m)
		if err := m.startFromWake(d.id, "request1"); err != nil || currentJobID(m) != before {
			t.Fatalf("a repeat: %v, jobs %d → %d", err, before, currentJobID(m))
		}
	})
}

// Going to sleep while the resume after the last wake still waits for the
// network calls it off: its timers would carry on after the next wake of
// any kind (a wake timer, a Mac's dark wake) and start games nobody asked
// for. What it would have started stays owed, and the next wake on
// purpose resumes it afresh. The same when the computer slept some other
// way (its lid) and the app only notices the wake (spec 4.7).
func TestSleepHoldsAWaitingResume(t *testing.T) {
	clk := &lifeFakeClock{}
	var network atomic.Bool
	set(t, &gatewayAnswers, func(context.Context) bool { return network.Load() })
	env, insts := setupLife(t, lifeOpts{clock: clk, sample: time.Hour, demos: []lifeDemo{{id: "demo"}}})
	m := env.m
	d := insts["demo"]
	lifeInstall(t, m, d)
	owed := func() []string {
		m.mu.Lock()
		defer m.mu.Unlock()
		return append([]string(nil), m.resumeSet...)
	}
	pending := func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.life.resume != nil
	}
	wake := func() {
		m.onWake(time.Hour)
		m.wokenOnPurpose()
		waitFor(t, "the resume to wait on the clock", 5*time.Second, func() bool { return clk.waiting() > 0 })
	}

	m.mu.Lock()
	m.resumeSet = []string{d.id}
	m.mu.Unlock()
	wake()
	if !pending() {
		t.Fatal("no resume after the wake")
	}
	if err := m.Hibernate(""); err != nil {
		t.Fatal(err)
	}
	if job := waitJob(t, m, currentJobID(m)); job.Error != "" {
		t.Fatalf("sleep job: %+v", job)
	}
	if pending() || !slices.Equal(owed(), []string{d.id}) {
		t.Fatalf("after Sleep: resume pending %v, owed %v; want none pending and %s owed", pending(), owed(), d.id)
	}
	// The computer wakes by itself, with the network back: the old resume
	// would now go ahead, past its wait for the network and the 30 s after.
	before := currentJobID(m)
	network.Store(true)
	for range 3 {
		clk.Advance(5 * time.Minute)
		time.Sleep(200 * time.Millisecond)
	}
	if currentJobID(m) != before || lifeRuns(m, d) {
		t.Fatal("a resume called off by going to sleep started the game")
	}

	// The lid: the app only notices the wake, with a resume still waiting.
	network.Store(false)
	wake()
	m.onWake(time.Hour)
	if pending() || !slices.Equal(owed(), []string{d.id}) {
		t.Fatalf("after a sleep the app didn't start: resume pending %v, owed %v", pending(), owed())
	}

	// The next wake on purpose resumes afresh, once the network is back.
	// (Fire the called-off resume's timer first, so wake waits for the new
	// one's.)
	clk.Advance(networkMax)
	network.Store(true)
	wake()
	clk.Advance(resumeSettle + time.Second)
	waitFor(t, "Demo online", 30*time.Second, func() bool { return lifeOnline(m, d) })
	if len(owed()) != 0 {
		t.Fatalf("still owed after the resume: %v", owed())
	}
}

// A resume that comes to a game just as going to sleep begins, before the
// sleep job has called it off, finds the start refused as busy: that game
// and the ones after it stay owed for the next wake, with no warning that
// they weren't started again.
func TestResumeMeetsSleep(t *testing.T) {
	noNetwork(t)
	env, insts := setupLife(t, lifeOpts{sample: time.Hour, demos: []lifeDemo{{id: "demo"}, {id: "other"}}})
	m := env.m
	d, o := insts["demo"], insts["other"]
	lifeInstall(t, m, d)
	lifeInstall(t, m, o)

	// The sleep job holds the app's slot and hasn't got to the resume yet.
	resting := &Job{ID: 1000, Kind: "hibernate", Title: "Putting the computer to sleep", Started: time.Now()}
	m.mu.Lock()
	m.appJob = resting
	m.resumeSet = []string{d.id, o.id}
	m.mu.Unlock()
	m.resumeNow() // goes at once: no wait for the network
	waitFor(t, "the resume to end", 10*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.life.resume == nil
	})
	m.mu.Lock()
	owed := append([]string(nil), m.resumeSet...)
	ended := time.Now()
	resting.Finished = &ended
	m.mu.Unlock()
	if !slices.Equal(owed, []string{d.id, o.id}) {
		t.Fatalf("owed after meeting the sleep = %v, want both", owed)
	}
	if lifeHasText(m, "wasn't started again") {
		t.Fatal("a warning for a game that stays owed")
	}
	if lifeRuns(m, d) || lifeRuns(m, o) {
		t.Fatal("a game started while the computer was going to sleep")
	}
}

// The KVM's own Wake-on-LAN button (a packet without a request) starts
// the games that ran before the sleep at once, also cutting short a resume
// waiting for the network (spec 4.7, decided).
func TestKVMPacketResumes(t *testing.T) {
	noNetwork(t)
	env, insts := setupLife(t, lifeOpts{sample: time.Hour, demos: []lifeDemo{{id: "demo"}, {id: "other"}}})
	m := env.m
	d, o := insts["demo"], insts["other"]
	lifeInstall(t, m, d)
	lifeInstall(t, m, o)

	m.mu.Lock()
	m.resumeSet = []string{d.id}
	m.observeLocked(func(ob observer) { ob.onWake(wakeUntagged) }) // as wakesignal.go calls it for each packet
	m.mu.Unlock()
	waitFor(t, "Demo online", 30*time.Second, func() bool { return lifeOnline(m, d) })
	if !lifeHasText(m, "Starting Demo again: someone asked with Wake-on-LAN, and it was running before the computer went to sleep.") {
		t.Fatal("no activity about the resume")
	}

	// A resume waiting for a network that doesn't come back goes ahead at once.
	m.mu.Lock()
	m.resumeSet = []string{o.id}
	m.mu.Unlock()
	m.onWake(time.Hour)
	m.wokenOnPurpose()
	time.Sleep(300 * time.Millisecond)
	if lifeRuns(m, o) {
		t.Fatal("started before the network was back")
	}
	m.resumeNow()
	waitFor(t, "Other online", 30*time.Second, func() bool { return lifeOnline(m, o) })

	// With nothing owed, a packet only wakes.
	before := currentJobID(m)
	m.mu.Lock()
	m.observeLocked(func(ob observer) { ob.onWake(wakeUntagged) })
	m.mu.Unlock()
	m.resumeNow()
	time.Sleep(300 * time.Millisecond)
	if currentJobID(m) != before {
		t.Fatal("a packet with nothing to resume started something")
	}
}

// A Discord request that finds its game busy is kept, so a repeat of the
// waker's packet starts it once it can; once met, its repeats do nothing,
// so a game someone stops on purpose stays stopped (spec 4.7, 6.4).
func TestDiscordRequestWhileBusy(t *testing.T) {
	noNetwork(t)
	env, insts := setupLife(t, lifeOpts{sample: time.Hour, demos: []lifeDemo{{id: "demo"}}})
	m := env.m
	d := insts["demo"]
	lifeInstall(t, m, d)
	release := make(chan struct{})
	if err := m.runInstJob(d, "backup", "Backing up", false, func(context.Context, *jobCtl) error {
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var be *BusyError
	if err := m.startFromWake(d.id, "request1"); !errors.As(err, &be) {
		t.Fatalf("a request while busy = %v, want a BusyError", err)
	}
	close(release)
	waitFor(t, "the backup job", 10*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return d.job.Finished != nil
	})
	if err := m.startFromWake(d.id, "request1"); err != nil {
		t.Fatalf("the repeat = %v", err)
	}
	waitFor(t, "Demo online", 30*time.Second, func() bool { return lifeOnline(m, d) })
	if err := m.StopGame(d.id); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "Demo stopped", 30*time.Second, func() bool { return lifeExit(m, d) != nil })
	waitFor(t, "the stop job", 10*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return d.job.Finished != nil
	})
	before := currentJobID(m)
	if err := m.startFromWake(d.id, "request1"); err != nil || currentJobID(m) != before {
		t.Fatalf("a repeat after a stop on purpose: %v, jobs %d → %d", err, before, currentJobID(m))
	}
}

// Removing Valheim only hides it: it is stopped and its files stay where
// they are (I1).
func TestRemoveLegacyOnlyHides(t *testing.T) {
	env, _ := setupLife(t, lifeOpts{sample: time.Hour})
	m := env.m
	v := m.legacy()
	install(t, m)
	lifeStartOnline(t, m, v)
	exe := v.srv.Program()
	id, err := m.RemoveGame(v.id)
	if err != nil {
		t.Fatal(err)
	}
	job := waitJob(t, m, id)
	if _, err := os.Stat(exe); err != nil {
		t.Fatalf("Valheim's server is gone: %v", err)
	}
	if job.Error != "" {
		t.Fatalf("the hide failed: %s", job.Error)
	}
	m.mu.Lock()
	hidden, starred := m.isHiddenLocked(v), m.isStarredLocked(v)
	m.mu.Unlock()
	if !hidden || starred || lifeRuns(m, v) {
		t.Fatalf("after removing: hidden %v, starred %v, running %v", hidden, starred, lifeRuns(m, v))
	}
}

// A hidden Valheim (removed from the panel) is never started: not from
// the panel, not by a Discord request, not by the resume, and it isn't
// remembered when the computer goes to sleep (invariant I1).
func TestHiddenLegacyNeverStarted(t *testing.T) {
	noNetwork(t)
	env, _ := setupLife(t, lifeOpts{sample: time.Hour})
	m := env.m
	v := m.legacy()
	install(t, m)
	hide := func(on bool) {
		m.mu.Lock()
		m.publishServersLocked(servers.File{Schema: servers.Schema, App: servers.App{Stars: []string{}},
			Instances: []servers.Instance{{ID: v.id, Game: v.id, Legacy: true, Hidden: on}}})
		m.mu.Unlock()
	}
	hide(true)
	const refusal = "Valheim is removed from the panel. Add it again first."
	before := currentJobID(m)
	if err := m.StartGame(v.id, StartOptions{Source: SourcePanel}); err == nil || err.Error() != refusal {
		t.Fatalf("the panel's start = %v", err)
	}
	if err := m.startFromWake(v.id, "request1"); err == nil || err.Error() != refusal {
		t.Fatalf("a Discord start = %v", err)
	}
	m.mu.Lock()
	m.resumeSet = []string{v.id}
	m.mu.Unlock()
	m.resumeNow()
	waitFor(t, "the resume to give up", 10*time.Second, func() bool { return lifeHasText(m, "Valheim wasn't started again after waking") })
	if currentJobID(m) != before || lifeRuns(m, v) {
		t.Fatal("a hidden Valheim was started")
	}

	// Hidden while it ran (removing it stops it, but say it didn't): going
	// to sleep doesn't remember it.
	hide(false)
	lifeStartOnline(t, m, v)
	hide(true)
	if err := m.Hibernate(""); err != nil {
		t.Fatal(err)
	}
	waitJob(t, m, currentJobID(m))
	m.mu.Lock()
	owed := len(m.resumeSet)
	m.mu.Unlock()
	if owed != 0 || lifeRuns(m, v) {
		t.Fatalf("after sleeping: %d owed, running %v", owed, lifeRuns(m, v))
	}
}

// --- firewall ---------------------------------------------------------------------

// Every installed game gets its rule, by its public ports (or any port
// for a game with none); one that isn't installed is left out, because
// Windows makes no rule for a program that isn't there (spec 2.12).
func TestGameFirewallRules(t *testing.T) {
	env := &testEnv{data: t.TempDir()}
	m := newTestManager(t, env.data, env)
	d := lifeAddDemo(t, m, lifeDemo{id: "demo"})
	port := d.saved().Ports["game"]
	m.mu.Lock()
	rules := m.gameRulesLocked()
	m.mu.Unlock()
	if len(rules) != 1 || rules[0].Instance != "demo" || !slices.Equal(rules[0].TCP, []int{port}) ||
		!slices.Equal(rules[0].UDP, []int{port}) || rules[0].AnyPort {
		t.Fatalf("rules = %+v", rules)
	}
	if got := realPrograms(rules); len(got) != 0 {
		t.Fatalf("a game that isn't installed has a rule: %+v", got)
	}
	ready := 0
	set(t, &gameFirewallReady, func(platform.GameRule) bool { ready++; return true })
	if m.checkGameFirewall(d) || ready != 0 {
		t.Fatal("a game that isn't installed counts as let through")
	}

	// Installed: its rule is made, and checked.
	if err := os.MkdirAll(filepath.Dir(d.srv.Program()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d.srv.Program(), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := realPrograms(rules); len(got) != 1 {
		t.Fatalf("an installed game has no rule: %+v", got)
	}
	if !m.checkGameFirewall(d) || ready != 1 {
		t.Fatal("the installed game's rule wasn't checked")
	}

	// A game with no public ports gets one rule for its program.
	m.mu.Lock()
	d.info.Ports = []game.PortRole{{Role: "rcon", Protos: []string{"tcp"}, Default: 7200}}
	rules = m.gameRulesLocked()
	m.mu.Unlock()
	if len(rules) != 1 || !rules[0].AnyPort || len(rules[0].TCP) != 0 {
		t.Fatalf("rules without public ports = %+v", rules)
	}
}

// --- Add a game and removing ----------------------------------------------------------

// The catalog lists the games this build offers, with what Add a game
// asks; adding checks what was asked before anything is written (spec
// 6.1, 6.2).
func TestCatalogAndAddChecks(t *testing.T) {
	env := &testEnv{data: t.TempDir()}
	m := newTestManager(t, env.data, env, fakeInstalls(t))
	cat := m.Catalog()
	byID := map[string]CatalogGame{}
	for _, c := range cat {
		byID[c.ID] = c
		if c.Stage == game.Hidden {
			t.Errorf("%s is hidden but listed", c.ID)
		}
		for _, f := range c.Schema {
			if !f.Add {
				t.Errorf("%s lists %s, which Add a game doesn't ask", c.ID, f.Key)
			}
		}
	}
	if v, ok := byID[m.legacyID]; !ok || !v.Added {
		t.Fatalf("Valheim in the catalog: %+v", v)
	}
	mcGame, _ := game.Get("minecraft")
	_, licensed := mcGame.(game.Licensed)
	mc, ok := byID["minecraft"]
	if !ok || mc.Added || licensed && mc.EULA == "" {
		t.Fatalf("Minecraft in the catalog: %+v", mc)
	}
	if want, _ := mcGame.Info().LevelOn(runtime.GOOS, runtime.GOARCH); mc.CanRun != want {
		t.Fatalf("Minecraft can run here: %s, want %s", mc.CanRun, want)
	}

	// A game that can't run on this computer isn't added; with fake
	// servers every game runs everywhere.
	if mc.CanRun == game.CantRun {
		if _, err := m.AddGame(AddGameRequest{Game: "minecraft", ServerName: "Friends", EULA: true,
			World: AddWorld{Mode: "new", Name: "Riverbend"}}); err == nil || !strings.Contains(err.Error(), "can't be added here") {
			t.Fatalf("adding a game that can't run here = %v", err)
		}
	}
	t.Setenv("EGS_DEV_FAKE_SERVERS", "1")

	// What's asked is checked first, and nothing is written for it.
	_, err := m.AddGame(AddGameRequest{Game: "minecraft", World: AddWorld{Mode: "new", Name: "Riverbend"}})
	var fe game.FieldErrors
	if !errors.As(err, &fe) || fe["server.serverName"] == "" || licensed && fe["eula"] == "" {
		t.Fatalf("an empty request = %v", err)
	}
	if _, err := m.AddGame(AddGameRequest{Game: "minecraft", ServerName: "Friends", World: AddWorld{Mode: "new", Name: "bad/name"}}); !errors.As(err, &fe) || fe["world.name"] == "" {
		t.Fatalf("a bad world name = %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.data, "games", "minecraft")); err == nil {
		t.Fatal("a refused game left a folder")
	}
	if _, err := m.AddGame(AddGameRequest{Game: "nosuchgame"}); err == nil {
		t.Fatal("an unknown game was added")
	}
	if _, err := m.AddGame(AddGameRequest{Game: m.legacyID, ServerName: "x"}); err == nil || !strings.Contains(err.Error(), "added already") {
		t.Fatalf("adding Valheim again = %v", err)
	}

	// A right request is added: in servers.json, not starred (decided:
	// "Star it" is unticked by default), with its install job.
	added, err := m.AddGame(AddGameRequest{Game: "minecraft", ServerName: "Friends", EULA: true, World: AddWorld{Mode: "new", Name: "Riverbend"}})
	if err != nil || added.ID != "minecraft" || added.Job == 0 {
		t.Fatalf("a right request = %+v, %v", added, err)
	}
	waitAddInstall(t, m, added.ID)
	m.mu.Lock()
	entry, starred := m.servers.Instance(added.ID), slices.Contains(m.servers.App.Stars, added.ID)
	m.mu.Unlock()
	if entry == nil || entry.Settings["serverName"] != "Friends" || starred {
		t.Fatalf("its servers.json entry = %+v, starred %v", entry, starred)
	}
	if _, err := m.AddGame(AddGameRequest{Game: "minecraft", ServerName: "Again", EULA: true, World: AddWorld{Mode: "new", Name: "Two"}}); err == nil || !strings.Contains(err.Error(), "added already") {
		t.Fatalf("adding Minecraft twice = %v", err)
	}
}

// fakeInstalls makes every game but the legacy one install and run the
// stand-in server (cmd/fakeserver), as EGS_DEV_FAKE_SERVERS does, so an
// added game's install job never downloads the real one.
func fakeInstalls(t *testing.T) func(*Options) {
	prog := gametest.BuildFakeserver(t)
	return func(o *Options) { o.Fake = &game.Fake{Program: prog} }
}

// waitAddInstall waits for the install job of a game just added, so it
// writes nothing into the test's folder once the test is over.
func waitAddInstall(t *testing.T, m *Manager, id string) {
	t.Helper()
	inst, err := m.instanceByID(id)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the install job of "+id, 60*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return inst.job != nil && inst.job.Finished != nil
	})
}

// Removing a game backs its worlds up before anything else, and deletes
// nothing when the list of games can't be written (spec 6.2).
func TestRemoveKeepsWorlds(t *testing.T) {
	env, insts := setupLife(t, lifeOpts{sample: time.Hour, demos: []lifeDemo{{id: "demo"}}})
	m := env.m
	d := insts["demo"]
	lifeInstall(t, m, d)
	lifeStartOnline(t, m, d) // makes its world
	id, err := m.RemoveGame(d.id)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the remove job", 60*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return d.job != nil && d.job.ID == id && d.job.Finished != nil
	})
	if lifeRuns(m, d) {
		t.Fatal("the removed game still runs")
	}
	backups, _ := filepath.Glob(filepath.Join(d.dirs.Backups, "Demo", "*_removed*"))
	if len(backups) != 1 {
		t.Fatalf("backups of the world: %v", backups)
	}
	m.mu.Lock()
	jobErr := d.job.Error
	m.mu.Unlock()
	if jobErr != "" {
		t.Fatalf("the remove job failed: %s", jobErr)
	}
	if _, err := m.instanceByID(d.id); err == nil {
		t.Fatal("the removed game is still in the list of games")
	}
	if _, err := os.Stat(d.srv.Program()); err == nil {
		t.Fatal("the server files are still there")
	}
	if _, err := os.Stat(filepath.Join(d.dirs.Saves, "Demo")); err != nil {
		t.Fatalf("the world itself is gone: %v", err)
	}
}

// An import-mode add that fails after the world was copied takes the copy
// back: a retry would import it a second time, and a games/<id> folder is
// what rebuilding a lost servers.json reads as an added game (spec 3.4).
// A folder that was there before (the worlds a removed game keeps) stays,
// without the copy.
func TestFailedAddTakesBackTheImport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("EGS_DEV_FAKE_SERVERS", "1")
	own := filepath.Join(home, ".local", "share", "Terraria", "Worlds") // Terraria's own worlds (terraria/worlds.go)
	switch runtime.GOOS {
	case "windows":
		own = filepath.Join(home, "Documents", "My Games", "Terraria", "Worlds")
	case "darwin":
		own = filepath.Join(home, "Library", "Application Support", "Terraria", "Worlds")
	}
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(own, "Forest.wld"), []byte("a Terraria world"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := &testEnv{data: t.TempDir()}
	fake := fakeInstalls(t)
	m := newTestManager(t, env.data, env, fake)
	root := m.gameDirs("terraria").Root
	add := func() error {
		t.Helper()
		cands, err := m.CatalogLocal("terraria")
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(cands, func(c worlds.Candidate) bool { return c.Name == "Forest" })
		if i < 0 {
			t.Fatalf("the owner's world isn't found: %+v", cands)
		}
		_, err = m.AddGame(AddGameRequest{Game: "terraria", ServerName: "Friends", Password: "secret12",
			World: AddWorld{Mode: "import", Candidate: cands[i].ID}})
		return err
	}
	copied := func() bool {
		_, err := os.Stat(filepath.Join(root, "saves", "Forest.wld"))
		return err == nil
	}

	// noList takes servers.json away, so the add fails after the import,
	// at the last step (servers.Update refuses a missing list); it answers
	// how to put it back.
	noList := func(m *Manager) func() {
		t.Helper()
		path := filepath.Join(m.opt.DataDir, servers.FileName)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		return func() {
			if err := os.WriteFile(path, b, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	putBack := noList(m)
	if err := add(); err == nil {
		t.Fatal("added a game with the list of games missing")
	} else if _, serr := os.Stat(root); serr == nil {
		t.Fatalf("a failed add (%v) left the game's folder", err)
	}
	if _, err := os.Stat(filepath.Join(own, "Forest.wld")); err != nil {
		t.Fatalf("the owner's own world is gone: %v", err)
	}
	// With the list back, the same add goes through, with its world.
	putBack()
	if err := add(); err != nil {
		t.Fatalf("adding with the list back: %v", err)
	}
	if !copied() {
		t.Fatal("the game was added without its world")
	}
	waitAddInstall(t, m, "terraria")
	if _, err := os.Stat(filepath.Join(own, "Forest.wld")); err != nil {
		t.Fatalf("the owner's own world is gone: %v", err)
	}

	// The worlds of a game removed earlier are kept.
	m2 := newTestManager(t, t.TempDir(), env, fake)
	root = m2.gameDirs("terraria").Root
	kept := filepath.Join(root, "saves", "Old.wld")
	if err := os.MkdirAll(filepath.Dir(kept), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kept, []byte("a world kept from before"), 0o644); err != nil {
		t.Fatal(err)
	}
	m = m2
	noList(m)
	if err := add(); err == nil {
		t.Fatal("added a game with the list of games missing")
	} else if copied() {
		t.Fatalf("a failed add (%v) left the copied world", err)
	}
	if _, serr := os.Stat(kept); serr != nil {
		t.Fatalf("a failed add removed a world that was there before: %v", serr)
	}
}

// --- small parts --------------------------------------------------------------------

func TestSizeMB(t *testing.T) {
	for mb, want := range map[int]string{512: "512 MB", 1024: "1 GB", 1536: "1.5 GB", 3170: "3.1 GB", 6349: "6.2 GB", 10240: "10 GB"} {
		if got := sizeMB(mb); got != want {
			t.Errorf("sizeMB(%d) = %q, want %q", mb, got, want)
		}
	}
}

// The running games are listed largest first, without one on its way out.
func TestRunningGamesLargestFirst(t *testing.T) {
	m := &Manager{}
	byID, order := map[string]*instance{}, []string{}
	for i, mb := range []uint64{100, 3000, 2000} {
		id := fmt.Sprintf("g%d", i)
		inst := &instance{id: id, info: game.Info{Short: id}}
		inst.run = &serverRun{inst: inst, memNow: mb << 20}
		byID[id] = inst
		order = append(order, id)
	}
	m.setInstancesLocked(byID, order) // the published list eachInstance reads
	byID["g0"].run.stopping = true
	got := m.runningGamesLocked()
	if len(got) != 2 || got[0].ID != "g1" || got[1].ID != "g2" || got[0].MemMB != 3000 {
		t.Fatalf("running = %+v", got)
	}
}
