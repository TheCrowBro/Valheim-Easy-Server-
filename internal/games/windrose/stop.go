package windrose

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/platform"
)

// stopWait is how long a stop waits for the server to save and exit. Real
// servers saved and closed their log in under a second after Ctrl+C
// (FACTS.md, "Stopping"); while a world is still loading the server only
// acts on it once the load is done, which can take minutes on a slow
// computer.
const stopWait = 4 * time.Minute

// Stop asks the server to save the world and quit, built from the run
// record alone so it works after a takeover too (spec 2.7).
//
// Ctrl+C is how the server is stopped: Unreal Engine answers it with
// "*** INTERRUPTED *** : CTRL-C TO FORCE QUIT", writes the world's exit
// backup and quits (FACTS.md). It is sent once and never again: a second
// Ctrl+C is Unreal's "force quit", which would cut the save short.
//
//   - Windows: the server is a windowed ("GUI") program, so it has no
//     console of its own from Windows; with -log it opens one itself (its
//     log window, hidden here because the app starts it hidden). Ctrl+C is
//     sent to that console. If that can't be done (a server someone
//     started without -log has none), its window is asked to close
//     instead, the publisher's own way to stop it ("To shut down, just
//     close the window"), which saves less: Windows gives a closing
//     program only a few seconds.
//   - The stand-in (fake mode) is a console program on every system, and
//     elsewhere Ctrl+C is SIGINT: the plain "interrupt" step.
func (s *server) Stop(rec game.Record) []game.StopStep {
	if runtime.GOOS == "windows" && s.env.Fake == nil {
		return []game.StopStep{{Kind: "call", Call: ctrlC(rec), Wait: stopWait}}
	}
	return []game.StopStep{{Kind: "interrupt", Wait: stopWait}}
}

// ctrlC sends the running server Ctrl+C through its own console, or asks
// its window to close when it has no console.
func ctrlC(rec game.Record) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		p, err := platform.OpenProc(rec.PID)
		if err != nil {
			return fmt.Errorf("the Windrose server (process %d) isn't running: %w", rec.PID, err)
		}
		defer p.Close()
		// Never ask another program that has since been given the same
		// process number to quit (adoption checks the start time the same
		// way, within 2 seconds).
		if !rec.Started.IsZero() {
			if d := p.StartTime().Sub(rec.Started); d > 2*time.Second || d < -2*time.Second {
				return fmt.Errorf("process %d is no longer the Windrose server (it started at %s, the server at %s)",
					rec.PID, p.StartTime().Format(time.RFC3339), rec.Started.Format(time.RFC3339))
			}
		}
		return askToStop(ctx, p)
	}
}

// askToStop sends Ctrl+C, or on Windows closes the window when Ctrl+C
// can't reach the server.
func askToStop(ctx context.Context, p *platform.Proc) error {
	err := p.Interrupt()
	if err == nil || runtime.GOOS != "windows" {
		return err
	}
	if cerr := closeWindow(ctx, p.PID()); cerr != nil {
		return fmt.Errorf("Ctrl+C couldn't reach the Windrose server (%v), and asking its window to close failed too: %w", err, cerr)
	}
	return nil
}

// closeWindow runs "taskkill /pid <pid>" (never /F, which would end the
// server without saving), hidden: without /F it asks the program's
// windows to close, as closing the server's log window by hand does.
func closeWindow(ctx context.Context, pid int) error {
	exe := "taskkill.exe"
	if root := os.Getenv("SystemRoot"); root != "" {
		exe = filepath.Join(root, "System32", "taskkill.exe")
	}
	cmd := exec.CommandContext(ctx, exe, "/pid", strconv.Itoa(pid))
	cmd.SysProcAttr = platform.HiddenProcAttr()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("taskkill /pid %d: %v: %s", pid, err, strings.TrimSpace(string(out)))
	}
	return nil
}
