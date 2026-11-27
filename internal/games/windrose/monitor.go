package windrose

import (
	"regexp"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
)

// The server's log, R5/Saved/Logs/R5.log, as real servers wrote it and
// hosting tools read it (FACTS.md, "The log" and "Who is playing";
// samples in testdata/real). It is Unreal Engine's log:
//
//	[2026.06.05-03.40.53:314][347]R5LogCoopProxy:               [026308] UR5CoopProxyServer::OnAccountDisconnected          Account disconnected. Inform Cm. AccountId BA5D…. … [D:\Source\…\R5CoopProxyServer.cpp:613]
//
// a time in UTC and a frame number, a category (and "Warning:",
// "Verbose:" …), then Windrose's own part: a frame, the function, the
// message, and where in the source it was written. A message of several
// lines goes on below with no time, its source at the end of its last
// line. The lists of accounts ("Connected Accounts", "Reserved Accounts",
// "Disconnected Accounts", one numbered line per account) come that way,
// after an account connects or leaves and at other moments.
var (
	reStamp = regexp.MustCompile(`^\[(\d{4})\.(\d{2})\.(\d{2})-(\d{2})\.(\d{2})\.(\d{2}):(\d{3})\]\[\s*\d+\]`)
	// The source of a message, at the end of its (last) line.
	reSource = regexp.MustCompile(`\s*\[[A-Za-z]:\\[^\]]*:\d+\]\s*$`)
	// "R5LogCoopProxy: Verbose:" and the like: the category and level.
	reCategory = regexp.MustCompile(`^([A-Za-z0-9_]+):\s+(?:(Verbose|VeryVerbose|Log|Display|Warning|Error|Fatal):\s+)?`)

	// Ready: the world is loaded and friends can connect ("fires after
	// the save DB has loaded and the listeners are up", FACTS.md), or the
	// server's data keeper is waiting for its first player.
	reReady   = regexp.MustCompile(`Host server is ready for owner to connect`)
	reWaiting = regexp.MustCompile(`Change state \w+ => WaitingForFirstAccount\b`)
	// What the server says about itself at the start.
	reInvite     = regexp.MustCompile(`>\s*InviteCode\s*:\s*([0-9A-Za-z]+)`)
	rePasswordIs = regexp.MustCompile(`(>\s*Password\s*:\s*)(.*)$`)
	reDeployment = regexp.MustCompile(`"DeploymentId"\s*:\s*"([^"]*)"`)
	reIsland     = regexp.MustCompile(`Server initialized\. CurrentIslandId ([0-9A-Fa-f]{32})`)
	reRegistered = regexp.MustCompile(`Server registration finished successfully`)
	reNoBackend  = regexp.MustCompile(`Server registration finished with error|GcStream is broken|GsStream is broken`)
	reLoadFailed = regexp.MustCompile(`Cannot load island '([^']*)'`)

	// Players. A player who joins is named by Unreal's own join line,
	// and the server's account lines give the account (a 32-digit id)
	// and tie it to the name; a player who leaves is named only by the
	// account, in several lines (one per part of the server that lets
	// go of it). Each may come twice.
	reJoinSucceeded = regexp.MustCompile(`LogNet: Join succeeded: (.+?)\s*$`)
	reAccountName   = regexp.MustCompile(`AccountName '([^']*)'\. AccountId ([0-9A-Fa-f]{32})`)
	reConnected     = regexp.MustCompile(`Account connected\. AccountId ([0-9A-Fa-f]{32})`)
	reGone          = regexp.MustCompile(`Account (?:disconnected\.(?: Inform Cm\.)?|was disconnected\.) AccountId ([0-9A-Fa-f]{32})`)
	reDumpRow       = regexp.MustCompile(`^\s*\d+\.\s+Name '(.*?)'\. AccountId '([0-9A-Fa-f]*)'\. State '([^']*)'`)
	reWrongPassword = regexp.MustCompile(`Client sent wrong password`)
	reBadVersion    = regexp.MustCompile(`DeploymentId mismatch\. AccountId ([0-9A-Fa-f]*)\. ServerDeploymentId '([^']*)'\. ClientDeploymentId '([^']*)'`)
	// The passwords that line names, hidden on the Log page, and the
	// password in the settings file the server prints when it starts.
	rePasswords    = regexp.MustCompile(`((?:Client|Server)Password )\S*`)
	reJSONPassword = regexp.MustCompile(`^(\s*"Password"\s*:\s*)".*"(,?)\s*$`)

	// Saving: the server's own backup of the world, every minute or so
	// while it changes and when it stops.
	reSaved      = regexp.MustCompile(`Save backup has finished successfully`)
	reSaveFailed = regexp.MustCompile(`Save backup finished with error\. Error '(.*)'`)
	// A stop: Unreal's answer to Ctrl+C (and to its window being closed).
	// Only a request from outside makes these lines; a crash doesn't.
	reStopping = regexp.MustCompile(`\*\*\* INTERRUPTED \*\*\*|Engine exit requested \(reason: ConsoleCtrl`)
)

