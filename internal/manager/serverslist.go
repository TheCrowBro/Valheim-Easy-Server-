package manager

// The list of games (servers.json, spec 3.2 and 3.4) as the manager uses
// it: read once in New, the instances made from it, Valheim-only safe mode
// when it can't be used, each instance's settings kept in it
// (serversStore), and what Start does with it before anything runs.

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/servers"
)

// errSafeMode answers a change to the list of games while it can't be
// written (safe mode, spec 8.3).
var errSafeMode = errors.New("The list of games can't be changed right now: only the main game runs (see the Overview).")

// fakeFromEnv is fake mode as EGS_DEV_FAKE_SERVERS asks for it: the path
// of a built cmd/fakeserver, which every non-legacy game then installs and
// runs instead of its real server (spec 7.1). Development only.
func fakeFromEnv() *game.Fake {
	path := os.Getenv("EGS_DEV_FAKE_SERVERS")
	if path == "" {
		return nil
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return &game.Fake{Program: path, Delay: 3 * time.Second}
}

// gameByID finds a game: first in Options.Games (tests), then in the
// registry.
func (m *Manager) gameByID(id string) (game.Game, bool) {
	for _, g := range m.opt.Games {
		if g.Info().ID == id {
			return g, true
		}
	}
	return game.Get(id)
}

// openServers reads servers.json when the app starts, by spec 3.4's rules
// (servers.Open: a damaged list falls back to its backup, a missing one is
// made), and makes an instance for each game in it. Only when the list
// can't be used (written by a newer version, or it and its backup both
// damaged), or EGS_DEV_SAFE_MODE=1, it is Valheim-only safe mode: only
// the legacy instance runs, servers.json is never written, and the other
// games' run files are only watched (foreign.go), so their servers still
// keep the computer awake. It reads only servers.json (and its backup and
// the games/ folder's names when it is missing or damaged), so New stays
// quick (I5).
func (m *Manager) openServers(now time.Time) {
	inst := m.legacy()
	if os.Getenv("EGS_DEV_SAFE_MODE") == "1" {
		m.enterSafeModeLocked(now, fmt.Sprintf("Only %s runs: safe mode is switched on for development (EGS_DEV_SAFE_MODE).", inst.short()))
		return
	}
	isGame := func(id string) bool {
		_, ok := m.gameByID(id)
		return ok
	}
	f, o, err := servers.Open(m.opt.DataDir, m.legacyID, isGame, now)
	if o.Kept != "" {
		m.log.Printf("servers.json was damaged; it was kept as %s", o.Kept)
	}
	if o.WriteErr != nil {
		m.log.Printf("servers.json: %v", o.WriteErr)
	}
	if err != nil || o.SafeMode {
		m.log.Printf("safe mode: %v", err)
		m.enterSafeModeLocked(now, safeModeText(inst, err))
		return
	}
	m.serversNote = o.Warning()
	m.found = o.Found
	m.publishServersLocked(f)
	byID := map[string]*instance{inst.id: inst}
	order := []string{inst.id}
	for _, in := range f.Instances {
		if in.ID == m.legacyID {
			continue
		}
		if _, dup := byID[in.ID]; dup {
			continue // servers.Update never writes a list like this; a hand edit can
		}
		gi, err := m.newGameInstance(in)
		if err != nil {
			m.log.Printf("servers.json: %v", err)
			continue
		}
		byID[gi.id] = gi
		order = append(order, gi.id)
	}
	m.setInstancesLocked(byID, order)
}

// enterSafeModeLocked is Valheim-only safe mode: why is what the Overview
// says. The list in memory has only the legacy game, starred, as a first
// start's would; it is never written.
func (m *Manager) enterSafeModeLocked(now time.Time, why string) {
	m.safeMode = why
	m.publishServersLocked(servers.New(m.legacyID, now))
}

// safeModeText says why only the legacy game runs, for the Overview.
func safeModeText(inst *instance, err error) string {
	switch {
	case errors.Is(err, servers.ErrNewer):
		return fmt.Sprintf("Only %s runs: the list of your other games was saved by a newer version of the app, so this version leaves it alone. Install the newest version to use them again.", inst.short())
	case err != nil:
		return fmt.Sprintf("Only %s runs: the list of your other games (servers.json) can't be read, and neither can its backup (%v).", inst.short(), err)
	}
	return fmt.Sprintf("Only %s runs: the list of your other games can't be used.", inst.short())
}

// publishServersLocked makes f the list in use. f must not be changed
// afterwards. m.mu must be held (or, in New, nothing else can see the
// manager yet).
func (m *Manager) publishServersLocked(f servers.File) {
	m.servers = f
	m.serversSnap.Store(&f)
}

// serversEntry is the instance's entry in the published list, or nil. It
// never takes m.mu. The entry must not be changed.
func (m *Manager) serversEntry(id string) *servers.Instance {
	f := m.serversSnap.Load()
	if f == nil {
		return nil
	}
	return f.Instance(id)
}

// serversStore is a non-legacy instance's game.Store: its settings, worlds,
// selected world, ports and the adapter's own State, in its servers.json
// entry (spec 3.2). Get reads the published list and never takes m.mu, so
// a Monitor or Join may call it while the manager holds the lock; Save
// changes servers.json through updateServers, so it must not be called
// with m.mu held (the contract says it runs from jobs).
type serversStore struct {
	m  *Manager
	id string
}

func (s serversStore) Get() game.Saved {
	in := s.m.serversEntry(s.id)
	if in == nil {
		return game.Saved{}
	}
	return savedOf(*in)
}

func (s serversStore) Save(change func(*game.Saved) error) error {
	return s.m.updateServers(func(f *servers.File) error {
		in := f.Instance(s.id)
		if in == nil {
			return fmt.Errorf("This game was removed.")
		}
		sv := savedOf(*in)
		if err := change(&sv); err != nil {
			return err
		}
		in.Settings, in.Worlds, in.World, in.Ports, in.State = sv.Server, sv.Worlds, sv.World, sv.Ports, sv.State
		return nil
	})
}

// savedOf is an entry's settings as the adapter sees them: a copy, so a
// change to it never reaches the published list.
func savedOf(in servers.Instance) game.Saved {
	return game.Saved{Server: in.Settings, Worlds: in.Worlds, World: in.World, Ports: in.Ports, State: in.State}.Clone()
}

// gameDirs are a non-legacy instance's folders (spec 3.1): all under
// games/<id>, but SteamCMD, which every game shares with the legacy one.
func (m *Manager) gameDirs(id string) game.Dirs {
	root := filepath.Join(m.opt.DataDir, "games", id)
	return game.Dirs{Root: root, Server: filepath.Join(root, "server"), Saves: filepath.Join(root, "saves"),
		Backups: filepath.Join(root, "backups"), Logs: filepath.Join(root, "logs"), Runtime: filepath.Join(root, "runtime"),
		Staging: filepath.Join(root, "uploads"), SteamCMD: filepath.Join(m.opt.DataDir, "steamcmd")}
}

// gameClient is the HTTP client adapters get (downloads, probes, APIs);
// each call is bounded by its own context.
var gameClient = &http.Client{}

// prepareInstances is what Start does for the non-legacy instances before
// anything may start or take over one: their saved players, records and
// "last played with", what is installed (the cache Status shows), and
// ports for any instance servers.json was rebuilt with (spec 3.4: picked
// again, keeping each role's default when it's free).
func (m *Manager) prepareInstances() {
	m.eachInstance(func(inst *instance) {
		if inst.legacy || inst.srv == nil {
			return
		}
		// Under the lock: the control panel may already be asking.
		m.mu.Lock()
		m.loadKnown(inst)
		m.loadPlayed(inst)
		m.loadRecords(inst)
		m.mu.Unlock()
		m.refreshCache(inst)
	})
	m.mu.Lock()
	found := m.found
	m.found = nil
	m.mu.Unlock()
	for _, id := range found {
		inst, err := m.instanceByID(id)
		if err != nil || inst.srv == nil {
			continue
		}
		m.mu.Lock()
		ports, err := m.pickPortsLocked(inst.info, nil)
		m.mu.Unlock()
		if err != nil || len(ports) == 0 {
			m.log.Printf("no ports picked for %s: %v", id, err)
			continue
		}
		if err := m.updateServers(func(f *servers.File) error {
			if in := f.Instance(id); in != nil && len(in.Ports) == 0 {
				in.Ports = ports
			}
			return nil
		}); err != nil {
			m.log.Printf("saving the ports picked for %s: %v", id, err)
		}
	}
}

// startWithApp starts the games whose "start when the app opens" is on
// (spec 2.9): the legacy game's from settings.json, exactly as before (its
// own path, with today's message); each other game's from its servers.json
// entry, through the one start path. None starts when the app restarted to
// update itself: a server that was stopped stays stopped (one that was
// running was taken over).
func (m *Manager) startWithApp(updated bool) {
	inst := m.legacy()
	m.mu.Lock()
	legacy := m.settings.StartWithApp && inst.run == nil && m.installedLocked(inst) && !m.isHiddenLocked(inst) && !m.isDisabledLocked(inst)
	var others []*instance
	m.eachInstance(func(i *instance) {
		if i.legacy || i.srv == nil || i.run != nil || !m.installedLocked(i) || m.isDisabledLocked(i) {
			return
		}
		if in := m.servers.Instance(i.id); in != nil && in.StartWithApp {
			if l, _ := m.canRunLocked(i); l == game.Runs {
				others = append(others, i)
			}
		}
	})
	m.mu.Unlock()
	if updated && (legacy || len(others) > 0) {
		m.log.Printf("not starting any game: the app restarted after an update")
		return
	}
	if legacy {
		m.addActivity("info", "", "Starting the server because \"Start the server when the app opens\" is on.")
		if err := m.StartServer(); err != nil {
			m.log.Printf("auto start: %v", err)
		}
	}
	for _, i := range others {
		m.instActivity(i, "info", "", fmt.Sprintf("Starting %s because \"Start it when the app opens\" is on.", i.short()))
		if err := m.startGameWith(i, StartOptions{Source: SourceApp}); err != nil {
			m.log.Printf("auto start of %s: %v", i.id, err)
			m.instActivity(i, "warning", "", fmt.Sprintf("Couldn't start %s with the app: %s", i.short(), strings.TrimSuffix(err.Error(), ".")+"."))
		}
	}
}
