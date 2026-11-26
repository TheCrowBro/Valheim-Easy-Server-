package satisfactory

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
)

// The server's log lines the Monitor reads (FACTS.md has each with its
// source). Unreal Engine writes each line with the time in UTC and the
// frame number first, "[2024.09.12-11.27.38:063][  0]LogServer: …"; the
// lines written before the engine's clock starts have no time.
//
//	LogInit: Build: ++FactoryGame+rel-main-1.0.0-CL-365306
//	LogServer: Warning: SHA256:Re8Y8l7EB+699kPspYduzdfA9PewDxrATl2tZlL2kRQ=
//	LogServer: Display: Server API listening on '0.0.0.0:7777' (Standalone)
//	LogNet: GameNetDriver IpNetDriver_2147482574 IpNetDriver listening on port 7777
//	LogNet: Login request: ?…?Name=Alex userId: Steam:76561198000000001 platform: Steam
//	LogNet: Join succeeded: Alex
//	LogNet: UNetConnection::Close: [UNetConnection] RemoteAddr: …, UniqueId: Steam:76561198000000001, …
//	LogExit: Preparing to exit.
//
// Who is on comes first from the API's count, which the probe asks every
// 30 seconds and which a player can't fake: its answer is the count the app
// uses (spec 2.6). The join and leave lines only name the players, and
// show a change sooner. A count of 0 means nobody is on, whatever the log
// said.
var (
	reStamp       = regexp.MustCompile(`^\[(\d{4})\.(\d{2})\.(\d{2})-(\d{2})\.(\d{2})\.(\d{2}):(\d{3})\]\[\s*\d+\]`)
	reCategory    = regexp.MustCompile(`^(Log[A-Za-z0-9_]+):`)
	reBuild       = regexp.MustCompile(`^LogInit: Build: \+\+FactoryGame\+(\S*?)-CL-(\d+)`)
	reVersionNum  = regexp.MustCompile(`(\d+(?:\.\d+)+)$`)
	reFingerprint = regexp.MustCompile(`LogServer: Warning: (SHA256:[A-Za-z0-9+/]{40,}={0,2})\s*$`)
	reAPIListen   = regexp.MustCompile(`^LogServer: Display: Server API listening on '([^']+)'`)
	reNetListen   = regexp.MustCompile(`^LogNet: GameNetDriver .*listening on port (\d+)`)
	reLogin       = regexp.MustCompile(`^LogNet: Login request: .*\?Name=(.*?) userId: (\S+)`)
	reJoined      = regexp.MustCompile(`^LogNet: Join succeeded: (.+?)\s*$`)
	reClosed      = regexp.MustCompile(`^LogNet: (?:UNetConnection::Close|UChannel::CleanUp): .*UniqueId: ([^,\s]+)`)
	reSaved       = regexp.MustCompile(`World Serialization \(save\)|^LogGame: .*(?i:saving game|game saved)`)
	reStopping    = regexp.MustCompile(`^LogExit: Preparing to exit|\*\*\* INTERRUPTED \*\*\*`)
)

// RunState.Extra keys.
const (
	extraFingerprint = "certFingerprint" // the certificate fingerprint the server logged
	extraAPI         = "api"             // where its API listens
	extraBuild       = "build"           // its build number (Net CL)
)

// noise are the start-up chatter categories of every Unreal Engine server,
// which tell the owner nothing. Their errors still show.
var noise = map[string]bool{
	"LogPluginManager": true, "LogPackageName": true, "LogLocaTools": true, "LogTextLocalizationResource": true,
	"LogStringTable": true, "LogConsoleManager": true, "LogIoDispatcher": true, "LogPakFile": true,
	"LogConfig": true, "LogDeviceProfileManager": true, "LogStreaming": true, "LogTextureEncodingSettings": true,
	"LogAudio": true, "LogMetasoundEngine": true, "LogWwiseNiagara": true, "LogStats": true, "LogCore": true,
	"LogHAL": true, "LogUProjectInfo": true, "LogICUInternationalization": true, "LogAssetRegistry": true,
	"LogRendererCore": true, "LogLinux": true, "LogTextLocalizationManager": true, "LogPackageLocalizationCache": true,
	"LogOnlineSchema": true, "LogOnlineServices": true, "LogNetVersion": true, "LogHttpListener": true,
	"LogHttpServerModule": true, "LogMemory": true, "LogWindows": true, "LogShaderLibrary": true, "LogLoad": true,
}

type monitor struct {
	known    func(name string) string
	gamePort int
	state    game.RunState
	ids      map[string]string // the account each name logged in with
	names    map[string]string // and the name of each account
}

// Watch follows one run.
func (s *server) Watch(rec game.Record, known func(name string) string) game.Monitor {
	if known == nil {
		known = func(string) string { return "" }
	}
	return &monitor{known: known, gamePort: s.gamePort(rec), ids: map[string]string{}, names: map[string]string{},
		state: game.RunState{Count: -1, Extra: map[string]string{}}}
}

// splitLine takes the engine's time and frame off a line.
func splitLine(line string) (string, time.Time) {
	line = strings.TrimRight(line, "\r\n")
	m := reStamp.FindStringSubmatch(line)
	if m == nil {
		return strings.TrimSpace(line), time.Time{}
	}
	n := make([]int, 7)
	for i := range n {
		n[i], _ = strconv.Atoi(m[i+1])
	}
	t := time.Date(n[0], time.Month(n[1]), n[2], n[3], n[4], n[5], n[6]*int(time.Millisecond), time.UTC)
	return strings.TrimSpace(line[len(m[0]):]), t
}

