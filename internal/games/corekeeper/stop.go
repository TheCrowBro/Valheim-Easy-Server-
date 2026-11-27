package corekeeper

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

// Stop asks the server to save the world and quit, the way it understands
// on this system, built from the run record alone so it works after a
// takeover too (spec 2.7):
//
//   - Windows: CoreKeeperServer.exe is a windowed ("GUI") program, so
//     Ctrl+C can't reach it (FACTS.md: PE subsystem 2). Pugstorm's own
//     launcher (Launch.ps1) stops it, when the owner presses q, with
//     "taskkill /pid <pid>": without /F that asks the program's window to
//     close, which the server takes as a quit request. The app does the
//     same, as a "call" step.
//   - Linux (only the real-server check runs it there): Ctrl+C (SIGINT),
//     which the real server takes as the same quit request.
//   - The stand-in (fake mode) is a console program on every system, with
//     no window to close, so it gets Ctrl+C everywhere, Windows included.
//
// Either way the server saves before it quits ("Quit blocked" while it
// saves); on a test computer it exited 3 to 4 seconds after the request.
// The request is sent again every minute in case one is missed (Unity's
// issue tracker has a batch-mode player, on Vulkan, that ignored the first
// taskkill); the server ignores a second one while it saves.
func (s *server) Stop(rec game.Record) []game.StopStep {
	if runtime.GOOS == "windows" && s.env.Fake == nil {
		return []game.StopStep{{Kind: "call", Call: closeWindow(rec), Wait: 3 * time.Minute, Resend: time.Minute}}
	}
	return []game.StopStep{{Kind: "interrupt", Wait: 3 * time.Minute, Resend: time.Minute}}
}

// closeWindow runs "taskkill /pid <pid>" (never /F, which would end the
// server without saving), hidden, as Pugstorm's Launch.ps1 does.
func closeWindow(rec game.Record) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		p, err := platform.OpenProc(rec.PID)
		if err != nil {
			return fmt.Errorf("the Core Keeper server (process %d) isn't running: %w", rec.PID, err)
		}
		started := p.StartTime()
		p.Close()
		// Never ask another program that has since been given the same
		// process number to quit (adoption checks the start time the same
		// way, within 2 seconds).
		if !rec.Started.IsZero() {
			if d := started.Sub(rec.Started); d > 2*time.Second || d < -2*time.Second {
				return fmt.Errorf("process %d is no longer the Core Keeper server (it started at %s, the server at %s)",
					rec.PID, started.Format(time.RFC3339), rec.Started.Format(time.RFC3339))
			}
		}
		exe := "taskkill.exe"
		if root := os.Getenv("SystemRoot"); root != "" {
			exe = filepath.Join(root, "System32", "taskkill.exe")
		}
		cmd := exec.CommandContext(ctx, exe, "/pid", strconv.Itoa(rec.PID))
		cmd.SysProcAttr = platform.HiddenProcAttr()
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("asking the Core Keeper server's window to close (taskkill /pid %d): %v: %s", rec.PID, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
}
