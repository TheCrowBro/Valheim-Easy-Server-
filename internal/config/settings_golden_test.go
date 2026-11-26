package config

// settings.json is frozen (docs/dev/multigame/spec.md I3): no key is
// added, removed, renamed or given a new meaning, because every copy of the
// app, newer or older, reads and writes the same file. New settings go in
// files of their own. These tests hold today's keys, types and defaults.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"testing"
)

// settingsKeys is every key settings.json can hold, with its JSON type.
var settingsKeys = map[string]string{
	"serverName":          "string",
	"password":            "string",
	"world":               "string",
	"port":                "number",
	"public":              "bool",
	"crossplay":           "bool",
	"saveIntervalMinutes": "number",
	"backups":             "number",
	"backupShortMinutes":  "number",
	"backupLongMinutes":   "number",
	"updateOnStart":       "bool",
	"autoUpdate":          "bool",
	"autoRestart":         "bool",
	"startWithApp":        "bool",
	"startAfterWake":      "bool",
	"idleHibernate":       "bool",
	"idleMinutes":         "number",
	"restMode":            "string",
	"discord":             "bool",
	"discordWebhook":      "string",
	"discordPassword":     "bool",
	"discordStopped":      "bool",
	"discordDaily":        "bool",
	"discordBotToken":     "string",
	"jetkvm":              "string",
	"discordWake":         "bool",
	"worldOptions":        "object",
	"mods":                "array",
	"serverDir":           "string",
	"saveDir":             "string",
	"extraArgs":           "string",
}

// worldOptionsKeys is what each world's entry in worldOptions holds.
var worldOptionsKeys = map[string]string{
	"preset":    "string",
	"modifiers": "object",
	"keys":      "array",
	"off":       "array",
	"mods":      "array",
}

// fullSettings has every field set, so every key is written.
func fullSettings() Settings {
	return Settings{
		ServerName: "Viking Night", Password: "raven-fjord-4821", World: "Midgard", Port: 2456,
		Public: true, Crossplay: true, SaveIntervalMinutes: 15, Backups: 4, BackupShortMinutes: 120, BackupLongMinutes: 720,
		UpdateOnStart: true, AutoUpdate: true, AutoRestart: true, StartWithApp: true, StartAfterWake: true,
		IdleHibernate: true, IdleMinutes: 60, RestMode: RestSleep,
		Discord: true, DiscordWebhook: "https://discord.com/api/webhooks/123/abc", DiscordPassword: true,
		DiscordStopped: true, DiscordDaily: true,
		DiscordBotToken: "bot-token", JetKVM: "192.168.1.30", DiscordWake: true,
		WorldOptions: map[string]WorldOptions{"Midgard": {
			Preset: "hard", Modifiers: map[string]string{"combat": "hard"}, Keys: []string{"nomap"},
			Off: []string{"fire"}, Mods: []string{"planteverything"},
		}},
		Mods:      []string{"riverheim"},
		ServerDir: "/srv/valheim/server", SaveDir: "/srv/valheim/saves", ExtraArgs: "-instanceid 2",
	}
}

func TestSettingsShapeFrozen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := Save(path, fullSettings()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]any
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}
	sameKeys(t, "settings.json", file, settingsKeys)
	world, _ := file["worldOptions"].(map[string]any)["Midgard"].(map[string]any)
	sameKeys(t, "settings.json worldOptions", world, worldOptionsKeys)

	// Defaults are what a first start writes, and what fills in a value an
	// old or hand-edited file leaves out.
	d := Defaults()
	if !regexp.MustCompile(`^[a-z]+-[a-z]+-\d{4}$`).MatchString(d.Password) {
		t.Errorf("default password %q isn't two words and a number", d.Password)
	}
	d.Password = ""
	want := Settings{
		ServerName: "Valheim with Friends", World: "Midgard", Port: 2456, Crossplay: true,
		SaveIntervalMinutes: 15, Backups: 4, BackupShortMinutes: 120, BackupLongMinutes: 720,
		UpdateOnStart: true, AutoUpdate: true, AutoRestart: true, StartAfterWake: true,
		IdleMinutes: 60, RestMode: "sleep", DiscordStopped: true, DiscordDaily: true,
	}
	if !reflect.DeepEqual(d, want) {
		t.Errorf("defaults = %+v\nwant %+v", d, want)
	}

	// A settings.json as today's app writes it reads back the same (the
	// top-level mods move to the selected world, as they always have).
	if err := os.WriteFile(path, []byte(bed004bSettings), 0o600); err != nil {
		t.Fatal(err)
	}
	got, existed, err := Load(path)
	wantLoaded := fullSettings()
	wantLoaded.Mods = nil
	o := wantLoaded.WorldOptions["Midgard"]
	o.Mods = append(o.Mods, "riverheim")
	wantLoaded.WorldOptions["Midgard"] = o
	if err != nil || !existed || !reflect.DeepEqual(got, wantLoaded) {
		t.Errorf("today's settings.json reads as %+v (%v)\nwant %+v", got, err, wantLoaded)
	}
}

// bed004bSettings is a settings.json written by today's app, with every key.
const bed004bSettings = `{
  "serverName": "Viking Night",
  "password": "raven-fjord-4821",
  "world": "Midgard",
  "port": 2456,
  "public": true,
  "crossplay": true,
  "saveIntervalMinutes": 15,
  "backups": 4,
  "backupShortMinutes": 120,
  "backupLongMinutes": 720,
  "updateOnStart": true,
  "autoUpdate": true,
  "autoRestart": true,
  "startWithApp": true,
  "startAfterWake": true,
  "idleHibernate": true,
  "idleMinutes": 60,
  "restMode": "sleep",
  "discord": true,
  "discordWebhook": "https://discord.com/api/webhooks/123/abc",
  "discordPassword": true,
  "discordStopped": true,
  "discordDaily": true,
  "discordBotToken": "bot-token",
  "jetkvm": "192.168.1.30",
  "discordWake": true,
  "worldOptions": {
    "Midgard": {
      "preset": "hard",
      "modifiers": {
        "combat": "hard"
      },
      "keys": [
        "nomap"
      ],
      "off": [
        "fire"
      ],
      "mods": [
        "planteverything"
      ]
    }
  },
  "mods": [
    "riverheim"
  ],
  "serverDir": "/srv/valheim/server",
  "saveDir": "/srv/valheim/saves",
  "extraArgs": "-instanceid 2"
}
`

// sameKeys checks that obj has exactly the keys in want, each with its
// JSON type.
func sameKeys(t *testing.T, what string, obj map[string]any, want map[string]string) {
	t.Helper()
	var keys []string
	for k := range obj {
		keys = append(keys, k)
	}
	for k := range want {
		if _, ok := obj[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		v, have := obj[k]
		kind, known := want[k]
		switch {
		case !known:
			t.Errorf("%s has a new key %q: settings.json is frozen, so new settings go in a file of their own", what, k)
		case !have:
			t.Errorf("%s has no %q any more: every copy of the app reads it", what, k)
		case jsonKind(v) != kind:
			t.Errorf("%s: %q is a %s, want a %s", what, k, jsonKind(v), kind)
		}
	}
}

func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	}
	return "object"
}
