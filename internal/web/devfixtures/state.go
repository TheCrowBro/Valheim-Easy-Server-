package main

// The made-up games and everything the panel asks about them. Each one is
// a real game of the registry with its real adapter (made in a temporary
// folder, never installed or started), so its schema, ports, lists and
// "how friends join" are the game's own; its state, players, worlds and
// numbers are made up here, and change as the panel asks.

import (
	"fmt"
	"io"
	"log"
	"math"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/manager"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/records"
	"valheim-easy-server/internal/stats"
	"valheim-easy-server/internal/steamcmd"
	"valheim-easy-server/internal/worlds"
)

// Made-up addresses: documentation ranges, never anyone's real ones.
const (
	publicIP = "203.0.113.25"
	lanIP    = "192.168.1.20"
	totalMB  = 32768
)

type options struct {
	GOOS, GOARCH string
	FreeMB       int           // free memory with the made-up running games counted
	Step         time.Duration // how long a pretend install, start or stop takes
	Dir          string        // a temporary folder for the adapters
	Now          func() time.Time
}

type state struct {
	mu    sync.Mutex
	opt   options
	insts []*instance

	stars       []string
	sidebar     manager.SidebarStatus
	app         manager.AppSettingsChange // every field set
	mainWebhook string

	baseUsed int // MB the games running from the start use

	jobSeq   int
	appJob   *manager.Job
	activity []manager.Activity
	actSeq   int64
	upSeq    int
	uploads  map[string]string // upload id → instance
}

// instance is one made-up game on this pretend computer.
type instance struct {
	id   string
	g    game.Game
	info game.Info
	srv  game.Server

	state        manager.State
	installed    bool
	build        *game.Build
	disabled     bool
	startWithApp bool
	autoStop     int
	discord      manager.GameDiscordSettings
	chosen       map[string]bool // ports the owner typed
	players      []game.Player
	worlds       []worlds.World
	orphans      []worlds.Orphan
	known        []manager.KnownPlayer
	lists        map[string][]string
	logs         []manager.LogLine
	logSeq       int64
	memMB        int
	cpu          float64
	started      time.Time
	runWorld     string
	job          *manager.Job
	lastExit     *manager.ExitInfo
	ask          *manager.AskStatus
	gen          int // bumped by each start and stop, so an older timer does nothing
}

func (inst *instance) saved() game.Saved { return inst.srv.Store().Get() }

func (st *state) now() time.Time {
	if st.opt.Now != nil {
		return st.opt.Now()
	}
	return time.Now()
}

// --- making the games ---------------------------------------------------------------

var names = []string{"Astrid", "Bjorn", "Sigrid", "Alex", "Sam", "Robin", "Kim", "Noor"}

