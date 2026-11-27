package gametest

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/worlds"
)

// Conformance checks everything every adapter must do (spec 7.1's list).
//
// With the stand-in (the default) it runs in seconds on every system, so
// it is each adapter's first test. With o.Real it runs the same checks
// against the real server, except the scripted players (nobody joins a
// real server in CI), with waits sized for a real server (waits.go).
//
// In order: the install leaves Program() and Build(); the first start
// plans a new world; the server says it's online within 30 s; its public
// ports are in use; the script's joins, deaths and leaves become events
// (or counts, for a game that only counts); the probe answers, when the
// adapter has one; a console game's save line saves; Join has steps for
// friends; Validate takes the
// defaults and rejects a 200-character server name; Fingerprint matches
// the plan's and changes exactly when a setting marked Restart does; the
// stop chain ends the server and its world's files changed; Worlds() lists
// the world and backs it up; a second start loads the same world; after a
// takeover the stop chain still ends it (a console step gives
// ErrNoConsole and the chain moves on); the backup restores. Every stop
// step the chain tries must get through, except a console step after a
// takeover.
func Conformance(t *testing.T, g game.Game, o Options) {
	t.Helper()
	info := g.Info()
	checkInfo(t, info)
	script := o.Script
	if script == "" && !o.Real {
		script = defaultScript(info)
		o.Script = script
	}
	steps, err := parseScript(script)
	if err != nil {
		t.Fatalf("gametest: Options.Script: %v", err)
	}

	s := Start(t, g, o)
	srv := s.Server()
	checkSchema(t, srv.Schema())
	world := s.world
	s.mu.Lock()
	req, plan := s.req, s.plan
	s.mu.Unlock()
	if !req.New {
		t.Errorf("the first start wasn't planned as a new world: Worlds().Exists(%q) said yes before the server had ever run", world)
	}
	if plan.Fingerprint == "" {
		t.Errorf("the plan has no Fingerprint")
	}
	if fp := srv.Fingerprint(req.Saved, world); fp != plan.Fingerprint {
		t.Errorf("Fingerprint(settings) = %q but the plan's is %q: the panel would ask for a restart that changes nothing", fp, plan.Fingerprint)
	}

	online := 30 * time.Second
	if o.Real {
		online = max(online, plan.ReadyWait+5*time.Minute) // as the manager waits (spec 4.3)
	}
	s.WaitEvent("online", "", online)
	if !s.State().Ready {
		t.Errorf("after the online event, State().Ready is false")
	}

	checkPorts(t, s)
	if o.Real {
		t.Log("gametest: no scripted players with the real server")
	} else {
		checkPlayers(t, s, steps)
	}
	if p, ok := srv.(game.Prober); ok {
		asked := time.Now()
		if res, tries, err := probeUp(p, s.Record(), o.Real); err != nil || !res.Up {
			again := ""
			if tries > 1 {
				again = fmt.Sprintf(" (asked %d times over %v)", tries, time.Since(asked).Round(time.Second))
			}
			t.Errorf("the probe of the running server%s: Up=%v, %v", again, res.Up, err)
		}
	}
	if plan.Console {
		// Before the app exits with a console game running, it types the
		// save line and waits up to 10 s for the world to be saved (spec
		// 2.11). A real server gets longer (saveWait): its save goes on
		// after the app has gone, so what must hold is that the line
		// saves; taking longer than the app waits is noted for the game's
		// FACTS.md.
		if plan.SaveLine == "" {
			t.Errorf("a console game's plan needs a SaveLine: the line that saves without stopping, which the app types before it exits")
		} else {
			s.skip("saved")
			typed := time.Now()
			if err := s.Console(plan.SaveLine); err != nil {
				t.Errorf("typing the save line %q: %v", plan.SaveLine, err)
			} else {
				s.WaitEvent("saved", "", saveWait(o.Real))
				if took := time.Since(typed); took > appSaveWait {
					t.Logf("gametest: the save line %q saved after %v, longer than the %v the app waits for it before it exits", plan.SaveLine, took.Round(100*time.Millisecond), appSaveWait)
				}
			}
		}
	}
	checkJoin(t, s)
	saved := s.store.Get()
	checkValidate(t, srv, saved, world)
	checkFingerprint(t, srv, saved, world)

	// The stop chain ends the server, which saved the world on its way out.
	wl := srv.Worlds()
	before, beforeErr := wl.Get(world)
	if beforeErr != nil {
		t.Errorf("Worlds().Get(%q) while it runs: %v", world, beforeErr)
	}
	if last := s.Stop(); last.IsZero() {
		t.Log("gametest: the Monitor never saw the world saved (State().LastSave is zero)")
	}
	// With the app that started it, every step it tried must have gone
	// through: a step that fails here (a wrong port or password in the
	// record) would leave every stop to the chain's last resort.
	s.mu.Lock()
	for _, r := range s.stopLog {
		if r.Err != nil {
			t.Errorf("the stop chain's %q step failed (%v), so a later step had to stop the server", r.Kind, r.Err)
		}
	}
	s.mu.Unlock()
	stopped, err := wl.Get(world)
	switch {
	case err != nil:
		t.Errorf("Worlds().Get(%q) after the stop: %v", world, err)
	case beforeErr == nil && stopped.Size == before.Size && stopped.Modified.Equal(before.Modified):
		t.Errorf("stopping didn't change the world's files (%d bytes, changed %v): the stop chain must make the server save", stopped.Size, stopped.Modified)
	}
	names := worldNames(t, wl)
	if !slices.Contains(names, world) {
		t.Errorf("Worlds().List() after the first run = %q, without %q", names, world)
	}
	backup, backupErr := wl.Backup(world, "conformance")
	if backupErr != nil {
		t.Errorf("Worlds().Backup(%q): %v", world, backupErr)
	} else if w, err := wl.Get(world); err == nil && !slices.ContainsFunc(w.Backups, func(b worlds.Backup) bool { return b.ID == backup.ID }) {
		t.Errorf("the world's Backups don't list the backup just made (%q)", backup.ID)
	}

	// A second start runs the same world.
	s.StartAgain()
	s.mu.Lock()
	again := s.req
	s.mu.Unlock()
	if again.New {
		t.Errorf("the second start was planned as a new world, though %q has been played", world)
	}
	s.WaitEvent("online", "", online)
	if after := worldNames(t, wl); !slices.Equal(after, names) {
		t.Errorf("after the second start the worlds are %q, not %q: it didn't load the same world", after, names)
	}

	// After an app restart, the new copy's stop chain still ends it.
	s.Takeover()
	waitFor(t, readyAfterTakeover(o.Real), "State().Ready after the takeover replayed the log", func() bool { return s.State().Ready })
	s.Stop()
	s.mu.Lock()
	log := slices.Clone(s.stopLog)
	s.mu.Unlock()
	for i, r := range log {
		switch {
		case r.Kind != "console" && r.Err != nil:
			// Everything but the console comes from the run record, so
			// it must still work for the copy of the app that took over.
			t.Errorf("after a takeover, the stop chain's %q step failed: %v", r.Kind, r.Err)
		case r.Kind != "console":
		case !errors.Is(r.Err, game.ErrNoConsole):
			t.Errorf("after a takeover, the console step gave %v, not game.ErrNoConsole", r.Err)
		case i == len(log)-1:
			t.Errorf("after a takeover the stop chain ended at its console step: a console game's chain needs a step after it (Ctrl+C)")
		}
	}

	// The backup restores.
	if backupErr == nil {
		replayed, _ := wl.Get(world)
		if err := wl.Restore(world, backup.ID); err != nil {
			t.Errorf("Worlds().Restore(%q, %q): %v", world, backup.ID, err)
		} else if w, err := wl.Get(world); err != nil {
			t.Errorf("Worlds().Get(%q) after the restore: %v", world, err)
		} else if w.Size != stopped.Size {
			t.Errorf("after restoring the backup the world has %d bytes, not the %d it had when backed up", w.Size, stopped.Size)
		} else if replayed.Size == stopped.Size {
			t.Log("gametest: the second run didn't change the world's size, so the restore check is weak")
		}
	}
}

