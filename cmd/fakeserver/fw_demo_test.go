package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	t     *testing.T
	cmd   *exec.Cmd
	stdin io.WriteCloser
	saves string
	port  int

	mu     sync.Mutex
	lines  []string
	stderr strings.Builder
	added  chan struct{}
	done   chan struct{}
	code   int
}

// startDemo copies the program into a folder of its own with config as
// its fakeserver.json ("" for none) and starts it on a free port.
func startDemo(t *testing.T, config string, extra ...string) *demoRun {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "bin", filepath.Base(built))
	os.MkdirAll(filepath.Dir(exe), 0o755)
	b, _ := os.ReadFile(built)
	if err := os.WriteFile(exe, b, 0o755); err != nil {
		t.Fatal(err)
	}
	if config != "" {
		os.WriteFile(filepath.Join(dir, configName), []byte(config), 0o644) // one folder up
	}
	r := &demoRun{t: t, saves: filepath.Join(dir, "saves"), port: freePort(t), added: make(chan struct{}, 1), done: make(chan struct{})}
	args := append([]string{"-port", strconv.Itoa(r.port), "-world", "Test World", "-saves", r.saves, "-name", "Test"}, extra...)
	r.cmd = exec.Command(exe, args...)
	r.cmd.Stderr = &lockedWriter{mu: &r.mu, b: &r.stderr}
	r.stdin, _ = r.cmd.StdinPipe()
	out, _ := r.cmd.StdoutPipe()
	if err := r.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			r.mu.Lock()
			r.lines = append(r.lines, sc.Text())
			r.mu.Unlock()
			select {
			case r.added <- struct{}{}:
			default:
			}
		}
		err := r.cmd.Wait()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			r.code = ee.ExitCode()
		}
		close(r.done)
	}()
	t.Cleanup(func() {
		r.cmd.Process.Kill()
		<-r.done
	})
	return r
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
// the last one found, and returns the last line found.
func (r *demoRun) waitLine(texts ...string) string {
	r.t.Helper()
	deadline := time.After(15 * time.Second)
	from := 0
	var found string
	for _, text := range texts {
		for {
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
			select {
			case <-r.done: // every line is in by now
				r.t.Fatalf("the stand-in exited (code %d) before %q; it wrote:\n%s\nstderr: %s", r.code, text, lines, r.stderrText())
			default:
			}
			select {
			case <-r.added:
			case <-r.done:
			case <-deadline:
				r.t.Fatalf("no line with %q; the stand-in wrote:\n%s\nstderr: %s", text, lines, r.stderrText())
			}
		}
	}
	return found
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

func portTaken(proto string, port int) bool {
	addr := "127.0.0.1:" + strconv.Itoa(port)
	if proto == "udp" {
		c, err := net.ListenPacket("udp", addr)
		if err == nil {
			c.Close()
		}
		return err != nil
	}
	l, err := net.Listen("tcp", addr)
	if err == nil {
		l.Close()
	}
	return err != nil
}

// TestDemo: a whole run with a script, the console and a stop.
func TestDemo(t *testing.T) {
	r := startDemo(t, `{"script":"join:Alex@0.2,death:Alex@0.3,deny:Bob@0.4,count@0.5,dance:Alex@0.6,leave:Alex@0.7,save@0.8","speed":60,"version":"9.9"}`)
	r.waitLine("Demo Server version 9.9", `Creating world "Test World"`, "Server ready: players can join \"Test\" on port "+strconv.Itoa(r.port))
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
	port := freePort(t)
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
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
	api := freePort(t)
	r := startDemo(t, `{"speed":60,"script":"join:Alex@0.1"}`, "-api", strconv.Itoa(api), "-apipassword", "s3cret")
	r.waitLine("Player joined: Alex")
	call := func(method, path, pw string) (int, map[string]any) {
		req, _ := http.NewRequest(method, "http://127.0.0.1:"+strconv.Itoa(api)+path, nil)
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
	rp, qp := freePort(t), freePort(t)
	if rconReady {
		extra = append(extra, "-rcon", strconv.Itoa(rp), "-rconpassword", "pw")
	}
	if a2sReady {
		extra = append(extra, "-query", strconv.Itoa(qp))
	}
	r := startDemo(t, `{"speed":60,"script":"join:Alex@0.1"}`, append(extra, "-maxplayers", "5")...)
	r.waitLine("Player joined: Alex")
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
		rp := freePort(t)
		addr := "127.0.0.1:" + strconv.Itoa(rp)
		r := startDemo(t, `{"speed":1}`, "-rcon", strconv.Itoa(rp), "-rconpassword", "pw")
		r.waitLine("Server ready")
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
		qp := freePort(t)
		r := startDemo(t, `{"speed":1}`, "-query", strconv.Itoa(qp))
		r.waitLine("Server ready")
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
	cmd := exec.Command(exe, "-port", strconv.Itoa(freePort(t)), "-saves", dir)
	stdout, _ := cmd.StdoutPipe()
	cmd.Start()
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		if strings.Contains(sc.Text(), "Server ready") {
			return
		}
	}
	t.Error("with \"game\": \"demo\" it never became ready")
}