func newState(opt options) *state {
	st := &state{opt: opt, uploads: map[string]string{},
		sidebar: manager.SidebarStatus{ActiveOpen: true, InactiveOpen: false}}
	on, minutes := true, 60
	st.app = manager.AppSettingsChange{IdleHibernate: &on, IdleMinutes: &minutes, AutoUpdate: &on, StartAfterWake: &on, RouterPorts: &on}

	// The legacy game and the next four games in the panel's order, plus
	// the first one that needs Windows, so the Mac's greyed-out state shows.
	var picked []game.Game
	var needsWindows game.Game
	for _, g := range game.All() {
		info := g.Info()
		if info.Stage == game.Hidden {
			continue
		}
		switch {
		case info.Legacy, len(picked) < 5:
			picked = append(picked, g)
		case needsWindows == nil:
			if l, _ := info.LevelOn("darwin", "amd64"); l == game.CantRun {
				needsWindows = g
			}
		}
	}
	if needsWindows != nil {
		picked = append(picked, needsWindows)
	}
	now := st.now()
	for n, g := range picked {
		inst, err := st.makeInstance(g)
		if err != nil {
			log.Printf("devfixtures: %s left out: %v", g.Info().ID, err)
			continue
		}
		st.insts = append(st.insts, inst)
		inst.installed = true
		inst.build = &game.Build{Version: "1.0.3", Date: now.AddDate(0, 0, -12)}
		switch n {
		case 0: // starred, online, three people playing
			st.stars = append(st.stars, inst.id)
			st.goOnline(inst, 3, now.Add(-95*time.Minute))
		case 1: // starred, stopped
			st.stars = append(st.stars, inst.id)
			inst.lastExit = &manager.ExitInfo{At: now.Add(-26 * time.Hour), Expected: true, World: inst.saved().World, Uptime: 3 * 3600}
		case 2: // running without a star: under Active only while it runs
			st.goOnline(inst, 1, now.Add(-20*time.Minute))
		case 3: // added, stopped, not starred: under Inactive
		default: // added but not installed yet
			inst.installed, inst.build = false, nil
		}
	}
	for _, inst := range st.insts {
		for i, name := range names[:4] {
			inst.known = append(inst.known, manager.KnownPlayer{Name: name, PlatformID: fmt.Sprintf("765611980000000%02d", i+1),
				FirstSeen: now.AddDate(0, 0, -30+i), LastSeen: now.Add(-time.Duration(i) * 24 * time.Hour),
				PlaySeconds: float64(36000 - i*5000), Sessions: 12 - i})
		}
	}
	for _, inst := range st.running() {
		st.baseUsed += inst.memMB
	}
	st.note(nil, "update", "The app is up to date.")
	return st
}

// makeInstance makes a game's adapter in the temporary folder, with
// made-up settings: a server name, a password where it has one, its
// default ports and a world.
func (st *state) makeInstance(g game.Game) (inst *instance, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("its adapter couldn't be made here: %v", r)
		}
	}()
	info := g.Info()
	root := filepath.Join(st.opt.Dir, "games", info.ID)
	env := game.Env{Instance: info.ID, Data: st.opt.Dir, Log: log.New(io.Discard, "", 0),
		Dirs: game.Dirs{Root: root, Server: filepath.Join(root, "server"), Saves: filepath.Join(root, "saves"),
			Backups: filepath.Join(root, "backups"), Logs: filepath.Join(root, "logs"), Runtime: filepath.Join(root, "runtime"),
			Staging: filepath.Join(root, "uploads"), SteamCMD: filepath.Join(st.opt.Dir, "steamcmd")}}
	world := "Riverbend"
	if info.Legacy {
		s := config.Defaults()
		s.ServerName, s.Password, s.World = "Friends' "+info.Short, "frost123", "Midgard"
		env.Legacy = &legacyStore{s: s, dir: st.opt.Dir}
		world = "Midgard"
	} else {
		env.Store = &memStore{}
	}
	srv := g.New(env)
	inst = &instance{id: info.ID, g: g, info: info, srv: srv, state: manager.StateStopped, autoStop: 60,
		chosen: map[string]bool{}, lists: map[string][]string{},
		discord: manager.GameDiscordSettings{On: true, Stopped: true, Daily: true, Password: true, GuildOK: true}}
	if !info.Legacy {
		saved := defaultSaved(info, srv.Schema(), world)
		if err := srv.Store().Save(func(s *game.Saved) error { *s = saved; return nil }); err != nil {
			return nil, err
		}
	}
	if l, ok := srv.(game.Lister); ok {
		for _, k := range l.Lists() {
			inst.lists[k.ID] = []string{}
		}
	}
	now := st.now()
	inst.worlds = []worlds.World{
		{Name: world, Format: "folder", Size: 48 << 20, Modified: now.Add(-2 * time.Hour), Backups: []worlds.Backup{
			{ID: "auto-1", Kind: "auto", Label: "Automatic backup", Time: now.Add(-3 * time.Hour), Size: 47 << 20, Format: "zip"},
			{ID: "manual-1", Kind: "manual", Label: "Backup", Time: now.AddDate(0, 0, -2), Size: 45 << 20, Format: "zip"}}},
		{Name: "Old Times", Format: "folder", Size: 12 << 20, Modified: now.AddDate(0, -2, 0), Backups: []worlds.Backup{}},
	}
	inst.orphans = []worlds.Orphan{{Name: "Deleted World", Backups: []worlds.Backup{
		{ID: "deleted-1", Kind: "deleted", Label: "Before it was deleted", Time: now.AddDate(0, 0, -9), Size: 8 << 20, Format: "zip"}}}}
	return inst, nil
}

