package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/logparse"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/worlds"
)

// errNotInstalled is the answer to starting a server that isn't installed.
func errNotInstalled(inst *instance) error {
	return &ActionError{
		Msg:    inst.exact("not_installed", fmt.Sprintf("The %s server isn't installed yet. Click Install first.", inst.short())),
		Action: "install",
	}
}

// StartServer starts the legacy game's server (updating it first if that
// setting is on).
func (m *Manager) StartServer() error { return m.startGame(m.legacy()) }

// startGame starts an instance's server as a job: R2's start, which
// StartServer (the old routes, the tray, the legacy game's start-with-app,
// which never checked memory, and until streams A and WK move them, the
// resume and the wake packet) still uses. startGameWith
// (games.go), the R3 path every other start takes (crash restarts too), is
// the same checks and job with 4.3's memory step between them.
func (m *Manager) startGame(inst *instance) error {
	if err := m.startChecks(inst); err != nil {
		return err
	}
	return m.runStartJob(inst, nil)
}

// startChecks refuses a start that can't work (spec 4.3's steps 1 and 2):
// startRefusal's reasons (foreign, hidden, turned off, can't run on this
// computer), then a server that is already running, isn't installed, or
// whose settings need fixing. Every start asks it before its job (the
// panel, the old routes, Discord, a wake, the resume, start-with-app, a
// crash restart). A restart asks startRefusal instead, and doStart asks it
// again inside every job, so a hidden or turned-off game never starts
// unseen, whichever way it is asked.
func (m *Manager) startChecks(inst *instance) error {
	if err := m.startRefusal(inst); err != nil {
		return err
	}
	m.mu.Lock()
	running := inst.run != nil
	m.mu.Unlock()
	var installed bool
	if inst.legacy {
		m.mu.Lock()
		installed = m.installedLocked(inst)
		m.mu.Unlock()
	} else {
		installed = m.checkInstalled(inst)
	}
	saved := inst.saved()
	errs := inst.srv.Validate(saved, selectedWorld(inst, saved))
	switch {
	case running:
		return errors.New("The server is already running.")
	case !installed:
		return errNotInstalled(inst)
	case len(errs) > 0: // an adapter may answer an empty map for "no errors"
		return fieldErrors(errs) // the panel shows these next to the settings
	}
	return nil
}

// startRefusal is why the instance's server may not be started at all,
// whether it runs now or not: this version can't run the game (foreign),
// the legacy game was removed from the panel (hidden, I1), the owner
// turned the game off (disabled, 8.3), or its server can't run on this
// computer (2.10). nil when none of these holds.
func (m *Manager) startRefusal(inst *instance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.startRefusalLocked(inst)
}

func (m *Manager) startRefusalLocked(inst *instance) error {
	if inst.srv == nil {
		return foreignError(inst)
	}
	if m.isHiddenLocked(inst) {
		return hiddenError(inst)
	}
	if m.isDisabledLocked(inst) {
		return fmt.Errorf("%s is turned off on its Settings tab. Turn it on there to start it.", inst.short())
	}
	if level, needs := m.canRunLocked(inst); level != game.Runs {
		if needs == "" {
			needs = "It can't run on this computer."
		}
		return fmt.Errorf("The %s server can't run on this computer. %s", inst.short(), strings.TrimSuffix(needs, ".")+".")
	}
	return nil
}

// runStartJob runs the start as the instance's job "start" (4.3's step 4).
// first, when not nil, runs inside the job before the server is started,
// and an error from it ends the job: that is where the memory dialog's
// "Stop Valheim and start" stops the other game (stream A's
// memoryInStartJob), so the two never look like separate actions.
func (m *Manager) runStartJob(inst *instance, first func(context.Context, *jobCtl) error) error {
	return m.runInstJob(inst, "start", "Starting "+inst.serverObject(), true, func(ctx context.Context, j *jobCtl) error {
		if first != nil {
			if err := first(ctx, j); err != nil {
				return err
			}
		}
		return m.doStart(ctx, j, inst, "")
	})
}

// StopServer saves the world and stops the legacy game's server.
func (m *Manager) StopServer() error { return m.stopGame(m.legacy()) }

func (m *Manager) stopGame(inst *instance) error {
	m.mu.Lock()
	running := inst.run != nil
	m.mu.Unlock()
	if !running {
		return errors.New("The server isn't running.")
	}
	m.endStartWait(inst)
	return m.runInstJob(inst, "stop", "Stopping "+inst.serverObject(), false, func(ctx context.Context, j *jobCtl) error {
		return m.doStop(ctx, j, inst)
	})
}

