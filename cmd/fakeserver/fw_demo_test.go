package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"valheim-easy-server/internal/a2s"
	"valheim-easy-server/internal/rcon"
)

// These run the built program as the demo personality, the way the app
// runs a game's server: its own command line, its fakeserver.json beside
// it, its log on standard output.

var built string // the program, built once in TestMain

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakeserver-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	name := "demoserver" // the name picks the demo personality
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	built = filepath.Join(dir, name)
	if out, err := exec.Command("go", "build", "-o", built, ".").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building the stand-in: %v\n%s", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// demoRun is one running copy of the stand-in.
type demoRun struct {
	t     testing.TB
	exe   string
	saves string
	extra []string       // arguments after the usual ones, with "{name}" where the port of that name goes
	ports map[string]int // "game", and one per "{name}" in extra
	tries int            // how many times it was started (retry starts it again on new ports)

	beforeLaunch func() // tests: runs once the ports are chosen, before each start

	// The current run's; replaced only by launch, on the test's goroutine.
	cmd   *exec.Cmd
	stdin io.WriteCloser
	port  int // ports["game"]
	added chan struct{}
	done  chan struct{}

	mu     sync.Mutex
	lines  []string
	stderr strings.Builder
	code   int
}

// startDemo copies the program into a folder of its own with config as
// its fakeserver.json ("" for none) and starts it on ports of its own.
// Where extra has "{name}", say "-api", "{api}", a port is chosen for that
// name too, and r.ports[name] says which.
func startDemo(t *testing.T, config string, extra ...string) *demoRun {
	t.Helper()
	r := newDemoRun(t, startDemoDir(t, config), extra...)
	r.launch()
	return r
}

// startDemoDir copies the program into a folder of its own, named so it
// is the demo personality, with config as its fakeserver.json ("" for
// none) one folder up, as install.Fake lays them out.
func startDemoDir(t *testing.T, config string) string {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "bin", filepath.Base(built))
	os.MkdirAll(filepath.Dir(exe), 0o755)
	b, _ := os.ReadFile(built)
	if err := os.WriteFile(exe, b, 0o755); err != nil {
		t.Fatal(err)
	}
	if config != "" {
		os.WriteFile(filepath.Join(dir, configName), []byte(config), 0o644)
	}
	return exe
}

// newDemoRun chooses the ports for running exe; launch starts it.
func newDemoRun(t testing.TB, exe string, extra ...string) *demoRun {
	t.Helper()
	r := &demoRun{t: t, exe: exe, saves: filepath.Join(t.TempDir(), "saves"), extra: extra}
	r.choosePorts()
	t.Cleanup(func() {
		if r.cmd != nil {
			r.cmd.Process.Kill()
			<-r.done
		}
	})
	return r
}

func (r *demoRun) choosePorts() {
	r.t.Helper()
	r.ports = map[string]int{"game": freePort(r.t)}
	for _, a := range r.extra {
		if name, ok := portName(a); ok && r.ports[name] == 0 {
			r.ports[name] = freePort(r.t)
		}
	}
}

// portName reads "{name}".
func portName(arg string) (string, bool) {
	if len(arg) > 2 && strings.HasPrefix(arg, "{") && strings.HasSuffix(arg, "}") {
		return arg[1 : len(arg)-1], true
	}
	return "", false
}

// launch starts the program on the chosen ports, collecting its output.
func (r *demoRun) launch() {
	r.t.Helper()
	r.tries++
	if r.beforeLaunch != nil {
		r.beforeLaunch()
	}
	args := []string{"-port", strconv.Itoa(r.ports["game"]), "-world", "Test World", "-saves", r.saves, "-name", "Test"}
	for _, a := range r.extra {
		if name, ok := portName(a); ok {
			a = strconv.Itoa(r.ports[name])
		}
		args = append(args, a)
	}
	r.mu.Lock()
	r.lines, r.code = nil, 0
	r.stderr.Reset()
	r.mu.Unlock()
	cmd := exec.Command(r.exe, args...)
	cmd.Stderr = &lockedWriter{mu: &r.mu, b: &r.stderr}
	stdin, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		r.t.Fatalf("starting the stand-in: %v", err)
	}
	added, done := make(chan struct{}, 1), make(chan struct{})
	r.cmd, r.stdin, r.port, r.added, r.done = cmd, stdin, r.ports["game"], added, done
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			r.mu.Lock()
			r.lines = append(r.lines, sc.Text())
			r.mu.Unlock()
			select {
			case added <- struct{}{}:
			default:
			}
		}
		err := cmd.Wait()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			r.mu.Lock()
			r.code = ee.ExitCode()
			r.mu.Unlock()
		}
		close(done)
	}()
}