// defaultScript has a player join, die (when the game has deaths) and
// leave, slowly enough to check the probe while they're on.
func defaultScript(info game.Info) string {
	if info.Deaths {
		return "join:Alex@1,death:Alex@2,leave:Alex@6"
	}
	return "join:Alex@1,leave:Alex@6"
}

// scriptStep is one step of a stand-in's script, as gametest reads it to
// know what to expect (cmd/fakeserver reads the same grammar).
type scriptStep struct {
	verb, name string
	at         time.Duration
}

func parseScript(script string) ([]scriptStep, error) {
	var out []scriptStep
	for part := range strings.SplitSeq(script, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		vn, at, hasAt := strings.Cut(part, "@")
		verb, name, _ := strings.Cut(vn, ":")
		st := scriptStep{verb: strings.ToLower(strings.TrimSpace(verb)), name: strings.TrimSpace(name)}
		if hasAt {
			sec, err := strconv.ParseFloat(strings.TrimSpace(at), 64)
			if err != nil || sec < 0 {
				return nil, fmt.Errorf("step %q: %q isn't a number of seconds", part, at)
			}
			st.at = time.Duration(sec * float64(time.Second))
		}
		out = append(out, st)
	}
	slices.SortStableFunc(out, func(a, b scriptStep) int { return int(a.at - b.at) })
	return out, nil
}