// defaultSaved are a new game's settings: each server field's default, a
// server name, a password when the game has one, its default ports and
// one world with its world fields' defaults.
func defaultSaved(info game.Info, schema game.Schema, world string) game.Saved {
	s := game.Saved{Server: game.Values{}, Worlds: map[string]game.Values{world: {}}, World: world,
		Ports: map[string]int{}, State: game.Values{game.StateEULA: "2026-11-01T12:00:00Z"}}
	for _, f := range schema {
		switch f.Scope {
		case game.ServerScope:
			s.Server[f.Key] = f.Default
		case game.WorldScope, game.NewWorldScope:
			s.Worlds[world][f.Key] = f.Default
		}
	}
	s.Server["serverName"] = "Friends' " + info.Short
	if info.Password.Use != "none" {
		s.Server["password"] = "frost123"
	}
	for _, p := range info.Ports {
		s.Ports[p.Role] = p.Default
	}
	for _, p := range info.Ports {
		if p.Tie == "game+1" {
			s.Ports[p.Role] = s.Ports["game"] + 1
		}
	}
	return s
}

// memStore is a game's settings in memory (the app keeps them in
// servers.json).
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
	next := m.s.Clone()
	if err := change(&next); err != nil {
		return err
	}
	m.s = next
	return nil
}

// legacyStore is the legacy game's settings.json in memory: what its
// adapter needs from the manager (games/valheim.LegacyStore), with nothing
// that installs or posts anything.
type legacyStore struct {
	mu  sync.Mutex
	s   config.Settings
	dir string
}

func (l *legacyStore) Settings() config.Settings {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.s.Clone()
}

func (l *legacyStore) Paths() config.Paths { return config.ResolvePaths(l.dir, l.Settings()) }

func (l *legacyStore) Update(change func(*config.Settings) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	next := l.s.Clone()
	if err := change(&next); err != nil {
		return err
	}
	l.s = next
	return nil
}

func (l *legacyStore) Installer() steamcmd.Installer { return nil }
func (l *legacyStore) ModsURL() string               { return "" }
func (l *legacyStore) Note(kind, text string)        {}

// safely calls one of an adapter's methods, which may not expect to run in
// a pretend instance, and uses fallback if it panics.
func safely[T any](fallback T, fn func() T) (out T) {
	defer func() {
		if recover() != nil {
			out = fallback
		}
	}()
	return fn()
}

// --- what the panel shows -------------------------------------------------------------

func (st *state) find(id string) *instance {
	for _, inst := range st.insts {
		if inst.id == id {
			return inst
		}
	}
	return nil
}

func (st *state) running() []*instance {
	var out []*instance
	for _, inst := range st.insts {
		if inst.state == manager.StateOnline || inst.state == manager.StateStarting {
			out = append(out, inst)
		}
	}
	return out
}

// freeMBLocked is the memory free now: what -free says (with the games
// that run from the start counted), less what games started since use,
// plus what stopped ones gave back.
func (st *state) freeMBLocked() int {
	used := 0
	for _, inst := range st.running() {
		used += inst.memMB
	}
	return st.opt.FreeMB - used + st.baseUsed
}

func (st *state) joinLocked(inst *instance) game.Join {
	saved := inst.saved()
	in := game.JoinInput{Saved: saved, PublicIP: publicIP, LAN: []string{lanIP}, Router: map[string]string{}}
	for _, p := range inst.info.Ports {
		if p.Public {
			in.Router[p.Role] = "open"
		}
	}
	if inst.state == manager.StateOnline {
		rec := game.Record{PID: 4242, Started: inst.started, Launched: inst.started, World: inst.runWorld,
			Server: saved.Server["serverName"], Port: saved.Ports["game"], Password: saved.Server["password"], Ports: saved.Ports}
		rs := game.RunState{Ready: true, Players: inst.players, Count: len(inst.players), Code: "482913"}
		in.Rec, in.State = &rec, &rs
	}
	fallback := game.Join{How: "address", Address: publicIP, Port: saved.Ports["game"], LAN: []string{}, Steps: []string{}}
	j := safely(fallback, func() game.Join { return inst.srv.Join(in) })
	if j.LAN == nil {
		j.LAN = []string{}
	}
	if j.Steps == nil {
		j.Steps = []string{}
	}
	return j
}

