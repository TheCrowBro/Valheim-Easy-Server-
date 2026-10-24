package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultsAreValid(t *testing.T) {
	for i := 0; i < 50; i++ { // the password is random
		if errs := Defaults().Validate(); errs != nil {
			t.Fatalf("defaults invalid: %v", errs)
		}
	}
}

func TestValidation(t *testing.T) {
	cases := []struct {
		field  string
		change func(*Settings)
	}{
		{"password", func(s *Settings) { s.Password = "abc" }},
		{"password", func(s *Settings) { s.ServerName, s.Password = "Odins Hall", "odins" }},
		{"password", func(s *Settings) { s.ServerName, s.Password = "Mead Hall", "mead hall" }},
		{"password", func(s *Settings) { s.World, s.Password = "Asgard2024", "asgard" }},
		{"password", func(s *Settings) { s.Password = `has"quote` }},
		{"password", func(s *Settings) { s.Password = "has space" }},
		{"serverName", func(s *Settings) { s.ServerName = "" }},
		{"serverName", func(s *Settings) { s.ServerName = `Bad "name"` }},
		{"world", func(s *Settings) { s.World = "bad/name" }},
		{"world", func(s *Settings) { s.World = "CON" }},
		{"world", func(s *Settings) { s.World = "Midgard_backup_auto-1" }},
		{"port", func(s *Settings) { s.Port = 80 }},
		{"idleMinutes", func(s *Settings) { s.IdleMinutes = 1 }},
		{"extraArgs", func(s *Settings) { s.ExtraArgs = `-x "unclosed` }},
		{"worldOptions", func(s *Settings) {
			s.WorldOptions = map[string]WorldOptions{s.World: {Modifiers: map[string]string{"combat": "impossible"}}}
		}},
		{"worldOptions", func(s *Settings) { s.WorldOptions = map[string]WorldOptions{s.World: {Mods: []string{"epicloot"}}} }},
	}
	for _, c := range cases {
		s := Defaults()
		s.Password = "fjord-1234"
		c.change(&s)
		errs := s.Validate()
		if _, ok := errs[c.field]; !ok {
			t.Errorf("expected a %s error for %+v, got %v", c.field, s, errs)
		}
	}
}

func TestServerArgs(t *testing.T) {
	s := Defaults()
	s.ServerName, s.Password, s.World = "Viking Friends", "fjord-1234", "My World"
	s.Public, s.Crossplay = true, true
	s.SaveIntervalMinutes, s.Backups, s.BackupShortMinutes, s.BackupLongMinutes = 10, 3, 60, 360
	s.WorldOptions = map[string]WorldOptions{"My World": {
		Preset:    "hard",
		Modifiers: map[string]string{"raids": "none", "combat": "veryhard"},
		Keys:      []string{"fire", "nomap", "nobuildcost"},
	}}
	s.ExtraArgs = `-instanceid 2 -something "with space"`
	args, err := s.ServerArgs(`C:\VES\saves`, `C:\VES\logs\server.log`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-nographics", "-batchmode", "-name", "Viking Friends", "-port", "2456", "-world", "My World",
		"-password", "fjord-1234", "-public", "1", "-savedir", `C:\VES\saves`, "-logFile", `C:\VES\logs\server.log`,
		"-saveinterval", "600", "-backups", "3", "-backupshort", "3600", "-backuplong", "21600", "-crossplay",
		"-preset", "hard", "-modifier", "combat", "veryhard", "-modifier", "raids", "none",
		"-setkey", "nobuildcost", "-setkey", "nomap", "-setkey", "fire", "-instanceid", "2", "-something", "with space",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args\n got  %q\n want %q", args, want)
	}
	// Options of other worlds are not used.
	s.World = "Other"
	args, _ = s.ServerArgs("x", "y")
	if strings.Contains(strings.Join(args, " "), "-preset") {
		t.Fatalf("options leaked to another world: %v", args)
	}
}

func TestWorldOptionsOff(t *testing.T) {
	on := WorldOptions{Keys: []string{"fire", "nomap"}}
	off := WorldOptions{Keys: []string{"nomap"}}.WithOff(on)
	if !reflect.DeepEqual(off.Off, []string{"fire"}) {
		t.Fatalf("unticking fire: Off = %v", off.Off)
	}
	// Keys stay off through later changes, and come back when ticked again.
	later := WorldOptions{Modifiers: map[string]string{"combat": "hard"}}.WithOff(off)
	if !reflect.DeepEqual(later.Off, []string{"nomap", "fire"}) {
		t.Fatalf("after another change: Off = %v", later.Off)
	}
	back := WorldOptions{Keys: []string{"fire"}}.WithOff(later)
	if !reflect.DeepEqual(back.Off, []string{"nomap"}) {
		t.Fatalf("ticking fire again: Off = %v", back.Off)
	}
	// Only keys turned off are kept, so the options aren't empty.
	if (WorldOptions{Keys: []string{"fire"}}).WithOff(WorldOptions{}).Off != nil || (WorldOptions{}.WithOff(on)).IsZero() {
		t.Fatal("WithOff kept too much or too little")
	}

	s := Defaults()
	s.World = "Ember"
	s.WorldOptions = map[string]WorldOptions{"Ember": back}
	args, err := s.ServerArgs("x", "y")
	if err != nil {
		t.Fatal(err)
	}
	// Valheim keeps keys, so a world with one turned off is reset first.
	if got := strings.Join(args, " "); !strings.HasSuffix(got, "-crossplay -resetmodifiers -setkey fire") {
		t.Fatalf("args = %s", got)
	}
	s.WorldOptions = map[string]WorldOptions{"Ember": {Keys: []string{"fire"}}}
	if args, _ := s.ServerArgs("x", "y"); strings.Contains(strings.Join(args, " "), "-resetmodifiers") {
		t.Fatalf("reset without a key turned off: %v", args)
	}
	if (WorldOptions{Off: []string{"nope"}}).Problem() == "" {
		t.Fatal("an unknown key in Off was accepted")
	}
}

