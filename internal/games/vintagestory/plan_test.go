package vintagestory

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/game/gametest"
	"valheim-easy-server/internal/install"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/golden")

// golden compares got with testdata/golden/name (or rewrites it with -update).
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run the test with -update to write it)", err)
	}
	// A Windows checkout may turn the file's line ends into \r\n.
	want := strings.TrimRight(strings.ReplaceAll(string(b), "\r\n", "\n"), "\r\n")
	if strings.TrimRight(got, "\r\n") != want {
		t.Errorf("%s differs from the golden file:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func testEnv(t *testing.T) game.Env {
	t.Helper()
	root := t.TempDir()
	d := game.Dirs{
		Root: root, Server: filepath.Join(root, "server"), Saves: filepath.Join(root, "saves"),
		Backups: filepath.Join(root, "backups"), Logs: filepath.Join(root, "logs"),
		Runtime: filepath.Join(root, "runtime"), Staging: filepath.Join(root, "uploads"),
	}
	return game.Env{Instance: ID, Data: filepath.Dir(root), Dirs: d}
}

// installed puts a pretend 1.22.7 install in place: the build record, the
// server's dll and runtimeconfig, and a program where the private .NET's
// dotnet would be (the stand-in, when fake is set, so --genconfig works).
func installed(t *testing.T, env game.Env, fake string) *server {
	t.Helper()
	srv := Game{}.New(env).(*server)
	dir := srv.versionDir("1.22.7")
	for _, f := range []string{serverDLL} {
		writeFile(t, filepath.Join(dir, f), "")
	}
	writeFile(t, filepath.Join(dir, "VintagestoryServer.runtimeconfig.json"), readFile(t, filepath.Join("testdata", "runtimeconfig-1.22.7.json")))
	if err := install.WriteBuild(env.Dirs.Server, game.Build{Version: "1.22.7"}); err != nil {
		t.Fatal(err)
	}
	prog := srv.Program()
	if fake != "" {
		b, err := os.ReadFile(fake)
		if err != nil {
			t.Fatal(err)
		}
		os.MkdirAll(filepath.Dir(prog), 0o755)
		if err := os.WriteFile(prog, b, 0o755); err != nil {
			t.Fatal(err)
		}
	} else {
		writeFile(t, prog, "")
	}
	if _, ok := srv.Build(); !ok {
		t.Fatal("the pretend install isn't seen by Build")
	}
	return srv
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func settings(world string) game.Saved {
	return game.Saved{
		Server: game.Values{"serverName": "Friends' Vintage Story", "password": "s3cret&<word>", "maxPlayers": "8", "updateOnStart": "true"},
		World:  world, Ports: map[string]int{"game": 42433}, State: game.Values{},
	}
}

// portableJSON writes the paths in a JSON file the same on every system:
// the instance's folder as <root>, with forward slashes (in JSON a Windows
// backslash is written "\\").
func portableJSON(s, root string) string {
	esc, _ := json.Marshal(root)
	s = strings.ReplaceAll(s, strings.Trim(string(esc), `"`), "<root>")
	return strings.ReplaceAll(s, `\\`, "/")
}

// portable does the same for plain text.
func portable(s, root string) string {
	s = strings.ReplaceAll(s, root, "<root>")
	if runtime.GOOS == "windows" {
		s = strings.ReplaceAll(s, `\`, "/")
	}
	return strings.ReplaceAll(s, "dotnet.exe", "dotnet")
}

func commandLine(p game.Plan, root string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "exe: %s\n", p.Exe)
	for _, a := range p.Args {
		fmt.Fprintf(&b, "arg: %s\n", a)
	}
	fmt.Fprintf(&b, "dir: %s\noutput: %d\nconsole: %v\nsaveLine: %q\nreadyWait: %v\nrecord: server=%q port=%d password=%q\n",
		p.Dir, p.Output, p.Console, p.SaveLine, p.ReadyWait, p.Record.Server, p.Record.Port, p.Record.Password)
	return portable(b.String(), root)
}

// TestPlanNewWorld: the server's own default file (as --genconfig wrote it
// on the real 1.22.7) gets the app's keys for a new world; every other key
// stays as it was, in its place.
func TestPlanNewWorld(t *testing.T) {
	env := testEnv(t)
	srv := installed(t, env, "")
	writeFile(t, filepath.Join(env.Dirs.Saves, configName), readFile(t, filepath.Join("testdata", "serverconfig-genconfig-1.22.7.json")))
	p, err := srv.Plan(t.Context(), game.PlanRequest{World: "Riverbend", New: true, Saved: settings("Riverbend"),
		NewWorld: game.Values{"playStyle": "wildernesssurvival", "seed": "12345"},
		LogPath:  filepath.Join(env.Dirs.Logs, "vintagestory-2026-11-26_21-00-00.log")})
	if err != nil {
		t.Fatal(err)
	}
	got := readFile(t, filepath.Join(env.Dirs.Saves, configName))
	golden(t, "serverconfig-new.json", portableJSON(got, env.Dirs.Root))
	golden(t, "command-line", commandLine(p, env.Dirs.Root))
	if p.Fingerprint != srv.Fingerprint(settings("Riverbend"), "Riverbend") {
		t.Error("the plan's fingerprint differs from Fingerprint's")
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(filepath.Join(env.Dirs.Saves, configName)); st.Mode().Perm() != 0o600 {
			t.Errorf("serverconfig.json (it holds the password) has mode %v, want 0600", st.Mode().Perm())
		}
	}
	// Only the app's keys changed: the rest is byte for byte the server's.
	orig, _ := parseObject([]byte(readFile(t, filepath.Join("testdata", "serverconfig-genconfig-1.22.7.json"))))
	now, err := parseObject([]byte(got))
	if err != nil {
		t.Fatal(err)
	}
	mine := map[string]bool{"ServerName": true, "Port": true, "MaxClients": true, "Password": true, "WhitelistMode": true,
		"OnlyWhitelisted": true, "AdvertiseServer": true, "Upnp": true, "WorldConfig": true}
	if strings.Join(orig.keys, ",") != strings.Join(now.keys, ",") {
		t.Errorf("the keys changed order or number:\n%q\n%q", orig.keys, now.keys)
	}
	for _, k := range orig.keys {
		if !mine[k] && compact(orig.vals[k]) != compact(now.vals[k]) {
			t.Errorf("%s changed: %s -> %s", k, orig.vals[k], now.vals[k])
		}
	}
}

func compact(b []byte) string {
	var o any
	json.Unmarshal(b, &o)
	c, _ := json.Marshal(o)
	return string(c)
}

// TestPlanExistingWorld: a file the server rewrote after its first start
// (with its own server id and the mods it ran), for a world that exists:
// the world's style and seed stay as they are, because the server keeps a
// world's own (FACTS.md).
func TestPlanExistingWorld(t *testing.T) {
	env := testEnv(t)
	srv := installed(t, env, "")
	writeFile(t, filepath.Join(env.Dirs.Saves, configName), readFile(t, filepath.Join("testdata", "serverconfig-first-run-1.22.7.json")))
	writeFile(t, filepath.Join(savesDir(env.Dirs.Saves), "Old Farm.vcdbs"), "world")
	p, err := srv.Plan(t.Context(), game.PlanRequest{World: "Old Farm", Saved: settings("Old Farm"),
		LogPath: filepath.Join(env.Dirs.Logs, "vintagestory-2026-11-26_21-00-00.log")})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "serverconfig-existing.json", portableJSON(readFile(t, filepath.Join(env.Dirs.Saves, configName)), env.Dirs.Root))
	if p.ReadyWait != readyWaitStart {
		t.Errorf("ReadyWait = %v", p.ReadyWait)
	}
}

// TestPlanGenConfig: with no settings file the installed server writes its
// own (here the stand-in, which answers --genconfig as the real server
// does), and a file that can't be used is set aside, never lost.
func TestPlanGenConfig(t *testing.T) {
	env := testEnv(t)
	srv := installed(t, env, gametest.BuildFakeserver(t))
	req := game.PlanRequest{World: "Fresh", New: true, Saved: settings("Fresh"), LogPath: filepath.Join(env.Dirs.Logs, "x.log")}
	if _, err := srv.Plan(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(env.Dirs.Saves, configName)
	if err := usable([]byte(readFile(t, cfgPath))); err != nil {
		t.Fatalf("the generated file: %v", err)
	}
	var c map[string]any
	json.Unmarshal([]byte(readFile(t, cfgPath)), &c)
	if c["Password"] != "s3cret&<word>" || c["WhitelistMode"] != 1.0 || c["Port"] != 42433.0 || c["DefaultRoleCode"] != "suplayer" {
		t.Errorf("generated then set: %v", c)
	}

	// Broken by hand: set aside, a new one made. Each of these stops the
	// real server (or the app) at every start, so keeping it would never
	// work.
	for _, broken := range []string{
		`{"ServerName": "x",,`,
		`{"ServerName": "x", "Port": 1}`,
		`{"Roles": [{"Code": "admin"}], "DefaultRoleCode": "suplayer"}`,
		`{"Roles": [{"Code": "suplayer"}], "WorldConfig": [1]}`,
	} {
		writeFile(t, cfgPath, broken)
		var said []string
		req.Step = func(msg string, _ float64) { said = append(said, msg) }
		if _, err := srv.Plan(t.Context(), req); err != nil {
			t.Fatal(err)
		}
		if err := usable([]byte(readFile(t, cfgPath))); err != nil {
			t.Errorf("after %q: %v", broken, err)
		}
		matches, _ := filepath.Glob(cfgPath + ".damaged-*")
		found := false
		for _, m := range matches {
			if readFile(t, m) == broken {
				found = true
			}
			os.Remove(m)
		}
		if !found {
			t.Errorf("the broken file %q wasn't kept beside the new one", broken)
		}
		if len(said) == 0 || !strings.Contains(said[len(said)-1], "set aside") {
			t.Errorf("the start didn't say the file was set aside: %q", said)
		}
	}
}

func TestPlanRefuses(t *testing.T) {
	env := testEnv(t)
	srv := Game{}.New(env).(*server)
	if _, err := srv.Plan(t.Context(), game.PlanRequest{World: "W", New: true, Saved: settings("W")}); err == nil || !strings.Contains(err.Error(), "isn't installed") {
		t.Errorf("not installed: %v", err)
	}
	srv = installed(t, env, "")
	if _, err := srv.Plan(t.Context(), game.PlanRequest{World: "Gone", Saved: settings("Gone")}); err == nil || !strings.Contains(err.Error(), "isn't in") {
		t.Errorf("a world that isn't there: %v", err)
	}
	sv := settings("W")
	sv.Server["password"] = ""
	_, err := srv.Plan(t.Context(), game.PlanRequest{World: "W", New: true, Saved: sv})
	var fe game.FieldErrors
	if !errors.As(err, &fe) || fe["password"] == "" {
		t.Errorf("no password: %v", err)
	}
	_, err = srv.Plan(t.Context(), game.PlanRequest{World: "W", New: true, Saved: settings("W"), NewWorld: game.Values{"playStyle": "nonsense"}})
	if !errors.As(err, &fe) || fe["playStyle"] == "" {
		t.Errorf("an unknown play style: %v", err)
	}
}

func TestValidate(t *testing.T) {
	srv := Game{}.New(testEnv(t)).(*server)
	for _, c := range []struct {
		key, value, field string
	}{
		{"password", "", "password"},
		{"password", "two words", "password"},
		{"password", "tab\there", "password"},
		{"password", strings.Repeat("p", maxPassword+1), "password"},
		{"serverName", "", "serverName"},
		{"serverName", strings.Repeat("n", maxServerName+1), "serverName"},
		{"serverName", "line\nbreak", "serverName"},
		{"maxPlayers", "0", "maxPlayers"},
		{"maxPlayers", "lots", "maxPlayers"},
		{"maxPlayers", fmt.Sprint(maxSlots + 1), "maxPlayers"},
	} {
		sv := settings("W")
		sv.Server[c.key] = c.value
		if errs := srv.Validate(sv, "W"); errs[c.field] == "" {
			t.Errorf("%s=%q: no error for %s (%v)", c.key, c.value, c.field, errs)
		}
	}
	if errs := srv.Validate(settings("W"), "W"); len(errs) != 0 {
		t.Errorf("good settings: %v", errs)
	}
	if errs := srv.Validate(settings("CON"), "CON"); errs["world"] == "" {
		t.Errorf("a world name Windows can't use: %v", errs)
	}
}

func TestFingerprint(t *testing.T) {
	srv := Game{}.New(testEnv(t)).(*server)
	base := srv.Fingerprint(settings("W"), "W")
	for key, v := range map[string]string{"password": "other", "serverName": "Other", "maxPlayers": "9"} {
		sv := settings("W")
		sv.Server[key] = v
		if srv.Fingerprint(sv, "W") == base {
			t.Errorf("changing %s doesn't change the fingerprint", key)
		}
	}
	sv := settings("W")
	sv.Ports["game"] = 42500
	if srv.Fingerprint(sv, "W") == base || srv.Fingerprint(settings("W"), "Other") == base {
		t.Error("the port or the world doesn't change the fingerprint")
	}
	sv = settings("W")
	sv.Server["updateOnStart"] = "false"
	sv.Server["autoRestart"] = "false"
	if srv.Fingerprint(sv, "W") != base {
		t.Error("settings that need no restart change the fingerprint")
	}
}