func (st *state) gameStatusLocked(inst *instance) manager.GameStatus {
	info, saved := inst.info, inst.saved()
	canRun, needs := info.LevelOn(st.opt.GOOS, st.opt.GOARCH)
	gs := manager.GameStatus{ID: inst.id, Game: info.ID, Name: info.Name, Short: info.Short, Letter: info.Letter,
		Color: info.Color, Stage: info.Stage, ServerName: saved.Server["serverName"], Starred: slices.Contains(st.stars, inst.id),
		Disabled: inst.disabled, CanRun: canRun, NeedsText: needs, State: inst.state, Installed: inst.installed,
		Build: inst.build, World: saved.World, Players: append([]game.Player{}, inst.players...), Count: len(inst.players),
		MaxPlayers: info.MaxPlayers, Presence: info.Presence, Join: st.joinLocked(inst), Ports: []manager.PortStatus{},
		LastExit: inst.lastExit, MemMB: 0, NeedMB: info.MemoryMB, Ask: inst.ask, Reminder: []string{}, Warnings: []string{}}
	if inst.job != nil {
		j := *inst.job
		gs.Job = &j
	}
	if inst.state == manager.StateOnline {
		gs.RunningWorld, gs.MemMB, gs.CPU = inst.runWorld, inst.memMB, inst.cpu
		if inst.autoStop > 0 {
			as := &manager.AutoStopStatus{Minutes: inst.autoStop}
			if len(inst.players) == 0 {
				at := st.now().Add(time.Duration(inst.autoStop) * time.Minute)
				as.At = &at
			}
			gs.AutoStop = as
		}
		posted := inst.started.Add(40 * time.Second)
		gs.Discord = &manager.GameDiscordStatus{Posted: &posted}
	}
	for _, p := range info.Ports {
		ps := manager.PortStatus{Role: p.Role, Protos: p.Protos, Port: saved.Ports[p.Role], Public: p.Public, Chosen: inst.chosen[p.Role]}
		if p.Public {
			ps.Router = "open"
		}
		gs.Ports = append(gs.Ports, ps)
	}
	return gs
}

// machine is the pretend computer.
func (st *state) machine() platform.MachineInfo {
	return platform.MachineInfo{Hostname: "GAMING-PC", OS: osName(st.opt.GOOS), CPU: "Made-up CPU (12 threads)",
		Threads: 12, MemTotal: totalMB << 20, GPUs: []string{}}
}

func (st *state) statusLocked() manager.Status {
	now := st.now()
	out := manager.Status{Platform: st.opt.GOOS, App: manager.AppInfo{Version: "dev fixtures"}, Machine: st.machine(),
		Live:    manager.Live{MemTotal: totalMB << 20, GPUs: []platform.GPUSample{}},
		Players: []manager.PlayerView{}, Join: manager.JoinInfo{LANAddrs: []string{lanIP}},
		Network: []platform.NetAdapter{}, Warnings: []string{}, RestMode: config.RestSleep,
		Sleep: manager.HibernateInfo{Available: true}, Now: now, Jobs: []manager.Job{},
		Games: []manager.GameStatus{}, Stars: append([]string{}, st.stars...), Sidebar: st.sidebar,
		Router: &manager.RouterStatus{Kind: "upnp", ExternalIP: publicIP}}
	out.Live.MemSys = uint64(totalMB-st.freeMBLocked()) << 20
	if n := len(st.activity); n > 40 {
		out.Activity = append([]manager.Activity{}, st.activity[n-40:]...)
	} else {
		out.Activity = append([]manager.Activity{}, st.activity...)
	}
	for _, inst := range st.insts {
		gs := st.gameStatusLocked(inst)
		out.Games = append(out.Games, gs)
		if gs.Job != nil && (gs.Job.Finished == nil || now.Sub(*gs.Job.Finished) < time.Minute) {
			out.Jobs = append(out.Jobs, *gs.Job)
		}
		if gs.State == manager.StateOnline {
			out.Online++
			out.Playing += gs.Count
		}
		if inst.info.Legacy { // the old panel's top-level fields
			out.State, out.Installed, out.ServerName, out.World, out.RunningWorld = gs.State, gs.Installed, gs.ServerName, gs.World, gs.RunningWorld
			for _, p := range inst.players {
				out.Players = append(out.Players, manager.PlayerView{Name: p.Name, PlatformID: p.Account, Since: p.Since})
			}
			out.Job, out.LastExit = gs.Job, gs.LastExit
		}
	}
	if st.appJob != nil {
		out.Jobs = append(out.Jobs, *st.appJob)
	}
	return out
}

