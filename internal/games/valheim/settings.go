package valheim

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"strconv"
	"strings"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/mods"
)

// Valheim's settings stay in settings.json (it is frozen, invariant I3).
// The game contract sees them as values by key (spec 2.9): the server's
// own settings, and each world's difficulty and mods from worldOptions.
// Only those are mapped; the app-wide settings in the same file (idle
// sleep, Discord, the waker...) are never touched here.

// serverKeys are the settings.json fields in Valheim's server scope.
var serverKeys = []string{"serverName", "password", "port", "public", "crossplay", "saveIntervalMinutes",
	"backups", "backupShortMinutes", "backupLongMinutes", "updateOnStart", "autoRestart", "extraArgs",
	"serverDir", "saveDir"}

// serverValues are s's server-scope settings as values.
func serverValues(s config.Settings) game.Values {
	b := strconv.FormatBool
	n := strconv.Itoa
	return game.Values{
		"serverName": s.ServerName, "password": s.Password, "port": n(s.Port),
		"public": b(s.Public), "crossplay": b(s.Crossplay),
		"saveIntervalMinutes": n(s.SaveIntervalMinutes), "backups": n(s.Backups),
		"backupShortMinutes": n(s.BackupShortMinutes), "backupLongMinutes": n(s.BackupLongMinutes),
		"updateOnStart": b(s.UpdateOnStart), "autoRestart": b(s.AutoRestart),
		"extraArgs": s.ExtraArgs, "serverDir": s.ServerDir, "saveDir": s.SaveDir,
	}
}

// worldValues are one world's options as values: "preset",
// "modifier.<name>", "key.<name>" ("true") and "mods" (catalog ids,
// comma-separated, in their order). Off isn't a value: which keys were
// turned off is worked out when the values are saved (see toSettings).
func worldValues(o config.WorldOptions) game.Values {
	v := game.Values{}
	if o.Preset != "" {
		v["preset"] = o.Preset
	}
	for k, val := range o.Modifiers {
		v["modifier."+k] = val
	}
	for _, k := range o.Keys {
		v["key."+k] = "true"
	}
	if len(o.Mods) > 0 {
		v["mods"] = strings.Join(o.Mods, ",")
	}
	return v
}

// fromSettings is everything Valheim's Store holds, from settings.json.
func fromSettings(s config.Settings) game.Saved {
	saved := game.Saved{
		Server: serverValues(s),
		World:  s.World,
		Ports:  map[string]int{"game": s.Port, "query": s.Port + 1},
		Worlds: map[string]game.Values{},
	}
	for name, o := range s.WorldOptions {
		saved.Worlds[name] = worldValues(o)
	}
	return saved
}

// toSettings is base with saved's values put back into settings.json's
// fields. A value that equals what base already gives is left as base has
// it, so values read with fromSettings and handed back unchanged give
// exactly base again (the fingerprint and the checks depend on that).
func toSettings(base config.Settings, saved game.Saved) config.Settings {
	s := base.Clone()
	cur := serverValues(base)
	for _, k := range serverKeys {
		v, ok := saved.Server[k]
		if !ok || v == cur[k] {
			continue
		}
		setServerValue(&s, k, v)
	}
	if p := saved.Ports["game"]; p != 0 && p != base.Port {
		s.Port = p
	}
	if saved.World != "" {
		s.World = saved.World
	}
	if saved.Worlds != nil {
		next := map[string]config.WorldOptions{}
		for name, v := range saved.Worlds {
			prev := base.WorldOptions[name]
			if maps.Equal(v, worldValues(prev)) {
				if !prev.IsZero() {
					next[name] = prev.Clone()
				}
				continue
			}
			if o := worldOptions(v, prev); !o.IsZero() {
				next[name] = o
			}
		}
		s.WorldOptions = next
		if len(next) == 0 {
			s.WorldOptions = nil
		}
	}
	return s
}

