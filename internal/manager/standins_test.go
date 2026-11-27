package manager

// Test helpers that more than one stream's tests use, kept in one place so
// each stream's tests add games and stand-ins the same way, the way R3's
// manager does: an instance is only seen once it is in the published list
// (addInstanceLocked, which eachInstance and instanceByID read), and a
// game's servers.json entry is saved through updateServers, as Add a game
// saves it, so code that changes the list later finds it on disk too.

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/records"
	"valheim-easy-server/internal/servers"
	"valheim-easy-server/internal/worlds"
)

// testStore keeps a test game's settings in memory, as servers.json does
// for the app. saves counts the saves that went through.
type testStore struct {
	mu    sync.Mutex
	saved game.Saved
	saves int
}

func (s *testStore) Get() game.Saved {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saved.Clone()
}

func (s *testStore) Save(change func(*game.Saved) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.saved.Clone()
	if err := change(&c); err != nil {
		return err
	}
	s.saved, s.saves = c, s.saves+1
	return nil
}

// hookLink is the webhook link of the stand-in webhook id. The stand-ins
// answer for any token.
func hookLink(id string) string { return "https://discord.com/api/webhooks/" + id + "/tok" + id }

// set replaces a variable of the app's for one test.
func set[T any](t *testing.T, v *T, to T) {
	old := *v
	*v = to
	t.Cleanup(func() { *v = old })
}

// putInstance adds inst to the manager's games the way Add a game does.
// With entry set, its servers.json entry is saved first (updateServers, so
// the file and the published list both have it). It may be called before
// or after Start, without m.mu.
func putInstance(t *testing.T, m *Manager, inst *instance, entry *servers.Instance) {
	t.Helper()
	if entry != nil {
		if err := m.updateServers(func(f *servers.File) error {
			f.Instances = append(f.Instances, *entry)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	m.mu.Lock()
	m.addInstanceLocked(inst)
	m.mu.Unlock()
}

// newTestInstance makes an instance of g the way newGameInstance does for a
// servers.json entry (its folders under games/<id>, a Note that stamps the
// game on its activity entries), but with store as its game.Store, so the
// test keeps the game's settings itself.
func newTestInstance(m *Manager, g game.Game, store game.Store) *instance {
	info := g.Info()
	inst := &instance{id: info.ID, g: g, info: info, dirs: m.gameDirs(info.ID), known: map[string]*KnownPlayer{},
		uploads: map[string]*worlds.Upload{}, logs: newRing[LogLine](3000),
		recs: recordsData{Runs: map[string][]records.Event{}}}
	inst.srv = g.New(game.Env{Instance: info.ID, Data: m.opt.DataDir, Dirs: inst.dirs, Log: m.log, Store: store,
		Note: func(kind, text string) { m.instActivity(inst, kind, "", text) }})
	return inst
}

// addTestGame adds an instance of g beside the legacy one, as Add a game
// does: its servers.json entry (d nil: its Discord settings were never
// changed) and the instance. Like New, it reads none of the game's files:
// its records are read the first time they're needed.
func addTestGame(t *testing.T, m *Manager, g game.Game, d *servers.Discord) *instance {
	t.Helper()
	inst := newTestInstance(m, g, &testStore{})
	putInstance(t, m, inst, &servers.Instance{ID: inst.id, Game: inst.id, Added: time.Now(), Discord: d})
	return inst
}

// --- a stand-in game ---------------------------------------------------------------

// testGame is a game made up in the test, with just enough of an adapter
// for Discord's posts, the records and Status (testServer).
type testGame struct{ info game.Info }

func (g testGame) Info() game.Info { return g.info }

func (g testGame) New(env game.Env) game.Server {
	return &testServer{info: g.info, dir: env.Dirs.Server, store: &testStore{saved: game.Saved{
		Server: game.Values{"serverName": g.info.Name + " Night"}, World: "Home"}}}
}

// testServer is testGame's server. Anything it doesn't answer isn't called
// in these tests (it would panic).
type testServer struct {
	game.Server
	info  game.Info
	dir   string
	store *testStore
}

func (s *testServer) Store() game.Store                            { return s.store }
func (s *testServer) Program() string                              { return filepath.Join(s.dir, "not-installed") }
func (s *testServer) Texts() game.Texts                            { return game.Texts{Players: "players", Player: "player"} }
func (s *testServer) Fingerprint(game.Saved, string) string        { return "" }
func (s *testServer) Validate(game.Saved, string) game.FieldErrors { return nil }
func (s *testServer) Worlds() game.Worlds                          { return testWorlds{} }
func (s *testServer) Join(in game.JoinInput) game.Join {
	j := game.Join{How: "address", Port: 7777, LAN: []string{}, Steps: []string{}, Hint: "In " + s.info.Short + ", choose Join."}
	if s.info.Presence == game.Unknown { // like Windrose: an invite code
		j.How, j.CodeLabel, j.Code, j.Port = "code", "Invite code", "Ab12Cd", 0
	}
	return j
}

// testWorlds is testServer's worlds: none yet, and nothing to tidy up.
type testWorlds struct{ game.Worlds }

func (testWorlds) CleanStaging(time.Duration) {}
func (testWorlds) List() ([]worlds.World, []worlds.Orphan, error) {
	return []worlds.World{}, []worlds.Orphan{}, nil
}