func osName(goos string) string {
	switch goos {
	case "windows":
		return "Windows 11"
	case "darwin":
		return "macOS 12 Monterey"
	}
	return "Linux"
}

func (st *state) catalogLocked() []manager.CatalogGame {
	out := []manager.CatalogGame{}
	for _, g := range game.All() {
		info := g.Info()
		if info.Stage == game.Hidden {
			continue
		}
		c := manager.CatalogGame{ID: info.ID, Name: info.Name, Short: info.Short, Letter: info.Letter, Color: info.Color,
			Stage: info.Stage, DownloadMB: info.DownloadMB, DiskMB: info.DiskMB, MemoryMB: info.MemoryMB,
			MaxPlayers: info.MaxPlayers, FriendsNeed: info.FriendsNeed, Password: info.Password,
			Ports: append([]game.PortRole{}, info.Ports...), Import: info.Import, Schema: game.Schema{},
			Added: st.find(info.ID) != nil}
		c.CanRun, c.NeedsText = info.LevelOn(st.opt.GOOS, st.opt.GOARCH)
		if l, ok := g.(game.Licensed); ok {
			c.EULA = l.EULA()
		}
		schema := st.schemaOf(g)
		for _, f := range schema {
			if f.Add {
				c.Schema = append(c.Schema, f)
			}
		}
		out = append(out, c)
	}
	return out
}

// schemaOf is a game's settings schema, from the added instance's adapter
// or from a scratch one.
func (st *state) schemaOf(g game.Game) game.Schema {
	if inst := st.find(g.Info().ID); inst != nil {
		return safely(game.Schema{}, inst.srv.Schema)
	}
	scratch, err := st.makeInstance(g)
	if err != nil {
		return game.Schema{}
	}
	return safely(game.Schema{}, scratch.srv.Schema)
}

func (st *state) settingsLocked(inst *instance) manager.GameSettingsView {
	saved := inst.saved()
	v := manager.GameSettingsView{Schema: safely(game.Schema{}, inst.srv.Schema),
		Values:          manager.GameValues{Server: saved.Server, Worlds: saved.Worlds, World: saved.World},
		Ports:           []manager.PortSetting{},
		AutoStopMinutes: inst.autoStop, StartWithApp: inst.startWithApp, Disabled: inst.disabled, Discord: inst.discord}
	if v.Values.Server == nil {
		v.Values.Server = game.Values{}
	}
	if v.Values.Worlds == nil {
		v.Values.Worlds = map[string]game.Values{}
	}
	for _, p := range inst.info.Ports {
		v.Ports = append(v.Ports, manager.PortSetting{Role: p.Role, Protos: p.Protos, Port: saved.Ports[p.Role], Public: p.Public, Chosen: inst.chosen[p.Role]})
	}
	return v
}