// checkPlayers follows the script through the Monitor's events.
func checkPlayers(t *testing.T, s *Session, steps []scriptStep) {
	t.Helper()
	info := s.info
	const within = 15 * time.Second
	switch info.Presence {
	case game.Unknown:
		t.Log("gametest: the game has no presence signal; scripted players aren't checked")
		return
	case game.ByCount:
		on := 0
		for _, st := range steps {
			switch st.verb {
			case "join":
				on++
			case "leave":
				on--
			default:
				continue
			}
			want := on
			waitFor(t, within, fmt.Sprintf("State().Count = %d after %s:%s", want, st.verb, st.name), func() bool { return s.State().Count == want })
		}
		return
	}
	probed := false
	for i, st := range steps {
		switch st.verb {
		case "join":
			s.WaitEvent("join", st.name, within)
			// Unless the script has them leave at once, they're listed now.
			if !leavesSoon(steps[i+1:], st) && !listed(s.State(), st.name) {
				t.Errorf("after %s's join event, State().Players doesn't list them", st.name)
			}
			if p, ok := s.Server().(game.Prober); ok && !probed && !leavesSoon(steps[i+1:], st) {
				probed = true
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				res, err := p.Probe(ctx, s.Record())
				cancel()
				switch {
				case err != nil:
					t.Errorf("the probe while %s is on: %v", st.name, err)
				case res.Count >= 0 && res.Count < 1:
					t.Errorf("the probe says %d players are on while %s is", res.Count, st.name)
				case res.Names != nil && !slices.Contains(res.Names, st.name):
					t.Errorf("the probe's names %q don't include %s", res.Names, st.name)
				}
			}
		case "death":
			if info.Deaths {
				s.WaitEvent("death", st.name, within)
			}
		case "leave":
			s.WaitEvent("leave", st.name, within)
		}
	}
}

// leavesSoon says whether the player leaves within 2 s of joining, too
// soon to look at who's on in between.
func leavesSoon(rest []scriptStep, join scriptStep) bool {
	for _, st := range rest {
		if st.verb == "leave" && st.name == join.name {
			return st.at-join.at < 2*time.Second
		}
	}
	return false
}

func listed(st game.RunState, name string) bool {
	return slices.ContainsFunc(st.Players, func(p game.Player) bool { return p.Name == name })
}

