package manager

// How the manager launches a game's server and follows it (spec 2.4, 2.6,
// 2.11): its output (a file the app gives it, or the game's own log, which
// the game may rotate), its console (a pipe on its standard input that the
// app keeps), and its probe (RCON, an HTTP API or A2S, for an
// authoritative count of who is on). The legacy game (Valheim) writes the
// log it is told to and has neither a console nor a probe, so for it this
// is today's tail and nothing more.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/rcon"
	"valheim-easy-server/internal/servers"
)

// Waits used while a game runs; tests shorten some.
var (
	probeStarting   = 5 * time.Second  // between probes until the server is ready
	probeReady      = 30 * time.Second // between probes once it is
	probeWait       = 3 * time.Second  // one probe's limit (the contract's)
	consoleTypeWait = 5 * time.Second  // a console that takes no input (the game stopped reading)
	consoleSaveWait = 10 * time.Second // the save line before the app exits (spec 2.11)
	stepCallWait    = 15 * time.Second // an RCON or "call" stop step's own limit
)

// probeFailWarn is how many probes in a row may fail once the server is
// ready before the game's page shows a warning (spec 2.6).
const probeFailWarn = 3

// launched is what launch set up for a run beyond its process.
type launched struct {
	stdin    *os.File // a console game's end of its stdin pipe
	saveLine string   // the console line that saves (Plan.SaveLine)
	gameLog  bool     // Output GameLog: the game writes its own log
	skip     logSkip  // what of that log is from before this run
}

// logSkip is what the tail skips of a log: from the start of file (when
// the path still names that file) up to size bytes, and the first lines
// lines it would copy (a takeover's copy already has them).
type logSkip struct {
	file  os.FileInfo
	size  int64
	lines int
}

// startProgram is platform.Start. Starting a program that was only just
// copied into place can fail on Linux with "text file busy" while another
// goroutine starts some other program at the same moment (the new process
// briefly holds the copy's handle, Go issue 22315): fake games are
// installed by copying the stand-in, and several can install and start at
// once. So it tries again a few times, as go build does.
var startProgram = func(o platform.StartOptions) (*platform.Proc, error) {
	for try := 1; ; try++ {
		p, err := platform.Start(o)
		if errors.Is(err, syscall.ETXTBSY) && try < 10 {
			time.Sleep(time.Duration(try) * 20 * time.Millisecond)
			continue
		}
		return p, err
	}
}

// launch starts a non-legacy game's server as its plan says (spec 2.4 and
// 2.11): with Capture its output goes to plan.LogPath, a file (never a
// pipe, so the output survives the app exiting); a console game gets a
// pipe on its standard input whose write end the app keeps and of which
// the game holds a copy, so it never sees the end of its input when the
// app exits; with GameLog the app notes how much of the game's own log is
// from before, so the tail doesn't read an old run's lines as this one's.
func (m *Manager) launch(plan game.Plan) (*platform.Proc, launched, error) {
	var l launched
	o := platform.StartOptions{Exe: plan.Exe, Args: plan.Args, Dir: plan.Dir, Env: append(os.Environ(), plan.Env...)}
	var out, stdinRead *os.File
	switch plan.Output {
	case game.Capture:
		if err := os.MkdirAll(filepath.Dir(plan.LogPath), 0o755); err != nil {
			return nil, l, err
		}
		f, err := os.OpenFile(plan.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, l, err
		}
		out = f
		o.Stdout, o.Stderr = f, f
	case game.GameLog:
		l.gameLog = true
		if fi, err := os.Stat(plan.LogPath); err == nil {
			l.skip = logSkip{file: fi, size: fi.Size()}
		}
	}
	if plan.Console {
		r, w, err := os.Pipe()
		if err != nil {
			if out != nil {
				out.Close()
			}
			return nil, l, err
		}
		o.Stdin, o.Inherit = r, []*os.File{w}
		stdinRead, l.stdin, l.saveLine = r, w, plan.SaveLine
	}
	proc, err := startProgram(o)
	// The game has its own copies of these now.
	if out != nil {
		out.Close()
	}
	if stdinRead != nil {
		stdinRead.Close()
	}
	if err != nil {
		if l.stdin != nil {
			l.stdin.Close()
		}
		return nil, launched{}, err
	}
	return proc, l, nil
}

