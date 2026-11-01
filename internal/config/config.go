// Package config holds the settings people can change in the app, checks
// them, and turns them into valheim_server command-line arguments.
package config

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"valheim-easy-server/internal/discord"
	"valheim-easy-server/internal/mods"
)

// Settings is everything stored in settings.json.
type Settings struct {
	ServerName string `json:"serverName"`
	Password   string `json:"password"`
	World      string `json:"world"`
	Port       int    `json:"port"`
	Public     bool   `json:"public"`
	Crossplay  bool   `json:"crossplay"`

	SaveIntervalMinutes int `json:"saveIntervalMinutes"`
	Backups             int `json:"backups"`
	BackupShortMinutes  int `json:"backupShortMinutes"`
	BackupLongMinutes   int `json:"backupLongMinutes"`

	UpdateOnStart  bool `json:"updateOnStart"`
	AutoUpdate     bool `json:"autoUpdate"` // the app updates itself from GitHub
	AutoRestart    bool `json:"autoRestart"`
	StartWithApp   bool `json:"startWithApp"`
	StartAfterWake bool `json:"startAfterWake"`
	IdleHibernate  bool `json:"idleHibernate"` // rest (see RestMode) when nobody plays
	IdleMinutes    int  `json:"idleMinutes"`
	// RestMode is how the computer rests: RestHibernate uses no power;
	// RestSleep keeps a little, which most network adapters need to wake
	// the computer with Wake-on-LAN.
	RestMode string `json:"restMode"`

	// Discord: post the join code in a Discord channel, through the
	// channel's webhook link, each time the server is online.
	Discord         bool   `json:"discord"`
	DiscordWebhook  string `json:"discordWebhook,omitempty"`
	DiscordPassword bool   `json:"discordPassword"` // put the password in the message too
	DiscordStopped  bool   `json:"discordStopped"`  // also say when the server stops
	DiscordDaily    bool   `json:"discordDaily"`    // and who died the most each day

	// Starting the server from Discord: a waker on the JetKVM (an IP KVM
	// that's always on) answers /valheim by waking this computer. These are
	// set when it's installed, not on the Settings page.
	DiscordBotToken string `json:"discordBotToken,omitempty"`
	JetKVM          string `json:"jetkvm,omitempty"` // its address on the home network
	DiscordWake     bool   `json:"discordWake"`      // the waker is installed: listen for its wake signals

	// WorldOptions holds difficulty settings per world name.
	WorldOptions map[string]WorldOptions `json:"worldOptions,omitempty"`

	// Mods is where the previous version kept one list of mods for every
	// world. Load moves it to the selected world's options.
	Mods []string `json:"mods,omitempty"`

	// Advanced: other folders and extra launch options.
	ServerDir string `json:"serverDir,omitempty"`
	SaveDir   string `json:"saveDir,omitempty"`
	ExtraArgs string `json:"extraArgs,omitempty"`
}

// WorldOptions are Valheim's world modifiers for one world.
type WorldOptions struct {
	Preset    string            `json:"preset,omitempty"`
	Modifiers map[string]string `json:"modifiers,omitempty"`
	Keys      []string          `json:"keys,omitempty"`
	// Off are keys that were turned on here and later off again. The world
	// keeps a key it was given, so while any are listed the server starts
	// with -resetmodifiers, which clears the world's modifiers (not its boss
	// progress) before the ones here are applied again.
	Off []string `json:"off,omitempty"`
	// Mods are the mods (IDs from the mods catalog) the world is played with.
	Mods []string `json:"mods,omitempty"`
}

// WorldMods returns the mods the selected world is played with.
func (s Settings) WorldMods() []string {
	return s.WorldOptions[s.World].Mods
}

const DefaultGamePort = 2456

// Rest modes.
const (
	RestHibernate = "hibernate"
	RestSleep     = "sleep"
)