// The account-list headers, each on a line of its own.
const (
	sectionConnected    = "Connected Accounts"
	sectionReserved     = "Reserved Accounts"
	sectionDisconnected = "Disconnected Accounts"
)

// noisy are the parts of the server that write many lines the owner never
// needs (its database's syncs, the network's handshakes, the memory
// report), hidden on the Log page with the lines that go on below them.
var noisy = []string{
	"R5LogBLDalAQ", "R5LogP2pGate", "R5LogIceProtocol", "R5LogSystemResources", "R5LogBLBiz",
	"R5LogNetBL", "R5LogNetCm", "R5LogNet", "LogStreaming", "R5LogBLDocMan", "R5LogObjectInspector",
	"R5LogTerrainGenerator", "R5LogIslandGenerator",
}

// registerWait is how long after the server registered with Windrose's
// online service (it prints its invite code then) the Monitor waits for
// its ready line before it says the server is online anyway, in case a
// later version words that line differently.
const registerWait = 3 * time.Minute

// monitor follows one run of the server. Every method is called with the
// manager's lock held, so it calls nothing outside itself.
type monitor struct {
	state game.RunState

	accounts map[string]string    // account (upper case) → the name it plays as
	waiting  map[string]time.Time // accounts that connected before their name was known

	// The account list being read, if one is.
	dump     bool
	section  string
	sections map[string]bool
	rows     []dumpRow

	hidden       bool // the message the coming lines belong to is hidden
	codeSeen     bool
	registeredAt time.Time
	stopping     bool
	warned       map[string]bool
}

type dumpRow struct{ section, name, account, state string }

// Watch follows a run. Every account line names the account itself, so
// the Monitor needs no account the name last played from.
func (s *server) Watch(rec game.Record, known func(name string) string) game.Monitor {
	m := &monitor{state: game.RunState{Count: 0}, accounts: map[string]string{}, waiting: map[string]time.Time{}, warned: map[string]bool{}}
	if code := extraString(rec, "inviteCode"); code != "" {
		m.state.Code = code
	}
	if id := extraString(rec, "worldId"); id != "" {
		m.state.Extra = map[string]string{"worldId": id}
	}
	return m
}

