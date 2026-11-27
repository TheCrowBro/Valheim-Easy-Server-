package manager

// Tests of what R2 adds to the manager (spec 7.2): the legacy game through
// the game contract with its folders unchanged, other games' servers it
// only watches, the wake packet's game, and that New and Status stay
// quick.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/game"
	// The registry the app ships: main imports internal/games/all, so the
	// count below must see every game wired in there, not only Valheim,
	// which legacy_names_test.go imports by itself. (Only test files may
	// import it here: the manager's own code reaches games through the
	// registry, which TestImportRules checks.)
	_ "valheim-easy-server/internal/games/all"
	"valheim-easy-server/internal/jetkvm/fakekvm"
	"valheim-easy-server/internal/mods/modstest"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/servers"
	"valheim-easy-server/internal/steamcmd"
	"valheim-easy-server/internal/wol"
)

// I1: every Valheim folder the manager and its adapter use is
// config.ResolvePaths', with and without the folder overrides, before and
// after a settings save.
func TestLegacyPathsUnchanged(t *testing.T) {
	env := &testEnv{data: t.TempDir()}
	m := newTestManager(t, env.data, env)
	check := func(what string) {
		t.Helper()
		p := config.ResolvePaths(env.data, m.Settings())
		inst := m.legacy()
		m.mu.Lock()
		d := inst.dirs
		m.mu.Unlock()
		if d.Root != env.data || d.Server != p.Server || d.Saves != p.Save || d.Backups != p.Backups ||
			d.Logs != p.Logs || d.Staging != p.Staging || d.SteamCMD != p.SteamCMD {
			t.Errorf("%s: folders %+v, want those of %+v", what, d, p)
		}
		if got := inst.srv.Program(); got != p.ServerExe {
			t.Errorf("%s: program %s, want %s", what, got, p.ServerExe)
		}
		if got := m.Paths(); got != p {
			t.Errorf("%s: Paths %+v, want %+v", what, got, p)
		}
		if lib := m.library(); lib.Worlds != p.Worlds || lib.Backups != p.Backups || lib.Staging != p.Staging {
			t.Errorf("%s: world library %+v", what, lib)
		}
		if in := m.opt.Installer.(*steamcmd.Fake); in.InstallDir != p.Server {
			t.Errorf("%s: installs into %s, want %s", what, in.InstallDir, p.Server)
		}
		if got := m.runFile(inst); got != filepath.Join(env.data, "runtime.json") {
			t.Errorf("%s: run file %s", what, got)
		}
		if got := m.instFile(inst, "players.json"); got != filepath.Join(env.data, "players.json") {
			t.Errorf("%s: players file %s", what, got)
		}
	}
	check("defaults")
	s := m.Settings()
	s.ServerDir, s.SaveDir = filepath.Join(t.TempDir(), "server"), filepath.Join(t.TempDir(), "saves")
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	check("other folders")
	s.ServerDir, s.SaveDir = "", ""
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	check("back to the data folder")
}

