package manager

// Starting a game from Discord. A small program, the waker, runs on the
// JetKVM (the IP KVM next to this computer, which is always on). Each
// starred game has its own slash command (/valheim, /minecraft…) that works
// in that game's channel; when someone types it, the waker wakes this
// computer with Wake-on-LAN, and the app starts that game when it hears the
// same signal (wakesignal.go). The app puts the waker on the JetKVM over
// SSH, through the JetKVM's Developer Mode, and keeps it up to date,
// commands included.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/discord"
	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/jetkvm"
	"valheim-easy-server/internal/servers"
	"valheim-easy-server/internal/waker"
)

// wakerFile is what the app knows about the waker it put on the JetKVM,
// kept in jetkvm.json. The bot token is in the settings.
type wakerFile struct {
	HostKey []byte     `json:"hostKey,omitempty"` // the JetKVM's SSH host key
	AppID   string     `json:"appId,omitempty"`   // the bot's Discord application
	Bot     string     `json:"bot,omitempty"`     // its name in Discord
	Setup   wakerSetup `json:"setup"`             // what the waker was installed with
	At      time.Time  `json:"at,omitzero"`
}

// wakerSetup is what a waker is installed with, apart from the token. It
// stays comparable with ==, which is how the upkeep tells whether the
// waker needs installing again.
type wakerSetup struct {
	Version string `json:"version,omitempty"` // the app's, which the waker matches
	MAC     string `json:"mac,omitempty"`     // the network adapter it wakes
	Query   string `json:"query,omitempty"`   // where it asks whether Valheim is up (the /valheim of R2 and before)
	Name    string `json:"name,omitempty"`    // the server's (likewise)
	Guild   string `json:"guild,omitempty"`   // the Discord server it answers in
	// Games is a fingerprint of the commands it was given, one per starred
	// game (commandsPrint), so a change to any of them shows here; "" for a
	// waker set up with /valheim alone, as R2 and older apps set it up.
	// Older apps ignore it in jetkvm.json (spec 1.3).
	Games string `json:"games,omitempty"`
}

// wakerState is the waker's side of the Manager, under its mu.
type wakerState struct {
	file    wakerFile
	status  *waker.Status // last read from the JetKVM
	at      time.Time     // when
	problem string        // why it couldn't be read or updated then
	action  string        // what the problem needs, as for WakerView.Action
	busy    bool          // being installed, updated or removed

	// R3 (spec 6.4), guarded by m.mu like the rest unless it says otherwise.

	// problems says, by instance, why a starred game got no command when
	// the waker was last set up (its channel is in another Discord server,
	// or its webhook doesn't work), for the Discord page (WakerView).
	problems map[string]string
	// picked is what the sample loop last saw of the commands the waker
	// should have (noticeCommandsLocked), so a change to any of them, not
	// only to the stars, updates the waker. A successful install sets it to
	// planned, what its plan (wakerPlan) was made from, so a change made
	// while it was being installed is still noticed.
	picked, planned string
	// asked holds the instances whose start a Discord request asked for,
	// until that run ends: a failed start is then answered in the game's
	// channel, and the game's online post goes out there whatever its own
	// Discord settings say (askedFromDiscordLocked).
	asked map[string]bool
	// kept holds, by instance, the request (tag) that came while the game
	// was busy or stopping: the waker's repeats may still start it, and
	// once they are over the app tries one last time and says why not.
	kept map[string]string

	kickMu sync.Mutex    // guards kick, so starsChanged works with or without m.mu
	kick   chan struct{} // the commands changed: wakerLoop updates the waker a little later
}

// WakerView is the Discord start setup, for the Settings page.
type WakerView struct {
	On        bool          `json:"on"`      // the waker is installed; wake signals start the server
	Address   string        `json:"address"` // the JetKVM's
	Token     bool          `json:"token"`   // a bot token is saved
	Key       string        `json:"key"`     // the app's public key, for the JetKVM's Developer Mode
	Bot       string        `json:"bot,omitempty"`
	Invite    string        `json:"invite,omitempty"` // adds the bot to the Discord server
	Guild     bool          `json:"guild"`            // it only answers in the join-code channel's Discord server
	Status    *waker.Status `json:"status,omitempty"` // last read from the JetKVM
	CheckedAt time.Time     `json:"checkedAt,omitzero"`
	Problem   string        `json:"problem,omitempty"` // why the waker couldn't be reached or updated
	Action    string        `json:"action,omitempty"`  // what the problem needs: "trust" (the JetKVM's identity changed) or "key"
	Busy      bool          `json:"busy"`
	Missing   string        `json:"missing,omitempty"` // why this copy of the app can't install it
	CanPost   bool          `json:"canPost"`           // the join-code channel is set up, so links can be posted there
	// Commands are the starred games' slash commands (R3), each with why it
	// can't work when it can't, for the Discord page; none when the waker
	// is set up as R2 sets it up (/valheim alone).
	Commands []WakerCommand `json:"commands,omitempty"`
}

// WakerCommand is one starred game's slash command.
type WakerCommand struct {
	Game    string `json:"game"`              // the instance
	Command string `json:"command"`           // "minecraft", typed as /minecraft
	Problem string `json:"problem,omitempty"` // why it won't work: "Add a Discord channel so /minecraft can work."
}