// checkPorts: each public port is in use while the server runs, by the
// server itself. Binding the port says it's taken; platform.PortInUse,
// once it can see ports on this system, must agree and name the server.
//
// A real server may open a port (a query port, say) a moment after its
// log says it's online, so with one each port gets portGrace to show up in
// the system's port table first. That wait only looks at the table: trying
// to open the port again and again could take it from the server just as
// it opens it. The ports are opened once, after it.
func checkPorts(t *testing.T, s *Session) {
	t.Helper()
	saved := s.store.Get()
	rec := s.Record()
	real := s.o.Real
	sees := platformSeesPorts()
	if !sees {
		t.Log("gametest: platform.PortInUse can't see ports here yet; the ports are checked by trying to open them")
	}
	deadline := time.Now().Add(portGrace(real))
	for _, r := range s.info.Ports {
		if !r.Public {
			continue
		}
		port := saved.Ports[r.Role]
		if port == 0 {
			t.Errorf("the %s role has no port in the settings", r.Role)
			continue
		}
		for _, proto := range r.Protos {
			if sees {
				for {
					if inUse, _ := platform.PortInUse(proto, port); inUse || !time.Now().Before(deadline) {
						break
					}
					time.Sleep(500 * time.Millisecond)
				}
			}
			taken := bindTaken(proto, port, real)
			var inUse bool
			var pid int
			if sees {
				inUse, pid = platform.PortInUse(proto, port)
			}
			switch {
			case !taken && real && inUse:
				// The system says it's held, though this process could still
				// open it: a way of sharing a port bindTaken doesn't know.
				// The table is the better witness; who holds it is checked
				// below.
				t.Logf("gametest: the %s port %d (%s) could still be opened here, but the system shows it held by process %d (the server is %d)", r.Role, port, proto, pid, rec.PID)
			case !taken:
				t.Errorf("the %s port %d (%s) is free while the server runs: the plan must give the server its ports", r.Role, port, proto)
			}
			if !sees {
				continue
			}
			switch {
			case !inUse:
				t.Errorf("platform.PortInUse(%q, %d) says the %s port is free while the server runs", proto, port, r.Role)
			case pid != 0 && pid != rec.PID:
				t.Errorf("the %s port %d (%s) is held by process %d, not the server (%d)", r.Role, port, proto, pid, rec.PID)
			}
		}
	}
}

// checkJoin: friends get steps, running or not.
func checkJoin(t *testing.T, s *Session) {
	t.Helper()
	rec, st, saved := s.Record(), s.State(), s.store.Get()
	router := map[string]string{}
	for _, r := range s.info.Ports {
		if r.Public {
			router[r.Role] = "open"
		}
	}
	srv := s.Server()
	running := srv.Join(game.JoinInput{Rec: &rec, State: &st, Saved: saved, PublicIP: "203.0.113.7", LAN: []string{"192.168.1.20"}, Router: router})
	stopped := srv.Join(game.JoinInput{Saved: saved, PublicIP: "203.0.113.7", LAN: []string{"192.168.1.20"}, Router: router})
	for _, j := range []struct {
		when string
		j    game.Join
	}{{"running", running}, {"stopped", stopped}} {
		if len(j.j.Steps) == 0 {
			t.Errorf("Join (%s) has no steps for friends", j.when)
		}
		if !slices.Contains([]string{"code", "address", "id"}, j.j.How) {
			t.Errorf("Join (%s).How = %q, not code, address or id", j.when, j.j.How)
		}
	}
}

// nameField finds the server's name among the settings: a server-scope
// text field with "name" in its key or label, preferring one asked in Add a
// game.
func nameField(schema game.Schema) (game.Field, bool) {
	for _, add := range []bool{true, false} {
		for _, f := range schema {
			if f.Scope == game.ServerScope && f.Kind == "text" && f.Add == add &&
				strings.Contains(strings.ToLower(f.Key+" "+f.Label), "name") {
				return f, true
			}
		}
	}
	return game.Field{}, false
}