// stolenPortRetries is how many times one stand-in is started again on
// new ports when another program took one of its ports first.
const stolenPortRetries = 3

// inUse finds the system's words for a port another program has (Unix;
// Windows; and a port Windows set aside after it was checked).
var inUse = regexp.MustCompile(`(?i)address already in use|only one usage of each socket address|forbidden by its access permissions`)

// retry starts the stand-in again on new ports when it stopped before it
// was ready because another program had taken one of the ports chosen for
// it ("Error: can't open <what> <port>: ... in use", which the demo only
// says while starting), at most stolenPortRetries times, and says so in
// the test's log. It says whether it did.
//
// Choosing a free port and the stand-in opening it can't be one step, so
// on a busy computer another program can take the port in between. A
// stand-in that fails in any other way, or a port that stays taken, still
// fails the test.
func (r *demoRun) retry() bool {
	r.t.Helper()
	if r.tries > stolenPortRetries {
		return false
	}
	r.mu.Lock()
	lines := slices.Clone(r.lines)
	r.mu.Unlock()
	if slicesContain(lines, "Server ready") {
		return false
	}
	var line string
	for _, l := range lines {
		if strings.Contains(l, "Error: can't open") && inUse.MatchString(l) {
			line = l
		}
	}
	if line == "" {
		return false
	}
	numbers := strings.FieldsFunc(line, func(c rune) bool { return c < '0' || c > '9' })
	var taken []string
	for _, name := range slices.Sorted(maps.Keys(r.ports)) {
		if p := r.ports[name]; slices.Contains(numbers, strconv.Itoa(p)) {
			taken = append(taken, fmt.Sprintf("%s port %d", name, p))
		}
	}
	if len(taken) == 0 {
		return false
	}
	r.t.Logf("another program took the %s before the stand-in could open it (%q); starting it again on new ports (retry %d of %d)",
		strings.Join(taken, " and "), line, r.tries, stolenPortRetries)
	os.RemoveAll(r.saves) // it began making the world; the next start makes it again
	r.choosePorts()
	r.launch()
	return true
}

type lockedWriter struct {
	mu *sync.Mutex
	b  *strings.Builder
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

// waitLine waits for a log line containing each text, in order, after
// the last one found, and returns the last line found. A start that
// failed on a port another program took is tried again (retry), and the
// lines are then looked for in the new run's output.
func (r *demoRun) waitLine(texts ...string) string {
	r.t.Helper()
	for {
		found, missing, exited := r.find(texts)
		if !exited {
			return found
		}
		if !r.retry() {
			r.mu.Lock()
			lines := strings.Join(r.lines, "\n")
			r.mu.Unlock()
			r.t.Fatalf("the stand-in exited (code %d) before %q; it wrote:\n%s\nstderr: %s", r.code, missing, lines, r.stderrText())
		}
	}
}

// find waits for the texts in the current run's output. When the program
// exits first, it says which text it was still waiting for.
func (r *demoRun) find(texts []string) (found, missing string, exited bool) {
	r.t.Helper()
	deadline := time.After(15 * time.Second)
	from := 0
	for _, text := range texts {
		for {
			// Once the program has ended, every line is in: look at
			// them all once more before saying it exited first.
			ended := false
			select {
			case <-r.done:
				ended = true
			default:
			}
			r.mu.Lock()
			i := from
			for ; i < len(r.lines) && !strings.Contains(r.lines[i], text); i++ {
			}
			ok := i < len(r.lines)
			if ok {
				found, from = r.lines[i], i+1
			}
			lines := strings.Join(r.lines, "\n")
			r.mu.Unlock()
			if ok {
				break
			}
			if ended {
				return "", text, true
			}
			select {
			case <-r.added:
			case <-r.done:
			case <-deadline:
				r.t.Fatalf("no line with %q; the stand-in wrote:\n%s\nstderr: %s", text, lines, r.stderrText())
			}
		}
	}
	return found, "", false
}

func slicesContain(lines []string, text string) bool {
	for _, l := range lines {
		if strings.Contains(l, text) {
			return true
		}
	}
	return false
}

func (r *demoRun) stderrText() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stderr.String()
}

func (r *demoRun) send(line string) {
	r.t.Helper()
	if _, err := io.WriteString(r.stdin, line+"\n"); err != nil {
		r.t.Fatal(err)
	}
}