func (m *monitor) Line(raw string, now time.Time) game.Line {
	text := strings.TrimRight(strings.TrimPrefix(raw, byteOrderMark), "\r")
	out := game.Line{}
	add := func(e game.Event) {
		if e.Time.IsZero() {
			e.Time = out.Time
			if e.Time.IsZero() {
				e.Time = now
			}
		}
		out.Events = append(out.Events, e)
	}

	stamp := reStamp.FindStringSubmatch(text)
	if stamp != nil {
		out.Time = lineTime(stamp)
		text = text[len(stamp[0]):]
		m.endDump(now, add)
		m.hidden = false
	}
	body := reSource.ReplaceAllString(text, "")
	trimmed := strings.TrimSpace(body)

	switch {
	case stamp != nil:
		cat := reCategory.FindStringSubmatch(body)
		if cat != nil && (cat[2] == "Verbose" || cat[2] == "VeryVerbose" || isNoisy(cat[1])) {
			m.hidden = true
		}
	case trimmed == sectionConnected || trimmed == sectionReserved || trimmed == sectionDisconnected:
		if !m.dump {
			m.dump, m.sections, m.rows = true, map[string]bool{}, nil
		}
		m.section = trimmed
		m.sections[trimmed] = true
	case m.dump && reDumpRow.MatchString(body):
		g := reDumpRow.FindStringSubmatch(body)
		m.rows = append(m.rows, dumpRow{section: m.section, name: g[1], account: strings.ToUpper(g[2]), state: g[3]})
	default:
		// A line that isn't part of an account list ends one; its
		// source line (" [D:\…]") is the end of the list itself.
		m.endDump(now, add)
	}
	out.Hide = m.hidden || trimmed == ""
	out.Text = collapse(strings.TrimRight(body, " \t"))
	if reJSONPassword.MatchString(body) {
		out.Text = reJSONPassword.ReplaceAllString(body, `${1}"(hidden)"${2}`)
	}

	switch {
	case reReady.MatchString(body), reWaiting.MatchString(body):
		for _, e := range m.online(now) {
			add(e)
		}

	case reInvite.MatchString(body):
		m.state.Code = reInvite.FindStringSubmatch(body)[1]
		if !m.codeSeen {
			m.codeSeen = true
			add(game.Event{Kind: "code"})
		}

	case rePasswordIs.MatchString(body):
		// The server prints its password with its invite code; the Log
		// page leaves it out.
		out.Text = collapse(rePasswordIs.ReplaceAllString(strings.TrimRight(body, " \t"), "${1}(hidden)"))

	case reDeployment.MatchString(body):
		v, _, _ := strings.Cut(reDeployment.FindStringSubmatch(body)[1], "-")
		m.state.Version = v

	case reIsland.MatchString(body):
		m.extra("worldId", strings.ToUpper(reIsland.FindStringSubmatch(body)[1]))

	case reRegistered.MatchString(body):
		if m.registeredAt.IsZero() {
			m.registeredAt = now // the wait counts on the app's clock
		}

	case reNoBackend.MatchString(body):
		m.extra("backend", "failed")
		m.warn("no_backend", add, "The Windrose server can't reach Windrose's online service, so friends can't find it by its invite code. Check this computer's internet connection.")

	case reLoadFailed.MatchString(body):
		add(game.Event{Kind: "load_failed", Text: "The Windrose server couldn't load the world it was asked to."})

	case reJoinSucceeded.MatchString(body):
		name := reJoinSucceeded.FindStringSubmatch(body)[1]
		account := m.accountOf(name)
		if account == "" {
			// The account line came first, and no list of accounts named
			// it: the one account that has just connected is this player
			// (the server writes the two within a second or two).
			account = m.lone(now)
			if account != "" {
				m.accounts[account] = name
			}
		}
		m.join(name, account, now, add)

	case reAccountName.MatchString(body):
		g := reAccountName.FindStringSubmatch(body)
		name, account := g[1], strings.ToUpper(g[2])
		m.accounts[account] = name
		if _, ok := m.waiting[account]; ok {
			m.join(name, account, now, add)
		} else if i := m.find(account); i >= 0 {
			m.state.Players[i].Name = name
		} else if i := m.findName(name); i >= 0 && m.state.Players[i].Account == "" {
			m.state.Players[i].Account = account // its join line came first
		}

	case reConnected.MatchString(body):
		account := strings.ToUpper(reConnected.FindStringSubmatch(body)[1])
		switch name, ok := m.accounts[account]; {
		case ok && name != "":
			m.join(name, account, now, add)
		case m.find(account) >= 0:
		default:
			// Its join line came first: the one player just listed without
			// an account is this one.
			if i := m.loneUnnamed(now); i >= 0 {
				m.state.Players[i].Account = account
				m.accounts[account] = m.state.Players[i].Name
			} else {
				m.waiting[account] = now
			}
		}

	case reGone.MatchString(body):
		m.leave(strings.ToUpper(reGone.FindStringSubmatch(body)[1]), add)

	case reWrongPassword.MatchString(body):
		out.Text = collapse(rePasswords.ReplaceAllString(strings.TrimRight(body, " \t"), "${1}(hidden)"))
		add(game.Event{Kind: "wrong_password", Text: "Someone tried to connect with the wrong password."})

	case reBadVersion.MatchString(body):
		g := reBadVersion.FindStringSubmatch(body)
		server, _, _ := strings.Cut(g[2], "-")
		client, _, _ := strings.Cut(g[3], "-")
		account := strings.ToUpper(g[1])
		add(game.Event{Kind: "bad_version", Account: account, Name: m.accounts[account],
			Text: "A friend's Windrose (" + client + ") isn't the same version as the server (" + server + "), so they couldn't join. Update the game or the server so both match."})

	case reSaved.MatchString(body):
		m.state.LastSave = out.Time
		if m.state.LastSave.IsZero() {
			m.state.LastSave = now
		}
		// Every minute or so: the feed doesn't show it.
		add(game.Event{Kind: "saved"})

	case reSaveFailed.MatchString(body):
		add(game.Event{Kind: "save_failed", Text: "The Windrose server couldn't save the world (" + reSaveFailed.FindStringSubmatch(body)[1] + ")."})

	case reStopping.MatchString(body):
		if !m.stopping {
			m.stopping = true
			m.state.Stopping = true
			m.state.StoppingAt = now
			add(game.Event{Kind: "stopping"})
		}
	}
	return out
}