func (m *Manager) loadWaker() {
	b, err := os.ReadFile(m.dataFile("jetkvm.json"))
	if err != nil {
		return
	}
	var f wakerFile
	if err := json.Unmarshal(b, &f); err != nil {
		m.log.Printf("jetkvm.json: %v", err)
		return
	}
	m.wk.file = f
}

func (m *Manager) saveWaker() {
	m.mu.Lock()
	b, err := json.MarshalIndent(m.wk.file, "", "  ")
	m.mu.Unlock()
	if err == nil {
		err = writeFileAtomic(m.dataFile("jetkvm.json"), b, 0o600)
	}
	if err != nil {
		m.log.Printf("saving jetkvm.json: %v", err)
	}
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// jetkvmKey is the app's own SSH key for the JetKVM, made the first time.
func (m *Manager) jetkvmKey() (ssh.Signer, error) {
	m.keyMu.Lock()
	defer m.keyMu.Unlock()
	path := m.dataFile("jetkvm-key.pem")
	pemKey, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if pemKey, err = jetkvm.NewKey(); err == nil {
			err = writeFileAtomic(path, pemKey, 0o600)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("couldn’t make the app’s key for the JetKVM: %w", err)
	}
	key, err := ssh.ParsePrivateKey(pemKey)
	if err != nil {
		return nil, fmt.Errorf("the app’s key for the JetKVM (%s) is damaged: delete it, and paste the new key into the JetKVM: %w", path, err)
	}
	return key, nil
}

func (m *Manager) jetkvmTarget(addr string, key ssh.Signer, hostKey []byte) jetkvm.Target {
	t := jetkvm.Target{Addr: addr, Key: key, HostKey: hostKey}
	if d := m.opt.JetKVMDirs; d != nil {
		t.Base, t.Run, t.Arch = d.Base, d.Run, d.Arch
	}
	return t
}

// wakerMissing says why this copy of the app can't install the waker.
func (m *Manager) wakerMissing() string {
	if m.opt.WakerBinary != nil || jetkvm.HasWaker() {
		return ""
	}
	return sentence(jetkvm.ErrNoWaker.Error())
}

func (m *Manager) wakerBinary() ([]byte, error) {
	if m.opt.WakerBinary != nil {
		return m.opt.WakerBinary()
	}
	return jetkvm.Waker()
}

var reHost = regexp.MustCompile(`^[\w.\-]+$|^\[?[0-9A-Fa-f:.]+\]?$`)

// jetkvmAddr reads the JetKVM's address as typed or pasted: an IP address
// or a name, perhaps with a port, or the link to its web page.
func jetkvmAddr(s string) string {
	s = strings.TrimSpace(s)
	if u, err := url.Parse(s); err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") {
		return u.Hostname() // its web page; SSH is on port 22
	}
	return strings.TrimRight(s, "/")
}

func validJetKVMAddr(addr string) bool {
	host := addr
	if h, port, err := net.SplitHostPort(addr); err == nil {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return false
		}
		host = h
	}
	return reHost.MatchString(host)
}

