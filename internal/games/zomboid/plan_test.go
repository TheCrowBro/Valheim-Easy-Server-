package zomboid

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
)

// go test ./internal/games/zomboid/ -run Golden -update writes the golden
// files again from the code; check the diff before committing them.
var update = flag.Bool("update", false, "rewrite the golden files")

// crlf is text with every line ending in CRLF, whatever it had. Git may
// turn the testdata's line endings either way on checkout (Windows CI
// checks out with core.autocrlf), so files are compared by their lines.
func crlf(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
}

func lf(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

// golden compares got with testdata/golden/<name>, line by line.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run the test with -update to write it)", err)
	}
	if lf(got) != lf(string(want)) {
		t.Errorf("%s differs from what the code makes now (-update rewrites it):\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

// The launch files in testdata are the real Build 42 ones, byte for byte:
// their SHA-256 sums are the ones recorded on a real Windows install
// (FACTS.md).
func TestLaunchFilesAreTheRecordedOnes(t *testing.T) {
	for name, want := range map[string]string{
		"launch/StartServer64.bat":     "e9d76cadbb74729100808cd6d8a6af1d2a499a774054191c5af515ffd992af82",
		"launch/ProjectZomboid64.json": "403263cf4f15461470bb1d36a76a6665c4b436ef35746bcb8d91f970a36eca52",
	} {
		sum := sha256.Sum256([]byte(crlf(string(readTestdata(t, name)))))
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("%s: SHA-256 %s, want %s", name, got, want)
		}
	}
}

// The real Build 42 StartServer64.bat, read as cmd.exe would run it.
func TestParseBatB42(t *testing.T) {
	l, err := parseBat(string(readTestdata(t, "launch/StartServer64.bat")))
	if err != nil {
		t.Fatal(err)
	}
	wantJVM := []string{"-Djava.awt.headless=true", "-Dzomboid.steam=1", "-Dzomboid.znetlog=1", "-XX:+UseZGC",
		"-XX:-CreateCoredumpOnCrash", "-XX:-OmitStackTraceInFastThrow", "-Djava.library.path=natives/"}
	if !slices.Equal(l.JVM, wantJVM) {
		t.Errorf("JVM options = %q, want %q (without -Xms16g -Xmx16g)", l.JVM, wantJVM)
	}
	if l.ClassPath != "java/;java/projectzomboid.jar" || l.Main != mainClass || !slices.Equal(l.Game, []string{"-statistic", "0"}) {
		t.Errorf("class path %q, main %q, game args %q", l.ClassPath, l.Main, l.Game)
	}
	if !slices.Equal(l.Env, []string{"PZ_CLASSPATH=java/;java/projectzomboid.jar"}) {
		t.Errorf("environment %q, want the .bat's SET", l.Env)
	}
	// The same file with LF line endings and a BOM, as an editor might
	// leave it, reads the same.
	l2, err := parseBat("\ufeff" + lf(string(readTestdata(t, "launch/StartServer64.bat"))))
	if err != nil || !slices.Equal(l2.JVM, l.JVM) || l2.ClassPath != l.ClassPath {
		t.Errorf("with LF and a BOM: %+v, %v", l2, err)
	}
}

func TestParseBatOtherForms(t *testing.T) {
	for _, c := range []struct {
		name, bat string
		cp        string
		jvm, game []string
		err       string
	}{
		{name: "quoted SET, -classpath, a variable inside another",
			bat: "@echo off\r\nset \"JARS=java/projectzomboid.jar\"\r\nSET PZ_CLASSPATH=java/;%JARS%\r\n" +
				`"%~dp0jre64\bin\java.exe" -Xms2g -XX:+UseZGC "-Dname=with space" -classpath "%PZ_CLASSPATH%" zombie.network.GameServer %*` + "\r\n",
			cp: "java/;java/projectzomboid.jar", jvm: []string{"-XX:+UseZGC", "-Dname=with space"}},
		{name: "a comment and the nosteam launcher's game arguments",
			bat: "rem Project Zomboid\r\n:: start\r\njre64\\bin\\java -cp java/ zombie.network.GameServer -nosteam %1\r\n",
			cp:  "java/", game: []string{"-nosteam"}},
		{name: "the .bat's own folder, %~dp0, stands for the server folder",
			bat: "@cd /d \"%~dp0\"\r\nSET PZ_CLASSPATH=%~DP0java/;%~dp0java/projectzomboid.jar\r\n" +
				`"%~dp0jre64\bin\java.exe" -Djava.library.path=%~dp0natives/ -cp %PZ_CLASSPATH% zombie.network.GameServer -statistic 0 %1 %2` + "\r\n",
			cp: `.\java/;.\java/projectzomboid.jar`, jvm: []string{`-Djava.library.path=.\natives/`}, game: []string{"-statistic", "0"}},
		{name: "another part of the .bat's own path", bat: `java.exe -Dself=%~f0 -cp java/ zombie.network.GameServer`, err: "doesn't set"},
		{name: "no Java line", bat: "@echo hello\r\nPAUSE\r\n", err: "no line runs"},
		{name: "a variable the .bat never sets", bat: `java.exe -cp %MISSING% zombie.network.GameServer`, err: "doesn't set"},
		{name: "no class path", bat: `java.exe -Xmx1g zombie.network.GameServer`, err: "no class path"},
		{name: "not Java", bat: `start.exe -cp x zombie.network.GameServer`, err: "not Java"},
	} {
		t.Run(c.name, func(t *testing.T) {
			l, err := parseBat(c.bat)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Errorf("error %v, want one saying %q", err, c.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if l.ClassPath != c.cp || !slices.Equal(l.JVM, c.jvm) || !slices.Equal(l.Game, c.game) {
				t.Errorf("got %+v", l)
			}
		})
	}
}

// Every SET before the Java line goes into the plan's environment, in the
// order first set, with its last value; "SET NAME=" adds nothing; a SET
// after the Java line never reached it.
func TestParseBatEnvironment(t *testing.T) {
	l, err := parseBat("set _JAVA_OPTIONS=-Dfoo=1\r\nSET \"JARS=java/a.jar\"\r\nset EMPTY=\r\nset _java_options=-Dfoo=2\r\n" +
		"java -cp %JARS% zombie.network.GameServer\r\nset LATER=x\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"_JAVA_OPTIONS=-Dfoo=2", "JARS=java/a.jar"}; !slices.Equal(l.Env, want) {
		t.Errorf("environment %q, want %q", l.Env, want)
	}
}

// ProjectZomboid64.json, the fallback: on Windows with the options for
// Windows 10, the class path joined with ";"; on Linux with ":".
func TestParseLaunchJSON(t *testing.T) {
	l, err := parseLaunchJSON(readTestdata(t, "launch/ProjectZomboid64.json"), "windows")
	if err != nil {
		t.Fatal(err)
	}
	if l.ClassPath != "java/;java/projectzomboid.jar" || l.Main != mainClass || len(l.Game) != 0 {
		t.Errorf("windows: %+v", l)
	}
	if !slices.Contains(l.JVM, "-XX:+UseZGC") || slices.Contains(l.JVM, "-XX:+UseG1GC") || slices.ContainsFunc(l.JVM, dropJVM) {
		t.Errorf("windows options %q: want Windows 10's -XX:+UseZGC and no heap sizes", l.JVM)
	}
	l, err = parseLaunchJSON(readTestdata(t, "launch/ProjectZomboid64-linux.json"), "linux")
	if err != nil || l.ClassPath != "java/:java/projectzomboid.jar" || !slices.Contains(l.JVM, "-Djava.library.path=linux64/") {
		t.Errorf("linux: %+v, %v", l, err)
	}
	if _, err := parseLaunchJSON([]byte(`{"mainClass": "zombie/gameStates/MainScreenState", "classpath": ["java/"]}`), "windows"); err == nil {
		t.Errorf("the game's own launcher settings (another main class) were taken for the server's")
	}
}

// Windows reads the .bat first, Linux the .json; either does when the
// other is missing or broken, and neither gives the spec's message.
func TestReadLaunch(t *testing.T) {
	dir := t.TempDir()
	if _, err := readLaunch(dir, "windows"); !errors.Is(err, errNoLaunch) {
		t.Errorf("with no launch files: %v", err)
	}
	os.WriteFile(filepath.Join(dir, jsonName), readTestdata(t, "launch/ProjectZomboid64.json"), 0o644)
	if l, err := readLaunch(dir, "windows"); err != nil || l.Source != jsonName {
		t.Errorf("with only the .json: %+v, %v", l, err)
	}
	os.WriteFile(filepath.Join(dir, batName), readTestdata(t, "launch/StartServer64.bat"), 0o644)
	if l, err := readLaunch(dir, "windows"); err != nil || l.Source != batName {
		t.Errorf("windows with both: %+v, %v", l, err)
	}
	if l, err := readLaunch(dir, "linux"); err != nil || l.Source != jsonName {
		t.Errorf("linux with both: %+v, %v", l, err)
	}
	os.WriteFile(filepath.Join(dir, batName), []byte("PAUSE\r\n"), 0o644)
	if l, err := readLaunch(dir, "windows"); err != nil || l.Source != jsonName {
		t.Errorf("windows with a broken .bat: %+v, %v", l, err)
	}
	if errNoLaunch.Error() != "Couldn't read how to start the Project Zomboid server. Try Repair." {
		t.Errorf("the message is %q (spec 5.6)", errNoLaunch)
	}
}

// doneInstaller is a SteamCMD that has nothing to do.
type doneInstaller struct{}

func (doneInstaller) Install(context.Context, bool, func(game.Progress)) error { return nil }

// An install whose launch files can't be read says exactly the spec's
// sentence (5.6), as a start does; what went wrong with each file goes to
// the log, not to the owner.
func TestCheckedInstaller(t *testing.T) {
	dir := t.TempDir()
	var logged strings.Builder
	i := &checkedInstaller{inner: doneInstaller{}, server: dir, goos: "windows", log: log.New(&logged, "", 0)}
	err := i.Install(context.Background(), false, func(game.Progress) {})
	if !errors.Is(err, errNoLaunch) || err.Error() != errNoLaunch.Error() {
		t.Errorf("with no launch files the install says %q, want exactly %q", err, errNoLaunch)
	}
	if !strings.Contains(logged.String(), batName) || !strings.Contains(logged.String(), jsonName) {
		t.Errorf("the log doesn't say what went wrong with each file: %q", logged.String())
	}
	os.WriteFile(filepath.Join(dir, batName), readTestdata(t, "launch/StartServer64.bat"), 0o644)
	if err := i.Install(context.Background(), false, func(game.Progress) {}); err != nil {
		t.Errorf("with the real .bat: %v", err)
	}
}

// commandLine is a plan's command line for a golden file: one argument a
// line, with this test's folders and the admin password named.
func commandLine(s *server, args []string, env []string) string {
	var b strings.Builder
	for _, a := range append(args, env...) {
		a = strings.ReplaceAll(a, s.env.Dirs.Saves, "<saves>")
		a = strings.ReplaceAll(a, s.env.Dirs.Server, "<server>")
		b.WriteString(filepath.ToSlash(a) + "\n")
	}
	return b.String()
}

// The command line a start gives the server's Java, from the real
// Build 42 .bat (Windows) and from the .json (Linux, and the Windows
// fallback).
func TestCommandLineGolden(t *testing.T) {
	s := Game{}.New(testEnv(t)).(*server)
	sv := defaultSaved(t, s)
	sv.Server["memoryMB"] = "8192"
	sv.Ports = map[string]int{"game": 16300, "direct": 16301, "rcon": 27100}
	bat, err := parseBat(string(readTestdata(t, "launch/StartServer64.bat")))
	if err != nil {
		t.Fatal(err)
	}
	saves := s.env.Dirs.Saves
	// The .bat's SET lines go into the environment too.
	golden(t, "command-line-windows.txt", commandLine(s, s.args(bat, sv, "World", "ADMINPASSWORD", saves), bat.Env))
	j, err := parseLaunchJSON(readTestdata(t, "launch/ProjectZomboid64.json"), "windows")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "command-line-windows-json.txt", commandLine(s, s.args(j, sv, "World", "ADMINPASSWORD", saves), j.Env))
	j, err = parseLaunchJSON(readTestdata(t, "launch/ProjectZomboid64-linux.json"), "linux")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "command-line-linux.txt", commandLine(s, s.args(j, sv, "World", "ADMINPASSWORD", saves), linuxEnv(s.env.Dirs.Server)))
}