func (m *monitor) Line(raw string, now time.Time) game.Line {
	text, at := splitLine(raw)
	l := game.Line{Text: text, Time: at}
	when := at
	if when.IsZero() {
		when = now
	}
	cat := ""
	if c := reCategory.FindStringSubmatch(text); c != nil {
		cat = c[1]
	}
	switch {
	case text == "" || strings.HasPrefix(text, "Log file open") || strings.HasPrefix(text, "Log file closed"):
		l.Hide = true
	case reBuild.MatchString(text):
		g := reBuild.FindStringSubmatch(text)
		m.state.Extra[extraBuild] = g[2]
		if v := reVersionNum.FindString(g[1]); v != "" {
			m.state.Version = v + " (build " + g[2] + ")"
		} else {
			m.state.Version = "build " + g[2]
		}
	case reFingerprint.MatchString(text):
		m.state.Extra[extraFingerprint] = reFingerprint.FindStringSubmatch(text)[1]
	case reAPIListen.MatchString(text):
		m.state.Extra[extraAPI] = reAPIListen.FindStringSubmatch(text)[1]
	case reNetListen.MatchString(text):
		port, _ := strconv.Atoi(reNetListen.FindStringSubmatch(text)[1])
		if m.gamePort > 0 && port > 0 && port != m.gamePort {
			// The server takes the next free port by itself when its own
			// is taken (FACTS.md), and friends would knock on the wrong one.
			l.Events = append(l.Events, game.Event{Kind: "port_moved", Time: when,
				Text: "The Satisfactory server couldn't use port " + strconv.Itoa(m.gamePort) + " and took port " + strconv.Itoa(port) +
					" instead, so friends can't reach it on the usual port. Stop it, close the program that holds port " + strconv.Itoa(m.gamePort) + ", and start it again."})
		}
	case reLogin.MatchString(text):
		g := reLogin.FindStringSubmatch(text)
		name, id := strings.TrimSpace(g[1]), strings.TrimSpace(g[2])
		if name != "" && id != "" {
			m.ids[name], m.names[id] = id, name
		}
	case reJoined.MatchString(text):
		l.Events = append(l.Events, m.join(strings.TrimSpace(reJoined.FindStringSubmatch(text)[1]), when)...)
	case reClosed.MatchString(text):
		if name := m.names[reClosed.FindStringSubmatch(text)[1]]; name != "" {
			l.Events = append(l.Events, m.leave(name, when)...)
		}
	case reSaved.MatchString(text):
		m.state.LastSave = when
		l.Events = append(l.Events, game.Event{Kind: "saved", Time: when})
	case reStopping.MatchString(text):
		if !m.state.Stopping {
			m.state.Stopping, m.state.StoppingAt = true, when
			l.Events = append(l.Events, game.Event{Kind: "stopping", Time: when})
		}
	case noise[cat] && !strings.Contains(text, "Error") && !strings.Contains(text, "Fatal"):
		l.Hide = true
	}
	return l
}

func (m *monitor) listed(name string) bool {
	return slices.ContainsFunc(m.state.Players, func(p game.Player) bool { return p.Name == name })
}

func (m *monitor) join(name string, now time.Time) []game.Event {
	if name == "" || m.listed(name) {
		return nil
	}
	account := m.ids[name]
	if account == "" {
		account = m.known(name)
	}
	m.state.Players = append(m.state.Players, game.Player{Name: name, Account: account, Since: now})
	return []game.Event{{Kind: "join", Time: now, Name: name, Account: account}}
}

func (m *monitor) leave(name string, now time.Time) []game.Event {
	i := slices.IndexFunc(m.state.Players, func(p game.Player) bool { return p.Name == name })
	if i < 0 {
		return nil
	}
	p := m.state.Players[i]
	m.state.Players = slices.Delete(m.state.Players, i, i+1)
	return []game.Event{{Kind: "leave", Time: now, Name: name, Account: p.Account}}
}

func (m *monitor) Tick(now time.Time) []game.Event { return nil }

// Probed folds in the API's answer: once it says the run's world is being
// played, the server is online; its count is who is on.
func (m *monitor) Probed(p game.ProbeResult, now time.Time) []game.Event {
	var evs []game.Event
	if p.Up && !m.state.Ready {
		m.state.Ready = true
		evs = append(evs, game.Event{Kind: "online", Time: now, Text: "The Satisfactory server is online."})
	}
	if p.Version != "" {
		m.state.Version = p.Version
	}
	if p.Count >= 0 {
		m.state.Count = p.Count
		switch {
		case p.Count > 0:
			// The server's own count can't be faked by a player: it
			// holds the computer awake (spec 2.6).
			evs = append(evs, game.Event{Kind: "count", Time: now, Count: p.Count, Hold: true})
		case len(m.state.Players) > 0:
			// Nobody is on: a leave the log didn't show (or worded
			// another way) mustn't keep a name listed.
			for _, pl := range slices.Clone(m.state.Players) {
				evs = append(evs, m.leave(pl.Name, now)...)
			}
		}
	}
	return evs
}

func (m *monitor) State() game.RunState {
	st := m.state
	st.Players = slices.Clone(m.state.Players)
	st.Extra = map[string]string{}
	for k, v := range m.state.Extra {
		st.Extra[k] = v
	}
	return st
}

// End: the run is over, so whoever is listed leaves.
func (m *monitor) End(now time.Time) []game.Event {
	var evs []game.Event
	for _, p := range slices.Clone(m.state.Players) {
		evs = append(evs, m.leave(p.Name, now)...)
	}
	m.state.Ready = false
	m.state.Count = -1
	return evs
}