// followSetup notes on a new run what its tail and console need: from the
// launch (l), or, after a takeover (l nil), from the run record alone. A
// taken-over game has no console pipe: its console stop steps give
// game.ErrNoConsole, and the chain moves on (2.11).
func (m *Manager) followSetup(run *serverRun, l *launched) {
	inst := run.inst
	if l != nil {
		run.stdin, run.saveLine, run.gameLog, run.logSkip = l.stdin, l.saveLine, l.gameLog, l.skip
	} else {
		run.gameLog = writesOwnLog(inst, run.rec)
	}
	run.ownLog = run.rec.LogPath
	if run.gameLog {
		run.ownLog = filepath.Join(inst.dirs.Logs, logPrefix(inst)+run.rec.Launched.Local().Format("2006-01-02_15-04-05")+".log")
		if l == nil {
			// The copy already has what the app read before it closed: the
			// catch-up reads the game's log again from its start, and
			// copies only what comes after those lines.
			run.logSkip.lines = countLines(run.ownLog)
		}
	}
}

// writesOwnLog says whether a run record's log is the game's own file
// (Output GameLog) rather than one the app named: the app names every log
// it gives a game <instance>-<time>.log in the instance's logs folder
// (Capture, LogArg), so any other path is the game's. The legacy game's
// log is always the app's.
func writesOwnLog(inst *instance, rec game.Record) bool {
	if inst.legacy || rec.LogPath == "" {
		return false
	}
	base := filepath.Base(rec.LogPath)
	return filepath.Dir(rec.LogPath) != inst.dirs.Logs || !strings.HasPrefix(base, logPrefix(inst)) || filepath.Ext(base) != ".log"
}

// logFile is the run's own log in its instance's logs folder: the one the
// Log page downloads, the records name runs by and pruning counts. It is
// the run record's log, except for a game that writes its own (GameLog),
// whose lines the app copies into a file named from the run's launch.
func (r *serverRun) logFile() string {
	if r.ownLog != "" {
		return r.ownLog
	}
	return r.rec.LogPath
}

// countLines counts the lines in a file; 0 when it can't be read.
func countLines(path string) int {
	b, err := readFile(path)
	if err != nil {
		return 0
	}
	return bytes.Count(b, []byte{'\n'})
}

// tail follows the server's log file, feeding each line to handleLine.
func (m *Manager) tail(ctx context.Context, run *serverRun) {
	t := &follower{path: run.rec.LogPath, shared: !run.inst.legacy, rotates: run.gameLog, skip: run.logSkip}
	if run.gameLog {
		t.copyPath, t.copySkip = run.ownLog, run.logSkip.lines
	}
	defer t.close()
	line := func(s string) { m.handleLine(run, s) }
	tick := time.NewTicker(400 * time.Millisecond)
	defer tick.Stop()
	misses := 0
	for {
		if t.read(line) {
			misses = 0
		} else {
			misses++
		}
		// A taken-over server's log read back to its end ends the catch-up.
		// So does a log that can't be opened (gone, or never written): its
		// catch-up would otherwise never end, and the online post and the
		// takeover's hooks would wait for good.
		if run.replaying && (t.f != nil || misses >= noLogTries) {
			m.caughtUp(run)
		}
		m.tick(run)
		select {
		case <-ctx.Done():
			t.flush(line)
			return
		case <-tick.C:
		}
	}
}

// noLogTries is how many reads (400 ms apart) a taken-over server's log
// may fail to open before the catch-up ends without it.
const noLogTries = 5

// follower reads a log as it grows. A game that writes its own log
// (rotates) may rename it and start a new one: the follower notices when
// the path names another file, reads what is left of the old one, and goes
// on with the new one from its start. Each line it reads is also copied
// into copyPath when set (the run's own log, for a game's own log).
type follower struct {
	path     string
	shared   bool // open with platform.OpenShared, so the game can rename it (Windows)
	rotates  bool
	skip     logSkip
	copyPath string
	copySkip int

	f       *os.File
	offset  int64
	partial []byte
	buf     []byte
	copy    *os.File
}

