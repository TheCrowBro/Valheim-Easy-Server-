package satisfactory

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/golden")

// golden compares got with testdata/golden/<name> (go test -update rewrites it).
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update to make it)", err)
	}
	if strings.ReplaceAll(string(want), "\r\n", "\n") != got {
		t.Errorf("%s differs from the golden file:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

// setupFixture is an instance whose server is the recording fake API.
type setupFixture struct {
	t    *testing.T
	s    *server
	api  *fakeAPI
	runs int
}

func newSetupFixture(t *testing.T) *setupFixture {
	t.Helper()
	old, oldGone := newSecret, runGone
	newSecret = func() string { return "app-made-admin-password" }
	// The records here name this test's own process with made-up start
	// times, which the real check would take for another program.
	runGone = func(game.Record) bool { return false }
	t.Cleanup(func() { newSecret, runGone = old, oldGone })
	s := testServer(t, true)
	t.Cleanup(func() { stopSetups(s) }) // before the replaced functions are put back
	api := newFakeAPI(t, s.savesDir())
	s.store.Save(func(sv *game.Saved) error {
		sv.Ports = map[string]int{"game": api.port(), "reliable": 8888}
		return nil
	})
	return &setupFixture{t: t, s: s, api: api}
}

// rec is the record of a new run, as the plan makes it.
func (f *setupFixture) rec(isNew bool, start string) game.Record {
	f.t.Helper()
	f.runs++
	sv := f.s.store.Get()
	gamePort, reliablePort := ports(sv)
	rec := game.Record{PID: os.Getpid(), Started: time.Unix(1700000000+int64(f.runs), 0), World: sv.World,
		Server: serverName(sv), Port: gamePort, Password: sv.Server["password"],
		Ports: map[string]int{"game": gamePort, "reliable": reliablePort}, Secrets: map[string]string{}}
	if a := sv.State[stateAdmin]; a != "" {
		rec.Secrets[secretAdmin] = a
	}
	if isNew {
		b, _ := json.Marshal(newWorld{StartingLocation: start})
		rec.Extra = map[string]json.RawMessage{extraNewWorld: b}
	}
	return rec
}

// after runs After for rec and returns the calls it made.
func (f *setupFixture) after(rec game.Record) (string, error) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	extra, err := f.s.After(ctx, rec)
	if err == nil && string(extra[extraSetUp]) == "" {
		f.t.Errorf("After's extra = %v: want the setUp marker", extra)
	}
	return strings.Join(f.api.Calls(), "\n") + "\n", err
}

// The first start of a new install: the app claims the server with a
// password it makes, sets the friends' password, makes the world at the
// chosen start, saves it, and makes it the one loaded at start. The second
// start finds it all done and only checks.
func TestSetupFirstAndSecondStart(t *testing.T) {
	f := newSetupFixture(t)
	f.s.store.Save(func(sv *game.Saved) error { sv.Server["password"] = "letmein"; return nil })
	rec := f.rec(true, "Rocky Desert")
	if !f.s.Pending(rec) {
		t.Errorf("Pending is false for a run whose setup never ran")
	}
	calls, err := f.after(rec)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "setup-first-start.txt", calls)

	st := f.s.store.Get()
	if st.Server["adminPassword"] != "app-made-admin-password" || st.State[stateAdmin] != "app-made-admin-password" {
		t.Errorf("the admin password isn't kept: settings %q, state %q", st.Server["adminPassword"], st.State[stateAdmin])
	}
	if st.State[stateClaimed] == "" || st.State[stateClaimPort] != itoa(f.api.port()) || st.State[stateCertPort] != itoa(f.api.port()) ||
		!strings.HasPrefix(st.State[stateCert], "SHA256:") || st.State[stateName] != "Factory Friends" {
		t.Errorf("State after the first setup = %v", st.State)
	}
	if !f.s.worlds.Exists("Factory") {
		t.Errorf("the new world isn't on disk after the setup")
	}

	f.api.restart()
	rec = f.rec(false, "")
	if rec.Secrets[secretAdmin] != "app-made-admin-password" {
		t.Errorf("the second run's record has secrets %v", rec.Secrets)
	}
	calls, err = f.after(rec)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "setup-second-start.txt", calls)
}

