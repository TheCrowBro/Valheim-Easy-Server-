package manager

// Add a game (spec 6.1 and 6.2, stream A): the catalog of every game this
// build can run, the owner's own worlds for a game not yet added, and
// adding one: its servers.json entry with ports picked (ports.go), the
// licence accepted (game.StateEULA), the star only when asked (10.2 Q3),
// its first world, and the install job.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/servers"
	"valheim-easy-server/internal/worlds"
)

// CatalogGame is one game in Add a game (GET /api/catalog).
type CatalogGame struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Short       string            `json:"short"`
	Letter      string            `json:"letter"`
	Color       string            `json:"color"`
	Stage       game.Stage        `json:"stage"`     // "preview" is labelled; hidden games aren't listed
	CanRun      game.Level        `json:"canRun"`    // on this computer, from Info.OS
	NeedsText   string            `json:"needsText"` // "Needs Windows", when it can't
	DownloadMB  int               `json:"downloadMB"`
	DiskMB      int               `json:"diskMB"`
	MemoryMB    int               `json:"memoryMB"`
	MaxPlayers  int               `json:"maxPlayers"`
	FriendsNeed string            `json:"friendsNeed"`
	Password    game.PasswordRule `json:"password"`
	Schema      game.Schema       `json:"schema"` // the fields asked in Add a game (Field.Add)
	Ports       []game.PortRole   `json:"ports"`
	Import      bool              `json:"import"`         // worlds can come from the owner's own game
	EULA        string            `json:"eula,omitempty"` // the licence to accept first (game.Licensed)
	Added       bool              `json:"added"`          // there is an instance of it already
}

// The developer switches Add a game follows (spec 8.3): every stage shown
// (hidden games as preview), and fake servers, which run on every system.
// They are read each time, so a test can set them.
func allStages() bool { return os.Getenv("EGS_DEV_ALL_STAGES") == "1" }
func fakeMode() bool  { return os.Getenv("EGS_DEV_FAKE_SERVERS") != "" }

// shownStage is the stage Add a game shows a game with, and whether it is
// shown at all.
func shownStage(info game.Info) (game.Stage, bool) {
	if info.Stage == game.Hidden {
		return game.Preview, allStages()
	}
	return info.Stage, true
}

// Catalog lists every game that isn't hidden, in Info.Order.
func (m *Manager) Catalog() []CatalogGame {
	out := []CatalogGame{}
	for _, g := range game.All() {
		info := g.Info()
		stage, shown := shownStage(info)
		if !shown {
			continue
		}
		c := CatalogGame{ID: info.ID, Name: info.Name, Short: info.Short, Letter: info.Letter, Color: info.Color,
			Stage: stage, DownloadMB: info.DownloadMB, DiskMB: info.DiskMB, MemoryMB: info.MemoryMB,
			MaxPlayers: info.MaxPlayers, FriendsNeed: info.FriendsNeed, Password: info.Password,
			Ports: append([]game.PortRole{}, info.Ports...), Import: info.Import}
		c.CanRun, c.NeedsText = canRunHere(info)
		if l, ok := g.(game.Licensed); ok {
			c.EULA = l.EULA()
		}
		inst, srv := m.serverForGame(g)
		if inst != nil {
			m.mu.Lock()
			c.Added = !m.isHiddenLocked(inst)
			m.mu.Unlock()
		}
		c.Schema = game.Schema{}
		if srv != nil {
			for _, f := range srv.Schema() {
				if f.Add {
					c.Schema = append(c.Schema, f)
				}
			}
		}
		out = append(out, c)
	}
	return out
}

// canRunHere is whether a game's server runs on this computer, from
// Info.OS alone (Add a game can't know what an install would find); fake
// servers run everywhere.
func canRunHere(info game.Info) (game.Level, string) {
	if fakeMode() {
		return game.Runs, ""
	}
	return info.LevelOn(runtime.GOOS, runtime.GOARCH)
}