// read reads whatever the log has gained since the last read. It reports
// whether the log is open.
func (t *follower) read(line func(string)) bool {
	if t.path == "" {
		return false
	}
	if t.f != nil && t.rotates {
		if st, err := os.Stat(t.path); err == nil {
			if cur, err := t.f.Stat(); err == nil && !os.SameFile(st, cur) {
				// Rotated: the rest of the old file, then the new one.
				t.drain(line)
				t.flush(line)
				t.f.Close()
				t.f, t.offset = nil, 0
			}
		}
	}
	if t.f == nil {
		f, err := t.open()
		if err != nil {
			return false
		}
		t.f = f
		if t.skip.file != nil {
			// The same file as before this run started: what it had then
			// is the last run's.
			if cur, err := f.Stat(); err == nil && os.SameFile(cur, t.skip.file) && cur.Size() >= t.skip.size {
				if _, err := f.Seek(t.skip.size, io.SeekStart); err == nil {
					t.offset = t.skip.size
				}
			}
			t.skip.file = nil
		}
	}
	if st, err := t.f.Stat(); err == nil && st.Size() < t.offset {
		t.offset, t.partial = 0, nil // the file was replaced or truncated
		t.f.Seek(0, io.SeekStart)
	}
	t.drain(line)
	return true
}

func (t *follower) open() (*os.File, error) {
	if t.shared {
		return platform.OpenShared(t.path)
	}
	return os.Open(t.path)
}

// drain reads the open file to its end, handing on each whole line.
func (t *follower) drain(line func(string)) {
	if t.buf == nil {
		t.buf = make([]byte, 64*1024)
	}
	for {
		n, err := t.f.Read(t.buf)
		if n > 0 {
			t.offset += int64(n)
			t.partial = append(t.partial, t.buf[:n]...)
			for {
				i := bytes.IndexByte(t.partial, '\n')
				if i < 0 {
					break
				}
				t.emit(string(bytes.TrimRight(t.partial[:i], "\r")), line)
				t.partial = t.partial[i+1:]
			}
		}
		if err != nil || n == 0 {
			return
		}
	}
}

// flush hands on a last line that has no line break (yet).
func (t *follower) flush(line func(string)) {
	if len(t.partial) > 0 {
		t.emit(string(bytes.TrimRight(t.partial, "\r")), line)
		t.partial = nil
	}
}

// emit copies a line into the run's own log (when it has one) and hands it
// on.
func (t *follower) emit(s string, line func(string)) {
	if t.copyPath != "" {
		if t.copySkip > 0 {
			t.copySkip--
		} else {
			t.copyLine(s)
		}
	}
	line(s)
}

func (t *follower) copyLine(s string) {
	if t.copy == nil {
		os.MkdirAll(filepath.Dir(t.copyPath), 0o755)
		f, err := os.OpenFile(t.copyPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			t.copyPath = "" // can't be written: the game's own log still is followed
			return
		}
		t.copy = f
	}
	t.copy.WriteString(s + "\n")
}

func (t *follower) close() {
	if t.f != nil {
		t.f.Close()
	}
	if t.copy != nil {
		t.copy.Close()
	}
}

// --- the console (spec 2.11) -----------------------------------------------

// typeLine types a line on a console game's console. A game that stopped
// reading its console would leave the write waiting for good (the pipe is
// in blocking mode once the game is started), so the write runs on its own
// and the step gives up after consoleTypeWait.
func (m *Manager) typeLine(run *serverRun, line string) error {
	m.mu.Lock()
	w := run.stdin
	m.mu.Unlock()
	if w == nil {
		return game.ErrNoConsole // no console: taken over after an app restart, or not a console game
	}
	done := make(chan error, 1)
	go func() {
		_, err := io.WriteString(w, line+"\n")
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(consoleTypeWait):
		return errors.New("the game's console isn't taking input")
	}
}

// closeConsole lets go of the app's end of a run's console pipe. The game
// keeps its own copy of it, so it never sees its console end.
func (m *Manager) closeConsole(run *serverRun) {
	m.mu.Lock()
	w := run.stdin
	run.stdin = nil
	m.mu.Unlock()
	if w != nil {
		w.Close()
	}
}