// A start gives the server its folders spelt in full, with the launcher's
// environment and none of the computer's own Java options, and
// Fingerprint doesn't read the disk to know them.
func TestPlanFoldersAndEnvironment(t *testing.T) {
	env := testEnv(t)
	s := Game{}.New(env).(*server)
	if err := s.Installer().Install(context.Background(), false, nil); err != nil {
		t.Fatal(err)
	}
	sv := defaultSaved(t, s)
	s.store.Save(func(c *game.Saved) error { *c = sv.Clone(); return nil })
	p, err := s.Plan(context.Background(), game.PlanRequest{World: "World", New: true, Saved: sv, LogPath: filepath.Join(env.Dirs.Logs, "zomboid-x.log")})
	if err != nil {
		t.Fatal(err)
	}
	if p.Dir != longPath(env.Dirs.Server) {
		t.Errorf("Dir = %q, want %q", p.Dir, longPath(env.Dirs.Server))
	}
	if !slices.Contains(p.Args, "-cachedir="+longPath(env.Dirs.Saves)) {
		t.Errorf("no -cachedir=%s in %q", longPath(env.Dirs.Saves), p.Args)
	}
	// The fake install has the real Build 42 .bat beside the stand-in; the
	// variables Java takes extra options from are emptied.
	if want := []string{"PZ_CLASSPATH=java/;java/projectzomboid.jar", "_JAVA_OPTIONS=", "JAVA_TOOL_OPTIONS=", "JDK_JAVA_OPTIONS="}; !slices.Equal(p.Env, want) {
		t.Errorf("Env = %q, want %q", p.Env, want)
	}
	if p.Fingerprint != s.Fingerprint(sv, "World") {
		t.Errorf("the plan's fingerprint %q isn't Fingerprint's %q", p.Fingerprint, s.Fingerprint(sv, "World"))
	}
}

