//go:build !windows

package platform

import (
	"os"
	"os/signal"
	"path/filepath"
	"testing"
	"time"
)

// Interrupt (SIGINT, as Ctrl+C) reaches a game started with Start, with
// its console pipe, from an app that handles SIGINT itself, as main.go
// does. A game that handles it saves and stops; one that leaves it to the
// system stops too, so Start doesn't hand games an ignored SIGINT.
func TestStartInterruptReachesGame(t *testing.T) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt) // as the app does
	defer signal.Stop(c)

	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	p, err := Start(StartOptions{Exe: selfExe(t), Args: helperArgs(marker), Env: helperEnviron("waiter"), Dir: dir,
		Stdin: r, Inherit: []*os.File{w}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	defer p.Kill()
	r.Close()
	waitFor(t, 30*time.Second, "the game to start", func() bool {
		if p.Exited() {
			t.Fatal("the game stopped early")
		}
		_, err := os.Stat(marker + ".ready")
		return err == nil
	})
	if err := p.Interrupt(); err != nil {
		t.Fatal(err)
	}
	waitExit(t, p, 0)
	if b, _ := os.ReadFile(marker); string(b) != "got ctrl+c" {
		t.Fatalf("marker = %q", b)
	}

	sleeper, err := Start(StartOptions{Exe: "/bin/sleep", Args: []string{"30"}, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer sleeper.Kill()
	time.Sleep(200 * time.Millisecond)
	if err := sleeper.Interrupt(); err != nil {
		t.Fatal(err)
	}
	waitExit(t, sleeper, -1) // ended by the signal
}

func waitExit(t *testing.T, p *Proc, want int) {
	t.Helper()
	done := make(chan int, 1)
	go func() { code, _ := p.Wait(); done <- code }()
	select {
	case code := <-done:
		if code != want {
			t.Fatalf("exit code %d, want %d", code, want)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("still running after Ctrl+C")
	}
}
