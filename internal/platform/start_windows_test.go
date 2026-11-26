//go:build windows

package platform

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"testing"
	"time"
)

func init() {
	extraHelpers["guiapp"] = guiAppHelper
}

// guiAppHelper plays the app, a GUI program with no console of its own:
// it starts the game (args[0], the waiter) with Start and its console
// pipe, sends it Ctrl+C with Interrupt, and writes what happened to
// args[2] ("ok", or the problem).
func guiAppHelper(args []string) int {
	exe, marker, result := args[0], args[1], args[2]
	fail := func(format string, a ...any) int {
		writeAtomic(result, fmt.Sprintf(format, a...))
		return 1
	}
	signal.Notify(make(chan os.Signal, 1), os.Interrupt) // as the app does
	r, w, err := os.Pipe()
	if err != nil {
		return fail("pipe: %v", err)
	}
	defer w.Close()
	p, err := Start(StartOptions{Exe: exe, Args: helperArgs(marker), Env: helperEnviron("waiter"),
		Dir: filepath.Dir(marker), Stdin: r, Inherit: []*os.File{w}})
	if err != nil {
		return fail("start: %v", err)
	}
	defer p.Close()
	r.Close()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(marker + ".ready"); err == nil {
			break
		}
		if p.Exited() || time.Now().After(deadline) {
			p.Kill()
			return fail("the game never got going")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := p.Interrupt(); err != nil {
		p.Kill()
		return fail("Interrupt: %v", err)
	}
	done := make(chan int, 1)
	go func() { code, _ := p.Wait(); done <- code }()
	select {
	case code := <-done:
		if code != 0 {
			return fail("the game's exit code was %d", code)
		}
	case <-time.After(20 * time.Second):
		p.Kill()
		return fail("the game didn't stop after Ctrl+C")
	}
	writeAtomic(result, "ok")
	return 0
}

// Ctrl+C reaches a game started with Start (with its console pipe) when
// it is sent from a GUI program, as the app is (-H=windowsgui): the app
// has no console, and each game gets a hidden one of its own. The test
// program is built again as a GUI program to play the app; the game is
// the test program as it is, a console program, as game servers are.
func TestStartCtrlCFromGUIApp(t *testing.T) {
	dir := t.TempDir()
	gui := filepath.Join(dir, "guiapp.exe")
	build := exec.Command("go", "test", "-c", "-ldflags=-H=windowsgui", "-o", gui, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the GUI helper: %v\n%s", err, out)
	}
	marker := filepath.Join(dir, "marker")
	result := filepath.Join(dir, "result")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, gui, helperArgs(selfExe(t), marker, result)...)
	cmd.Env = helperEnviron("guiapp")
	out, err := cmd.CombinedOutput()
	got, _ := os.ReadFile(result)
	if err != nil || string(got) != "ok" {
		t.Fatalf("the GUI app: %v, %q\n%s", err, got, out)
	}
	if b, _ := os.ReadFile(marker); string(b) != "got ctrl+c" {
		t.Fatalf("marker = %q", b)
	}
}