func (st *state) worldsLocked(inst *instance) manager.GameWorldsView {
	saved := inst.saved()
	v := manager.GameWorldsView{Worlds: append([]worlds.World{}, inst.worlds...), Orphans: append([]worlds.Orphan{}, inst.orphans...),
		Selected: saved.World, Folder: filepath.Join(`C:\Users\Owner\AppData\LocalLow`, inst.info.Short, "worlds"),
		Schema: manager.WorldSchemas{World: game.Schema{}, NewWorld: game.Schema{}}, Values: map[string]game.Values{}}
	if inst.state == manager.StateOnline {
		v.Running = inst.runWorld
	}
	for _, f := range safely(game.Schema{}, inst.srv.Schema) {
		switch f.Scope {
		case game.WorldScope:
			v.Schema.World = append(v.Schema.World, f)
		case game.NewWorldScope:
			v.Schema.NewWorld = append(v.Schema.NewWorld, f)
		}
	}
	for _, w := range inst.worlds {
		v.Values[w.Name] = saved.Worlds[w.Name].Clone()
		if v.Values[w.Name] == nil {
			v.Values[w.Name] = game.Values{}
		}
	}
	return v
}

func (st *state) listsLocked(inst *instance) []manager.GameList {
	out := []manager.GameList{}
	l, ok := inst.srv.(game.Lister)
	if !ok {
		return out
	}
	for _, k := range safely(nil, l.Lists) {
		out = append(out, manager.GameList{Kind: k, IDs: append([]string{}, inst.lists[k.ID]...)})
	}
	return out
}

func (st *state) recordsLocked(inst *instance, period string) (records.Summary, error) {
	now := st.now()
	from, to, ok := records.Period(period, now)
	if !ok {
		return records.Summary{}, fmt.Errorf("unknown period %q", period)
	}
	sum := records.Summary{From: from, To: to, Players: []records.Player{}, Records: []records.Record{}}
	for i, k := range inst.known {
		p := records.Player{Name: k.Name, PlaySeconds: k.PlaySeconds, Visits: k.Sessions, Days: 3 + i, LongestSession: 7200 - float64(i)*900}
		if inst.info.Deaths {
			p.Deaths = 7 - i
			p.LongestLife = 3600 - float64(i)*600
		}
		sum.Players = append(sum.Players, p)
	}
	if len(inst.known) > 0 {
		sum.Records = append(sum.Records, records.Record{ID: "longestSession", Names: []string{inst.known[0].Name}, Value: 7200})
		if inst.info.Deaths {
			sum.Records = append(sum.Records, records.Record{ID: "mostDeaths", Names: []string{inst.known[0].Name}, Value: 7})
		}
	}
	return sum, nil
}

// samplesLocked are made-up readings every 10 seconds over d: the game's
// CPU and memory with its players, while it runs.
func (st *state) samplesLocked(inst *instance, d time.Duration) []stats.Sample {
	now := st.now()
	out := []stats.Sample{}
	for t := now.Add(-d); !t.After(now); t = t.Add(max(10*time.Second, d/360)) {
		on := inst.state == manager.StateOnline && !t.Before(inst.started)
		s := stats.Sample{T: t.UnixMilli(), CPUSys: 9 + 3*math.Sin(float64(t.Unix())/300), MemSys: uint64(totalMB-st.opt.FreeMB) << 20}
		if on {
			s.Running, s.Players = true, len(inst.players)
			s.CPU = 4 + 2*float64(s.Players) + math.Sin(float64(t.Unix())/120)
			s.Mem = uint64(inst.memMB) << 20
		}
		out = append(out, s)
	}
	return out
}

func (st *state) byPlayersLocked(inst *instance) stats.Summary {
	sum := stats.Summary{Since: st.now().AddDate(0, 0, -30), Buckets: []stats.Bucket{}}
	for n := 0; n <= 3; n++ {
		sum.Buckets = append(sum.Buckets, stats.Bucket{Players: n, Seconds: float64(3600 * (4 - n)), CPU: 4 + 2*float64(n),
			CPUMax: 9 + 3*float64(n), CPUSys: 10, Mem: float64(inst.info.MemoryMB<<20) * 0.6, MemMax: float64(inst.info.MemoryMB<<20) * 0.75})
	}
	return sum
}

