package windrose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/game/gametest"
	"valheim-easy-server/internal/install"
	"valheim-easy-server/internal/platform"
)

// How friends join: the invite code from the running server's log, else
// the record's, else the one kept from the last start; a password and the
// server's version are steps of their own; the problems the owner must
// know about.
func TestJoin(t *testing.T) {
	s := Game{}.New(testEnv(t)).(*server)
	sv := defaultSaved("W")

	j := s.Join(game.JoinInput{Saved: sv})
	if j.How != "code" || j.CodeLabel != "Invite code" || j.Code != "" || j.Router || !j.Public || len(j.Steps) == 0 || j.Manual == "" || j.LAN == nil {
		t.Errorf("never started: %+v", j)
	}

	sv.State[stateInvite] = "d6221bb7"
	sv.Server["password"] = "parrot"
	j = s.Join(game.JoinInput{Saved: sv})
	if j.Code != "d6221bb7" || j.Password != "parrot" || !strings.Contains(strings.Join(j.Steps, " "), "d6221bb7") ||
		!strings.Contains(strings.Join(j.Steps, " "), "password") || !strings.Contains(j.Hint, "d6221bb7") {
		t.Errorf("stopped: %+v", j)
	}

	rec := recWith(map[string]string{"inviteCode": "fba3990a"})
	rec.Password = "run password"
	st := &game.RunState{Ready: true, Version: "0.10.0.9.32"}
	j = s.Join(game.JoinInput{Saved: sv, Rec: &rec, State: st})
	if j.Code != "fba3990a" || j.Password != "run password" || !strings.Contains(strings.Join(j.Steps, " "), "0.10.0.9.32") {
		t.Errorf("running: %+v", j)
	}
	st.Code = "c0ffee11"
	if j = s.Join(game.JoinInput{Saved: sv, Rec: &rec, State: st}); j.Code != "c0ffee11" || j.Problem != "" {
		t.Errorf("from the log: %+v", j)
	}

	st.Extra = map[string]string{"backend": "failed"}
	if j = s.Join(game.JoinInput{Saved: sv, Rec: &rec, State: st}); !strings.Contains(j.Problem, "online service") {
		t.Errorf("backend problem: %q", j.Problem)
	}
	sv.State[stateDirect] = "true"
	if j = s.Join(game.JoinInput{Saved: sv}); !strings.Contains(j.Problem, "UseDirectConnection") {
		t.Errorf("direct connection problem: %q", j.Problem)
	}
}

func TestValidate(t *testing.T) {
	s := Game{}.New(testEnv(t)).(*server)
	ok := defaultSaved("Crow's Nest")
	if errs := s.Validate(ok, ""); len(errs) != 0 {
		t.Errorf("defaults: %v", errs)
	}
	for _, c := range []struct {
		key, value, field string
	}{
		{"serverName", strings.Repeat("x", 200), "serverName"},
		{"serverName", "  ", "serverName"},
		{"serverName", "a\nb", "serverName"},
		{"password", strings.Repeat("p", 65), "password"},
		{"password", "tab\there", "password"},
		{"password", " spaced", "password"},
		{"maxPlayers", "0", "maxPlayers"},
		{"maxPlayers", "11", "maxPlayers"},
		{"maxPlayers", "four", "maxPlayers"},
		{"region", "US", "region"},
	} {
		sv := ok.Clone()
		sv.Server[c.key] = c.value
		if errs := s.Validate(sv, ""); errs[c.field] == "" {
			t.Errorf("%s=%q: no error for %s (%v)", c.key, c.value, c.field, errs)
		}
	}
	if errs := s.Validate(ok, "0123456789ABCDEF0123456789ABCDEF"); errs["world"] == "" {
		t.Errorf("a world named like an id: %v", errs)
	}
	if errs := s.Validate(ok, ""); errs["world"] != "" {
		t.Errorf("\"\" is the selected world: %v", errs)
	}
}

// The stop: one Ctrl+C, never sent again (Unreal takes a second one as
// "force quit"), waiting long enough for a world that is still loading.
// The Windows server gets it as a call that falls back to closing its
// window; that path only runs on Windows (CI).
func TestStop(t *testing.T) {
	s := Game{}.New(testEnv(t)).(*server)
	steps := s.Stop(game.Record{PID: 1})
	if len(steps) != 1 || steps[0].Wait != stopWait || steps[0].Resend != 0 {
		t.Fatalf("steps %+v", steps)
	}
	if steps[0].Kind != "interrupt" {
		t.Errorf("on %s the step is %q", "this system", steps[0].Kind)
	}
	s.env.Fake = &game.Fake{}
	if steps := s.Stop(game.Record{}); steps[0].Kind != "interrupt" {
		t.Errorf("fake mode: %q", steps[0].Kind)
	}
}