// The settings file the plan writes: a new world's (only the app's keys),
// and the real Build 42 file of a world that has run (every other line
// kept, the app's keys changed in place).
func TestIniGolden(t *testing.T) {
	s := Game{}.New(testEnv(t)).(*server)
	sv := defaultSaved(t, s)
	sv.Server["serverName"] = "Friday night — Ñandú"
	sv.Server["password"] = "two words"
	sv.Server["pauseEmpty"] = "false"
	ini := append(s.iniSettings(sv, "RCONPASSWORD"), setting{"Seed", "Abc123"})
	golden(t, "servertest.ini-new", crlf(iniSet("", ini, "\r\n")))
	existing := string(readTestdata(t, "real/servertest-42.20.4.ini"))
	got := iniSet(crlf(existing), s.iniSettings(sv, "RCONPASSWORD"), "\r\n")
	golden(t, "servertest.ini-existing", got)
	// Every line but the app's keys is as it was, in place, and the file
	// still has no newline after its last line, as the server writes it.
	before, after := strings.Split(crlf(existing), "\r\n"), strings.Split(got, "\r\n")
	if len(before) != len(after) {
		t.Fatalf("%d lines became %d: every key was in the file already", len(before), len(after))
	}
	ours := map[string]bool{}
	for _, st := range s.iniSettings(sv, "") {
		ours[st.Key] = true
	}
	for i := range before {
		key, _, _ := strings.Cut(before[i], "=")
		if before[i] != after[i] && !ours[key] {
			t.Errorf("line %d changed though it isn't the app's: %q → %q", i+1, before[i], after[i])
		}
	}
	for _, want := range []string{"PublicName=Friday night — Ñandú", "Password=two words", "PauseEmpty=false", "UPnP=false", "Public=false", "RCONPassword=RCONPASSWORD", "MaxPlayers=16"} {
		if !slices.Contains(after, want) {
			t.Errorf("the edited file has no line %q", want)
		}
	}
	if v, _ := iniGet(got, "PublicName"); v != "Friday night — Ñandú" {
		t.Errorf("read back as the server would: PublicName=%q", v)
	}
}

