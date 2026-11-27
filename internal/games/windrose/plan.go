package windrose

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/install"
)

// How long a start waits for the server to say it's ready before the
// manager's fallback (it adds 5 minutes, spec 4.3). A world that is there
// already loads in well under a minute on the publisher's 2-core
// hardware (FACTS.md); a new one is made and loaded first.
const (
	readyWaitNew   = 10 * time.Minute
	readyWaitStart = 5 * time.Minute
)

// serverArgs is the whole command line: -log, as the shipped
// StartServerForeground.bat starts it. Everything else the server reads
// from ServerDescription.json. -log also gives the server its own console
// (its log window), which is where the app's Ctrl+C goes (stop.go).
var serverArgs = []string{"-log"}

// Keys in the instance's State (servers.json) besides the world names.
const (
	stateInvite = "inviteCode"       // the invite code the settings file had at the last start
	stateDirect = "directConnection" // "true" when the file has UseDirectConnection on
)

// Fingerprint covers what the server reads from ServerDescription.json when
// it starts and keeps until it stops: the name friends see, the password,
// the player limit, the region and the world. "" is the selected world.
func (s *server) Fingerprint(sv game.Saved, world string) string {
	if world == "" {
		world = sv.World
	}
	h := sha256.New()
	for _, v := range []string{s.value(sv, "serverName"), sv.Server["password"], strconv.Itoa(s.players(sv)), s.region(sv), world} {
		io.WriteString(h, v+"\x00")
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Plan says how to start the server, after writing the app's settings
// into ServerDescription.json. With req.Update it first lets SteamCMD
// fetch a newer build, if Steam has one; a start never waits for another
// game's download (spec 2.3).
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
		return game.Plan{}, errors.New("The Windrose server isn't installed. Click Install (or Repair) first.")
	}
	world := req.World
	if world == "" {
		world = req.Saved.World
	}
	if errs := s.Validate(req.Saved, world); len(errs) > 0 {
		return game.Plan{}, errs
	}

	path := s.descriptionPath()
	madeFirst := false
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		if err := s.firstRun(ctx, step); err != nil {
			return game.Plan{}, err
		}
		madeFirst = true
	}
	d, err := readDescription(path)
	if err != nil {
		return game.Plan{}, fmt.Errorf("The Windrose server's settings file (%s) can't be used: %v. Fix it, or move it aside and start again to let the server make a new one (friends would then need a new invite code).", path, err)
	}
	id, isNew, err := s.worldFor(req.New, world, d, madeFirst)
	if err != nil {
		return game.Plan{}, err
	}

	sv := req.Saved
	password := sv.Server["password"]
	sets := []struct {
		key string
		v   any
	}{
		{keyServerName, s.value(sv, "serverName")},
		{keyPassword, password},
		// The publisher: true when a password is set, false when it is
		// empty, "otherwise it may cause unexpected behavior".
		{keyProtected, password != ""},
		{keyMaxPlayers, s.players(sv)},
		{keyRegion, s.region(sv)},
		{keyWorld, id},
	}
	for _, kv := range sets {
		if err := d.pers.set(kv.key, kv.v); err != nil {
			return game.Plan{}, err
		}
	}
	if addr, changed := fixProxyAddress(d.str(keyProxy)); changed {
		s.log.Printf("P2pProxyAddress %s isn't an address of this computer any more; using %s", d.str(keyProxy), addr)
		d.pers.set(keyProxy, addr)
	}
	if err := d.write(path); err != nil {
		return game.Plan{}, fmt.Errorf("couldn't write the Windrose server's settings file: %w", err)
	}

	code, direct := d.str(keyInviteCode), d.boolean(keyDirect)
	err = s.store.Save(func(sv *game.Saved) error {
		if sv.State == nil {
			sv.State = game.Values{}
		}
		if code != "" {
			sv.State[stateInvite] = code
		}
		if direct {
			sv.State[stateDirect] = "true"
		} else {
			delete(sv.State, stateDirect)
		}
		return nil
	})
	if err != nil {
		return game.Plan{}, fmt.Errorf("couldn't keep the server's invite code: %w", err)
	}

	// The server's log from the last run would otherwise be read as this
	// run's until the server renames it itself as it starts.
	if err := s.setLogAside(); err != nil {
		s.log.Printf("couldn't move the server's last log aside: %v", err)
	}
	wait := readyWaitStart
	if isNew {
		wait = readyWaitNew
	}
	extra := map[string]json.RawMessage{}
	for k, v := range map[string]string{"inviteCode": code, "worldId": id} {
		if v != "" {
			b, _ := json.Marshal(v)
			extra[k] = b
		}
	}
	return game.Plan{
		Exe:         s.Program(),
		Args:        serverArgs,
		Dir:         s.env.Dirs.Server,
		Output:      game.GameLog,
		LogPath:     s.logPath(),
		ReadyWait:   wait,
		Fingerprint: s.Fingerprint(req.Saved, world),
		Record:      game.Record{Server: s.value(sv, "serverName"), Password: password, Extra: extra},
	}, nil
}