// writeRun writes a run record as a newer version writes it.
func writeRun(t *testing.T, data, id string, rec game.Record) string {
	t.Helper()
	path := filepath.Join(data, "games", id, "run.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// thisProcess stands in for another game's server: a process that is
// running, with its start time.
func thisProcess(t *testing.T) (int, time.Time) {
	t.Helper()
	p, err := platform.OpenProc(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	return os.Getpid(), p.StartTime()
}

// A newer version's other game still running after a rollback (spec 3.6):
// this version keeps the computer awake for it, never lets it sleep by
// itself, says so, and doesn't touch its files.
func TestForeignInstance(t *testing.T) {
	env := &testEnv{data: t.TempDir()}
	m := newTestManager(t, env.data, env)
	s := m.Settings()
	s.IdleHibernate, s.IdleMinutes = true, 10
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	pid, started := thisProcess(t)
	live := writeRun(t, env.data, "minecraft", game.Record{PID: pid, Started: started, Launched: started,
		Game: "minecraft", Instance: "minecraft", Name: "Minecraft", Port: 25565})
	// Its PID used again by another program: not that server any more.
	stale := writeRun(t, env.data, "terraria", game.Record{PID: pid, Started: started.Add(-time.Hour), Name: "Terraria"})
	broken := filepath.Join(env.data, "games", "necesse", "run.json")
	os.MkdirAll(filepath.Dir(broken), 0o755)
	os.WriteFile(broken, []byte("{not json"), 0o600)
	before := map[string][]byte{}
	for _, f := range []string{live, stale, broken} {
		b, _ := os.ReadFile(f)
		before[f] = b
	}

	m.scanForeign()
	const warning = "Minecraft is running, started by a newer version of the app; this version can't control it."
	st := m.Status()
	if !slices.Contains(st.Warnings, warning) {
		t.Fatalf("warnings: %q", st.Warnings)
	}
	for _, w := range st.Warnings {
		if strings.Contains(w, "Terraria") || strings.Contains(w, "necesse") {
			t.Errorf("warned about a server that isn't running: %q", w)
		}
	}
	m.mu.Lock()
	awake := m.wantAwakeLocked(time.Now())
	deadline := m.idleDeadlineLocked()
	m.machineIdleSince = time.Now().Add(-time.Hour) // nobody has done anything for an hour
	m.mu.Unlock()
	if !awake || !deadline.IsZero() {
		t.Fatalf("kept awake %v, sleeps at %v", awake, deadline)
	}
	m.checkIdle(time.Now())
	if env.slept.Load()+env.hibernated.Load() != 0 {
		t.Fatal("the computer went to sleep while another game's server ran")
	}
	for f, b := range before {
		if now, _ := os.ReadFile(f); string(now) != string(b) {
			t.Errorf("%s was changed", f)
		}
	}

	// Once its process has ended, it's forgotten.
	old := openProc
	openProc = func(int) (*platform.Proc, error) { return nil, os.ErrNotExist }
	t.Cleanup(func() { openProc = old })
	m.scanForeign()
	m.mu.Lock()
	awake = m.wantAwakeLocked(time.Now())
	deadline = m.idleDeadlineLocked()
	m.mu.Unlock()
	if awake || deadline.IsZero() || slices.Contains(m.Status().Warnings, warning) {
		t.Fatalf("after it ended: awake %v, sleeps at %v, warnings %q", awake, deadline, m.Status().Warnings)
	}
}

// R2 runs only the legacy game: a wake packet for another game (from a
// newer app's waker, after a rollback) starts nothing (spec 6.4, 8.1).
func TestWakePacketForOtherGame(t *testing.T) {
	mac := localMAC()
	if mac == nil {
		t.Skip("no network adapter with an address")
	}
	env := setup(t)
	m := env.m
	install(t, m)
	old := wakePacketGame
	t.Cleanup(func() { wakePacketGame = old })
	asked := "minecraft"
	wakePacketGame = func([]byte) string { return asked }

	jobs := currentJobID(m)
	m.hearWakeSignals(&scriptedConn{reads: []any{wol.Packet(mac, []byte("mc0001"))}})
	time.Sleep(500 * time.Millisecond)
	if st := m.Status(); st.State != StateStopped || currentJobID(m) != jobs {
		t.Fatalf("a packet for another game: state %s, job %+v", st.State, st.Job)
	}
	asked = m.legacyID
	m.hearWakeSignals(&scriptedConn{reads: []any{wol.Packet(mac, []byte("vh0001"))}})
	waitFor(t, "the server to start", 20*time.Second, func() bool { return m.Status().State == StateOnline })
}

// The legacy game's work after launch runs again after a takeover when the
// app that started the server closed before it was done (spec 4.4): a
// record with codeMods and no modsCode gets its mods code, and the online
// post has it.
func TestAdoptRerunsAfter(t *testing.T) {
	store := httptest.NewServer(modstest.New())
	defer store.Close()
	chat := newFakeDiscord(t)
	env := setup(t)
	m := env.m
	m.opt.ModsURL, m.opt.DiscordURL = store.URL, chat.URL
	install(t, m)
	turnOnDiscord(t, m)
	if err := m.SetWorldMods(m.Settings().World, []string{"advancedportals"}); err != nil {
		t.Fatal(err)
	}
	join := startOnline(t, m)
	chat.wait(t, "Join code: **"+join+"**", 1)
	waitFor(t, "the post to be noted", 10*time.Second, func() bool {
		d := m.Status().Discord
		return d != nil && d.Posted != nil
	})
	m.Close()

	// As if the app had closed before the code was made.
	path := filepath.Join(env.data, "runtime.json")
	rec, err := loadRuntime(path)
	if err != nil || rec.Extra["codeMods"] == nil || rec.Extra["modsCode"] == nil {
		t.Fatalf("runtime.json: %+v, %v", rec, err)
	}
	delete(rec.Extra, "modsCode")
	rec.Announced, rec.Posted, rec.PostError = "", time.Time{}, ""
	saveRuntime(path, rec)

	m2 := newTestManager(t, env.data, env)
	m2.opt.ModsURL, m2.opt.DiscordURL = store.URL, chat.URL
	m2.Start()
	env.m = m2
	chat.wait(t, "Join code: **"+join+"**", 2)
	posts := chat.all()
	if last := posts[len(posts)-1]; !strings.Contains(last, "📦 Mods code: `") {
		t.Fatalf("the post after the takeover has no mods code:\n%s", last)
	}
	if m2.Status().ModsCode == "" {
		t.Fatal("no mods code on the panel")
	}
}

// otherGames are ten folders like a newer version's other games.
var otherGames = []string{"minecraft", "terraria", "necesse", "vintagestory", "zomboid", "enshrouded",
	"corekeeper", "palworld", "satisfactory", "windrose"}

// countAccess counts the run records read and the processes opened while
// fn runs (the counting hook of spec 4.8 and I5).
func countAccess(t *testing.T, fn func()) (games, procs int) {
	t.Helper()
	var mu sync.Mutex
	oldRead, oldOpen := readFile, openProc
	readFile = func(path string) ([]byte, error) {
		mu.Lock()
		if strings.Contains(filepath.ToSlash(path), "/games/") {
			games++
		}
		mu.Unlock()
		return oldRead(path)
	}
	openProc = func(pid int) (*platform.Proc, error) {
		mu.Lock()
		procs++
		mu.Unlock()
		return oldOpen(pid)
	}
	defer func() { readFile, openProc = oldRead, oldOpen }()
	fn()
	mu.Lock()
	defer mu.Unlock()
	return games, procs
}

// Status is read all the time by the panel while the settings change and
// the server's output comes in (run with -race).
func TestStatusWhileThingsChange(t *testing.T) {
	env := setup(t)
	m := env.m
	install(t, m)
	if err := m.StartServer(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "server online", 20*time.Second, func() bool { return m.Status().State == StateOnline })
	m.mu.Lock()
	run := m.legacy().run
	m.mu.Unlock()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				m.Status()
			}
		}
	}()
	go func() {
		defer wg.Done()
		s := m.Settings()
		for i := 0; i < 40; i++ {
			s.ServerName = fmt.Sprintf("Viking Night %d", i%3)
			if err := m.UpdateSettings(s); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for i := 0; i < 300; i++ {
		m.handleLine(run, fmt.Sprintf("Connections %d ZDOS:1000  sent:0 recv:0", i%3))
	}
	close(stop)
	wg.Wait()
	if st := m.Status(); st.State != StateOnline || !strings.HasPrefix(st.ServerName, "Viking Night") || !st.NeedsRestart {
		t.Fatalf("state %s, name %q, restart needed %v", st.State, st.ServerName, st.NeedsRestart)
	}
}

// Saving the settings while the server starts is safe: a save replaces the
// legacy instance's folders (the owner can move the server and save
// folders), and the start reads them to name its log file and to prune old
// logs (run with -race). The saves don't stop, restart or fail the start.
//
// The saves go on, back to back, until the start has launched the server,
// which it does after naming the log (the second goroutine prunes the logs
// all the while); only then does the test wait for the server. It never
// times the start while the saves go on: a save holds the manager's lock
// while it flushes settings.json to the disk, which on a Mac is a full
// flush of the drive (F_FULLFSYNC) and on a busy CI Mac can take hundreds
// of milliseconds, and the start takes turns at that lock with the saves,
// so there a start under this load can take many times as long as usual
// (build 33 saved the same way). Nobody saves like this in real use.
func TestSettingsSaveDuringStart(t *testing.T) {
	env := setup(t)
	m := env.m
	install(t, m)
	inst := m.legacy()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var once sync.Once
	stopLoad := func() { once.Do(func() { close(stop); wg.Wait() }) }
	var start *Job // the start's job
	startEnded := func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return start == nil || start.Finished != nil
	}
	// Cleanups run last first, so this one runs before setup's, which
	// closes the manager, and before the test's folder is removed: nothing
	// may still be saving there, and the server must have exited (on
	// Windows a server that is still running holds its log open, and the
	// folder can't be removed).
	//
	// It forces the server to stop before it waits for the start: if the
	// test failed because the server never came online, the start would
	// otherwise go on waiting for it for many minutes (the game's ready
	// wait, plus the manager's 5). The server exiting ends that wait at
	// once. With no server running, ForceStop only returns an error.
	t.Cleanup(func() {
		stopLoad()
		if m.ForceStop() == nil {
			waitFor(t, "the server to exit", 30*time.Second, func() bool {
				m.mu.Lock()
				defer m.mu.Unlock()
				return inst.run == nil
			})
		}
		waitFor(t, "the start to end", time.Minute, startEnded)
	})

	wg.Add(2)
	go func() {
		defer wg.Done()
		s := m.Settings()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			s.ServerName = fmt.Sprintf("Viking Night %d", i%3) // the folders stay the same
			if err := m.UpdateSettings(s); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				m.pruneLogs(inst)
				m.CurrentLogPath()
			}
		}
	}()

	if err := m.StartServer(); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	start = inst.job // set before StartServer returns
	m.mu.Unlock()
	// Only a guard against a hang, not a time the start must keep to.
	waitFor(t, "the start to launch the server", 5*time.Minute, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return inst.run != nil || start.Finished != nil
	})
	stopLoad()

	// With the saves over, the start finishes as usual.
	waitFor(t, "the start to end", 2*time.Minute, startEnded)
	m.mu.Lock()
	failed := start.Error
	m.mu.Unlock()
	if failed != "" {
		t.Fatalf("the start failed: %s", failed)
	}
	// Online, and never stopped on the way (a stop or a restart would have
	// left how it ended).
	if st := m.Status(); st.State != StateOnline || st.LastExit != nil {
		t.Fatalf("state %s, last exit %+v after the start, want online and none", st.State, st.LastExit)
	}
	if got := m.CurrentLogPath(); filepath.Dir(got) != m.Paths().Logs {
		t.Fatalf("log %s isn't in %s", got, m.Paths().Logs)
	}

	// Stopped the usual way, so it has exited before the test's folder,
	// where it writes its log and world, is removed.
	if err := m.StopServer(); err != nil {
		t.Fatal(err)
	}
	if job := waitJob(t, m, currentJobID(m)); job.Kind != "stop" || job.Error != "" {
		t.Fatalf("stop: %+v", job)
	}
	if st := m.Status(); st.State != StateStopped {
		t.Fatalf("state %s after the stop, want stopped", st.State)
	}
}

