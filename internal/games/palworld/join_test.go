package palworld

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/install"
)

// Friends type the address and port; at home the LAN addresses work; the
// running server's port and password win over settings waiting for a
// restart; the version is named once the server has said it.
func TestJoin(t *testing.T) {
	s := testServer(t)
	sv := defaultSaved(s, "Island")
	sv.Server["password"] = "new one"
	sv.Ports["game"] = 8300 // changed, waiting for a restart
	rec := game.Record{Port: 8211, Password: "old one"}
	st := game.RunState{Version: "v1.0.5.102999"}
	j := s.Join(game.JoinInput{Rec: &rec, State: &st, Saved: sv, PublicIP: "203.0.113.7", LAN: []string{"192.168.1.20"}, Router: map[string]string{"game": "open"}})
	if j.How != "address" || j.Address != "203.0.113.7:8211" || j.Port != 8211 || j.Password != "old one" || !j.Router || j.Public || j.Crossplay {
		t.Errorf("Join = %+v", j)
	}
	if !slices.Equal(j.LAN, []string{"192.168.1.20:8211"}) {
		t.Errorf("LAN = %q", j.LAN)
	}
	steps := strings.Join(j.Steps, " | ")
	for _, want := range []string{"Join Multiplayer Game", "203.0.113.7:8211", "password", "v1.0.5.102999"} {
		if !strings.Contains(steps, want) {
			t.Errorf("steps %q lack %q", steps, want)
		}
	}
	if j.Problem != "" || !strings.Contains(j.Hint, "203.0.113.7:8211") {
		t.Errorf("hint %q, problem %q", j.Hint, j.Problem)
	}

	// Stopped, no public address known, the router failed, IPv6 at home.
	sv.Server["password"] = ""
	j = s.Join(game.JoinInput{Saved: sv, LAN: []string{"fd00::5"}, Router: map[string]string{"game": "failed"}})
	if j.Address != "" || j.Port != 8300 || j.Password != "" || !slices.Equal(j.LAN, []string{"[fd00::5]:8300"}) || !strings.Contains(j.Problem, "UDP port 8300") {
		t.Errorf("stopped Join = %+v", j)
	}
	if steps := strings.Join(j.Steps, " "); strings.Contains(steps, "password") || !strings.Contains(steps, "Update Palworld") {
		t.Errorf("stopped steps = %q", j.Steps)
	}
}

// What the panel checks before the server sees the settings.
func TestValidate(t *testing.T) {
	s := testServer(t)
	good := defaultSaved(s, "Island")
	if errs := s.Validate(good, "Island"); len(errs) > 0 {
		t.Fatalf("the defaults: %v", errs)
	}
	for _, c := range []struct {
		key, value, field string
	}{
		{"serverName", strings.Repeat("a", 65), "serverName"},
		{"serverName", " ", "serverName"},
		{"serverName", `My "best" server`, "serverName"},
		{"serverName", `C:\server`, "serverName"},
		{"serverName", "two\nlines", "serverName"},
		{"password", `pa"ss`, "password"},
		{"password", " padded", "password"},
		{"password", strings.Repeat("p", 65), "password"},
		{"maxPlayers", "33", "maxPlayers"},
		{"maxPlayers", "0", "maxPlayers"},
		{"maxPlayers", "lots", "maxPlayers"},
	} {
		sv := good.Clone()
		sv.Server[c.key] = c.value
		if errs := s.Validate(sv, "Island"); errs[c.field] == "" {
			t.Errorf("%s = %q was taken (%v)", c.key, c.value, errs)
		}
	}
	sv := good.Clone()
	sv.Server["serverName"] = "Pals, (and friends) 日本語"
	sv.Server["password"] = "two words, ok"
	if errs := s.Validate(sv, "Island"); len(errs) > 0 {
		t.Errorf("commas, brackets and other scripts are fine in quoted text: %v", errs)
	}
	sv = good.Clone()
	sv.Worlds["Island"]["expRate"] = "100"
	if errs := s.Validate(sv, "Island"); errs["expRate"] == "" {
		t.Errorf("an experience rate that isn't a choice was taken")
	}
	if errs := s.Validate(good, idA); errs["world"] == "" {
		t.Errorf("a new world named like an id was taken")
	}
}