// worldFor is the id of the world a start loads, keeping the instance's
// State (world.<id> = name) up to date. It says whether the server will
// make the world at this start.
//
// A new world is the first start's own world when this plan ran that
// start (the server made it for nobody else, so it is used even when an
// earlier start gave the name an id), else the id an earlier start gave it
// (one that stopped before the server made the world), else a new id: the
// server makes a world when the one its file names isn't there (FACTS.md,
// "Worlds").
func (s *server) worldFor(isNew bool, world string, d *description, madeFirst bool) (string, bool, error) {
	worlds, err := s.worlds.scan()
	if err != nil {
		return "", false, fmt.Errorf("couldn't read Windrose's saves folder: %w", err)
	}
	current := strings.ToUpper(d.str(keyWorld))
	if !madeFirst {
		if err := s.keepServerMadeName(worlds, current); err != nil {
			return "", false, fmt.Errorf("couldn't keep the name of the last new world: %w", err)
		}
	}
	st := s.store.Get().State
	names := s.worlds.named(st, worlds, current)
	known := stateNames(st)
	for id, n := range names {
		if n != world {
			continue
		}
		if known[id] != n {
			// Named by its WorldName or a name made from it
			// (fallbackNames), or in a plan whose first start just made
			// the file, by the rule for a new world the server made under
			// its own id (named): keep that name, so the world stays the
			// same one whatever happens to the file or to other worlds.
			err := s.store.Save(func(sv *game.Saved) error {
				if sv.State == nil {
					sv.State = game.Values{}
				}
				if p := strings.ToUpper(sv.State[statePlanned]); p != "" && known[p] == n && worlds[p] == nil {
					delete(sv.State, statePrefix+p)
					delete(sv.State, statePlanned)
				}
				sv.State[statePrefix+id] = n
				return nil
			})
			if err != nil {
				return "", false, err
			}
		}
		return worlds[id].id, false, nil
	}
	if !isNew {
		return "", false, fmt.Errorf("The world %q isn't in Windrose's saves folder any more. Restore one of its backups on the Worlds page, or make a new world.", world)
	}
	id, made := "", true
	if madeFirst && current != "" && worlds[current] != nil && known[current] == "" {
		id, made = current, false
	} else {
		for sid, n := range known {
			if n == world && worlds[sid] == nil {
				id = sid
			}
		}
	}
	if id == "" {
		id = newWorldID()
	}
	err = s.store.Save(func(sv *game.Saved) error {
		if sv.State == nil {
			sv.State = game.Values{}
		}
		sv.State[statePrefix+id] = world
		sv.State[statePlanned] = id
		return nil
	})
	if err != nil {
		return "", false, fmt.Errorf("couldn't keep the new world's name: %w", err)
	}
	return id, made, nil
}