// --- what the panel changes ------------------------------------------------------------

func (st *state) note(inst *instance, kind, text string) {
	st.actSeq++
	a := manager.Activity{Seq: st.actSeq, Time: st.now(), Kind: kind, Text: text}
	if inst != nil {
		a.Game = inst.id
	}
	st.activity = append(st.activity, a)
}

func (st *state) logLine(inst *instance, text string) {
	inst.logSeq++
	inst.logs = append(inst.logs, manager.LogLine{Seq: inst.logSeq, Time: st.now(), Text: text})
	if len(inst.logs) > 2000 {
		inst.logs = inst.logs[len(inst.logs)-2000:]
	}
}

// goOnline makes a game run with n players since started.
func (st *state) goOnline(inst *instance, n int, started time.Time) {
	inst.gen++
	inst.state, inst.started, inst.runWorld = manager.StateOnline, started, inst.saved().World
	inst.memMB, inst.cpu = inst.info.MemoryMB*3/4, 6.5
	inst.players = nil
	for i := 0; i < n && i < len(names); i++ {
		inst.players = append(inst.players, game.Player{Name: names[i], Account: fmt.Sprintf("765611980000000%02d", i+1),
			Since: started.Add(time.Duration(i+1) * 4 * time.Minute)})
	}
	st.logLine(inst, "Server started on port "+fmt.Sprint(inst.saved().Ports["game"]))
	for _, p := range inst.players {
		st.logLine(inst, p.Name+" joined the game")
	}
	st.note(inst, "online", inst.info.Short+" is online.")
}

func (st *state) goOffline(inst *instance, expected bool) {
	inst.gen++
	uptime := st.now().Sub(inst.started).Seconds()
	inst.lastExit = &manager.ExitInfo{At: st.now(), Code: 0, Expected: expected, World: inst.runWorld, Uptime: uptime}
	if !expected {
		inst.lastExit.Code = 1
		inst.lastExit.LogTail = []string{"[made up] the server was stopped at once"}
	}
	inst.state, inst.players, inst.memMB, inst.cpu, inst.runWorld = manager.StateStopped, nil, 0, 0, ""
	st.logLine(inst, "Server stopped")
	st.note(inst, "stopped", inst.info.Short+" stopped.")
}

// job starts a pretend job in inst's slot that ends after the step time,
// running done (with st.mu held) unless it was cancelled or replaced.
func (st *state) job(inst *instance, kind, title, step string, cancellable bool, done func()) *manager.Job {
	st.jobSeq++
	j := &manager.Job{ID: st.jobSeq, Game: inst.id, Kind: kind, Title: title, Message: step, Percent: -1,
		Started: st.now(), Cancellable: cancellable}
	inst.job = j
	time.AfterFunc(st.opt.Step, func() {
		st.mu.Lock()
		defer st.mu.Unlock()
		if inst.job != j || j.Finished != nil {
			return
		}
		t := st.now()
		j.Finished, j.Result, j.Percent = &t, "Done.", 100
		done()
	})
	return j
}

// busy is the refusal while a game has a job running (409 in the app).
type busy struct{ title string }

func (b busy) Error() string { return "Please wait: " + b.title + " is still going." }

func (st *state) busyLocked(inst *instance) error {
	if inst.job != nil && inst.job.Finished == nil {
		return busy{strings.ToLower(inst.job.Title[:1]) + inst.job.Title[1:]}
	}
	return nil
}

func gb(mb int) string {
	g := float64(mb) / 1024
	if g == math.Trunc(g) {
		return fmt.Sprintf("%.0f GB", g)
	}
	return fmt.Sprintf("%.1f GB", g)
}

// runningGamesLocked are the running games with their memory, largest
// first (the memory warning's buttons).
func (st *state) runningGamesLocked() []manager.RunningGame {
	out := []manager.RunningGame{}
	for _, inst := range st.running() {
		out = append(out, manager.RunningGame{ID: inst.id, Name: inst.info.Short, MemMB: inst.memMB})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].MemMB > out[j].MemMB })
	return out
}