// Presets are the values Valheim accepts for -preset.
var Presets = []string{"normal", "casual", "easy", "hard", "hardcore", "immersive", "hammer"}

// Modifiers are the values Valheim accepts for each -modifier.
var Modifiers = map[string][]string{
	"combat":       {"veryeasy", "easy", "hard", "veryhard"},
	"deathpenalty": {"casual", "veryeasy", "easy", "hard", "hardcore"},
	"resources":    {"muchless", "less", "more", "muchmore", "most"},
	"raids":        {"none", "muchless", "less", "more", "muchmore"},
	"portals":      {"casual", "hard", "veryhard"},
}

// ModifierOrder is the order modifiers are shown and passed in.
var ModifierOrder = []string{"combat", "deathpenalty", "resources", "raids", "portals"}

// Keys are the values Valheim accepts for -setkey. "fire" is the Fire hazard
// world modifier: fire spreads to wood everywhere, not only in the Ashlands.
var Keys = []string{"nobuildcost", "playerevents", "passivemobs", "nomap", "fire"}

// Defaults returns first-run settings, with a freshly generated password.
func Defaults() Settings {
	return Settings{
		ServerName:          "Valheim with Friends",
		Password:            GeneratePassword(),
		World:               "Midgard",
		Port:                DefaultGamePort,
		Public:              false,
		Crossplay:           true,
		SaveIntervalMinutes: 15,
		Backups:             4,
		BackupShortMinutes:  120,
		BackupLongMinutes:   720,
		UpdateOnStart:       true,
		AutoUpdate:          true,
		AutoRestart:         true,
		StartAfterWake:      true,
		IdleMinutes:         60,
		RestMode:            defaultRestMode(),
		DiscordStopped:      true,
		DiscordDaily:        true,
	}
}

// Clone returns a deep copy so callers can't share maps and slices.
func (s Settings) Clone() Settings {
	c := s
	c.Mods = append([]string(nil), s.Mods...)
	if s.WorldOptions != nil {
		c.WorldOptions = make(map[string]WorldOptions, len(s.WorldOptions))
		for k, v := range s.WorldOptions {
			c.WorldOptions[k] = v.Clone()
		}
	}
	return c
}

// Clone returns a deep copy.
func (o WorldOptions) Clone() WorldOptions {
	c := WorldOptions{Preset: o.Preset}
	if o.Modifiers != nil {
		c.Modifiers = make(map[string]string, len(o.Modifiers))
		for k, v := range o.Modifiers {
			c.Modifiers[k] = v
		}
	}
	c.Keys = append([]string(nil), o.Keys...)
	c.Off = append([]string(nil), o.Off...)
	c.Mods = append([]string(nil), o.Mods...)
	return c
}

// IsZero reports whether nothing is set, so the world keeps its own
// modifiers and is played without mods.
func (o WorldOptions) IsZero() bool {
	return o.Preset == "" && len(o.Modifiers) == 0 && len(o.Keys) == 0 && len(o.Off) == 0 && len(o.Mods) == 0
}

// WithOff returns o, changed from prev, with Off listing every key that was
// on (or already off) in prev and isn't on in o.
func (o WorldOptions) WithOff(prev WorldOptions) WorldOptions {
	o.Off = nil
	for _, k := range Keys {
		if !contains(o.Keys, k) && (contains(prev.Keys, k) || contains(prev.Off, k)) {
			o.Off = append(o.Off, k)
		}
	}
	return o
}