// Choosing another world: a new one is made, an existing one is loaded
// from its newest save, and each becomes the one loaded at start.
func TestSetupOtherWorlds(t *testing.T) {
	f := newSetupFixture(t)
	if _, err := f.after(f.rec(true, "Grass Fields")); err != nil {
		t.Fatal(err)
	}
	writeTestSave(t, f.s.savesDir(), "Factory_autosave_0", "Factory", time.Now().Add(time.Minute))

	f.api.restart()
	f.s.store.Save(func(sv *game.Saved) error { sv.World = "Island"; return nil })
	// "Let the game choose" reaches the API as an empty start.
	calls, err := f.after(f.rec(true, startingLocation(game.Values{"startingLocation": randomStart})))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(calls, `CreateNewGame(newGameData={"advancedGameSettings":{},"bSkipOnboarding":true,"customOptionsOnlyForModding":{},"gameModeSettings":{},"mapName":"","sessionName":"Island","startingLocation":""}) as Administrator`) {
		t.Errorf("no CreateNewGame for the new world at a random start:\n%s", calls)
	}

	f.api.restart()
	f.s.store.Save(func(sv *game.Saved) error { sv.World = "Factory"; return nil })
	calls, err = f.after(f.rec(false, ""))
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "setup-load-other-world.txt", calls)
}

// The owner set the server up by hand before: the app can't log in until
// it is told the admin password, and then keeps it.
func TestSetupClaimedElsewhere(t *testing.T) {
	f := newSetupFixture(t)
	f.api.set(func(a *fakeAPI) { a.claimed, a.admin, a.name = true, "owners-own", "Old name" })
	_, err := f.after(f.rec(true, "Grass Fields"))
	if err == nil || !strings.Contains(err.Error(), "admin password the app doesn't know") {
		t.Fatalf("After = %v; want the explanation that the admin password is needed", err)
	}
	f.s.store.Save(func(sv *game.Saved) error { sv.Server["adminPassword"] = "owners-own"; return nil })
	if _, err := f.after(f.rec(true, "Grass Fields")); err != nil {
		t.Fatal(err)
	}
	if st := f.s.store.Get().State; st[stateAdmin] != "owners-own" || st[stateClaimed] == "" {
		t.Errorf("State = %v; want the owner's admin password kept and the claim noted", st)
	}
	f.api.set(func(a *fakeAPI) {
		if a.name != "Factory Friends" {
			t.Errorf("the server's name is %q; want the settings' name", a.name)
		}
	})
}

// A new admin password typed in the settings is given to the server at the
// next start, and logging in goes on with it.
func TestSetupAdminPasswordChange(t *testing.T) {
	f := newSetupFixture(t)
	if _, err := f.after(f.rec(true, "Grass Fields")); err != nil {
		t.Fatal(err)
	}
	f.api.restart()
	f.s.store.Save(func(sv *game.Saved) error { sv.Server["adminPassword"] = "a-new-one"; return nil })
	calls, err := f.after(f.rec(false, ""))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(calls, "SetAdminPassword(password=<new admin password>) as Administrator") {
		t.Errorf("no SetAdminPassword:\n%s", calls)
	}
	if st := f.s.store.Get().State; st[stateAdmin] != "a-new-one" {
		t.Errorf("State admin = %q", st[stateAdmin])
	}
	f.api.set(func(a *fakeAPI) {
		if a.admin != "a-new-one" {
			t.Errorf("the server's admin password is %q", a.admin)
		}
	})
	// Clearing the box puts back the one the server has.
	f.api.restart()
	f.s.store.Save(func(sv *game.Saved) error { sv.Server["adminPassword"] = ""; return nil })
	if _, err := f.after(f.rec(false, "")); err != nil {
		t.Fatal(err)
	}
	if got := f.s.store.Get().Server["adminPassword"]; got != "a-new-one" {
		t.Errorf("after clearing it, the settings' admin password is %q", got)
	}
}