// Every world option is a choice whose values the file can hold, and the
// schema's world fields all apply at the next start.
func TestSchema(t *testing.T) {
	s := testServer(t)
	keys := map[string]bool{}
	for _, f := range s.Schema() {
		keys[string(f.Scope)+"."+f.Key] = true
		if f.Scope == game.WorldScope && !f.Restart {
			t.Errorf("%s isn't marked Restart", f.Key)
		}
	}
	for _, k := range []string{"server.serverName", "server.password", "server.maxPlayers", "server.updateOnStart", "server.autoRestart", "world.deathPenalty", "world.expRate"} {
		if !keys[k] {
			t.Errorf("no %s", k)
		}
	}
	opts, _ := parseINI(string(defaultSettings)).optionsLine()
	for _, o := range worldOptions {
		if _, ok := opts.get(o.ini); !ok {
			t.Errorf("%s isn't a key of the 1.0.5 DefaultPalWorldSettings.ini", o.ini)
		}
	}
	if got := rateText("1.5"); got != "1.500000" {
		t.Errorf("rateText = %q", got)
	}
}

// The install: on Windows the Visual C++ runtime first (so a "No" stops it
// before the big download), then SteamCMD; a repair always runs the
// runtime's installer.
func TestInstallOrder(t *testing.T) {
	old, oldRuntime := needsVCRuntime, vcRuntime
	needsVCRuntime = true
	vcRuntime.Installed = func() bool { return true } // whatever this computer has
	defer func() { needsVCRuntime, vcRuntime = old, oldRuntime }()

	var calls []string
	steam := installFunc(func(ctx context.Context, repair bool, _ func(game.Progress)) error {
		calls = append(calls, "steam repair="+boolText(repair))
		return nil
	})
	prereq := func(_ context.Context, p install.Prereq, _ func(game.Progress)) error {
		calls = append(calls, "prereq checked="+boolText(p.Installed != nil))
		if !strings.HasPrefix(p.URL, "https://") || !slices.Equal(p.Args, []string{"/install", "/quiet", "/norestart"}) {
			t.Errorf("prereq = %+v", p)
		}
		// The shared messages start sentences with the name and put "The"
		// before it, so it must be a capitalised name with no article.
		if first, _ := utf8.DecodeRuneInString(p.Name); !unicode.IsUpper(first) || strings.HasPrefix(strings.ToLower(p.Name), "the ") {
			t.Errorf("prereq name %q: the owner would see \"The %s installer failed\"", p.Name, p.Name)
		}
		return nil
	}
	in := &realInstaller{prereq: prereq, steam: steam}
	if err := in.Install(context.Background(), false, nil); err != nil {
		t.Fatal(err)
	}
	if err := in.Install(context.Background(), true, nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"prereq checked=True", "steam repair=False", "prereq checked=False", "steam repair=True"}
	if !slices.Equal(calls, want) {
		t.Errorf("calls = %q, want %q", calls, want)
	}

	calls = nil
	in.prereq = func(context.Context, install.Prereq, func(game.Progress)) error { return errors.New("declined") }
	if err := in.Install(context.Background(), false, nil); err == nil || len(calls) != 0 {
		t.Errorf("a refused runtime: %v, calls %q", err, calls)
	}
}

type installFunc func(ctx context.Context, repair bool, progress func(game.Progress)) error

func (f installFunc) Install(ctx context.Context, repair bool, progress func(game.Progress)) error {
	return f(ctx, repair, progress)
}

// Exit hints from the end of a log.
func TestExitHint(t *testing.T) {
	s := testServer(t)
	for _, c := range []struct {
		tail []string
		want string
	}{
		{[]string{"Game version is v1.0.5.102999", "Failed to bind UDP port 8211: listen udp 0.0.0.0:8211: bind: address already in use"}, "ports"},
		{[]string{"Game version is v1.0.5.102999", "LowLevelFatalError [File:Unknown] [Line: 0] Ran out of memory"}, "memory"},
		{[]string{"Game version is v1.0.5.102999", "Running Palworld dedicated server on :8211", "LowLevelFatalError [File:Unknown] [Line: 0] oops"}, "crashed"},
		{nil, "Visual C++"},
		{[]string{"Setting breakpad minidump AppID = 2394010"}, "Visual C++"},
		{[]string{"Game version is v1.0.5.102999", "Running Palworld dedicated server on :8211"}, ""},
	} {
		got := s.ExitHint(c.tail)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("ExitHint(%q) = %q, want one about %q", c.tail, got, c.want)
		}
	}
}
