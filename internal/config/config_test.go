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
		Keys:      []string{"nomap", "nobuildcost"},
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
		"-setkey", "nobuildcost", "-setkey", "nomap", "-instanceid", "2", "-something", "with space",
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
	if err != nil || old.Port != DefaultGamePort || old.World != "Midgard" || old.Panel.Port != DefaultPanelPort {
		t.Fatalf("old file: %+v %v", old, err)
	}
	os.WriteFile(path, []byte(`{broken`), 0o644)
	if _, _, err := Load(path); err == nil {
		t.Fatal("damaged file not reported")
	}
}

func TestPanelPassword(t *testing.T) {
	if _, err := HashPanelPassword("short"); err == nil {
		t.Fatal("short password accepted")
	}
	h, err := HashPanelPassword("valhalla!")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPanelPassword(h, "valhalla!") || CheckPanelPassword(h, "Valhalla!") || CheckPanelPassword("garbage", "x") {
		t.Fatal("password check wrong")
	}
	h2, _ := HashPanelPassword("valhalla!")
	if h == h2 {
		t.Fatal("hashes must be salted")
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
