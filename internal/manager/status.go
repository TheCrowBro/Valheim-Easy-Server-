package manager

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/mods"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/steamcmd"
)

// Status is everything the dashboard shows. It is polled every few seconds.
type Status struct {
	State        State                 `json:"state"`
	Installed    bool                  `json:"installed"`
	Job          *Job                  `json:"job,omitempty"`
	ServerName   string                `json:"serverName"`
	World        string                `json:"world"`        // selected world
	RunningWorld string                `json:"runningWorld"` // world of the running server
	Players      []PlayerView          `json:"players"`
	Join         JoinInfo              `json:"join"`
	Mods         []ModInfo             `json:"mods,omitempty"` // the mods players need
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
	RestMode     string                `json:"restMode"` // config.RestHibernate or config.RestSleep
	Network      []platform.NetAdapter `json:"network"`
	Checklist    Checklist             `json:"checklist"`
	Warnings     []string              `json:"warnings"`
	Activity     []Activity            `json:"activity"`
	Machine      platform.MachineInfo  `json:"machine"`
	Now          time.Time             `json:"now"`
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
	publicAt   time.Time
	publicBusy bool
}

type checkCache struct {
	at            time.Time
	firewall      bool
	hibernate     HibernateInfo
	sleep         HibernateInfo
	network       []platform.NetAdapter
	launchOnLogin bool
	freeDisk      uint64
	freeDiskOK    bool
}

// Status returns a snapshot for the dashboard.
func (m *Manager) Status() Status {
	acts := m.activity.last(40)
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.settings
	st := Status{
		State:      m.stateLocked(),
		Installed:  m.installedLocked(),
		ServerName: s.ServerName,
		World:      s.World,
		Players:    []PlayerView{},
		Live:       m.live,
		LastExit:   m.lastExit,
		Hibernate:  m.checks.hibernate,
		Sleep:      m.checks.sleep,
		RestMode:   s.RestMode,
		Network:    append([]platform.NetAdapter{}, m.checks.network...),
		Activity:   acts,
		Machine:    m.machine,
		Now:        time.Now(),
		Warnings:   []string{},
		App:        m.appInfoLocked(),
	}
	if m.job != nil {
		j := *m.job
		st.Job = &j
	}
	if build, date, ok := steamcmd.InstalledBuild(m.paths.Server); ok {
		st.Build = build
		if !date.IsZero() {
			st.BuildDate = &date
		}
	}
	st.Join = JoinInfo{Crossplay: s.Crossplay, Port: s.Port, Password: s.Password, Public: s.Public, LANAddrs: lanAddrs(s.Port)}
	world := s.World // the mods players need: the running world's, or the next one's
	if m.run != nil {
		world = m.run.info.World
	}
	for _, id := range s.WorldOptions[world].Mods {
		if md, ok := mods.Find(id); ok {
			st.Mods = append(st.Mods, ModInfo{Mod: md, Version: m.modVersions[md.Package]})
		}
	}
	if r := m.run; r != nil {
		st.RunningWorld = r.info.World
		launched := r.info.Launched
		st.StartedAt = &launched
		for _, p := range r.tracker.Players() {
			st.Players = append(st.Players, PlayerView{Name: p.Name, PlatformID: p.PlatformID, Since: p.Since})
		}
		st.Version = r.tracker.Version
		if !r.tracker.LastSave.IsZero() {
			ls := r.tracker.LastSave
			st.LastSave = &ls
		}
		st.Join.Code = r.tracker.JoinCode
		st.Join.PublicAddr = r.tracker.PublicAddr
		st.Reminder = m.reminderLocked(r)
		if s.Discord {
			st.Discord = &DiscordInfo{Error: r.discordErr}
			if !r.discordAt.IsZero() {
				at := r.discordAt
				st.Discord.Posted = &at
			}
		}
		st.NeedsRestart = r.info.ArgsKey != "" && r.info.ArgsKey != argsKey(s, m.paths.Save)
		if r.replaying {
			st.Warnings = append(st.Warnings, "Catching up with the server log…")
		}
	}
	if s.Discord && st.Discord == nil {
		st.Discord = &DiscordInfo{}
	}
	if st.Join.PublicAddr == "" && m.net.public != "" {
		st.Join.PublicAddr = net.JoinHostPort(m.net.public, strconv.Itoa(s.Port))
	}
	if st.Join.PublicAddr == "" || time.Since(m.net.publicAt) > 30*time.Minute {
		m.lookupPublicIPLocked()
	}
	if at := m.idleDeadlineLocked(); !at.IsZero() {
		st.Idle = &IdleInfo{Minutes: s.IdleMinutes, HibernateAt: at}
	}
	st.Checklist = Checklist{
		Installed:     st.Installed,
		Firewall:      m.checks.firewall,
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
			st.Warnings = append(st.Warnings, "The drive with the worlds is almost full. Valheim stops saving when it runs out of space: free up some room.")
		}
	}
	if errs := s.Validate(); errs != nil {
		st.Warnings = append(st.Warnings, "Some settings need fixing before the server can start: "+errs.Error())
	}
	return st
}

// lanAddrs lists this computer's local network addresses with the port.
func lanAddrs(port int) []string {
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
			out = append(out, net.JoinHostPort(ipn.IP.String(), strconv.Itoa(port)))
		}
	}
	return out
}

// lookupPublicIPLocked fetches the internet address in the background.
func (m *Manager) lookupPublicIPLocked() {
	if m.opt.NoPublicIP || m.net.publicBusy || time.Since(m.net.publicAt) < 2*time.Minute {
		return
	}
	m.net.publicBusy = true
	go func() {
		defer m.recoverPanic()
		ip := fetchPublicIP()
		m.mu.Lock()
		m.net.publicBusy = false
		m.net.publicAt = time.Now()
		if ip != "" {
			m.net.public = ip
		}
		m.mu.Unlock()
	}()
}

func fetchPublicIP() string {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.ipify.org", nil)
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
		m.mu.Lock()
		due := time.Since(m.checks.at) > time.Minute
		save := m.paths.Save
		m.mu.Unlock()
		if due {
			var c checkCache
			c.at = time.Now()
			c.firewall = platform.FirewallReady()
			c.hibernate.Available, c.hibernate.Detail = m.opt.HibernateStatus()
			c.sleep.Available, c.sleep.Detail = m.opt.SleepStatus()
			c.network = platform.NetAdapters()
			c.launchOnLogin, _ = platform.LaunchOnLogin()
			c.freeDisk, c.freeDiskOK = platform.FreeDisk(save)
			m.mu.Lock()
			m.checks = c
			m.mu.Unlock()
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

// FixFirewall adds the firewall rules (asks for admin permission).
func (m *Manager) FixFirewall() error {
	m.mu.Lock()
	exe, data := m.paths.ServerExe, m.opt.DataDir
	m.mu.Unlock()
	err := platform.FixFirewall(exe, m.opt.AppExe, data)
	m.recheckSoon()
	return err
}

// SetLaunchOnLogin adds or removes the app from Windows startup.
func (m *Manager) SetLaunchOnLogin(on bool) error {
	err := platform.SetLaunchOnLogin(on, m.opt.AppExe, "--background")
	m.RefreshChecks()
	return err
}
