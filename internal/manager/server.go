package manager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/logparse"
	"valheim-easy-server/internal/mods"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/steamcmd"
	"valheim-easy-server/internal/worlds"
)

var errNotInstalled = &ActionError{
	Msg:    "The Valheim server isn't installed yet. Click Install first (it's free, about a 1 GB download).",
	Action: "install",
}

// StartServer starts the Valheim server (updating it first if that setting
// is on).
func (m *Manager) StartServer() error {
	m.mu.Lock()
	running := m.run != nil
	installed := m.installedLocked()
	errs := m.settings.Validate()
	m.mu.Unlock()
	switch {
	case running:
		return errors.New("The server is already running.")
	case !installed:
		return errNotInstalled
	case errs != nil:
		return errs // the panel shows these next to the settings
	}
	return m.runJob("start", "Starting the server", true, m.doStart)
}

// StopServer saves the world and stops the server.
func (m *Manager) StopServer() error {
	m.mu.Lock()
	running := m.run != nil
	m.mu.Unlock()
	if !running {
		return errors.New("The server isn't running.")
	}
	m.endStartWait()
	return m.runJob("stop", "Stopping the server", false, m.doStop)
}

// endStartWait finishes a start job that has launched the server and is
// only waiting for the world to load, so Stop works straight away.
func (m *Manager) endStartWait() {
	m.mu.Lock()
	job, cancel := m.job, m.jobCancel
	waiting := job != nil && job.Finished == nil && job.Kind == "start" && m.run != nil
	m.mu.Unlock()
	if !waiting {
		return
	}
	cancel()
	for i := 0; i < 50; i++ {
		m.mu.Lock()
		done := job.Finished != nil
		m.mu.Unlock()
		if done {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// RestartServer stops and starts the server again.
func (m *Manager) RestartServer() error {
	return m.runJob("restart", "Restarting the server", false, func(ctx context.Context, j *jobCtl) error {
		if err := m.doStop(ctx, j); err != nil {
			return err
		}
		return m.doStart(ctx, j)
	})
}

// ForceStop ends the server process immediately, without saving.
func (m *Manager) ForceStop() error {
	m.mu.Lock()
	run := m.run
	if run != nil {
		run.stopping, run.forced = true, true
	}
	m.mu.Unlock()
	if run == nil {
		return errors.New("The server isn't running.")
	}
	m.addActivity("warning", "", "The server was forced to stop. Anything since the last save is lost.")
	return run.proc.Kill()
}

// InstallServer installs or updates the server. repair re-checks all files.
func (m *Manager) InstallServer(repair bool) error {
	m.mu.Lock()
	running := m.run != nil
	installed := m.installedLocked()
	m.mu.Unlock()
	if running {
		return errors.New("Stop the server before updating it (Valheim can't update files that are in use).")
	}
	title := "Installing the Valheim server"
	switch {
	case repair:
		title = "Repairing the Valheim server"
	case installed:
		title = "Updating the Valheim server"
	}
	return m.runJob("install", title, true, func(ctx context.Context, j *jobCtl) error {
		err := m.opt.Installer.Install(ctx, repair, func(p steamcmd.Progress) { j.step(p.Step, p.Percent) })
		if ctx.Err() != nil {
			return errCancelled
		}
		if err != nil {
			return err
		}
		m.mu.Lock()
		exe := m.paths.ServerExe
		m.mu.Unlock()
		if _, err := os.Stat(exe); err != nil {
			return fmt.Errorf("SteamCMD finished, but %s wasn't found. Try Repair.", filepath.Base(exe))
		}
		m.addActivity("update", "", "The Valheim server is installed and up to date.")
		j.result("The Valheim server is installed and up to date.")
		return nil
	})
}

// argsKey identifies the settings a server was started with, so the panel
// can say when a restart is needed for changes to apply.
func argsKey(s config.Settings, saveDir string) string {
	args, err := s.ServerArgs(saveDir, "")
	if err != nil {
		return ""
	}
	key := strings.Join(args, "\x00")
	if ids := mods.ServerMods(s.WorldMods()); len(ids) > 0 { // players' own mods need no restart
		key += "\x00mods\x00" + strings.Join(slices.Sorted(slices.Values(ids)), "\x00")
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// prepareMods installs, updates and switches on the mods the selected world
// is played with, or switches the mod loader off when it has none.
func (m *Manager) prepareMods(ctx context.Context, j *jobCtl, s config.Settings, p config.Paths) error {
	in := &mods.Installer{Dir: p.Server, URL: m.opt.ModsURL}
	ids := s.WorldMods()
	if len(mods.ServerMods(ids)) > 0 {
		j.step("Getting the mods ready…", -1)
	}
	res, err := in.Prepare(ctx, ids, s.UpdateOnStart, j.step)
	installed := in.Installed()
	m.mu.Lock()
	m.modVersions = installed
	m.mu.Unlock()
	for _, c := range res.Changed {
		m.addActivity("update", "", c)
	}
	if res.Warning != "" {
		m.addActivity("warning", "", res.Warning)
	}
	if ctx.Err() != nil {
		return errCancelled
	}
	if err != nil {
		return &ActionError{Msg: fmt.Sprintf("Couldn't get the mods ready (%v). To start without them, turn them off on the world's card on the Worlds page.", err), Action: "worlds"}
	}
	return nil
}

func (m *Manager) doStart(ctx context.Context, j *jobCtl) error {
	m.mu.Lock()
	if m.run != nil {
		m.mu.Unlock()
		return errors.New("The server is already running.")
	}
	s, p := m.settings.Clone(), m.paths
	installed := m.installedLocked()
	m.mu.Unlock()

	if errs := s.Validate(); errs != nil {
		return &ActionError{Msg: "Please fix the settings first: " + errs.Error(), Action: "settings"}
	}
	if !installed {
		return errNotInstalled
	}
	// A copy may already be running (for example started before this app).
	if procs, _ := platform.FindByExe(p.ServerExe); len(procs) > 0 {
		if m.adopt(procs[0], runInfo{}) {
			j.result("The server was already running.")
			return nil
		}
	}
	if s.UpdateOnStart {
		j.step("Checking for Valheim server updates…", -1)
		err := m.opt.Installer.Install(ctx, false, func(pr steamcmd.Progress) {
			msg := pr.Step
			if msg == "Server files are up to date." {
				msg = "The server is up to date."
			}
			j.step(msg, pr.Percent)
		})
		if ctx.Err() != nil {
			return errCancelled
		}
		if err != nil {
			m.addActivity("warning", "", "Couldn't check for server updates ("+err.Error()+"). Starting the version that's installed.")
		}
	}
	if err := m.prepareMods(ctx, j, s, p); err != nil {
		return err
	}
	j.setCancellable(false)
	if platform.UDPPortInUse(s.Port) {
		return fmt.Errorf("Port %d is already being used by another program, maybe another Valheim server. Close it or pick another port in Settings.", s.Port)
	}
	for _, dir := range []string{p.Save, p.Worlds, p.Logs} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	logPath := filepath.Join(p.Logs, "server-"+time.Now().Format("2006-01-02_15-04-05")+".log")
	args, err := s.ServerArgs(p.Save, logPath)
	if err != nil {
		return err
	}
	env := append(os.Environ(), "SteamAppId=892970")
	if runtime.GOOS == "darwin" {
		// The macOS server comes without Steam's client library: SteamCMD
		// has one, for both kinds of Mac.
		env = append(env, "DYLD_FALLBACK_LIBRARY_PATH="+p.SteamCMD+":/usr/local/lib:/usr/lib")
	}
	env = append(env, mods.Env(p.Server, len(mods.ServerMods(s.WorldMods())) > 0)...)
	j.step("Starting the Valheim server…", -1)
	proc, err := platform.StartDetached(p.ServerExe, args, p.Server, env)
	if err != nil {
		return fmt.Errorf("Couldn't start the Valheim server: %v", err)
	}
	info := runInfo{PID: proc.PID(), Started: proc.StartTime(), Launched: time.Now(), LogPath: logPath,
		World: s.World, ArgsKey: argsKey(s, p.Save), Exe: p.ServerExe, Played: m.startedWith(s), NoCrossplay: !s.Crossplay}
	run := m.attach(proc, info, false)
	m.addActivity("starting", "", fmt.Sprintf("Starting the server with the world %q.", s.World))
	m.pruneLogs()

	loading := "Loading the world… The first start after an update can take a few minutes."
	if w, err := m.library().Get(s.World); errors.Is(err, worlds.ErrNotFound) || err == nil && w.NeverSaved {
		loading = "Making the new world… A world’s first start takes a few minutes."
	}
	j.step(loading, -1)
	select {
	case <-run.readyCh:
		j.result("The server is online.")
		return nil
	case <-run.done:
		msg := "The server stopped while starting. " + m.exitHint()
		for _, id := range s.WorldMods() {
			if md, ok := mods.Find(id); ok && md.NewWorlds {
				msg += fmt.Sprintf(" %s only loads worlds that were created with it: if %q wasn't, turn %s off for it on the Worlds page.", md.Name, s.World, md.Name)
			}
		}
		return &ActionError{Msg: msg, Action: "log"}
	case <-ctx.Done():
		j.result("Stopped waiting for the server to finish loading.")
		return nil
	case <-time.After(readyWait + 5*time.Minute):
		j.result("The server is running but hasn't said it's ready yet.")
		return nil
	}
}

// exitHint explains the most likely reason the server stopped, from the
// last lines of its log.
func (m *Manager) exitHint() string {
	lines, _ := m.logs.after(0, 80)
	for i := len(lines) - 1; i >= 0; i-- {
		t := lines[i].Text
		switch {
		case strings.Contains(t, "Error bad password"):
			return "Valheim rejected the password: it can't be part of the server name, the world name or the seed."
		case strings.Contains(t, "World load failed"):
			return "The world file couldn't be loaded. It may be damaged: restore a backup on the Worlds page."
		case strings.Contains(strings.ToLower(t), "address already in use"), strings.Contains(t, "Failed to bind"):
			return "The network port is in use by another program."
		}
	}
	return "Open the Log page to see what happened."
}

func (m *Manager) doStop(ctx context.Context, j *jobCtl) error {
	m.mu.Lock()
	run := m.run
	if run == nil {
		m.mu.Unlock()
		return nil
	}
	run.stopping = true
	lastSave := run.tracker.LastSave
	m.mu.Unlock()
	m.notify()

	j.step("Saving the world and shutting down… Please don't turn off the computer.", -1)
	if err := run.proc.Interrupt(); err != nil {
		m.mu.Lock()
		run.stopping = false
		m.mu.Unlock()
		return &ActionError{
			Msg:    fmt.Sprintf("Couldn't ask the server to save and stop (%v). You can force it to stop, but progress since the last save%s will be lost.", err, sinceText(lastSave)),
			Action: "force_stop",
		}
	}
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
			return nil
		case <-ctx.Done():
			return nil // the app is closing; the server finishes stopping by itself
		case <-tick.C:
			waited := time.Since(start)
			m.mu.Lock()
			saved := run.tracker.LastSave.After(start)
			m.mu.Unlock()
			if saved {
				j.step("World saved. Shutting down…", -1)
			} else {
				j.step(fmt.Sprintf("Saving the world… (%d s) Please don't turn off the computer.", int(waited.Seconds())), -1)
			}
			if waited > 90*time.Second && !resent {
				resent = true
				run.proc.Interrupt()
			}
			if waited > 4*time.Minute {
				m.mu.Lock()
				run.stopping = false
				m.mu.Unlock()
				return &ActionError{
					Msg:    "The server is taking unusually long to stop. You can wait and try again, or force it to stop (progress since the last save will be lost).",
					Action: "force_stop",
				}
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
func (m *Manager) attach(proc *platform.Proc, info runInfo, adopted bool) *serverRun {
	ctx, cancel := context.WithCancel(m.ctx)
	run := &serverRun{proc: proc, info: info, done: make(chan struct{}), readyCh: make(chan struct{}),
		cancelTail: cancel, replaying: adopted}
	run.tracker.NoJoinCode = info.NoCrossplay
	m.mu.Lock()
	m.run = run
	m.idleSince = time.Now()
	runtimePath := m.dataFile("runtime.json")
	m.mu.Unlock()
	saveRuntime(runtimePath, info)
	m.updateAwake()
	m.go_(func() { m.tail(ctx, run) })
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

// adopt takes over a server that is already running.
func (m *Manager) adopt(pi platform.ProcInfo, saved runInfo) bool {
	proc, err := platform.OpenProc(pi.PID)
	if err != nil {
		return false
	}
	info := saved
	if info.PID != pi.PID {
		m.mu.Lock()
		info = runInfo{PID: pi.PID, Started: pi.Started, Launched: pi.Started, World: m.settings.World,
			LogPath: newestLog(m.paths.Logs), Exe: pi.Exe, NoCrossplay: !m.settings.Crossplay}
		m.mu.Unlock()
	}
	if info.Launched.IsZero() {
		info.Launched = proc.StartTime()
	}
	m.attach(proc, info, true)
	m.addActivity("info", "", "Found the Valheim server already running and took over watching it.")
	return true
}

// adoptRunning looks for a server left running by an earlier session.
func (m *Manager) adoptRunning() {
	m.mu.Lock()
	exe := m.paths.ServerExe
	runtimePath := m.dataFile("runtime.json")
	m.mu.Unlock()
	if info, err := loadRuntime(runtimePath); err == nil {
		if proc, err := platform.OpenProc(info.PID); err == nil {
			same := absDuration(proc.StartTime().Sub(info.Started)) < 2*time.Second
			proc.Close()
			if same {
				m.adopt(platform.ProcInfo{PID: info.PID, Exe: info.Exe, Started: info.Started}, info)
				return
			}
		}
		os.Remove(runtimePath)
	}
	if procs, _ := platform.FindByExe(exe); len(procs) > 0 {
		m.adopt(procs[0], runInfo{})
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
	now := time.Now()
	if m.ctx.Err() != nil {
		return // the app is closing; the next start of the app takes over
	}
	m.mu.Lock()
	if m.run != run {
		m.mu.Unlock()
		return
	}
	// A shutdown line only counts shortly before the exit: a player can
	// make one up, which mustn't hide a later crash.
	expected := run.stopping || (run.tracker.Stopping && now.Sub(run.tracker.StoppingAt) < 5*time.Minute)
	for _, p := range run.tracker.Players() {
		m.notePlayerLocked(logparse.Activity{Kind: "leave", Time: now, Name: p.Name, PlatformID: p.PlatformID}, p.Since)
	}
	noteEventsLocked(run, run.tracker.Reset(now))
	archived := m.archiveRunLocked(run)
	m.run = nil
	m.idleSince = now
	exit := &ExitInfo{At: now, Code: code, Expected: expected, World: run.info.World,
		Uptime: now.Sub(run.info.Launched).Seconds()}
	autoRestart := false
	if !expected {
		lines, _ := m.logs.after(0, 30)
		for _, l := range lines {
			exit.LogTail = append(exit.LogTail, l.Text)
		}
		// Restart after a crash, but give up after 3 crashes in 15 minutes.
		recent := m.crashes[:0]
		for _, t := range m.crashes {
			if now.Sub(t) < 15*time.Minute {
				recent = append(recent, t)
			}
		}
		m.crashes = append(recent, now)
		autoRestart = m.settings.AutoRestart && len(m.crashes) <= 3 && run.ready
	}
	m.lastExit = exit
	runtimePath := m.dataFile("runtime.json")
	why := stopStopped
	job := m.job
	switch {
	case !expected && autoRestart:
		why = stopCrashRestart
	case !expected:
		why = stopCrash
	case job != nil && job.Finished == nil && job.Kind == "restart":
		why = stopRestart
	case job != nil && job.Finished == nil && job.Kind == "hibernate":
		why = stopRest
	}
	// Queued before unlocking, so anyone who sees the server stopped (and
	// closes the app) knows the message is waiting to go out.
	if post, ok := m.stoppedPostLocked(run, why); ok {
		m.queueDiscord(post)
	}
	m.mu.Unlock()

	run.proc.Close()
	os.Remove(runtimePath)
	m.updateAwake() // still awake if it has just woken up
	if expected {
		m.addActivity("stopped", "", "The server stopped.")
	} else {
		m.addActivity("crashed", "", fmt.Sprintf("The server stopped unexpectedly (exit code %d). %s", code, m.exitHint()))
	}
	if autoRestart {
		m.addActivity("info", "", fmt.Sprintf("Restarting the server in %d seconds because \"Restart if it crashes\" is on.", int(m.opt.RestartDelay.Seconds())))
		time.AfterFunc(m.opt.RestartDelay, func() {
			m.mu.Lock()
			still := m.run == nil && m.ctx.Err() == nil
			m.mu.Unlock()
			if still {
				if err := m.StartServer(); err != nil {
					m.log.Printf("auto restart: %v", err)
				}
			}
		})
	}
	m.saveData()
	if archived {
		m.saveRecords()
	}
	m.notify()
}

// tail follows the server's log file.
func (m *Manager) tail(ctx context.Context, run *serverRun) {
	var f *os.File
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	var offset int64
	var partial []byte
	buf := make([]byte, 64*1024)
	tick := time.NewTicker(400 * time.Millisecond)
	defer tick.Stop()
	for {
		if f == nil && run.info.LogPath != "" {
			if fh, err := os.Open(run.info.LogPath); err == nil {
				f = fh
			}
		}
		if f != nil {
			if st, err := f.Stat(); err == nil && st.Size() < offset {
				offset, partial = 0, nil // the file was replaced or truncated
				f.Seek(0, io.SeekStart)
			}
			for {
				n, err := f.Read(buf)
				if n > 0 {
					offset += int64(n)
					partial = append(partial, buf[:n]...)
					for {
						i := bytes.IndexByte(partial, '\n')
						if i < 0 {
							break
						}
						m.handleLine(run, string(bytes.TrimRight(partial[:i], "\r")))
						partial = partial[i+1:]
					}
				}
				if err != nil || n == 0 {
					break
				}
			}
			if run.replaying {
				m.mu.Lock()
				run.replaying = false // caught up with the existing log
				// Remember players who joined while this app was closed.
				for _, p := range run.tracker.Players() {
					if m.known[p.Name] == nil {
						m.notePlayerLocked(logparse.Activity{Kind: "join", Time: p.Since, Name: p.Name, PlatformID: p.PlatformID}, time.Time{})
					}
				}
				// Post the join code if the app that started the server didn't.
				post, announce := m.announceLocked(run)
				m.mu.Unlock()
				if announce {
					m.queueDiscord(post)
					m.saveRunInfo(run)
				}
				m.notify()
			}
		}
		m.checkOverdue(run)
		select {
		case <-ctx.Done():
			if len(partial) > 0 {
				m.handleLine(run, string(partial))
			}
			return
		case <-tick.C:
		}
	}
}

// handleLine processes one line of server output.
func (m *Manager) handleLine(run *serverRun, line string) {
	if logparse.IsNoise(line) {
		return
	}
	ev := logparse.Parse(line)
	at := ev.Time
	if at.IsZero() {
		at = time.Now()
	}
	text := ev.Text // without Valheim's own timestamp; Time carries it
	m.logs.add(func(seq int64) LogLine { return LogLine{Seq: seq, Time: at, Text: text} })
	m.handleEvent(run, ev)
}

// readyWait is how long after logging in to Steam a server that never says
// it opened is counted as ready anyway. Making a big new world can take
// several minutes on an older computer.
var readyWait = 10 * time.Minute

// checkOverdue counts a server as ready when it logged in to Steam long ago
// but never said it opened, so a Valheim update that words that line
// differently can't leave it "starting" forever.
func (m *Manager) checkOverdue(run *serverRun) {
	m.mu.Lock()
	overdue := m.run == run && !run.replaying && run.tracker.Overdue(time.Now(), readyWait)
	m.mu.Unlock()
	if overdue {
		m.log.Printf("the server logged in to Steam %v ago but never said it opened; counting it as online", readyWait)
		m.handleEvent(run, logparse.Event{Kind: logparse.ServerReady, Count: -1})
	}
}

// handleEvent updates the server's state with one log event.
func (m *Manager) handleEvent(run *serverRun, ev logparse.Event) {
	m.mu.Lock()
	if m.run != run {
		m.mu.Unlock()
		return
	}
	before := m.stateLocked()
	sinceByName := map[string]time.Time{}
	for _, p := range run.tracker.Players() {
		sinceByName[p.Name] = p.Since
	}
	acts := run.tracker.Apply(ev, time.Now())
	noteEventsLocked(run, acts)
	if run.tracker.Ready && !run.ready {
		run.ready, run.readyAt = true, time.Now()
		close(run.readyCh)
	}
	post, announce := m.announceLocked(run)
	replaying := run.replaying
	savePlayed := false
	if !replaying {
		for _, a := range acts {
			m.notePlayerLocked(a, sinceByName[a.Name])
			if a.Kind == "join" {
				m.idleSince = time.Now()
				savePlayed = m.notePlayedLocked(run) || savePlayed
			}
		}
	}
	changed := before != m.stateLocked()
	m.mu.Unlock()

	if savePlayed {
		m.savePlayed()
	}
	if announce {
		m.queueDiscord(post)
		m.saveRunInfo(run)
	}
	if replaying {
		return // don't repeat old news when taking over a running server
	}
	for _, a := range acts {
		m.addActivityAt(a.Time, a.Kind, a.Name, a.Text)
	}
	if changed || len(acts) > 0 {
		m.notify()
	}
}

// pruneLogs keeps the 30 newest server logs.
func (m *Manager) pruneLogs() {
	m.mu.Lock()
	dir := m.paths.Logs
	m.mu.Unlock()
	matches, _ := filepath.Glob(filepath.Join(dir, "server-*.log"))
	sort.Strings(matches)
	for i := 0; i < len(matches)-30; i++ {
		os.Remove(matches[i])
	}
}