// closeConsoles lets go of every console pipe, as the app closes.
func (m *Manager) closeConsoles() {
	m.eachInstance(func(inst *instance) {
		m.mu.Lock()
		run := inst.run
		m.mu.Unlock()
		if run != nil {
			m.closeConsole(run)
		}
	})
}

// saveConsoles types each running console game's save line and waits up to
// wait for it to say it saved, all at once (spec 2.11). It runs as the app
// closes (an update, Quit, Close app, the Mac's quit), because the copy of
// the app that takes the game over has no console to type a save on, so
// its stop can only use Ctrl+C.
func (m *Manager) saveConsoles(wait time.Duration) {
	var runs []*serverRun
	m.mu.Lock()
	m.eachInstance(func(inst *instance) {
		if r := inst.run; r != nil && r.stdin != nil && r.saveLine != "" && !r.stopping {
			runs = append(runs, r)
		}
	})
	m.mu.Unlock()
	done := make(chan struct{}, len(runs))
	for _, run := range runs {
		go func() {
			defer func() { done <- struct{}{} }()
			defer m.recoverPanic()
			m.saveConsole(run, wait)
		}()
	}
	for range runs {
		<-done
	}
}

// saveConsole types one game's save line and waits for its save.
func (m *Manager) saveConsole(run *serverRun, wait time.Duration) {
	inst := run.inst
	m.mu.Lock()
	saves, sent := run.saves, time.Now()
	m.mu.Unlock()
	if err := m.typeLine(run, run.saveLine); err != nil {
		m.log.Printf("saving %s before closing: %v", inst.id, err)
		return
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		saved := run.saves > saves || run.state().LastSave.After(sent)
		m.mu.Unlock()
		if saved {
			m.instActivity(inst, "saved", "", fmt.Sprintf("%s saved its world before the app closed.", inst.short()))
			return
		}
		select {
		case <-run.done:
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
	m.instActivity(inst, "warning", "", fmt.Sprintf("%s didn't say it saved before the app closed. Its own autosave still protects the world.", inst.short()))
}

// consoleInUseLocked lists the running console games whose console an app
// update would cut while someone plays, or may (their presence isn't
// known or not yet proven, 2.6): an automatic update waits for them
// (spec 2.11), and the panel warns before a manual one.
func (m *Manager) consoleInUseLocked() []string {
	var out []string
	m.eachInstance(func(inst *instance) {
		if m.consoleInUseOfLocked(inst) {
			out = append(out, inst.short())
		}
	})
	return out
}

// consoleInUseOfLocked says whether one game is such a console game.
func (m *Manager) consoleInUseOfLocked(inst *instance) bool {
	r := inst.run
	if r == nil || r.stdin == nil {
		return false
	}
	return r.state().Count > 0 || inst.info.Presence == game.Unknown || !m.presenceProvenLocked(inst)
}

// --- RCON stop steps ----------------------------------------------------------

// sendRCON sends an RCON stop step: to 127.0.0.1 on the port the run
// started with (role "rcon"), with the password it started with (secret
// "rcon"), so it works after a takeover too. A server that hangs up after
// the command (it is stopping) counts as sent, so its Wait follows; a
// refused connection or any other failure is the step failing, and the
// chain moves on.
func (m *Manager) sendRCON(ctx context.Context, run *serverRun, line string) error {
	m.mu.Lock()
	port, password := run.rec.Ports["rcon"], run.rec.Secrets["rcon"]
	m.mu.Unlock()
	if port == 0 {
		return errors.New(`the run record has no "rcon" port`)
	}
	cctx, cancel := context.WithTimeout(ctx, stepCallWait)
	defer cancel()
	_, err := rcon.Exec(cctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), password, line)
	if err != nil && (errors.Is(err, io.EOF) || connReset(err)) {
		return nil
	}
	return err
}

// --- probes (spec 2.6) ----------------------------------------------------------

// probeLoop asks a game that has a probe (RCON, an API, A2S) who is on: every
// probeStarting until the server is ready (failures then count for
// nothing), once at ready, and every probeReady while it is. Each answer
// goes through the Monitor's Probed, whose count is authoritative, and an
// answer with a count proves the game's presence (2.6) until a probe
// fails. Three failures in a row once it is ready show a warning on the
// game's page; the log keeps working alone. It waits while a takeover
// catches up.
func (m *Manager) probeLoop(ctx context.Context, run *serverRun, p game.Prober) {
	readyCh := run.readyCh
	timer := time.NewTimer(probeStarting)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-readyCh:
			readyCh = nil // once at ready, at once
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
		m.mu.Lock()
		if run.inst.run != run {
			m.mu.Unlock()
			return
		}
		replaying, ready, rec := run.replaying, run.ready, run.rec
		m.mu.Unlock()
		next := probeStarting
		if ready {
			next = probeReady
		}
		timer.Reset(next)
		if replaying {
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, probeWait)
		res, err := p.Probe(pctx, rec)
		cancel()
		if ctx.Err() != nil {
			return
		}
		m.mu.Lock()
		if run.inst.run != run {
			m.mu.Unlock()
			return
		}
		if err != nil {
			// The latest probe didn't answer, so its count no longer proves
			// who is on (2.6): the Monitor still holds the last answer, which
			// may be 0 while people play. Until the next answer, auto-stop
			// and idle sleep don't trust it and the "still playing?" rule
			// applies instead, unless the game's log presence is proven.
			unproven := run.probed
			run.probed = false
			warn := false
			if run.ready {
				run.probeFails++
				warn = run.probeFails == probeFailWarn
			}
			m.mu.Unlock()
			if warn {
				m.log.Printf("the %s probe failed %d times in a row: %v", run.inst.id, probeFailWarn, err)
			}
			if warn || unproven {
				m.notify()
			}
			continue
		}
		warned := run.probeFails >= probeFailWarn
		proven := !run.probed && res.Count >= 0
		run.probeFails = 0
		run.probed = res.Count >= 0
		sinceByName := m.sinceByNameLocked(run)
		evs := run.mon.Probed(res, time.Now())
		m.mu.Unlock()
		m.handleEvents(run, evs, sinceByName)
		if warned || proven {
			m.notify()
		}
	}
}

