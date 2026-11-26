// Package servers reads and writes servers.json: the list of game servers
// (instances) on this computer and the app-wide settings that came with
// several games (spec 3.2). It is used from R3 on; R2 never reads or writes
// the file (spec 3.4).
//
// Installed copies update themselves and roll back by themselves, so the
// file is read and written with care:
//
//   - every write is atomic and private (0600, it holds Discord webhooks),
//     and keeps the previous good copy as servers.json.bak;
//   - keys this build doesn't know (at the top, in "app", in each instance
//     and in its "discord") are kept and written back unchanged, so an
//     older build that changes the file loses nothing a newer one put there;
//   - a damaged file falls back to its backup, a missing one is rebuilt, and
//     a file from a newer version is never rewritten (Load, Open, Update).
package servers

import (
	"encoding/json"
	"time"

	"valheim-easy-server/internal/game"
)

// Schema is the version of servers.json this build writes. A file with a
// higher number was written by a newer version of the app, whose meaning
// this build can't know, so it is never rewritten (Newer, safe mode).
const Schema = 1

// FileName is the list's name in the data folder; BackupSuffix is added to
// it for the previous good copy.
const (
	FileName     = "servers.json"
	BackupSuffix = ".bak"
)

// DefaultAutoStopMinutes is how long a game runs with nobody on before it
// stops itself, when its instance doesn't say (decided: one hour, all games).
const DefaultAutoStopMinutes = 60

// File is servers.json.
type File struct {
	Schema    int        `json:"schema"`
	App       App        `json:"app"`
	Instances []Instance `json:"instances"`
	// Unknown holds the top-level keys this build doesn't know, written
	// back as they were.
	Unknown map[string]json.RawMessage `json:"-"`
}

// App holds the app-wide settings that are new with several games. The
// older ones (idle sleep, auto-update, the main Discord channel, the waker)
// stay in settings.json, whose shape is frozen (invariant I3).
type App struct {
	Stars         []string                   `json:"stars"`
	ActiveOpen    *bool                      `json:"activeOpen,omitempty"`   // nil = open
	InactiveOpen  *bool                      `json:"inactiveOpen,omitempty"` // nil = closed
	DisabledGames []string                   `json:"disabledGames,omitempty"`
	RouterPorts   *bool                      `json:"routerPorts,omitempty"` // nil = on
	Unknown       map[string]json.RawMessage `json:"-"`
}

// Instance is one server of a game on this computer.
type Instance struct {
	ID              string                     `json:"id"`
	Game            string                     `json:"game"`
	Legacy          bool                       `json:"legacy,omitempty"`
	Added           time.Time                  `json:"added"`
	Hidden          bool                       `json:"hidden,omitempty"` // legacy only: "removed" from the panel (I1: stopped, unstarred, never started)
	Settings        game.Values                `json:"settings,omitempty"`
	World           string                     `json:"world,omitempty"`
	Worlds          map[string]game.Values     `json:"worlds,omitempty"`
	Ports           map[string]int             `json:"ports,omitempty"`
	PortsChosen     []string                   `json:"portsChosen,omitempty"`
	State           game.Values                `json:"state,omitempty"`
	AutoStopMinutes *int                       `json:"autoStopMinutes,omitempty"` // nil = 60 (R3); 0 = off
	StartWithApp    bool                       `json:"startWithApp,omitempty"`    // non-legacy only (2.9)
	PresenceSeen    time.Time                  `json:"presenceSeen,omitzero"`     // first join seen on this install (2.6)
	Discord         *Discord                   `json:"discord,omitempty"`         // nil = DefaultDiscord
	Unknown         map[string]json.RawMessage `json:"-"`
}

// Discord is an instance's own Discord posting rules. For the legacy
// instance only Webhook is used; its on/stopped/daily/password stay in
// settings.json, as today.
type Discord struct {
	On       bool   `json:"on"`
	Webhook  string `json:"webhook,omitempty"` // "" = the main channel (settings.json discordWebhook)
	Stopped  bool   `json:"stopped"`
	Daily    bool   `json:"daily"`
	Password bool   `json:"password"`
	// Unknown keeps keys a newer build added here, like the other levels,
	// so a rewrite by this build doesn't drop them.
	Unknown map[string]json.RawMessage `json:"-"`
}

// DefaultDiscord is what a nil Instance.Discord means: post in the main
// channel when it starts and stops, and the daily records, without the
// password. So a newly added game posts in the main channel, as decided,
// without the owner touching anything. (With no main channel and no own
// webhook nothing can be posted; the Discord page says so.)
func DefaultDiscord() Discord {
	return Discord{On: true, Stopped: true, Daily: true}
}

// DiscordRules returns the instance's Discord rules, with the defaults
// filled in when it has none of its own. The result is a copy.
func (in Instance) DiscordRules() Discord {
	if in.Discord == nil {
		return DefaultDiscord()
	}
	d := *in.Discord
	d.Unknown = cloneRaw(d.Unknown)
	return d
}

// AutoStop is how many minutes the game may run with nobody on before it
// stops itself; 0 means never. A missing value is the decided hour; a
// negative one (only a hand edit makes one) means never, as 0 does.
func (in Instance) AutoStop() int {
	switch {
	case in.AutoStopMinutes == nil:
		return DefaultAutoStopMinutes
	case *in.AutoStopMinutes < 0:
		return 0
	}
	return *in.AutoStopMinutes
}

// ActiveListOpen says whether the sidebar's Active list is open (it is
// unless the owner closed it).
func (a App) ActiveListOpen() bool { return a.ActiveOpen == nil || *a.ActiveOpen }

// InactiveListOpen says whether the sidebar's Inactive list is open (it is
// closed unless the owner opened it).
func (a App) InactiveListOpen() bool { return a.InactiveOpen != nil && *a.InactiveOpen }

// RouterPortsOn says whether the app opens router ports itself (on unless
// the owner turned it off).
func (a App) RouterPortsOn() bool { return a.RouterPorts == nil || *a.RouterPorts }

// Instance finds the instance with this id, or nil. The pointer is into
// f.Instances, so a change through it changes f.
func (f *File) Instance(id string) *Instance {
	for i := range f.Instances {
		if f.Instances[i].ID == id {
			return &f.Instances[i]
		}
	}
	return nil
}

// New is the list a first start makes (spec 3.4): only the legacy game,
// starred, added now. Every other setting is left out, so it means its
// default. With no legacy game (legacyID "", as in tests that register no
// games) the list is empty.
func New(legacyID string, now time.Time) File {
	f := File{Schema: Schema, App: App{Stars: []string{}}, Instances: []Instance{}}
	if legacyID != "" {
		f.App.Stars = []string{legacyID}
		f.Instances = []Instance{{ID: legacyID, Game: legacyID, Legacy: true, Added: now.UTC()}}
	}
	return f
}

// ValidID says whether id can be an instance id: a lower-case letter, then
// up to 63 lower-case letters, digits and hyphens ("minecraft",
// "minecraft-2"). The id names the instance's folder under games/, so this
// keeps a hand-edited id from reaching outside it ("../", "C:", a dot
// folder). The manager uses it before it touches an instance's folder;
// internal/wol reads ids from wake packets by the same rule.
func ValidID(id string) bool {
	if len(id) == 0 || len(id) > 64 || id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for i := 1; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