// ctrlC never signals a process that isn't the server any more.
func TestCtrlCChecksTheProcess(t *testing.T) {
	err := ctrlC(game.Record{PID: os.Getpid(), Started: time.Now().Add(-time.Hour)})(t.Context())
	if err == nil || !strings.Contains(err.Error(), "no longer the Windrose server") {
		t.Errorf("err %v", err)
	}
}

func TestExitHint(t *testing.T) {
	s := Game{}.New(testEnv(t)).(*server)
	for line, want := range map[string]string{
		"": "Visual C++",
		"[x]R5LogCoopProxy: Error: Another instance of server is running from the same folder. ProjectDir 'C:/x'": "Another copy",
		"[x]R5LogCoopProxy: Error: Cannot load island 'first'":                                                    "couldn't load the world",
		"[x]R5LogCoopProxy: Error: GcStream is broken. ErrorMessage 'x'":                                          "online service",
		"[x]LogWindows: Error: appError called: Fatal error: [File:x]":                                            "crashed",
	} {
		if got := s.ExitHint([]string{line}); !strings.Contains(got, want) {
			t.Errorf("ExitHint(%q) = %q, want it to mention %q", line, got, want)
		}
	}
	if got := s.ExitHint([]string{"[x]LogExit: Exiting."}); got != "" {
		t.Errorf("a normal end: %q", got)
	}
}

// A server found running without its run file is followed in its own log.
func TestRecover(t *testing.T) {
	s := Game{}.New(testEnv(t)).(*server)
	s.store.Save(func(sv *game.Saved) error { *sv = defaultSaved("W"); return nil })
	at := time.Now()
	rec := s.Recover(42, at)
	if rec.PID != 42 || !rec.Started.Equal(at) || rec.LogPath != s.logPath() || rec.Exe != s.Program() || rec.World != "W" {
		t.Errorf("record %+v", rec)
	}
}

// The real installer: the Visual C++ runtime first (a "No" to Windows'
// prompt stops it before the download), then SteamCMD; a repair always
// runs the runtime's installer.
func TestInstallerOrder(t *testing.T) {
	old := needsVCRuntime
	t.Cleanup(func() { needsVCRuntime = old })
	needsVCRuntime = true
	var order []string
	ri := &realInstaller{
		prereq: func(_ context.Context, p install.Prereq, _ func(game.Progress)) error {
			order = append(order, "prereq installed-check="+map[bool]string{true: "yes", false: "no"}[p.Installed != nil])
			return nil
		},
		steam: installerFunc(func() error { order = append(order, "steam"); return nil }),
	}
	if err := ri.Install(t.Context(), false, nil); err != nil {
		t.Fatal(err)
	}
	if err := ri.Install(t.Context(), true, nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"prereq installed-check=yes", "steam", "prereq installed-check=no", "steam"}
	if !slices.Equal(order, want) {
		t.Errorf("order %q, want %q", order, want)
	}
	order = nil
	ri.prereq = func(context.Context, install.Prereq, func(game.Progress)) error { return errors.New("said no") }
	if err := ri.Install(t.Context(), false, nil); err == nil || len(order) != 0 {
		t.Errorf("a refused runtime: %v, %q", err, order)
	}
}

type installerFunc func() error

func (f installerFunc) Install(context.Context, bool, func(game.Progress)) error { return f() }

