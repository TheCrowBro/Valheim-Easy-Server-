package palworld

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/game/gametest"
)

// Everything every adapter must do, against the stand-in (cmd/fakeserver's
// palworld personality): install, a new world, online, the scripted join
// and leave through the REST API, Join, the stop through the REST API's
// save and shutdown, a second start on the same world, a takeover whose
// stop still reaches the REST API from the run record, and a backup that
// restores.
func TestConformance(t *testing.T) {
	gametest.Conformance(t, Game{}, gametest.Options{})
}

// The registry knows Palworld once the package is imported (the lead adds
// internal/games/all/palworld.go after the R2 cut).
func TestRegistered(t *testing.T) {
	g, ok := game.Get(ID)
	if !ok {
		t.Fatal("palworld isn't registered")
	}
	in := g.Info()
	if in.Command != "palworld" || in.Stage != game.Preview || in.Legacy || in.Order != 80 {
		t.Errorf("Info: command %q, stage %q, legacy %v, order %d", in.Command, in.Stage, in.Legacy, in.Order)
	}
	if in.OS["windows"] != game.Runs || in.OS["darwin"] == game.Runs || in.OS["darwin/amd64"] == game.Runs || in.NeedsText["darwin"] != "Needs Windows" {
		t.Errorf("Info.OS = %v, NeedsText = %v: Windows only, \"Needs Windows\" on a Mac", in.OS, in.NeedsText)
	}
	want := []game.PortRole{
		{Role: "game", Label: "Game port", Protos: []string{"udp"}, Default: 8211, Public: true},
		{Role: "rest", Label: "REST API port (only for the app)", Protos: []string{"tcp"}, Default: 8212},
	}
	if !slices.EqualFunc(in.Ports, want, func(a, b game.PortRole) bool {
		return a.Role == b.Role && a.Default == b.Default && slices.Equal(a.Protos, b.Protos) && a.Public == b.Public
	}) {
		t.Errorf("Info.Ports = %+v, want the public UDP game port 8211 and the private TCP REST port 8212", in.Ports)
	}
	if in.Presence != game.ByName || in.Deaths || in.Import || in.Lists || in.Mods || in.Password.Use != "optional" {
		t.Errorf("Info = %+v", in)
	}
}

// A new world gets an id the app chose before the start, written as
// DedicatedServerName, and the stand-in (like the real server) makes the
// world in that folder: the world is listed under its name, and the second
// start loads the same folder. A second new world gets a folder of its own.
func TestNewWorldsKeepTheirNames(t *testing.T) {
	s := gametest.Start(t, Game{}, gametest.Options{World: "Island", Script: "join:Alex@1"})
	s.WaitEvent("online", "", 30*time.Second)
	s.WaitEvent("join", "Alex", 15*time.Second)
	srv := s.Server().(*server)
	st := s.Env().Store.Get().State
	id := st[statePlanned]
	if !reWorldID.MatchString(id) || st[statePrefix+id] != "Island" {
		t.Fatalf("State = %v: want the new world's id recorded with its name", st)
	}
	if got := srv.currentWorldChoice(); got != id {
		t.Errorf("GameUserSettings.ini names %q, not the planned %q", got, id)
	}
	waitFile(t, filepath.Join(srv.savesDir(), id, levelFile))

	// The REST API says the same world runs.
	c, err := restFor(s.Record())
	if err != nil {
		t.Fatal(err)
	}
	in, err := c.info(context.Background())
	if err != nil || in.WorldGUID != id {
		t.Errorf("REST info = %+v, %v; want worldguid %s", in, err, id)
	}
	if st := s.State(); len(st.Players) != 1 || st.Players[0].Account == "" {
		t.Errorf("players = %+v: want Alex with the user id from the join line", st.Players)
	}
	s.Stop()

	// A second new world gets its own id; the first keeps its own.
	sv := s.Env().Store.Get()
	sv.World = "Valley"
	if _, err := srv.Plan(context.Background(), game.PlanRequest{World: "Valley", New: true, Saved: sv}); err != nil {
		t.Fatal(err)
	}
	st = s.Env().Store.Get().State
	id2 := st[statePlanned]
	if id2 == id || !reWorldID.MatchString(id2) || st[statePrefix+id2] != "Valley" || st[statePrefix+id] != "Island" {
		t.Fatalf("after planning a second world, State = %v (the first world's id is %s)", st, id)
	}
	if got := srv.currentWorldChoice(); got != id2 {
		t.Errorf("GameUserSettings.ini names %q, not the second world's %q", got, id2)
	}
	// Until the server has saved it, the second world isn't there yet.
	if got := worldNames(t, srv.Worlds()); !slices.Equal(got, []string{"Island"}) {
		t.Errorf("worlds = %q, want Island only", got)
	}
	// Going back to the first world plans its own folder again.
	sv.World = "Island"
	if _, err := srv.Plan(context.Background(), game.PlanRequest{World: "Island", Saved: sv}); err != nil {
		t.Fatal(err)
	}
	if got := srv.currentWorldChoice(); got != id {
		t.Errorf("GameUserSettings.ini names %q, not Island's %q", got, id)
	}
}

// The probe names who is on through the REST API, and counts them.
func TestProbeOnStandIn(t *testing.T) {
	s := gametest.Start(t, Game{}, gametest.Options{Script: "join:Alex@0.5,join:Sam Smith@0.5"})
	s.WaitEvent("online", "", 30*time.Second)
	s.WaitEvent("join", "Alex", 15*time.Second)
	s.WaitEvent("join", "Sam Smith", 15*time.Second)
	res, err := s.Server().(game.Prober).Probe(context.Background(), s.Record())
	if err != nil || !res.Up || res.Count != 2 || !slices.Equal(sorted(res.Names), []string{"Alex", "Sam Smith"}) || res.Version != "v1.0.5.102999" {
		t.Errorf("Probe = %+v, %v", res, err)
	}
	// A wrong password is turned away, and says so.
	rec := s.Record()
	rec.Secrets = map[string]string{restSecret: "wrong"}
	if _, err := s.Server().(game.Prober).Probe(context.Background(), rec); err == nil || !strings.Contains(err.Error(), "admin password") {
		t.Errorf("Probe with a wrong password: %v", err)
	}
	s.Stop()
}

// A server found running without its run file is given the REST API's
// port and this run's admin password from the settings file, so it can
// still be stopped through the API.
func TestRecoverReadsTheAPIBack(t *testing.T) {
	s := gametest.Start(t, Game{}, gametest.Options{})
	s.WaitEvent("online", "", 30*time.Second)
	rec := s.Record()
	got := s.Server().(game.Recoverer).Recover(rec.PID, rec.Started)
	if got.Ports["rest"] != rec.Ports["rest"] || got.Secrets[restSecret] != rec.Secrets[restSecret] || got.Exe != rec.Exe || got.World != "Conformance" {
		t.Errorf("Recover = %+v\nrun record = %+v", got, rec)
	}
	if got.LogPath != rec.LogPath {
		t.Errorf("Recover's log = %q, want the newest, %q", got.LogPath, rec.LogPath)
	}
	if err := restStop(context.Background(), got); err != nil {
		t.Errorf("stopping through the recovered record: %v", err)
	}
	s.WaitEvent("stopping", "", 15*time.Second)
}

func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never appeared", path)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func worldNames(t *testing.T, w game.Worlds) []string {
	t.Helper()
	list, _, err := w.List()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, x := range list {
		out = append(out, x.Name)
	}
	return out
}

func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}