// The old panel follows one job (Status.job): the newer of the legacy
// game's and the app's. The waker's install and removal and the firewall
// fix aren't jobs, so they never change it, and a finished Sleep job is
// still reported (spec 4.2 and 8.2's R2 gate).
func TestStatusJobIsTheOldPanels(t *testing.T) {
	// The waker goes on a stand-in JetKVM, which runs only on Linux (it
	// runs the JetKVM's own start script). The rest of the test runs on
	// every system.
	var kvm *fakekvm.Server
	if runtime.GOOS == "linux" {
		kvm = fakekvm.New(t, nil)
	}
	hook := newFakeDiscord(t)
	env := setup(t, wakerOptions(t, kvm, hook.URL,
		platform.NetAdapter{Name: "Ethernet", MAC: "3C:07:54:5A:21:9F", IPv4: []string{"192.168.1.20"}, Wired: true, Connected: true}))
	m := env.m
	if err := m.BackupWorld("Nowhere"); err != nil {
		t.Fatal(err)
	}
	backup := waitJob(t, m, currentJobID(m))
	if backup.Kind != "backup" || backup.Game != m.legacyID {
		t.Fatalf("job %+v", backup)
	}
	// unchanged runs fn while reading Status the way the panel does, and
	// fails if Status.job is ever anything but the backup's, during fn or
	// after it.
	unchanged := func(what string, fn func()) {
		t.Helper()
		done := make(chan struct{})
		seen := make(chan *Job, 1) // the first other job seen, if any
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if j := m.Status().Job; j == nil || j.ID != backup.ID {
					seen <- j
					return
				}
				select {
				case <-done:
					return
				case <-time.After(time.Millisecond):
				}
			}
		}()
		func() {
			defer func() { close(done); wg.Wait() }()
			fn()
		}()
		select {
		case j := <-seen:
			t.Fatalf("while %s: job %+v", what, j)
		default:
		}
		if j := m.Status().Job; j == nil || j.ID != backup.ID {
			t.Fatalf("after %s: job %+v", what, j)
		}
	}
	if kvm != nil {
		unchanged("installing the waker", func() {
			key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(m.WakerView(false).Key))
			if err != nil {
				t.Fatal(err)
			}
			kvm.Allow(key) // pasted in the JetKVM's Developer Mode
			if err := m.InstallWaker(kvm.Addr, "secret", false); err != nil {
				t.Fatal(err)
			}
		})
		if !m.Settings().DiscordWake {
			t.Fatal("the waker isn't on after its install")
		}
		unchanged("taking the waker off", func() {
			if err := m.RemoveWaker(false); err != nil {
				t.Fatal(err)
			}
		})
	} else {
		unchanged("taking the waker off", func() { m.RemoveWaker(true) })
	}
	// The firewall fix only here: on Linux it answers at once that it
	// can't, while on Windows and macOS it would ask for an administrator's
	// OK, which a test can't give.
	if runtime.GOOS == "linux" {
		unchanged("fixing the firewall", func() {
			if err := m.FixFirewall(); !errors.Is(err, platform.ErrUnsupported) {
				t.Fatalf("FixFirewall on Linux: %v", err)
			}
		})
	}
	if err := m.Hibernate(""); err != nil {
		t.Fatal(err)
	}
	slept := waitJob(t, m, currentJobID(m))
	if slept.Kind != "hibernate" || slept.Game != "" || slept.Error != "" || slept.ID <= backup.ID {
		t.Fatalf("sleep job %+v", slept)
	}
	st := m.Status()
	if st.Job == nil || st.Job.ID != slept.ID || st.Job.Result == "" {
		t.Fatalf("the finished sleep job isn't reported: %+v", st.Job)
	}
	if len(st.Jobs) != 2 {
		t.Fatalf("jobs %+v", st.Jobs)
	}
}

// I9: the files with passwords and tokens are readable only by the user
// running the app.
func TestSecretFilesPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows doesn't use these permission bits")
	}
	env := &testEnv{data: t.TempDir()}
	m := newTestManager(t, env.data, env)
	saveRuntime(m.runFile(m.legacy()), game.Record{PID: 1, Password: "secret123"})
	m.saveWaker()
	other := filepath.Join(env.data, "games", "minecraft", "run.json")
	saveRuntime(other, game.Record{PID: 1, Secrets: map[string]string{"rcon": "x"}})
	// servers.json holds Discord webhooks and adapters' passwords; changing
	// it keeps the previous copy as its backup, which holds them too.
	if err := m.SetSidebar(false, false); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"settings.json", "runtime.json", "jetkvm.json", filepath.Join("games", "minecraft", "run.json"),
		servers.FileName, servers.FileName + servers.BackupSuffix} {
		fi, err := os.Stat(filepath.Join(env.data, name))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", name, fi.Mode().Perm())
		}
	}
}