// Worlds: named from State first, else WorldName, else the id; a world
// the server has only in its backups is still one; the server's loaded
// world isn't copied while it runs.
func TestWorlds(t *testing.T) {
	notRunning(t)
	s := installed(t, "ServerDescription-publisher-example.json")
	a, b, c, d := exampleWorld, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
	makeWorld(t, s, a, "The Archipelago")
	makeWorld(t, s, b, "The Archipelago")
	makeWorld(t, s, c, "Bad/Name")
	// Only the server's backups of d are left.
	makeWorld(t, s, d, "Gone live")
	os.RemoveAll(filepath.Join(s.savesDir(), liveRoot, "0.10.0", worldsFolder, d))
	// A live folder with nothing in it isn't a world.
	os.MkdirAll(filepath.Join(s.savesDir(), liveRoot, "0.10.0", worldsFolder, "DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD"), 0o755)
	s.store.Save(func(sv *game.Saved) error {
		*sv = defaultSaved("Home")
		sv.State[statePrefix+b] = "Home"
		return nil
	})
	got := worldList(t, s)
	want := []string{"Home", "The Archipelago", c, d}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("worlds %q, want %q", got, want)
	}
	if w, err := s.Worlds().Get("Home"); err != nil || w.Size == 0 {
		t.Errorf("Get: %+v %v", w, err)
	}
	if err := s.Worlds().Rename("Home", "Other"); !errors.Is(err, game.ErrCantRename) {
		t.Errorf("Rename: %v", err)
	}

	// A backup holds both of the world's folders and restores them.
	bk, err := s.Worlds().Backup("The Archipelago", "test")
	if err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(s.savesDir(), liveRoot, "0.10.0", worldsFolder, a)
	os.WriteFile(filepath.Join(live, "CURRENT"), []byte("changed"), 0o644)
	if err := s.Worlds().Restore("The Archipelago", bk.ID); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(live, "CURRENT")); string(b) != "MANIFEST-000001\n" {
		t.Errorf("after the restore CURRENT is %q", b)
	}
	if _, err := os.Stat(filepath.Join(s.savesDir(), backupRoot, "0.10.0", worldsFolder, a)); err != nil {
		t.Errorf("the server's own backups weren't restored: %v", err)
	}

	// While the server runs, the world it loaded (the file's
	// WorldIslandId) isn't copied; another one is.
	running = func(string) bool { return true }
	if _, err := s.Worlds().Backup("The Archipelago", "test"); !errors.Is(err, errStopFirst) {
		t.Errorf("backup of the running world: %v", err)
	}
	if _, err := s.Worlds().Backup("Home", "test"); err != nil {
		t.Errorf("backup of another world while it runs: %v", err)
	}
	if p := s.Worlds().NameProblem(a); p == "" {
		t.Errorf("a name like an id is allowed")
	}
}

// The stand-in's lists of accounts alone (no account lines) keep who is
// on right through gametest, and a new world the server makes under an
// id of its own keeps the owner's name.
func TestStandInOptions(t *testing.T) {
	fake := gametest.BuildFakeserver(t)
	s := gametest.Start(t, Game{}, gametest.Options{Fake: fake, World: "First"})
	s.WaitEvent("online", "", 30*time.Second)
	s.Stop()

	srv := s.Server().(*server)
	conf := filepath.Join(filepath.Dir(srv.Program()), "fakeserver.json")
	os.WriteFile(conf, []byte(`{"script": "join:Alex@0.5,join:Sam@1,leave:Alex@2", "speed": 60, "options": {"dumpsOnly": "true", "ownWorldId": "true"}}`), 0o644)
	s.StartAgain()
	s.WaitEvent("join", "Alex", 20*time.Second)
	s.WaitEvent("join", "Sam", 20*time.Second)
	s.WaitEvent("leave", "Alex", 20*time.Second)
	if st := s.State(); st.Count != 1 || st.Players[0].Name != "Sam" || len(st.Players[0].Account) != 32 {
		t.Errorf("state %+v", st)
	}
	s.Stop()

	// A second world: the stand-in makes it under its own id.
	srv.store.Save(func(sv *game.Saved) error { sv.World = "Second"; return nil })
	plan, err := srv.Plan(t.Context(), game.PlanRequest{World: "Second", New: !srv.Worlds().Exists("Second"), Saved: srv.store.Get()})
	if err != nil {
		t.Fatal(err)
	}
	planned := extraString(plan.Record, "worldId")
	if err := runOnce(t, plan); err != nil {
		t.Fatal(err)
	}
	if !srv.Worlds().Exists("Second") || !srv.Worlds().Exists("First") {
		t.Fatalf("worlds %q", worldList(t, srv))
	}
	d, _ := readDescription(srv.descriptionPath())
	if d.str(keyWorld) == planned {
		t.Errorf("the stand-in didn't use an id of its own")
	}
	if _, err := srv.Plan(t.Context(), game.PlanRequest{World: "Second", Saved: srv.store.Get()}); err != nil {
		t.Fatal(err)
	}
	if st := srv.store.Get().State; st[statePrefix+strings.ToUpper(d.str(keyWorld))] != "Second" {
		t.Errorf("State %v", st)
	}
}

// runOnce starts a plan's program, waits for its ready line, stops it with
// Ctrl+C and waits for it to exit, as the app would.
func runOnce(t *testing.T, plan game.Plan) error {
	t.Helper()
	p, err := startServer(platform.StartOptions{Exe: plan.Exe, Args: plan.Args, Dir: plan.Dir, Env: os.Environ()})
	if err != nil {
		return err
	}
	defer p.Close()
	waitUntil(t, 30*time.Second, "the ready line", func() bool {
		b, _ := os.ReadFile(plan.LogPath)
		return reReady.Match(b)
	})
	if err := p.Interrupt(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() { p.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-time.After(30 * time.Second):
		p.Kill()
		return errors.New("the stand-in didn't stop")
	}
}