func TestLoadMovesModsToTheSelectedWorld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	old := `{"serverName":"Viking Night","password":"fjord-1234","world":"Midgard","port":2456,
		"worldOptions":{"Midgard":{"keys":["fire"]}},"mods":["advancedportals"]}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	o := s.WorldOptions["Midgard"]
	if s.Mods != nil || !reflect.DeepEqual(o.Mods, []string{"advancedportals"}) || !reflect.DeepEqual(o.Keys, []string{"fire"}) {
		t.Fatalf("after load: mods %v, Midgard %+v", s.Mods, o)
	}
	if !reflect.DeepEqual(s.WorldMods(), []string{"advancedportals"}) {
		t.Fatalf("WorldMods = %v", s.WorldMods())
	}
	if s.Discord || !s.DiscordStopped {
		t.Fatalf("an older file should get the Discord defaults: %+v", s)
	}
}

func TestLoadDropsModsNoLongerOffered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	old := `{"serverName":"Viking Night","password":"fjord-1234","world":"Midgard","port":2456,
		"worldOptions":{"Midgard":{"mods":["advancedportals","fueleternal"]},"Asgard":{"mods":["azuepi"]}}}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.WorldOptions["Midgard"].Mods, []string{"advancedportals"}) || len(s.WorldOptions["Asgard"].Mods) != 0 {
		t.Fatalf("world options = %+v", s.WorldOptions)
	}
	if errs := s.Validate(); errs != nil {
		t.Fatalf("settings with a removed mod don't pass: %v", errs)
	}
}

func TestDiscordSettings(t *testing.T) {
	s := Defaults()
	s.DiscordWebhook = "not a link" // kept while posting is off
	if errs := s.Validate(); errs != nil {
		t.Fatalf("off: %v", errs)
	}
	s.Discord = true
	if errs := s.Validate(); errs["discordWebhook"] == "" {
		t.Fatalf("on with a bad link: %v", errs)
	}
	s.DiscordWebhook = "  https://discord.com/api/webhooks/1/abc  "
	s.Normalize()
	if errs := s.Validate(); errs != nil || s.DiscordWebhook != "https://discord.com/api/webhooks/1/abc" {
		t.Fatalf("on with a good link: %v %q", errs, s.DiscordWebhook)
	}
}

func TestLoadSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	s, existed, err := Load(path)
	if err != nil || existed {
		t.Fatalf("missing file: existed=%v err=%v", existed, err)
	}
	s.ServerName = "Saved Server"
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	back, existed, err := Load(path)
	if err != nil || !existed || back.ServerName != "Saved Server" || back.Password != s.Password {
		t.Fatalf("round trip: %+v %v %v", back, existed, err)
	}
	// Missing fields in an old file get defaults.
	os.WriteFile(path, []byte(`{"serverName":"Old","password":"oldpass1"}`), 0o644)
	old, _, err := Load(path)
	if err != nil || old.Port != DefaultGamePort || old.World != "Midgard" {
		t.Fatalf("old file: %+v %v", old, err)
	}
	os.WriteFile(path, []byte(`{broken`), 0o644)
	if _, _, err := Load(path); err == nil {
		t.Fatal("damaged file not reported")
	}
}

func TestLists(t *testing.T) {
	dir := t.TempDir()
	if ids, err := ReadList(dir, Admins); err != nil || len(ids) != 0 {
		t.Fatalf("missing list: %v %v", ids, err)
	}
	if err := WriteList(dir, Admins, []string{" Steam_1 ", "Steam_1", "V_22", ""}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "adminlist.txt"))
	if !strings.HasPrefix(string(b), "// List admin players") {
		t.Fatalf("file = %q", b)
	}
	ids, _ := ReadList(dir, Admins)
	if !reflect.DeepEqual(ids, []string{"Steam_1", "V_22"}) {
		t.Fatalf("ids = %v", ids)
	}
	if err := WriteList(dir, Banned, []string{"not an id!"}); err == nil {
		t.Fatal("bad id accepted")
	}
}

func TestSplitArgs(t *testing.T) {
	got, err := SplitArgs(`  -a "b c"  d""e  `)
	if err != nil || !reflect.DeepEqual(got, []string{"-a", "b c", "de"}) {
		t.Fatalf("SplitArgs = %q %v", got, err)
	}
	if got, _ := SplitArgs(`-path C:\Games\x`); !reflect.DeepEqual(got, []string{"-path", `C:\Games\x`}) {
		t.Fatalf("backslashes changed: %q", got)
	}
}