// CatalogLocal lists the worlds in the owner's own copy of a game on this
// computer, for a game not added yet (GET /api/catalog/{game}/local). The
// ids it gives are the ones AddGame's "import" takes.
func (m *Manager) CatalogLocal(gameID string) ([]worlds.Candidate, error) {
	g, ok := game.Get(gameID)
	if !ok {
		return nil, errUnknownGame(gameID)
	}
	if _, shown := shownStage(g.Info()); !shown {
		return nil, errUnknownGame(gameID)
	}
	if g.Info().Legacy {
		return m.LocalWorlds(), nil
	}
	_, srv := m.serverForGame(g)
	cands := srv.Worlds().Local()
	if cands == nil {
		cands = []worlds.Candidate{}
	}
	// Each look gives the worlds new ids, so AddGame imports from this
	// list, the one the owner chose from.
	m.mu.Lock()
	if m.life.localCands == nil {
		m.life.localCands = map[string][]worlds.Candidate{}
	}
	m.life.localCands[gameID] = cands
	m.mu.Unlock()
	return cands, nil
}

// errUnknownGame answers a request for a game this build doesn't offer.
func errUnknownGame(id string) error {
	return fmt.Errorf("This version of the app has no game %q.", id)
}

// serverForGame is the game's instance and its server when it has been
// added, else a server made only to ask the game about itself (its schema,
// the owner's own worlds), kept until the game is added so the world ids
// CatalogLocal gave still mean the same worlds then. It runs nothing and
// writes nothing.
func (m *Manager) serverForGame(g game.Game) (*instance, game.Server) {
	info := g.Info()
	m.mu.Lock()
	defer m.mu.Unlock()
	var found *instance
	m.eachInstance(func(inst *instance) {
		if found == nil && inst.info.ID == info.ID {
			found = inst
		}
	})
	if found != nil && found.srv != nil {
		return found, found.srv
	}
	if info.Legacy {
		return found, nil // the legacy game always has its instance
	}
	if srv := m.life.scratch[info.ID]; srv != nil {
		return found, srv
	}
	if m.life.scratch == nil {
		m.life.scratch = map[string]game.Server{}
	}
	srv := g.New(game.Env{Instance: info.ID, Data: m.opt.DataDir, Dirs: m.gameDirs(info.ID), Log: m.log})
	m.life.scratch[info.ID] = srv
	return found, srv
}

// gameDirs are a non-legacy instance's folders under games/<id> (spec
// 3.1). They are where its first world goes when Add a game imports one
// from the owner's own game, before the instance exists.
func (m *Manager) gameDirs(id string) game.Dirs {
	root := filepath.Join(m.opt.DataDir, "games", id)
	return game.Dirs{Root: root, Server: filepath.Join(root, "server"), Saves: filepath.Join(root, "saves"),
		Backups: filepath.Join(root, "backups"), Logs: filepath.Join(root, "logs"), Runtime: filepath.Join(root, "runtime"),
		Staging: filepath.Join(root, "uploads"), SteamCMD: filepath.Join(m.opt.DataDir, "steamcmd")}
}

// AddGameRequest is POST /api/games.
type AddGameRequest struct {
	Game       string      `json:"game"`
	ServerName string      `json:"serverName"`
	Password   string      `json:"password"`
	Values     game.Values `json:"values,omitempty"` // the other Add fields of the server scope, by key
	EULA       bool        `json:"eula"`             // the owner ticked the licence box
	Star       bool        `json:"star"`             // "Star it" (unticked by default)
	World      AddWorld    `json:"world"`
}

// AddWorld is the first world of a game being added.
type AddWorld struct {
	Mode      string      `json:"mode"` // "new" or "import"
	Name      string      `json:"name"`
	Values    game.Values `json:"values,omitempty"`    // new-world values, by key
	Candidate string      `json:"candidate,omitempty"` // "import": a CatalogLocal candidate's ID
}

// AddedGame is what adding a game answers: its instance and install job.
type AddedGame struct {
	ID  string `json:"id"`
	Job int    `json:"job"`
}