// wakerPlan works out what the waker should be set up with.
func (m *Manager) wakerPlan(ctx context.Context, token string) (waker.Config, wakerSetup, error) {
	m.mu.Lock()
	s := m.settings
	r2 := m.wakerR2Locked()
	var picks []wakerPick
	if !r2 {
		picks = m.wakerPicksLocked()
	}
	m.wk.planned = m.picksKeyLocked(picks)
	m.mu.Unlock()
	var setup wakerSetup
	for _, a := range m.opt.NetAdapters() {
		if a.Wired && a.Connected {
			setup.MAC = a.MAC
			if len(a.IPv4) > 0 {
				setup.Query = net.JoinHostPort(a.IPv4[0], strconv.Itoa(s.Port+1))
			}
			break
		}
	}
	if setup.MAC == "" {
		return waker.Config{}, setup, errors.New("This computer isn’t connected to the router with a network cable, and Wake-on-LAN only works over a cable.")
	}
	setup.Version = m.opt.AppVersion
	// The bot answers only in the main channel's Discord server, so nobody
	// can start a game from another. That holds whenever the main channel
	// is set, whatever Valheim's own posts are set to (spec 6.3): before R3
	// it was set only while Valheim posted there, and an empty guild means
	// any Discord server.
	main := strings.TrimSpace(s.DiscordWebhook)
	hooks := map[string]hookPlace{} // what each webhook link said, asked once each
	if discord.Problem(main) == "" {
		g, ch, err := discord.Webhook(ctx, nil, m.webhookURL(main))
		var de *discord.Error
		switch {
		case err == nil:
			setup.Guild = g
		case r2 || ctx.Err() != nil || !errors.As(err, &de) || de.Temporary:
			// /valheim of R2 works in any channel, so the Discord server is
			// all that keeps it to the owner's; and a Discord that can't be
			// reached just now may answer next time.
			where := "the main Discord channel"
			if r2 {
				where = "the join-code channel"
			}
			return waker.Config{}, setup, fmt.Errorf("Couldn’t find out which Discord server %s is in. %v", where, err)
		}
		// A link Discord turned down for good (the webhook was deleted)
		// leaves no Discord server to keep to, but every game's command
		// still works only in its own channel, and the games that post in
		// the main channel get no command until it's fixed.
		hooks[main] = hookPlace{guild: g, channel: ch, err: err}
	}
	if r2 {
		// No list of games here (or Valheim-only safe mode): /valheim alone,
		// set up from Query and Name, as R2 and older apps do.
		setup.Name = s.ServerName
		cfg := waker.Config{Token: token, MAC: setup.MAC, Query: setup.Query, Name: setup.Name, Guild: setup.Guild}
		return cfg, setup, nil
	}

	// One command per starred game that has a channel, working only there.
	cmds := []waker.GameCommand{} // written as [] when there are none
	problems := map[string]string{}
	for _, p := range picks {
		if p.link == "" {
			continue // no channel at all; WakerView says so
		}
		place, ok := hooks[p.link]
		if !ok {
			g, ch, err := discord.Webhook(ctx, nil, m.webhookURL(p.link))
			var de *discord.Error
			if err != nil && (ctx.Err() != nil || !errors.As(err, &de) || de.Temporary) {
				// Discord can't be reached just now: try again later rather
				// than take the game's command away for a moment.
				return waker.Config{}, setup, fmt.Errorf("Couldn’t find out which channel %s posts in. %v", p.inst.short(), err)
			}
			place = hookPlace{guild: g, channel: ch, err: err}
			hooks[p.link] = place
		}
		switch {
		case place.err != nil:
			problems[p.inst.id] = fmt.Sprintf("/%s can't work until its Discord channel works again. %s", p.cmd.Command, sentence(place.err.Error()))
			continue
		case setup.Guild != "" && place.guild != setup.Guild:
			problems[p.inst.id] = fmt.Sprintf("This channel is in another Discord server, so /%s won't work there.", p.cmd.Command)
			continue
		}
		c := p.cmd
		c.Channel = place.channel
		if p.inst.legacy {
			c.Query = setup.Query // Valheim answers Steam's queries: the bot sees when it's up
		}
		cmds = append(cmds, c)
	}
	m.mu.Lock()
	m.wk.problems = problems
	m.mu.Unlock()
	setup.Query = "" // in the commands now
	setup.Games = commandsPrint(cmds)
	cfg := waker.Config{Token: token, MAC: setup.MAC, Guild: setup.Guild, Games: &cmds}
	return cfg, setup, nil
}

// hookPlace is where a webhook posts, as Discord said, or why it couldn't.
type hookPlace struct {
	guild, channel string
	err            error
}

// commandsPrint is a fingerprint of the commands: the same commands give
// the same one, so wakerSetup stays a comparable struct of strings.
func commandsPrint(cmds []waker.GameCommand) string {
	b, _ := json.Marshal(cmds)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// beginWaker marks the waker busy, or says why it can't be.
func (m *Manager) beginWaker() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.wk.busy {
		return errors.New("The app is busy with the JetKVM. Try again in a minute.")
	}
	m.wk.busy = true
	return nil
}

func (m *Manager) endWaker() {
	m.mu.Lock()
	m.wk.busy = false
	m.mu.Unlock()
	go m.notify()
}

// InstallWaker puts the waker on the JetKVM at addr with the bot token
// (the saved ones when empty), and turns on starting the server from
// Discord. trust accepts a JetKVM whose identity changed.
func (m *Manager) InstallWaker(addr, token string, trust bool) error {
	addr, token = jetkvmAddr(addr), strings.TrimSpace(token)
	m.mu.Lock()
	if addr == "" {
		addr = m.settings.JetKVM
	}
	if token == "" {
		token = m.settings.DiscordBotToken
	}
	m.mu.Unlock()
	fields := config.FieldErrors{}
	if token == "" {
		fields["botToken"] = "Paste the bot’s token from the Discord Developer Portal."
	}
	if addr == "" {
		fields["jetkvm"] = "Type the JetKVM’s address. It shows on the JetKVM’s little screen, like 192.168.1.50."
	} else if !validJetKVMAddr(addr) {
		fields["jetkvm"] = "That isn’t an address. Type it the way the JetKVM’s little screen shows it, like 192.168.1.50."
	}
	if len(fields) > 0 {
		return fields
	}
	if msg := m.wakerMissing(); msg != "" {
		return errors.New(msg)
	}
	if err := m.beginWaker(); err != nil {
		return err
	}
	defer m.endWaker()
	ctx, cancel := context.WithTimeout(m.ctx, 3*time.Minute)
	defer cancel()

	// The token is checked here first: Discord's answer is easier to
	// explain from here than from the JetKVM.
	bot := waker.New(waker.Config{Token: token, API: m.opt.DiscordAPI}, m.log)
	name, err := bot.Check(ctx)
	var appID string
	if err == nil {
		appID, err = bot.Application(ctx)
	}
	if errors.Is(err, waker.ErrToken) {
		return config.FieldErrors{"botToken": sentence(err.Error())}
	} else if err != nil {
		return fmt.Errorf("Couldn’t reach Discord to check the bot token: %v", err)
	}
	cfg, setup, err := m.wakerPlan(ctx, token)
	if err != nil {
		return err
	}
	bin, err := m.wakerBinary()
	if err != nil {
		return errors.New(sentence(err.Error()))
	}
	key, err := m.jetkvmKey()
	if err != nil {
		return errors.New(sentence(err.Error()))
	}
	m.mu.Lock()
	hostKey := m.wk.file.HostKey
	m.mu.Unlock()
	if trust {
		hostKey = nil
	}
	st, hostKey, err := jetkvm.Install(ctx, m.jetkvmTarget(addr, key, hostKey), bin, cfg)
	if err != nil {
		return jetkvmError(err)
	}

	now := time.Now()
	m.mu.Lock()
	next := m.settings.Clone()
	next.DiscordBotToken, next.JetKVM, next.DiscordWake = token, addr, true
	err = m.saveSettingsLocked(next)
	m.wk.file = wakerFile{HostKey: hostKey, AppID: appID, Bot: name, Setup: setup, At: now}
	m.wk.status, m.wk.at, m.wk.problem, m.wk.action = &st, now, "", ""
	m.wk.picked = m.wk.planned
	m.mu.Unlock()
	m.saveWaker()
	if err != nil {
		return err
	}
	m.syncWakeListener()
	m.recheckSoon()
	text := "The waker is on the JetKVM: friends can type /valheim in Discord to wake this computer and start the server."
	if cfg.Games != nil {
		text = commandsText(*cfg.Games)
	}
	m.addActivity("info", "", text)
	return nil
}