func checkValidate(t *testing.T, srv game.Server, saved game.Saved, world string) {
	t.Helper()
	if errs := srv.Validate(saved, world); len(errs) > 0 {
		t.Errorf("Validate rejects the default settings: %v", errs)
	}
	f, ok := nameField(srv.Schema())
	if !ok {
		t.Errorf("found no server name in the settings (a server-scope text field with \"name\" in its key or label)")
		return
	}
	bad := saved.Clone()
	bad.Server[f.Key] = strings.Repeat("a", 200)
	if errs := srv.Validate(bad, world); errs[f.Key] == "" && errs["server."+f.Key] == "" {
		t.Errorf("Validate takes a 200-character %s (errors: %v)", f.Key, errs)
	}
}

// checkFingerprint: changing a setting marked Restart changes the
// fingerprint (the panel says "restart to apply"); changing one that
// applies live doesn't.
func checkFingerprint(t *testing.T, srv game.Server, saved game.Saved, world string) {
	t.Helper()
	base := srv.Fingerprint(saved, world)
	restart := 0
	for _, f := range srv.Schema() {
		if f.Scope == game.NewWorldScope {
			continue
		}
		c := saved.Clone()
		vals := c.Server
		if f.Scope == game.WorldScope {
			if c.Worlds == nil {
				c.Worlds = map[string]game.Values{}
			}
			if c.Worlds[world] == nil {
				c.Worlds[world] = game.Values{}
			}
			vals = c.Worlds[world]
		}
		vals[f.Key] = changedValue(f, vals[f.Key])
		changed := srv.Fingerprint(c, world) != base
		switch {
		case f.Restart && !changed:
			t.Errorf("changing %s (marked Restart) doesn't change Fingerprint, so the panel wouldn't ask for the restart it needs", f.Key)
		case !f.Restart && changed:
			t.Errorf("changing %s (not marked Restart) changes Fingerprint, so the panel would ask for a restart it doesn't need", f.Key)
		}
		if f.Restart {
			restart++
		}
	}
	if restart == 0 {
		t.Log("gametest: no setting is marked Restart")
	}
}

// changedValue is a different value of the field's kind.
func changedValue(f game.Field, cur string) string {
	if cur == "" {
		cur = f.Default
	}
	switch f.Kind {
	case "bool":
		if (game.Values{"v": cur}).Bool("v") {
			return "false"
		}
		return "true"
	case "number":
		n, err := strconv.Atoi(cur)
		if err != nil {
			n = f.Min
		}
		for _, c := range []int{n + 1, n - 1, f.Min, f.Max} {
			if c != n && c >= f.Min && (f.Max == 0 || c <= f.Max) {
				return strconv.Itoa(c)
			}
		}
		return strconv.Itoa(n + 1)
	case "choice":
		for _, c := range f.Choices {
			if c.Value != cur {
				return c.Value
			}
		}
	}
	for _, v := range []string{"abcd1234", "efgh5678"} {
		if f.Max > 0 && len(v) > f.Max {
			v = v[:f.Max]
		}
		if v != cur {
			return v
		}
	}
	return cur + "x"
}

func worldNames(t *testing.T, wl game.Worlds) []string {
	t.Helper()
	list, _, err := wl.List()
	if err != nil {
		t.Errorf("Worlds().List(): %v", err)
	}
	var names []string
	for _, w := range list {
		names = append(names, w.Name)
	}
	slices.Sort(names)
	return names
}

// probeUp asks the probe whether the server is up, again every 2 seconds
// until it says so or probeTries' time is over (one try with the
// stand-in). It gives the last answer and how many tries it took.
func probeUp(p game.Prober, rec game.Record, real bool) (game.ProbeResult, int, error) {
	total, each := probeTries(real)
	deadline := time.Now().Add(total)
	for tries := 1; ; tries++ {
		ctx, cancel := context.WithTimeout(context.Background(), each)
		res, err := p.Probe(ctx, rec)
		cancel()
		if (err == nil && res.Up) || !time.Now().Before(deadline) {
			return res, tries, err
		}
		time.Sleep(2 * time.Second)
	}
}