// fillMissing repairs zero values left by an old or hand-edited file.
func (s *Settings) fillMissing() {
	d := Defaults()
	if strings.TrimSpace(s.ServerName) == "" {
		s.ServerName = d.ServerName
	}
	if s.Password == "" {
		s.Password = d.Password
	}
	if strings.TrimSpace(s.World) == "" {
		s.World = d.World
	}
	if len(s.Mods) > 0 { // from the previous version: the selected world's now
		o := s.WorldOptions[s.World].Clone()
		for _, id := range s.Mods {
			if !contains(o.Mods, id) {
				o.Mods = append(o.Mods, id)
			}
		}
		if s.WorldOptions == nil {
			s.WorldOptions = map[string]WorldOptions{}
		}
		s.WorldOptions[s.World] = o
		s.Mods = nil
	}
	for name, o := range s.WorldOptions { // mods this version no longer offers
		known := slices.DeleteFunc(slices.Clone(o.Mods), func(id string) bool { _, ok := mods.Find(id); return !ok })
		if len(known) != len(o.Mods) {
			o.Mods = known
			s.WorldOptions[name] = o
		}
	}
	if s.Port == 0 {
		s.Port = d.Port
	}
	if s.SaveIntervalMinutes == 0 {
		s.SaveIntervalMinutes = d.SaveIntervalMinutes
	}
	if s.BackupShortMinutes == 0 {
		s.BackupShortMinutes = d.BackupShortMinutes
	}
	if s.BackupLongMinutes == 0 {
		s.BackupLongMinutes = d.BackupLongMinutes
	}
	if s.IdleMinutes == 0 {
		s.IdleMinutes = d.IdleMinutes
	}
	if s.RestMode == "" || runtime.GOOS == "darwin" && s.RestMode == RestHibernate { // Macs sleep; they don't hibernate
		s.RestMode = d.RestMode
	}
}

// Normalize trims whitespace that people paste by accident.
func (s *Settings) Normalize() {
	s.ServerName = strings.TrimSpace(s.ServerName)
	s.Password = strings.TrimSpace(s.Password)
	s.World = strings.TrimSpace(s.World)
	s.ServerDir = strings.TrimSpace(s.ServerDir)
	s.SaveDir = strings.TrimSpace(s.SaveDir)
	s.ExtraArgs = strings.TrimSpace(s.ExtraArgs)
	s.DiscordWebhook = strings.TrimSpace(s.DiscordWebhook)
	s.DiscordBotToken = strings.TrimSpace(s.DiscordBotToken)
	s.JetKVM = strings.TrimSpace(s.JetKVM)
	if runtime.GOOS == "darwin" && s.RestMode == RestHibernate {
		s.RestMode = RestSleep // Macs sleep; they don't hibernate
	}
	for name, o := range s.WorldOptions {
		if o.IsZero() {
			delete(s.WorldOptions, name)
		}
	}
}

// Load reads settings from path. A missing file gives defaults and
// existed=false. A damaged file is reported as an error together with
// defaults, so the caller can decide what to do with the broken file.
func Load(path string) (s Settings, existed bool, err error) {
	s = Defaults()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, false, nil
	}
	if err != nil {
		return s, false, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return Defaults(), true, fmt.Errorf("settings file is damaged: %w", err)
	}
	s.fillMissing()
	return s, true, nil
}

// Save writes settings atomically.
func Save(path string, s Settings) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, append(b, '\n'), 0o600)
}

// WriteFileAtomic writes to a temp file and renames it over path, so a
// crash or power cut never leaves a half-written file behind.
func WriteFileAtomic(path string, data []byte, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil && runtime.GOOS != "windows" {
		return err
	}
	return os.Rename(tmpName, path)
}

// FieldErrors maps a settings field (its JSON name) to a friendly message.
type FieldErrors map[string]string

func (e FieldErrors) Error() string {
	keys := make([]string, 0, len(e))
	for k := range e {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, e[k])
	}
	return strings.Join(parts, " ")
}