// commandsText says what friends can type now, for the activity feed.
func commandsText(cmds []waker.GameCommand) string {
	if len(cmds) == 0 {
		return "The waker is on the JetKVM. Star a game to give it a command in Discord."
	}
	names := make([]string, len(cmds))
	for i, c := range cmds {
		names[i] = "/" + c.Command
	}
	list := names[0]
	if n := len(names); n > 1 {
		list = strings.Join(names[:n-1], ", ") + " or " + names[n-1]
	}
	return "The waker is on the JetKVM: friends can type " + list + " in Discord, each in its game's channel, to wake this computer and start that game."
}

// jetkvmError explains a problem with the JetKVM, with what to do about it.
func jetkvmError(err error) error {
	switch {
	case errors.Is(err, jetkvm.ErrHostKey):
		return &ActionError{Msg: sentence(err.Error()), Action: "trust"}
	case errors.Is(err, jetkvm.ErrNotLetIn), errors.Is(err, jetkvm.ErrNoSSH):
		return &ActionError{Msg: sentence(err.Error()), Action: "key"}
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("The JetKVM took too long to answer. Try again in a minute.")
	}
	return errors.New(sentence(err.Error()))
}

// problemOf is how a problem shows in WakerView.
func problemOf(err error) (problem, action string) {
	var ae *ActionError
	if errors.As(err, &ae) {
		return ae.Msg, ae.Action
	}
	return err.Error(), ""
}

// sentence makes s start with a capital and end with a full stop.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	r, n := utf8.DecodeRuneInString(s)
	s = string(unicode.ToUpper(r)) + s[n:]
	if !strings.ContainsAny(s[len(s)-1:], ".!?") {
		s += "."
	}
	return s
}

// WakerView is how starting the server from Discord is set up. live reads
// the waker's status from the JetKVM first, unless the app is busy with it.
func (m *Manager) WakerView(live bool) WakerView {
	if live {
		m.mu.Lock()
		busy := m.wk.busy
		m.mu.Unlock()
		if !busy {
			ctx, cancel := context.WithTimeout(m.ctx, 20*time.Second)
			m.readWaker(ctx)
			cancel()
		}
	}
	var pub string
	if key, err := m.jetkvmKey(); err == nil {
		pub = jetkvm.PublicKey(key)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, f := m.settings, m.wk.file
	v := WakerView{
		On: s.DiscordWake, Address: s.JetKVM, Token: s.DiscordBotToken != "", Key: pub,
		Bot: f.Bot, Guild: f.Setup.Guild != "", Busy: m.wk.busy, Missing: m.wakerMissing(),
		CanPost: m.canPostLocked(),
	}
	if s.DiscordWake {
		v.Status, v.CheckedAt, v.Problem, v.Action = m.wk.status, m.wk.at, m.wk.problem, m.wk.action
	}
	if f.AppID != "" {
		v.Invite = waker.Invite(f.AppID, f.Setup.Guild)
	}
	if !m.wakerR2Locked() {
		v.Commands = []WakerCommand{}
		for _, p := range m.wakerPicksLocked() {
			c := WakerCommand{Game: p.inst.id, Command: p.cmd.Command, Problem: m.wk.problems[p.inst.id]}
			if p.link == "" {
				c.Problem = fmt.Sprintf("Add a Discord channel so /%s can work.", p.cmd.Command)
			}
			v.Commands = append(v.Commands, c)
		}
	}
	return v
}

// canPostLocked reports whether links can be posted in the main channel:
// before R3 only while Valheim posted there (its "Post the join code in
// Discord"); from R3 whenever the main channel is set, as it's no longer
// Valheim's alone.
func (m *Manager) canPostLocked() bool {
	s := m.settings
	return (s.Discord || !m.wakerR2Locked()) && discord.Problem(s.DiscordWebhook) == ""
}

// readWaker reads the waker's status from the JetKVM.
func (m *Manager) readWaker(ctx context.Context) (waker.Status, error) {
	m.mu.Lock()
	on, addr, hostKey := m.settings.DiscordWake, m.settings.JetKVM, m.wk.file.HostKey
	m.mu.Unlock()
	if !on {
		return waker.Status{}, errors.New("not installed")
	}
	key, err := m.jetkvmKey()
	var st waker.Status
	if err == nil {
		st, err = jetkvm.Status(ctx, m.jetkvmTarget(addr, key, hostKey))
	}
	m.mu.Lock()
	m.wk.at = time.Now()
	if err != nil {
		m.wk.status = nil
		m.wk.problem, m.wk.action = problemOf(jetkvmError(err))
	} else {
		m.wk.status, m.wk.problem, m.wk.action = &st, "", ""
	}
	m.mu.Unlock()
	return st, err
}

// How long after the games' commands change (a star, say) the waker is
// updated, so several changes in a row make one update; and how long it
// waits to try again when the app is already busy with the JetKVM. Tests
// shorten them.
var (
	commandsDelay = 30 * time.Second
	commandsRetry = 30 * time.Second
)

// wakerLoop keeps the waker on the JetKVM up to date (after the app updates
// itself, or when this computer's network address, the server's name or the
// games' commands change) and running.
func (m *Manager) wakerLoop() {
	t := time.NewTimer(90 * time.Second)
	defer t.Stop()
	kick := m.wakerKick()
	var due *time.Timer // the update after the commands changed
	var dueC <-chan time.Time
	defer func() {
		if due != nil {
			due.Stop()
		}
	}()
	later := func(d time.Duration) {
		if due != nil {
			due.Stop()
		}
		due = time.NewTimer(d)
		dueC = due.C
	}
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-t.C:
			m.upkeepWaker()
			t.Reset(30 * time.Minute)
		case <-kick:
			later(commandsDelay) // counted from the last change
		case <-dueC:
			dueC = nil
			if !m.upkeepWakerOnce() {
				// Being installed, updated or removed from the Settings
				// page: once that's done this one may still be needed.
				m.log.Printf("the app is busy with the JetKVM; the waker's commands are updated in %s", commandsRetry)
				later(commandsRetry)
			}
		}
	}
}

