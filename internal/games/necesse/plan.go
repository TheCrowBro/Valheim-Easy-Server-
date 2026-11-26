package necesse

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/install"
)

// How long a start waits for the server to say it's ready before the
// manager's fallback (it adds 5 minutes, spec 4.3). A new world is made in
// about 10 seconds and an existing one loads in about 3 on a fast computer
// (FACTS.md); these leave room for an old one and a big world.
const (
	readyWaitNew   = 10 * time.Minute
	readyWaitStart = 5 * time.Minute
)

// javaOptions are the Java settings Necesse's own start scripts use (the
// StartServer-nogui scripts in the download, FACTS.md). The app starts Java
// itself rather than through a script, so it passes them the same way.
var javaOptions = []string{
	"-XX:+UnlockExperimentalVMOptions", "-XX:+UseG1GC", "-XX:+ExplicitGCInvokesConcurrent",
	"-XX:G1NewSizePercent=20", "-XX:G1ReservePercent=20", "-XX:MaxGCPauseMillis=50", "-XX:G1HeapRegionSize=32M",
}

func port(sv game.Saved) int {
	if p := sv.Ports["game"]; p > 0 {
		return p
	}
	return defaultPort
}

// args is the command line for a start of world. Every setting is given
// on it, so the server.cfg the server keeps in its data folder never
// decides anything (the server only falls back to it for options left out).
//
//   - -datadir keeps the server's settings, cache, log and worlds in the
//     instance's saves folder, never in the Necesse folder the owner's own
//     game uses (%APPDATA%\Necesse, ~/Library/Application Support/Necesse).
//   - -logging 0: the app keeps its own log of each run from the console,
//     so the server's copy in <datadir>/logs would only fill the disk.
//   - -zipsaves 1 and -language en are the server's defaults, given so a
//     change in server.cfg can't move the worlds or change the console's
//     wording under the app.
//   - -password is always given, even empty, so a password left in
//     server.cfg (the server falls back to it for options not given) can
//     never stay on unseen.
func (s *server) args(sv game.Saved, world string) []string {
	mem := sv.Server.Int("memoryMB", defMemoryMB)
	a := []string{"-Xmx" + strconv.Itoa(mem) + "M"}
	a = append(a, javaOptions...)
	return append(a,
		"-jar", jarName, "-nogui",
		"-datadir", s.env.Dirs.Saves,
		"-world", world,
		"-port", strconv.Itoa(port(sv)),
		"-slots", strconv.Itoa(sv.Server.Int("maxPlayers", 10)),
		"-password", sv.Server["password"],
		"-motd", s.value(sv, "serverName"),
		"-logging", "0",
		"-zipsaves", "1",
		"-language", "en",
	)
}

// Fingerprint covers the whole command line, which is everything the server
// reads when it starts, so a change to any of it says "restart to apply".
func (s *server) Fingerprint(sv game.Saved, world string) string {
	h := sha256.New()
	for _, a := range s.args(sv, world) {
		io.WriteString(h, a+"\x00")
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Plan says how to start the server. With req.Update it first lets
// SteamCMD fetch a newer build, if Steam has one; a start never waits for
// another game's download (spec 2.3).
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
	for _, p := range []string{s.Program(), filepath.Join(s.env.Dirs.Server, jarName)} {
		if _, err := os.Stat(p); err != nil {
			return game.Plan{}, errors.New("The Necesse server isn't installed. Click Install (or Repair) first.")
		}
	}
	if errs := s.Validate(req.Saved, req.World); len(errs) > 0 {
		return game.Plan{}, errs
	}
	wait := readyWaitStart
	if req.New {
		// The server makes the world when it doesn't find it.
		wait = readyWaitNew
	} else if !s.worlds.Exists(req.World) {
		// Without the world the server would quietly make a new, empty
		// one under the same name; saying so here is clearer.
		return game.Plan{}, fmt.Errorf("The world %q isn't in Necesse's saves folder any more. Restore one of its backups on the Worlds page, or make a new world.", req.World)
	}
	if err := os.MkdirAll(s.env.Dirs.Saves, 0o755); err != nil {
		return game.Plan{}, err
	}
	return game.Plan{
		Exe:         s.Program(),
		Args:        s.args(req.Saved, req.World),
		Dir:         s.env.Dirs.Server, // -jar Server.jar is found here
		Output:      game.Capture,
		LogPath:     req.LogPath,
		Console:     true,
		SaveLine:    "save",
		ReadyWait:   wait,
		Fingerprint: s.Fingerprint(req.Saved, req.World),
		Record: game.Record{
			Server:   s.value(req.Saved, "serverName"),
			Port:     port(req.Saved),
			Password: req.Saved.Server["password"],
		},
	}, nil
}

// updateOnStart runs SteamCMD before a start. Steam downloads only when a
// newer build is out. If SteamCMD is busy with another game's download, or
// the update fails, the start goes on with the build that's installed: a
// server that is a version behind beats no server.
func (s *server) updateOnStart(ctx context.Context, step func(string, float64)) {
	step("Checking for Necesse server updates…", -1)
	err := s.Installer().Install(install.SkipIfBusy(ctx), false, func(p game.Progress) { step(p.Step, p.Percent) })
	switch {
	case err == nil:
	case ctx.Err() != nil:
	case errors.Is(err, install.ErrBusy):
		step("Couldn't check for server updates ("+err.Error()+"). Starting the version that's installed.", -1)
		s.log.Printf("update on start skipped: %v", err)
	default:
		step("Couldn't check for server updates ("+err.Error()+"). Starting the version that's installed.", -1)
		s.log.Printf("update on start failed: %v", err)
	}
}