// exit waits for the program to end and returns its exit code.
func (r *demoRun) exit() int {
	r.t.Helper()
	select {
	case <-r.done:
		return r.code
	case <-time.After(15 * time.Second):
		r.t.Fatal("the stand-in didn't exit")
		return 0
	}
}

func (r *demoRun) world() demoWorld {
	r.t.Helper()
	var w demoWorld
	b, err := os.ReadFile(filepath.Join(r.saves, "Test World", "world.json"))
	if err != nil || json.Unmarshal(b, &w) != nil {
		r.t.Fatalf("world.json: %s, %v", b, err)
	}
	return w
}

// TestDemo: a whole run with a script, the console and a stop.
func TestDemo(t *testing.T) {
	r := startDemo(t, `{"script":"join:Alex@0.2,death:Alex@0.3,deny:Bob@0.4,count@0.5,dance:Alex@0.6,leave:Alex@0.7,save@0.8","speed":60,"version":"9.9"}`)
	// The port is checked after the wait, not put in the text waited for:
	// if another program took it first, waitLine starts the stand-in
	// again on a new port, and r.port is only that new port once it
	// returns.
	ready := r.waitLine("Demo Server version 9.9", `Creating world "Test World"`, "Server ready: players can join \"Test\" on port ")
	if want := "on port " + strconv.Itoa(r.port); !strings.HasSuffix(ready, want) {
		t.Errorf("the ready line %q doesn't end with %q", ready, want)
	}
	if !portTaken("tcp", r.port) || !portTaken("udp", r.port) {
		t.Error("the game port isn't in use while the stand-in runs")
	}
	r.waitLine("Player joined: Alex", "Player died: Alex", "Turned away Bob: wrong password", "Players online: 1 (Alex)", "Player left: Alex", "World saved")
	if line := r.waitLine("Player joined: Alex"); !strings.HasPrefix(line, "[") || line[20:22] != "] " {
		t.Errorf("a line without the game's timestamp: %q", line)
	}
	r.send("list")
	r.waitLine("Players online: 0")
	r.send("frobnicate")
	r.waitLine("Unknown command: frobnicate")
	saves := r.world().Saves
	r.send("stop")
	r.waitLine("Stopping the server", "World saved", "Server stopped")
	if code := r.exit(); code != 0 {
		t.Errorf("exit code %d", code)
	}
	if w := r.world(); w.Saves != saves+1 || w.Name != "Test World" || w.Seed == "" {
		t.Errorf("after the stop, world.json = %+v (was saved %d times)", w, saves)
	}
	if !strings.Contains(r.stderrText(), `no script step "dance"`) {
		t.Errorf("an unknown step wasn't reported: stderr %q", r.stderrText())
	}
}

// TestDemoCtrlC: Ctrl+C (SIGINT) saves and stops. Windows sends Ctrl+C
// through the game's console, which only platform.Interrupt can do; the
// gametest tests check it there.
func TestDemoCtrlC(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Ctrl+C on Windows is checked by internal/game/gametest through platform.Interrupt")
	}
	r := startDemo(t, `{"speed":60}`)
	r.waitLine("Server ready")
	r.cmd.Process.Signal(os.Interrupt)
	r.waitLine("Ctrl+C received: stopping", "World saved", "Server stopped")
	if code := r.exit(); code != 0 {
		t.Errorf("exit code %d", code)
	}
	if r.world().Saves != 1 {
		t.Error("Ctrl+C didn't save")
	}
}

// TestDemoNoConsole: with its console ended (the app that held it gone),
// the server carries on, as real servers do.
func TestDemoNoConsole(t *testing.T) {
	r := startDemo(t, `{"speed":60}`)
	r.waitLine("Server ready")
	r.stdin.Close()
	select {
	case <-r.done:
		t.Fatalf("the stand-in exited (code %d) when its console ended", r.code)
	case <-time.After(time.Second):
	}
}

// TestDemoWorldKept: a second run loads the world the first one saved.
func TestDemoWorldKept(t *testing.T) {
	r := startDemo(t, `{"speed":60}`, "-seed", "12345", "-difficulty", "hard")
	r.waitLine("Creating world \"Test World\" (seed 12345, difficulty hard)", "Server ready")
	r.send("stop")
	r.exit()

	again := exec.Command(r.cmd.Path, r.cmd.Args[1:]...)
	out, _ := again.StdoutPipe()
	again.Start()
	sc := bufio.NewScanner(out)
	loaded := false
	for sc.Scan() {
		if strings.Contains(sc.Text(), `Loading world "Test World"`) {
			loaded = true
		}
		if strings.Contains(sc.Text(), "Server ready") {
			break
		}
	}
	again.Process.Kill()
	again.Wait()
	if !loaded {
		t.Error("the second run didn't load the saved world")
	}
	if w := r.world(); w.Seed != "12345" || w.Difficulty != "hard" {
		t.Errorf("world.json = %+v", w)
	}
}

