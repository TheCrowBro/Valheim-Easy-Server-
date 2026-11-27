package terraria

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
)

// configName is the config file the Plan writes and gives the server with
// -config. It sits in the server folder, outside the system's own folder
// that an update replaces.
const configName = "serverconfig.txt"

// How long a start waits for "Server started" before the manager's
// fallback (it adds 5 minutes, spec 4.3). Making a large world takes
// minutes on an older computer; loading one takes well under one.
const (
	readyWaitNew   = 15 * time.Minute
	readyWaitStart = 5 * time.Minute
)

// haveXNA says whether XNA is installed; tests replace it.
var haveXNA = xnaInstalled

// worldFile is the world's file in the saves folder.
func (s *server) worldFile(world string) string {
	return filepath.Join(s.env.Dirs.Saves, world+".wld")
}

// configLines is serverconfig.txt for a start of world, without the lines
// that make a new world. Every path is absolute: on a Mac the server runs
// inside its app bundle, and the default places for the ban list and the
// worlds would be inside the bundle that the next update replaces.
func (s *server) configLines(sv game.Saved, world string) []string {
	return []string{
		"world=" + s.worldFile(world),
		"worldpath=" + s.env.Dirs.Saves,
		"port=" + strconv.Itoa(port(sv)),
		"maxplayers=" + strconv.Itoa(sv.Server.Int("maxPlayers", 8)),
		"password=" + sv.Server["password"],
		"motd=" + s.value(sv, "serverName"),
		// The app opens the router itself (spec 6.5); the server's own
		// UPnP would map the same port a second time.
		"upnp=0",
		"secure=1",
		// English whatever the computer's language, so the Monitor can
		// read the console.
		"language=en-US",
		"banlist=" + filepath.Join(s.env.Dirs.Server, "banlist.txt"),
	}
}

// newWorldLines make the world when it isn't there yet. The server reads
// them only when the world file is missing.
func newWorldLines(world string, v game.Values) []string {
	size := sizeCodes[v["size"]]
	if size == "" {
		size = sizeCodes["medium"]
	}
	diff := difficultyCodes[v["difficulty"]]
	if diff == "" {
		diff = difficultyCodes["classic"]
	}
	lines := []string{"autocreate=" + size, "worldname=" + world, "difficulty=" + diff}
	if seed := v["seed"]; seed != "" {
		lines = append(lines, "seed="+seed)
	}
	return lines
}

// configText is the whole file, with a note for anyone who opens it.
func configText(lines []string) string {
	var b strings.Builder
	b.WriteString("# Written by Easy Game Server before each start of the Terraria server.\n")
	b.WriteString("# Change the settings in the app: changes made here are replaced.\n")
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	return b.String()
}

func port(sv game.Saved) int {
	if p := sv.Ports["game"]; p > 0 {
		return p
	}
	return defaultPort
}

// args is the command line: the config file says everything else, apart
// from where the server keeps its own files. Without -savedirectory it
// keeps them in the owner's own Terraria folder (Documents\My Games\Terraria
// on Windows, ~/Library/Application Support/Terraria on a Mac): when it
// makes a world it reads and rewrites favorites.json there, the list the
// owner's own game keeps of its favourite worlds and characters
// (FACTS.md, 4). So it is pointed at the server folder, beside the config
// and the ban list.
func (s *server) args() []string {
	return []string{"-config", filepath.Join(s.env.Dirs.Server, configName), "-savedirectory", s.env.Dirs.Server}
}

// Fingerprint covers everything the server reads when it starts, so a
// change to any of it says "restart to apply". The new-world lines are
// left out: they only matter the first time.
func (s *server) Fingerprint(sv game.Saved, world string) string {
	h := sha256.New()
	for _, l := range append(s.configLines(sv, world), s.args()...) {
		io.WriteString(h, l+"\x00")
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// launch is how the program is started on goos: the folder it runs in and
// the environment it needs. It does what the zip's TerrariaServer script
// would do before starting the program, so the app can start the program
// itself (spec 2.4).
func launch(program, goos string) (dir string, env []string) {
	dir = filepath.Dir(program)
	if goos == "darwin" {
		// The script cds into Contents/MacOS and adds ./osx to
		// DYLD_LIBRARY_PATH, where libGNS_Wrapper, libSDL3, libFNA3D and
		// libFAudio are: Mono loads them by their bare names.
		env = []string{"DYLD_LIBRARY_PATH=" + filepath.Join(dir, "osx")}
	}
	return dir, env
}

// Plan writes serverconfig.txt and says how to start the server. With
// req.Update it first installs a newer server, if terraria.org has one.
func (s *server) Plan(ctx context.Context, req game.PlanRequest) (game.Plan, error) {
	step := req.Step
	if step == nil {
		step = func(string, float64) {}
	}
	if req.Update && s.env.Fake == nil {
		s.updateOnStart(ctx, step)
	}
	if _, err := os.Stat(s.Program()); err != nil {
		return game.Plan{}, errors.New("The Terraria server isn't installed. Click Install (or Repair) first.")
	}
	if s.env.Fake == nil && runtime.GOOS == "windows" && !haveXNA() {
		return game.Plan{}, errors.New("Terraria's Windows server needs Microsoft XNA Framework 4.0, which isn't installed on this computer. Click Repair to install it.")
	}
	errs := s.Validate(req.Saved, req.World)
	if req.New {
		for k, v := range validateNewWorld(req.NewWorld) {
			errs[k] = v
		}
	}
	if len(errs) > 0 {
		return game.Plan{}, errs
	}

	lines := s.configLines(req.Saved, req.World)
	wait := readyWaitStart
	if req.New {
		lines = append(lines, newWorldLines(req.World, req.NewWorld)...)
		wait = readyWaitNew
	} else if _, err := os.Stat(s.worldFile(req.World)); err != nil {
		// Without autocreate the server would stop with "Load failed!";
		// saying why here is clearer.
		return game.Plan{}, fmt.Errorf("The world %q isn't in Terraria's saves folder any more. Restore one of its backups on the Worlds page, or make a new world.", req.World)
	}
	if err := os.MkdirAll(s.env.Dirs.Saves, 0o755); err != nil {
		return game.Plan{}, err
	}
	// It holds the password, so only this user may read it.
	if err := writeFileAtomic(filepath.Join(s.env.Dirs.Server, configName), []byte(configText(lines)), 0o600); err != nil {
		return game.Plan{}, fmt.Errorf("couldn't write Terraria's %s: %w", configName, err)
	}

	program := s.Program()
	goos := runtime.GOOS
	if s.env.Fake != nil {
		goos = "fake"
	}
	dir, env := launch(program, goos)
	s.Build() // notes the installed version for Join
	return game.Plan{
		Exe:         program,
		Args:        s.args(),
		Dir:         dir,
		Env:         env,
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

// writeFileAtomic writes a file whole or not at all, so a crash never
// leaves the server half a config.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), perm); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
