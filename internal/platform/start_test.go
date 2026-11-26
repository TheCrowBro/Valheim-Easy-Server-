package platform

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The tests of Start need programs to start: a game reading its console,
// an app holding the console's other end, a game waiting for Ctrl+C. This
// test program plays each of them. Started with helperEnv set to a mode,
// TestStartHelper does that job and exits, instead of testing.
const helperEnv = "EGS_PLATFORM_HELPER"

// helperArgs are the arguments that make the test program run as helper,
// followed by the helper's own.
func helperArgs(args ...string) []string {
	return append([]string{"-test.run=^TestStartHelper$", "--"}, args...)
}

// helperEnviron is this environment with the helper's mode set.
func helperEnviron(mode string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, helperEnv+"=") {
			env = append(env, kv)
		}
	}
	return append(env, helperEnv+"="+mode)
}

func selfExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestStartHelper(t *testing.T) {
	mode := os.Getenv(helperEnv)
	if mode == "" {
		return // a normal test run
	}
	var args []string
	for i, a := range os.Args {
		if a == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	// However a test ends, its helpers don't run on for ever.
	time.AfterFunc(2*time.Minute, func() { os.Exit(9) })
	os.Exit(runHelper(mode, args))
}

func runHelper(mode string, args []string) int {
	switch mode {
	case "reader":
		// A game reading console lines: it answers each one, and says
		// when its console has ended.
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			fmt.Println("got: " + sc.Text())
		}
		fmt.Println("EOF")
		return 3

	case "app":
		// The app: it starts the reader with a console pipe, keeps the
		// pipe's write end, types one line, says the reader's PID
		// (args[1]), and then waits to be killed.
		out, err := os.OpenFile(args[0], os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			fmt.Println(err)
			return 1
		}
		r, w, err := os.Pipe()
		if err != nil {
			fmt.Println(err)
			return 1
		}
		exe, _ := os.Executable()
		p, err := Start(StartOptions{Exe: exe, Args: helperArgs(), Env: helperEnviron("reader"),
			Stdin: r, Stdout: out, Stderr: out, Inherit: []*os.File{w}})
		if err != nil {
			fmt.Println("start:", err)
			return 1
		}
		r.Close()
		out.Close()
		if _, err := w.WriteString("hello\n"); err != nil {
			fmt.Println("write:", err)
			return 1
		}
		if err := writeAtomic(args[1], strconv.Itoa(p.PID())); err != nil {
			fmt.Println(err)
			return 1
		}
		time.Sleep(3 * time.Minute) // until killed (or the two minutes are up)
		runtime.KeepAlive(w)        // held open all along, as the app does
		return 0

	case "waiter":
		// A game that saves and stops on Ctrl+C (SIGINT).
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt)
		writeAtomic(args[0]+".ready", "")
		select {
		case <-c:
			writeAtomic(args[0], "got ctrl+c")
			return 0
		case <-time.After(60 * time.Second):
			return 2
		}

	case "listen":
		// A game holding a TCP and a UDP port; it says which (args[0]).
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fmt.Println(err)
			return 1
		}
		u, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			fmt.Println(err)
			return 1
		}
		writeAtomic(args[0], fmt.Sprintf("%d %d", l.Addr().(*net.TCPAddr).Port, u.LocalAddr().(*net.UDPAddr).Port))
		time.Sleep(3 * time.Minute) // until killed (or the two minutes are up)
		runtime.KeepAlive(l)        // else the collector could close them
		runtime.KeepAlive(u)
		return 0
	}
	if extra, ok := extraHelpers[mode]; ok {
		return extra(args)
	}
	fmt.Println("no such helper:", mode)
	return 1
}

// extraHelpers are helpers only one system has.
var extraHelpers = map[string]func(args []string) int{}