// wakerKick is the channel that tells wakerLoop the commands changed. It
// holds one message, which is all wakerLoop needs: the update reads the
// commands as they are then.
func (m *Manager) wakerKick() chan struct{} {
	m.wk.kickMu.Lock()
	defer m.wk.kickMu.Unlock()
	if m.wk.kick == nil {
		m.wk.kick = make(chan struct{}, 1)
	}
	return m.wk.kick
}

func (m *Manager) upkeepWaker() { m.upkeepWakerOnce() }

// upkeepWakerOnce checks the waker on the JetKVM and installs it again
// when it stopped or is out of date. It reports false when it couldn't
// look because the app was already busy with the JetKVM.
func (m *Manager) upkeepWakerOnce() bool {
	m.mu.Lock()
	on, token, addr, file := m.settings.DiscordWake, m.settings.DiscordBotToken, m.settings.JetKVM, m.wk.file
	m.mu.Unlock()
	if !on {
		return true
	}
	if m.beginWaker() != nil {
		return false
	}
	defer m.endWaker()
	ctx, cancel := context.WithTimeout(m.ctx, 3*time.Minute)
	defer cancel()
	st, err := m.readWaker(ctx)
	var stopped *jetkvm.ErrStopped
	if err != nil && !errors.As(err, &stopped) {
		return true // it can't be reached: that shows on the Settings page
	}
	if m.wakerMissing() != "" {
		return true // a development build: it can't update it
	}
	cfg, setup, err := m.wakerPlan(ctx, token)
	if err != nil {
		m.log.Printf("checking the waker's settings: %v", err)
		return true
	}
	if stopped == nil && setup == file.Setup && st.Version == setup.Version {
		return true
	}
	why := "to match this app"
	switch {
	case stopped != nil:
		why = "because it had stopped"
	case setup.Games != file.Setup.Games && setup.Version == file.Setup.Version:
		why = "for the games' commands"
	}
	m.log.Printf("updating the waker on the JetKVM %s", why)
	bin, err := m.wakerBinary()
	key, err2 := m.jetkvmKey()
	if err != nil || err2 != nil {
		m.log.Printf("updating the waker: %v %v", err, err2)
		return true
	}
	st, hostKey, err := jetkvm.Install(ctx, m.jetkvmTarget(addr, key, file.HostKey), bin, cfg)
	now := time.Now()
	m.mu.Lock()
	m.wk.at = now
	if err != nil {
		m.wk.status = nil
		m.wk.problem, m.wk.action = problemOf(jetkvmError(err))
		m.wk.problem = "Couldn’t update the waker on the JetKVM: " + lowerFirst(m.wk.problem)
	} else {
		m.wk.status, m.wk.problem, m.wk.action = &st, "", ""
		m.wk.file.HostKey, m.wk.file.Setup, m.wk.file.At = hostKey, setup, now
		m.wk.picked = m.wk.planned
	}
	m.mu.Unlock()
	if err != nil {
		m.log.Printf("updating the waker: %v", err)
		return true
	}
	m.saveWaker()
	return true
}