// A key written twice is changed both times (the server reads the last
// one), and a file ending in a newline keeps it.
func TestIniSetRepeatsAndEndings(t *testing.T) {
	got := iniSet("# c\nPassword=a\nPVP=true\nPassword=b\n", []setting{{"Password", "x"}, {"Open", "true"}}, "\r\n")
	if got != "# c\nPassword=x\nPVP=true\nPassword=x\n\nOpen=true\n" {
		t.Errorf("got %q", got)
	}
	if v, ok := iniGet("A=1\r\n A = 2 \r\nB=c=d\r\n", "B"); !ok || v != "c" {
		t.Errorf("B = %q, %v: the server cuts a value at its next \"=\"", v, ok)
	}
}

// The SandboxVars file: a new world's, and the real Build 42 file's
// first options and ZombieConfig table with "Zombies" set to very high
// (the file had High, with the multiplier left at Normal's).
func TestSandboxGolden(t *testing.T) {
	level, _ := zombieLevelOf("veryhigh")
	sb := sandboxSettings{Zombies: level.Count, Multiplier: level.Multiplier}
	got, err := sandboxSet("", sb, "\r\n")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "servertest_SandboxVars.lua-new", got)
	existing := crlf(string(readTestdata(t, "real/servertest_SandboxVars-42.20.4.lua")))
	got, err = sandboxSet(existing, sb, "\r\n")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "servertest_SandboxVars.lua-existing", got)
	if strings.Count(got, "\r\n") != strings.Count(existing, "\r\n") {
		t.Errorf("lines were added to a file that had both values")
	}
	for _, want := range []string{"\r\n    Zombies = 2,\r\n", "\r\n        PopulationMultiplier = 1.6,\r\n",
		"PopulationStartMultiplier = 1.0,", "-- 3 = High\r\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("the edited file has no %q", want)
		}
	}
}

