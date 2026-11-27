package vintagestory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/game/gametest"
	"valheim-easy-server/internal/platform"
)

// withPassword is what Add a game saves: Vintage Story asks for a
// password (Info.Password.Use is "required"), so a server without one never
// starts.
func withPassword(sv *game.Saved) {
	sv.Server["password"] = "secret-word"
}

// TestConformance runs every check each adapter must pass (spec 7.1)
// against the stand-in.
func TestConformance(t *testing.T) {
	gametest.Conformance(t, Game{}, gametest.Options{Change: withPassword})
}

// TestStandInPlayers: the stand-in's join, wrong password and leave lines
// become events, with the player's uid as their account, and the settings
// the app wrote reached the server.
func TestStandInPlayers(t *testing.T) {
	s := gametest.Start(t, Game{}, gametest.Options{Change: func(sv *game.Saved) {
		withPassword(sv)
		sv.Server["serverName"] = "Friends' world"
		sv.Server["maxPlayers"] = "6"
	}, Script: "join:Alex@1,deny:Bob@2,leave:Alex@3", NewWorld: game.Values{"playStyle": "exploration", "seed": "424242"}})
	s.WaitEvent("online", "", 30*time.Second)
	join := s.WaitEvent("join", "Alex", 15*time.Second)
	if join.Account == "" || len(join.Account) != 24 {
		t.Errorf("Alex's join has account %q, want the 24-letter player uid from the identification line", join.Account)
	}
	if e := s.WaitEvent("wrong_password", "Bob", 15*time.Second); !strings.Contains(e.Text, "wrong password") {
		t.Errorf("wrong password text %q", e.Text)
	}
	s.WaitEvent("leave", "Alex", 15*time.Second)

	var cfg map[string]any
	b, err := os.ReadFile(filepath.Join(s.Env().Dirs.Saves, configName))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	wc, _ := cfg["WorldConfig"].(map[string]any)
	for k, want := range map[string]any{"ServerName": "Friends' world", "MaxClients": 6.0, "Password": "secret-word", "WhitelistMode": 1.0, "AdvertiseServer": false} {
		if cfg[k] != want {
			t.Errorf("serverconfig.json %s = %v, want %v", k, cfg[k], want)
		}
	}
	if wc["PlayStyle"] != "exploration" || wc["Seed"] != "424242" || wc["WorldName"] != "Conformance" {
		t.Errorf("serverconfig.json WorldConfig = %v", wc)
	}
	if !strings.HasSuffix(wc["SaveFileLocation"].(string), filepath.Join("Saves", "Conformance.vcdbs")) {
		t.Errorf("SaveFileLocation = %v", wc["SaveFileLocation"])
	}
	s.Stop()
}

// TestStandInPortInUse: with its port taken the server (real and
// stand-in) prints a crash report and exits with code 0; ExitHint names
// the port.
func TestStandInPortInUse(t *testing.T) {
	first := gametest.Start(t, Game{}, gametest.Options{Change: withPassword})
	first.WaitEvent("online", "", 30*time.Second)
	taken := first.Env().Store.Get().Ports["game"]

	second := gametest.Start(t, Game{}, gametest.Options{Change: func(sv *game.Saved) {
		withPassword(sv)
		sv.Ports["game"] = taken
	}})
	deadline := time.Now().Add(30 * time.Second)
	for !exited(second) && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if !exited(second) {
		t.Fatal("the second server on a taken port didn't exit")
	}
	if second.State().Ready {
		t.Error("the second server says it's ready on a taken port")
	}
	tail := readLines(t, second.Plan().LogPath)
	if hint := second.Server().ExitHint(tail); !strings.Contains(hint, "port") {
		t.Errorf("ExitHint = %q, want the port explained\n%s", hint, strings.Join(tail, "\n"))
	}
	first.Stop()
}

func exited(s *gametest.Session) bool {
	p, err := platform.OpenProc(s.Record().PID)
	if err != nil {
		return true
	}
	defer p.Close()
	return p.Exited()
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}
