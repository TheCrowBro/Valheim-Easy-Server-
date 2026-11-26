package enshrouded

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/install"
)

// How long a start waits for the server to say it's online before the
// manager's fallback (it adds 5 minutes, spec 4.3). Real logs show the
// session up about a second after the start, with the world loading in the
// seconds after (FACTS.md); these leave room for a slow disk, a first
// start that makes the world, and Steam being slow to answer.
const (
	readyWaitNew   = 10 * time.Minute
	readyWaitStart = 5 * time.Minute
)

// keepGameLogs is how many of the server's old logs the app keeps in the
// game log folder's backup/ (the app keeps its own copy of every line it
// read, so these are only for someone looking at the server's own files).
const keepGameLogs = 10

// values are the settings the server is started with for a world ("" is
// the selected world).
func (s *server) values(sv game.Saved, world string) configValues {
	world = worldFor(sv, world)
	return configValues{
		name:      s.value(sv, "serverName"),
		saveDir:   s.worlds.saveDir(world),
		logDir:    s.gameLogDir(),
		port:      s.port(sv),
		slots:     s.slots(sv),
		textChat:  s.boolValue(sv, "textChat"),
		voiceChat: s.boolValue(sv, "voiceChat"),
		preset:    presetName(s.worldValue(sv, world, "difficulty")),
		password:  s.value(sv, "password"),
	}
}

// Fingerprint covers what the server reads from its settings file when it
// starts and keeps until it stops: everything the Plan writes there. The
// folders are the instance's own and only change with the world. World ""
// is the selected world (values sees to that), which is how the manager
// asks when it compares a running server with the settings.
func (s *server) Fingerprint(sv game.Saved, world string) string {
	c := s.values(sv, world)
	h := sha256.New()
	for _, v := range []string{c.name, c.saveDir, strconv.Itoa(c.port), strconv.Itoa(c.slots),
		strconv.FormatBool(c.textChat), strconv.FormatBool(c.voiceChat), c.preset, c.password} {
		io.WriteString(h, v+"\x00")
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Plan says how to start the server: it writes the world's settings into
// enshrouded_server.json and moves the last run's log out of the way. With
// req.Update it first lets SteamCMD fetch a newer build, if Steam has one;
// a start never waits for another game's download (spec 2.3).
func (s *server) Plan(ctx context.Context, req game.PlanRequest) (game.Plan, error) {
	req.World = worldFor(req.Saved, req.World)
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
		return game.Plan{}, errors.New("The Enshrouded server isn't installed. Click Install (or Repair) first.")
	}
	if errs := s.Validate(req.Saved, req.World); len(errs) > 0 {
		return game.Plan{}, errs
	}
	wait := readyWaitStart
	if req.New {
		// The server makes a new world in an empty saveDirectory.
		wait = readyWaitNew
	} else if !s.worlds.Exists(req.World) {
		// Without the folder the server would quietly make a new, empty
		// world; saying so here is clearer.
		return game.Plan{}, fmt.Errorf("The world %q isn't in Enshrouded's saves folder any more. Restore one of its backups on the Worlds page, or make a new world.", req.World)
	}
	c := s.values(req.Saved, req.World)
	for _, d := range []string{c.saveDir, c.logDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return game.Plan{}, err
		}
	}
	if err := s.writeConfig(c); err != nil {
		return game.Plan{}, fmt.Errorf("couldn't write Enshrouded's settings file: %w", err)
	}
	if err := s.setAsideLog(); err != nil {
		return game.Plan{}, err
	}
	return game.Plan{
		Exe:         s.Program(),
		Dir:         s.env.Dirs.Server,
		Output:      game.GameLog,
		LogPath:     s.gameLog(),
		ReadyWait:   wait,
		Fingerprint: s.Fingerprint(req.Saved, req.World),
		Record: game.Record{Server: c.name, Port: c.port, Password: c.password,
			Ports: map[string]int{roleQuery: c.port}},
	}, nil
}

// setAsideLog moves the last run's log into the folder's backup/ before a
// start. The app follows the log from its first line as soon as the server
// starts, which can be before the server moves the old one aside itself;
// the old one's "online" and join lines would then be read as this run's.
// If it can't be moved (another program has it open) the start stops,
// rather than report a server online that may not be.
func (s *server) setAsideLog() error {
	cur := s.gameLog()
	st, err := os.Stat(cur)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	backup := filepath.Join(s.gameLogDir(), "backup")
	if err := os.MkdirAll(backup, 0o755); err != nil {
		return err
	}
	to := filepath.Join(backup, fmt.Sprintf("%s_%s.log", programName, st.ModTime().Format("2006-01-02_15-04-05")))
	for i := 2; fileExists(to); i++ {
		to = filepath.Join(backup, fmt.Sprintf("%s_%s_%d.log", programName, st.ModTime().Format("2006-01-02_15-04-05"), i))
	}
	if err := os.Rename(cur, to); err != nil {
		if rerr := os.Remove(cur); rerr != nil {
			return fmt.Errorf("The Enshrouded server's last log, %s, can't be moved or removed (%v): another program may have it open. Close it, then start again.", cur, err)
		}
	}
	pruneLogs(backup, programName+"_", keepGameLogs)
	return nil
}

// pruneLogs keeps the newest keep logs in dir whose names start with
// prefix (the app's own and the server's own backups are named so).
func pruneLogs(dir, prefix string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type old struct {
		path string
		mod  time.Time
	}
	var logs []old
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasPrefix(e.Name(), prefix) || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		if info, err := e.Info(); err == nil {
			logs = append(logs, old{filepath.Join(dir, e.Name()), info.ModTime()})
		}
	}
	sort.Slice(logs, func(i, j int) bool { return logs[i].mod.After(logs[j].mod) })
	for i := keep; i < len(logs); i++ {
		os.Remove(logs[i].path)
	}
}

func fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// updateOnStart runs SteamCMD before a start. Steam downloads only when a
// newer build is out. If SteamCMD is busy with another game's download, or
// the update fails, the start goes on with the build that's installed: a
// server that is a version behind beats no server.
func (s *server) updateOnStart(ctx context.Context, step func(string, float64)) {
	step("Checking for Enshrouded server updates…", -1)
	err := s.Installer().Install(install.SkipIfBusy(ctx), false, func(p game.Progress) { step(p.Step, p.Percent) })
	switch {
	case err == nil:
	case ctx.Err() != nil:
	default:
		step("Couldn't check for server updates ("+err.Error()+"). Starting the version that's installed.", -1)
		s.log.Printf("update on start: %v", err)
	}
}