// AddGame adds a game and starts installing it. Adding a hidden legacy game
// only shows it again (I1). Field errors are keyed "server.<key>",
// "newWorld.<key>".
//
// Nothing is written until everything asked is known to be right: the
// settings pass the game's own checks, the licence is accepted, the ports
// are picked, and an imported world is copied in. Only then is the game
// written into servers.json (starred only if the owner ticked "Star it",
// decided 10.2 Q3) and its install job started. The imported world is the
// one thing written before that, as its name goes into servers.json; if
// a later step fails, it is taken out again (undoFirstWorld).
func (m *Manager) AddGame(in AddGameRequest) (AddedGame, error) {
	g, ok := game.Get(in.Game)
	if !ok {
		return AddedGame{}, errUnknownGame(in.Game)
	}
	info := g.Info()
	if _, shown := shownStage(info); !shown {
		return AddedGame{}, errUnknownGame(in.Game)
	}
	if info.Legacy {
		return m.showLegacy(in)
	}
	id := info.ID // one server per game this round: its id is the game's
	inst, srv := m.serverForGame(g)
	if inst != nil {
		return AddedGame{}, fmt.Errorf("%s is added already.", info.Short)
	}
	if l, why := canRunHere(info); l != game.Runs {
		if why == "" {
			why = "It can't run on this computer"
		}
		return AddedGame{}, fmt.Errorf("%s can't be added here: %s.", info.Short, strings.TrimSuffix(why, "."))
	}
	now := time.Now().UTC()
	saved, errs := addSaved(g, srv, in, now)
	if len(errs) > 0 {
		return AddedGame{}, errs
	}

	ports, unlock, err := m.pickPorts(id, info, nil)
	if err != nil {
		return AddedGame{}, err
	}
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	saved.Ports = ports

	// fail answers a step that went wrong after the import began, taking
	// back what it wrote. It runs while the ports lock is held, which
	// keeps a second add of the same game from importing meanwhile.
	undo := func() {}
	fail := func(err error) (AddedGame, error) {
		undo()
		return AddedGame{}, err
	}
	if in.World.Mode == "import" {
		// A game removed earlier keeps its worlds in its folder, so the
		// folder may be there already (remove.go).
		root := m.gameDirs(id).Root
		_, statErr := os.Stat(root)
		fresh := errors.Is(statErr, fs.ErrNotExist)
		name := ""
		undo = func() { m.undoFirstWorld(id, srv, name, fresh) }
		name, err = m.importFirstWorld(id, srv, in.World)
		if err != nil {
			return fail(err)
		}
		saved.World = name
		saved.Worlds = map[string]game.Values{name: worldDefaults(srv.Schema(), nil, false)}
	}

	entry := servers.Instance{ID: id, Game: info.ID, Added: now, Settings: saved.Server, World: saved.World,
		Worlds: saved.Worlds, Ports: saved.Ports, State: saved.State}
	inst, err = m.newGameInstance(entry)
	if err != nil {
		return fail(err)
	}
	err = m.updateServers(func(f *servers.File) error {
		if f.Instance(id) != nil {
			return fmt.Errorf("%s is added already.", info.Short)
		}
		f.Instances = append(f.Instances, entry)
		if in.Star && !slices.Contains(f.App.Stars, id) {
			f.App.Stars = append(f.App.Stars, id)
		}
		return nil
	})
	if err != nil {
		return fail(err) // the deferred unlock lets the next pick go after the undo
	}
	unlock() // the ports are written: another pick now sees them
	unlock = nil
	m.mu.Lock()
	m.addInstanceLocked(inst)
	delete(m.life.scratch, id)
	delete(m.life.localCands, id)
	m.mu.Unlock()
	if in.Star {
		m.starsChanged() // its Discord command (6.4)
	}
	m.addGameActivity(inst, "info", "", fmt.Sprintf("%s was added. Its ports: %s.", info.Short, portsText(info, ports)))
	if err := m.installGame(inst, false); err != nil {
		return AddedGame{ID: id}, err
	}
	m.mu.Lock()
	job := 0
	if inst.job != nil {
		job = inst.job.ID
	}
	m.mu.Unlock()
	m.notify()
	return AddedGame{ID: id, Job: job}, nil
}

