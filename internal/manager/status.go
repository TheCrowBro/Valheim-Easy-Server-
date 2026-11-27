package manager

import (
	"context"
	"io"
	"net"
	"net/http"
	"runtime"
	"strings"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/platform"
)

// Status is everything the dashboard shows. It is polled every few seconds.
type Status struct {
	Platform     string                `json:"platform"` // "windows" or "darwin" (macOS)
	State        State                 `json:"state"`
	Installed    bool                  `json:"installed"`
	Job          *Job                  `json:"job,omitempty"`
	ServerName   string                `json:"serverName"`
	World        string                `json:"world"`        // selected world
	RunningWorld string                `json:"runningWorld"` // world of the running server
	Players      []PlayerView          `json:"players"`
	Join         JoinInfo              `json:"join"`
	Mods         []ModInfo             `json:"mods,omitempty"`     // the mods players need
	ModsCode     string                `json:"modsCode,omitempty"` // imports the running world's mods in r2modman or Gale
	Discord      *DiscordInfo          `json:"discord,omitempty"`
	Reminder     []string              `json:"reminder,omitempty"` // what players need to do before joining
	App          AppInfo               `json:"app"`
	Version      string                `json:"version,omitempty"`
	Build        string                `json:"build,omitempty"`
	BuildDate    *time.Time            `json:"buildDate,omitempty"`
	StartedAt    *time.Time            `json:"startedAt,omitempty"`
	LastSave     *time.Time            `json:"lastSave,omitempty"`
	LastExit     *ExitInfo             `json:"lastExit,omitempty"`
	NeedsRestart bool                  `json:"needsRestart"`
	Live         Live                  `json:"live"`
	Idle         *IdleInfo             `json:"idle,omitempty"`
	Hibernate    HibernateInfo         `json:"hibernate"`
	Sleep        HibernateInfo         `json:"sleep"`
	RestMode     string                `json:"restMode"`     // config.RestHibernate or config.RestSleep
	CanHibernate bool                  `json:"canHibernate"` // hibernation can be chosen (see config.HibernationOffered)
	Network      []platform.NetAdapter `json:"network"`
	Checklist    Checklist             `json:"checklist"`
	Warnings     []string              `json:"warnings"`
	Activity     []Activity            `json:"activity"`
	Machine      platform.MachineInfo  `json:"machine"`
	Now          time.Time             `json:"now"`

	// New in R2 (spec 4.9); the old panel ignores them.
	Games []GameStatus `json:"games"` // every game this build runs (Valheim only in R2); status_games.go
	Jobs  []Job        `json:"jobs"`  // every unfinished job and each slot's last finished one

	// New in R3 (spec 4.9), filled in by gamesStatusLocked (status_games.go);
	// the old panel ignores them.
	Stars    []string      `json:"stars"`    // the starred instances
	Sidebar  SidebarStatus `json:"sidebar"`  // which sidebar lists are open
	Online   int           `json:"online"`   // games online
	Playing  int           `json:"playing"`  // people playing, in every game
	Router   *RouterStatus `json:"router"`   // null until router.go has looked
	SafeMode string        `json:"safeMode"` // why only the legacy game runs; "" normally
}

// PlayerView is a player in the dashboard list.
type PlayerView struct {
	Name       string    `json:"name"`
	PlatformID string    `json:"platformId,omitempty"`
	Since      time.Time `json:"since"`
}

// JoinInfo tells friends how to connect.
type JoinInfo struct {
	Crossplay  bool     `json:"crossplay"`
	Code       string   `json:"code,omitempty"`
	PublicAddr string   `json:"publicAddr,omitempty"`
	LANAddrs   []string `json:"lanAddrs"`
	Port       int      `json:"port"`
	Password   string   `json:"password"`
	Public     bool     `json:"public"`
	// Problem says why there's no join code: "crossplay" when crossplay
	// couldn't start, "late" when none came a few minutes after the server
	// was ready.
	Problem string `json:"problem,omitempty"`
}

// IdleInfo describes upcoming automatic hibernation.
type IdleInfo struct {
	Minutes     int       `json:"minutes"`
	HibernateAt time.Time `json:"hibernateAt"`
}

// HibernateInfo says whether hibernating (or sleeping) is possible.
type HibernateInfo struct {
	Available bool   `json:"available"`
	Detail    string `json:"detail,omitempty"`
}

// Checklist is the one-time setup on the server computer.
type Checklist struct {
	Installed     bool    `json:"installed"`
	Firewall      bool    `json:"firewall"`
	Hibernate     bool    `json:"hibernate"`
	Wired         bool    `json:"wired"`     // connected by network cable
	WakeOnLAN     bool    `json:"wakeOnLan"` // a connected wired adapter may wake the computer
	LaunchOnLogin bool    `json:"launchOnLogin"`
	FreeDiskGB    float64 `json:"freeDiskGB"`
}