// Values the file lacks are added where the server reads them, and a key
// of the same name in another table is left alone.
func TestSandboxSetAddsAndKeepsOthers(t *testing.T) {
	sb := sandboxSettings{Zombies: 6, Multiplier: "0.0"}
	in := "SandboxVars = {\n    VERSION = 6,\n    Other = {\n        Zombies = 9, -- not ours\n        PopulationMultiplier = 9.0,\n    },\n    Text = \"a } brace\",\n}\n"
	got, err := sandboxSet(in, sb, "\n")
	if err != nil {
		t.Fatal(err)
	}
	want := "SandboxVars = {\n    VERSION = 6,\n    Zombies = 6,\n    Other = {\n        Zombies = 9, -- not ours\n        PopulationMultiplier = 9.0,\n    },\n    Text = \"a } brace\",\n    ZombieConfig = {\n        PopulationMultiplier = 0.0,\n    },\n}\n"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	in = "SandboxVars = {\n    ZombieConfig = {\n        RespawnHours = 0.0,\n    },\n}"
	got, _ = sandboxSet(in, sb, "\n")
	want = "SandboxVars = {\n    Zombies = 6,\n    ZombieConfig = {\n        PopulationMultiplier = 0.0,\n        RespawnHours = 0.0,\n    },\n}"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	for _, bad := range []string{"return { Zombies = 1 }", "SandboxVars = {\n    Zombies = 1,\n", "SandboxVars = { Zombies = 1 }\nx = 2"} {
		if out, err := sandboxSet(bad, sb, "\n"); !errors.Is(err, errSandboxShape) || out != bad {
			t.Errorf("%q: %q, %v: want it left alone", bad, out, err)
		}
	}
}

