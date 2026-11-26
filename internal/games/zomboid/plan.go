package zomboid

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/install"
)

// How long a start waits for the server to say it's ready before the
// manager's fallback (it adds 5 minutes, spec 4.3). A real server took
// 40 to 76 seconds from launch to "*** SERVER STARTED" on a fresh world
// (FACTS.md); these leave room for an old computer and a big world.
const (
	readyWaitNew   = 10 * time.Minute
	readyWaitStart = 8 * time.Minute
)

// stateAdminPassword is where the admin account's password is kept, in the
// instance's State: made once, and given to the server at every start.
const stateAdminPassword = "adminPassword"

// extraJVM are the Java options the app adds to the launcher's:
//   - -Drconlo=true makes the server listen for RCON on 127.0.0.1 only
//     (FACTS.md), so the RCON port can't be reached from the home network
//     at all; only the app uses it.
//   - English as the server's language, so the comments it writes into
//     the settings files and its messages read the same on every PC
//     (FACTS.md: otherwise they follow Windows' language).
var extraJVM = []string{"-Drconlo=true", "-Duser.language=en", "-Duser.country=US"}

func port(sv game.Saved, role string, def int) int {
	if p := sv.Ports[role]; p > 0 {
		return p
	}
	return def
}

func gamePort(sv game.Saved) int { return port(sv, roleGame, defaultGamePort) }

// directPort is the game port plus one, as the role's tie says, unless
// the ports were set otherwise.
func directPort(sv game.Saved) int { return port(sv, roleDirect, gamePort(sv)+1) }

func rconPort(sv game.Saved) int { return port(sv, roleRCON, defaultRCONPort) }

// args is the command line for a start of world, from the launcher's
// settings l. adminPW is left out of the fingerprint (see Fingerprint).
//
//   - The heap is the "Java memory" setting (the launcher's own -Xms/-Xmx
//     are left out, parseBat).
//   - -cachedir=<saves> keeps the server's whole data folder (settings,
//     worlds, accounts, logs) in the instance's saves folder, never in
//     %USERPROFILE%\Zomboid, the folder the owner's own game uses.
//   - -servername picks the world: its Server/<world>.ini and its map.
//   - -adminusername and -adminpassword make the admin account on a
//     world's first start, which otherwise stops to ask for a password on
//     the console (FACTS.md), and give it the same password every start.
//   - -port and -udpport are the instance's ports, which win over the
//     settings file; the file gets them too.
func (s *server) args(l launch, sv game.Saved, world, adminPW string) []string {
	mem := sv.Server.Int("memoryMB", defMemoryMB)
	a := append([]string(nil), l.JVM...)
	a = append(a, "-Xmx"+strconv.Itoa(mem)+"m")
	a = append(a, extraJVM...)
	a = append(a, "-cp", l.ClassPath, l.Main)
	a = append(a, l.Game...)
	return append(a,
		"-cachedir="+s.env.Dirs.Saves,
		"-servername", world,
		"-adminusername", adminUser,
		"-adminpassword", adminPW,
		"-port", strconv.Itoa(gamePort(sv)),
		"-udpport", strconv.Itoa(directPort(sv)),
	)
}

// iniSettings are the keys the app sets in the world's settings file
// (FACTS.md has each one and the server's default):
//   - PublicName, Password, MaxPlayers, PVP and PauseEmpty from the
//     settings;
//   - Open=true: friends make their own account (a name and a password of
//     their own) the first time they join, so nobody has to add them;
//   - Public=false: the server isn't listed in the game's public server
//     list; friends add it by its address;
//   - UPnP=false: the app opens the router's ports itself (spec 6.5), and
//     the server's own try can hang its start on some routers;
//   - the ports, and the RCON password of this run.
func (s *server) iniSettings(sv game.Saved, rconPW string) []setting {
	tf := func(key string) string {
		if s.boolValue(sv, key) {
			return "true"
		}
		return "false"
	}
	return []setting{
		{"PublicName", s.value(sv, "serverName")},
		{"Password", sv.Server["password"]},
		{"MaxPlayers", strconv.Itoa(sv.Server.Int("maxPlayers", defSlots))},
		{"PVP", tf("pvp")},
		{"PauseEmpty", tf("pauseEmpty")},
		{"Open", "true"},
		{"Public", "false"},
		{"UPnP", "false"},
		{"DefaultPort", strconv.Itoa(gamePort(sv))},
		{"UDPPort", strconv.Itoa(directPort(sv))},
		{"RCONPort", strconv.Itoa(rconPort(sv))},
		{"RCONPassword", rconPW},
	}
}

// sandboxFor is what the world's "Zombies" setting asks of its SandboxVars
// file; false when it isn't set (a world the app didn't make, left as it
// is).
func sandboxFor(sv game.Saved, world string) (sandboxSettings, bool) {
	z, ok := zombieLevelOf(sv.Worlds[world]["zombies"])
	if !ok {
		return sandboxSettings{}, false
	}
	return sandboxSettings{Zombies: z.Count, Multiplier: z.Multiplier}, true
}