// RemoveWaker takes the waker off the JetKVM and turns off starting the
// server from Discord. force turns it off here even if the JetKVM can't be
// reached.
func (m *Manager) RemoveWaker(force bool) error {
	if err := m.beginWaker(); err != nil {
		return err
	}
	defer m.endWaker()
	m.mu.Lock()
	addr, hostKey := m.settings.JetKVM, m.wk.file.HostKey
	m.mu.Unlock()
	if !force {
		ctx, cancel := context.WithTimeout(m.ctx, time.Minute)
		defer cancel()
		key, err := m.jetkvmKey()
		if err == nil {
			err = jetkvm.Remove(ctx, m.jetkvmTarget(addr, key, hostKey))
		}
		if err != nil {
			msg, _ := problemOf(jetkvmError(err))
			return &ActionError{Msg: "Couldn’t take the waker off the JetKVM. " + msg, Action: "force"}
		}
	}
	m.mu.Lock()
	next := m.settings.Clone()
	next.DiscordWake = false // the token and address stay, for next time
	err := m.saveSettingsLocked(next)
	m.wk.file.Setup, m.wk.file.At = wakerSetup{}, time.Time{}
	m.wk.status, m.wk.problem, m.wk.action = nil, "", ""
	m.mu.Unlock()
	m.saveWaker()
	if err != nil {
		return err
	}
	m.syncWakeListener()
	m.recheckSoon()
	m.addActivity("info", "", "Starting the server from Discord is off.")
	return nil
}

// PostWakerLink posts the link that adds the bot to the Discord server
// ("invite"), or the app's key for the JetKVM ("key"), in the join-code
// channel, to open or copy on another computer.
func (m *Manager) PostWakerLink(what string) error {
	pub := ""
	if what == "key" {
		key, err := m.jetkvmKey()
		if err != nil {
			return errors.New(sentence(err.Error()))
		}
		pub = jetkvm.PublicKey(key)
	}
	m.mu.Lock()
	s, f := m.settings, m.wk.file
	canPost, r2 := m.canPostLocked(), m.wakerR2Locked()
	cmds := m.wakerCommandsLocked()
	m.mu.Unlock()
	if !canPost {
		if r2 {
			return errors.New("Turn on “Post the join code in Discord” first: the link is posted in that channel.")
		}
		return errors.New("Set up the main Discord channel first: the link is posted there.")
	}
	var text string
	switch what {
	case "invite":
		if f.AppID == "" {
			return errors.New("Install the waker on the JetKVM first.")
		}
		text = "🔗 Add the bot to this Discord server, so everyone here can type **/valheim** to start the Valheim server (it needs someone who can manage the Discord server):\n" + waker.Invite(f.AppID, f.Setup.Guild)
		if !r2 {
			like := ""
			if len(cmds) > 0 {
				like = " (like **/" + cmds[0].Command + "**)"
			}
			text = "🔗 Add the bot to this Discord server, so everyone here can type a game's command" + like +
				" in its channel to start it (it needs someone who can manage the Discord server):\n" + waker.Invite(f.AppID, f.Setup.Guild)
		}
	case "key":
		text = "🔑 Valheim Easy Server’s key for the JetKVM. Copy it into the JetKVM’s settings, under Advanced (Developer Mode), in SSH Public Key:\n```\n" + pub + "\n```"
	default:
		return errors.New("Nothing to post.")
	}
	ctx, cancel := context.WithTimeout(m.ctx, 45*time.Second)
	defer cancel()
	return discord.Post(ctx, nil, m.webhookURL(s.DiscordWebhook), text)
}

// --- R3 (stream WK) -----------------------------------------------------------
//
// Spec 6.4: one slash command per starred game, each working only in its
// game's channel, and the app's replies to a Discord request posted in that
// channel. WK keeps the rest of its state in wakerState (Manager.wk).

// wakerR2Locked reports whether the waker is set up as R2 and older apps
// set it up: /valheim alone, from Query and Name, with no Games. That is
// when this copy has no list of games in memory (servers.json isn't read:
// a test, or a build before servers.json was made) or runs in Valheim-only
// safe mode (spec 8.3), where only Valheim can start anyway.
func (m *Manager) wakerR2Locked() bool {
	return m.safeMode != "" || m.servers.Schema == 0
}

// wakerPick is a starred game that should have a command.
type wakerPick struct {
	inst *instance
	cmd  waker.GameCommand // without Channel and Query, which wakerPlan fills in
	link string            // the webhook its posts go to; "" when it has no channel at all
}

// wakerPicksLocked are the games that should have a command: the starred
// instances that this copy runs, that are turned on and not hidden (I1), in
// the instances' order, each with its channel's webhook link.
func (m *Manager) wakerPicksLocked() []wakerPick {
	var picks []wakerPick
	for _, id := range m.order {
		inst := m.instances[id]
		if inst == nil || inst.srv == nil || inst.foreign != "" || !m.isStarredLocked(inst) ||
			m.isDisabledLocked(inst) || m.isHiddenLocked(inst) {
			continue
		}
		command := inst.info.Command
		if command == "" {
			command = inst.info.ID // a game's command is its id unless it says
		}
		name := strings.TrimSpace(inst.saved().Server["serverName"])
		if name == "" {
			name = inst.short()
		}
		picks = append(picks, wakerPick{inst: inst, link: m.wakerLinkLocked(inst), cmd: waker.GameCommand{
			ID: inst.id, Command: command, Name: name,
			Description: "Wake the server computer and start " + inst.short(),
		}})
	}
	return picks
}