// waitFor polls cond until it holds, failing the test after within.
func waitFor(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Errorf("gametest: no %s within %v", what, within)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

var validID = regexp.MustCompile(`^[a-z][a-z0-9]{0,31}$`)

// checkInfo: what the registry and the manager rely on in Info.
func checkInfo(t *testing.T, in game.Info) {
	t.Helper()
	if !validID.MatchString(in.ID) {
		t.Errorf("Info.ID %q must be 1-32 lower-case letters and digits, starting with a letter", in.ID)
	}
	if in.Name == "" || in.Short == "" {
		t.Errorf("Info needs a Name and a Short name")
	}
	if !slices.Contains([]game.Stage{game.Hidden, game.Preview, game.Ready}, in.Stage) {
		t.Errorf("Info.Stage %q is not hidden, preview or ready", in.Stage)
	}
	if !slices.Contains([]game.Presence{game.ByName, game.ByCount, game.Unknown}, in.Presence) {
		t.Errorf("Info.Presence %q is not names, count or none", in.Presence)
	}
	if !slices.Contains([]string{"required", "optional", "none"}, in.Password.Use) {
		t.Errorf("Info.Password.Use %q is not required, optional or none", in.Password.Use)
	}
	roles := map[string]bool{}
	for _, r := range in.Ports {
		if r.Role == "" || roles[r.Role] {
			t.Errorf("port role %q is empty or listed twice", r.Role)
		}
		roles[r.Role] = true
		if len(r.Protos) == 0 {
			t.Errorf("port role %q has no protocols", r.Role)
		}
		for _, p := range r.Protos {
			if p != "tcp" && p != "udp" {
				t.Errorf("port role %q has protocol %q, not tcp or udp", r.Role, p)
			}
		}
	}
	for _, r := range in.Ports {
		if r.Tie != "" {
			if _, err := tiedPort(r.Tie, map[string]int{tieBase(r.Tie, in.Ports): 1}); err != nil {
				t.Errorf("port role %q: %v", r.Role, err)
			}
		}
	}
}

// tieBase is the role a tie names, when it is a role without a tie of its
// own ("" otherwise, so tiedPort reports it).
func tieBase(tie string, ports []game.PortRole) string {
	base, _, _ := strings.Cut(tie, "+")
	for _, r := range ports {
		if r.Role == base && r.Tie == "" {
			return base
		}
	}
	return ""
}

// checkSchema: keys are unique in their scope, and kinds and choices make
// sense, so the panel's generic form can draw it.
func checkSchema(t *testing.T, schema game.Schema) {
	t.Helper()
	seen := map[string]bool{}
	for _, f := range schema {
		k := string(f.Scope) + "." + f.Key
		if f.Key == "" || seen[k] {
			t.Errorf("setting %q is empty or listed twice in its scope", k)
		}
		seen[k] = true
		if !slices.Contains([]game.Scope{game.ServerScope, game.WorldScope, game.NewWorldScope}, f.Scope) {
			t.Errorf("setting %q has scope %q", f.Key, f.Scope)
		}
		if !slices.Contains([]string{"text", "password", "number", "bool", "choice", "list"}, f.Kind) {
			t.Errorf("setting %q has kind %q", f.Key, f.Kind)
		}
		if f.Kind == "choice" {
			if len(f.Choices) == 0 {
				t.Errorf("setting %q is a choice with no choices", f.Key)
			} else if f.Default != "" && !slices.ContainsFunc(f.Choices, func(c game.Choice) bool { return c.Value == f.Default }) {
				t.Errorf("setting %q's default %q isn't one of its choices", f.Key, f.Default)
			}
		}
	}
}