// addSaved is a new instance's settings from what Add a game asked, with
// every other field at its default (so "update on start", "restart if it
// crashes" and the rest mean what the game says by default), checked by
// the game. The errors are keyed by scope, as the new routes give them.
func addSaved(g game.Game, srv game.Server, in AddGameRequest, now time.Time) (game.Saved, game.FieldErrors) {
	schema := srv.Schema()
	sv := game.Saved{Server: game.Values{}, Worlds: map[string]game.Values{}, State: game.Values{}}
	addable := map[string]bool{}
	for _, f := range schema {
		if f.Scope == game.ServerScope {
			if f.Default != "" {
				sv.Server[f.Key] = f.Default
			}
			addable[f.Key] = f.Add
		}
	}
	for k, v := range in.Values {
		if addable[k] {
			sv.Server[k] = v
		}
	}
	errs := game.FieldErrors{}
	nk := nameKey(schema)
	if name := strings.TrimSpace(in.ServerName); name != "" {
		sv.Server[nk] = name
	} else {
		errs["server."+nk] = "Give the server a name."
	}
	rule := g.Info().Password
	if key := passwordKey(schema); key != "" && rule.Use != "none" {
		pw := in.Password
		switch {
		case rule.Use == "required" && pw == "":
			errs["server."+key] = "Choose a password: friends type it when they join."
		case pw != "" && rule.Min > 0 && len([]rune(pw)) < rule.Min:
			errs["server."+key] = fmt.Sprintf("The password must be at least %d characters.", rule.Min)
		case pw != "" && rule.Max > 0 && len([]rune(pw)) > rule.Max:
			errs["server."+key] = fmt.Sprintf("The password can be at most %d characters.", rule.Max)
		}
		sv.Server[key] = pw
	}
	if l, ok := g.(game.Licensed); ok && l.EULA() != "" {
		if !in.EULA {
			errs["eula"] = "Tick the box to accept the licence first."
		} else {
			sv.State[game.StateEULA] = now.Format(time.RFC3339)
		}
	}
	w := in.World
	switch w.Mode {
	case "new":
		name := strings.TrimSpace(w.Name)
		if name == "" {
			errs["world.name"] = "Give the world a name."
		} else if p := srv.Worlds().NameProblem(name); p != "" {
			errs["world.name"] = p
		}
		sv.World = name
		sv.Worlds[name] = worldDefaults(schema, w.Values, true)
	case "import":
		if w.Candidate == "" {
			errs["world.candidate"] = "Choose a world from your own game."
		}
	default:
		errs["world.mode"] = "Choose a new world, or one from your own game."
	}
	if len(errs) > 0 {
		return sv, errs
	}
	if w.Mode == "new" {
		for k, msg := range srv.Validate(sv, sv.World) {
			errs[scopedKey(schema, k)] = msg
		}
	}
	return sv, errs
}

// worldDefaults are a first world's values: the world fields at their
// defaults and, for a new world, the new-world fields too, with what the
// owner chose over them. They are kept with the world (Saved.Worlds), so
// the first start can make the world from them.
func worldDefaults(schema game.Schema, chosen game.Values, newWorld bool) game.Values {
	v := game.Values{}
	for _, f := range schema {
		if f.Scope != game.WorldScope && !(newWorld && f.Scope == game.NewWorldScope) {
			continue
		}
		if f.Default != "" {
			v[f.Key] = f.Default
		}
		if c, ok := chosen[f.Key]; ok {
			v[f.Key] = c
		}
	}
	return v
}

// nameKey is the key of the game's server name field: "serverName" in
// every game so far, else the first server text field Add a game asks
// whose key or label says "name".
func nameKey(schema game.Schema) string {
	for _, f := range schema {
		if f.Key == "serverName" {
			return f.Key
		}
	}
	for _, f := range schema {
		if f.Scope == game.ServerScope && f.Kind == "text" && f.Add &&
			(strings.Contains(strings.ToLower(f.Key), "name") || strings.Contains(strings.ToLower(f.Label), "name")) {
			return f.Key
		}
	}
	return "serverName"
}