// wakerLinkLocked is the webhook link of the game's channel: its own, else
// the main one (spec 6.3), "" when it has neither. That rule is stream D's
// gameLinkLocked; until D's is filled in, the same rule is applied here,
// so the commands work whichever stream lands first.
func (m *Manager) wakerLinkLocked(inst *instance) string {
	if link := m.gameLinkLocked(inst); link != "" {
		return link
	}
	if in := m.servers.Instance(inst.id); in != nil {
		if own := strings.TrimSpace(in.DiscordRules().Webhook); own != "" && discord.Problem(own) == "" {
			return own
		}
	}
	if main := strings.TrimSpace(m.settings.DiscordWebhook); discord.Problem(main) == "" {
		return main
	}
	return ""
}

// wakerCommandsLocked are the commands the waker should register: the
// starred, non-foreign, turned-on, non-hidden instances that have a channel
// (their own webhook's, else the main one's). The channel ids come from
// discord.Webhook, which needs the network, so wakerPlan finds them; this
// only picks the games (Channel and Query are left empty). A game whose
// channel turns out to be in another Discord server is left out there.
func (m *Manager) wakerCommandsLocked() []waker.GameCommand {
	cmds := []waker.GameCommand{}
	for _, p := range m.wakerPicksLocked() {
		if p.link != "" {
			cmds = append(cmds, p.cmd)
		}
	}
	return cmds
}

// starsChanged runs the waker's upkeep 30 s after the stars last changed
// (debounced, in wakerLoop), so the bot's commands follow them; if the
// waker is busy (wk.busy) it tries again 30 s later. M3's SetStar calls
// it; it may be called with or without m.mu. The sample loop also calls it
// when anything else the commands depend on changes (noticeCommandsLocked).
func (m *Manager) starsChanged() {
	select {
	case m.wakerKick() <- struct{}{}:
	default: // one is already waiting, which is enough
	}
}

// noticeCommandsLocked updates the waker when the commands it should have
// change for any reason: a star, a game turned off or hidden, a game's
// channel or server name. The sample loop asks every tick (onTick); it is
// cheap, as it reads only what's in memory.
func (m *Manager) noticeCommandsLocked() {
	if !m.settings.DiscordWake {
		m.wk.picked = ""
		return
	}
	var picks []wakerPick
	if !m.wakerR2Locked() {
		picks = m.wakerPicksLocked()
	}
	key := m.picksKeyLocked(picks)
	switch m.wk.picked {
	case key:
	case "":
		m.wk.picked = key // the first look: the regular upkeep compares it with the JetKVM
	default:
		m.wk.picked = key
		m.starsChanged()
	}
}

// picksKeyLocked sums up the picked commands, to notice a change: "r2"
// while the waker is set up with /valheim alone.
func (m *Manager) picksKeyLocked(picks []wakerPick) string {
	if m.wakerR2Locked() {
		return "r2"
	}
	var b strings.Builder
	b.WriteString("r3\n")
	for _, p := range picks {
		fmt.Fprintf(&b, "%s\x00%s\x00%s\x00%s\x00%s\n", p.cmd.ID, p.cmd.Command, p.cmd.Name, p.cmd.Description, p.link)
	}
	return b.String()
}

// askedFromDiscordLocked reports whether a Discord request started the
// instance's current run (or start). Its posts then go to its channel
// whatever its own Discord settings say, because someone asked there
// (spec 6.4): stream D's online post asks this.
func (m *Manager) askedFromDiscordLocked(inst *instance) bool {
	return m.wk.asked[inst.id]
}

// wakeReply posts, in the game's channel, why a game a Discord request
// asked for didn't start (err from startFromWake: memory short, busy,
// turned off, not installed, failed), whatever the game's own Discord
// on/off says, because someone asked there: "🧠 There isn't enough memory
// to start **Palworld** right now. Running: …" or "⚠️ **Palworld** didn't
// start: <reason>." (6.4). The activity feed says so too, for the owner.
func (m *Manager) wakeReply(inst *instance, err error) {
	m.mu.Lock()
	link := m.wakerLinkLocked(inst)
	running := m.runningGamesLocked()
	m.mu.Unlock()
	text := wakeReplyText(inst.short(), err, running)
	m.addGameActivity(activityGame(inst), "warning", "", fmt.Sprintf("%s didn't start for a request from Discord. %s", inst.short(), sentence(err.Error())))
	if link == "" {
		return // nowhere to say it; the activity feed has it
	}
	m.queueDiscord(discordPost{link: link, text: func() string { return text }, done: func(err error) {
		if err != nil {
			m.log.Printf("posting why %s didn't start in Discord: %v", inst.short(), err)
		}
	}})
}

// activityGame is the instance an activity entry is marked with: none for
// the legacy game, whose entries stay as activity.json has always had them.
func activityGame(inst *instance) *instance {
	if inst.legacy {
		return nil
	}
	return inst
}