// The server keeps its claim per game port, so after the app picks new
// ports the server there is unclaimed, and the app claims it again with
// the same admin password.
func TestSetupNewPortClaimsAgain(t *testing.T) {
	f := newSetupFixture(t)
	if _, err := f.after(f.rec(true, "Grass Fields")); err != nil {
		t.Fatal(err)
	}
	moved := newFakeAPI(t, f.s.savesDir())
	f.api = moved
	f.s.store.Save(func(sv *game.Saved) error { sv.Ports["game"] = moved.port(); return nil })
	calls, err := f.after(f.rec(false, ""))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(calls, "ClaimServer(adminPassword=<admin password>, serverName=\"Factory Friends\") as InitialAdmin") {
		t.Errorf("no claim on the new port:\n%s", calls)
	}
	moved.set(func(a *fakeAPI) {
		if a.admin != "app-made-admin-password" {
			t.Errorf("claimed with %q, not the app's admin password", a.admin)
		}
	})
	if st := f.s.store.Get().State; st[stateClaimPort] != itoa(moved.port()) || st[stateCertPort] != itoa(moved.port()) {
		t.Errorf("State = %v; want the claim and the key noted for the new port", st)
	}
}

// The probe calls the server up only once the run's world plays and the
// setup is done, and counts who is on; the probe itself starts the setup
// when nothing has (a test harness without After, or an app that took the
// server over).
func TestProbeStartsSetupAndCounts(t *testing.T) {
	f := newSetupFixture(t)
	rec := f.rec(true, "Grass Fields")
	deadline := time.Now().Add(20 * time.Second)
	var res game.ProbeResult
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		var err error
		res, err = f.s.Probe(ctx, rec)
		cancel()
		if err == nil && res.Up {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the probe never said up: %+v, %v", res, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if res.Count != 0 {
		t.Errorf("Count = %d with nobody on", res.Count)
	}
	f.api.set(func(a *fakeAPI) { a.players = 2 })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if res, err := f.s.Probe(ctx, rec); err != nil || !res.Up || res.Count != 2 || res.Names != nil {
		t.Errorf("Probe = %+v, %v; want up, 2 on, no names", res, err)
	}
	// The probe reuses its login.
	f.api.Calls()
	f.s.Probe(ctx, rec)
	if calls := f.api.Calls(); len(calls) != 1 || !strings.HasPrefix(calls[0], "QueryServerState() as Administrator") {
		t.Errorf("a later probe called %q; want one QueryServerState with the kept token", calls)
	}
	// Playing another world than the run's isn't up.
	f.api.set(func(a *fakeAPI) { a.session = "Something else" })
	if res, err := f.s.Probe(ctx, rec); err != nil || res.Up {
		t.Errorf("Probe while another world plays = %+v, %v; want not up", res, err)
	}
}