func setServerValue(s *config.Settings, key, v string) {
	num := func(old int) int {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
		return old
	}
	on := game.Values{key: v}.Bool(key)
	switch key {
	case "serverName":
		s.ServerName = v
	case "password":
		s.Password = v
	case "port":
		s.Port = num(s.Port)
	case "public":
		s.Public = on
	case "crossplay":
		s.Crossplay = on
	case "saveIntervalMinutes":
		s.SaveIntervalMinutes = num(s.SaveIntervalMinutes)
	case "backups":
		s.Backups = num(s.Backups)
	case "backupShortMinutes":
		s.BackupShortMinutes = num(s.BackupShortMinutes)
	case "backupLongMinutes":
		s.BackupLongMinutes = num(s.BackupLongMinutes)
	case "updateOnStart":
		s.UpdateOnStart = on
	case "autoRestart":
		s.AutoRestart = on
	case "extraArgs":
		s.ExtraArgs = v
	case "serverDir":
		s.ServerDir = v
	case "saveDir":
		s.SaveDir = v
	}
}

// worldOptions reads a world's values back. Keys come out in Valheim's
// order (config.Keys), and Off lists the keys that were on (or already
// off) before and aren't now, which makes the server start with
// -resetmodifiers, as the Worlds page's difficulty dialog does.
func worldOptions(v game.Values, prev config.WorldOptions) config.WorldOptions {
	var o config.WorldOptions
	o.Preset = v["preset"]
	var extraKeys []string
	for k, val := range v {
		switch {
		case strings.HasPrefix(k, "modifier."):
			if val != "" {
				if o.Modifiers == nil {
					o.Modifiers = map[string]string{}
				}
				o.Modifiers[strings.TrimPrefix(k, "modifier.")] = val
			}
		case strings.HasPrefix(k, "key."):
			if name := strings.TrimPrefix(k, "key."); v.Bool(k) && !slices.Contains(config.Keys, name) {
				extraKeys = append(extraKeys, name)
			}
		}
	}
	for _, k := range config.Keys {
		if v.Bool("key." + k) {
			o.Keys = append(o.Keys, k)
		}
	}
	slices.Sort(extraKeys)
	o.Keys = append(o.Keys, extraKeys...)
	if m := strings.TrimSpace(v["mods"]); m != "" {
		for _, id := range strings.Split(m, ",") {
			if id = strings.TrimSpace(id); id != "" {
				o.Mods = append(o.Mods, id)
			}
		}
	}
	return o.WithOff(prev)
}

// legacyValues is Valheim's game.Store: settings.json through LegacyStore.
type legacyValues struct{ s *Server }

func (l legacyValues) Get() game.Saved { return fromSettings(l.s.store.Settings()) }

func (l legacyValues) Save(change func(*game.Saved) error) error {
	return l.s.store.Update(func(cur *config.Settings) error {
		saved := fromSettings(*cur)
		if err := change(&saved); err != nil {
			return err
		}
		next := toSettings(*cur, saved)
		next.Normalize()
		if errs := next.Validate(); errs != nil {
			return errs
		}
		*cur = next
		return nil
	})
}

// settingsFor is the settings a start or check with saved and world uses.
func (s *Server) settingsFor(saved game.Saved, world string) config.Settings {
	st := toSettings(s.store.Settings(), saved)
	if world != "" {
		st.World = world
	}
	return st
}

// Validate checks the settings as settings.json's own checks do, with the
// same field keys and messages.
func (s *Server) Validate(saved game.Saved, world string) game.FieldErrors {
	errs := s.settingsFor(saved, world).Validate()
	if len(errs) == 0 {
		return nil
	}
	return game.FieldErrors(errs)
}

// Fingerprint identifies what the server would start with, so the panel
// can say when a restart is needed for changes to apply.
func (s *Server) Fingerprint(saved game.Saved, world string) string {
	return ArgsKey(s.settingsFor(saved, world), s.store.Paths().Save)
}

