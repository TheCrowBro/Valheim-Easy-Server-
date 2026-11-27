package palworld

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
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

// defaultSettings is DefaultPalWorldSettings.ini as the 1.0.5 server ships
// it (FACTS.md). The plan starts from the installed copy, and from this one
// only when that is missing or has no options line, so a stand-in install
// and a damaged one still get every option.
//
//go:embed default/DefaultPalWorldSettings.ini
var defaultSettings []byte

// readyWait is how long a start waits for the server to say it's up before
// the manager's fallback (it adds 5 minutes, spec 4.3). It leaves room for a
// slow disk, which the docs warn against, and a big world; the real-server
// check records how long a start really takes (FACTS.md).
const readyWait = 5 * time.Minute

// The REST API's user is always "admin"; its password is AdminPassword
// (FACTS.md). The app makes a new one for every run and keeps it in the
// run record, so the probe and the stop work after a takeover too.
const (
	restUser   = "admin"
	restSecret = "rest"
)

// newSecret makes a run's admin password: letters and digits only, so it
// never needs quoting in the settings file. Tests replace it.
var newSecret = func() string {
	const letters = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 24)
	rand.Read(b)
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return string(b)
}

// ports are the instance's game and REST ports.
func ports(sv game.Saved) (gamePort, restPort int) {
	gamePort, restPort = sv.Ports["game"], sv.Ports["rest"]
	if gamePort <= 0 {
		gamePort = defaultGamePort
	}
	if restPort <= 0 {
		restPort = defaultRESTPort
	}
	return gamePort, restPort
}

// args is the command line, as Steam's launcher passes it on: the project
// name "Pal" first (as PalServer.sh does), then the game port and the
// player limit (FACTS.md). The 1.0 docs say leaving out
// -useperfthreads -NoAsyncLoadingThread -UseMultithreadForDS may run
// better, so they are left out.
func args(sv game.Saved) []string {
	gamePort, _ := ports(sv)
	return []string{"Pal", "-port=" + strconv.Itoa(gamePort), "-players=" + strconv.Itoa(players(sv))}
}

// settings are the OptionSettings values the plan sets for world (the
// admin password apart, which is new every run).
func (s *server) settings(sv game.Saved, world string) [][2]string {
	_, restPort := ports(sv)
	out := [][2]string{
		{"ServerName", quote(s.value(sv, "serverName"))},
		{"ServerPassword", quote(sv.Server["password"])},
		{"ServerPlayerMaxNum", strconv.Itoa(players(sv))},
		{"RESTAPIEnabled", boolText(true)},
		{"RESTAPIPort", strconv.Itoa(restPort)},
		// RCON is off: the REST API does all the app needs, and RCON's
		// port would be one more for nobody (FACTS.md).
		{"RCONEnabled", boolText(false)},
		// The Monitor reads the log as text, so a hand-edited Json can't
		// leave it reading nothing.
		{"LogFormatType", "Text"},
	}
	for _, o := range worldOptions {
		out = append(out, [2]string{o.ini, o.write(worldValue(sv, world, o.field.Key))})
	}
	return out
}

// Fingerprint covers what the server reads when it starts: its command
// line, the settings the plan writes and the world, so a change to any of
// it says "restart to apply". The admin password is left out: it is new
// every run, and changing it is the app's business.
func (s *server) Fingerprint(sv game.Saved, world string) string {
	h := sha256.New()
	for _, a := range args(sv) {
		io.WriteString(h, a+"\x00")
	}
	for _, kv := range s.settings(sv, world) {
		io.WriteString(h, kv[0]+"="+kv[1]+"\x00")
	}
	io.WriteString(h, "world="+world)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Plan says how to start the server. With req.Update it first lets SteamCMD
// fetch a newer build, if Steam has one: friends can only join a server of
// exactly their game's version. A start never waits for another game's
// download (spec 2.3).
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
	id, err := s.worldID(req.World, req.New)
	if err != nil {
		return game.Plan{}, err
	}
	admin := newSecret()
	if err := s.writeSettings(req.Saved, req.World, admin); err != nil {
		return game.Plan{}, fmt.Errorf("couldn't write Palworld's settings: %w", err)
	}
	if err := s.writeWorldChoice(id); err != nil {
		return game.Plan{}, fmt.Errorf("couldn't choose the world in Palworld's settings: %w", err)
	}
	gamePort, restPort := ports(req.Saved)
	return game.Plan{
		Exe:  s.Program(),
		Args: args(req.Saved),
		// The server's own folder: the Steam library it loads reads
		// steam_appid.txt from the folder it is started in (FACTS.md).
		Dir:         filepath.Dir(s.Program()),
		Output:      game.Capture,
		LogPath:     req.LogPath,
		ReadyWait:   readyWait,
		Fingerprint: s.Fingerprint(req.Saved, req.World),
		Record: game.Record{
			Server:   s.value(req.Saved, "serverName"),
			Port:     gamePort,
			Password: req.Saved.Server["password"],
			Ports:    map[string]int{"game": gamePort, "rest": restPort},
			Secrets:  map[string]string{restSecret: admin},
		},
	}, nil
}

