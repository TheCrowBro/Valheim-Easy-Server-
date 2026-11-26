package corekeeper

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/install"
)

// How long a start waits for the server to say it's ready before the
// manager's fallback (it adds 5 minutes, spec 4.3). On a computer with no
// graphics card of its own, a new world was ready in about 20 seconds and
// an existing one in about 20 too (FACTS.md); these leave room for a slow
// computer and a big world.
const (
	readyWaitNew   = 10 * time.Minute
	readyWaitStart = 5 * time.Minute
)

// gameIDKey is where the instance's Game ID is kept (Saved.State).
const gameIDKey = "gameId"

// args is the command line for a start of world, apart from what only a
// new world needs (newWorldArgs).
//
//   - -batchmode, as Pugstorm's own launcher starts it; never -nographics,
//     because part of making a world runs on the graphics card.
//   - -logfile: the server writes its log where the app follows it
//     (Output LogArg), one file per run.
//   - -datapath: the world's own folder, which holds the server's world 0,
//     its ServerConfig.json and its admin and ban lists. -world 0 is
//     always that world.
//   - -worldname: what friends see. The app gives it from the server name.
//   - -gameid: the instance's own Game ID, the same for every world, so
//     friends never need a new one.
//
// Every value is given on every start, so the ServerConfig.json the server
// keeps in the folder never decides them (it only falls back to that file
// for what the command line leaves out).
func (s *server) args(sv game.Saved, world, gameID, logPath string) []string {
	a := []string{
		"-batchmode",
		"-logfile", logPath,
		"-datapath", s.worlds.dataPath(world),
		"-world", "0",
		"-worldname", s.value(sv, "serverName"),
		"-maxplayers", strconv.Itoa(s.slots(sv)),
	}
	if gameID != "" {
		a = append(a, "-gameid", gameID)
	}
	return a
}

// newWorldArgs is what a new world is made with. The server stores the
// seed and the mode in the world (and in its ServerConfig.json), so later
// starts leave them out.
func newWorldArgs(v game.Values) []string {
	a := []string{"-worldmode", modeArg(v["mode"])}
	if seed := v["seed"]; strings.TrimSpace(seed) != "" {
		a = append(a, "-worldseed", seed)
	}
	return a
}

// Fingerprint covers what the server reads from the command line when it
// starts and keeps until it stops: the world's folder, the name friends
// see and the player limit. The log file is new for every run and the
// Game ID never changes, so they are left out; a new world's mode and
// seed only count once.
func (s *server) Fingerprint(sv game.Saved, world string) string {
	h := sha256.New()
	for _, a := range s.args(sv, world, "", "") {
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
	if _, err := os.Stat(s.Program()); err != nil {
		return game.Plan{}, errors.New("The Core Keeper server isn't installed. Click Install (or Repair) first.")
	}
	if errs := s.Validate(req.Saved, req.World); len(errs) > 0 {
		return game.Plan{}, errs
	}
	if req.New {
		if errs := validateNewWorld(req.NewWorld); len(errs) > 0 {
			return game.Plan{}, errs
		}
	}
	dir := s.worlds.dataPath(req.World)
	wait := readyWaitStart
	switch {
	case req.New:
		// The server makes the world when its folder has none.
		wait = readyWaitNew
	case !s.worlds.Exists(req.World):
		// Without a world in the folder the server would quietly make a
		// new one, Normal with a random seed; saying so here is clearer.
		// A folder with nothing of the server's in it is a new world whose
		// first start stopped early (worldStore.Exists): it must be started
		// as new, with the mode and seed it was made for.
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return game.Plan{}, fmt.Errorf("The world %q hasn't been made yet: the start that was to make it stopped before the server got that far. Make it again as a new world on the Worlds page.", req.World)
		}
		return game.Plan{}, fmt.Errorf("The world %q isn't in Core Keeper's saves folder any more. Restore one of its backups on the Worlds page, or make a new world.", req.World)
	default:
		moved, err := tidyWorld(dir)
		if err != nil {
			return game.Plan{}, err
		}
		if moved != "" {
			s.log.Printf("world %q: moved %s to worlds/0.world.gzip, where the server looks for it", req.World, moved)
		}
	}
	// A new world's folder is made here, so the first start doesn't depend
	// on the server making it (seen on Linux only), and the world shows on
	// the Worlds page straight away. If that start then fails before the
	// server writes anything, the empty folder doesn't count as a world
	// (worldStore.Exists), so the next start is still a new world's.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return game.Plan{}, err
	}
	id, err := s.gameID()
	if err != nil {
		return game.Plan{}, err
	}
	// The server writes these next to its program at every start. An old
	// one left from the last run would show the owner, or anything that
	// reads it, last run's details.
	for _, f := range []string{gameInfoFile, gameIDFile} {
		if err := os.Remove(filepath.Join(s.env.Dirs.Server, f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.log.Printf("couldn't remove the old %s: %v", f, err)
		}
	}
	a := s.args(req.Saved, req.World, id, req.LogPath)
	if req.New {
		a = append(a, newWorldArgs(req.NewWorld)...)
	}
	return game.Plan{
		Exe:         s.Program(),
		Args:        a,
		Dir:         s.env.Dirs.Server,
		Env:         s.extraEnv(),
		Output:      game.LogArg,
		LogPath:     req.LogPath,
		ReadyWait:   wait,
		Fingerprint: s.Fingerprint(req.Saved, req.World),
		Record:      game.Record{Server: s.value(req.Saved, "serverName")},
	}, nil
}