// keepServerMadeName keeps, for good, the name of a new world the server
// made under an id of its own at the last start (madeUnderOwnID). It runs
// before the plan looks at the world it starts, whichever that is: named
// works the name out only while State's "planned" is that world's, and a
// plan for another new world moves "planned" on. The world would then be
// listed under its WorldName, and its name would be left on the planned
// id, which isn't on disk: picking that name again would have the server
// make yet another, empty, world.
//
// A plan whose first start has just made the settings file skips this:
// the file then selects that start's own world, not one an earlier plan
// planned.
func (s *server) keepServerMadeName(worlds map[string]*found, current string) error {
	if madeUnderOwnID(s.store.Get().State, worlds, current) == "" {
		return nil
	}
	return s.store.Save(func(sv *game.Saved) error {
		p := madeUnderOwnID(sv.State, worlds, current)
		if p == "" {
			return nil
		}
		sv.State[statePrefix+current] = stateNames(sv.State)[p]
		for k := range sv.State {
			if strings.EqualFold(k, statePrefix+p) {
				delete(sv.State, k)
			}
		}
		delete(sv.State, statePlanned)
		return nil
	})
}

// setLogAside renames the last run's R5.log the way the server itself does
// when it starts (R5-backup-<date>.log, FACTS.md). Doing it first means the
// app, which starts following the file as soon as the server is launched,
// never reads the last run's lines as this run's.
func (s *server) setLogAside() error {
	path := s.logPath()
	st, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	stamp := st.ModTime().UTC().Format("2006.01.02-15.04.05")
	for i := 0; ; i++ {
		name := "R5-backup-" + stamp + ".log"
		if i > 0 {
			name = fmt.Sprintf("R5-backup-%s-%d.log", stamp, i)
		}
		dest := filepath.Join(dir, name)
		if _, err := os.Stat(dest); errors.Is(err, fs.ErrNotExist) {
			return os.Rename(path, dest)
		}
	}
}

// updateOnStart runs SteamCMD before a start. Steam downloads only when a
// newer build is out. If SteamCMD is busy with another game's download, or
// the update fails, the start goes on with the build that's installed;
// friends whose game updated already may then not get in until the next
// start updates it.
func (s *server) updateOnStart(ctx context.Context, step func(string, float64)) {
	step("Checking for Windrose server updates…", -1)
	err := s.steam().Install(install.SkipIfBusy(ctx), false, func(p game.Progress) { step(p.Step, p.Percent) })
	switch {
	case err == nil:
	case ctx.Err() != nil:
	default:
		step("Couldn't check for server updates ("+err.Error()+"). Starting the version that's installed: friends with a newer Windrose can't join until it is updated.", -1)
		s.log.Printf("update on start: %v", err)
	}
}

// The addresses of this computer, and the one it reaches the internet
// from. Tests replace them.
var (
	localIPs = func() []net.IP {
		addrs, err := net.InterfaceAddrs()
		if err != nil {
			return nil
		}
		var out []net.IP
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				out = append(out, n.IP)
			}
		}
		return out
	}
	outboundIP = func() net.IP {
		// Connecting a UDP socket sends nothing; it only makes the system
		// choose the address it would send from. 203.0.113.1 is an
		// address set aside for examples, routed like any other.
		c, err := net.Dial("udp4", "203.0.113.1:9")
		if err != nil {
			return nil
		}
		defer c.Close()
		if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
			return a.IP
		}
		return nil
	}
)

// fixProxyAddress keeps P2pProxyAddress usable. The server fills it in at
// its first start with this computer's network address and offers it to
// friends' games as where to connect (FACTS.md); a home computer's address
// can change (a new one from the router), and the server would then offer
// one that leads nowhere. An address this computer still has, a loopback
// or an empty one is kept as it is.
func fixProxyAddress(addr string) (string, bool) {
	ip := net.ParseIP(strings.TrimSpace(addr))
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() {
		return addr, false
	}
	for _, l := range localIPs() {
		if l.Equal(ip) {
			return addr, false
		}
	}
	out := outboundIP()
	if out == nil || out.IsLoopback() || out.IsUnspecified() {
		return addr, false
	}
	return out.String(), true
}