// A start writes the world's files, gives RCON a new password every run
// and the admin the same one every run, and refuses a missing world.
func TestPlanWritesTheWorldsFiles(t *testing.T) {
	env := testEnv(t)
	s := Game{}.New(env).(*server)
	if err := s.Installer().Install(context.Background(), false, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Build(); !ok {
		t.Fatal("after the fake install, Build() says nothing is installed")
	}
	sv := defaultSaved(t, s)
	sv.Worlds["World"]["zombies"] = "none"
	s.store.Save(func(c *game.Saved) error { *c = sv.Clone(); return nil })
	req := game.PlanRequest{World: "World", New: true, NewWorld: game.Values{"seed": "Seed42"}, Saved: sv, LogPath: filepath.Join(env.Dirs.Logs, "zomboid-x.log")}
	p, err := s.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	ini, _ := os.ReadFile(iniFile(env.Dirs.Saves, "World"))
	if pw, _ := iniGet(string(ini), "RCONPassword"); pw == "" || pw != p.Record.Secrets["rcon"] {
		t.Errorf("RCONPassword %q in the file, %q in the run record", pw, p.Record.Secrets["rcon"])
	}
	if seed, _ := iniGet(string(ini), "Seed"); seed != "Seed42" {
		t.Errorf("a new world's Seed = %q", seed)
	}
	if p.Record.Ports["rcon"] != 27015 || p.Record.Ports["direct"] != 16262 || p.Record.Port != 16261 || p.Output != game.Capture || p.Console {
		t.Errorf("plan record %+v, output %v, console %v", p.Record, p.Output, p.Console)
	}
	lua, _ := os.ReadFile(sandboxFile(env.Dirs.Saves, "World"))
	if !strings.Contains(string(lua), "Zombies = 6,") || !strings.Contains(string(lua), "PopulationMultiplier = 0.0,") {
		t.Errorf("SandboxVars:\n%s", lua)
	}
	admin := s.store.Get().State[stateAdminPassword]
	if i := slices.Index(p.Args, "-adminpassword"); admin == "" || i < 0 || p.Args[i+1] != admin {
		t.Errorf("the admin password %q isn't the one on the command line %q", admin, p.Args)
	}

	// The world has run once now.
	os.MkdirAll(filepath.Join(multiplayerDir(env.Dirs.Saves), "World"), 0o755)
	os.WriteFile(filepath.Join(multiplayerDir(env.Dirs.Saves), "World", "map_t.bin"), []byte("map"), 0o644)
	req.New, req.Saved = false, s.store.Get()
	p2, err := s.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if p2.Record.Secrets["rcon"] == p.Record.Secrets["rcon"] {
		t.Errorf("RCON got the same password twice")
	}
	if i := slices.Index(p2.Args, "-adminpassword"); p2.Args[i+1] != admin {
		t.Errorf("the admin password changed between starts")
	}
	if p2.Fingerprint != p.Fingerprint || p2.Fingerprint != s.Fingerprint(req.Saved, "World") {
		t.Errorf("fingerprints %q, %q, %q: the settings didn't change", p.Fingerprint, p2.Fingerprint, s.Fingerprint(req.Saved, "World"))
	}

	req.World = "Gone"
	if _, err := s.Plan(context.Background(), req); err == nil || !strings.Contains(err.Error(), "isn't in Project Zomboid's saves folder") {
		t.Errorf("a missing world: %v", err)
	}
	req.World, req.New, req.NewWorld = "Fresh", true, game.Values{"seed": "no spaces!"}
	if _, err := s.Plan(context.Background(), req); err == nil {
		t.Errorf("a bad seed was taken")
	}
	req.NewWorld = nil
	req.Saved.Server["password"] = "a=b"
	var fe game.FieldErrors
	if _, err := s.Plan(context.Background(), req); !errors.As(err, &fe) || fe["password"] == "" {
		t.Errorf("a password with \"=\": %v", err)
	}
}

// A folder whose path Windows can't give Java whole is refused before the
// server could put its world somewhere else.
func TestFolderProblem(t *testing.T) {
	old := argRuneFits
	t.Cleanup(func() { argRuneFits = old })
	argRuneFits = func(r rune) bool { return r < 0x100 } // a Western code page
	if p := folderProblem(`C:\Users\Jürgen\AppData\Local\ValheimEasyServer\games\zomboid\saves`); p != "" {
		t.Errorf("a path the code page has: %q", p)
	}
	p := folderProblem(`C:\Users\Łukasz\AppData\Local\ValheimEasyServer\games\zomboid\saves`)
	if !strings.Contains(p, `"Ł"`) || !strings.Contains(p, "can't run from the app's folder") {
		t.Errorf("a path with Ł: %q", p)
	}
}

func TestValidate(t *testing.T) {
	s := Game{}.New(testEnv(t)).(*server)
	base := defaultSaved(t, s)
	if errs := s.Validate(base, "World"); len(errs) != 0 {
		t.Fatalf("the defaults: %v", errs)
	}
	for _, c := range []struct {
		key, value, world string
		field             string
	}{
		{"serverName", strings.Repeat("a", 65), "World", "serverName"},
		{"serverName", "a=b", "World", "serverName"},
		{"serverName", "two\nlines", "World", "serverName"},
		{"password", " padded", "World", "password"},
		{"password", "x=y", "World", "password"},
		{"maxPlayers", "101", "World", "maxPlayers"},
		{"memoryMB", "1024", "World", "memoryMB"},
		{"serverName", "fine", "Wörld", "world"},
		{"serverName", "fine", "bad.name", "world"},
		{"serverName", "fine", "-dash", "world"},
	} {
		sv := base.Clone()
		sv.Server[c.key] = c.value
		if sv.Worlds[c.world] == nil {
			sv.Worlds[c.world] = game.Values{}
		}
		if errs := s.Validate(sv, c.world); errs[c.field] == "" {
			t.Errorf("%s=%q in world %q: no error for %s (%v)", c.key, c.value, c.world, c.field, errs)
		}
	}
	sv := base.Clone()
	sv.Worlds["World"]["zombies"] = "lots"
	if errs := s.Validate(sv, "World"); errs["zombies"] == "" {
		t.Errorf("an unknown zombie level was taken")
	}
	if p := seedProblem("Abc123"); p != "" {
		t.Errorf("a good seed: %q", p)
	}
}

// Recover finds the RCON port and password the running server took from
// the world's settings file.
func TestRecover(t *testing.T) {
	env := testEnv(t)
	s := Game{}.New(env).(*server)
	sv := defaultSaved(t, s)
	s.store.Save(func(c *game.Saved) error { *c = sv.Clone(); return nil })
	ini := iniSet(string(readTestdata(t, "real/servertest-42.20.4.ini")), []setting{{"RCONPort", "27222"}, {"RCONPassword", "abc"}, {"DefaultPort", "16400"}, {"UDPPort", "16401"}}, "\r\n")
	if err := writeFile(iniFile(env.Dirs.Saves, "World"), ini); err != nil {
		t.Fatal(err)
	}
	rec := game.Server(s).(game.Recoverer).Recover(42, time.Time{})
	if rec.Ports["rcon"] != 27222 || rec.Secrets["rcon"] != "abc" || rec.Port != 16400 || rec.Ports["direct"] != 16401 || rec.Server != "Prueba Ñandú" || rec.World != "World" {
		t.Errorf("recovered %+v", rec)
	}
}