// updateOnStart runs SteamCMD before a start; Steam downloads only when a
// newer build is out. If SteamCMD is busy with another game's download, or
// the update fails, the start goes on with the build that's installed, and
// says that friends with a newer game may not get in until the next start.
func (s *server) updateOnStart(ctx context.Context, step func(string, float64)) {
	step("Checking for Palworld server updates…", -1)
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

// worldID finds the id of the world to start, or picks a new one for a new
// world, and records it in State. Picking the id here, rather than leaving
// DedicatedServerName empty for the server to fill in, means the app knows
// the new world's folder before the server has saved anything, so its name
// is never lost (FACTS.md).
func (s *server) worldID(world string, isNew bool) (string, error) {
	current := s.currentWorldChoice()
	var id string
	err := s.store.Save(func(sv *game.Saved) error {
		if sv.State == nil {
			sv.State = game.Values{}
		}
		s.adoptServerChoice(sv.State, current)
		found, ok := s.worlds.idIn(sv.State, world)
		switch {
		case ok:
			id = found
		case isNew:
			id = newWorldID()
			sv.State[statePrefix+id] = world
		default:
			return fmt.Errorf("The world %q isn't in Palworld's saves folder any more. Restore one of its backups on the Worlds page, or make a new world.", world)
		}
		if !isNew {
			if _, err := os.Stat(filepath.Join(s.savesDir(), id, levelFile)); err != nil {
				return fmt.Errorf("The world %q isn't in Palworld's saves folder any more. Restore one of its backups on the Worlds page, or make a new world.", world)
			}
		}
		sv.State[statePlanned] = id
		return nil
	})
	return id, err
}

// adoptServerChoice handles a server that made its world under an id of
// its own instead of the one the last start gave it: the planned folder
// never appeared, while GameUserSettings.ini names another folder that has
// a world and no name. That world is the one the last start made, so it
// takes the planned world's name. st is changed in place.
func (s *server) adoptServerChoice(st game.Values, current string) {
	planned := st[statePlanned]
	if planned == "" || current == "" || strings.EqualFold(planned, current) || !reWorldID.MatchString(current) {
		return
	}
	if _, err := os.Stat(filepath.Join(s.savesDir(), planned, levelFile)); err == nil {
		return
	}
	if _, err := os.Stat(filepath.Join(s.savesDir(), current, levelFile)); err != nil {
		return
	}
	cur := strings.ToUpper(current)
	if names(st)[cur] != "" {
		return
	}
	name := st[statePrefix+planned]
	if name == "" {
		return
	}
	st[statePrefix+cur] = name
	delete(st, statePrefix+planned)
	st[statePlanned] = cur
	s.log.Printf("the server made world %q under its own id %s (the app had given it %s); the name now follows it", name, cur, planned)
}

// writeSettings writes PalWorldSettings.ini for this start: the copy the
// docs say to make of DefaultPalWorldSettings.ini when there is none yet,
// with the app's values set and everything else kept.
func (s *server) writeSettings(sv game.Saved, world, admin string) error {
	path := s.settingsPath()
	var f *iniFile
	if b, err := os.ReadFile(path); err == nil {
		f = parseINI(string(b))
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	opts, ok := options(nil), false
	if f != nil {
		opts, ok = f.optionsLine()
	}
	if !ok {
		def := s.defaults()
		if f == nil {
			f = fromDefaults(def)
		}
		opts, _ = def.optionsLine()
	}
	for _, kv := range s.settings(sv, world) {
		opts = opts.set(kv[0], kv[1])
	}
	opts = opts.set("AdminPassword", quote(admin))
	f.setOptions(opts)
	return writeFile(path, []byte(f.String()), 0o600)
}

// defaults is the installed DefaultPalWorldSettings.ini, or the copy built
// into the app when that is missing or has no options line.
func (s *server) defaults() *iniFile {
	if b, err := os.ReadFile(s.defaultSettingsPath()); err == nil {
		if f := parseINI(string(b)); hasOptions(f) {
			return f
		}
	}
	return parseINI(string(defaultSettings))
}

// settingsNote heads a PalWorldSettings.ini the app makes, in place of the
// sample's own comments, which say that changes to the file do nothing.
var settingsNote = []string{
	"; Palworld's server settings. The app that runs this server sets the server name, password,",
	"; player limit, REST API and the world's options here before each start; anything else you",
	"; change in this file is kept.",
}

// fromDefaults is the sample made into a settings file: its comment lines
// are replaced by settingsNote, everything else is kept.
func fromDefaults(def *iniFile) *iniFile {
	f := &iniFile{eol: def.eol, lines: append([]string(nil), settingsNote...)}
	for _, l := range def.lines {
		if !strings.HasPrefix(strings.TrimSpace(l), ";") {
			f.lines = append(f.lines, l)
		}
	}
	return f
}

func hasOptions(f *iniFile) bool {
	_, ok := f.optionsLine()
	return ok
}

// currentWorldChoice is the world GameUserSettings.ini names now ("" when
// it names none).
func (s *server) currentWorldChoice() string {
	b, err := os.ReadFile(s.userSettingsPath())
	if err != nil {
		return ""
	}
	_, v := parseINI(string(b)).find(worldKey)
	return strings.TrimSpace(v)
}

// writeWorldChoice sets DedicatedServerName, which says which world the
// server loads, keeping the rest of GameUserSettings.ini (the server makes
// the rest itself on its first start).
func (s *server) writeWorldChoice(id string) error {
	path := s.userSettingsPath()
	f := parseINI("")
	if b, err := os.ReadFile(path); err == nil {
		f = parseINI(string(b))
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f.setKey(localSection, worldKey, id)
	return writeFile(path, []byte(f.String()), 0o644)
}

// writeFile replaces path in one step, so the server never reads a file
// half written.
func writeFile(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".egs-*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	os.Chmod(tmp.Name(), perm)
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