type netInfo struct {
	public     string
	publicAt   time.Time // when public was last found; zero after waking up
	publicTry  time.Time // the last lookup, which may not have worked
	publicBusy bool
}

// publicMaxAge is how long a public address that was looked up is trusted.
const publicMaxAge = 30 * time.Minute

type checkCache struct {
	at            time.Time
	firewall      bool
	wakeRule      bool // the app may hear Wake-on-LAN signals
	hibernate     HibernateInfo
	sleep         HibernateInfo
	network       []platform.NetAdapter
	launchOnLogin bool
	freeDisk      uint64
	freeDiskOK    bool
}

// Status returns a snapshot for the dashboard. It holds m.mu only for
// what is in memory (spec 4.8), apart from the legacy game's quick look at
// its program and build, as it always did.
func (m *Manager) Status() Status {
	acts := m.activity.last(40)
	lan := lanIPs()
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.settings
	inst := m.legacy()
	st := Status{
		Platform:     runtime.GOOS,
		CanHibernate: config.CanHibernate(),
		State:        m.instStateLocked(inst),
		Installed:    m.installedLocked(inst),
		Players:      []PlayerView{},
		Live:         m.live,
		LastExit:     inst.lastExit,
		Hibernate:    m.checks.hibernate,
		Sleep:        m.checks.sleep,
		RestMode:     s.RestMode,
		Network:      append([]platform.NetAdapter{}, m.checks.network...),
		Activity:     acts,
		Machine:      m.machine,
		Now:          time.Now(),
		Warnings:     []string{},
		App:          m.appInfoLocked(),
		Jobs:         m.jobsLocked(),
	}
	if j := m.legacyJobLocked(); j != nil {
		c := *j
		st.Job = &c
	}
	saved := inst.saved()
	st.ServerName, st.World = saved.Server["serverName"], saved.World
	build, hasBuild := inst.srv.Build()
	if hasBuild {
		st.Build = build.Build
		if !build.Date.IsZero() {
			date := build.Date
			st.BuildDate = &date
		}
	}
	r := inst.run
	var runState *game.RunState
	var rec *game.Record
	if r != nil {
		rs := r.state()
		runState, rec = &rs, &r.rec
	}
	// What friends join with: the running server's name, port, password
	// and crossplay, not changes waiting for a restart.
	j := inst.srv.Join(game.JoinInput{Rec: rec, State: runState, Saved: saved, PublicIP: m.net.public, LAN: lan})
	st.Join = JoinInfo{Crossplay: j.Crossplay, Code: j.Code, PublicAddr: j.Address, LANAddrs: j.LAN, Port: j.Port,
		Password: j.Password, Public: j.Public, Problem: j.Problem}
	world := saved.World // the mods players need: the running world's, or the next one's
	if r != nil {
		world = r.rec.World
	}
	ml, hasMods := inst.srv.(game.ModLister)
	if hasMods {
		st.Mods = ml.PlayerMods(world, rec)
	}
	if r != nil {
		st.RunningWorld = r.rec.World
		launched := r.rec.Launched
		st.StartedAt = &launched
		for _, p := range runState.Players {
			st.Players = append(st.Players, PlayerView{Name: p.Name, PlatformID: p.Account, Since: p.Since})
		}
		st.Version = runState.Version
		if !runState.LastSave.IsZero() {
			ls := runState.LastSave
			st.LastSave = &ls
		}
		if hasMods {
			st.ModsCode = ml.ModsCode(rec)
		}
		st.Reminder = m.reminderLocked(inst, r)
		if s.Discord {
			st.Discord = &DiscordInfo{Error: r.discordErr}
			if !r.discordAt.IsZero() {
				at := r.discordAt
				st.Discord.Posted = &at
			}
		}
		st.NeedsRestart = r.rec.ArgsKey != "" && r.rec.ArgsKey != inst.srv.Fingerprint(saved, "")
		if r.replaying {
			st.Warnings = append(st.Warnings, "Catching up with the server log…")
		}
	}
	if s.Discord && st.Discord == nil {
		st.Discord = &DiscordInfo{}
	}
	if st.Discord != nil {
		st.Discord.Daily = s.DiscordDaily
	}
	if st.Join.PublicAddr == "" || time.Since(m.net.publicAt) > publicMaxAge {
		m.lookupPublicIPLocked()
	}
	if at := m.idleDeadlineLocked(); !at.IsZero() {
		st.Idle = &IdleInfo{Minutes: s.IdleMinutes, HibernateAt: at}
	}
	st.Checklist = Checklist{
		Installed:     st.Installed,
		Firewall:      m.checks.firewall && (m.checks.wakeRule || !s.DiscordWake),
		Hibernate:     m.checks.hibernate.Available,
		LaunchOnLogin: m.checks.launchOnLogin,
	}
	for _, a := range m.checks.network {
		if a.Wired && a.Connected {
			st.Checklist.Wired = true
			st.Checklist.WakeOnLAN = st.Checklist.WakeOnLAN || a.WakeArmed
		}
	}
	if m.checks.freeDiskOK {
		st.Checklist.FreeDiskGB = float64(m.checks.freeDisk) / (1 << 30)
		if m.checks.freeDisk < 2<<30 {
			st.Warnings = append(st.Warnings, "The drive with the worlds is almost full. "+inst.short()+" stops saving when it runs out of space: free up some room.")
		}
	}
	if errs := inst.srv.Validate(saved, ""); errs != nil {
		st.Warnings = append(st.Warnings, "Some settings need fixing before the server can start: "+errs.Error())
	}
	st.Warnings = append(st.Warnings, m.foreignWarningsLocked()...)
	// The new panel's part, with what was worked out above for the legacy
	// game, so its adapter isn't asked twice.
	m.gamesStatusLocked(&st, lan, gameView{saved: saved, state: st.State, installed: st.Installed,
		build: build, hasBuild: hasBuild, runState: runState, rec: rec, join: j, needsRestart: st.NeedsRestart})
	return st
}