// wakeReplyText is the reply to a Discord request that didn't start the
// game short. running lists what runs now, for the memory reply when the
// refusal doesn't carry its own list.
func wakeReplyText(short string, err error, running []RunningGame) string {
	name := "**" + discord.Escape(short) + "**"
	var ae *ActionError
	if errors.As(err, &ae) && ae.Action == "memory" {
		switch d := ae.Data.(type) {
		case MemoryShort:
			running = d.Running
		case *MemoryShort:
			if d != nil {
				running = d.Running
			}
		}
		text := fmt.Sprintf("🧠 There isn't enough memory to start %s right now.", name)
		if len(running) > 0 {
			list := make([]string, len(running))
			for i, g := range running {
				list[i] = fmt.Sprintf("%s (%s)", discord.Escape(g.Name), memText(g.MemMB))
			}
			text += " Running: " + strings.Join(list, ", ") + "."
		}
		return text
	}
	var be *BusyError
	if errors.As(err, &be) {
		return fmt.Sprintf("⚠️ %s didn't start: %s is still in progress. Ask again once it's done.", name, be.Title)
	}
	reason := sentence(err.Error())
	if !strings.HasPrefix(reason, short) {
		reason = lowerFirst(reason) // "the server isn't installed yet", but "Minecraft needs…" keeps its capital
	}
	return fmt.Sprintf("⚠️ %s didn't start: %s", name, reason)
}

// memText is an amount of memory as people read it: "3.1 GB", "800 MB".
func memText(mb int) string {
	if mb < 1024 {
		return fmt.Sprintf("%d MB", mb)
	}
	return fmt.Sprintf("%.1f GB", float64(mb)/1024)
}

// alreadyOnlinePostLocked is the reply to a Discord request for a game
// that is already online: "✅ **Minecraft** is already online." with the
// same join information as its online post. ok is false while a game that
// joins by code has no code yet: its online post comes with it.
func (m *Manager) alreadyOnlinePostLocked(inst *instance, run *serverRun) (p discordPost, ok bool) {
	link := m.wakerLinkLocked(inst)
	if link == "" {
		return p, false
	}
	saved := inst.saved()
	st := run.state()
	j := inst.srv.Join(game.JoinInput{Rec: &run.rec, State: &st, Saved: saved})
	if j.How == "code" && j.Code == "" {
		return p, false
	}
	server := run.rec.Server
	if server == "" {
		server = saved.Server["serverName"]
	}
	in := game.OnlineInput{Server: server, World: run.rec.World, Join: j, Reminder: m.reminderLocked(inst, run)}
	if m.wakePasswordLocked(inst) {
		in.Password = j.Password
	}
	rec := run.rec
	in.Rec = &rec
	texter, _ := inst.srv.(game.OnlineTexter)
	short := inst.short()
	return discordPost{
		link: link,
		text: func() string {
			if in.Join.Code == "" {
				in.Join.Address = m.publicAddress(in.Join.Port)
			}
			body := ""
			if texter != nil {
				body = texter.Online(in)
			} else {
				body = onlineText(in)
			}
			return alreadyOnlineText(short, body)
		},
		done: func(err error) {
			if err != nil {
				m.log.Printf("posting that %s is already online in Discord: %v", short, err)
			}
		},
	}, true
}

// alreadyOnlineText puts "✅ **Minecraft** is already online." in place of
// the first line of the game's online post ("🟢 **<server>** is online"),
// keeping the rest of it: the world, how to join, the password and what
// to update first.
func alreadyOnlineText(short, online string) string {
	head := "✅ **" + discord.Escape(short) + "** is already online."
	if strings.HasPrefix(online, "🟢") {
		_, rest, _ := strings.Cut(online, "\n")
		online = rest
	}
	if online == "" {
		return head
	}
	return head + "\n" + online
}

// wakePasswordLocked reports whether the game's posts include its password.
func (m *Manager) wakePasswordLocked(inst *instance) bool {
	if inst.legacy {
		return m.settings.DiscordPassword
	}
	if in := m.servers.Instance(inst.id); in != nil {
		return in.DiscordRules().Password
	}
	return servers.DefaultDiscord().Password
}

// wakerObserver is stream WK's observer (hooks.go). Every method runs with
// m.mu held and must not block.
type wakerObserver struct{ m *Manager }

func (o wakerObserver) onReady(inst *instance)                {}
func (o wakerObserver) onEvent(inst *instance, ev game.Event) {}

// onExit: the run a Discord request started is over.
func (o wakerObserver) onExit(inst *instance, run *serverRun, exit *ExitInfo) {
	delete(o.m.wk.asked, inst.id)
}

// onTick: the waker follows any change to the commands it should have.
func (o wakerObserver) onTick(now time.Time) { o.m.noticeCommandsLocked() }

// onJob: a start a Discord request asked for failed, so the channel the
// request came from hears why (spec 6.4's "failed").
func (o wakerObserver) onJob(inst *instance, job *Job) {
	m := o.m
	if inst == nil || job.Finished == nil || job.Kind != "start" || job.Error == "" || !m.wk.asked[inst.id] || inst.run != nil {
		return
	}
	delete(m.wk.asked, inst.id)
	err := errors.New(job.Error)
	m.go_(func() { m.wakeReply(inst, err) })
}

func (o wakerObserver) onTakeover(inst *instance) {}
func (o wakerObserver) onWake(cause string)       {}