// probeWarningLocked is the game page's warning while its probe fails.
func (m *Manager) probeWarningLocked(inst *instance) string {
	if r := inst.run; r != nil && r.probeFails >= probeFailWarn {
		return fmt.Sprintf("The app can't reach the %s server's own count of players right now, so who is on comes from its log only.", inst.short())
	}
	return ""
}

// --- presence (spec 2.6) ----------------------------------------------------------

// firstJoinLocked reports whether a live join on a non-legacy instance is
// the first this install has seen of it, so the caller notes it in
// servers.json (presenceSeen): from then on its log's joins and leaves
// count as proven, and auto-stop and idle sleep may trust its count.
func (m *Manager) firstJoinLocked(inst *instance) bool {
	if inst.legacy || inst.srv == nil || inst.presenceNoting {
		return false
	}
	if in := m.servers.Instance(inst.id); in == nil || !in.PresenceSeen.IsZero() {
		return false
	}
	inst.presenceNoting = true
	return true
}

// notePresenceSeen writes the instance's first join into servers.json.
func (m *Manager) notePresenceSeen(inst *instance) {
	now := time.Now().UTC()
	err := m.updateServers(func(f *servers.File) error {
		if in := f.Instance(inst.id); in != nil && in.PresenceSeen.IsZero() {
			in.PresenceSeen = now
		}
		return nil
	})
	if err != nil {
		m.log.Printf("noting the first join of %s: %v", inst.id, err)
	}
	m.mu.Lock()
	inst.presenceNoting = false
	m.mu.Unlock()
}

// newWorldValues are the values a new world is made with: the new-world
// fields of the schema, from what the owner chose (kept with the world's
// values) or each field's default.
func newWorldValues(schema game.Schema, chosen game.Values) game.Values {
	out := game.Values{}
	for _, f := range schema {
		if f.Scope != game.NewWorldScope {
			continue
		}
		if v := chosen[f.Key]; v != "" {
			out[f.Key] = v
		} else if f.Default != "" {
			out[f.Key] = f.Default
		}
	}
	return out
}