// lineTime is a line's own time, which Unreal writes in UTC.
func lineTime(g []string) time.Time {
	t, err := time.Parse("2006.01.02-15.04.05.000", g[1]+"."+g[2]+"."+g[3]+"-"+g[4]+"."+g[5]+"."+g[6]+"."+g[7])
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func isNoisy(category string) bool {
	for _, n := range noisy {
		if category == n {
			return true
		}
	}
	return false
}

// collapse shortens the wide padding the server puts between the parts of
// a line, so the Log page shows it in one piece.
func collapse(s string) string {
	for strings.Contains(s, "   ") {
		s = strings.ReplaceAll(s, "   ", "  ")
	}
	return s
}

// extra sets one of the adapter's own state values.
func (m *monitor) extra(key, value string) {
	if m.state.Extra == nil {
		m.state.Extra = map[string]string{}
	}
	m.state.Extra[key] = value
}

// warn adds a warning once per run.
func (m *monitor) warn(kind string, add func(game.Event), text string) {
	if m.warned[kind] {
		return
	}
	m.warned[kind] = true
	add(game.Event{Kind: kind, Text: text})
}

// accountOf is the account a name plays from, when a line has said.
func (m *monitor) accountOf(name string) string {
	for account, n := range m.accounts {
		if n == name {
			return account
		}
	}
	return ""
}

// pairWithin is how close in time a join line and an account line must be
// to be taken for the same player when nothing else ties them.
const pairWithin = time.Minute

// lone is the one account that connected within pairWithin and has no
// name yet; "" when there is none or more than one.
func (m *monitor) lone(now time.Time) string {
	found := ""
	for account, at := range m.waiting {
		if now.Sub(at) > pairWithin {
			continue
		}
		if found != "" {
			return ""
		}
		found = account
	}
	return found
}

// loneUnnamed is the one listed player who joined within pairWithin and
// has no account yet; -1 when there is none or more than one.
func (m *monitor) loneUnnamed(now time.Time) int {
	found := -1
	for i, p := range m.state.Players {
		if p.Account != "" || now.Sub(p.Since) > pairWithin {
			continue
		}
		if found >= 0 {
			return -1
		}
		found = i
	}
	return found
}

// find is the listed player with this account.
func (m *monitor) find(account string) int {
	for i, p := range m.state.Players {
		if account != "" && p.Account == account {
			return i
		}
	}
	return -1
}

// findName is the listed player with this name.
func (m *monitor) findName(name string) int {
	for i, p := range m.state.Players {
		if p.Name == name {
			return i
		}
	}
	return -1
}

// join lists a player, once: the server says a join in several lines.
func (m *monitor) join(name, account string, now time.Time, add func(game.Event)) {
	if name == "" {
		return
	}
	if i := m.find(account); i >= 0 {
		m.state.Players[i].Name = name
		return
	}
	if i := m.findName(name); i >= 0 {
		if m.state.Players[i].Account == "" {
			m.state.Players[i].Account = account
		}
		return
	}
	since := now
	if t, ok := m.waiting[account]; ok {
		since = t
	}
	delete(m.waiting, account)
	m.state.Players = append(m.state.Players, game.Player{Name: name, Account: account, Since: since})
	m.state.Count = len(m.state.Players)
	add(game.Event{Kind: "join", Name: name, Account: account, Text: name + " joined the game."})
}

// leave takes a player off the list, once: the server says a leave in
// several lines.
func (m *monitor) leave(account string, add func(game.Event)) {
	delete(m.waiting, account)
	i := m.find(account)
	if i < 0 {
		if name := m.accounts[account]; name != "" {
			i = m.findName(name)
		}
	}
	if i < 0 {
		return
	}
	m.drop(i, add)
}

func (m *monitor) drop(i int, add func(game.Event)) {
	p := m.state.Players[i]
	m.state.Players = append(m.state.Players[:i], m.state.Players[i+1:]...)
	m.state.Count = len(m.state.Players)
	add(game.Event{Kind: "leave", Name: p.Name, Account: p.Account, Text: p.Name + " left the game."})
}

// endDump finishes the account list being read, if one is. A whole list
// (all three headers) says who is on: everyone under Connected or
// Reserved Accounts (Reserved are still loading in). A list cut short says
// nothing.
func (m *monitor) endDump(now time.Time, add func(game.Event)) {
	if !m.dump {
		return
	}
	rows, whole := m.rows, len(m.sections) == 3
	m.dump, m.section, m.sections, m.rows = false, "", nil, nil
	for _, r := range rows {
		if r.account != "" && r.name != "" {
			m.accounts[r.account] = r.name
		}
	}
	if !whole {
		return
	}
	on := map[string]bool{}
	for _, r := range rows {
		if r.section == sectionConnected || r.section == sectionReserved {
			on[key(r.account, r.name)] = true
		}
	}
	for i := len(m.state.Players) - 1; i >= 0; i-- {
		p := m.state.Players[i]
		if !on[key(p.Account, p.Name)] {
			m.drop(i, add)
		}
	}
	for _, r := range rows {
		if r.section == sectionConnected || r.section == sectionReserved {
			m.join(r.name, r.account, now, add)
		}
	}
}

// key is how an account list names a player: by account, else by name.
func key(account, name string) string {
	if account != "" {
		return "a:" + account
	}
	return "n:" + name
}

// online says the server is ready for players, once.
func (m *monitor) online(now time.Time) []game.Event {
	if m.state.Ready || m.stopping {
		return nil
	}
	m.state.Ready = true
	return []game.Event{{Kind: "online", Time: now, Text: "The server is online and ready for players."}}
}

// Tick says the server is online registerWait after it registered with
// Windrose's online service, when no line said it was ready: the invite
// code works from then on.
func (m *monitor) Tick(now time.Time) []game.Event {
	if m.registeredAt.IsZero() || m.state.Ready || now.Sub(m.registeredAt) < registerWait {
		return nil
	}
	return m.online(now)
}

// Probed is never called: Windrose has no query the app can ask.
func (m *monitor) Probed(p game.ProbeResult, now time.Time) []game.Event { return nil }

func (m *monitor) State() game.RunState {
	st := m.state
	st.Players = append([]game.Player(nil), m.state.Players...)
	if m.state.Extra != nil {
		st.Extra = make(map[string]string, len(m.state.Extra))
		for k, v := range m.state.Extra {
			st.Extra[k] = v
		}
	}
	return st
}

// End: the run is over, so whoever is listed has left.
func (m *monitor) End(now time.Time) []game.Event {
	var evs []game.Event
	m.endDump(now, func(e game.Event) { evs = append(evs, e) })
	for _, p := range m.state.Players {
		evs = append(evs, game.Event{Kind: "leave", Time: now, Name: p.Name, Account: p.Account})
	}
	m.state.Players = nil
	m.state.Count = 0
	m.state.Ready = false
	return evs
}