// Fingerprint covers everything a start gives the server from the
// settings: the command line (with the launcher's own options left out,
// since reading them means reading a file, and the admin password, which
// doesn't change), the keys set in the settings file (without the RCON
// password, which is new every run) and the world's sandbox values. A
// change to any of them says "restart to apply".
func (s *server) Fingerprint(sv game.Saved, world string) string {
	h := sha256.New()
	for _, a := range s.args(launch{Main: mainClass}, sv, world, "") {
		io.WriteString(h, a+"\x00")
	}
	for _, st := range s.iniSettings(sv, "") {
		io.WriteString(h, st.Key+"="+st.Value+"\x00")
	}
	if sb, ok := sandboxFor(sv, world); ok {
		fmt.Fprintf(h, "Zombies=%d\x00PopulationMultiplier=%s\x00", sb.Zombies, sb.Multiplier)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Plan says how to start the server, and writes the world's settings files
// for this start. With req.Update it first lets SteamCMD fetch a newer
// build, if Steam has one; a start never waits for another game's download
// (spec 2.3).
func (s *server) Plan(ctx context.Context, req game.PlanRequest) (game.Plan, error) {
	step := req.Step
	if step == nil {
		step = func(string, float64) {}
	}
	if req.Update && s.env.Fake == nil {
		s.updateOnStart(ctx, step)
		if err := ctx.Err(); err != nil {
			return game.Plan{}, err
		}
	}
	if _, err := os.Stat(s.Program()); err != nil {
		return game.Plan{}, errors.New("The Project Zomboid server isn't installed. Click Install (or Repair) first.")
	}
	l, err := readLaunch(s.env.Dirs.Server, s.launchOS())
	if err != nil {
		s.log.Printf("reading the launch files: %v", err)
		return game.Plan{}, errNoLaunch
	}
	if errs := s.Validate(req.Saved, req.World); len(errs) > 0 {
		return game.Plan{}, errs
	}
	seed := strings.TrimSpace(req.NewWorld["seed"])
	if req.New {
		if p := seedProblem(seed); p != "" {
			return game.Plan{}, game.FieldErrors{"seed": p}
		}
	}
	// The data folder is on the command line (-cachedir).
	if p := folderProblem(s.env.Dirs.Saves); p != "" {
		return game.Plan{}, errors.New(p)
	}
	wait := readyWaitStart
	if req.New {
		wait = readyWaitNew
	} else if !s.worlds.Exists(req.World) {
		// Without its map the server would quietly make a new, empty
		// world under the same name; saying so here is clearer.
		return game.Plan{}, fmt.Errorf("The world %q isn't in Project Zomboid's saves folder any more. Restore one of its backups on the Worlds page, or make a new world.", req.World)
	}

	adminPW, err := s.adminPassword(req.Saved)
	if err != nil {
		return game.Plan{}, err
	}
	rconPW, err := secret()
	if err != nil {
		return game.Plan{}, err
	}
	ini := s.iniSettings(req.Saved, rconPW)
	if req.New && seed != "" {
		ini = append(ini, setting{"Seed", seed})
	}
	if err := s.writeSettings(req.World, ini, req.Saved); err != nil {
		return game.Plan{}, fmt.Errorf("couldn't write Project Zomboid's settings for %q: %w", req.World, err)
	}

	var env []string
	if s.env.Fake == nil && s.launchOS() == "linux" {
		env = linuxEnv(s.env.Dirs.Server)
	}
	sv := req.Saved
	return game.Plan{
		Exe:         s.Program(),
		Args:        s.args(l, sv, req.World, adminPW),
		Dir:         s.env.Dirs.Server, // the class path and natives/ are relative to it
		Env:         env,
		Output:      game.Capture, // the server prints its whole log on standard output
		LogPath:     req.LogPath,
		ReadyWait:   wait,
		Fingerprint: s.Fingerprint(sv, req.World),
		Record: game.Record{
			Server:   s.value(sv, "serverName"),
			Port:     gamePort(sv),
			Password: sv.Server["password"],
			Ports:    map[string]int{roleGame: gamePort(sv), roleDirect: directPort(sv), roleRCON: rconPort(sv)},
			Secrets:  map[string]string{roleRCON: rconPW},
		},
	}, nil
}

// writeSettings edits the world's settings file and its SandboxVars file
// for this start (config.go).
func (s *server) writeSettings(world string, ini []setting, sv game.Saved) error {
	saves := s.env.Dirs.Saves
	path := iniFile(saves, world)
	text, err := readText(path)
	if err != nil {
		return err
	}
	if err := writeFile(path, iniSet(text, ini, newline())); err != nil {
		return err
	}
	sb, ok := sandboxFor(sv, world)
	if !ok {
		return nil
	}
	path = sandboxFile(saves, world)
	if text, err = readText(path); err != nil {
		return err
	}
	out, err := sandboxSet(text, sb, newline())
	if err != nil {
		// A file the server can't have written: leave it to the server,
		// which says what is wrong with it when it starts.
		s.log.Printf("%s: %v", path, err)
		return nil
	}
	return writeFile(path, out)
}

// adminPassword is the admin account's password: made the first time and
// kept in the instance's State, so it stays the same.
func (s *server) adminPassword(sv game.Saved) (string, error) {
	if pw := sv.State[stateAdminPassword]; pw != "" {
		return pw, nil
	}
	var pw string
	err := s.store.Save(func(c *game.Saved) error {
		if c.State == nil {
			c.State = game.Values{}
		}
		if c.State[stateAdminPassword] == "" {
			p, err := secret()
			if err != nil {
				return err
			}
			c.State[stateAdminPassword] = p
		}
		pw = c.State[stateAdminPassword]
		return nil
	})
	return pw, err
}

// secret is a new random password: 24 hex digits, which pass through the
// settings file and the command line unchanged on every system.
func secret() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// linuxEnv is what the server's Linux script, start-server.sh, sets before
// it runs the server (FACTS.md). Linux isn't a system the app is offered
// on; this is for running the real server in CI there.
func linuxEnv(serverDir string) []string {
	lib := strings.Join([]string{
		filepath.Join(serverDir, "linux64"), filepath.Join(serverDir, "natives"), serverDir,
		filepath.Join(serverDir, "jre64", "lib"), filepath.Join(serverDir, "jre64", "lib", "amd64"),
	}, ":")
	return []string{"LD_LIBRARY_PATH=" + lib, "LD_PRELOAD=libjsig.so"}
}

// updateOnStart runs SteamCMD before a start. Steam downloads only when a
// newer build is out. If SteamCMD is busy with another game's download, or
// the update fails, the start goes on with the build that's installed: a
// server that is a version behind beats no server.
func (s *server) updateOnStart(ctx context.Context, step func(string, float64)) {
	step("Checking for Project Zomboid server updates…", -1)
	err := s.Installer().Install(install.SkipIfBusy(ctx), false, func(p game.Progress) { step(p.Step, p.Percent) })
	switch {
	case err == nil:
	case ctx.Err() != nil:
	case errors.Is(err, install.ErrBusy):
		step("Couldn't check for server updates ("+err.Error()+"). Starting the version that's installed.", -1)
		s.log.Printf("update on start skipped: %v", err)
	case errors.Is(err, errNoLaunch):
		// The update itself went through, but the launch files can't be
		// read. Plan reads them again just below and stops with the same
		// plain message, so a step saying the start goes on would be wrong.
		s.log.Printf("update on start: %v", err)
	default:
		step("Couldn't check for server updates ("+err.Error()+"). Starting the version that's installed.", -1)
		s.log.Printf("update on start failed: %v", err)
	}
}

// Recover makes a run record for a server found running without its run
// file. The RCON port and password the server is using are in the
// selected world's settings file (the plan wrote them there, and the
// server keeps them), so the app can still count players and stop it
// gently.
func (s *server) Recover(pid int, started time.Time) game.Record {
	sv := s.store.Get()
	rec := game.Record{PID: pid, Started: started, Launched: started, World: sv.World, Exe: s.Program(),
		LogPath: newestLog(s.env.Dirs.Logs, s.env.Instance),
		Ports:   map[string]int{roleGame: gamePort(sv), roleDirect: directPort(sv), roleRCON: rconPort(sv)},
		Secrets: map[string]string{}}
	rec.Port = rec.Ports[roleGame]
	text, err := readText(iniFile(s.env.Dirs.Saves, sv.World))
	if err != nil || text == "" {
		return rec
	}
	num := func(key string) int {
		v, _ := iniGet(text, key)
		n, _ := strconv.Atoi(v)
		return n
	}
	for role, key := range map[string]string{roleGame: "DefaultPort", roleDirect: "UDPPort", roleRCON: "RCONPort"} {
		if p := num(key); p > 0 {
			rec.Ports[role] = p
		}
	}
	rec.Port = rec.Ports[roleGame]
	if pw, ok := iniGet(text, "RCONPassword"); ok && pw != "" {
		rec.Secrets[roleRCON] = pw
	}
	rec.Server, _ = iniGet(text, "PublicName")
	rec.Password, _ = iniGet(text, "Password")
	return rec
}

// newestLog is the newest <instance>-*.log in dir, "" when there's none.
func newestLog(dir, instance string) string {
	matches, _ := filepath.Glob(filepath.Join(dir, instance+"-*.log"))
	var best string
	var bestTime time.Time
	for _, m := range matches {
		if st, err := os.Stat(m); err == nil && (best == "" || st.ModTime().After(bestTime)) {
			best, bestTime = m, st.ModTime()
		}
	}
	return best
}