// The stop saves the world being played under its own name, then shuts
// the server down; after the server restarted with a new token, it logs in
// again by itself.
func TestStopSavesThenShutsDown(t *testing.T) {
	f := newSetupFixture(t)
	rec := f.rec(true, "Grass Fields")
	if _, err := f.after(rec); err != nil {
		t.Fatal(err)
	}
	f.api.set(func(a *fakeAPI) { a.tokens = map[string]string{} }) // the kept token no longer works
	steps := f.s.Stop(rec)
	if len(steps) != 2 || steps[0].Kind != "call" || steps[1].Kind != "interrupt" || steps[0].Wait != stopWait || steps[1].Wait != interruptWait || steps[1].Resend != 0 || steps[1].Say == "" {
		t.Fatalf("Stop = %+v", steps)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := steps[0].Call(ctx); err != nil {
		t.Fatal(err)
	}
	golden(t, "stop-calls.txt", strings.Join(f.api.Calls(), "\n")+"\n")
}

// A run taken over by a new copy of the app runs the setup again only when
// the record says it never finished.
func TestPending(t *testing.T) {
	s := testServer(t, true)
	if !s.Pending(game.Record{}) {
		t.Errorf("Pending(no marker) = false")
	}
	if s.Pending(game.Record{Extra: map[string]json.RawMessage{extraSetUp: json.RawMessage(`"Factory"`)}}) {
		t.Errorf("Pending(marker) = true")
	}
}

// A save imported under a file name of its own: starting that name loads
// it, and the selected world becomes the session inside it, which is how
// the Worlds page lists it.
func TestSetupImportedSave(t *testing.T) {
	f := newSetupFixture(t)
	writeTestSave(t, f.s.savesDir(), "My upload", "Home Base", time.Now())
	f.s.store.Save(func(sv *game.Saved) error { sv.World = "My upload"; return nil })
	if !f.s.worlds.Exists("My upload") {
		t.Fatalf("the imported file's name isn't a world, so a start would make a new one")
	}
	calls, err := f.after(f.rec(false, ""))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(calls, `LoadGame(enableAdvancedGameSettings=false, saveName="My upload") as Administrator`) ||
		!strings.Contains(calls, `SetAutoLoadSessionName(sessionName="Home Base") as Administrator`) {
		t.Errorf("calls:\n%s", calls)
	}
	if w := f.s.store.Get().World; w != "Home Base" {
		t.Errorf("the selected world is %q, not the session's name", w)
	}
}

// A server that forgot its claim (its settings file was deleted) is claimed
// again, rather than reported as set up by someone else.
func TestSetupServerForgotItsClaim(t *testing.T) {
	f := newSetupFixture(t)
	if _, err := f.after(f.rec(true, "Grass Fields")); err != nil {
		t.Fatal(err)
	}
	f.api.set(func(a *fakeAPI) { a.claimed, a.admin = false, "" })
	f.api.restart()
	calls, err := f.after(f.rec(false, ""))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(calls, "ClaimServer(adminPassword=<admin password>, serverName=\"Factory Friends\") as InitialAdmin") {
		t.Errorf("not claimed again:\n%s", calls)
	}
}

// A setup still waiting for an earlier run's server stops when a new run
// starts, so two setups never drive one server.
func TestNewRunCancelsOldSetup(t *testing.T) {
	s := testServer(t, true)
	old := runGone
	runGone = func(game.Record) bool { return false }
	defer func() { runGone = old }()
	// Port 1 has nothing listening: the first setup keeps waiting.
	s.store.Save(func(sv *game.Saved) error { sv.Ports["game"] = 1; return nil })
	first := s.ensureSetup(game.Record{PID: 1, Started: time.Unix(1, 0), World: "Factory", Ports: map[string]int{"game": 1}})
	s.ensureSetup(game.Record{PID: 2, Started: time.Unix(2, 0), World: "Factory", Ports: map[string]int{"game": 1}})
	select {
	case <-first.done:
		if first.err == nil {
			t.Errorf("the cancelled setup says it finished well")
		}
	case <-time.After(5 * time.Second):
		t.Errorf("the first run's setup is still waiting after a new run started")
	}
	stopSetups(s)
}

// stopSetups cancels the setups still running and waits for them, before a
// test puts back what it replaced.
func stopSetups(s *server) {
	s.setupMu.Lock()
	var runs []*setupRun
	for _, r := range s.setups {
		r.cancel()
		runs = append(runs, r)
	}
	s.setupMu.Unlock()
	for _, r := range runs {
		select {
		case <-r.done:
		case <-time.After(15 * time.Second):
		}
	}
}