// The files the server writes next to its program at each start.
const (
	gameInfoFile = "GameInfo.txt"
	gameIDFile   = "GameID.txt"
)

// extraEnv is the environment the server needs on top of the app's. Only the
// Linux server needs anything: Steam's library folder, as the shipped
// _launch.sh sets it. Linux is only used by the real-server check; it
// also needs a display (Xvfb), which the check starts.
func (s *server) extraEnv() []string {
	if runtime.GOOS != "linux" || s.env.Fake != nil {
		return nil
	}
	return []string{"LD_LIBRARY_PATH=" + filepath.Join(s.env.Dirs.Server, "linux64")}
}

// gameID is the instance's Game ID, made the first time a world starts
// and kept in the instance's settings. Friends join with it, so it stays
// the same for every world and every start.
func (s *server) gameID() (string, error) {
	if id := s.store.Get().State[gameIDKey]; validGameID(id) {
		return id, nil
	}
	var id string
	err := s.store.Save(func(sv *game.Saved) error {
		if cur := sv.State[gameIDKey]; validGameID(cur) {
			id = cur // made by another start meanwhile
			return nil
		}
		n, err := newGameID()
		if err != nil {
			return err
		}
		if sv.State == nil {
			sv.State = game.Values{}
		}
		sv.State[gameIDKey] = n
		id = n
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("couldn't keep the server's Game ID: %w", err)
	}
	return id, nil
}

// gameIDChars are the characters a Game ID may have: letters and digits
// without Y, y, x, 0 and O (the server's ARGUMENTS.txt).
const gameIDChars = "123456789ABCDEFGHIJKLMNPQRSTUVWXZabcdefghijklmnopqrstuvwz"

// gameIDLen is the length of the Game IDs the app makes: the longest the
// server takes (15 to 28), so nobody can guess one. The Game ID is all a
// player needs to join (the README: "the game ID alone provides all
// capabilities a password would").
const gameIDLen = 28

func newGameID() (string, error) {
	var b strings.Builder
	max := big.NewInt(int64(len(gameIDChars)))
	for range gameIDLen {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b.WriteByte(gameIDChars[n.Int64()])
	}
	return b.String(), nil
}

// validGameID says whether the server would take id as its Game ID; it
// makes a random one of its own for an id it can't use.
func validGameID(id string) bool {
	if len(id) < 15 || len(id) > 28 {
		return false
	}
	for _, r := range id {
		if !strings.ContainsRune(gameIDChars, r) {
			return false
		}
	}
	return true
}

// updateOnStart runs SteamCMD before a start. Steam downloads only when a
// newer build is out. If SteamCMD is busy with another game's download, or
// the update fails, the start goes on with the build that's installed: a
// server that is a version behind beats no server.
func (s *server) updateOnStart(ctx context.Context, step func(string, float64)) {
	step("Checking for Core Keeper server updates…", -1)
	err := s.Installer().Install(install.SkipIfBusy(ctx), false, func(p game.Progress) { step(p.Step, p.Percent) })
	switch {
	case err == nil:
	case ctx.Err() != nil:
	default:
		step("Couldn't check for server updates ("+err.Error()+"). Starting the version that's installed.", -1)
		s.log.Printf("update on start: %v", err)
	}
}
