package gametest

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"valheim-easy-server/internal/platform"
)

// TestLaunchWaitsForBusyProgram: a program still open for writing (as it
// is, for a moment, in a process another goroutine just started while the
// program was being copied) can't be started on Linux ("text file busy");
// launch tries again until it can.
func TestLaunchWaitsForBusyProgram(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("only Linux refuses to start a program that is open for writing")
	}
	b, err := os.ReadFile(BuildFakeserver(t))
	if err != nil {
		t.Fatal(err)
	}
	// A name no stand-in personality knows, so it starts and exits at once.
	exe := filepath.Join(t.TempDir(), "busyprogram")
	if err := os.WriteFile(exe, b, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(exe, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(150*time.Millisecond, func() { f.Close() })
	p, err := launch(platform.StartOptions{Exe: exe, Dir: filepath.Dir(exe), Env: os.Environ()})
	if err != nil {
		t.Fatalf("launch while the program was open for writing for 150 ms: %v", err)
	}
	p.Wait()
	p.Close()
}