// endStartWait finishes a job (a start, a restart, or an import that
// replaced the running world) that has launched the server and is only
// waiting for the world to load, so Stop works straight away.
func (m *Manager) endStartWait(inst *instance) {
	m.mu.Lock()
	job, cancel := inst.job, inst.jobCancel
	waiting := job != nil && job.Finished == nil && inst.loadWait == job && inst.run != nil
	m.mu.Unlock()
	if !waiting {
		return
	}
	cancel()
	// What the job does after the wait (such as tidying up an upload) is
	// quick, but give it a few seconds.
	for i := 0; i < 250; i++ {
		m.mu.Lock()
		done := job.Finished != nil
		m.mu.Unlock()
		if done {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// RestartServer stops and starts the legacy game's server again.
func (m *Manager) RestartServer() error { return m.restartGame(m.legacy()) }

// restartGame stops the instance's server and starts it again as one job.
// A server that isn't running is only started, as the old route always
// did, so a restart is refused for startRefusal's reasons just as a start
// is. It is asked before anything is stopped: a restart that couldn't
// start the server again would only be a stop, which Stop does (and still
// does for a hidden or turned-off game that runs).
func (m *Manager) restartGame(inst *instance) error {
	if err := m.startRefusal(inst); err != nil {
		return err
	}
	return m.runInstJob(inst, "restart", "Restarting "+inst.serverObject(), false, func(ctx context.Context, j *jobCtl) error {
		if err := m.doStop(ctx, j, inst); err != nil {
			return err
		}
		return m.doStart(ctx, j, inst, "")
	})
}

// ForceStop ends the legacy game's server process immediately, without
// saving.
func (m *Manager) ForceStop() error { return m.forceStopGame(m.legacy()) }

func (m *Manager) forceStopGame(inst *instance) error {
	m.mu.Lock()
	run := inst.run
	var stopping, forced bool
	if run != nil {
		// Marked before killing it, so its exit counts as expected.
		stopping, forced = run.stopping, run.forced
		run.stopping, run.forced = true, true
	}
	m.mu.Unlock()
	if run == nil {
		return errors.New("The server isn't running.")
	}
	if err := run.proc.Kill(); err != nil {
		// It's still running: don't show it as stopping for good, or count
		// a later crash as a stop. A stop in progress stays one.
		m.mu.Lock()
		run.stopping, run.forced = stopping, forced
		m.mu.Unlock()
		m.notify()
		msg := fmt.Sprintf("Couldn't force the server to stop (%v).", err)
		if runtime.GOOS == "windows" {
			msg += " It may have been started by a copy of this app that was run as administrator: close it in Task Manager, or run this app as administrator."
		}
		return errors.New(msg)
	}
	m.instActivity(inst, "warning", "", inst.serverWord()+" was forced to stop. Anything since the last save is lost.")
	return nil
}

// InstallServer installs or updates the legacy game's server. repair
// re-checks all files.
func (m *Manager) InstallServer(repair bool) error { return m.installGame(m.legacy(), repair) }

func (m *Manager) installGame(inst *instance, repair bool) error {
	if inst.srv == nil {
		return foreignError(inst)
	}
	m.mu.Lock()
	running := inst.run != nil
	hidden := m.isHiddenLocked(inst)
	m.mu.Unlock()
	if hidden {
		return hiddenError(inst)
	}
	installed := m.checkInstalled(inst)
	short := inst.short()
	if running {
		return fmt.Errorf("Stop the server before updating it (%s can't update files that are in use).", short)
	}
	title := "Installing the " + short + " server"
	switch {
	case repair:
		title = "Repairing the " + short + " server"
	case installed:
		title = "Updating the " + short + " server"
	}
	return m.runInstJob(inst, "install", title, true, func(ctx context.Context, j *jobCtl) error {
		// What Status shows of a non-legacy game comes from its cache: it is
		// refreshed before the job finishes, so a start straight after it is
		// never refused as "not installed" (spec 4.3).
		defer m.refreshCache(inst)
		if !inst.legacy {
			os.MkdirAll(m.dirsOf(inst).Root, 0o755)
		}
		err := inst.srv.Installer().Install(ctx, repair, func(p game.Progress) { j.step(p.Step, p.Percent) })
		if ctx.Err() != nil {
			return errCancelled
		}
		if err != nil {
			return err
		}
		if _, err := os.Stat(inst.srv.Program()); err != nil {
			return fmt.Errorf(inst.exact("install_missing", "The download finished, but %s wasn't found. Try Repair."), filepath.Base(inst.srv.Program()))
		}
		done := "The " + short + " server is installed and up to date."
		m.instActivity(inst, "update", "", done)
		j.result(done)
		return nil
	})
}

// doStart starts the instance's server with world, or the selected world
// when world is empty.
func (m *Manager) doStart(ctx context.Context, j *jobCtl, inst *instance, world string) error {
	m.mu.Lock()
	// Asked again here, inside the job, for every start, restart and
	// import that starts the server again: the game may have been removed
	// from the panel or turned off since the job was asked for.
	if err := m.startRefusalLocked(inst); err != nil {
		m.mu.Unlock()
		return err
	}
	if inst.run != nil {
		m.mu.Unlock()
		return errors.New("The server is already running.")
	}
	logs := inst.dirs.Logs // a settings save may replace dirs once the lock is let go
	m.mu.Unlock()
	var installed bool
	if inst.legacy {
		m.mu.Lock()
		installed = m.installedLocked(inst)
		m.mu.Unlock()
	} else {
		installed = m.checkInstalled(inst)
	}
	saved := inst.saved()
	if world == "" {
		world = saved.World
	}

	if errs := inst.srv.Validate(saved, world); len(errs) > 0 {
		return &ActionError{Msg: "Please fix the settings first: " + errs.Error(), Action: "settings"}
	}
	if !installed {
		return errNotInstalled(inst)
	}
	// A copy may already be running (for example started before this app).
	if procs, _ := platform.FindByExe(inst.srv.Program()); len(procs) > 0 {
		if m.adopt(inst, procs[0], nil) {
			j.result("The server was already running.")
			return nil
		}
	}
	req := game.PlanRequest{
		World: world, Saved: saved, Update: saved.Server.Bool("updateOnStart"),
		LogPath:     filepath.Join(logs, logPrefix(inst)+time.Now().Format("2006-01-02_15-04-05")+".log"),
		Step:        j.step,
		Cancellable: j.setCancellable,
	}
	if !inst.legacy {
		// A world the server hasn't made yet is made from its new-world
		// values (kept with its world values until it exists, so a first
		// start that fails keeps them for the next try).
		req.New = !inst.srv.Worlds().Exists(world)
		if req.New {
			req.NewWorld = newWorldValues(inst.srv.Schema(), saved.Worlds[world])
		}
		os.MkdirAll(logs, 0o755)
	}
	plan, err := inst.srv.Plan(ctx, req)
	if ctx.Err() != nil {
		return errCancelled
	}
	if err != nil {
		return actionOf(err)
	}
	// The ports check (spec 2.5, stream A's ports.go) for every game but the
	// legacy one, which keeps today's check inside its Plan.
	if !inst.legacy {
		if err := m.checkPorts(inst); err != nil {
			return err
		}
	}
	j.setCancellable(false)
	j.step(fmt.Sprintf("Starting the %s server…", inst.short()), -1)
	if plan.LogPath == "" {
		plan.LogPath = req.LogPath
	}
	var proc *platform.Proc
	var l launched
	if inst.legacy {
		// Exactly as before: Valheim writes its own log (LogArg) and has no
		// console.
		proc, err = platform.StartDetached(plan.Exe, plan.Args, plan.Dir, append(os.Environ(), plan.Env...))
	} else {
		proc, l, err = m.launch(plan)
	}
	if err != nil {
		return fmt.Errorf("Couldn't start the %s server: %v", inst.short(), err)
	}
	rec := plan.Record
	rec.PID, rec.Started, rec.Launched, rec.LogPath = proc.PID(), proc.StartTime(), time.Now(), plan.LogPath
	rec.World, rec.ArgsKey, rec.Exe = world, plan.Fingerprint, plan.Exe
	rec.Played = m.startedWith(inst, world, rec.Password)
	if !inst.legacy {
		rec.Game, rec.Instance, rec.Name = inst.info.ID, inst.id, inst.short()
		if rec.Ports == nil {
			rec.Ports = maps.Clone(saved.Ports) // the ports it started with, for the stop chain after a takeover
		}
	}
	// From now on the job only waits for the world to load, which Stop
	// can end (see endStartWait).
	m.mu.Lock()
	inst.loadWait = j.job
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		if inst.loadWait == j.job {
			inst.loadWait = nil
		}
		m.mu.Unlock()
	}()
	run := m.attachWith(inst, proc, rec, false, &l)
	m.instActivity(inst, "starting", "", fmt.Sprintf("Starting %s with the world %q.", inst.serverObject(), world))
	m.pruneLogs(inst)

	loading := "Loading the world… The first start after an update can take a few minutes."
	if w, err := inst.srv.Worlds().Get(world); errors.Is(err, worlds.ErrNotFound) || err == nil && w.NeverSaved {
		loading = "Making the new world… A world’s first start takes a few minutes."
	}
	j.step(loading, -1)
	select {
	case <-run.readyCh:
		j.result("The server is online.")
		return nil
	case <-run.done:
		msg := "The server stopped while starting. " + m.exitHintFor(inst)
		if ml, ok := inst.srv.(game.ModLister); ok {
			for _, md := range ml.PlayerMods(world, nil) {
				if md.NewWorlds {
					msg += fmt.Sprintf(" %s only loads worlds that were created with it: if %q wasn't, turn %s off for it on the Worlds page.", md.Name, world, md.Name)
				}
			}
		}
		return &ActionError{Msg: msg, Action: "log"}
	case <-ctx.Done():
		j.result("Stopped waiting for the server to finish loading.")
		return nil
	case <-time.After(plan.ReadyWait + 5*time.Minute):
		j.result("The server is running but hasn't said it's ready yet.")
		return nil
	}
}

// exitHintFor explains the most likely reason the instance's server
// stopped, from the last lines of its log.
func (m *Manager) exitHintFor(inst *instance) string {
	lines, _ := inst.logs.after(0, 80)
	tail := make([]string, len(lines))
	for i, l := range lines {
		tail[i] = l.Text
	}
	return inst.srv.ExitHint(tail)
}

// defaultStopSay is a stop step's job text when the game gives none.
const defaultStopSay = "Saving the world and shutting down… Please don't turn off the computer."

// doStop runs the game's stop chain (spec 2.7): each step asks the server
// to save and stop, and waits for it to exit. Killing it is never
// automatic: only the owner's Force stop does that.
func (m *Manager) doStop(ctx context.Context, j *jobCtl, inst *instance) error {
	m.mu.Lock()
	run := inst.run
	if run == nil {
		m.mu.Unlock()
		return nil
	}
	run.stopping = true
	lastSave := run.state().LastSave
	steps := inst.srv.Stop(run.rec)
	m.mu.Unlock()
	m.notify()

	notStopping := func() {
		m.mu.Lock()
		run.stopping = false
		m.mu.Unlock()
	}
	if len(steps) == 0 {
		steps = []game.StopStep{{Kind: "interrupt", Wait: 4 * time.Minute}}
	}
	for i, step := range steps {
		last := i == len(steps)-1
		say := step.Say
		if say == "" {
			say = defaultStopSay
		}
		j.step(say, -1)
		if err := m.sendStop(ctx, run, step); err != nil {
			if !last {
				// This way can't be used now (no console after a takeover, a
				// refused connection, an API that doesn't answer): the next
				// one at once (spec 2.7).
				if !errors.Is(err, game.ErrNoConsole) {
					m.log.Printf("stopping %s: the %q step: %v; trying the next one", inst.id, step.Kind, err)
				}
				continue
			}
			notStopping()
			return &ActionError{
				Msg:    fmt.Sprintf("Couldn't ask the server to save and stop (%v). You can force it to stop, but progress since the last save%s will be lost.", err, sinceText(lastSave)),
				Action: "force_stop",
			}
		}
		stopped, err := m.waitStop(ctx, j, run, step)
		if stopped || err != nil {
			return err
		}
		if last {
			notStopping()
			return &ActionError{
				Msg:    "The server is taking unusually long to stop. You can wait and try again, or force it to stop (progress since the last save will be lost).",
				Action: "force_stop",
			}
		}
	}
	return nil
}

// stopInstances runs the stop chains of several games at the same time
// (spec 2.7), inside one job j (going to sleep): each in its own goroutine,
// so their saving overlaps (on Windows the Ctrl+C sends themselves still go
// one at a time through the console lock, about a second each). j shows
// one step for all of them. It waits for every chain to end and returns
// each game's error, by instance id (none when all stopped). It runs
// without m.mu.
func (m *Manager) stopInstances(ctx context.Context, j *jobCtl, insts []*instance) map[string]error {
	if len(insts) == 0 {
		return nil
	}
	names := make([]string, len(insts))
	for i, inst := range insts {
		names[i] = inst.short()
	}
	j.step("Saving the worlds and stopping "+andList(names)+"… Please don't turn off the computer.", -1)
	type result struct {
		id  string
		err error
	}
	results := make(chan result, len(insts))
	for _, inst := range insts {
		go func() {
			// The result is sent even if the chain panics, so the wait
			// below always ends.
			err := errStopPanicked
			defer func() { results <- result{inst.id, err} }()
			defer m.recoverPanic()
			// Each chain reports its steps to a job of its own that no slot
			// shows, so the chains don't overwrite each other's words.
			own := &jobCtl{m: m, job: &Job{Game: inst.id, Kind: "stop", Percent: -1}}
			err = m.doStop(ctx, own, inst)
		}()
	}
	errs := map[string]error{}
	for range insts {
		r := <-results
		if r.err != nil {
			errs[r.id] = r.err
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// errStopPanicked is a stop chain's answer when it failed in a way the app
// didn't expect (recoverPanic logs the details).
var errStopPanicked = errors.New("Stopping it failed unexpectedly.")

// andList joins names for people: "Valheim", "Valheim and Minecraft",
// "Valheim, Minecraft and Terraria".
func andList(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// sendStop sends one stop step to the server: a line on its console (the
// app's stdin pipe; game.ErrNoConsole after a takeover), an RCON command
// (to the port and password the run started with), the adapter's call, or
// Ctrl+C. A step of a kind this build doesn't know can't be used: the next
// one runs at once.
func (m *Manager) sendStop(ctx context.Context, run *serverRun, step game.StopStep) error {
	switch step.Kind {
	case "interrupt":
		return run.proc.Interrupt()
	case "console":
		return m.typeLine(run, step.Line)
	case "rcon":
		return m.sendRCON(ctx, run, step.Line)
	case "call":
		if step.Call == nil {
			return game.ErrNoConsole
		}
		cctx, cancel := context.WithTimeout(ctx, stepCallWait)
		defer cancel()
		return step.Call(cctx)
	}
	return game.ErrNoConsole
}

// waitStop waits up to step.Wait for the server to exit, sending the step
// once more after step.Resend. It reports whether the server stopped (or
// the app is closing, when the server finishes stopping by itself).
func (m *Manager) waitStop(ctx context.Context, j *jobCtl, run *serverRun, step game.StopStep) (bool, error) {
	start := time.Now()
	resent := false
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-run.done:
			m.mu.Lock()
			forced := run.forced
			m.mu.Unlock()
			if forced {
				j.result("The server was forced to stop.")
			} else {
				j.result("The world was saved and the server stopped.")
			}
			return true, nil
		case <-ctx.Done():
			return true, nil // the app is closing; the server finishes stopping by itself
		case <-tick.C:
			waited := time.Since(start)
			m.mu.Lock()
			saved := run.state().LastSave.After(start)
			m.mu.Unlock()
			if saved {
				j.step("World saved. Shutting down…", -1)
			} else {
				j.step(fmt.Sprintf("Saving the world… (%d s) Please don't turn off the computer.", int(waited.Seconds())), -1)
			}
			if step.Resend > 0 && waited > step.Resend && !resent {
				resent = true
				m.sendStop(ctx, run, step)
			}
			if waited > step.Wait {
				return false, nil
			}
		}
	}
}

func sinceText(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return " (" + logparse.HumanDuration(time.Since(t)) + " ago)"
}

// attach starts following a server process: its log and its exit.
func (m *Manager) attach(inst *instance, proc *platform.Proc, rec game.Record, adopted bool) *serverRun {
	return m.attachWith(inst, proc, rec, adopted, nil)
}

// attachWith is attach with what the launch set up (l, nil after a
// takeover): the console pipe, and where a game that writes its own log
// left off.
func (m *Manager) attachWith(inst *instance, proc *platform.Proc, rec game.Record, adopted bool, l *launched) *serverRun {
	ctx, cancel := context.WithCancel(m.ctx)
	run := &serverRun{inst: inst, proc: proc, rec: rec, done: make(chan struct{}), readyCh: make(chan struct{}),
		cancelTail: cancel, replaying: adopted, discordAt: rec.Posted, discordErr: rec.PostError}
	m.followSetup(run, l)
	if adopted && rec.Announced != "" && rec.Posted.IsZero() && rec.PostError == "" {
		// The app that posted it didn't note how it went (older versions
		// don't): it most likely went out soon after the start.
		run.rec.Posted, run.discordAt = rec.Launched, rec.Launched
	}
	// The game's work after launch (Valheim's mods code) runs for a new
	// start, and again after a takeover when the app that started the
	// server closed before it was done.
	after, isAfterer := inst.srv.(game.Afterer)
	run.afterPending = isAfterer && after.Pending(rec)
	// Friends who connect at the same time are told apart by the account
	// each last played from. The Monitor runs with m.mu held, so this
	// mustn't lock it again.
	run.mon = inst.srv.Watch(rec, func(name string) string { return m.knownAccountLocked(inst, name) })
	m.mu.Lock()
	inst.run = run
	m.machineIdleSince = time.Now()
	m.mu.Unlock()
	saveRuntime(m.runFile(inst), rec)
	m.updateAwake()
	m.go_(func() { m.tail(ctx, run) })
	if p, ok := inst.srv.(game.Prober); ok {
		m.go_(func() { m.probeLoop(ctx, run, p) })
	}
	if run.afterPending {
		m.go_(func() { m.runAfter(run, after) })
	}
	go func() {
		defer m.recoverPanic()
		code, err := proc.Wait()
		if err != nil {
			m.log.Printf("waiting for server: %v", err)
		}
		// The app closing waits for what's left to do after the server
		// exits, such as saving the records, but never for the server.
		m.mu.Lock()
		closing := m.closing
		if !closing {
			m.exits.Add(1)
		}
		m.mu.Unlock()
		if !closing {
			defer m.exits.Done()
		}
		time.Sleep(700 * time.Millisecond) // let the log reader catch the last lines
		cancel()
		m.onExit(run, code)
		close(run.done)
	}()
	m.notify()
	return run
}

// replayer is a Monitor that reads a log back differently while the app
// catches up with a server it took over (games/valheim's Monitor: a line
// then counts at its own time when that is earlier). The manager tells it
// before each line whether it is catching up. It is the contract's
// game.Replayer, so every game can offer it.
type replayer = game.Replayer

// fromLogReader is a Monitor that can read a finished run's log for the
// records, by the log's own clock (game.LogReader).
type fromLogReader = game.LogReader

// runAfter runs the game's work after launch, then lets the online post go
// out, with what it made (the mods code) if anything.
func (m *Manager) runAfter(run *serverRun, after game.Afterer) {
	var extra map[string]json.RawMessage
	defer func() { // whatever happens, the post doesn't wait any longer
		m.mu.Lock()
		run.afterPending = false
		for k, v := range extra {
			if run.rec.Extra == nil {
				run.rec.Extra = map[string]json.RawMessage{}
			}
			run.rec.Extra[k] = v
		}
		var post discordPost
		announce := false
		if !m.closing && m.ctx.Err() == nil { // closing: the app that takes over posts it
			post, announce = m.announceLocked(run)
		}
		m.mu.Unlock()
		if announce {
			m.queueDiscord(post)
		}
		m.saveRunInfo(run)
		m.notify()
	}()
	m.mu.Lock()
	rec := run.rec
	m.mu.Unlock()
	var err error
	if extra, err = after.After(m.ctx, rec); err != nil {
		m.log.Printf("after the start: %v", err)
	}
}

// adopt takes over a server that is already running. saved is its run
// record when it has one.
func (m *Manager) adopt(inst *instance, pi platform.ProcInfo, saved *game.Record) bool {
	proc, err := platform.OpenProc(pi.PID)
	if err != nil {
		return false
	}
	var rec game.Record
	if saved != nil && saved.PID == pi.PID {
		rec = *saved
	} else if rc, ok := inst.srv.(game.Recoverer); ok {
		rec = rc.Recover(pi.PID, pi.Started)
		if pi.Exe != "" {
			rec.Exe = pi.Exe
		}
	} else {
		rec = game.Record{PID: pi.PID, Started: pi.Started, Launched: pi.Started, World: inst.saved().World,
			LogPath: newestLog(inst, m.dirsOf(inst).Logs), Exe: inst.srv.Program(), Played: game.Played{Mods: map[string]string{}}}
	}
	if rec.Launched.IsZero() {
		rec.Launched = proc.StartTime()
	}
	if !inst.legacy && saved == nil {
		// A record made up for a server found without its run file: what an
		// older build and the stop chain read of it (its ports, for RCON).
		if rec.Game == "" {
			rec.Game, rec.Instance, rec.Name = inst.info.ID, inst.id, inst.short()
		}
		if rec.Ports == nil {
			rec.Ports = maps.Clone(inst.saved().Ports)
		}
		if rec.Played.Mods == nil {
			rec.Played.Mods = map[string]string{}
		}
	}
	m.attach(inst, proc, rec, true)
	m.instActivity(inst, "info", "", fmt.Sprintf("Found the %s server already running and took over watching it.", inst.short()))
	return true
}

// adoptRunning looks for the instance's server left running by an earlier
// session of the app (spec 4.4): its run record, if the process it names
// is still the same one, else a process running the server's program.
func (m *Manager) adoptRunning(inst *instance) {
	path := m.runFile(inst)
	if rec, err := loadRuntime(path); err == nil {
		if proc, err := platform.OpenProc(rec.PID); err == nil {
			same := absDuration(proc.StartTime().Sub(rec.Started)) < 2*time.Second
			proc.Close()
			if same {
				m.adopt(inst, platform.ProcInfo{PID: rec.PID, Exe: rec.Exe, Started: rec.Started}, &rec)
				return
			}
		}
		os.Remove(path)
	}
	if procs, _ := platform.FindByExe(inst.srv.Program()); len(procs) > 0 {
		m.adopt(inst, procs[0], nil)
	}
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// onExit records that the server process ended.
func (m *Manager) onExit(run *serverRun, code int) {
	inst := run.inst
	now := time.Now()
	if m.ctx.Err() != nil {
		return // the app is closing; the next start of the app takes over
	}
	m.mu.Lock()
	if inst.run != run {
		m.mu.Unlock()
		return
	}
	// A shutdown line only counts shortly before the exit: a player can
	// make one up, which mustn't hide a later crash.
	st := run.state()
	expected := run.stopping || (st.Stopping && now.Sub(st.StoppingAt) < 5*time.Minute)
	for _, p := range st.Players {
		m.notePlayerLocked(inst, game.Event{Kind: "leave", Time: now, Name: p.Name, Account: p.Account}, p.Since)
	}
	noteEventsLocked(run, run.mon.End(now))
	archived := m.archiveRunLocked(run)
	inst.run = nil
	// An exit caused by auto-stop or the "still playing?" rule isn't someone
	// doing something, so the computer may sleep right after it (spec 4.5).
	if !run.autoStopped {
		m.machineIdleSince = now
	}
	exit := &ExitInfo{At: now, Code: code, Expected: expected, World: run.rec.World,
		Uptime: now.Sub(run.rec.Launched).Seconds()}
	autoRestart := false
	if !expected {
		lines, _ := inst.logs.after(0, 30)
		for _, l := range lines {
			exit.LogTail = append(exit.LogTail, l.Text)
		}
		// Restart after a crash, but give up after 3 crashes in 15 minutes.
		recent := inst.crashes[:0]
		for _, t := range inst.crashes {
			if now.Sub(t) < 15*time.Minute {
				recent = append(recent, t)
			}
		}
		inst.crashes = append(recent, now)
		autoRestart = inst.saved().Server.Bool("autoRestart") && len(inst.crashes) <= 3 && run.ready
	}
	inst.lastExit = exit
	runtimePath := m.runFile(inst)
	why := stopStopped
	job, app := inst.job, m.appJob
	switch {
	case !expected && autoRestart:
		why = stopCrashRestart
	case !expected:
		why = stopCrash
	case run.autoStopped:
		why = run.autoReason // stopIdle or stopAsked
	case job != nil && job.Finished == nil && job.Kind == "restart":
		why = stopRestart
	case app != nil && app.Finished == nil && app.Kind == "hibernate":
		why = stopRest
	}
	// Queued before unlocking, so anyone who sees the server stopped (and
	// closes the app) knows the message is waiting to go out.
	if post, ok := m.stoppedPostLocked(run, why); ok {
		m.queueDiscord(post)
	}
	// The observers get the run that ended (inst.run is nil now): its ports,
	// router mappings and why auto-stop stopped it.
	m.observeLocked(func(o observer) { o.onExit(inst, run, exit) }) // hooks.go
	// The activity entry says why, as the stopped post does: auto-stop's
	// text comes from the run (stream A's autostop.go).
	stopped := inst.serverWord() + " stopped."
	if run.autoStopped {
		stopped = m.autoStopActivityLocked(run)
	}
	m.mu.Unlock()

	run.proc.Close()
	m.closeConsole(run)
	os.Remove(runtimePath)
	m.updateAwake() // still awake if it has just woken up
	if expected {
		m.instActivity(inst, "stopped", "", stopped)
	} else {
		m.instActivity(inst, "crashed", "", fmt.Sprintf("%s stopped unexpectedly (exit code %d). %s", inst.serverWord(), code, m.exitHintFor(inst)))
	}
	if autoRestart {
		m.instActivity(inst, "info", "", fmt.Sprintf("Restarting %s in %d seconds because \"Restart if it crashes\" is on.", inst.serverObject(), int(m.opt.RestartDelay.Seconds())))
		time.AfterFunc(m.opt.RestartDelay, func() {
			m.mu.Lock()
			still := inst.run == nil && m.ctx.Err() == nil
			m.mu.Unlock()
			if still {
				// The one start path (spec 4.3); a restart needs no memory check.
				if err := m.startGameWith(inst, StartOptions{Source: SourceRestart}); err != nil {
					m.log.Printf("auto restart: %v", err)
				}
			}
		})
	}
	m.saveData()
	if archived {
		m.saveRecordsFor(inst)
	}
	m.notify()
}

// caughtUp ends the catching up with a taken-over server's log.
func (m *Manager) caughtUp(run *serverRun) {
	inst := run.inst
	m.mu.Lock()
	run.replaying = false // caught up with the existing log
	// Tell the Monitor too, so what no line says (such as Valheim's "ready
	// after a while" fallback) works straight away, not at the next line.
	if r, ok := run.mon.(replayer); ok {
		r.Replaying(false)
	}
	// Remember players who joined while this app was closed.
	for _, p := range run.state().Players {
		if inst.known[p.Name] == nil {
			m.notePlayerLocked(inst, game.Event{Kind: "join", Time: p.Since, Name: p.Name, Account: p.Account}, time.Time{})
		}
	}
	if inst.run == run {
		m.observeLocked(func(o observer) { o.onTakeover(inst) }) // hooks.go
	}
	// Post the online message if the app that started the server didn't.
	post, announce := m.announceLocked(run)
	m.mu.Unlock()
	if announce {
		m.queueDiscord(post)
		m.saveRunInfo(run)
	}
	m.notify()
}

// handleLine processes one line of server output.
func (m *Manager) handleLine(run *serverRun, line string) {
	m.mu.Lock()
	if run.inst.run != run {
		m.mu.Unlock()
		return
	}
	sinceByName := m.sinceByNameLocked(run)
	// The Monitor times a taken-over server's lines by their own clock
	// while the app catches up with its log (run.replaying says when).
	if r, ok := run.mon.(replayer); ok {
		r.Replaying(run.replaying)
	}
	l := run.mon.Line(line, time.Now())
	m.mu.Unlock()
	if l.Hide {
		return
	}
	at := l.Time
	if at.IsZero() {
		at = time.Now()
	}
	run.inst.logs.add(func(seq int64) LogLine { return LogLine{Seq: seq, Time: at, Text: l.Text} })
	m.handleEvents(run, l.Events, sinceByName)
}

// tick lets the Monitor notice what no line says, such as a server that
// never said it's ready (Valheim counts it as ready after a while).
func (m *Manager) tick(run *serverRun) {
	m.mu.Lock()
	if run.inst.run != run || run.replaying {
		m.mu.Unlock()
		return
	}
	sinceByName := m.sinceByNameLocked(run)
	evs := run.mon.Tick(time.Now())
	m.mu.Unlock()
	if len(evs) > 0 {
		m.handleEvents(run, evs, sinceByName)
	}
}

// sinceByNameLocked is when each listed player joined, for their play time.
func (m *Manager) sinceByNameLocked(run *serverRun) map[string]time.Time {
	out := map[string]time.Time{}
	for _, p := range run.state().Players {
		out[p.Name] = p.Since
	}
	return out
}

// handleEvents updates the instance with what the Monitor made of a line
// (or a tick). sinceByName is when each player joined, from before it.
func (m *Manager) handleEvents(run *serverRun, evs []game.Event, sinceByName map[string]time.Time) {
	inst := run.inst
	m.mu.Lock()
	if inst.run != run {
		m.mu.Unlock()
		return
	}
	noteEventsLocked(run, evs)
	becameReady := false
	if !run.ready && (hasKind(evs, "online") || run.state().Ready) {
		run.ready, run.readyAt = true, time.Now()
		close(run.readyCh)
		becameReady = true
	}
	post, announce := m.announceLocked(run)
	replaying := run.replaying
	savePlayed, seen := false, false
	var shown []game.Event
	if !replaying {
		for _, ev := range evs {
			if ev.Text != "" {
				shown = append(shown, ev)
			}
			m.notePlayerLocked(inst, ev, sinceByName[ev.Name])
			if ev.Kind == game.KindSaved {
				run.saves++
			}
			if ev.Kind == "join" {
				m.machineIdleSince = time.Now()
				savePlayed = m.notePlayedLocked(run) || savePlayed
				seen = seen || m.firstJoinLocked(inst)
			}
			// The server's own count of who's connected, which a player
			// can't fake to empty the list: it holds the computer awake,
			// at least until the next count (spec 2.6).
			if ev.Kind == "count" && ev.Hold && ev.Count > 0 {
				now := time.Now()
				m.machineIdleSince = now
				if hold := now.Add(15 * time.Minute); m.keepAwakeUntil.Before(hold) {
					m.keepAwakeUntil = hold
				}
			}
		}
		// hooks.go: what happened, once the manager has noted it. A run
		// read back after a takeover gets onTakeover from caughtUp instead.
		if becameReady {
			m.observeLocked(func(o observer) { o.onReady(inst) })
		}
		for _, ev := range evs {
			m.observeLocked(func(o observer) { o.onEvent(inst, ev) })
		}
	}
	m.mu.Unlock()

	if savePlayed {
		m.savePlayedFor(inst)
	}
	if seen {
		m.go_(func() { m.notePresenceSeen(inst) })
	}
	if announce {
		m.queueDiscord(post)
		m.saveRunInfo(run)
	}
	if replaying {
		return // don't repeat old news when taking over a running server
	}
	for _, ev := range shown {
		m.instActivityAt(inst, ev.Time, ev.Kind, ev.Name, ev.Text)
	}
	if len(evs) > 0 {
		m.notify()
	}
}

func hasKind(evs []game.Event, kind string) bool {
	for _, ev := range evs {
		if ev.Kind == kind {
			return true
		}
	}
	return false
}

// pruneLogs keeps the instance's 30 newest server logs. It runs without
// m.mu, so it reads the folders through dirsOf.
func (m *Manager) pruneLogs(inst *instance) {
	matches, _ := filepath.Glob(filepath.Join(m.dirsOf(inst).Logs, logPrefix(inst)+"*.log"))
	sort.Strings(matches)
	for i := 0; i < len(matches)-30; i++ {
		os.Remove(matches[i])
	}
}
