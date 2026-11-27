package vintagestory

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
)

// How long a start waits for the server to say it's ready before the
// manager's fallback (it adds 5 minutes, spec 4.3). On a 4-core computer a
// new world was ready in 30 seconds and an existing one in 18 (FACTS.md);
// these leave room for a slow computer and a big world.
const (
	readyWaitNew   = 10 * time.Minute
	readyWaitStart = 5 * time.Minute
)

// saveLine saves the world without stopping; the app types it before it
// exits with the server running (spec 2.11). "/autosavenow" is the
// server's own command for it (FACTS.md).
const saveLine = "/autosavenow"

// savesDir is where the server keeps its worlds, given --dataPath.
func savesDir(saves string) string { return filepath.Join(saves, "Saves") }

func (s *server) worldFile(world string) string {
	return filepath.Join(savesDir(s.env.Dirs.Saves), world+worldExt)
}

// args is the command line. Everything else the server reads comes from
// serverconfig.json in --dataPath, which keeps its settings, worlds, logs
// and player data in the instance's saves folder, never in the
// VintagestoryData folder the owner's own game uses.
func (s *server) args(version string) []string {
	return []string{filepath.Join(s.versionDir(version), serverDLL), "--dataPath", s.env.Dirs.Saves}
}

// Fingerprint covers what the server reads when it starts: the settings
// the app writes into serverconfig.json and the world. A change to any of
// them says "restart to apply".
func (s *server) Fingerprint(sv game.Saved, world string) string {
	h := sha256.New()
	for _, v := range []string{
		s.value(sv, "serverName"),
		sv.Server["password"],
		strconv.Itoa(sv.Server.Int("maxPlayers", 16)),
		strconv.Itoa(port(sv)),
		world,
	} {
		io.WriteString(h, v+"\x00")
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Plan says how to start the server. With req.Update it first fetches a
// newer server, if there is one.
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
	b, ok := s.Build()
	if !ok {
		return game.Plan{}, errors.New("The Vintage Story server isn't installed. Click Install (or Repair) first.")
	}
	errs := s.Validate(req.Saved, req.World)
	if req.New {
		for k, v := range newWorldProblems(req.NewWorld) {
			errs[k] = v
		}
	}
	if len(errs) > 0 {
		return game.Plan{}, errs
	}
	wait := readyWaitStart
	if req.New {
		wait = readyWaitNew
	} else if !s.worlds.Exists(req.World) {
		// Without the world the server would quietly make a new, empty one
		// under the same name; saying so here is clearer.
		return game.Plan{}, fmt.Errorf("The world %q isn't in Vintage Story's saves folder any more. Restore one of its backups on the Worlds page, or make a new world.", req.World)
	}
	args := s.args(b.Version)
	if err := os.MkdirAll(savesDir(s.env.Dirs.Saves), 0o755); err != nil {
		return game.Plan{}, err
	}
	if err := s.writeConfig(ctx, args[0], s.configValues(req), step); err != nil {
		return game.Plan{}, err
	}
	return game.Plan{
		Exe:         s.Program(),
		Args:        args,
		Dir:         s.versionDir(b.Version), // as the shipped server.sh runs it: its own Mods folder is found from here
		Output:      game.Capture,
		LogPath:     req.LogPath,
		Console:     true,
		SaveLine:    saveLine,
		ReadyWait:   wait,
		Fingerprint: s.Fingerprint(req.Saved, req.World),
		Record: game.Record{
			Server:   s.value(req.Saved, "serverName"),
			Port:     port(req.Saved),
			Password: req.Saved.Server["password"],
		},
	}, nil
}

// configValues are the serverconfig.json keys for this start.
func (s *server) configValues(req game.PlanRequest) configValues {
	v := configValues{
		ServerName: s.value(req.Saved, "serverName"),
		Port:       port(req.Saved),
		MaxClients: req.Saved.Server.Int("maxPlayers", 16),
		Password:   req.Saved.Server["password"],
		World:      req.World,
		SaveFile:   s.worldFile(req.World),
		New:        req.New,
	}
	if req.New {
		v.Seed = req.NewWorld["seed"]
		v.Style = styleOf(req.NewWorld["playStyle"])
	}
	return v
}
