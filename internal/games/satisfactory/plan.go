package satisfactory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// readyWait is how long a start waits for the server to be playable before
// the manager's fallback (it adds 5 minutes, spec 4.3). A start includes
// the server's own start-up, the app's setup through the API and loading
// the world, which takes a few minutes for a big factory on a slow disk.
const readyWait = 10 * time.Minute

// The run record's keys: the admin password the run started with (so the
// probe and the stop work after a takeover even if the settings went
// missing), and, for a new world, what the setup needs to make it.
const (
	secretAdmin   = "admin"
	extraNewWorld = "newWorld"
	extraSetUp    = "setUp"
)

// ports are the instance's game and reliable messaging ports.
func ports(sv game.Saved) (gamePort, reliablePort int) {
	gamePort, reliablePort = sv.Ports["game"], sv.Ports["reliable"]
	if gamePort <= 0 {
		gamePort = defaultGamePort
	}
	if reliablePort <= 0 {
		reliablePort = defaultReliablePort
	}
	return gamePort, reliablePort
}

// args is the command line, as Steam's launcher passes it on: the project
// name first, the two ports, the player limit (set the way the wiki and
// the widely used Docker image set it, FACTS.md), then -log and
// -unattended as Steam's launch does (-unattended keeps the server from
// ever waiting on a message box nobody can see).
func args(sv game.Saved) []string {
	gamePort, reliablePort := ports(sv)
	n := strconv.Itoa(players(sv))
	return []string{
		"FactoryGame",
		"-Port=" + strconv.Itoa(gamePort),
		"-ReliablePort=" + strconv.Itoa(reliablePort),
		"-ini:Game:[/Script/Engine.GameSession]:MaxPlayers=" + n,
		"-ini:GameUserSettings:[/Script/Engine.GameSession]:MaxPlayers=" + n,
		"-log",
		"-unattended",
	}
}

// Fingerprint covers what a start gives the server: its command line, the
// server name and password the setup applies, and the world. The admin
// password is left out: it is the app's own business, and a new one is
// applied without a restart.
func (s *server) Fingerprint(sv game.Saved, world string) string {
	h := sha256.New()
	for _, a := range args(sv) {
		io.WriteString(h, a+"\x00")
	}
	io.WriteString(h, "name="+serverName(sv)+"\x00")
	io.WriteString(h, "password="+sv.Server["password"]+"\x00")
	io.WriteString(h, "world="+world)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Plan says how to start the server. With req.Update it first lets SteamCMD
// fetch a newer build, if Steam has one: friends can only join a server of
// the same version as their game. A start never waits for another game's
// download (spec 2.3).
//
// The plan writes no files for the server: everything it is told comes on
// its command line, and the rest goes through its API once it runs
// (setup.go).
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
	if err := s.installed(); err != nil {
		return game.Plan{}, err
	}
	if errs := s.Validate(req.Saved, req.World); len(errs) > 0 {
		return game.Plan{}, errs
	}
	if err := s.setLogAside(); err != nil {
		// Not worth refusing a start for: the server renames it itself as
		// it starts, and until then the app may show a few old lines.
		s.log.Printf("couldn't move the server's last log aside: %v", err)
	}
	gamePort, reliablePort := ports(req.Saved)
	rec := game.Record{
		Server:   serverName(req.Saved),
		Port:     gamePort,
		Password: req.Saved.Server["password"],
		Ports:    map[string]int{"game": gamePort, "reliable": reliablePort},
		Secrets:  map[string]string{},
	}
	if admin := req.Saved.State[stateAdmin]; admin != "" {
		rec.Secrets[secretAdmin] = admin
	}
	if req.New {
		b, _ := json.Marshal(newWorld{StartingLocation: startingLocation(req.NewWorld)})
		rec.Extra = map[string]json.RawMessage{extraNewWorld: b}
	}
	var env []string
	if s.env.Fake != nil {
		// The stand-in keeps its saves where the real server would, in
		// %LocalAppData%; pointing that at the instance's saves folder
		// keeps fake games away from the owner's real saves.
		env = append(env, "LOCALAPPDATA="+s.env.Dirs.Saves)
	}
	return game.Plan{
		Exe:         s.Program(),
		Args:        args(req.Saved),
		Dir:         filepath.Dir(s.Program()),
		Env:         env,
		Output:      game.GameLog,
		LogPath:     s.logPath(),
		ReadyWait:   readyWait,
		Fingerprint: s.Fingerprint(req.Saved, req.World),
		Record:      rec,
	}, nil
}

// newWorld is what the setup needs to make a world the server hasn't got.
type newWorld struct {
	StartingLocation string `json:"startingLocation"`
}

// updateOnStart runs SteamCMD before a start; Steam downloads only when a
// newer build is out. If SteamCMD is busy with another game's download, or
// the update fails, the start goes on with the build that's installed, and
// says that friends with a newer game may not get in until the next start.
func (s *server) updateOnStart(ctx context.Context, step func(string, float64)) {
	step("Checking for Satisfactory server updates…", -1)
	err := s.steam().Install(install.SkipIfBusy(ctx), false, func(p game.Progress) { step(p.Step, p.Percent) })
	switch {
	case err == nil:
	case ctx.Err() != nil:
	default:
		step("Couldn't check for server updates ("+err.Error()+"). Starting the version that's installed; friends whose game is newer can't join until it updates.", -1)
		if errors.Is(err, install.ErrBusy) {
			s.log.Printf("update on start skipped: %v", err)
		} else {
			s.log.Printf("update on start failed: %v", err)
		}
	}
}

// setLogAside renames the last run's FactoryGame.log the way the server
// itself does when it starts (FactoryGame-backup-<date>.log, FACTS.md).
// Doing it first means the app, which starts following the file as soon as
// the server is launched, never reads the last run's lines as this run's.
func (s *server) setLogAside() error {
	path := s.logPath()
	st, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	stamp := st.ModTime().UTC().Format("2006.01.02-15.04.05")
	dir := filepath.Dir(path)
	for i := 0; ; i++ {
		name := "FactoryGame-backup-" + stamp + ".log"
		if i > 0 {
			name = fmt.Sprintf("FactoryGame-backup-%s_%d.log", stamp, i)
		}
		dest := filepath.Join(dir, name)
		if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
			return os.Rename(path, dest)
		}
	}
}
