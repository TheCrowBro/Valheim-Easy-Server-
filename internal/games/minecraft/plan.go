package minecraft

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
)

// newSecret makes the RCON password of one run. Tests replace it to get the
// same server.properties every time.
var newSecret = rand.Text

// now is the clock the plan writes into eula.txt and the state; tests
// replace it.
var now = time.Now

// readyWait is how long a start waits for the "Done" line before the
// manager's fallback. A new world on an old computer takes a while, and
// the first start after an install also unpacks the jar's libraries.
var readyWait = 3 * time.Minute

// Plan writes the server's files and gives the command line. It never
// starts anything itself.
func (s *server) Plan(ctx context.Context, req game.PlanRequest) (game.Plan, error) {
	step := req.Step
	if step == nil {
		step = func(string, float64) {}
	}
	saved := req.Saved
	if why := saved.State[stateUnsupported]; why != "" && runtime.GOOS == "darwin" && s.env.Fake == nil {
		return game.Plan{}, errors.New(why + ".")
	}
	if saved.State[stateEULA] == "" {
		return game.Plan{}, errors.New("Minecraft's server needs you to agree to the Minecraft EULA (https://aka.ms/MinecraftEULA) before it starts. Tick \"I agree\" on Minecraft's Settings tab.")
	}
	if errs := s.Validate(saved, req.World); len(errs) > 0 {
		return game.Plan{}, errs
	}
	c := readSettings(saved, req.World)
	if c.port == 0 || c.rconPort == 0 {
		return game.Plan{}, errors.New("Minecraft has no ports picked yet; pick new ports on its page.")
	}

	if req.Update && s.env.Fake == nil {
		if err := s.update(ctx, step); err != nil {
			if ctx.Err() != nil {
				return game.Plan{}, ctx.Err()
			}
			if _, ok := s.Build(); !ok {
				return game.Plan{}, err
			}
			// What's installed still runs; the next start checks again.
			msg := "Couldn't check for Minecraft updates (" + strings.TrimSuffix(err.Error(), ".") + "). Starting the version that's installed."
			step(msg, -1)
			if s.env.Log != nil {
				s.env.Log.Print(msg)
			}
		}
	}
	if !isFile(s.jarPath()) || !isFile(s.Program()) {
		return game.Plan{}, errNotInstalled
	}

	secret := newSecret()
	allow, err := s.prepareAllowList(saved)
	if err != nil {
		return game.Plan{}, err
	}
	if err := s.writeProperties(c, req, secret, allow); err != nil {
		return game.Plan{}, fmt.Errorf("Couldn't write Minecraft's server.properties: %v", err)
	}
	if err := writeEULA(s.env.Dirs.Server, saved.State[stateEULA]); err != nil {
		return game.Plan{}, fmt.Errorf("Couldn't write Minecraft's eula.txt: %v", err)
	}

	onOff := allowOff
	if allow {
		onOff = allowOn
	}
	onOffJSON, _ := json.Marshal(onOff)
	modeJSON, _ := json.Marshal(c.allowList)
	return game.Plan{
		Exe:         s.Program(),
		Args:        s.args(c, req.World),
		Dir:         s.env.Dirs.Server,
		Output:      game.Capture,
		LogPath:     req.LogPath,
		ReadyWait:   readyWait,
		Fingerprint: s.Fingerprint(saved, req.World),
		Record: game.Record{
			Server:  c.name,
			Port:    c.port,
			Ports:   map[string]int{roleGame: c.port, roleRCON: c.rconPort},
			Secrets: map[string]string{roleRCON: secret},
			// Whether the allow list was on at the start, and the owner's
			// setting, for the Monitor: in "auto" it turns the list on
			// when a player comes in from the internet while it is off.
			Extra: map[string]json.RawMessage{extraAllowList: onOffJSON, extraAllowMode: modeJSON},
		},
	}, nil
}

// The run record's own fields (Record.Extra) for the allow list: whether
// it was on when the run started, and the owner's setting then.
const (
	extraAllowList = "allowList"
	extraAllowMode = "allowListMode"
)

// args is the command line (spec 5.2): the private Java, a memory limit,
// the jar, and the world. The game port is given on the command line too,
// which wins over server.properties.
func (s *server) args(c settings, world string) []string {
	return []string{
		"-Xms512M", "-Xmx" + strconv.Itoa(c.memoryMB) + "M",
		"-jar", s.jarPath(),
		"--nogui",
		"--universe", s.env.Dirs.Saves,
		"--world", world,
		"--port", strconv.Itoa(c.port),
	}
}

// levelTypes in server.properties are namespaced ("minecraft:normal"), as
// 26.3 writes them itself.
func levelType(v string) string {
	if !choiceOK(levelTypes, v) {
		v = defLevelType
	}
	return "minecraft:" + v
}

// writeProperties sets the keys the app looks after in server.properties
// and keeps every other key, comment and the order as they are.
func (s *server) writeProperties(c settings, req game.PlanRequest, secret string, allow bool) error {
	path := filepath.Join(s.env.Dirs.Server, "server.properties")
	text := "#Minecraft server properties\n"
	if b, err := os.ReadFile(path); err == nil {
		text = string(b)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	p := parseProperties(text)
	set := []struct{ k, v string }{
		{"server-port", strconv.Itoa(c.port)},
		{"motd", c.name},
		{"max-players", strconv.Itoa(c.maxPlayers)},
		{"difficulty", c.difficulty},
		{"gamemode", c.gamemode},
		{"view-distance", strconv.Itoa(c.view)},
		{"white-list", strconv.FormatBool(allow)},
		{"enforce-whitelist", strconv.FormatBool(allow)},
		{"enable-rcon", "true"},
		{"rcon.port", strconv.Itoa(c.rconPort)},
		{"rcon.password", secret},
		{"online-mode", "true"},
		{"enable-query", "false"},
		// The world's own name inside level.dat comes from level-name when
		// it's made, so it matches the name the app shows (--world picks
		// the folder).
		{"level-name", req.World},
		// The allow list's "auto" turns on when a player logs in from the
		// internet, which the app reads from the login line's address.
		{"log-ips", "true"},
	}
	if req.New {
		// Only read when the world is made; an existing world keeps its own.
		set = append(set,
			struct{ k, v string }{"level-seed", strings.TrimSpace(req.NewWorld["seed"])},
			struct{ k, v string }{"level-type", levelType(req.NewWorld["levelType"])},
			struct{ k, v string }{"hardcore", strconv.FormatBool(req.NewWorld.Bool("hardcore"))},
		)
	}
	for _, kv := range set {
		p.Set(kv.k, kv.v)
	}
	return writeFileAtomic(path, []byte(p.String()), 0o600)
}

// writeEULA writes eula.txt as Minecraft does, saying when the owner
// agreed (agreed is RFC 3339, from the state).
func writeEULA(dir, agreed string) error {
	when, err := time.Parse(time.RFC3339, agreed)
	if err != nil {
		when = now()
	}
	text := "#By changing the setting below to TRUE you are indicating your agreement to our EULA (https://aka.ms/MinecraftEULA).\n" +
		"#" + when.UTC().Format("Mon Jan 02 15:04:05 MST 2006") + "\n" +
		"eula=true\n"
	return writeFileAtomic(filepath.Join(dir, "eula.txt"), []byte(text), 0o644)
}