// writeAtomic writes a file whole, so a reader polling for it never sees
// it half written.
func writeAtomic(path, text string) error {
	if err := os.WriteFile(path+".tmp", []byte(text), 0o644); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// waitFor polls until ok says yes, failing the test after within.
func waitFor(t *testing.T, within time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func fileHas(path, text string) bool {
	b, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(b), text)
}

// killPID ends a process the test started indirectly, if it still runs.
func killPID(pid int) {
	if p, err := OpenProc(pid); err == nil {
		p.Kill()
		p.Close()
	}
}

// The control case: a game started with only a console pipe sees its
// console end (end-of-file) when the app closes its end. This is what the
// next test makes sure never happens to a game started with Inherit.
func TestStartConsolePipe(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "out.txt")
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	p, err := Start(StartOptions{Exe: selfExe(t), Args: helperArgs(), Env: helperEnviron("reader"), Dir: dir,
		Stdin: r, Stdout: out, Stderr: out})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	defer p.Kill()
	r.Close()
	if p.PID() <= 0 || p.StartTime().IsZero() || time.Since(p.StartTime()) > time.Minute {
		t.Errorf("pid %d, started %v", p.PID(), p.StartTime())
	}
	if _, err := w.WriteString("one\n"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the game to read a console line", func() bool { return fileHas(outPath, "got: one") })
	w.Close()
	waitFor(t, 30*time.Second, "the game to see its console end", func() bool { return fileHas(outPath, "EOF") })
	done := make(chan int, 1)
	go func() { code, _ := p.Wait(); done <- code }()
	select {
	case code := <-done:
		if code != 3 {
			t.Errorf("exit code %d, want 3", code)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the game didn't stop when its console ended")
	}

	if _, err := Start(StartOptions{Exe: filepath.Join(dir, "no such program")}); err == nil {
		t.Error("started a program that doesn't exist")
	}
}

// The game keeps the console pipe's write end too (StartOptions.Inherit),
// so when the app holding the other end is killed, as an update or a crash
// would, the game goes on running and never sees end-of-file.
func TestStartConsoleOutlivesApp(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "game.txt")
	pidPath := filepath.Join(dir, "game.pid")
	appLog, err := os.Create(filepath.Join(dir, "app.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer appLog.Close()
	app, err := Start(StartOptions{Exe: selfExe(t), Args: helperArgs(outPath, pidPath), Env: helperEnviron("app"), Dir: dir,
		Stdout: appLog, Stderr: appLog})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	defer app.Kill()
	appSays := func() string { b, _ := os.ReadFile(appLog.Name()); return string(b) }

	waitFor(t, 30*time.Second, "the app to start the game", func() bool {
		if app.Exited() {
			t.Fatalf("the app stopped: %s", appSays())
		}
		_, err := os.Stat(pidPath)
		return err == nil
	})
	b, _ := os.ReadFile(pidPath)
	gamePID, err := strconv.Atoi(string(b))
	if err != nil {
		t.Fatalf("game pid %q", b)
	}
	t.Cleanup(func() { killPID(gamePID) })
	waitFor(t, 30*time.Second, "the game to read the app's line", func() bool { return fileHas(outPath, "got: hello") })

	if err := app.Kill(); err != nil {
		t.Fatal(err)
	}
	app.Wait()

	game, err := OpenProc(gamePID)
	if err != nil {
		t.Fatalf("the game stopped with the app: %v", err)
	}
	defer game.Close()
	time.Sleep(3 * time.Second)
	if fileHas(outPath, "EOF") {
		t.Fatal("the game saw its console end when the app was killed")
	}
	if game.Exited() {
		b, _ := os.ReadFile(outPath)
		t.Fatalf("the game stopped after the app was killed:\n%s", b)
	}
	game.Kill()
}

// PortInUse names the program holding a port, here another process's TCP
// and UDP ports, and ProcName says what that program is.
func TestPortInUseFindsOtherProgram(t *testing.T) {
	dir := t.TempDir()
	portsPath := filepath.Join(dir, "ports.txt")
	p, err := Start(StartOptions{Exe: selfExe(t), Args: helperArgs(portsPath), Env: helperEnviron("listen"), Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	defer p.Kill()
	waitFor(t, 30*time.Second, "the ports to open", func() bool {
		if p.Exited() {
			t.Fatal("the program holding the ports stopped")
		}
		_, err := os.Stat(portsPath)
		return err == nil
	})
	b, _ := os.ReadFile(portsPath)
	var tcp, udp int
	if _, err := fmt.Sscanf(string(b), "%d %d", &tcp, &udp); err != nil {
		t.Fatalf("ports %q", b)
	}
	for _, c := range []struct {
		proto string
		port  int
	}{{"tcp", tcp}, {"udp", udp}} {
		inUse, pid := PortInUse(c.proto, c.port)
		if !inUse || pid != p.PID() {
			t.Errorf("PortInUse(%q, %d) = %v, %d; want true, %d", c.proto, c.port, inUse, pid, p.PID())
		}
	}
	if name, want := ProcName(p.PID()), filepath.Base(selfExe(t)); !strings.EqualFold(name, want) {
		t.Errorf("ProcName = %q, want %q", name, want)
	}
	p.Kill()
	p.Wait()
	waitFor(t, 10*time.Second, "the ports to be free", func() bool {
		a, _ := PortInUse("tcp", tcp)
		b, _ := PortInUse("udp", udp)
		return !a && !b
	})
}