// Validate checks every field and returns nil when all are fine.
func (s Settings) Validate() FieldErrors {
	errs := FieldErrors{}

	name := strings.TrimSpace(s.ServerName)
	switch {
	case name == "":
		errs["serverName"] = "Give the server a name."
	case utf8.RuneCountInString(name) > 64:
		errs["serverName"] = "The server name can be at most 64 characters."
	case hasBadChars(name):
		errs["serverName"] = `The server name can't contain quotes (") or control characters.`
	case strings.HasPrefix(name, "-"):
		errs["serverName"] = "The server name can't start with a dash (-)."
	}

	pw := s.Password
	switch {
	case utf8.RuneCountInString(pw) < 5:
		errs["password"] = "The password needs at least 5 characters."
	case utf8.RuneCountInString(pw) > 64:
		errs["password"] = "The password can be at most 64 characters."
	case hasBadChars(pw) || strings.ContainsFunc(pw, unicode.IsSpace):
		errs["password"] = `The password can't contain spaces or quotes (").`
	case strings.HasPrefix(pw, "-"):
		errs["password"] = "The password can't start with a dash (-)."
	case name != "" && strings.Contains(strings.ToLower(name), strings.ToLower(pw)):
		errs["password"] = "Valheim doesn't allow the password to be part of the server name."
	case s.World != "" && strings.Contains(strings.ToLower(s.World), strings.ToLower(pw)):
		errs["password"] = "Valheim doesn't allow the password to be part of the world name."
	}

	if msg := WorldNameProblem(s.World); msg != "" {
		errs["world"] = msg
	}

	// The folders are given to programs, some with admin rights (the
	// firewall rule), so they must be plain full paths.
	for field, dir := range map[string]string{"serverDir": s.ServerDir, "saveDir": s.SaveDir} {
		switch {
		case dir == "":
		case !filepath.IsAbs(dir):
			errs[field] = `Use the folder's full path, for example C:\Valheim Server\server.`
		case hasBadChars(dir):
			errs[field] = `The folder can't contain quotes (") or control characters.`
		}
	}

	if s.Port < 1024 || s.Port > 65534 {
		errs["port"] = "The game port must be between 1024 and 65534 (2456 is normal)."
	}
	if s.SaveIntervalMinutes < 1 || s.SaveIntervalMinutes > 240 {
		errs["saveIntervalMinutes"] = "Save every 1 to 240 minutes."
	}
	if s.Backups < 0 || s.Backups > 50 {
		errs["backups"] = "Keep between 0 and 50 automatic backups."
	}
	if s.BackupShortMinutes < 5 || s.BackupShortMinutes > 1440 {
		errs["backupShortMinutes"] = "The first backup interval must be 5 to 1440 minutes."
	}
	if s.BackupLongMinutes < 30 || s.BackupLongMinutes > 10080 {
		errs["backupLongMinutes"] = "The backup interval must be 30 to 10080 minutes."
	}
	if s.IdleMinutes < 10 || s.IdleMinutes > 1440 {
		errs["idleMinutes"] = "Choose between 10 and 1440 minutes."
	}
	if s.RestMode != RestHibernate && s.RestMode != RestSleep {
		errs["restMode"] = "Choose hibernate or sleep."
	}
	if s.Discord {
		if msg := discord.Problem(s.DiscordWebhook); msg != "" {
			errs["discordWebhook"] = msg
		}
	}
	if len(s.ExtraArgs) > 1000 {
		errs["extraArgs"] = "Extra arguments are too long."
	} else if _, err := SplitArgs(s.ExtraArgs); err != nil {
		errs["extraArgs"] = err.Error()
	}
	for world, o := range s.WorldOptions {
		if msg := o.Problem(); msg != "" {
			errs["worldOptions"] = fmt.Sprintf("%s: %s", world, msg)
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return errs
}

// Problem describes an invalid modifier, or returns "".
func (o WorldOptions) Problem() string {
	if o.Preset != "" && !contains(Presets, o.Preset) {
		return fmt.Sprintf("unknown difficulty preset %q.", o.Preset)
	}
	for k, v := range o.Modifiers {
		allowed, ok := Modifiers[k]
		if !ok {
			return fmt.Sprintf("unknown world modifier %q.", k)
		}
		if v != "" && !contains(allowed, v) {
			return fmt.Sprintf("%q is not a valid value for %s.", v, k)
		}
	}
	for _, list := range [][]string{o.Keys, o.Off} {
		for _, k := range list {
			if !contains(Keys, k) {
				return fmt.Sprintf("unknown world option %q.", k)
			}
		}
	}
	for _, id := range o.Mods {
		if _, ok := mods.Find(id); !ok {
			return fmt.Sprintf("there's no mod called %q in this app.", id)
		}
	}
	return ""
}

var reservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
	"COM¹": true, "COM²": true, "COM³": true, "LPT¹": true, "LPT²": true, "LPT³": true,
	"CONIN$": true, "CONOUT$": true,
}

// WorldNameProblem returns why name can't be used as a world name, or "".
// World names become file and folder names, so they must be safe on Windows.
func WorldNameProblem(name string) string {
	switch {
	case strings.TrimSpace(name) == "":
		return "Give the world a name."
	case name != strings.TrimSpace(name):
		return "The world name can't start or end with a space."
	case utf8.RuneCountInString(name) > 40:
		return "The world name can be at most 40 characters."
	case strings.ContainsAny(name, `\/:*?"<>|`) || strings.ContainsFunc(name, unicode.IsControl):
		return `The world name can't contain any of these: \ / : * ? " < > |`
	case strings.HasSuffix(name, ".") || strings.HasPrefix(name, "."):
		return "The world name can't start or end with a dot."
	case strings.HasPrefix(name, "-"):
		return "The world name can't start with a dash (-)." // Valheim would take it for an option
	case reservedNames[strings.ToUpper(strings.SplitN(name, ".", 2)[0])]:
		return "That name is reserved by Windows. Pick another."
	case strings.Contains(strings.ToLower(name), "_backup_"):
		return `The world name can't contain "_backup_" (Valheim uses that for backups).`
	}
	return ""
}

func hasBadChars(s string) bool {
	return strings.Contains(s, `"`) || strings.ContainsFunc(s, unicode.IsControl)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Paths are the folders the app works with.
type Paths struct {
	Data       string `json:"data"`        // the app's own folder (settings, logs, backups)
	Server     string `json:"server"`      // where valheim_server is installed
	Save       string `json:"save"`        // passed to -savedir
	Worlds     string `json:"worlds"`      // Save/worlds_local
	Backups    string `json:"backups"`     // the app's world backups
	Logs       string `json:"logs"`        // server and app logs
	SteamCMD   string `json:"steamcmd"`    // SteamCMD install
	Staging    string `json:"staging"`     // uploads waiting to be imported
	Settings   string `json:"settings"`    // settings.json
	ServerExe  string `json:"serverExe"`   // full path to the server executable
	SteamCMDEx string `json:"steamcmdExe"` // full path to steamcmd
}

// ServerExeName is the dedicated server binary for this OS, relative to the
// server folder. The macOS one is a universal program (Intel and Apple
// silicon) in a folder of its own.
func ServerExeName() string {
	switch runtime.GOOS {
	case "windows":
		return "valheim_server.exe"
	case "darwin":
		return filepath.Join("valheim_server", "Valheim")
	}
	return "valheim_server.x86_64"
}

// defaultRestMode is how the computer rests unless the settings say: Macs
// sleep (they don't hibernate), and Windows computers hibernate.
func defaultRestMode() string {
	if runtime.GOOS == "darwin" {
		return RestSleep
	}
	return RestHibernate
}

// ResolvePaths works out all folders from the data folder and settings.
func ResolvePaths(dataDir string, s Settings) Paths {
	p := Paths{
		Data:     dataDir,
		Server:   filepath.Join(dataDir, "server"),
		Save:     filepath.Join(dataDir, "saves"),
		Backups:  filepath.Join(dataDir, "backups"),
		Logs:     filepath.Join(dataDir, "logs"),
		SteamCMD: filepath.Join(dataDir, "steamcmd"),
		Staging:  filepath.Join(dataDir, "uploads"),
		Settings: filepath.Join(dataDir, "settings.json"),
	}
	if s.ServerDir != "" {
		p.Server = s.ServerDir
	}
	if s.SaveDir != "" {
		p.Save = s.SaveDir
	}
	p.Worlds = filepath.Join(p.Save, "worlds_local")
	p.ServerExe = filepath.Join(p.Server, ServerExeName())
	if runtime.GOOS == "windows" {
		p.SteamCMDEx = filepath.Join(p.SteamCMD, "steamcmd.exe")
	} else {
		p.SteamCMDEx = filepath.Join(p.SteamCMD, "steamcmd.sh")
	}
	return p
}

// ServerArgs builds the valheim_server command line (without the program).
func (s Settings) ServerArgs(saveDir, logFile string) ([]string, error) {
	public := "0"
	if s.Public {
		public = "1"
	}
	args := []string{
		"-nographics", "-batchmode",
		"-name", s.ServerName,
		"-port", strconv.Itoa(s.Port),
		"-world", s.World,
		"-password", s.Password,
		"-public", public,
		"-savedir", saveDir,
		"-logFile", logFile,
		"-saveinterval", strconv.Itoa(s.SaveIntervalMinutes * 60),
		"-backups", strconv.Itoa(s.Backups),
		"-backupshort", strconv.Itoa(s.BackupShortMinutes * 60),
		"-backuplong", strconv.Itoa(s.BackupLongMinutes * 60),
	}
	if s.Crossplay {
		args = append(args, "-crossplay")
	}
	// Valheim wants the preset first; modifiers given after it override it.
	if o, ok := s.WorldOptions[s.World]; ok {
		if len(o.Off) > 0 {
			args = append(args, "-resetmodifiers")
		}
		if o.Preset != "" {
			args = append(args, "-preset", o.Preset)
		}
		for _, k := range ModifierOrder {
			if v := o.Modifiers[k]; v != "" {
				args = append(args, "-modifier", k, v)
			}
		}
		for _, k := range Keys {
			if contains(o.Keys, k) {
				args = append(args, "-setkey", k)
			}
		}
	}
	extra, err := SplitArgs(s.ExtraArgs)
	if err != nil {
		return nil, err
	}
	return append(args, extra...), nil
}

// SplitArgs splits a command line on spaces, keeping "quoted parts"
// together. Backslashes are not special, so Windows paths work as typed.
func SplitArgs(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inQuote, have := false, false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			have = true
		case unicode.IsSpace(r) && !inQuote:
			if have {
				args = append(args, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteRune(r)
			have = true
		}
	}
	if inQuote {
		return nil, errors.New("Extra arguments have a quote (\") that is never closed.")
	}
	if have {
		args = append(args, cur.String())
	}
	return args, nil
}

var passwordWords = []string{
	"odin", "thor", "freya", "loki", "frigg", "tyr", "baldr", "heimdall",
	"fjord", "rune", "skald", "longship", "mead", "troll", "draugr",
	"yggdrasil", "bifrost", "valkyrie", "jotunn", "mjolnir", "raven",
	"wolf", "drakkar", "saga", "bronze", "silver", "serpent", "eikthyr",
}

// GeneratePassword makes an easy-to-share password such as
// "raven-fjord-4821": two words and a number, about 7 million possibilities,
// so guessing it by trying to join would take far too long.
func GeneratePassword() string {
	pick := func(n int) int64 {
		v, _ := rand.Int(rand.Reader, big.NewInt(int64(n)))
		return v.Int64()
	}
	return fmt.Sprintf("%s-%s-%d", passwordWords[pick(len(passwordWords))], passwordWords[pick(len(passwordWords))], 1000+pick(9000))
}