// jobsLocked lists every unfinished job and each slot's last finished one.
func (m *Manager) jobsLocked() []Job {
	out := []Job{}
	if m.appJob != nil {
		out = append(out, *m.appJob)
	}
	m.eachInstance(func(i *instance) {
		if i.job != nil {
			out = append(out, *i.job)
		}
	})
	return out
}

// lanIPs lists this computer's local network addresses (IPv4), for how
// friends at home join.
func lanIPs() []string {
	var out []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil || ipn.IP.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, ipn.IP.String())
		}
	}
	return out
}

// lookupPublicIPLocked fetches the internet address in the background.
func (m *Manager) lookupPublicIPLocked() {
	if m.opt.NoPublicIP || m.net.publicBusy || time.Since(m.net.publicTry) < 2*time.Minute {
		return
	}
	m.net.publicBusy, m.net.publicTry = true, time.Now()
	go func() {
		defer m.recoverPanic()
		ip := fetchPublicIP()
		m.mu.Lock()
		m.net.publicBusy = false
		// A lookup that didn't work (say, just after waking up, before the
		// network is back) doesn't make an old address count as fresh.
		if ip != "" {
			m.net.public, m.net.publicAt = ip, time.Now()
		}
		m.mu.Unlock()
	}()
}

// publicIPURL answers with this computer's internet address.
var publicIPURL = "https://api.ipify.org"

func fetchPublicIP() string {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, publicIPURL, nil)
	if err != nil {
		return ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	ip := strings.TrimSpace(string(b))
	if net.ParseIP(ip) == nil {
		return ""
	}
	return ip
}

// checksLoop refreshes slower checks (firewall, disk space, hibernation).
func (m *Manager) checksLoop() {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		inst := m.legacy()
		m.mu.Lock()
		due := time.Since(m.checks.at) > time.Minute
		save, serverExe := inst.dirs.Saves, inst.srv.Program()
		m.mu.Unlock()
		if due {
			var c checkCache
			c.at = time.Now()
			c.firewall = platform.FirewallReady(serverExe)
			c.wakeRule = platform.WakeRuleReady(m.opt.AppExe)
			c.hibernate.Available, c.hibernate.Detail = m.opt.HibernateStatus()
			c.sleep.Available, c.sleep.Detail = m.opt.SleepStatus()
			c.network = m.opt.NetAdapters()
			c.launchOnLogin, _ = platform.LaunchOnLogin()
			c.freeDisk, c.freeDiskOK = platform.FreeDisk(save)
			m.mu.Lock()
			m.checks = c
			m.mu.Unlock()
			m.scanForeign() // other games' servers: still running?
		}
		select {
		case <-m.ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// RefreshChecks makes the next status re-run the setup checks.
func (m *Manager) RefreshChecks() {
	m.mu.Lock()
	m.checks.at = time.Time{}
	m.mu.Unlock()
}

// recheckSoon re-runs the setup checks a few times over the next half
// minute, to notice changes made after a Windows permission prompt.
func (m *Manager) recheckSoon() {
	for _, d := range []time.Duration{0, 5 * time.Second, 15 * time.Second, 30 * time.Second} {
		time.AfterFunc(d, m.RefreshChecks)
	}
}

// SetLaunchOnLogin adds or removes the app from Windows startup.
func (m *Manager) SetLaunchOnLogin(on bool) error {
	err := platform.SetLaunchOnLogin(on, m.opt.AppExe, "--background")
	m.RefreshChecks()
	return err
}