// TestDemoPortInUse: like a real server, it stops with an error when
// another program has its port.
func TestDemoPortInUse(t *testing.T) {
	var l net.Listener
	port := openFree(t, func(port int) (err error) {
		l, err = net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
		return err
	})
	defer l.Close()
	out, err := exec.Command(built, "-port", strconv.Itoa(port), "-saves", t.TempDir()).CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 1 {
		t.Errorf("exit: %v, want code 1", err)
	}
	if !strings.Contains(string(out), fmt.Sprintf("Error: can't open port %d", port)) {
		t.Errorf("output:\n%s", out)
	}
}

// TestDemoCrashAndNoReady: fakeserver.json's crashAfter and noReady.
func TestDemoCrashAndNoReady(t *testing.T) {
	r := startDemo(t, `{"speed":60,"crashAfter":1,"noReady":true,"script":"join:Alex@0.1"}`)
	r.waitLine("Player joined: Alex", "Fatal error: the demo server crashed")
	if code := r.exit(); code != 3 {
		t.Errorf("exit code %d, want 3", code)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if slicesContain(r.lines, "Server ready") {
		t.Error("noReady still said it was ready")
	}
}

// TestDemoAPI: the HTTP JSON API with its password, and stopping through it.
func TestDemoAPI(t *testing.T) {
	r := startDemo(t, `{"speed":60,"script":"join:Alex@0.1"}`, "-api", "{api}", "-apipassword", "s3cret")
	r.waitLine("Player joined: Alex")
	call := func(method, path, pw string) (int, map[string]any) {
		req, _ := http.NewRequest(method, "http://127.0.0.1:"+strconv.Itoa(r.ports["api"])+path, nil)
		req.SetBasicAuth("admin", pw)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body map[string]any
		json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body
	}
	if code, _ := call("GET", "/api/status", "wrong"); code != http.StatusUnauthorized {
		t.Errorf("a wrong password got %d", code)
	}
	code, body := call("GET", "/api/status", "s3cret")
	if code != http.StatusOK || body["online"] != true || body["count"] != 1.0 || fmt.Sprint(body["players"]) != "[Alex]" || body["version"] != "1.4.2" {
		t.Errorf("status: %d %v", code, body)
	}
	if code, body := call("POST", "/api/stop", "s3cret"); code != http.StatusOK || body["stopping"] != true {
		t.Errorf("stop: %d %v", code, body)
	}
	r.waitLine("Stopping the server", "Server stopped")
	if code := r.exit(); code != 0 {
		t.Errorf("exit code %d", code)
	}
}

// TestDemoRCONAndA2S: the responders built on stream RC's test servers.
// Until internal/rcon and internal/a2s are written they only say
// ErrUnsupported, and this skips; once they land it runs with no change.
func TestDemoRCONAndA2S(t *testing.T) {
	rconReady, a2sReady := rconWritten(), a2sWritten()
	if !rconReady && !a2sReady {
		t.Skip("internal/rcon and internal/a2s aren't written yet (stream RC)")
	}
	var extra []string
	if rconReady {
		extra = append(extra, "-rcon", "{rcon}", "-rconpassword", "pw")
	}
	if a2sReady {
		extra = append(extra, "-query", "{query}")
	}
	r := startDemo(t, `{"speed":60,"script":"join:Alex@0.1"}`, append(extra, "-maxplayers", "5")...)
	r.waitLine("Player joined: Alex")
	rp, qp := r.ports["rcon"], r.ports["query"]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if rconReady {
		if out, err := rcon.Exec(ctx, "127.0.0.1:"+strconv.Itoa(rp), "pw", "list"); err != nil || out != "There are 1 of a max of 5 players online: Alex" {
			t.Errorf("rcon list = %q, %v", out, err)
		}
		if _, err := rcon.Exec(ctx, "127.0.0.1:"+strconv.Itoa(rp), "wrong", "list"); !errors.Is(err, rcon.ErrAuth) {
			t.Errorf("a wrong RCON password gave %v", err)
		}
	} else {
		t.Log("internal/rcon isn't written yet: RCON not checked")
	}
	if a2sReady {
		if in, err := a2s.Query(ctx, "127.0.0.1:"+strconv.Itoa(qp)); err != nil || in.Name != "Test" || in.Players != 1 || in.MaxPlayers != 5 || in.Map != "Test World" {
			t.Errorf("a2s = %+v, %v", in, err)
		}
	} else {
		t.Log("internal/a2s isn't written yet: A2S not checked")
	}
	if rconReady {
		rcon.Exec(ctx, "127.0.0.1:"+strconv.Itoa(rp), "pw", "stop")
		r.waitLine("Server stopped")
	}
}

// rconWritten and a2sWritten say whether stream RC has written
// internal/rcon's test server and internal/a2s's responder yet (until then
// they only say ErrUnsupported).
func rconWritten() bool {
	s, err := rcon.NewTestServer("127.0.0.1:0", "x", func(string) string { return "" })
	if err != nil {
		return false
	}
	s.Close()
	return true
}

func a2sWritten() bool {
	r, err := a2s.NewTestResponder("127.0.0.1:0", false, func() a2s.Info { return a2s.Info{} })
	if err != nil {
		return false
	}
	r.Close()
	return true
}

// TestDemoProbeDuringStop: a probe that arrives while the server saves on
// its way out (the app probes every 30 seconds, gametest every 2) doesn't
// keep it from exiting. The probe waits for the server's goroutine, which
// is busy saving and then exits; Exit closes the responders, and their
// Close waits for the probe in progress, so an Exit that waited for that
// would hang for good. Speed 1, so the save takes half a second and the
// probe surely lands in it. Each part skips until stream RC lands.
func TestDemoProbeDuringStop(t *testing.T) {
	// stopped checks the stand-in finished its stop and exited cleanly.
	stopped := func(t *testing.T, r *demoRun) {
		t.Helper()
		select {
		case <-r.done:
		case <-time.After(10 * time.Second):
			r.mu.Lock()
			lines := strings.Join(r.lines, "\n")
			r.mu.Unlock()
			t.Fatalf("the stand-in hung on its way out; it wrote:\n%s", lines)
		}
		if r.code != 0 {
			t.Errorf("exit code %d, want 0", r.code)
		}
		r.waitLine("World saved", "Server stopped")
	}

	t.Run("RCON during an RCON stop", func(t *testing.T) {
		if !rconWritten() {
			t.Skip("internal/rcon isn't written yet (stream RC)")
		}
		r := startDemo(t, `{"speed":1}`, "-rcon", "{rcon}", "-rconpassword", "pw")
		r.waitLine("Server ready")
		addr := "127.0.0.1:" + strconv.Itoa(r.ports["rcon"])
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if out, err := rcon.Exec(ctx, addr, "pw", "stop"); err != nil || out != "Stopping the server" {
			t.Fatalf("rcon stop = %q, %v", out, err)
		}
		r.waitLine("Saving the world...")
		probed := make(chan struct{})
		go func() {
			defer close(probed)
			pctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			rcon.Exec(pctx, addr, "pw", "list") // the server going away is a fine answer
		}()
		stopped(t, r)
		<-probed
	})

	t.Run("A2S during a console stop", func(t *testing.T) {
		if !a2sWritten() {
			t.Skip("internal/a2s isn't written yet (stream RC)")
		}
		r := startDemo(t, `{"speed":1}`, "-query", "{query}")
		r.waitLine("Server ready")
		qp := r.ports["query"]
		r.send("stop")
		r.waitLine("Saving the world...")
		probed := make(chan struct{})
		go func() {
			defer close(probed)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			a2s.Query(ctx, "127.0.0.1:"+strconv.Itoa(qp)) // no answer is fine
		}()
		stopped(t, r)
		<-probed
	})
}

// TestUnknownCommandLine: a program name no personality knows, without a
// game in fakeserver.json, stops with exit code 2 and says why.
func TestUnknownCommandLine(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "someserver"+filepath.Ext(built))
	b, _ := os.ReadFile(built)
	os.WriteFile(exe, b, 0o755)
	out, err := exec.Command(exe, "-port", "1").CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 2 || !strings.Contains(string(out), "no game's stand-in recognises") {
		t.Errorf("%v: %s", err, out)
	}
	// Naming the game in fakeserver.json makes any program name work.
	os.WriteFile(filepath.Join(dir, configName), []byte(`{"game":"demo","speed":60}`), 0o644)
	r := newDemoRun(t, exe)
	r.launch()
	r.waitLine("Server ready")
}
