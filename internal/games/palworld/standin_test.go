package palworld

import (
	"context"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/game/gametest"
	"valheim-easy-server/internal/platform"
)

// The stand-in turns down a shutdown without the waittime the docs make
// required, as the real API answers a bad request, and keeps running.
func TestStandInShutdownNeedsWaittime(t *testing.T) {
	s := gametest.Start(t, Game{}, gametest.Options{})
	s.WaitEvent("online", "", 30*time.Second)
	c, err := restFor(s.Record())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.call(context.Background(), "POST", "shutdown", map[string]string{"message": "bye"}); err == nil || !strings.Contains(err.Error(), "400") {
		t.Errorf("a shutdown without waittime: %v", err)
	}
	if _, err := c.info(context.Background()); err != nil {
		t.Errorf("the server stopped after a bad request: %v", err)
	}
	s.Stop()
}

// Ctrl+C ends the stand-in without saving (until the real server's
// behaviour is confirmed, FACTS.md): the stop chain's REST step is what
// saves.
func TestStandInCtrlCDoesNotSave(t *testing.T) {
	s := gametest.Start(t, Game{}, gametest.Options{})
	s.WaitEvent("online", "", 30*time.Second)
	srv := s.Server().(*server)
	id := s.Env().Store.Get().State[statePlanned]
	level := filepath.Join(srv.savesDir(), id, levelFile)
	waitFile(t, level)
	proc, err := platform.OpenProc(s.Record().PID)
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Close()
	if runtime.GOOS == "windows" {
		// Windows sends Ctrl+C through the game's console, which this test
		// would get too; take it, as the app's own signal.Notify does.
		signal.Notify(make(chan os.Signal, 1), os.Interrupt)
	}
	if err := proc.Interrupt(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !proc.Exited() {
		if time.Now().After(deadline) {
			t.Fatal("Ctrl+C didn't end the stand-in")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// The saving way out logs "REST API stopped"; Ctrl+C mustn't take it.
	if logHas(t, s.Plan().LogPath, "REST API stopped") {
		t.Error("Ctrl+C went through the saving shutdown")
	}
}

// The stand-in crashes the way fakeserver.json's crashAfter asks: the
// Monitor doesn't take it for a stop anyone asked for, and the exit hint
// says it crashed.
func TestStandInCrash(t *testing.T) {
	s := testServer(t)
	exe := s.Program()
	b, err := os.ReadFile(gametest.BuildFakeserver(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, b, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(exe), "fakeserver.json"), []byte(`{"speed":60,"crashAfter":3}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sv := defaultSaved(s, "Island")
	sv.Ports = map[string]int{"game": freePort(t, "udp"), "rest": freePort(t, "tcp")}
	plan, err := s.Plan(context.Background(), game.PlanRequest{World: "Island", New: true, Saved: sv})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, plan.Args...)
	cmd.Dir = plan.Dir
	out, _ := cmd.CombinedOutput()
	m := s.Watch(game.Record{}, nil)
	var tail []string
	for _, l := range strings.Split(string(out), "\n") {
		m.Line(l, time.Now())
		if strings.TrimSpace(l) != "" {
			tail = append(tail, l)
		}
	}
	if st := m.State(); st.Stopping || !st.Ready {
		t.Errorf("after the crash: %+v\n%s", st, out)
	}
	if hint := s.ExitHint(tail); !strings.Contains(hint, "crashed") {
		t.Errorf("ExitHint = %q\n%s", hint, out)
	}
}

func logHas(t *testing.T, path, text string) bool {
	t.Helper()
	b, _ := os.ReadFile(path)
	return strings.Contains(string(b), text)
}

// freePort finds a port nothing uses now, below the systems' own ranges
// and outside gametest's and cmd/fakeserver's (32000-32767 is free).
func freePort(t *testing.T, proto string) int {
	t.Helper()
	for p := 32000 + os.Getpid()%500; p < 32767; p++ {
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(p))
		if proto == "udp" {
			if c, err := net.ListenPacket("udp", addr); err == nil {
				c.Close()
				return p
			}
		} else if l, err := net.Listen("tcp", addr); err == nil {
			l.Close()
			return p
		}
	}
	t.Fatal("no free port")
	return 0
}
