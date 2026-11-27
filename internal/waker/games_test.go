package waker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Config.Games keeps its three meanings through the settings file (spec
// 6.4): absent (an older app: /valheim as always), an empty list (no
// commands at all), and a list of commands.
func TestConfigGames(t *testing.T) {
	base := Config{Token: "token", MAC: "3C:07:54:5A:21:9F"}
	none := []GameCommand{}
	two := []GameCommand{
		{ID: "valheim", Command: "valheim", Description: "Wake the server computer and start Valheim", Name: "Viking Night", Channel: "111", Query: "192.168.1.20:2457"},
		{ID: "minecraft", Command: "minecraft", Description: "Wake the server computer and start Minecraft", Name: "Friends' Minecraft", Channel: "222"},
	}
	for _, c := range []struct {
		what  string
		games *[]GameCommand
		has   string // in the file
		want  int    // commands read back; -1 = absent
	}{
		{"an older app's file", nil, "", -1},
		{"nothing starred", &none, `"games":[]`, 0},
		{"two starred games", &two, `"command":"minecraft"`, 2},
	} {
		cfg := base
		cfg.Games = c.games
		b, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if c.has == "" && strings.Contains(string(b), `"games"`) {
			t.Errorf("%s: written as %s, want no games key", c.what, b)
		}
		if c.has != "" && !strings.Contains(string(b), c.has) {
			t.Errorf("%s: written as %s, want %s in it", c.what, b, c.has)
		}
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case c.want < 0 && got.Games != nil:
			t.Errorf("%s: read back %v, want no list", c.what, *got.Games)
		case c.want >= 0 && (got.Games == nil || len(*got.Games) != c.want):
			t.Errorf("%s: read back %v, want %d commands", c.what, got.Games, c.want)
		}
	}
}