// ArgsKey is the fingerprint of a server started with s and saveDir: its
// command line and the mods it loads. It is runtime.json's argsKey, so it
// must stay exactly what older builds wrote, or every server taken over
// after an update would ask for a restart.
func ArgsKey(s config.Settings, saveDir string) string {
	args, err := s.ServerArgs(saveDir, "")
	if err != nil {
		return ""
	}
	key := strings.Join(args, "\x00")
	if ids := mods.ServerMods(s.WorldMods()); len(ids) > 0 { // players' own mods need no restart
		key += "\x00mods\x00" + strings.Join(slices.Sorted(slices.Values(ids)), "\x00")
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// Schema is Valheim's settings for the new panel's generic form (R3). The
// old panel keeps its own Settings page.
func (s *Server) Schema() game.Schema {
	choices := func(values ...string) []game.Choice {
		out := make([]game.Choice, 0, len(values)+1)
		out = append(out, game.Choice{Value: "", Label: "Default"})
		for _, v := range values {
			out = append(out, game.Choice{Value: v, Label: strings.ToUpper(v[:1]) + v[1:]})
		}
		return out
	}
	sc := game.Schema{
		{Key: "serverName", Scope: game.ServerScope, Kind: "text", Label: "Server name", Add: true, Restart: true, Max: 64},
		{Key: "password", Scope: game.ServerScope, Kind: "password", Label: "Password", Add: true, Restart: true, Min: 5, Max: 64,
			Help: "At least 5 characters, without spaces or quotes. It can't be part of the server name or the world name."},
		{Key: "crossplay", Scope: game.ServerScope, Kind: "bool", Label: "Crossplay (join code)", Default: "true", Restart: true,
			Help: "Friends join with a code, also from Xbox and Game Pass."},
		{Key: "public", Scope: game.ServerScope, Kind: "bool", Label: "Show in the public server list", Restart: true},
		{Key: "port", Scope: game.ServerScope, Kind: "number", Label: "Game port", Default: strconv.Itoa(config.DefaultGamePort),
			Min: 1024, Max: 65534, Restart: true, Advanced: true},
		{Key: "updateOnStart", Scope: game.ServerScope, Kind: "bool", Label: "Update the server when it starts", Default: "true"},
		{Key: "autoRestart", Scope: game.ServerScope, Kind: "bool", Label: "Restart it if it crashes", Default: "true"},
		{Key: "saveIntervalMinutes", Scope: game.ServerScope, Kind: "number", Label: "Save every (minutes)", Default: "20",
			Min: 1, Max: 240, Restart: true, Advanced: true},
		{Key: "backups", Scope: game.ServerScope, Kind: "number", Label: "Automatic backups to keep", Default: "4",
			Min: 0, Max: 50, Restart: true, Advanced: true},
		{Key: "backupShortMinutes", Scope: game.ServerScope, Kind: "number", Label: "First backup after (minutes)", Default: "120",
			Min: 5, Max: 1440, Restart: true, Advanced: true},
		{Key: "backupLongMinutes", Scope: game.ServerScope, Kind: "number", Label: "Then back up every (minutes)", Default: "720",
			Min: 30, Max: 10080, Restart: true, Advanced: true},
		{Key: "extraArgs", Scope: game.ServerScope, Kind: "text", Label: "Extra launch options", Restart: true, Advanced: true, Max: 1000},
		{Key: "serverDir", Scope: game.ServerScope, Kind: "text", Label: "Server folder", Restart: true, Advanced: true},
		{Key: "saveDir", Scope: game.ServerScope, Kind: "text", Label: "Worlds folder", Restart: true, Advanced: true},
		{Key: "preset", Scope: game.WorldScope, Kind: "choice", Label: "Difficulty", Choices: choices(config.Presets...), Restart: true},
	}
	for _, k := range config.ModifierOrder {
		sc = append(sc, game.Field{Key: "modifier." + k, Scope: game.WorldScope, Kind: "choice", Label: k,
			Choices: choices(config.Modifiers[k]...), Restart: true, Advanced: true})
	}
	for _, k := range config.Keys {
		sc = append(sc, game.Field{Key: "key." + k, Scope: game.WorldScope, Kind: "bool", Label: k, Restart: true, Advanced: true})
	}
	return sc
}