// passwordKey is the key of the game's server password field, or "" when
// it has none (Minecraft uses its allow list instead).
func passwordKey(schema game.Schema) string {
	for _, f := range schema {
		if f.Scope == game.ServerScope && f.Kind == "password" {
			return f.Key
		}
	}
	return ""
}

// scopedKey gives a game's field error the key the new routes use:
// "server.<key>", "world.<key>", "newWorld.<key>" by the field's scope; a
// world name problem is "world.name".
func scopedKey(schema game.Schema, key string) string {
	if strings.Contains(key, ".") || key == "eula" {
		return key
	}
	if key == "world" {
		return "world.name"
	}
	for _, f := range schema {
		if f.Key == key {
			switch f.Scope {
			case game.WorldScope:
				return "world." + key
			case game.NewWorldScope:
				return "newWorld." + key
			}
		}
	}
	return "server." + key
}

// importFirstWorld copies the world the owner chose from their own game
// into the new game's worlds (copied, never moved: their own game keeps
// it), and says the name it has there.
func (m *Manager) importFirstWorld(id string, srv game.Server, w AddWorld) (string, error) {
	m.mu.Lock()
	cands := m.life.localCands[id] // what CatalogLocal showed the owner
	m.mu.Unlock()
	var cand *worlds.Candidate
	for i := range cands {
		if cands[i].ID == w.Candidate {
			cand = &cands[i]
		}
	}
	if cand == nil {
		return "", errors.New("That world isn't in the list any more. Look for the worlds in your own game again.")
	}
	placed, err := srv.Worlds().Import(cands, []worlds.Choice{{ID: cand.ID, Action: "import", Name: strings.TrimSpace(w.Name)}},
		func(string) bool { return false }, false)
	if err != nil {
		return "", err
	}
	if len(placed) == 0 {
		return "", errors.New("Nothing was copied from your own game.")
	}
	return placed[0].Name, nil
}

// undoFirstWorld takes back what importing the first world wrote, when
// adding the game fails after the import began (the instance can't be
// made, or servers.json can't be written). Left there, the copy would be
// imported a second time by a retry, and a games/<id> folder is what
// rebuilding a lost servers.json (spec 3.4) takes for an added game. So
// when this add made the game's folder (fresh), all of it goes; else (the
// worlds of a game removed earlier are there) only the copied world goes,
// through the game's own Delete, which keeps a backup of it as every
// delete does. The owner's own game still has the world either way: an
// import only ever copies.
func (m *Manager) undoFirstWorld(id string, srv game.Server, name string, fresh bool) {
	if fresh {
		if err := os.RemoveAll(m.gameDirs(id).Root); err != nil {
			m.log.Printf("adding %s failed, and its folder couldn't be removed: %v", id, err)
		}
		return
	}
	if name == "" {
		return // nothing was copied: an import that fails puts everything back itself
	}
	if err := srv.Worlds().Delete(name); err != nil && !errors.Is(err, worlds.ErrNotFound) {
		m.log.Printf("adding %s failed, and the world %q copied for it couldn't be removed: %v", id, name, err)
	}
}

// showLegacy puts the hidden legacy game back in the panel: everything it
// had is where it was (I1), so nothing else is needed. It is starred only
// when the owner asked.
func (m *Manager) showLegacy(in AddGameRequest) (AddedGame, error) {
	inst := m.legacy()
	m.mu.Lock()
	hidden := m.isHiddenLocked(inst)
	m.mu.Unlock()
	if !hidden {
		return AddedGame{}, fmt.Errorf("%s is added already.", inst.short())
	}
	err := m.updateServers(func(f *servers.File) error {
		if e := f.Instance(inst.id); e != nil {
			e.Hidden = false
		}
		if in.Star && !slices.Contains(f.App.Stars, inst.id) {
			f.App.Stars = append(f.App.Stars, inst.id)
		}
		return nil
	})
	if err != nil {
		return AddedGame{}, err
	}
	if in.Star {
		m.starsChanged()
	}
	m.addGameActivity(inst, "info", "", inst.short()+" is back in the panel.")
	m.notify()
	return AddedGame{ID: inst.id}, nil
}
