package manager

// Starting the server from Discord. A small program, the waker, runs on the
// JetKVM (the IP KVM next to this computer, which is always on). When
// someone types /valheim in Discord, it wakes this computer with
// Wake-on-LAN, and the app starts the server when it hears the same signal
// (wakesignal.go). The app puts the waker on the JetKVM over SSH, through
// the JetKVM's Developer Mode, and keeps it up to date.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/discord"
	"valheim-easy-server/internal/jetkvm"
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

// wakerSetup is what a waker is installed with, apart from the token.
type wakerSetup struct {
	Version string `json:"version,omitempty"` // the app's, which the waker matches
	MAC     string `json:"mac,omitempty"`     // the network adapter it wakes
	Query   string `json:"query,omitempty"`   // where it asks whether Valheim is up
	Name    string `json:"name,omitempty"`    // the server's
	Guild   string `json:"guild,omitempty"`   // the Discord server it answers in
}

// wakerState is the waker's side of the Manager, under its mu.
type wakerState struct {
	file    wakerFile
	status  *waker.Status // last read from the JetKVM
	at      time.Time     // when
	problem string        // why it couldn't be read or updated then
	action  string        // what the problem needs, as for WakerView.Action
	busy    bool          // being installed, updated or removed
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
	setup.Version, setup.Name = m.opt.AppVersion, s.ServerName
	// With the join code posted in a Discord channel, the bot answers only
	// in that Discord server, so nobody can start the server from another.
	if s.Discord && discord.Problem(s.DiscordWebhook) == "" {
		g, err := discord.Guild(ctx, nil, m.webhookURL(s.DiscordWebhook))
		if err != nil {
			return waker.Config{}, setup, fmt.Errorf("Couldn’t find out which Discord server the join-code channel is in. %v", err)
		}
		setup.Guild = g
	}
	cfg := waker.Config{Token: token, MAC: setup.MAC, Query: setup.Query, Name: setup.Name, Guild: setup.Guild}
	return cfg, setup, nil
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
	m.mu.Unlock()
	m.saveWaker()
	if err != nil {
		return err
	}
	m.syncWakeListener()
	m.recheckSoon()
	m.addActivity("info", "", "The waker is on the JetKVM: friends can type /valheim in Discord to wake this computer and start the server.")
	return nil
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
		CanPost: s.Discord && discord.Problem(s.DiscordWebhook) == "",
	}
	if s.DiscordWake {
		v.Status, v.CheckedAt, v.Problem, v.Action = m.wk.status, m.wk.at, m.wk.problem, m.wk.action
	}
	if f.AppID != "" {
		v.Invite = waker.Invite(f.AppID, f.Setup.Guild)
	}
	return v
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

// wakerLoop keeps the waker on the JetKVM up to date (after the app updates
// itself, or when this computer's network address or the server's name
// changes) and running.
func (m *Manager) wakerLoop() {
	t := time.NewTimer(90 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-t.C:
		}
		m.upkeepWaker()
		t.Reset(30 * time.Minute)
	}
}

func (m *Manager) upkeepWaker() {
	m.mu.Lock()
	on, token, addr, file := m.settings.DiscordWake, m.settings.DiscordBotToken, m.settings.JetKVM, m.wk.file
	m.mu.Unlock()
	if !on || m.beginWaker() != nil {
		return
	}
	defer m.endWaker()
	ctx, cancel := context.WithTimeout(m.ctx, 3*time.Minute)
	defer cancel()
	st, err := m.readWaker(ctx)
	var stopped *jetkvm.ErrStopped
	if err != nil && !errors.As(err, &stopped) {
		return // it can't be reached: that shows on the Settings page
	}
	if m.wakerMissing() != "" {
		return // a development build: it can't update it
	}
	cfg, setup, err := m.wakerPlan(ctx, token)
	if err != nil {
		m.log.Printf("checking the waker's settings: %v", err)
		return
	}
	if stopped == nil && setup == file.Setup && st.Version == setup.Version {
		return
	}
	why := "to match this app"
	if stopped != nil {
		why = "because it had stopped"
	}
	m.log.Printf("updating the waker on the JetKVM %s", why)
	bin, err := m.wakerBinary()
	key, err2 := m.jetkvmKey()
	if err != nil || err2 != nil {
		m.log.Printf("updating the waker: %v %v", err, err2)
		return
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
	}
	m.mu.Unlock()
	if err != nil {
		m.log.Printf("updating the waker: %v", err)
		return
	}
	m.saveWaker()
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
	m.mu.Unlock()
	if !s.Discord || discord.Problem(s.DiscordWebhook) != "" {
		return errors.New("Turn on “Post the join code in Discord” first: the link is posted in that channel.")
	}
	var text string
	switch what {
	case "invite":
		if f.AppID == "" {
			return errors.New("Install the waker on the JetKVM first.")
		}
		text = "🔗 Add the bot to this Discord server, so everyone here can type **/valheim** to start the Valheim server (it needs someone who can manage the Discord server):\n" + waker.Invite(f.AppID, f.Setup.Guild)
	case "key":
		text = "🔑 Valheim Easy Server’s key for the JetKVM. Copy it into the JetKVM’s settings, under Advanced (Developer Mode), in SSH Public Key:\n```\n" + pub + "\n```"
	default:
		return errors.New("Nothing to post.")
	}
	ctx, cancel := context.WithTimeout(m.ctx, 45*time.Second)
	defer cancel()
	return discord.Post(ctx, nil, m.webhookURL(s.DiscordWebhook), text)
}
